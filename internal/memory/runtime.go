// Package memory serves approved existing resources without a container engine.
package memory

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/webdav"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

type export struct {
	root  *Root
	spec  model.MemoryExport
	locks webdav.LockSystem
}
type deployment struct {
	spec     model.Project
	ids      map[string]model.MemoryIdentity
	exports  map[string]*export
	stopped  bool
	revision string
}
type Runtime struct {
	mu       sync.RWMutex
	allowed  []string
	stateDir string
	apps     map[string]*deployment
}

func New(allowed []string, stateDir string) (*Runtime, error) {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return nil, model.Err("MEMORY_STATE", "Controller state requires a canonical absolute path")
	}
	// The controller may create state later, but existing ancestors must never
	// redirect its private state into an otherwise permitted export.
	for p := stateDir; p != "/"; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return nil, model.Err("MEMORY_STATE", "Controller state must not traverse links or files")
		}
	}
	if len(allowed) == 0 {
		return nil, model.Err("MEMORY_ROOTS", "Configure at least one memory root on the controller")
	}
	r := &Runtime{allowed: append([]string{}, allowed...), stateDir: stateDir, apps: map[string]*deployment{}}
	for _, p := range allowed {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return nil, model.Err("MEMORY_ROOTS", "Memory roots must be canonical absolute directories other than /")
		}
		root, err := OpenRoot(p)
		if err != nil {
			return nil, err
		}
		isFile := root.Identity().File
		root.Close()
		if isFile {
			return nil, model.Err("MEMORY_ROOTS", "Memory roots must be directories")
		}
	}
	return r, nil
}
func (r *Runtime) Name() string { return "memory" }
func (r *Runtime) InspectImage(context.Context, string) (model.ImageInfo, error) {
	return model.ImageInfo{}, model.Err("CONTROLLER_MODE", "Memory controllers do not run applications")
}
func (r *Runtime) Deploy(context.Context, model.Project, model.DeployContext) (model.Deployment, error) {
	return model.Deployment{}, model.Err("MEMORY_IDENTITY", "A reviewed storage identity is required")
}
func (r *Runtime) Logs(context.Context, string) (string, error) { return "", nil }
func (r *Runtime) Doctor(context.Context) (any, error) {
	return map[string]any{"runtime": "memory", "storage": true, "protocols": []string{"webdav", "s3"}}, nil
}

func beneath(root, p string) bool {
	rel, e := filepath.Rel(root, p)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
func (r *Runtime) allowedPath(p string) bool {
	if beneath(p, r.stateDir) || beneath(r.stateDir, p) || filesystemOverlap(p, r.stateDir) {
		return false
	}
	return slices.ContainsFunc(r.allowed, func(root string) bool { return beneath(root, p) })
}

// Lexical paths alone cannot protect state on case-insensitive filesystems or
// through macOS firmlink aliases. Compare the endpoints against actual ancestor
// identities too. Missing state ancestors are allowed because New runs before
// the controller creates its private state directory; other errors fail closed.
func filesystemOverlap(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		endpoint, err := os.Stat(pair[0])
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return true
		}
		for p := pair[1]; ; p = filepath.Dir(p) {
			ancestor, err := os.Stat(p)
			if err != nil && !os.IsNotExist(err) {
				return true
			}
			if err == nil && os.SameFile(endpoint, ancestor) {
				return true
			}
			if p == filepath.Dir(p) {
				break
			}
		}
	}
	return false
}
func (r *Runtime) Prepare(p model.Project) (map[string]model.MemoryIdentity, error) {
	if _, e := config.ValidateProject(p); e != nil {
		return nil, e
	}
	if p.Kind != "memory" {
		return nil, model.Err("CONTROLLER_MODE", "Memory controllers only accept kind: memory")
	}
	ids := map[string]model.MemoryIdentity{}
	for name, spec := range p.Exports {
		if !r.allowedPath(spec.Path) {
			return nil, model.Err("MEMORY_ROOTS", "Export is outside allowed roots or overlaps controller state")
		}
		root, e := OpenRoot(spec.Path)
		if e != nil {
			return nil, model.Err("MEMORY_SOURCE", "Export must be an existing accessible regular file or directory without symlinks or hard-linked files")
		}
		ids[name] = root.Identity()
		root.Close()
	}
	return ids, nil
}

