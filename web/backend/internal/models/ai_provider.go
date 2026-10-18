package models

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"server-monitor/internal/crypto"
)

const MaxAIProviders = 10
const DefaultDeepSeekBaseURL = "https://api.deepseek.com"
const DefaultDeepSeekModel = "deepseek-flash"

var ErrAIProviderLimit = errors.New("at most 10 AI providers per account")
var ErrAIProviderEndpointKey = errors.New("changing the provider base URL requires a newly supplied API key")
var ErrAIProviderKey = errors.New("an API key is required")
var ErrAIProviderDisabled = errors.New("AI provider is disabled")

type AIProvider struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"-"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	BaseURL   string    `json:"base_url"`
	Model     string    `json:"model"`
	APIKey    string    `json:"-"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PublicAIProvider struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	BaseURL   string    `json:"base_url"`
	Model     string    `json:"model"`
	HasAPIKey bool      `json:"has_api_key"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (p AIProvider) Public() PublicAIProvider {
	return PublicAIProvider{
		ID: p.ID, Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, Model: p.Model, HasAPIKey: p.APIKey != "",
		Enabled: p.Enabled, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// NormalizeAIProvider checks only syntax and size. The HTTP client separately
// validates DNS on every call and dials only that approved result.
func NormalizeAIProvider(p *AIProvider) error {
	p.Name = strings.TrimSpace(p.Name)
	p.Kind = strings.TrimSpace(p.Kind)
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	p.Model = strings.TrimSpace(p.Model)
	if p.Name == "" || len(p.Name) > 128 || strings.ContainsFunc(p.Name, unicode.IsControl) {
		return errors.New("provider name must be 1 to 128 bytes without control characters")
	}
	if p.Kind != "deepseek" && p.Kind != "compatible" {
		return errors.New("provider kind must be deepseek or compatible")
	}
	if p.Kind == "deepseek" {
		if p.BaseURL == "" {
			p.BaseURL = DefaultDeepSeekBaseURL
		}
		if p.Model == "" {
			p.Model = DefaultDeepSeekModel
		}
	}
	if _, err := ParseAIBaseURL(p.BaseURL); err != nil {
		return err
	}
	if p.Model == "" || len(p.Model) > 128 || strings.ContainsFunc(p.Model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("model must be 1 to 128 bytes without whitespace")
	}
	if len(p.APIKey) > 4096 || strings.ContainsFunc(p.APIKey, func(r rune) bool { return r < 33 || r > 126 }) {
		return errors.New("API key must contain at most 4096 visible ASCII characters")
	}
	return nil
}

func ParseAIBaseURL(raw string) (*url.URL, error) {
	invalid := errors.New("base URL must be an HTTPS URL without credentials, query, fragment, or ambiguous path")
	if raw == "" || len(raw) > 2048 || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '\\' }) {
		return nil, invalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return nil, invalid
	}
	if strings.Contains(u.Hostname(), "%") || strings.Contains(u.Hostname(), "_") {
		return nil, invalid
	}
	if net.ParseIP(u.Hostname()) == nil {
		hostname := strings.TrimSuffix(u.Hostname(), ".")
		if len(hostname) > 253 {
			return nil, invalid
		}
		for _, label := range strings.Split(hostname, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, invalid
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
					return nil, invalid
				}
			}
		}
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, invalid
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, invalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, invalid
		}
	}
	return u, nil
}

const aiProviderSelect = `SELECT id,user_id,name,kind,base_url,model,api_key_encrypted,enabled,created_at,updated_at FROM ai_providers`

type aiProviderScanner interface{ Scan(...any) error }

