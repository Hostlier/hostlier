//go:build linux || darwin

package memory

import (
	"context"
	"encoding/xml"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/webdav"
)

func TestMemoryNativeDAVLocksAndEditorSave(t *testing.T) {
	root, source, _ := memoryFixture(t)
	prefix := "/memory/webdav/native/files"
	locks := newNativeLocks()
	request := func(method, key, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://storage"+prefix+(&url.URL{Path: "/" + key}).EscapedPath(), strings.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		serveNativeDAV(w, req, root, locks, prefix)
		return w
	}
	lockBody := "<D:lockinfo xmlns:D=\"DAV:\"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype><D:owner>native client</D:owner></D:lockinfo>"
	key := "résumé + notes.txt"
	w := request("LOCK", key, lockBody, map[string]string{"Depth": "0", "Timeout": "Second-600"})
	if w.Code != 201 {
		t.Fatalf("LOCK: %d %s", w.Code, w.Body.String())
	}
	token := strings.Trim(w.Header().Get("Lock-Token"), "<>")
	uri, err := url.Parse(token)
	if err != nil || uri.Scheme == "" {
		t.Fatalf("lock token is not a URI: %q", token)
	}
	var props struct {
		Discovery struct {
			Active struct {
				Root struct {
					Href string `xml:"href"`
				} `xml:"lockroot"`
			} `xml:"activelock"`
		} `xml:"lockdiscovery"`
	}
	if err = xml.Unmarshal(w.Body.Bytes(), &props); err != nil {
		t.Fatal(err)
	}
	expected := (&url.URL{Path: prefix + "/" + key}).EscapedPath()
	if props.Discovery.Active.Root.Href != expected {
		t.Fatalf("lockroot %q expected %q", props.Discovery.Active.Root.Href, expected)
	}
	if w = request("PUT", key, "saved", map[string]string{"If": "(<" + token + ">)"}); w.Code != 201 {
		t.Fatalf("locked PUT %d %s", w.Code, w.Body.String())
	}
	if w = request("LOCK", key, "", map[string]string{"If": "(<" + token + ">)", "Timeout": "Second-600"}); w.Code != 200 {
		t.Fatalf("refresh: %d %s", w.Code, w.Body.String())
	}
	if w = request("UNLOCK", key, "", map[string]string{"Lock-Token": "<" + token + ">"}); w.Code != 204 {
		t.Fatal(w.Code)
	}
	os.WriteFile(filepath.Join(source, "save.tmp"), []byte("new content"), 0600)
	w = request("MOVE", "save.tmp", "", map[string]string{"Destination": "http://storage" + expected})
	if w.Code != 204 {
		t.Fatalf("implicit overwrite: %d %s", w.Code, w.Body.String())
	}
	data, _ := os.ReadFile(filepath.Join(source, key))
	if string(data) != "new content" {
		t.Fatal("editor replacement failed")
	}
	os.WriteFile(filepath.Join(source, "save.tmp"), []byte("must not replace"), 0600)
	if w = request("MOVE", "save.tmp", "", map[string]string{"Destination": "http://storage" + expected, "Overwrite": "F"}); w.Code != 412 {
		t.Fatal("explicit no-overwrite ignored")
	}
	data, _ = os.ReadFile(filepath.Join(source, key))
	if string(data) != "new content" {
		t.Fatal("failed move lost destination")
	}
	if w = request("MOVE", "missing", "", map[string]string{"Destination": "http://storage" + expected}); w.Code < 400 {
		t.Fatal("missing source moved")
	}
	data, _ = os.ReadFile(filepath.Join(source, key))
	if string(data) != "new content" {
		t.Fatal("missing source deleted destination")
	}
	os.Mkdir(filepath.Join(source, "parent"), 0700)
	os.WriteFile(filepath.Join(source, "parent", "child"), []byte("keep"), 0600)
	if w = request("MOVE", "parent/child", "", map[string]string{"Destination": "http://storage" + prefix + "/parent"}); w.Code != 403 {
		t.Fatal("ancestor move accepted")
	}
	if _, err = os.Stat(filepath.Join(source, "parent", "child")); err != nil {
		t.Fatal("ancestor move destroyed source")
	}
}
func TestMemoryNativeLockScopes(t *testing.T) {
	a, b := newNativeLocks(), newNativeLocks()
	now := time.Now()
	token, err := a.Create(now, webdav.LockDetails{Root: "/file", Duration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Confirm(now, "/file", "", webdav.Condition{Token: token}); err == nil {
		t.Fatal("token accepted across exports")
	}
	if _, err = a.Confirm(now, "/file", "", webdav.Condition{Token: strings.TrimPrefix(token, "opaquelocktoken:")}); err == nil {
		t.Fatal("invalid token accepted")
	}
	root, source, _ := memoryFixture(t)
	os.WriteFile(filepath.Join(source, "target"), []byte("keep"), 0600)
	adapter := nativeMoveFS{Root: root, source: "missing"}
	if err = adapter.RemoveAll(context.Background(), "target"); err != nil {
		t.Fatal(err)
	}
	if err = adapter.Rename(context.Background(), "missing", "target"); err == nil {
		t.Fatal("missing source renamed")
	}
	data, _ := os.ReadFile(filepath.Join(source, "target"))
	if string(data) != "keep" {
		t.Fatal("failed rename removed destination")
	}
}
