package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The initial filesystem-backed S3 surface implements path-style object
// operations, not bucket administration, ACLs or object versioning.
func serveS3(w http.ResponseWriter, r *http.Request, root *Root, prefix string) {
	key := strings.TrimPrefix(r.URL.Path, prefix+"/")
	if r.URL.Path == prefix || r.URL.Path == prefix+"/" {
		key = ""
	}
	for param := range r.URL.Query() {
		allowed := param == "x-id" || (key == "" && r.Method == "GET" && slices.Contains([]string{"location", "list-type", "prefix", "delimiter", "max-keys", "continuation-token", "start-after"}, param))
		if !allowed {
			s3Error(w, 501, "NotImplemented", "Unsupported S3 query operation")
			return
		}
	}
	if r.Method == "PUT" || r.Method == "DELETE" {
		for header := range r.Header {
			h := strings.ToLower(header)
			if slices.Contains([]string{"if-match", "if-none-match", "content-md5", "content-encoding", "x-amz-acl", "x-amz-tagging", "x-amz-storage-class"}, h) || strings.HasPrefix(h, "x-amz-meta-") || strings.HasPrefix(h, "x-amz-checksum-") || strings.HasPrefix(h, "x-amz-server-side-encryption") {
				s3Error(w, 501, "NotImplemented", "Conditional writes, metadata, extra checksums and encoded uploads are not supported")
				return
			}
		}
	}
	if key == "" {
		if r.Method == "HEAD" {
			w.WriteHeader(200)
			return
		}
		if r.Method == "GET" {
			if r.URL.Query().Has("location") {
				w.Header().Set("Content-Type", "application/xml")
				io.WriteString(w, "<LocationConstraint>us-east-1</LocationConstraint>")
				return
			}
			listObjects(w, r, root, path.Base(prefix))
			return
		}
		s3Error(w, 405, "MethodNotAllowed", "Buckets are configured by deployment")
		return
	}
	switch r.Method {
	case "GET", "HEAD":
		f, err := root.OpenFile(r.Context(), key, os.O_RDONLY, 0)
		if err != nil {
			s3FileError(w, err)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			s3Error(w, 404, "NoSuchKey", "Object not found")
			return
		}
		hash := sha256.New()
		if _, err = io.Copy(hash, f); err != nil {
			s3Error(w, 500, "InternalError", "Cannot read object")
			return
		}
		f.Seek(0, io.SeekStart)
		w.Header().Set("ETag", fmt.Sprintf("%q", hex.EncodeToString(hash.Sum(nil))))
		http.ServeContent(w, r, path.Base(key), info.ModTime(), f)
	case "PUT":
		if r.Header.Get("X-Amz-Copy-Source") != "" {
			s3Error(w, 501, "NotImplemented", "CopyObject is not supported")
			return
		}
		// Stage and verify before touching the destination. Invalid payloads never
		// truncate existing data; a later filesystem write failure may be partial.
		temp, err := os.CreateTemp("", "hostlier-memory-upload-*")
		if err != nil {
			s3Error(w, 503, "SlowDown", "Upload staging unavailable")
			return
		}
		defer os.Remove(temp.Name())
		defer temp.Close()
		hash := sha256.New()
		n, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(r.Body, (1<<30)+1))
		if err != nil || n > 1<<30 {
			s3Error(w, 400, "EntityTooLarge", "PutObject is limited to 1 GiB")
			return
		}
		sum := hex.EncodeToString(hash.Sum(nil))
		expected := r.Header.Get("X-Amz-Content-Sha256")
		if expected != "UNSIGNED-PAYLOAD" && expected != sum {
			s3Error(w, 400, "XAmzContentSHA256Mismatch", "Payload checksum mismatch")
			return
		}
		if err = makeParents(r.Context(), root, key); err != nil {
			s3FileError(w, err)
			return
		}
		// A regular file export intentionally writes the selected inode. Directory
		// exports use the same confined file operations as native drive clients.
		f, err := root.OpenFile(r.Context(), key, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			s3FileError(w, err)
			return
		}
		temp.Seek(0, io.SeekStart)
		_, err = io.Copy(f, temp)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			s3Error(w, 500, "InternalError", "Object write failed")
			return
		}
		w.Header().Set("ETag", fmt.Sprintf("%q", sum))
		w.WriteHeader(200)
	case "DELETE":
		info, err := root.Stat(r.Context(), key)
		if errors.Is(err, fs.ErrNotExist) {
			w.WriteHeader(204)
			return
		}
		if err != nil {
			s3FileError(w, err)
			return
		}
		if info.IsDir() {
			s3Error(w, 409, "InvalidRequest", "DeleteObject cannot recursively delete a directory")
			return
		}
		if err = root.RemoveAll(r.Context(), key); err != nil {
			s3FileError(w, err)
			return
		}
		w.WriteHeader(204)
	default:
		s3Error(w, 501, "NotImplemented", "Unsupported S3 operation")
	}
}