func scanAIProvider(row aiProviderScanner, key string) (AIProvider, error) {
	var provider AIProvider
	var encrypted string
	if err := row.Scan(&provider.ID, &provider.UserID, &provider.Name, &provider.Kind, &provider.BaseURL, &provider.Model, &encrypted, &provider.Enabled, &provider.CreatedAt, &provider.UpdatedAt); err != nil {
		return provider, err
	}
	plain, err := crypto.Decrypt(encrypted, key)
	if err != nil {
		return AIProvider{}, errors.New("cannot decrypt AI provider API key")
	}
	provider.APIKey = plain
	return provider, nil
}
func GetAIProvider(ctx context.Context, db *DB, id, userID uuid.UUID) (AIProvider, error) {
	return scanAIProvider(db.Raw.QueryRowContext(ctx, aiProviderSelect+` WHERE id=$1 AND user_id=$2`, id, userID), db.EncryptionKey)
}
func ListAIProviders(ctx context.Context, db *DB, userID uuid.UUID) ([]AIProvider, error) {
	rows, err := db.Raw.QueryContext(ctx, aiProviderSelect+` WHERE user_id=$1 ORDER BY created_at,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	providers := []AIProvider{}
	for rows.Next() {
		p, err := scanAIProvider(rows, db.EncryptionKey)
		if err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// APIKey is newly submitted input on save, not a copied decrypted old key.
// Empty retains the stored key only if the complete base URL is unchanged.
// Serialize all provider mutations by account to enforce the ten-row limit.
func SaveAIProvider(ctx context.Context, db *DB, p *AIProvider, create bool) error {
	if err := NormalizeAIProvider(p); err != nil {
		return err
	}
	tx, err := db.Raw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, p.UserID).Scan(&owner); err != nil {
		return err
	}
	encrypted := ""
	if create {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_providers WHERE user_id=$1`, p.UserID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxAIProviders {
			return ErrAIProviderLimit
		}
		if p.APIKey == "" {
			return ErrAIProviderKey
		}
		p.ID = uuid.New()
	} else {
		var oldURL string
		if err = tx.QueryRowContext(ctx, `SELECT base_url,api_key_encrypted FROM ai_providers WHERE id=$1 AND user_id=$2 FOR UPDATE`, p.ID, p.UserID).Scan(&oldURL, &encrypted); err != nil {
			return err
		}
		if p.BaseURL != oldURL && p.APIKey == "" {
			return ErrAIProviderEndpointKey
		}
	}
	if p.APIKey != "" {
		encrypted, err = crypto.Encrypt(p.APIKey, db.EncryptionKey)
		if err != nil {
			return err
		}
	}
	if encrypted == "" {
		return ErrAIProviderKey
	}
	if create {
		err = tx.QueryRowContext(ctx, `INSERT INTO ai_providers(id,user_id,name,kind,base_url,model,api_key_encrypted,enabled)
            VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at,updated_at`, p.ID, p.UserID, p.Name, p.Kind, p.BaseURL, p.Model, encrypted, p.Enabled).Scan(&p.CreatedAt, &p.UpdatedAt)
	} else {
		err = tx.QueryRowContext(ctx, `UPDATE ai_providers SET name=$3,kind=$4,base_url=$5,model=$6,api_key_encrypted=$7,enabled=$8,updated_at=NOW()
            WHERE id=$1 AND user_id=$2 RETURNING created_at,updated_at`, p.ID, p.UserID, p.Name, p.Kind, p.BaseURL, p.Model, encrypted, p.Enabled).Scan(&p.CreatedAt, &p.UpdatedAt)
	}
	if err != nil {
		return err
	}
	// Keep Public().HasAPIKey accurate without returning ciphertext or a
	// placeholder that could accidentally be used as a bearer token.
	p.APIKey, err = crypto.Decrypt(encrypted, db.EncryptionKey)
	if err != nil {
		return errors.New("cannot decrypt AI provider API key")
	}
	return tx.Commit()
}
func DeleteAIProvider(ctx context.Context, db *DB, id, userID uuid.UUID) error {
	result, err := db.Raw.ExecContext(ctx, `DELETE FROM ai_providers WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}
