package emailauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultEngineURL is the hosted Engine selected for Hostlier's email integration.
const DefaultEngineURL = "https://eng.usefused.com"

// DefaultEmailAppFamilyID identifies the hosted enrollment application family.
const DefaultEmailAppFamilyID = "1b1a3e00-54c3-458f-92c2-d9522d083eab"

var emailAppFamilyID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

// Fused sends a fixed enrollment contract through Engine; provider credentials remain in its bucket.
type Fused struct {
	Provider, URL, Token, From string
	HTTP                       *http.Client
}

// NewFused validates an explicit provider selection, app family ID, Engine origin and scoped execution token.
func NewFused(provider, engine, appFamilyID, token, from string) (*Fused, error) {
	// Provider selection is explicit and cannot silently fall back after delivery failure.
	if provider != "resend" && provider != "plunk" {
		return nil, errors.New("choose resend, plunk or smtp")
	}
	// The hosted Engine is the default; private installations can select their verified HTTPS origin.
	if engine == "" {
		engine = DefaultEngineURL
	}
	// Engine executes the reviewed active version of this dedicated application family.
	if appFamilyID == "" {
		appFamilyID = DefaultEmailAppFamilyID
	}
	parsed, err := emailEngineOrigin(engine)
	// Reject ambiguous targets before an execution token or enrollment code can leave this process.
	if err != nil || !emailAppFamilyID.MatchString(appFamilyID) {
		return nil, errors.New("email requires an HTTPS Engine origin and an app family UUID")
	}
	// An execution token is separate from a provider API key and must come from the configured secret source.
	if !validExecutionToken(token) {
		return nil, errors.New("Fused email execution token is missing or invalid")
	}
	// The selected provider must have verified this single bare sender address.
	normalizedFrom, err := Address(from)
	// Canonicalize the validated sender before the hosted schema and provider receive it.
	if err != nil {
		return nil, err
	}
	parsed.Path = "/v1/apps/" + appFamilyID + "/executions"
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	// Redirects must never forward this capability or its OTP payload to another endpoint.
	return &Fused{Provider: provider, URL: parsed.String(), Token: token, From: normalizedFrom, HTTP: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// emailEngineOrigin admits only one verified HTTPS origin with no path, credentials or request-routing suffix.
func emailEngineOrigin(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	// Engine URLs cannot smuggle another authority or execute an unrelated route.
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("invalid email Engine origin")
	}
	return parsed, nil
}

// Send performs exactly one bounded Unified App execution and requires its explicit terminal delivery receipt.
func (p *Fused) Send(ctx context.Context, message Message) error {
	// Invalid recipients never reach the hosted execution endpoint.
	if _, err := Address(message.Email); err != nil {
		return errors.New("email delivery unavailable")
	}
	id := make([]byte, 16)
	// Random execution identities cannot be reconstructed from low-entropy verification codes.
	if _, err := rand.Read(id); err != nil {
		return errors.New("email delivery unavailable")
	}
	payload := struct {
		Operation string `json:"operation"`
		Input     any    `json:"input"`
	}{Operation: "execute", Input: struct {
		Message
		Provider   string `json:"provider"`
		From       string `json:"from"`
		DeliveryID string `json:"deliveryId"`
	}{message, p.Provider, p.From, hex.EncodeToString(id)}}
	data, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, "POST", p.URL, bytes.NewReader(data))
	// Construction errors reveal neither the execution capability nor message contents.
	if err != nil {
		return errors.New("email delivery unavailable")
	}
	request.Header.Set("Authorization", "Bearer "+p.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", hex.EncodeToString(id))
	return p.execute(request)
}

// execute never retries an ambiguous send or exposes Engine/provider diagnostic bodies to the enrollment API.
func (p *Fused) execute(request *http.Request) error {
	response, err := p.HTTP.Do(request)
	// Transport failures invalidate the pending code through the existing enrollment service.
	if err != nil {
		return errors.New("email delivery unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	// Oversized or unsuccessful responses cannot count as accepted email delivery.
	if err != nil || len(data) > 65536 || response.StatusCode != http.StatusOK {
		return errors.New("email delivery unavailable")
	}
	return p.executionReceipt(data)
}

// executionReceipt requires both Engine completion and the app's matching provider/message acknowledgment.
func (p *Fused) executionReceipt(data []byte) error {
	var result struct {
		Status string `json:"status"`
		Output struct {
			Sent      bool   `json:"sent"`
			Provider  string `json:"provider"`
			MessageID string `json:"messageId"`
		} `json:"output"`
	}
	// A queued, failed or malformed execution must never leave its verification code usable.
	if json.Unmarshal(data, &result) != nil || result.Status != "succeeded" || !result.Output.Sent || result.Output.Provider != p.Provider || strings.TrimSpace(result.Output.MessageID) == "" {
		return errors.New("email delivery unavailable")
	}
	return nil
}

// validExecutionToken bounds the controller-held capability without ever reporting its value.
func validExecutionToken(value string) bool {
	return value != "" && len(value) <= 8192 && !strings.ContainsAny(value, " \t\r\n")
}
