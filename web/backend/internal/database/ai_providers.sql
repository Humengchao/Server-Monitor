CREATE TABLE IF NOT EXISTS ai_providers (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL,
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('deepseek','openai','qwen','moonshot','zhipu','siliconflow','gemini','compatible','cpa')),
    base_url VARCHAR(2048) NOT NULL,
    model VARCHAR(128) NOT NULL,
    api_key_encrypted TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(id,user_id)
);
CREATE INDEX IF NOT EXISTS ai_providers_user ON ai_providers(user_id,created_at,id);
ALTER TABLE ai_providers DROP CONSTRAINT IF EXISTS ai_providers_kind_check;
ALTER TABLE ai_providers ADD CONSTRAINT ai_providers_kind_check CHECK (kind IN ('deepseek','openai','qwen','moonshot','zhipu','siliconflow','gemini','compatible','cpa'));
