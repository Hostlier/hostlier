package recovery

import (
	"bytes"
	"hostlier/internal/model"
	"testing"
)

// TestBundleAuthentication rejects damaged content and wrong recovery keys before native import.
func TestBundleAuthentication(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	b := Bundle{ID: Digest([]byte("identity")), Source: "database", Spec: model.ResourceSpec{Type: "postgres"}, Evidence: model.DatabaseEvidence{Engine: "postgres", Version: "16.1", Digest: Digest([]byte("rows"))}, Data: []byte("private native snapshot")}
	sealed, err := Seal(key, b)
	// Stop the fixture on an unexpected operation result.
	if err != nil {
		t.Fatal(err)
	}
	// Fail the fixture if the recovery invariant is violated.
	if bytes.Contains(sealed, b.Data) {
		t.Fatal("plaintext leaked")
	}
	opened, err := Open(key, sealed)
	// Stop the fixture on an unexpected operation result.
	if err != nil || !bytes.Equal(opened.Data, b.Data) {
		t.Fatal(err)
	}
	// Exercise each native recovery case independently.
	for _, index := range []int{0, 20, len(sealed) - 1} {
		changed := append([]byte{}, sealed...)
		changed[index] ^= 1
		// Stop the fixture on an unexpected operation result.
		if _, err = Open(key, changed); err == nil {
			t.Fatal("accepted corrupted envelope")
		}
	}
	// Stop the fixture on an unexpected operation result.
	if _, err = Open(bytes.Repeat([]byte{8}, 32), sealed); err == nil {
		t.Fatal("wrong key accepted")
	}
}

// TestRedisRecoveryAllowsOnlyActualExpiration distinguishes native TTL expiry from lost or changed durable keys.
func TestRedisRecoveryAllowsOnlyActualExpiration(t *testing.T) {
	before := model.DatabaseEvidence{Engine: "redis", Items: []model.DatabaseItem{{Key: "stable", Hash: "one", Expires: -1}, {Key: "expired", Hash: "two", Expires: 100}}}
	after := model.DatabaseEvidence{Engine: "redis", Items: before.Items[:1]}
	// Fail the fixture if the recovery invariant is violated.
	if Match(before, after, 101) != nil {
		t.Fatal("expired key rejected")
	}
	// Fail the fixture if the recovery invariant is violated.
	if Match(before, after, 99) == nil {
		t.Fatal("unexpired key loss accepted")
	}
	after.Items = []model.DatabaseItem{{Key: "stable", Hash: "different", Expires: -1}}
	// Fail the fixture if the recovery invariant is violated.
	if Match(before, after, 101) == nil {
		t.Fatal("changed value accepted")
	}
}
