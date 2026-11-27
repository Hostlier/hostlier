//go:build linux || darwin

package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hostlier/internal/model"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryProtocolsAndLifecycle(t *testing.T) {
	base := memoryTempDir(t)
	source := filepath.Join(base, "files")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "original.txt"), []byte("existing bytes"), 0600)
	r, err := New([]string{source}, filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p := model.Project{Version: 3, Kind: "memory", Name: "library", Exports: map[string]model.MemoryExport{"files": {Path: source, Protocols: []string{"webdav", "s3"}}}}
	ids, err := r.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Install(p, ids, "rev1"); err != nil {
		t.Fatal(err)
	}
	request := func(protocol, method, key, body string, headers map[string]string, readOnly bool, revision string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://storage/memory/"+protocol+"/library/files/"+key, strings.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if protocol == "s3" {
			h := sha256.Sum256([]byte(body))
			req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(h[:]))
		}
		w := httptest.NewRecorder()
		r.Serve(w, req, "library", "files", protocol, readOnly, revision)
		return w
	}
	check := func(protocol, method, key, body string, headers map[string]string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := request(protocol, method, key, body, headers, false, "rev1")
		if w.Code != status {
			t.Fatalf("%s %s %s: %d %s", protocol, method, key, w.Code, w.Body.String())
		}
		return w
	}
	w := check("webdav", "PROPFIND", "", "", map[string]string{"Depth": "1"}, 207)
	if !strings.Contains(w.Body.String(), "original.txt") {
		t.Fatal("existing file absent")
	}
	check("webdav", "PUT", "new.txt", "native bytes", nil, 201)
	if w = check("s3", "GET", "new.txt", "", nil, 200); w.Body.String() != "native bytes" {
		t.Fatal(w.Body.String())
	}
	check("s3", "PUT", "sub/image.png", "binary\x00\xffbytes", nil, 200)
	if w = check("webdav", "GET", "sub/image.png", "", nil, 200); w.Body.String() != "binary\x00\xffbytes" {
		t.Fatal("binary content changed across protocols")
	}
	w = check("s3", "GET", "?list-type=2&prefix=sub/", "", nil, 200)
	if !strings.Contains(w.Body.String(), "sub/image.png") {
		t.Fatal(w.Body.String())
	}
	check("webdav", "MOVE", "new.txt", "", map[string]string{"Destination": "http://storage/memory/webdav/library/files/renamed.txt"}, 201)
	check("webdav", "COPY", "renamed.txt", "", map[string]string{"Destination": "http://storage/memory/webdav/library/other/copied.txt"}, 403)
	check("webdav", "GET", "sub/image.png", "", map[string]string{"Range": "bytes=0-5"}, 206)
	badUpload := httptest.NewRequest("PUT", "http://storage/memory/s3/library/files/original.txt", strings.NewReader("corrupt"))
	badUpload.Header.Set("X-Amz-Content-Sha256", strings.Repeat("0", 64))
	badResponse := httptest.NewRecorder()
	r.Serve(badResponse, badUpload, "library", "files", "s3", false, "rev1")
	if badResponse.Code != 400 {
		t.Fatal("corrupt upload admitted")
	}
	check("s3", "GET", "original.txt?versionId=unknown", "", nil, 501)
	check("s3", "PUT", "original.txt", "overwrite", map[string]string{"If-Match": "unimplemented"}, 501)
	check("webdav", "MKCOL", "folder", "", nil, 201)
	check("webdav", "DELETE", "folder", "", nil, 204)
	// Lock semantics are required by native writable WebDAV clients.
	lockBody := "<D:lockinfo xmlns:D=\"DAV:\"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype><D:owner>test</D:owner></D:lockinfo>"
	w = check("webdav", "LOCK", "renamed.txt", lockBody, map[string]string{"Timeout": "Second-60"}, 200)
	token := w.Header().Get("Lock-Token")
	if token == "" {
		t.Fatal("missing lock token")
	}
	check("webdav", "PUT", "renamed.txt", "locked", nil, 423)
	check("s3", "PUT", "renamed.txt", "locked", nil, 409)
	check("webdav", "UNLOCK", "renamed.txt", "", map[string]string{"Lock-Token": token}, 204)
	for _, protocol := range []string{"webdav", "s3"} {
		if w = request(protocol, "PUT", "original.txt", "bad", nil, true, "rev1"); w.Code != 403 {
			t.Fatal("read-only bypass")
		}
		if w = request(protocol, "GET", "original.txt", "", nil, false, "old"); w.Code != 503 {
			t.Fatal("revision bypass")
		}
	}
	if err = r.Action(context.Background(), "library", "stop", model.ActionContext{}); err != nil {
		t.Fatal(err)
	}
	check("webdav", "GET", "original.txt", "", nil, 503)
	if err = r.Action(context.Background(), "library", "start", model.ActionContext{}); err != nil {
		t.Fatal(err)
	}
	check("webdav", "GET", "original.txt", "", nil, 200)
	if err = r.Action(context.Background(), "library", "delete", model.ActionContext{Purge: true}); err == nil {
		t.Fatal("purge allowed")
	}
	if err = r.Action(context.Background(), "library", "delete", model.ActionContext{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(source, "original.txt"))
	if string(data) != "existing bytes" {
		t.Fatal("data lost")
	}
}
func TestMemorySourceReviewAndPolicy(t *testing.T) {
	base := memoryTempDir(t)
	allowed := filepath.Join(base, "allowed")
	os.Mkdir(allowed, 0700)
	source := filepath.Join(allowed, "files")
	os.Mkdir(source, 0700)
	r, err := New([]string{allowed}, filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p := model.Project{Version: 3, Kind: "memory", Name: "library", Exports: map[string]model.MemoryExport{"files": {Path: source, Protocols: []string{"webdav"}}}}
	ids, err := r.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	os.Rename(source, source+"-old")
	os.Mkdir(source, 0700)
	if _, err = r.Install(p, ids, "v1"); err == nil {
		t.Fatal("reviewed identity substitution")
	}
	for _, bad := range []string{base, filepath.Join(base, "state"), "/", filepath.Join(allowed, "missing")} {
		p.Exports["files"] = model.MemoryExport{Path: bad, Protocols: []string{"webdav"}}
		if _, err = r.Prepare(p); err == nil {
			t.Fatal(fmt.Sprintf("admitted %s", bad))
		}
	}
}
