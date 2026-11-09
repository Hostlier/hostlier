package emailauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

// captureSender retains test messages in memory without contacting any email provider.
type captureSender struct {
	messages []Message
	err      error
}

// Send implements isolated email acceptance for security tests.
func (s *captureSender) Send(_ context.Context, m Message) error {
	s.messages = append(s.messages, m)
	return s.err
}

// enrollmentFixture provides one trusted mailbox and a fresh device key.
func enrollmentFixture(t *testing.T) (*Service, *captureSender, Request, ed25519.PrivateKey) {
	t.Helper()
	sender := &captureSender{}
	s, err := New([]string{"owner@example.com"}, sender, "controller.example.com")
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	return s, sender, Request{Email: "owner@example.com", Name: "Laptop", PublicKey: base64.RawStdEncoding.EncodeToString(public)}, private
}

// deviceProof supplies both the delivered code and the matching private-device signature.
func deviceProof(s *Service, r Receipt, code string, key ed25519.PrivateKey) Proof {
	return Proof{ID: r.ID, Code: code, Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, EnrollmentMessage(s.Realm, r.ID)))}
}

// TestEmailEnrollmentRequiresDeviceAndConsumesCode proves a mailbox code cannot enroll an attacker's alternate key.
func TestEmailEnrollmentRequiresDeviceAndConsumesCode(t *testing.T) {
	s, sender, request, key := enrollmentFixture(t)
	r, err := s.Begin(context.Background(), request)
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	// Fail if the fixture violates this invariant: wrong device accepted.
	if _, err = s.Verify(deviceProof(s, r, sender.messages[0].Code, wrong)); err == nil {
		t.Fatal("wrong device accepted")
	}
	proof := deviceProof(s, r, sender.messages[0].Code, key)
	verified, err := s.Verify(proof)
	// Stop when fixture setup or the exercised operation fails.
	if err != nil || verified.PublicKey != request.PublicKey {
		t.Fatal(verified, err)
	}
	// Fail if the fixture violates this invariant: code replay accepted.
	if _, err = s.Verify(proof); err == nil {
		t.Fatal("code replay accepted")
	}
}

// TestEmailEnrollmentDeniesUnknownFailedExpiredAndExhaustedChallenges covers the fail-closed enrollment outcomes.
func TestEmailEnrollmentDeniesUnknownFailedExpiredAndExhaustedChallenges(t *testing.T) {
	cases := []string{"unknown", "failed", "expired", "exhausted"}
	// Exercise every case independently, including denied inputs.
	for _, name := range cases {
		// Isolate this case so credentials and mutable state cannot leak between scenarios.
		t.Run(name, func(t *testing.T) {
			assertRejectedEnrollment(t, name)
		})
	}
}

// TestEmailResendBudgetAndReplacement prevents resends from extending old code authority or flooding a mailbox.
func TestEmailResendBudgetAndReplacement(t *testing.T) {
	s, sender, request, key := enrollmentFixture(t)
	now := time.Now()
	// Keep this callback bounded to the current request and its captured state.
	s.Now = func() time.Time { return now }
	first, _ := s.Begin(context.Background(), request)
	proof := deviceProof(s, first, sender.messages[0].Code, key)
	s.Begin(context.Background(), request)
	// Fail if the fixture violates this invariant: cooldown bypass.
	if len(sender.messages) != 1 {
		t.Fatal("cooldown bypass")
	}
	now = now.Add(time.Minute)
	s.Begin(context.Background(), request)
	// Fail if the fixture violates this invariant: old code survived replacement.
	if _, err := s.Verify(proof); err == nil {
		t.Fatal("old code survived replacement")
	}
	now = now.Add(time.Minute)
	s.Begin(context.Background(), request)
	now = now.Add(time.Minute)
	s.Begin(context.Background(), request)
	// Fail if the fixture violates this invariant: hourly send budget bypass.
	if len(sender.messages) != 3 {
		t.Fatal("hourly send budget bypass", len(sender.messages))
	}
}

// assertRejectedEnrollment checks one denied ceremony without depending on another case.
func assertRejectedEnrollment(t *testing.T, name string) {
	t.Helper()
	s, sender, request, key := enrollmentFixture(t)
	// Select the fixture behavior required to exercise this boundary.
	if name == "unknown" {
		request.Email = "stranger@example.com"
	}
	// Select the fixture behavior required to exercise this boundary.
	if name == "failed" {
		sender.err = errors.New("provider failure with secret diagnostics")
	}
	r, err := s.Begin(context.Background(), request)
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	// Fail if the fixture violates this invariant: unknown mailbox mailed.
	if name == "unknown" {
		// Fail if the fixture violates this invariant: unknown mailbox mailed.
		if len(sender.messages) != 0 {
			t.Fatal("unknown mailbox mailed")
		}
		// Fail if the fixture violates this invariant: unknown accepted.
		if _, err = s.Verify(deviceProof(s, r, "000000", key)); err == nil {
			t.Fatal("unknown accepted")
		}
		return
	}
	proof := deviceProof(s, r, sender.messages[0].Code, key)
	// Select the fixture behavior required to exercise this boundary.
	if name == "expired" {
		// Keep this callback bounded to the current request and its captured state.
		s.Now = func() time.Time { return time.UnixMilli(r.ExpiresAt) }
	}
	exhaustProof(s, proof, name)
	// Fail if the fixture violates this invariant: invalid challenge accepted.
	if _, err = s.Verify(proof); err == nil {
		t.Fatal("invalid challenge accepted")
	}
}

// exhaustProof consumes the exact allowed guess budget before a correct code is attempted.
func exhaustProof(s *Service, proof Proof, name string) {
	// Select the fixture behavior required to exercise this boundary.
	if name == "exhausted" {
		wrong := proof
		wrong.Code = "not-a-code"
		// Exercise every case independently, including denied inputs.
		for i := 0; i < 5; i++ {
			s.Verify(wrong)
		}
	}
}
