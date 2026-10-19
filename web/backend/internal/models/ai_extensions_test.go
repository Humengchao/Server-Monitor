package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestAIExtensionsSkillIsolationAndValidation(t *testing.T) {
	db, user := aiProviderFixture(t)
	ctx := context.Background()
	skill := AISkill{UserID: user, Name: "  Operations  ", Description: "Managed instructions", Content: "# Inspect first\nPreserve original configuration.\n"}
	if err := SaveAISkill(ctx, db, &skill, true); err != nil {
		t.Fatal(err)
	}
	if skill.Name != "Operations" {
		t.Fatal("name not normalized")
	}
	if _, err := GetAISkill(ctx, db, skill.ID, uuid.New()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign read", err)
	}
	stale := skill
	skill.Content = "# Updated\n"
	if err := SaveAISkill(ctx, db, &skill, false); err != nil {
		t.Fatal(err)
	}
	if err := SaveAISkill(ctx, db, &stale, false); !errors.Is(err, ErrAIExtensionConflict) {
		t.Fatal("stale save accepted", err)
	}
	if err := DeleteAISkill(ctx, db, skill.ID, uuid.New()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign delete", err)
	}
	list, err := ListAISkills(ctx, db, user)
	if err != nil || len(list) != 1 || list[0].Content != skill.Content {
		t.Fatal("list/content", err)
	}
	for _, content := range []string{" ", strings.Repeat("x", AISkillContentLimit+1), "bad\x00content", string([]byte{0xff})} {
		copy := skill
		copy.Content = content
		if NormalizeAISkill(&copy) == nil {
			t.Fatal("invalid skill accepted")
		}
	}
	if err := DeleteAISkill(ctx, db, skill.ID, user); err != nil {
		t.Fatal(err)
	}
}

func TestAIExtensionsPluginEncryptionTokenBoundaryAndIsolation(t *testing.T) {
	db, user := aiProviderFixture(t)
	ctx := context.Background()
	p := AIPlugin{UserID: user, Name: "MCP", URL: "https://example.com/mcp", BearerToken: "private-fixture-token", Enabled: true}
	if err := SaveAIPlugin(ctx, db, &p, true, false); err != nil {
		t.Fatal(err)
	}
	var encrypted string
	if err := db.Raw.QueryRow(`SELECT bearer_token_encrypted FROM ai_plugins WHERE id=$1`, p.ID).Scan(&encrypted); err != nil || encrypted == "" || strings.Contains(encrypted, p.BearerToken) {
		t.Fatal("token not encrypted", err)
	}
	for _, value := range []any{p, p.Public()} {
		raw, _ := json.Marshal(value)
		if strings.Contains(string(raw), p.BearerToken) {
			t.Fatal("token leaked")
		}
	}
	p.BearerToken = ""
	p.Name = "Renamed"
	if err := SaveAIPlugin(ctx, db, &p, false, false); err != nil || p.BearerToken != "private-fixture-token" {
		t.Fatal("token not retained", err)
	}
	p.BearerToken = ""
	p.URL = "https://other.example/mcp"
	if err := SaveAIPlugin(ctx, db, &p, false, false); !errors.Is(err, ErrAIPluginEndpointToken) {
		t.Fatal("old token forwarded", err)
	}
	if err := SaveAIPlugin(ctx, db, &p, false, true); err != nil || p.BearerToken != "" || p.Public().HasBearerToken {
		t.Fatal("explicit clear failed", err)
	}
	p.BearerToken = "replacement-fixture"
	if err := SaveAIPlugin(ctx, db, &p, false, false); err != nil {
		t.Fatal(err)
	}
	stale := p
	p.BearerToken = ""
	p.Enabled = false
	if err := SaveAIPlugin(ctx, db, &p, false, false); err != nil {
		t.Fatal(err)
	}
	if err := SaveAIPlugin(ctx, db, &stale, false, false); !errors.Is(err, ErrAIExtensionConflict) {
		t.Fatal("stale save undid disable", err)
	}
	if _, err := GetAIPlugin(ctx, db, p.ID, uuid.New()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign lookup", err)
	}
	if err := DeleteAIPlugin(ctx, db, p.ID, uuid.New()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign deletion", err)
	}
	list, err := ListAIPlugins(ctx, db, uuid.New())
	if err != nil || len(list) != 0 {
		t.Fatal("foreign list", err)
	}
	if err := DeleteAIPlugin(ctx, db, p.ID, user); err != nil {
		t.Fatal(err)
	}
}

func TestAIExtensionsPluginLimitIsSerialized(t *testing.T) {
	db, user := aiProviderFixture(t)
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < MaxAIPlugins+5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := AIPlugin{UserID: user, Name: "plugin", URL: "https://example.com/mcp", Enabled: true}
			err := SaveAIPlugin(context.Background(), db, &p, true, false)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrAIExtensionLimit) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != MaxAIPlugins {
		t.Fatalf("created %d plugins", accepted.Load())
	}
}
