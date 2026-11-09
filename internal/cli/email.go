package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"hostlier/internal/client"
	emailauth "hostlier/internal/emailprotocol"
	"hostlier/internal/model"
)

// enrollEmail proves mailbox access and device possession before persisting a reusable device credential.
func (s *session) enrollEmail(remote Requester, name string) (enrollmentCredential, error) {
	var credential enrollmentCredential
	// Reject the request before side effects: Email enrollment requires your terminal to enter the code.
	if !s.interactive {
		return credential, model.Err("INTERACTIVE_REQUIRED", "Email enrollment requires your terminal to enter the code")
	}
	address, err := emailauth.Address(s.flags["email"])
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return credential, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return credential, err
	}
	var receipt emailauth.Receipt
	request := emailauth.Request{Email: address, Name: name, PublicKey: base64.RawStdEncoding.EncodeToString(public)}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err = remote.Request(s.ctx, "POST", "/v1/email/begin", request, &receipt); err != nil {
		return credential, err
	}
	fmt.Fprintln(s.errOut, "If this email is authorized, a verification code has been sent. Enter it only in this terminal.")
	code, err := s.ask("Email verification code: ", true)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return credential, err
	}
	proof := emailauth.Proof{ID: receipt.ID, Code: strings.TrimSpace(code), Signature: base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, emailauth.EnrollmentMessage(receipt.Realm, receipt.ID)))}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err = remote.Request(s.ctx, "POST", "/v1/email/verify", proof, &credential); err != nil {
		return credential, err
	}
	// Reject the request before side effects: Controller returned no device credential.
	if credential.Token == "" || credential.ClientID == "" {
		return credential, model.Err("ENROLLMENT", "Controller returned no device credential")
	}
	credential.Token = client.EncodeDeviceCredential(credential.Token, private)
	return credential, nil
}
