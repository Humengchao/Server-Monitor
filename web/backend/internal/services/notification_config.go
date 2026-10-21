package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	"server-monitor/internal/models"
)

var notificationFields = map[string][]string{
	"telegram": {"bot_token", "chat_id", "message_thread_id"},
	"wecom":    {"webhook_url"},
	"dingtalk": {"webhook_url", "secret"},
	"email":    {"smtp_host", "smtp_port", "tls_mode", "username", "password", "from", "to"},
	"webhook":  {"webhook_url", "bearer_token"},
}
var notificationSecrets = map[string]bool{"bot_token": true, "webhook_url": true, "secret": true, "password": true, "bearer_token": true}
var telegramTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

// MergeNotificationConfig preserves omitted fields and blank secret inputs,
// while rejecting unknown keys rather than storing silently ignored settings.
func MergeNotificationConfig(kind string, old models.NotificationConfig, patch map[string]json.RawMessage, clear []string) (models.NotificationConfig, error) {
	fields, ok := notificationFields[kind]
	if !ok {
		return old, errors.New("unsupported notification type")
	}
	allowed := map[string]bool{}
	for _, field := range fields {
		allowed[field] = true
	}
	raw, _ := json.Marshal(old)
	var merged map[string]json.RawMessage
	_ = json.Unmarshal(raw, &merged)
	for key, value := range patch {
		if !allowed[key] {
			return old, fmt.Errorf("unsupported configuration field: %s", key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return old, errors.New("configuration fields cannot be null")
		}
		if notificationSecrets[key] {
			var secret string
			if json.Unmarshal(value, &secret) != nil {
				return old, fmt.Errorf("%s must be a string", key)
			}
			if secret == "" {
				continue
			}
		}
		merged[key] = value
	}
	for _, key := range clear {
		if !allowed[key] || !notificationSecrets[key] {
			return old, errors.New("invalid secret field to clear")
		}
		delete(merged, key)
	}
	raw, _ = json.Marshal(merged)
	var result models.NotificationConfig
	if json.Unmarshal(raw, &result) != nil {
		return old, errors.New("invalid notification configuration field type")
	}
	if err := ValidateNotificationConfig(kind, &result); err != nil {
		return old, err
	}
	// Retained secrets are bound to the endpoint/account where supplied.
	explicit := func(field string) bool {
		for _, name := range clear {
			if name == field {
				return true
			}
		}
		var value string
		return json.Unmarshal(patch[field], &value) == nil && value != ""
	}
	bound := func(secret, field string, changed bool) error {
		if secret != "" && changed && !explicit(field) {
			return fmt.Errorf("destination changed; provide %s again or explicitly clear it", field)
		}
		return nil
	}
	var boundErr error
	switch kind {
	case "email":
		previous := old
		_ = ValidateNotificationConfig(kind, &previous)
		boundErr = bound(old.Password, "password", !strings.EqualFold(previous.SMTPHost, result.SMTPHost) || previous.SMTPPort != result.SMTPPort || previous.TLSMode != result.TLSMode || previous.Username != result.Username)
	case "webhook":
		boundErr = bound(old.BearerToken, "bearer_token", old.WebhookURL != result.WebhookURL)
	case "dingtalk":
		boundErr = bound(old.Secret, "secret", old.WebhookURL != result.WebhookURL)
	}
	if boundErr != nil {
		return old, boundErr
	}
	return result, nil
}

func ValidateNotificationConfig(kind string, c *models.NotificationConfig) error {
	if _, ok := notificationFields[kind]; !ok {
		return errors.New("unsupported notification type")
	}
	c.ChatID = strings.TrimSpace(c.ChatID)
	c.SMTPHost = strings.TrimSpace(c.SMTPHost)
	c.Username = strings.TrimSpace(c.Username)
	c.From = strings.TrimSpace(c.From)
	c.WebhookURL = strings.TrimSpace(c.WebhookURL)
	for _, s := range []string{c.BotToken, c.ChatID, c.WebhookURL, c.Secret, c.BearerToken, c.SMTPHost, c.Username, c.Password, c.From} {
		if len(s) > 4096 || strings.ContainsAny(s, "\r\n\x00") {
			return errors.New("configuration field is too long or contains control characters")
		}
	}
	switch kind {
	case "telegram":
		if !telegramTokenPattern.MatchString(c.BotToken) || c.ChatID == "" || len(c.ChatID) > 128 || c.MessageThreadID < 0 {
			return errors.New("Telegram requires a bot token, chat ID, and optional positive topic ID")
		}
	case "wecom", "dingtalk", "webhook":
		u, err := url.Parse(c.WebhookURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
			return errors.New("a valid HTTP(S) webhook URL without userinfo or fragment is required")
		}
		if kind == "webhook" && c.BearerToken != "" && u.Scheme != "https" {
			return errors.New("webhooks with bearer tokens require HTTPS")
		}
		if kind == "wecom" && (u.Scheme != "https" || u.Host != "qyapi.weixin.qq.com" || u.Path != "/cgi-bin/webhook/send" || u.Query().Get("key") == "") {
			return errors.New("use the enterprise WeChat group robot webhook URL")
		}
		if kind == "dingtalk" && (u.Scheme != "https" || u.Host != "oapi.dingtalk.com" || u.Path != "/robot/send" || u.Query().Get("access_token") == "") {
			return errors.New("use the DingTalk custom robot webhook URL")
		}
	case "email":
		if c.SMTPHost == "" || strings.ContainsAny(c.SMTPHost, "/\\?#@ \t") {
			return errors.New("SMTP host is required and must not contain a scheme or path")
		}
		if strings.Contains(c.SMTPHost, ":") && net.ParseIP(c.SMTPHost) == nil {
			return errors.New("put the SMTP port in smtp_port")
		}
		if c.TLSMode == "" {
			c.TLSMode = "starttls"
		}
		if c.TLSMode != "tls" && c.TLSMode != "starttls" {
			return errors.New("SMTP requires tls or starttls; plaintext delivery is not supported")
		}
		if c.SMTPPort == 0 {
			if c.TLSMode == "tls" {
				c.SMTPPort = 465
			} else {
				c.SMTPPort = 587
			}
		}
		if c.SMTPPort < 1 || c.SMTPPort > 65535 {
			return errors.New("invalid SMTP port")
		}
		if (c.Username == "") != (c.Password == "") {
			return errors.New("SMTP username and password must be provided together")
		}
		if !validNotificationMailbox(c.From) {
			return errors.New("a single valid sender email address is required")
		}
		if len(c.To) == 0 || len(c.To) > 20 {
			return errors.New("provide between 1 and 20 recipient addresses")
		}
		for i, address := range c.To {
			address = strings.TrimSpace(address)
			if !validNotificationMailbox(address) {
				return errors.New("recipient email address is invalid")
			}
			c.To[i] = address
		}
	}
	return nil
}
func validNotificationMailbox(value string) bool {
	if len(value) > 320 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}
