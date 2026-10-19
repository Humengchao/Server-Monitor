package models

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"server-monitor/internal/crypto"
)

const MaxAISkills = 50
const MaxAIPlugins = 10
const AISkillContentLimit = 32 << 10

var ErrAIExtensionLimit = errors.New("account extension limit reached")
var ErrAIExtensionConflict = errors.New("extension changed; reload before saving")
var ErrAIPluginEndpointToken = errors.New("changing the plugin URL requires a newly supplied token or clear_token=true")
var ErrAIPluginDisabled = errors.New("MCP plugin is disabled")

type AISkill struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"-"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AIPlugin struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"-"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	BearerToken string    `json:"-"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type PublicAIPlugin struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	HasBearerToken bool      `json:"has_bearer_token"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (p AIPlugin) Public() PublicAIPlugin {
	return PublicAIPlugin{p.ID, p.Name, p.URL, p.BearerToken != "", p.Enabled, p.CreatedAt, p.UpdatedAt}
}

func validAIExtensionName(name string) bool {
	return name != "" && len(name) <= 128 && utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl)
}
func NormalizeAISkill(s *AISkill) error {
	s.Name = strings.TrimSpace(s.Name)
	if !validAIExtensionName(s.Name) {
		return errors.New("skill name must be 1 to 128 UTF-8 bytes without control characters")
	}
	if len(s.Description) > 2048 || !utf8.ValidString(s.Description) || strings.ContainsRune(s.Description, 0) {
		return errors.New("skill description must be UTF-8 of at most 2048 bytes")
	}
	if strings.TrimSpace(s.Content) == "" || len(s.Content) > AISkillContentLimit || !utf8.ValidString(s.Content) || strings.ContainsRune(s.Content, 0) {
		return errors.New("skill content must be nonempty UTF-8 Markdown of at most 32 KiB")
	}
	return nil
}
func NormalizeAIPlugin(p *AIPlugin) error {
	p.Name = strings.TrimSpace(p.Name)
	p.URL = strings.TrimSpace(p.URL)
	if !validAIExtensionName(p.Name) {
		return errors.New("plugin name must be 1 to 128 UTF-8 bytes without control characters")
	}
	if _, err := ParseAIBaseURL(p.URL); err != nil {
		return err
	}
	if len(p.BearerToken) > 4096 || strings.ContainsFunc(p.BearerToken, func(r rune) bool { return r < 33 || r > 126 }) {
		return errors.New("bearer token must contain at most 4096 visible ASCII characters")
	}
	return nil
}

type aiExtensionScanner interface{ Scan(...any) error }

const aiSkillSelect = `SELECT id,user_id,name,description,content,created_at,updated_at FROM ai_skills`

