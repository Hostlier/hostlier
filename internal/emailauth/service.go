package emailauth

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hostlier/internal/emailprotocol"
	"math/big"
	"sync"
	"time"
)

// Request is the shared enrollment request contract.
type Request = emailprotocol.Request

// Receipt is the shared challenge receipt contract.
type Receipt = emailprotocol.Receipt

// Proof is the shared device proof contract.
type Proof = emailprotocol.Proof

// challenge retains only a keyed code digest and a bounded number of attempts.
type challenge struct {
	Request  Request
	Digest   []byte
	Expires  time.Time
	Attempts int
	Ready    bool
}

// budget tracks bounded sends or guesses in a fixed time window.
type budget struct {
	Start time.Time
	Last  time.Time
	Count int
}

// Service owns process-local challenges; restarting invalidates every unfinished enrollment.
type Service struct {
	mu                sync.Mutex
	Sender            Sender
	Allowed           map[string]bool
	Controller, Realm string
	Now               func() time.Time
	key               []byte
	challenges        map[string]*challenge
	recipients        map[string]budget
	sends, guesses    budget
}

// New requires explicit owner-approved mailbox addresses and a configured delivery backend.
func New(emails []string, sender Sender, controller string) (*Service, error) {
	// Stop before this invalid or unavailable state can reach the next protocol step.
	if sender == nil || len(emails) == 0 || len(emails) > 100 {
		return nil, fmt.Errorf("email enrollment requires a sender and 1–100 allowed addresses")
	}
	service := &Service{Sender: sender, Allowed: map[string]bool{}, Controller: controller, Realm: randomID(), Now: time.Now, key: make([]byte, 32), challenges: map[string]*challenge{}, recipients: map[string]budget{}}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if _, err := rand.Read(service.key); err != nil {
		return nil, err
	}
	// Validate each entry before accepting the complete collection.
	for _, value := range emails {
		address, err := Address(value)
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err != nil {
			return nil, err
		}
		service.Allowed[address] = true
	}
	return service, nil
}

// randomID generates an unguessable enrollment identity.
func randomID() string {
	value := make([]byte, 32)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

// EnrollmentMessage returns the shared domain-separated enrollment proof.
func EnrollmentMessage(realm, id string) []byte { return emailprotocol.EnrollmentMessage(realm, id) }

// PublicKey accepts one canonical Ed25519 device public key.
func PublicKey(value string) (ed25519.PublicKey, error) {
	raw, err := base64.RawStdEncoding.DecodeString(value)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid device public key")
	}
	return ed25519.PublicKey(raw), nil
}

// digest prevents offline enumeration of the short code from retained challenge data.
func (s *Service) digest(id, email, code string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(id + "\x00" + email + "\x00" + code))
	return mac.Sum(nil)
}

// takeBudget counts every attempted operation, including successful and unknown-recipient requests.
func (s *Service) takeBudget(value *budget, window time.Duration, limit int) bool {
	now := s.Now()
	// Apply this check before granting authority or advancing protocol state.
	if now.Sub(value.Start) >= window {
		*value = budget{Start: now}
	}
	value.Count++
	return value.Count <= limit
}

// Begin reserves a bounded challenge before releasing the lock for provider delivery.
func (s *Service) Begin(ctx context.Context, request Request) (Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt := Receipt{ID: randomID(), Realm: s.Realm, ExpiresAt: s.Now().Add(5 * time.Minute).UnixMilli()}
	// Apply this check before granting authority or advancing protocol state.
	if !s.takeBudget(&s.sends, time.Minute, 20) {
		return receipt, fmt.Errorf("enrollment throttled")
	}
	address, err := Address(request.Email)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return receipt, fmt.Errorf("invalid enrollment request")
	}
	request.Email = address
	key, err := PublicKey(request.PublicKey)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil || request.Name == "" || len(request.Name) > 100 {
		return receipt, fmt.Errorf("invalid enrollment request")
	}
	// Apply this check before granting authority or advancing protocol state.
	if !s.Allowed[address] {
		return receipt, nil
	}
	// Apply this check before granting authority or advancing protocol state.
	if !s.reserveRecipient(address) {
		return receipt, nil
	}
	number, err := rand.Int(rand.Reader, big.NewInt(1000000))
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return receipt, err
	}
	code := fmt.Sprintf("%06d", number.Int64())
	entry := &challenge{Request: request, Digest: s.digest(receipt.ID, address, code), Expires: time.UnixMilli(receipt.ExpiresAt)}
	s.challenges[receipt.ID] = entry
	sum := sha256.Sum256(key)
	message := Message{Email: address, Code: code, ExpiresInSeconds: 300, Controller: s.Controller, DeviceName: request.Name, DeviceFingerprint: hex.EncodeToString(sum[:])}
	s.mu.Unlock()
	err = s.Sender.Send(ctx, message)
	s.mu.Lock()
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		delete(s.challenges, receipt.ID)
	} else {
		entry.Ready = true
	}
	return receipt, nil
}

// reserveRecipient prevents mailbox flooding and invalidates older attempts when a fresh send is admitted.
func (s *Service) reserveRecipient(email string) bool {
	now := s.Now()
	rate := s.recipients[email]
	// Apply this check before granting authority or advancing protocol state.
	if now.Sub(rate.Start) >= time.Hour {
		rate = budget{Start: now}
	}
	// Fail closed when the required identity, scope or format does not match.
	if rate.Count >= 3 || now.Sub(rate.Last) < time.Minute {
		return false
	}
	rate.Count++
	rate.Last = now
	s.recipients[email] = rate
	// Expire retained entries to keep protocol memory bounded.
	for id, entry := range s.challenges {
		// Apply this check before granting authority or advancing protocol state.
		if entry.Request.Email == email || !now.Before(entry.Expires) {
			delete(s.challenges, id)
		}
	}
	return true
}

// Verify atomically consumes a matching code only with proof of the exact requesting device key.
func (s *Service) Verify(proof Proof) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	invalid := fmt.Errorf("invalid or expired enrollment code")
	// Apply this check before granting authority or advancing protocol state.
	if !s.takeBudget(&s.guesses, time.Minute, 20) {
		return Request{}, invalid
	}
	entry := s.challenges[proof.ID]
	// Apply this check before granting authority or advancing protocol state.
	if entry == nil {
		return Request{}, invalid
	}
	entry.Attempts++
	// Apply this check before granting authority or advancing protocol state.
	if !entry.Ready || !s.Now().Before(entry.Expires) || entry.Attempts > 5 {
		delete(s.challenges, proof.ID)
		return Request{}, invalid
	}
	key, _ := PublicKey(entry.Request.PublicKey)
	signature, _ := base64.RawStdEncoding.DecodeString(proof.Signature)
	valid := hmac.Equal(entry.Digest, s.digest(proof.ID, entry.Request.Email, proof.Code)) && ed25519.Verify(key, EnrollmentMessage(s.Realm, proof.ID), signature)
	// Apply this check before granting authority or advancing protocol state.
	if valid || entry.Attempts >= 5 {
		delete(s.challenges, proof.ID)
	}
	// Apply this check before granting authority or advancing protocol state.
	if !valid {
		return Request{}, invalid
	}
	return entry.Request, nil
}
