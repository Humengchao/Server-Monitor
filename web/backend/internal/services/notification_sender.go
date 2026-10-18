package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"server-monitor/internal/models"
)

const notificationSendTimeout = 20 * time.Second

// DeliveryError contains only our own fixed category and numeric provider
// code. Provider bodies/network errors often echo URLs or tokens: never store
// or return those verbatim in delivery history.
type DeliveryError struct {
	Category string
	Code     int
}

func (e *DeliveryError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("%s (code %d)", e.Category, e.Code)
	}
	return e.Category
}
func SafeNotificationError(err error) string {
	if err == nil {
		return ""
	}
	var safe *DeliveryError
	if errors.As(err, &safe) {
		return safe.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Notification timed out; remote acceptance is unknown"
	}
	if errors.Is(err, context.Canceled) {
		return "Notification canceled; remote acceptance is unknown"
	}
	return "Notification delivery failed"
}

type NotificationSender struct {
	http *WebhookNotifier
	now  func() time.Time
	// Tests substitute localhost transports, never third-party credentials.
	telegramBase string
	smtpRootCAs  *x509.CertPool // nil in production: use the system trust store
}

func NewNotificationSender(allowPrivate bool) *NotificationSender {
	notifier := NewWebhookNotifier(allowPrivate)
	// Environment proxies must not bypass the validated IP destination.
	if transport, ok := notifier.client.Transport.(*http.Transport); ok {
		transport.Proxy = nil
	}
	return &NotificationSender{http: notifier, now: time.Now, telegramBase: "https://api.telegram.org"}
}

func notificationText(raw json.RawMessage) string {
	var p struct {
		Status    string    `json:"status"`
		Rule      string    `json:"rule"`
		Server    string    `json:"server"`
		Message   string    `json:"message"`
		Timestamp time.Time `json:"timestamp"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return "Server Monitor notification"
	}
	text := "Server Monitor [" + p.Status + "]"
	if p.Rule != "" {
		text += "\n" + p.Rule
	}
	if p.Server != "" {
		text += "\n" + p.Server
	}
	if p.Message != "" {
		text += "\n" + p.Message
	}
	if !p.Timestamp.IsZero() {
		text += "\n" + p.Timestamp.UTC().Format(time.RFC3339)
	}
	// WeCom text is limited to 2048 UTF-8 bytes. One common bounded message
	// avoids splitting a delivery into independently failing parts.
	if len(text) > 1900 {
		text = text[:1900]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text += "…"
	}
	return text
}

func dingTalkSignedURL(raw, secret string, at time.Time) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("invalid robot endpoint")
	}
	q := parsed.Query()
	q.Del("timestamp")
	q.Del("sign")
	if secret != "" {
		timestamp := strconv.FormatInt(at.UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + secret))
		q.Set("timestamp", timestamp)
		q.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	}
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

func (s *NotificationSender) Send(ctx context.Context, channel models.NotificationChannel, payload json.RawMessage, deliveryID string) error {
	ctx, cancel := context.WithTimeout(ctx, notificationSendTimeout)
	defer cancel()
	config := channel.Config
	if err := ValidateNotificationConfig(channel.Type, &config); err != nil {
		return &DeliveryError{Category: "Invalid channel configuration"}
	}
	if channel.Type == "email" {
		return s.sendSMTP(ctx, config, notificationText(payload), deliveryID)
	}
	target := config.WebhookURL
	var body any = payload
	switch channel.Type {
	case "telegram":
		target = s.telegramBase + "/bot" + config.BotToken + "/sendMessage"
		message := map[string]any{"chat_id": config.ChatID, "text": notificationText(payload)}
		if config.MessageThreadID > 0 {
			message["message_thread_id"] = config.MessageThreadID
		}
		body = message
	case "wecom", "dingtalk":
		body = map[string]any{"msgtype": "text", "text": map[string]string{"content": notificationText(payload)}}
		if channel.Type == "dingtalk" {
			var err error
			target, err = dingTalkSignedURL(target, config.Secret, s.now())
			if err != nil {
				return &DeliveryError{Category: "Invalid robot endpoint"}
			}
		}
	}
	ips, err := s.http.validateTargetContext(ctx, target)
	if err != nil {
		return &DeliveryError{Category: "Endpoint rejected or DNS lookup failed"}
	}
	if len(ips) > 0 {
		ctx = context.WithValue(ctx, webhookPinnedIPs{}, ips)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return &DeliveryError{Category: "Invalid notification payload"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		return &DeliveryError{Category: "Invalid notification endpoint"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "server-monitor-notifications/1")
	req.Header.Set("X-Server-Monitor-Delivery", deliveryID)
	if channel.Type == "webhook" && config.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+config.BearerToken)
	}
	response, err := s.http.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &DeliveryError{Category: "HTTP connection or TLS verification failed"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &DeliveryError{Category: "HTTP endpoint rejected notification", Code: response.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		return &DeliveryError{Category: "Cannot read notification response"}
	}
	if len(data) > 65536 {
		return &DeliveryError{Category: "Notification response is too large"}
	}
	return checkNotificationResponse(channel.Type, data)
}
func checkNotificationResponse(kind string, data []byte) error {
	var result struct {
		OK        *bool `json:"ok"`
		Success   *bool `json:"success"`
		ErrCode   *int  `json:"errcode"`
		ErrorCode int   `json:"error_code"`
	}
	err := json.Unmarshal(data, &result)
	switch kind {
	case "telegram":
		if err != nil || result.OK == nil {
			return &DeliveryError{Category: "Telegram returned an invalid response"}
		}
		if !*result.OK {
			return &DeliveryError{Category: "Telegram rejected notification", Code: result.ErrorCode}
		}
	case "wecom", "dingtalk":
		if err != nil || result.ErrCode == nil {
			return &DeliveryError{Category: "Robot returned an invalid response"}
		}
		if *result.ErrCode != 0 {
			return &DeliveryError{Category: "Robot rejected notification", Code: *result.ErrCode}
		}
	case "webhook":
		if err == nil && ((result.OK != nil && !*result.OK) || (result.Success != nil && !*result.Success) || (result.ErrCode != nil && *result.ErrCode != 0)) {
			return &DeliveryError{Category: "Webhook reported an application error"}
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) == nil {
			for _, key := range []string{"ok", "success"} {
				var success bool
				if raw, exists := fields[key]; exists && json.Unmarshal(raw, &success) == nil && !success {
					return &DeliveryError{Category: "Webhook reported an application error"}
				}
			}
			for _, key := range []string{"errcode", "code"} {
				if raw, exists := fields[key]; exists {
					var code int
					if json.Unmarshal(raw, &code) == nil && code != 0 && code != 200 {
						return &DeliveryError{Category: "Webhook reported an application error", Code: code}
					}
				}
			}
		}
	}
	return nil
}

func notificationSubject(text string) string { return strings.SplitN(text, "\n", 2)[0] }