func scanAISkill(row aiExtensionScanner) (s AISkill, err error) {
	err = row.Scan(&s.ID, &s.UserID, &s.Name, &s.Description, &s.Content, &s.CreatedAt, &s.UpdatedAt)
	return
}
func GetAISkill(ctx context.Context, db *DB, id, user uuid.UUID) (AISkill, error) {
	return scanAISkill(db.Raw.QueryRowContext(ctx, aiSkillSelect+` WHERE id=$1 AND user_id=$2`, id, user))
}
func ListAISkills(ctx context.Context, db *DB, user uuid.UUID) ([]AISkill, error) {
	rows, err := db.Raw.QueryContext(ctx, aiSkillSelect+` WHERE user_id=$1 ORDER BY created_at,id`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AISkill{}
	for rows.Next() {
		s, err := scanAISkill(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}
func SaveAISkill(ctx context.Context, db *DB, s *AISkill, create bool) error {
	if err := NormalizeAISkill(s); err != nil {
		return err
	}
	tx, err := db.Raw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, s.UserID).Scan(&owner); err != nil {
		return err
	}
	if create {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_skills WHERE user_id=$1`, s.UserID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxAISkills {
			return ErrAIExtensionLimit
		}
		s.ID = uuid.New()
		err = tx.QueryRowContext(ctx, `INSERT INTO ai_skills(id,user_id,name,description,content) VALUES($1,$2,$3,$4,$5) RETURNING created_at,updated_at`, s.ID, s.UserID, s.Name, s.Description, s.Content).Scan(&s.CreatedAt, &s.UpdatedAt)
	} else {
		var previous time.Time
		if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM ai_skills WHERE id=$1 AND user_id=$2 FOR UPDATE`, s.ID, s.UserID).Scan(&previous); err != nil {
			return err
		}
		if !previous.Equal(s.UpdatedAt) {
			return ErrAIExtensionConflict
		}
		err = tx.QueryRowContext(ctx, `UPDATE ai_skills SET name=$3,description=$4,content=$5,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1 AND user_id=$2 RETURNING created_at,updated_at`, s.ID, s.UserID, s.Name, s.Description, s.Content).Scan(&s.CreatedAt, &s.UpdatedAt)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func DeleteAISkill(ctx context.Context, db *DB, id, user uuid.UUID) error {
	return deleteAIExtension(ctx, db, `DELETE FROM ai_skills WHERE id=$1 AND user_id=$2`, id, user)
}

const aiPluginSelect = `SELECT id,user_id,name,url,bearer_token_encrypted,enabled,created_at,updated_at FROM ai_plugins`

func scanAIPlugin(row aiExtensionScanner, key string) (p AIPlugin, err error) {
	var encrypted string
	err = row.Scan(&p.ID, &p.UserID, &p.Name, &p.URL, &encrypted, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return
	}
	if encrypted != "" {
		p.BearerToken, err = crypto.Decrypt(encrypted, key)
		if err != nil {
			return AIPlugin{}, errors.New("cannot decrypt MCP plugin token")
		}
	}
	return
}
func GetAIPlugin(ctx context.Context, db *DB, id, user uuid.UUID) (AIPlugin, error) {
	return scanAIPlugin(db.Raw.QueryRowContext(ctx, aiPluginSelect+` WHERE id=$1 AND user_id=$2`, id, user), db.EncryptionKey)
}
func ListAIPlugins(ctx context.Context, db *DB, user uuid.UUID) ([]AIPlugin, error) {
	rows, err := db.Raw.QueryContext(ctx, aiPluginSelect+` WHERE user_id=$1 ORDER BY created_at,id`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AIPlugin{}
	for rows.Next() {
		p, err := scanAIPlugin(rows, db.EncryptionKey)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// Empty input preserves the existing token. Clearing it is an explicit action;
// an endpoint change can never forward a previously stored token implicitly.
func SaveAIPlugin(ctx context.Context, db *DB, p *AIPlugin, create, clearToken bool) error {
	if err := NormalizeAIPlugin(p); err != nil {
		return err
	}
	if clearToken && p.BearerToken != "" {
		return errors.New("cannot set and clear a bearer token together")
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
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_plugins WHERE user_id=$1`, p.UserID).Scan(&count); err != nil {
			return err
		}
		if count >= MaxAIPlugins {
			return ErrAIExtensionLimit
		}
		p.ID = uuid.New()
	} else {
		var oldURL string
		var previous time.Time
		if err = tx.QueryRowContext(ctx, `SELECT url,bearer_token_encrypted,updated_at FROM ai_plugins WHERE id=$1 AND user_id=$2 FOR UPDATE`, p.ID, p.UserID).Scan(&oldURL, &encrypted, &previous); err != nil {
			return err
		}
		if !previous.Equal(p.UpdatedAt) {
			return ErrAIExtensionConflict
		}
		if oldURL != p.URL && encrypted != "" && p.BearerToken == "" && !clearToken {
			return ErrAIPluginEndpointToken
		}
	}
	if clearToken {
		encrypted = ""
	} else if p.BearerToken != "" {
		encrypted, err = crypto.Encrypt(p.BearerToken, db.EncryptionKey)
		if err != nil {
			return err
		}
	}
	if create {
		err = tx.QueryRowContext(ctx, `INSERT INTO ai_plugins(id,user_id,name,url,bearer_token_encrypted,enabled) VALUES($1,$2,$3,$4,$5,$6) RETURNING created_at,updated_at`, p.ID, p.UserID, p.Name, p.URL, encrypted, p.Enabled).Scan(&p.CreatedAt, &p.UpdatedAt)
	} else {
		err = tx.QueryRowContext(ctx, `UPDATE ai_plugins SET name=$3,url=$4,bearer_token_encrypted=$5,enabled=$6,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1 AND user_id=$2 RETURNING created_at,updated_at`, p.ID, p.UserID, p.Name, p.URL, encrypted, p.Enabled).Scan(&p.CreatedAt, &p.UpdatedAt)
	}
	if err != nil {
		return err
	}
	p.BearerToken = ""
	if encrypted != "" {
		p.BearerToken, err = crypto.Decrypt(encrypted, db.EncryptionKey)
		if err != nil {
			return errors.New("cannot decrypt MCP plugin token")
		}
	}
	return tx.Commit()
}
func DeleteAIPlugin(ctx context.Context, db *DB, id, user uuid.UUID) error {
	return deleteAIExtension(ctx, db, `DELETE FROM ai_plugins WHERE id=$1 AND user_id=$2`, id, user)
}
func deleteAIExtension(ctx context.Context, db *DB, query string, id, user uuid.UUID) error {
	result, err := db.Raw.ExecContext(ctx, query, id, user)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
