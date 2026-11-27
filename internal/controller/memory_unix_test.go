//go:build linux || darwin

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"hostlier/internal/memory"
	"hostlier/internal/model"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryControllerDeploymentGrantsAndRecovery(t *testing.T) {
	base, pathErr := filepath.EvalSymlinks(t.TempDir())
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	source := filepath.Join(base, "files")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "document.txt"), []byte("original"), 0600)
	state := filepath.Join(base, "state")
	rt, err := memory.New([]string{source}, state)
	if err != nil {
		t.Fatal(err)
	}
	c := newTestController(t, state, rt, nil)
	defer func() { c.Close(context.Background()); rt.Close() }()
	spec := model.Project{Version: 3, Kind: "memory", Name: "library", Exports: map[string]model.MemoryExport{"files": {Path: source, Protocols: []string{"webdav", "s3"}}}}
	deploy := func() {
		t.Helper()
		status, plan := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
		if status != 200 {
			t.Fatalf("plan %d %v", status, plan)
		}
		status, accepted := call(t, c, true, "POST", "/v1/plans/"+plan["id"].(string)+"/apply", map[string]any{"approved": true, "fingerprint": plan["fingerprint"], "requestId": random()}, "")
		if status != 200 && status != 202 {
			t.Fatalf("apply %d %v", status, accepted)
		}
		result := wait(t, c, accepted["operationId"].(string))
		if result["status"] != "succeeded" {
			t.Fatalf("operation %v", result)
		}
	}
	deploy()
	status, denied := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": project()}, "")
	if status == 200 || code(denied) != "CONTROLLER_MODE" {
		t.Fatalf("compute admitted %d %v", status, denied)
	}
	for _, path := range []string{"/v1/resources", "/v1/packages", "/v1/hosts", "/v1/artifacts", "/v1/recovery-jobs"} {
		status, _ = call(t, c, true, "GET", path, nil, "")
		if status == 200 {
			t.Fatal("compute capability exposed", path)
		}
	}
	grant := func(write bool) map[string]any {
		t.Helper()
		status, g := call(t, c, true, "POST", "/v1/memory/library/grants", map[string]any{"export": "files", "readOnly": !write, "durationSeconds": 3600}, "")
		if status != 200 {
			t.Fatalf("grant %d %v", status, g)
		}
		return g
	}
	g := grant(false)
	request := func(method, path, body string, g map[string]any) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://storage"+path, strings.NewReader(body))
		if g != nil {
			req.SetBasicAuth(g["username"].(string), g["password"].(string))
		}
		w := httptest.NewRecorder()
		c.Handler().ServeHTTP(w, req)
		return w
	}
	path := "/memory/webdav/library/files/document.txt"
	if w := request("GET", path, "", nil); w.Code != 401 {
		t.Fatalf("anonymous %d", w.Code)
	}
	if w := request("GET", path, "", g); w.Code != 200 || w.Body.String() != "original" {
		t.Fatalf("read %d %s", w.Code, w.Body.String())
	}
	if w := request("PUT", path, "bad", g); w.Code != 403 {
		t.Fatalf("readonly %d", w.Code)
	}
	if w := request("GET", "/memory/webdav/library/other/document.txt", "", g); w.Code != 401 {
		t.Fatalf("cross export %d", w.Code)
	}
	_, listed := call(t, c, true, "GET", "/v1/memory/library/grants", nil, "")
	if strings.Contains(fmt.Sprint(listed), g["password"].(string)) {
		t.Fatal("secret leaked in listing")
	}
	if w := request("GET", "/v1/apps", "", g); w.Code != 401 {
		t.Fatalf("storage became management: %d", w.Code)
	}
	writable := grant(true)
	server := httptest.NewServer(c.Handler())
	defer server.Close()
	sdk := s3.New(s3.Options{BaseEndpoint: aws.String(server.URL + "/memory/s3/library"), Region: "us-east-1", UsePathStyle: true,
		Credentials:                credentials.NewStaticCredentialsProvider(writable["accessKeyId"].(string), writable["secretAccessKey"].(string), ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	sdkKey := "documents/a + résumé.txt"
	if _, err := sdk.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String("files"), Key: aws.String(sdkKey), Body: strings.NewReader("SDK bytes")}); err != nil {
		t.Fatal(err)
	}
	object, err := sdk.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String("files"), Key: aws.String(sdkKey)})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(object.Body)
	object.Body.Close()
	if err != nil || string(contents) != "SDK bytes" {
		t.Fatalf("SDK object: %s %v", contents, err)
	}
	listing, err := sdk.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String("files"), Prefix: aws.String("documents/")})
	if err != nil || len(listing.Contents) != 1 || aws.ToString(listing.Contents[0].Key) != sdkKey {
		t.Fatalf("SDK listing: %v %v", listing, err)
	}
	if _, err := sdk.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String("files"), Key: aws.String(sdkKey)}); err != nil {
		t.Fatal(err)
	}
	signed := func(method, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://storage/memory/s3/library/files/"+key, strings.NewReader(body))
		sum := sha256.Sum256([]byte(body))
		hash := hex.EncodeToString(sum[:])
		req.Header.Set("X-Amz-Content-Sha256", hash)
		err := v4.NewSigner().SignHTTP(context.Background(), aws.Credentials{AccessKeyID: writable["accessKeyId"].(string), SecretAccessKey: writable["secretAccessKey"].(string)}, req, hash, "s3", "us-east-1", time.Now(), func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		c.Handler().ServeHTTP(w, req)
		return w
	}
	if w := signed("PUT", "agent.txt", "from agent"); w.Code != 200 {
		t.Fatalf("signed PUT %d %s", w.Code, w.Body.String())
	}
	if w := signed("GET", "agent.txt", ""); w.Code != 200 || w.Body.String() != "from agent" {
		t.Fatalf("signed GET %d %s", w.Code, w.Body.String())
	}
	if w := signed("GET", "?list-type=2", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "agent.txt") {
		t.Fatalf("signed list %d %s", w.Code, w.Body.String())
	}
	c.Close(context.Background())
	rt.Close()
	rt, err = memory.New([]string{source}, state)
	if err != nil {
		t.Fatal(err)
	}
	c = newTestController(t, state, rt, nil)
	if w := request("GET", path, "", g); w.Code != 200 {
		t.Fatalf("restart %d %s", w.Code, w.Body.String())
	}
	_, revoked := call(t, c, true, "DELETE", "/v1/memory/library/grants/"+g["username"].(string), nil, "")
	if revoked["revoked"] != true {
		t.Fatal(revoked)
	}
	if w := request("GET", path, "", g); w.Code != 401 {
		t.Fatal("revoked grant worked")
	}
	deploy()
	if w := request("GET", path, "", writable); w.Code != 401 {
		t.Fatal("old revision grant worked")
	}
	fresh := grant(true)
	c.mu.Lock()
	c.state.MemoryGrants[fresh["username"].(string)].ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	c.mu.Unlock()
	if w := request("GET", path, "", fresh); w.Code != 401 {
		t.Fatal("expired grant worked")
	}
	_, accepted := call(t, c, true, "POST", "/v1/apps/library/action", map[string]any{"action": "delete"}, "")
	result := wait(t, c, accepted["operationId"].(string))
	if result["status"] != "succeeded" {
		t.Fatal(result)
	}
	data, err := os.ReadFile(filepath.Join(source, "document.txt"))
	if err != nil || string(data) != "original" {
		t.Fatal("deletion changed existing files")
	}
}

func TestMemoryProjectRejectedByComputeController(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, nil)
	defer closeController(t, c)
	spec := model.Project{Version: 3, Kind: "memory", Name: "library", Exports: map[string]model.MemoryExport{"files": {Path: "/existing/on/controller", Protocols: []string{"webdav"}}}}
	status, out := call(t, c, true, "POST", "/v1/plans", map[string]any{"spec": spec}, "")
	if status == 200 || code(out) != "CONTROLLER_MODE" {
		t.Fatalf("compute controller accepted memory project: %d %v", status, out)
	}
}
