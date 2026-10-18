package services

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"server-monitor/internal/models"
)

// Local SMTP fixture supports both implicit TLS and STARTTLS. It records the
// complete DATA body and refuses authentication before the TLS handshake.
func smtpFixture(t *testing.T, mode string, advertiseTLS bool) (models.NotificationConfig, *x509.CertPool, <-chan string) {
	t.Helper()
	certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := certificateServer.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(certificateServer.Certificate())
	certificateServer.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	messages := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		secure := false
		tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
		if mode == "tls" {
			wrapped := tls.Server(conn, tlsConfig)
			if wrapped.Handshake() != nil {
				return
			}
			conn = wrapped
			secure = true
		}
		reader := bufio.NewReader(conn)
		_, _ = fmt.Fprint(conn, "220 localhost ESMTP\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			command := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(command, "EHLO"):
				_, _ = fmt.Fprint(conn, "250-localhost\r\n")
				if advertiseTLS && !secure {
					_, _ = fmt.Fprint(conn, "250-STARTTLS\r\n")
				}
				_, _ = fmt.Fprint(conn, "250 AUTH PLAIN\r\n")
			case command == "STARTTLS":
				if !advertiseTLS {
					return
				}
				_, _ = fmt.Fprint(conn, "220 Ready\r\n")
				wrapped := tls.Server(conn, tlsConfig)
				if wrapped.Handshake() != nil {
					return
				}
				conn = wrapped
				reader = bufio.NewReader(conn)
				secure = true
			case strings.HasPrefix(command, "AUTH"):
				if !secure {
					t.Error("SMTP authentication sent in cleartext")
					return
				}
				_, _ = fmt.Fprint(conn, "235 Authenticated\r\n")
			case strings.HasPrefix(command, "MAIL"), strings.HasPrefix(command, "RCPT"):
				_, _ = fmt.Fprint(conn, "250 OK\r\n")
			case command == "DATA":
				_, _ = fmt.Fprint(conn, "354 Send data\r\n")
				var body strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					body.WriteString(line)
				}
				messages <- body.String()
				_, _ = fmt.Fprint(conn, "250 Accepted\r\n")
			case command == "QUIT":
				_, _ = fmt.Fprint(conn, "221 Bye\r\n")
				return
			default:
				_, _ = fmt.Fprint(conn, "500 unsupported\r\n")
			}
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	return models.NotificationConfig{SMTPHost: host, SMTPPort: number, TLSMode: mode, Username: "sender", Password: "fixture-secret", From: "from@example.com", To: []string{"to@example.com"}}, roots, messages
}

func TestSMTPImplicitTLSAndSTARTTLSDeliverLocally(t *testing.T) {
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			config, roots, messages := smtpFixture(t, mode, true)
			sender := NewNotificationSender(true)
			sender.smtpRootCAs = roots
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := sender.sendSMTP(ctx, config, "Server Monitor [test]\nhello 中文", "42"); err != nil {
				t.Fatal(err)
			}
			select {
			case body := <-messages:
				if !strings.Contains(body, "Message-ID: <notification-42@server-monitor.local>") || !strings.Contains(body, "quoted-printable") || strings.Contains(body, "fixture-secret") {
					t.Fatalf("invalid MIME body: %s", body)
				}
			default:
				t.Fatal("SMTP DATA never received")
			}
		})
	}
}
func TestSMTPRefusesMissingSTARTTLSAndUntrustedCertificates(t *testing.T) {
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			config, _, messages := smtpFixture(t, mode, false)
			sender := NewNotificationSender(true)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := sender.sendSMTP(ctx, config, "test", "42"); err == nil {
				t.Fatal("insecure SMTP succeeded")
			}
			select {
			case <-messages:
				t.Fatal("message sent before required TLS verification")
			default:
			}
		})
	}
}
