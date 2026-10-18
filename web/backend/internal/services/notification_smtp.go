package services

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"server-monitor/internal/models"
)

func (s *NotificationSender) sendSMTP(ctx context.Context, c models.NotificationConfig, text, deliveryID string) error {
	ips, err := s.http.validateTargetContext(ctx, "https://"+net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort)))
	if err != nil {
		return &DeliveryError{Category: "SMTP endpoint rejected or DNS lookup failed"}
	}
	address := net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort))
	dialer := &net.Dialer{Timeout: notificationSendTimeout}
	var conn net.Conn
	if len(ips) > 0 {
		conn, err = dialPinned(ctx, dialer, "tcp", address, ips)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return &DeliveryError{Category: "SMTP connection failed"}
	}
	defer conn.Close()
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	tlsConfig := &tls.Config{ServerName: c.SMTPHost, MinVersion: tls.VersionTLS12, RootCAs: s.smtpRootCAs}
	if c.TLSMode == "tls" {
		secured := tls.Client(conn, tlsConfig)
		if err = secured.HandshakeContext(ctx); err != nil {
			return &DeliveryError{Category: "SMTP TLS verification failed"}
		}
		conn = secured
	}
	client, err := smtp.NewClient(conn, c.SMTPHost)
	if err != nil {
		return &DeliveryError{Category: "SMTP greeting failed"}
	}
	defer client.Close()
	if c.TLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return &DeliveryError{Category: "SMTP server does not support required STARTTLS"}
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return &DeliveryError{Category: "SMTP STARTTLS verification failed"}
		}
	}
	if c.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.SMTPHost)); err != nil {
			return &DeliveryError{Category: "SMTP authentication failed"}
		}
	}
	if err = client.Mail(c.From); err != nil {
		return &DeliveryError{Category: "SMTP sender rejected"}
	}
	for _, recipient := range c.To {
		if err = client.Rcpt(recipient); err != nil {
			return &DeliveryError{Category: "SMTP recipient rejected"}
		}
	}
	writer, err := client.Data()
	if err != nil {
		return &DeliveryError{Category: "SMTP message rejected"}
	}
	headers := "From: " + c.From + "\r\nTo: " + strings.Join(c.To, ", ") + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("UTF-8", notificationSubject(text)) + "\r\n" +
		"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n" +
		"Message-ID: <notification-" + deliveryID + "@server-monitor.local>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n"
	if _, err = fmt.Fprint(writer, headers); err != nil {
		return &DeliveryError{Category: "SMTP message write failed"}
	}
	encoded := quotedprintable.NewWriter(writer)
	if _, err = encoded.Write([]byte(text)); err != nil {
		return &DeliveryError{Category: "SMTP message write failed"}
	}
	if err = encoded.Close(); err != nil {
		return &DeliveryError{Category: "SMTP message write failed"}
	}
	if err = writer.Close(); err != nil {
		return &DeliveryError{Category: "SMTP final acceptance failed; delivery may have occurred"}
	}
	// DATA's final 250 confirms acceptance; a failed QUIT must not resend it.
	_ = client.Quit()
	return nil
}
