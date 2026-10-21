package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"server-monitor/internal/models"
)

type localNotificationTransport struct {
	target   *url.URL
	inner    http.RoundTripper
	original func(*http.Request)
}

func (t localNotificationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.original != nil {
		t.original(r)
	}
	copy := r.Clone(r.Context())
	target := *r.URL
	target.Scheme = t.target.Scheme
	target.Host = t.target.Host
	copy.URL = &target
	copy.Host = t.target.Host
	return t.inner.RoundTrip(copy)
}

func TestNotificationProtocolsUseLocalMocksAndCheckBusinessFailures(t *testing.T) {
	cases := []struct {
		kind      string
		config    models.NotificationConfig
		response  string
		wantError bool
	}{
		{"telegram", models.NotificationConfig{BotToken: "123:TEST_TOKEN", ChatID: "-100123", MessageThreadID: 42}, `{"ok":true}`, false},
		{"telegram", models.NotificationConfig{BotToken: "123:TEST_TOKEN", ChatID: "1"}, `{"ok":false,"error_code":400,"description":"TEST_TOKEN secret echo"}`, true},
		{"wecom", models.NotificationConfig{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=PRIVATE_KEY"}, `{"errcode":0,"errmsg":"ok"}`, false},
		{"wecom", models.NotificationConfig{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=PRIVATE_KEY"}, `{"errcode":93000,"errmsg":"PRIVATE_KEY failed"}`, true},
		{"dingtalk", models.NotificationConfig{WebhookURL: "https://oapi.dingtalk.com/robot/send?access_token=PRIVATE_TOKEN", Secret: "SECtest"}, `{"errcode":0}`, false},
		{"dingtalk", models.NotificationConfig{WebhookURL: "https://oapi.dingtalk.com/robot/send?access_token=PRIVATE_TOKEN"}, `{"errcode":310000,"errmsg":"PRIVATE_TOKEN rejected"}`, true},
		{"webhook", models.NotificationConfig{WebhookURL: "https://127.0.0.1/receive", BearerToken: "BEARER_SECRET"}, `{"success":true}`, false},
		{"webhook", models.NotificationConfig{WebhookURL: "http://127.0.0.1/receive"}, `{"success":false,"error":"private text"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.kind+tc.response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Server-Monitor-Delivery") != "123" {
					t.Error("missing stable delivery ID")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				switch tc.kind {
				case "telegram":
					if body["chat_id"] != tc.config.ChatID || !strings.Contains(body["text"].(string), "host alert") {
						t.Errorf("unexpected Telegram payload: %#v", body)
					}
					if tc.config.MessageThreadID > 0 && body["message_thread_id"] != float64(42) {
						t.Error("topic ID lost")
					}
				case "wecom", "dingtalk":
					if body["msgtype"] != "text" || body["text"] == nil {
						t.Error("invalid robot payload")
					}
				case "webhook":
					if body["message"] != "host alert" {
						t.Error("generic payload changed")
					}
					if tc.config.BearerToken != "" && r.Header.Get("Authorization") != "Bearer BEARER_SECRET" {
						t.Error("missing bearer authorization")
					}
				}
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			local, _ := url.Parse(server.URL)
			sender := NewNotificationSender(true)
			sender.now = func() time.Time { return time.UnixMilli(1600000000000) }
			sender.http.client.Transport = localNotificationTransport{target: local, inner: server.Client().Transport, original: func(r *http.Request) {
				if tc.kind == "telegram" && r.URL.Path != "/bot123:TEST_TOKEN/sendMessage" {
					t.Error("bad Telegram endpoint")
				}
				if tc.kind == "dingtalk" && tc.config.Secret != "" {
					timestamp := r.URL.Query().Get("timestamp")
					mac := hmac.New(sha256.New, []byte(tc.config.Secret))
					_, _ = mac.Write([]byte(timestamp + "\n" + tc.config.Secret))
					if r.URL.Query().Get("sign") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
						t.Error("invalid DingTalk signature")
					}
				}
			}}
			err := sender.Send(context.Background(), models.NotificationChannel{Type: tc.kind, Config: tc.config}, json.RawMessage(`{"status":"firing","message":"host alert"}`), "123")
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v want error=%v", err, tc.wantError)
			}
			safe := SafeNotificationError(err)
			for _, secret := range []string{"PRIVATE_KEY", "PRIVATE_TOKEN", "TEST_TOKEN", "BEARER_SECRET", "private text"} {
				if strings.Contains(safe, secret) {
					t.Fatalf("error leaked secret: %s", safe)
				}
			}
		})
	}
}

func TestNotificationResponseErrorsAreNotSuccess(t *testing.T) {
	for _, kind := range []string{"telegram", "wecom", "dingtalk"} {
		for _, body := range []string{"", "{}", "invalid JSON"} {
			if checkNotificationResponse(kind, []byte(body)) == nil {
				t.Fatalf("%s accepted %q", kind, body)
			}
		}
	}
	if checkNotificationResponse("webhook", nil) != nil {
		t.Fatal("204 generic webhook should succeed")
	}
	if got := SafeNotificationError(errors.New("https://secret?token=123")); strings.Contains(got, "secret") {
		t.Fatal("raw network error escaped")
	}
}

func TestNotificationConfigPreservesAndRedactsSecrets(t *testing.T) {
	old := models.NotificationConfig{WebhookURL: "https://oapi.dingtalk.com/robot/send?access_token=secret", Secret: "SECsecret"}
	merged, err := MergeNotificationConfig("dingtalk", old, map[string]json.RawMessage{"webhook_url": json.RawMessage(`""`)}, nil)
	if err != nil || !reflect.DeepEqual(old, merged) {
		t.Fatal("blank field erased stored secret", err)
	}
	cleared, err := MergeNotificationConfig("dingtalk", old, nil, []string{"secret"})
	if err != nil || cleared.Secret != "" {
		t.Fatal("optional secret was not cleared")
	}
	if _, err = MergeNotificationConfig("dingtalk", old, nil, []string{"webhook_url"}); err == nil {
		t.Fatal("required URL cleared")
	}
	if _, err = MergeNotificationConfig("dingtalk", old, map[string]json.RawMessage{"password": json.RawMessage(`"invalid"`)}, nil); err == nil {
		t.Fatal("type-incompatible setting accepted")
	}
	public := (models.NotificationChannel{Config: old}).Public()
	raw, _ := json.Marshal(public)
	if strings.Contains(string(raw), "SECsecret") || strings.Contains(string(raw), "access_token") || len(public.SecretFields) != 2 {
		t.Fatalf("unsafe public channel: %s", raw)
	}
	key := "01234567890123456789012345678901"
	encrypted, err := models.EncryptNotificationConfig(old, key)
	if err != nil || strings.Contains(encrypted, "SECsecret") {
		t.Fatal("config not encrypted")
	}
	decoded, err := models.DecryptNotificationConfig(encrypted, key)
	if err != nil || !reflect.DeepEqual(old, decoded) {
		t.Fatal("config did not round trip")
	}
	if _, err = models.DecryptNotificationConfig(encrypted, "01234567890123456789012345678902"); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestNotificationConfigRejectsSMTPInjectionAndPlaintext(t *testing.T) {
	valid := models.NotificationConfig{SMTPHost: "smtp.example", SMTPPort: 587, TLSMode: "starttls", Username: "user", Password: "secret", From: "from@example.com", To: []string{"to@example.com"}}
	for _, change := range []func(*models.NotificationConfig){
		func(c *models.NotificationConfig) { c.From = "from@example.com\r\nBcc: injected@example.com" },
		func(c *models.NotificationConfig) { c.TLSMode = "none" },
		func(c *models.NotificationConfig) { c.To = []string{"to@example.com\nBad: x"} },
		func(c *models.NotificationConfig) { c.SMTPHost = "http://localhost" },
		func(c *models.NotificationConfig) { c.SMTPPort = 65536 },
	} {
		config := valid
		change(&config)
		if ValidateNotificationConfig("email", &config) == nil {
			t.Fatal("unsafe SMTP configuration accepted")
		}
	}
	if err := ValidateNotificationConfig("email", &valid); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationHTTPErrorRedirectAndPrivateEndpointAreRejected(t *testing.T) {
	for _, code := range []int{302, 401, 429, 500} {
		t.Run(string(rune(code)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "http://127.0.0.1/unexpected")
				w.WriteHeader(code)
			}))
			defer server.Close()
			sender := NewNotificationSender(true)
			if sender.Send(context.Background(), models.NotificationChannel{Type: "webhook", Config: models.NotificationConfig{WebhookURL: server.URL}}, json.RawMessage(`{}`), "1") == nil {
				t.Fatalf("HTTP %d accepted", code)
			}
		})
	}
	sender := NewNotificationSender(false)
	if sender.Send(context.Background(), models.NotificationChannel{Type: "webhook", Config: models.NotificationConfig{WebhookURL: "http://127.0.0.1:1"}}, json.RawMessage(`{}`), "1") == nil {
		t.Fatal("private webhook accepted")
	}
}
