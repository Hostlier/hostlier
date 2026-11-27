package controller

import (
	"context"
	"crypto/subtle"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"hostlier/internal/memory"
)

// serveMemory never accepts management credentials, local-listener privilege,
// caller paths, or a credential scoped to another export.
func (c *Controller) serveMemory(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/memory/") {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/memory/"), "/", 4)
	if len(parts) < 3 || (parts[0] != "webdav" && parts[0] != "s3") {
		http.NotFound(w, r)
		return true
	}
	protocol, app, name := parts[0], parts[1], parts[2]
	runtime, ok := c.opts.Runtime.(*memory.Runtime)
	if !ok {
		http.NotFound(w, r)
		return true
	}
	key, password, basic := r.BasicAuth()
	if protocol == "s3" {
		key = s3AccessKey(r)
		basic = false
	}
	c.mu.Lock()
	g := c.state.MemoryGrants[key]
	valid := g != nil && !g.Revoked && g.App == app && g.Export == name && g.ExpiresAt > c.opts.Now().UnixMilli()
	a := c.state.Applications[app]
	valid = valid && a != nil && a.Status == "running" && a.Revision == g.Revision
	var secret string
	var readOnly bool
	var revision string
	var expires int64
	if valid {
		var err error
		secret, err = c.store.Decrypt(g.Secret)
		valid = err == nil
		readOnly, revision, expires = g.ReadOnly, g.Revision, g.ExpiresAt
	}
	now := c.opts.Now()
	c.mu.Unlock()
	if valid {
		if protocol == "webdav" {
			valid = basic && subtle.ConstantTimeCompare([]byte(password), []byte(secret)) == 1
		} else {
			valid = verifyS3(r, key, secret, now)
		}
	}
	if !valid {
		if protocol == "webdav" {
			w.Header().Set("WWW-Authenticate", `Basic realm="Hostlier memory", charset="UTF-8"`)
		}
		http.Error(w, "Storage credential required", http.StatusUnauthorized)
		return true
	}
	ctx, cancel := context.WithDeadline(r.Context(), time.UnixMilli(expires))
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
	runtime.Serve(w, r, app, name, protocol, readOnly, revision)
	return true
}

func s3AccessKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		return ""
	}
	for _, field := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ",") {
		if strings.HasPrefix(strings.TrimSpace(field), "Credential=") {
			value := strings.TrimPrefix(strings.TrimSpace(field), "Credential=")
			return strings.Split(value, "/")[0]
		}
	}
	return ""
}

// Reconstruct the signed request using the SDK signer, retaining precisely the
// signed header set. Proxy-added forwarding headers cannot alter the signature.
func verifyS3(r *http.Request, key, secret string, now time.Time) bool {
	auth := r.Header.Get("Authorization")
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || fields[k] != "" {
			return false
		}
		fields[k] = v
	}
	scope := strings.Split(fields["Credential"], "/")
	if len(scope) != 5 || scope[0] != key || scope[2] != "us-east-1" || scope[3] != "s3" || scope[4] != "aws4_request" {
		return false
	}
	t, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil || now.Sub(t) > 5*time.Minute || t.Sub(now) > 5*time.Minute || scope[1] != t.Format("20060102") {
		return false
	}
	signed := strings.Split(fields["SignedHeaders"], ";")
	if !slices.Contains(signed, "host") || !slices.Contains(signed, "x-amz-date") {
		return false
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload != "UNSIGNED-PAYLOAD" && len(payload) != 64 {
		return false
	}
	copy := r.Clone(r.Context())
	copy.Header = make(http.Header)
	for _, header := range signed {
		if header == "host" || header == "content-length" {
			continue
		}
		values, ok := r.Header[http.CanonicalHeaderKey(header)]
		if !ok {
			return false
		}
		copy.Header[http.CanonicalHeaderKey(header)] = append([]string{}, values...)
	}
	if !slices.Contains(signed, "content-length") {
		copy.ContentLength = 0
	}
	err = v4.NewSigner().SignHTTP(r.Context(), aws.Credentials{AccessKeyID: key, SecretAccessKey: secret}, copy, payload, "s3", "us-east-1", t, func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	return err == nil && subtle.ConstantTimeCompare([]byte(copy.Header.Get("Authorization")), []byte(auth)) == 1
}
