package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func TestAIProviderDefaultsAndURLValidation(t *testing.T) {
	provider := AIProvider{Name: "  DeepSeek  ", Kind: "deepseek", APIKey: "sk-secret"}
	if err := NormalizeAIProvider(&provider); err != nil {
		t.Fatal(err)
	}
	if provider.Name != "DeepSeek" || provider.BaseURL != DefaultDeepSeekBaseURL || provider.Model != DefaultDeepSeekModel {
		t.Fatal("wrong defaults", provider.Public())
	}
	for _, raw := range []string{"http://api.example/v1", "https://user:pass@api.example/v1", "https://api.example/v1?api_key=secret", "https://api.example/#fragment", "https://api.example/v1/../other", "https://api.example/a%2fb", "https://api.example:99999", "https://api.example:", "https://api.example/line\nbreak", "https://api.example\\@private/", "https://[fe80::1%25eth0]", "https://bad_host/v1", "https://exa mple/v1"} {
		if _, err := ParseAIBaseURL(raw); err == nil {
			t.Fatalf("unsafe endpoint accepted: %q", raw)
		}
	}
	for _, raw := range []string{"https://api.example", "https://api.example/v1/", "https://api.example/custom/api", "https://api.example:8443/v1", "https://[2606:4700:4700::1111]/v1"} {
		if _, err := ParseAIBaseURL(raw); err != nil {
			t.Fatalf("valid URL rejected: %s %v", raw, err)
		}
	}
	for _, secret := range []string{"key\r\nAuthorization: x", " key", strings.Repeat("a", 4097)} {
		copy := provider
		copy.APIKey = secret
		if NormalizeAIProvider(&copy) == nil {
			t.Fatal("invalid API key accepted")
		}
	}
	encoded, _ := json.Marshal(provider)
	public, _ := json.Marshal(provider.Public())
	if strings.Contains(string(encoded), "sk-secret") || strings.Contains(string(public), "sk-secret") || !strings.Contains(string(public), `"has_api_key":true`) {
		t.Fatal("API key leaked or missing flag")
	}
}

func aiProviderFixture(t *testing.T) (*DB, uuid.UUID) {
	t.Helper()
	raw := testdb.Open(t)
	user, _, _ := testdb.Seed(t, raw)
	return &DB{Raw: raw, EncryptionKey: "0123456789abcdef0123456789abcdef"}, user
}
func TestAIProviderCRUDEncryptionAndEndpointKeyBoundary(t *testing.T) {
	db, user := aiProviderFixture(t)
	ctx := context.Background()
	provider := AIProvider{UserID: user, Name: "First", Kind: "deepseek", APIKey: "original-secret", Enabled: true}
	if err := SaveAIProvider(ctx, db, &provider, true); err != nil {
		t.Fatal(err)
	}
	var encrypted string
	if err := db.Raw.QueryRow(`SELECT api_key_encrypted FROM ai_providers WHERE id=$1`, provider.ID).Scan(&encrypted); err != nil || strings.Contains(encrypted, "original-secret") {
		t.Fatal("key was not encrypted", err)
	}
	saved, err := GetAIProvider(ctx, db, provider.ID, user)
	if err != nil || saved.APIKey != "original-secret" {
		t.Fatal("stored key unavailable", err)
	}
	saved.APIKey = ""
	saved.Name = "Renamed"
	if err := SaveAIProvider(ctx, db, &saved, false); err != nil || saved.APIKey != "original-secret" {
		t.Fatal("empty key did not preserve", err)
	}
	saved.APIKey = ""
	saved.BaseURL += "/v1"
	if err := SaveAIProvider(ctx, db, &saved, false); !errors.Is(err, ErrAIProviderEndpointKey) {
		t.Fatal("path change reused old key", err)
	}
	current, err := GetAIProvider(ctx, db, provider.ID, user)
	if err != nil || current.BaseURL != DefaultDeepSeekBaseURL {
		t.Fatal("rejected update partially wrote", err)
	}
	saved.APIKey = "replacement-secret"
	if err := SaveAIProvider(ctx, db, &saved, false); err != nil {
		t.Fatal(err)
	}
	stale := current
	stale.APIKey = ""
	stale.Name = "stale edit"
	if err := SaveAIProvider(ctx, db, &stale, false); !errors.Is(err, ErrAIProviderEndpointKey) {
		t.Fatal("concurrent endpoint change escaped key requirement", err)
	}
	foreign := uuid.New()
	if _, err := GetAIProvider(ctx, db, provider.ID, foreign); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign read", err)
	}
	if err := DeleteAIProvider(ctx, db, provider.ID, foreign); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign delete", err)
	}
	list, err := ListAIProviders(ctx, db, user)
	if err != nil || len(list) != 1 || list[0].APIKey != "replacement-secret" {
		t.Fatal("list failed", err)
	}
	if err := DeleteAIProvider(ctx, db, provider.ID, user); err != nil {
		t.Fatal(err)
	}
	if err := DeleteAIProvider(ctx, db, provider.ID, user); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing delete", err)
	}
}

func TestAIProviderAccountLimitSerializesConcurrentCreates(t *testing.T) {
	db, user := aiProviderFixture(t)
	ctx := context.Background()
	for i := 0; i < 9; i++ {
		provider := AIProvider{UserID: user, Name: "Existing", Kind: "deepseek", APIKey: "secret", Enabled: true}
		if err := SaveAIProvider(ctx, db, &provider, true); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			p := AIProvider{UserID: user, Name: "Concurrent", Kind: "deepseek", APIKey: "secret", Enabled: true}
			results <- SaveAIProvider(ctx, db, &p, true)
		}()
	}
	group.Wait()
	close(results)
	successes, limited := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrAIProviderLimit) {
			limited++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || limited != 1 {
		t.Fatal("limit raced", successes, limited)
	}
	list, err := ListAIProviders(ctx, db, user)
	if err != nil || len(list) != MaxAIProviders {
		t.Fatal("wrong provider count", len(list), err)
	}
}

func TestAIProviderStaleUpdateCannotReenableOrRevertRotatedKey(t *testing.T) {
	db, user := aiProviderFixture(t)
	ctx := context.Background()
	provider := AIProvider{UserID: user, Name: "original", Kind: "deepseek", APIKey: "original-secret", Enabled: true}
	if err := SaveAIProvider(ctx, db, &provider, true); err != nil {
		t.Fatal(err)
	}
	stale := provider
	provider.Enabled = false
	provider.APIKey = "rotated-secret"
	if err := SaveAIProvider(ctx, db, &provider, false); err != nil {
		t.Fatal(err)
	}
	stale.Name = "slow stale edit"
	if err := SaveAIProvider(ctx, db, &stale, false); err == nil {
		t.Fatal("stale save reenabled a disabled provider and reverted its key")
	}
	stored, err := GetAIProvider(ctx, db, provider.ID, user)
	if err != nil || stored.Enabled || stored.APIKey != "rotated-secret" || stored.Name != "original" {
		t.Fatal("concurrent configuration lost", stored.Public(), err)
	}
}
