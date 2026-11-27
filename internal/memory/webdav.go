package memory

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/net/webdav"
)

// The upstream in-memory lock store uses bare integers as tokens. Native
// clients require absolute-URI lock tokens (RFC 4918 section 6.5).
type uriLocks struct {
	webdav.LockSystem
	prefix string
}

func newNativeLocks() webdav.LockSystem {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic(err)
	}
	return &uriLocks{webdav.NewMemLS(), "opaquelocktoken:" + hex.EncodeToString(nonce[:]) + "-"}
}
func (l *uriLocks) Create(now time.Time, d webdav.LockDetails) (string, error) {
	token, err := l.LockSystem.Create(now, d)
	if err != nil {
		return "", err
	}
	return l.prefix + token, nil
}
func (l *uriLocks) inner(token string) string {
	if !strings.HasPrefix(token, l.prefix) {
		return ""
	}
	return strings.TrimPrefix(token, l.prefix)
}
func (l *uriLocks) Unlock(now time.Time, token string) error {
	return l.LockSystem.Unlock(now, l.inner(token))
}
func (l *uriLocks) Refresh(now time.Time, token string, d time.Duration) (webdav.LockDetails, error) {
	return l.LockSystem.Refresh(now, l.inner(token), d)
}
func (l *uriLocks) Confirm(now time.Time, a, b string, conditions ...webdav.Condition) (func(), error) {
	copy := append([]webdav.Condition(nil), conditions...)
	for i := range copy {
		copy[i].Token = l.inner(copy[i].Token)
	}
	return l.LockSystem.Confirm(now, a, b, copy...)
}

// Avoid unlinking a regular destination before rename: editors replace an
// existing file during save and must retain it if the rename fails.
type nativeMoveFS struct {
	*Root
	source string
}

func (f nativeMoveFS) RemoveAll(ctx context.Context, name string) error {
	info, err := f.Root.Stat(ctx, name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	if source, err := f.Root.Stat(ctx, f.source); err == nil && sameFileInfo(source, info) {
		return nil // Case-only or Unicode-equivalent rename on macOS.
	}
	return f.Root.RemoveAll(ctx, name)
}

func sameFileInfo(a, b os.FileInfo) bool {
	if wrapped, ok := a.(interface{ underlyingInfo() os.FileInfo }); ok {
		a = wrapped.underlyingInfo()
	}
	if wrapped, ok := b.(interface{ underlyingInfo() os.FileInfo }); ok {
		b = wrapped.underlyingInfo()
	}
	return os.SameFile(a, b)
}

// Detect relationships by inode as well as spelling. macOS accepts case and
// Unicode aliases; deleting an aliased destination collection before a MOVE or
// COPY could otherwise delete the source itself.
func transferOverlap(ctx context.Context, root *Root, src, dst string) (same, overlap bool, err error) {
	a, err := root.Stat(ctx, src)
	if err != nil {
		return false, false, err
	}
	b, err := root.Stat(ctx, dst)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, false, err
	}
	if b != nil && sameFileInfo(a, b) {
		return true, false, nil
	}
	for _, pair := range []struct {
		endpoint os.FileInfo
		other    string
	}{{a, dst}, {b, src}} {
		if pair.endpoint == nil {
			continue
		}
		for p := path.Dir(pair.other); ; p = path.Dir(p) {
			ancestor, e := root.Stat(ctx, p)
			if e != nil && !errors.Is(e, fs.ErrNotExist) {
				return false, false, e
			}
			if e == nil && sameFileInfo(pair.endpoint, ancestor) {
				return false, true, nil
			}
			if p == path.Dir(p) {
				break
			}
		}
	}
	return false, false, nil
}

type davReply struct {
	header http.Header
	status int
	bytes.Buffer
}

func (w *davReply) Header() http.Header { return w.header }
func (w *davReply) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *davReply) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.Buffer.Write(p)
}

// LOCK responses must identify the externally visible, URL-escaped resource,
// not the internal path relative to Handler.Prefix.
func nativeLockXML(body []byte, prefix string) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	var stack []xml.Name
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch v := token.(type) {
		case xml.StartElement:
			stack = append(stack, v.Name)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) >= 2 && stack[len(stack)-2] == (xml.Name{Space: "DAV:", Local: "lockroot"}) && stack[len(stack)-1] == (xml.Name{Space: "DAV:", Local: "href"}) {
				token = xml.CharData((&url.URL{Path: prefix + string(v)}).EscapedPath())
			}
		}
		if err = enc.EncodeToken(token); err != nil {
			return nil, err
		}
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func serveNativeDAV(w http.ResponseWriter, r *http.Request, root *Root, locks webdav.LockSystem, prefix string) {
	fs := webdav.FileSystem(root)
	if r.Method == "MOVE" || r.Method == "COPY" {
		destination, err := url.Parse(r.Header.Get("Destination"))
		if err != nil {
			http.Error(w, "Invalid destination", http.StatusBadRequest)
			return
		}
		src := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/")
		dst := strings.TrimSuffix(strings.TrimPrefix(destination.Path, prefix), "/")
		// Never let the upstream overwrite-delete remove a source's ancestor or
		// a destination within the source tree before rename can reject it.
		if dst == "" || src == "" || strings.HasPrefix(src, dst+"/") || strings.HasPrefix(dst, src+"/") {
			http.Error(w, "Cannot move a collection into or over its own tree", http.StatusForbidden)
			return
		}
		// Fail before the upstream handler removes any destination collection.
		same, overlap, err := transferOverlap(r.Context(), root, src, dst)
		if err != nil {
			http.Error(w, "Source unavailable", http.StatusNotFound)
			return
		}
		if overlap || same && r.Method == "COPY" {
			http.Error(w, "Source and destination overlap", http.StatusForbidden)
			return
		}
		// RFC 4918 defaults an absent Overwrite header to T, including MOVE.
		r = r.Clone(r.Context())
		if r.Header.Get("Overwrite") == "" {
			r.Header.Set("Overwrite", "T")
		}
		if r.Method == "MOVE" {
			fs = nativeMoveFS{Root: root, source: src}
		}
	}
	h := webdav.Handler{Prefix: prefix, FileSystem: fs, LockSystem: locks}
	if r.Method != "LOCK" {
		h.ServeHTTP(w, r)
		return
	}
	// The upstream serializer represents its infinite duration as Second-0.
	// Select a real finite lease instead; native clients can renew it normally.
	if r.Header.Get("Timeout") == "" || r.Header.Get("Timeout") == "Infinite" {
		r = r.Clone(r.Context())
		r.Header.Set("Timeout", "Second-3600")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	reply := &davReply{header: make(http.Header)}
	h.ServeHTTP(reply, r)
	body := reply.Bytes()
	if reply.status == 200 || reply.status == 201 {
		var err error
		body, err = nativeLockXML(body, prefix)
		if err != nil {
			http.Error(w, "Invalid lock response", 500)
			return
		}
	}
	for k, values := range reply.header {
		w.Header()[k] = values
	}
	w.Header().Del("Content-Length")
	if reply.status == 0 {
		reply.status = 200
	}
	w.WriteHeader(reply.status)
	w.Write(body)
}
