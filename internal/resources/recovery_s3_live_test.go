package resources

import (
	"bytes"
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"hostlier/internal/model"
	"hostlier/internal/recovery"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// liveS3 starts an isolated S3-compatible object server that exists only for this test run.
func liveS3(t *testing.T) *recovery.S3 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Keep the callback bound to this fixture and its cleanup lifetime.
	docker := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", append([]string{"-H", "unix://" + os.Getenv("HOSTLIER_DOCKER_SOCKET")}, args...)...).CombinedOutput()
	}
	out, err := docker("run", "-d", "--rm", "--tmpfs", "/data", "-p", "127.0.0.1::7070", "-e", "ROOT_ACCESS_KEY_ID=hostlier-fixture", "-e", "ROOT_SECRET_ACCESS_KEY=hostlier-fixture-secret", "versity/versitygw:latest", "--port", ":7070", "posix", "/data")
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatalf("S3 fixture: %v %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	// Keep the callback bound to this fixture and its cleanup lifetime.
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		// Stop the fixture on an unexpected operation result.
		if output, err := exec.CommandContext(cleanup, "docker", "-H", "unix://"+os.Getenv("HOSTLIER_DOCKER_SOCKET"), "rm", "-f", id).CombinedOutput(); err != nil {
			t.Errorf("S3 cleanup: %v %s", err, output)
		}
	})
	port, err := docker("inspect", "--format", `{{(index (index .NetworkSettings.Ports "7070/tcp") 0).HostPort}}`, id)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal(err)
	}
	client := recovery.NewS3("http://127.0.0.1:"+strings.TrimSpace(string(port)), "us-east-1", "recovery-fixture", "native", "hostlier-fixture", "hostlier-fixture-secret")
	// Exercise each native recovery case independently.
	for attempt := 0; attempt < 30; attempt++ {
		_, err = client.Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(client.Bucket)})
		// Stop the fixture on an unexpected operation result.
		if err == nil {
			return client
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("S3 bucket unavailable", err)
	return nil
}

// liveSnapshotRoundTrip restores exclusively from the encrypted object fetched from another container.
func liveSnapshotRoundTrip(t *testing.T, ctx context.Context, objects *recovery.S3, spec model.ResourceSpec, evidence model.DatabaseEvidence, data []byte) []byte {
	t.Helper()
	key := bytes.Repeat([]byte{9}, 32)
	id := recovery.Digest([]byte(spec.Type))
	bundle := recovery.Bundle{ID: id, Source: spec.Name, Spec: spec, Evidence: evidence, Data: data, CreatedAt: time.Now().Unix()}
	sealed, err := recovery.Seal(key, bundle)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal(err)
	}
	// Stop the fixture on an unexpected operation result.
	if err = objects.Put(ctx, id, sealed); err != nil {
		t.Fatal("S3 upload", err)
	}
	// Stop the fixture on an unexpected operation result.
	if err = objects.Put(ctx, id, []byte("must never overwrite")); err == nil {
		t.Fatal("immutable object overwritten")
	}
	downloaded, err := objects.Get(ctx, id)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal("S3 download", err)
	}
	recovered, err := recovery.Open(key, downloaded)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal("S3 authentication", err)
	}
	// Fail the fixture if the recovery invariant is violated.
	if !bytes.Equal(data, recovered.Data) {
		t.Fatal("downloaded native data mismatch")
	}
	liveSnapshotDeletion(t, ctx, objects, id)
	t.Log(fmt.Sprintf("encrypted S3 upload, immutable identity, read-back, discovery and delete passed (%d bytes)", len(sealed)))
	return recovered.Data
}

// liveSnapshotDeletion verifies that portable discovery and bounded retention address the same immutable identity.
func liveSnapshotDeletion(t *testing.T, ctx context.Context, objects *recovery.S3, id string) {
	t.Helper()
	listed, err := objects.List(ctx)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal(err)
	}
	found := false
	// Exercise each native recovery case independently.
	for _, item := range listed {
		found = found || item == id
	}
	// Fail the fixture if the recovery invariant is violated.
	if !found {
		t.Fatal("object discovery failed")
	}
	// Stop the fixture on an unexpected operation result.
	if err = objects.Delete(ctx, id); err != nil {
		t.Fatal("retention delete", err)
	}
	// Stop the fixture on an unexpected operation result.
	if _, err = objects.Get(ctx, id); err == nil {
		t.Fatal("deleted object remains readable")
	}
}
