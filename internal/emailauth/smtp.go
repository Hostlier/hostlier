package emailauth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTP delivers directly using verified implicit TLS or mandatory STARTTLS.
type SMTP struct{ Host, Port, From, Username, Password, Mode string }

// Validate refuses plaintext delivery and malformed envelope/header configuration.
func (s SMTP) Validate() error {
	// Use only the explicitly selected transport contract.
	if s.Host == "" || strings.ContainsAny(s.Host, "/\r\n ") || (s.Mode != "tls" && s.Mode != "starttls") {
		return errors.New("SMTP requires a hostname and tls or starttls mode")
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if _, err := net.LookupPort("tcp", s.Port); err != nil {
		return errors.New("SMTP port is invalid")
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if _, err := Address(s.From); err != nil {
		return err
	}
	// Stop before this invalid or unavailable state can reach the next protocol step.
	if (s.Username == "") != (s.Password == "") {
		return errors.New("SMTP username and password must be configured together")
	}
	return nil
}

// Send bounds the full SMTP exchange and hides provider diagnostics from public errors.
func (s SMTP) Send(ctx context.Context, m Message) (err error) {
	// Keep this callback bounded to the current request and its captured state.
	defer func() {
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err != nil {
			err = errors.New("email delivery unavailable")
		}
	}()
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err = s.Validate(); err != nil {
		return err
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if _, err = Address(m.Email); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	config := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	conn, err := s.dial(ctx, config)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	// Keep this callback bounded to the current request and its captured state.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	client, err := smtp.NewClient(conn, s.Host)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	defer client.Close()
	// Use only the explicitly selected transport contract.
	if s.Mode == "starttls" {
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err = client.StartTLS(config); err != nil {
			return err
		}
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.Username != "" {
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err = client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return err
		}
	}
	return s.deliver(client, m)
}

// dial establishes transport without allowing STARTTLS fallback to plaintext.
func (s SMTP) dial(ctx context.Context, config *tls.Config) (net.Conn, error) {
	address := net.JoinHostPort(s.Host, s.Port)
	// Use only the explicitly selected transport contract.
	if s.Mode == "tls" {
		return (&tls.Dialer{Config: config}).DialContext(ctx, "tcp", address)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", address)
}

// deliver submits one fixed plaintext template and treats DATA acceptance as the delivery receipt.
func (s SMTP) deliver(client *smtp.Client, m Message) error {
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := client.Mail(s.From); err != nil {
		return err
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err := client.Rcpt(m.Email); err != nil {
		return err
	}
	writer, err := client.Data()
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return err
	}
	// JSON escaping keeps untrusted device labels from introducing deceptive terminal/email control characters.
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Verify your Hostlier CLI connection\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nController: %q\r\nDevice: %q\r\nDevice fingerprint: %s\r\n\r\nVerification code: %s\r\n\r\nExpires in five minutes. Enter it only in the terminal where you started this connection. This registers a device; it does not approve deployments. Ignore this message if you did not request it.\r\n", s.From, m.Email, m.Controller, m.DeviceName, m.DeviceFingerprint, m.Code)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if _, err = writer.Write([]byte(body)); err != nil {
		writer.Close()
		return err
	}
	return writer.Close()
}