func makeParents(ctx context.Context, root *Root, key string) error {
	// Never normalize traversal away before the filesystem boundary checks it.
	if strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\\\x00") {
		return fs.ErrPermission
	}
	parts := strings.Split(key, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fs.ErrPermission
		}
	}
	for i := 1; i < len(parts); i++ {
		err := root.Mkdir(ctx, strings.Join(parts[:i], "/"), 0700)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}

type objectItem struct {
	Key          string
	LastModified string
	Size         int64
}
type listResult struct {
	XMLName               xml.Name
	Xmlns                 string `xml:"xmlns,attr"`
	Name                  string
	Prefix                string
	KeyCount              int
	MaxKeys               int
	IsTruncated           bool
	NextContinuationToken string `xml:",omitempty"`
	Contents              []objectItem
	CommonPrefixes        []commonPrefix
}
type commonPrefix struct{ Prefix string }

func listObjects(w http.ResponseWriter, r *http.Request, root *Root, bucket string) {
	q := r.URL.Query()
	if q.Get("list-type") != "" && q.Get("list-type") != "2" {
		s3Error(w, 400, "InvalidArgument", "Use ListObjectsV2")
		return
	}
	max := 1000
	if raw := q.Get("max-keys"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 0 || n > 1000 {
			s3Error(w, 400, "InvalidArgument", "max-keys must be 0–1000")
			return
		}
		max = n
	}
	prefix, delimiter := q.Get("prefix"), q.Get("delimiter")
	if delimiter != "" && delimiter != "/" {
		s3Error(w, 400, "InvalidArgument", "Only / delimiter is supported")
		return
	}
	var items []objectItem
	visited := 0
	var walk func(string) error
	walk = func(dir string) error {
		if len(items) >= 10000 {
			return errors.New("listing limit")
		}
		f, e := root.OpenFile(r.Context(), dir, os.O_RDONLY, 0)
		if e != nil {
			return e
		}
		entries, e := f.Readdir(10001 - visited)
		f.Close()
		if e != nil && !errors.Is(e, io.EOF) {
			return e
		}
		visited += len(entries)
		if visited > 10000 {
			return errors.New("listing limit")
		}
		for _, entry := range entries {
			key := path.Join(dir, entry.Name())
			if entry.IsDir() {
				if e = walk(key); e != nil {
					return e
				}
			} else {
				items = append(items, objectItem{Key: key, Size: entry.Size(), LastModified: entry.ModTime().UTC().Format(time.RFC3339)})
			}
			if len(items) > 10000 {
				return errors.New("listing limit")
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		s3Error(w, 503, "SlowDown", "Listing unavailable or exceeds 10000 files")
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	start := q.Get("continuation-token")
	if start == "" {
		start = q.Get("start-after")
	}
	result := listResult{XMLName: xml.Name{Local: "ListBucketResult"}, Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/", Name: bucket, Prefix: prefix, MaxKeys: max}
	seen := map[string]bool{}
	for _, item := range items {
		if !strings.HasPrefix(item.Key, prefix) {
			continue
		}
		candidate := item.Key
		isPrefix := false
		if delimiter != "" {
			if i := strings.Index(strings.TrimPrefix(item.Key, prefix), "/"); i >= 0 {
				candidate = prefix + strings.TrimPrefix(item.Key, prefix)[:i+1]
				isPrefix = true
			}
		}
		if candidate <= start || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if result.KeyCount == max {
			result.IsTruncated = true
			break
		}
		if isPrefix {
			result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix{candidate})
		} else {
			result.Contents = append(result.Contents, item)
		}
		result.KeyCount++
		result.NextContinuationToken = candidate
	}
	if !result.IsTruncated {
		result.NextContinuationToken = ""
	}
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(result)
}
func s3Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	xml.NewEncoder(w).Encode(struct {
		XMLName       xml.Name
		Code, Message string
	}{xml.Name{Local: "Error"}, code, message})
}
func s3FileError(w http.ResponseWriter, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		s3Error(w, 404, "NoSuchKey", "Object not found")
		return
	}
	s3Error(w, 403, "AccessDenied", "Object is outside the accessible export")
}
