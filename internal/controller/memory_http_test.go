package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

func TestMemoryS3SignatureBoundary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	key, secret := "access-key", "private-key"
	signed := func() *http.Request {
		req := httptest.NewRequest("PUT", "https://storage/memory/s3/library/files/file.txt", strings.NewReader("bytes"))
		sum := sha256.Sum256([]byte("bytes"))
		hash := hex.EncodeToString(sum[:])
		req.Header.Set("X-Amz-Content-Sha256", hash)
		if err := v4.NewSigner().SignHTTP(context.Background(), aws.Credentials{AccessKeyID: key, SecretAccessKey: secret}, req, hash, "s3", "us-east-1", now, func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true }); err != nil {
			t.Fatal(err)
		}
		return req
	}
	if !verifyS3(signed(), key, secret, now) {
		t.Fatal("valid signature rejected")
	}
	for name, mutate := range map[string]func(*http.Request){
		"path":      func(r *http.Request) { r.URL.Path = "/memory/s3/library/other/file.txt" },
		"method":    func(r *http.Request) { r.Method = "DELETE" },
		"query":     func(r *http.Request) { r.URL.RawQuery = "uploads" },
		"host":      func(r *http.Request) { r.Host = "other" },
		"hash":      func(r *http.Request) { r.Header.Set("X-Amz-Content-Sha256", strings.Repeat("0", 64)) },
		"signature": func(r *http.Request) { r.Header.Set("Authorization", r.Header.Get("Authorization")+"0") },
		"date":      func(r *http.Request) { r.Header.Set("X-Amz-Date", now.Add(time.Minute).Format("20060102T150405Z")) },
	} {
		t.Run(name, func(t *testing.T) {
			req := signed()
			mutate(req)
			if verifyS3(req, key, secret, now) {
				t.Fatal("tampered request admitted")
			}
		})
	}
	if verifyS3(signed(), key, secret, now.Add(6*time.Minute)) || verifyS3(signed(), key, "wrong-secret", now) {
		t.Fatal("expired or forged request admitted")
	}
}
