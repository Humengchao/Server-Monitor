ALTER TABLE servers ADD COLUMN IF NOT EXISTS connection_mode TEXT NOT NULL DEFAULT 'ssh';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS is_public BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS monitoring_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS auto_renew_enabled BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE IF NOT EXISTS server_agents (
    server_id UUID PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    host_key TEXT NOT NULL,
    version TEXT NOT NULL DEFAULT '',
    hostname TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL DEFAULT '',
    arch TEXT NOT NULL DEFAULT '',
    run_user TEXT NOT NULL DEFAULT '',
    last_seen_at TIMESTAMPTZ,
    enrolled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS agent_enrollments (
    id UUID PRIMARY KEY,
    server_id UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ticket_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_agent_enrollments_server ON agent_enrollments(server_id);
CREATE TABLE IF NOT EXISTS configuration_imports (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    backup_id UUID NOT NULL,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(user_id,backup_id)
);
CREATE TABLE IF NOT EXISTS configuration_audit (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
