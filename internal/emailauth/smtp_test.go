package emailauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// TestSMTPDataReceipt verifies the envelope, escaped labels and server acceptance without sending external email.
func TestSMTPDataReceipt(t *testing.T) {
	// Exercise every case independently, including denied inputs.
	for _, accept := range []bool{true, false} {
		// Isolate this case so credentials and mutable state cannot leak between scenarios.
		t.Run(fmt.Sprint(accept), func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			clientConn.SetDeadline(time.Now().Add(3 * time.Second))
			received := make(chan string, 1)
			go smtpFixture(serverConn, accept, received)
			client, err := smtp.NewClient(clientConn, "localhost")
			// Stop when fixture setup or the exercised operation fails.
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			sender := SMTP{From: "sender@example.com"}
			err = sender.deliver(client, Message{Email: "owner@example.com", Code: "123456", DeviceName: "device\r\nBcc: injected@example.com", DeviceFingerprint: "fingerprint", Controller: "controller"})
			// Fail if the fixture violates this invariant: accepted=the returned value err=the returned value.
			if (err == nil) != accept {
				t.Fatalf("accepted=%v err=%v", accept, err)
			}
			body := <-received
			// Fail if the fixture violates this invariant: unsafe or missing message content.
			if !strings.Contains(body, "Verification code: 123456") || strings.Contains(body, "\nBcc:") || !strings.Contains(body, `device\r\nBcc:`) {
				t.Fatal("unsafe or missing message content", body)
			}
		})
	}
}

// smtpFixture implements only the submission protocol needed to verify DATA acceptance.
func smtpFixture(conn net.Conn, accept bool, received chan<- string) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	server := textproto.NewConn(conn)
	server.PrintfLine("220 fixture ESMTP")
	// Exercise every case independently, including denied inputs.
	for {
		line, err := server.ReadLine()
		// Stop when fixture setup or the exercised operation fails.
		if err != nil {
			return
		}
		// Keep distinct protocol outcomes on explicit execution paths.
		switch {
		// Handle only this explicit protocol outcome.
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			server.PrintfLine("250 fixture")
		// Handle only this explicit protocol outcome.
		case line == "MAIL FROM:<sender@example.com>", line == "RCPT TO:<owner@example.com>":
			server.PrintfLine("250 ok")
		// Handle only this explicit protocol outcome.
		case line == "DATA":
			server.PrintfLine("354 send data")
			body, err := server.ReadDotBytes()
			// Stop when fixture setup or the exercised operation fails.
			if err != nil {
				return
			}
			received <- string(body)
			// Select the fixture behavior required to exercise this boundary.
			if accept {
				server.PrintfLine("250 queued")
			} else {
				server.PrintfLine("550 refused")
			}
			return
		// Handle only this explicit protocol outcome.
		default:
			server.PrintfLine("502 unavailable")
		}
	}
}

// TestSMTPRejectsDowngradeAndUntrustedTLS ensures no credential or envelope is sent before verified encryption.
func TestSMTPRejectsDowngradeAndUntrustedTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	// Stop when fixture setup or the exercised operation fails.
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan []string, 1)
	// Keep this callback bounded to the current request and its captured state.
	go func() {
		conn, err := listener.Accept()
		// Stop when fixture setup or the exercised operation fails.
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		server := textproto.NewConn(conn)
		server.PrintfLine("220 fixture ESMTP")
		seen := []string{}
		// Exercise every case independently, including denied inputs.
		for {
			line, err := server.ReadLine()
			// Stop when fixture setup or the exercised operation fails.
			if err != nil {
				commands <- seen
				return
			}
			seen = append(seen, line)
			// Select the fixture behavior required to exercise this boundary.
			if strings.HasPrefix(line, "EHLO") {
				server.PrintfLine("250 fixture")
			} else {
				server.PrintfLine("502 STARTTLS unavailable")
			}
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	sender := SMTP{Host: host, Port: port, Mode: "starttls", From: "sender@example.com", Username: "user", Password: "private-password"}
	// Fail if the fixture violates this invariant: accepted unencrypted SMTP.
	if err := sender.Send(context.Background(), Message{Email: "owner@example.com"}); err == nil {
		t.Fatal("accepted unencrypted SMTP")
	}
	// Exercise every case independently, including denied inputs.
	for _, command := range <-commands {
		// Fail if the fixture violates this invariant: sent secrets before TLS.
		if strings.HasPrefix(command, "MAIL") || strings.HasPrefix(command, "AUTH") {
			t.Fatal("sent secrets before TLS", command)
		}
	}
	assertUntrustedSMTP(t, sender)
}

// assertUntrustedSMTP requires certificate validation before any provider credentials are sent.
func assertUntrustedSMTP(t *testing.T, sender SMTP) {
	t.Helper()
	// Keep this callback bounded to the current request and its captured state.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS reached application") }))
	defer server.Close()
	sender.Host, sender.Port, _ = net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	sender.Mode = "tls"
	// Fail if the fixture violates this invariant: accepted untrusted TLS.
	if err := sender.Send(context.Background(), Message{Email: "owner@example.com"}); err == nil {
		t.Fatal("accepted untrusted TLS")
	}
}
