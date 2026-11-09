package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"net/http"
	"strings"
	"time"
)

// Objects abstracts only the immutable operations used by retention and portable recovery.
type Objects interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
	List(context.Context) ([]string, error)
}

// Destination retains private transport and key material outside serialized controller state.
type Destination struct {
	ID, Endpoint, Region, Bucket, Prefix string
	Key                                  []byte
	Objects                              Objects
}

// Fingerprint makes destination or encryption-key changes require a newly approved policy.
func (d *Destination) Fingerprint() string {
	encoded, _ := json.Marshal([]string{d.ID, d.Endpoint, d.Region, d.Bucket, d.Prefix, Digest(d.Key)})
	return Digest(encoded)
}

// S3 uses the maintained AWS signer, explicit credentials and bounded no-redirect transport.
type S3 struct {
	Client         *s3.Client
	Bucket, Prefix string
}

// NewS3 constructs a path-style client suitable for AWS S3 and compatible private services.
func NewS3(endpoint, region, bucket, prefix, access, secret string) *S3 {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	// Keep asynchronous work bound to the controller lifetime.
	client := s3.New(s3.Options{Region: region, BaseEndpoint: aws.String(endpoint), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider(access, secret, ""), RetryMaxAttempts: 1, HTTPClient: &http.Client{Transport: transport, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	return &S3{Client: client, Bucket: bucket, Prefix: strings.Trim(prefix, "/") + "/"}
}

// key confines every read and deletion to the configured object prefix.
func (s *S3) key(key string) (string, error) {
	// Object identities are opaque lowercase hexadecimal IDs, not caller-supplied paths.
	if len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" {
		return "", errors.New("invalid recovery object identity")
	}
	return s.Prefix + key + ".hbk", nil
}

// Put creates one immutable object and never overwrites an existing recovery point.
func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	path, err := s.key(key)
	// Invalid identities cannot reach remote storage.
	if err != nil {
		return err
	}
	_, err = s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(path), Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))), IfNoneMatch: aws.String("*"), ContentType: aws.String("application/octet-stream")})
	return err
}

// Get refuses oversized or truncated remote objects before decrypting them.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	path, err := s.key(key)
	// Invalid identities cannot reach remote storage.
	if err != nil {
		return nil, err
	}
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(path)})
	// Missing objects remain unavailable instead of becoming empty recovery points.
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, MaxObject+1))
	// Bound actual bytes, not just caller-controlled Content-Length.
	if err != nil || len(data) > MaxObject {
		return nil, errors.New("backup object unreadable or too large")
	}
	return data, nil
}

// Delete removes only the exact retained identity selected by a verified retention decision.
func (s *S3) Delete(ctx context.Context, key string) error {
	path, err := s.key(key)
	// Invalid identities cannot reach remote storage.
	if err != nil {
		return err
	}
	_, err = s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(path)})
	return err
}

// List discovers bounded recovery object identities after loss of local controller metadata.
func (s *S3) List(ctx context.Context) ([]string, error) {
	pager := s3.NewListObjectsV2Paginator(s.Client, &s3.ListObjectsV2Input{Bucket: aws.String(s.Bucket), Prefix: aws.String(s.Prefix), MaxKeys: aws.Int32(1000)})
	keys := []string{}
	pages := 0
	// Pagination is bounded to keep an untrusted bucket from exhausting controller memory.
	for pager.HasMorePages() {
		pages++
		// Advance only after the required recovery invariant holds.
		if pages > 10 {
			return nil, errors.New("recovery listing limit exceeded")
		}
		page, err := pager.NextPage(ctx)
		// An incomplete listing cannot be reported as complete.
		if err != nil {
			return nil, err
		}
		// Ignore objects outside Hostlier's exact immutable identity format.
		for _, item := range page.Contents {
			key := strings.TrimSuffix(strings.TrimPrefix(aws.ToString(item.Key), s.Prefix), ".hbk")
			// Propagate failure before performing the next recovery side effect.
			if path, err := s.key(key); err == nil && path == aws.ToString(item.Key) {
				keys = append(keys, key)
			}
		}
		// Large inventories require operator partitioning into independent prefixes.
		if len(keys) > 10000 {
			return nil, errors.New("recovery listing limit exceeded")
		}
	}
	return keys, nil
}