// Install checks every identity before replacing any live export.
func (r *Runtime) Install(p model.Project, ids map[string]model.MemoryIdentity, revision string) (model.Deployment, error) {
	actual, e := r.Prepare(p)
	if e != nil {
		return model.Deployment{}, e
	}
	if config.Fingerprint(ids) != config.Fingerprint(actual) {
		return model.Deployment{}, model.Err("MEMORY_SOURCE_CHANGED", "Export identity changed; review a new plan")
	}
	d := &deployment{spec: p, ids: ids, revision: revision, exports: map[string]*export{}}
	for name, spec := range p.Exports {
		root, e := OpenRoot(spec.Path)
		if e != nil || root.Identity() != ids[name] {
			if root != nil {
				root.Close()
			}
			closeExports(d)
			return model.Deployment{}, model.Err("MEMORY_SOURCE_CHANGED", "Export identity changed; review a new plan")
		}
		d.exports[name] = &export{root: root, spec: spec, locks: newNativeLocks()}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prior := r.apps[p.Name]; prior != nil {
		closeExports(prior)
	}
	r.apps[p.Name] = d
	endpoints := map[string]string{}
	for name, spec := range p.Exports {
		for _, protocol := range spec.Protocols {
			endpoints[name+"."+protocol] = "/memory/" + protocol + "/" + p.Name + "/" + name + "/"
		}
	}
	return model.Deployment{Name: p.Name, Status: "running", RuntimeID: "memory:" + p.Name, Endpoints: endpoints}, nil
}
func closeExports(d *deployment) {
	for _, e := range d.exports {
		e.root.Close()
	}
}
func (r *Runtime) Action(_ context.Context, name, action string, ctx model.ActionContext) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.apps[name]
	if d == nil {
		return model.Err("NOT_FOUND", "Memory deployment unavailable; redeploy to review its source")
	}
	if ctx.Purge {
		return model.Err("MEMORY_RETAIN", "Memory deployments never purge host data")
	}
	switch action {
	case "stop":
		d.stopped = true
	case "start", "restart":
		for _, e := range d.exports {
			if e.root.Check() != nil {
				return model.Err("MEMORY_SOURCE_CHANGED", "Export identity changed; review a new plan")
			}
		}
		d.stopped = false
	case "delete":
		closeExports(d)
		delete(r.apps, name)
	default:
		return model.Err("INVALID_ACTION", "Unsupported memory lifecycle action")
	}
	return nil
}
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.apps {
		closeExports(d)
	}
	r.apps = map[string]*deployment{}
	return nil
}

// Serve is entered only after the controller verifies a live export-specific grant.
// The grant cannot select a host path or a different export via request headers.
func (r *Runtime) Serve(w http.ResponseWriter, req *http.Request, app, name, protocol string, readOnly bool, revision string) {
	// Serialize requests across exports, including overlapping exports. A remote
	// rename must not move a directory while another request holds its descriptor.
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := req.Context().Err(); err != nil {
		http.Error(w, "Request expired", http.StatusRequestTimeout)
		return
	}
	d := r.apps[app]
	if d == nil || d.stopped || d.revision != revision {
		http.Error(w, "Export unavailable", http.StatusServiceUnavailable)
		return
	}
	e := d.exports[name]
	if e == nil || !slices.Contains(e.spec.Protocols, protocol) {
		http.NotFound(w, req)
		return
	}
	if e.root.Check() != nil {
		http.Error(w, "Export identity changed", http.StatusServiceUnavailable)
		return
	}
	if (readOnly || e.spec.ReadOnly) && !slices.Contains([]string{"GET", "HEAD", "OPTIONS", "PROPFIND"}, req.Method) {
		http.Error(w, "Read-only export", http.StatusForbidden)
		return
	}
	prefix := fmt.Sprintf("/memory/%s/%s/%s", protocol, app, name)
	// Destination is an independent WebDAV path. Refuse cross-export moves/copies.
	if dst := req.Header.Get("Destination"); dst != "" && !validDestination(req, dst, prefix) {
		http.Error(w, "Destination outside export", http.StatusForbidden)
		return
	}
	if protocol == "webdav" {
		serveNativeDAV(w, req, e.root, e.locks, prefix)
		return
	}
	// S3 writers cannot bypass a lock held by a native WebDAV editor.
	if req.Method == "PUT" || req.Method == "DELETE" {
		key := strings.TrimPrefix(req.URL.Path, prefix)
		now := time.Now()
		token, err := e.locks.Create(now, webdav.LockDetails{Root: key, Duration: -1, ZeroDepth: true})
		if err != nil {
			s3Error(w, http.StatusConflict, "OperationAborted", "Object is locked by a native client")
			return
		}
		defer e.locks.Unlock(now, token)
	}
	serveS3(w, req, e.root, prefix)
}
