CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY,
    username VARCHAR(64) UNIQUE NOT NULL,
    password_hash VARCHAR(256) NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS servers (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL,
    host VARCHAR(256) NOT NULL,
    port INT DEFAULT 22,
    ssh_username VARCHAR(128) NOT NULL DEFAULT 'root',
    ssh_password TEXT DEFAULT '',
    ssh_key TEXT DEFAULT '',
    ssh_host_key TEXT DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tags (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(64) NOT NULL,
    color VARCHAR(7) DEFAULT '#1890ff',
    UNIQUE(user_id, name)
);

CREATE TABLE IF NOT EXISTS server_tags (
    server_id UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    tag_id UUID NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (server_id, tag_id)
);

CREATE TABLE IF NOT EXISTS server_metrics (
    id BIGSERIAL PRIMARY KEY,
    server_id UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    cpu_percent DECIMAL(5,2),
    memory_used BIGINT,
    memory_total BIGINT,
    network_rx_bytes BIGINT,
    network_tx_bytes BIGINT,
    uptime_seconds BIGINT,
    recorded_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_metrics_server_time ON server_metrics(server_id, recorded_at DESC);
CREATE INDEX IF NOT EXISTS idx_servers_user ON servers(user_id);

-- Normalize legacy connection fields that may have been pasted with tabs or
-- line breaks. Secrets are intentionally never trimmed because whitespace can
-- be part of a valid password or private key.
UPDATE servers
SET host = REGEXP_REPLACE(host, '^[[:space:]]+|[[:space:]]+$', '', 'g'),
    ssh_username = REGEXP_REPLACE(ssh_username, '^[[:space:]]+|[[:space:]]+$', '', 'g')
WHERE host ~ '^[[:space:]]|[[:space:]]$'
   OR ssh_username ~ '^[[:space:]]|[[:space:]]$';

-- Add system info columns to servers
ALTER TABLE servers ADD COLUMN IF NOT EXISTS cpu_cores INT DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS memory_total_bytes BIGINT DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS disk_total_bytes BIGINT DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS has_docker BOOLEAN DEFAULT FALSE;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS docker_version VARCHAR(32) DEFAULT '';

-- Add disk I/O columns to server_metrics
ALTER TABLE server_metrics ADD COLUMN IF NOT EXISTS disk_rx_bytes BIGINT DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN IF NOT EXISTS disk_tx_bytes BIGINT DEFAULT 0;

-- Login history
CREATE TABLE IF NOT EXISTS login_history (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ip VARCHAR(64),
    user_agent TEXT,
    success BOOLEAN NOT NULL DEFAULT FALSE,
    logged_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_login_history_user_time ON login_history(user_id, logged_at DESC);

-- SSH credentials (encrypted)
CREATE TABLE IF NOT EXISTS credentials (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL,
    ssh_username VARCHAR(128) NOT NULL DEFAULT 'root',
    ssh_password TEXT DEFAULT '',
    ssh_key TEXT DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_credentials_user ON credentials(user_id);

UPDATE credentials
SET ssh_username = REGEXP_REPLACE(ssh_username, '^[[:space:]]+|[[:space:]]+$', '', 'g')
WHERE ssh_username ~ '^[[:space:]]|[[:space:]]$';

-- Link servers to credentials
ALTER TABLE servers ADD COLUMN IF NOT EXISTS credential_id UUID REFERENCES credentials(id) ON DELETE SET NULL;

-- Older API versions accepted arbitrary credential UUIDs. Remove any legacy
-- cross-owner link before the collector starts resolving secrets.
UPDATE servers s
SET credential_id = NULL
WHERE credential_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM credentials c
      WHERE c.id = s.credential_id AND c.user_id = s.user_id
  );

-- Server expiration date and notes
ALTER TABLE servers ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS notes TEXT DEFAULT '';


-- Server type (linux / windows)
ALTER TABLE servers ADD COLUMN IF NOT EXISTS server_type VARCHAR(16) DEFAULT 'linux';

-- Optional public-status commercial metadata. These values never contain
-- connection details and are only used to render plan/expiry information.
ALTER TABLE servers ADD COLUMN IF NOT EXISTS billing_price DECIMAL(12,2) DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS billing_currency VARCHAR(8) DEFAULT 'CNY';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS billing_cycle VARCHAR(16) DEFAULT 'year';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS traffic_limit_bytes BIGINT DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS public_location VARCHAR(128) DEFAULT '';

-- Rich metrics used by the public probe dashboard.
ALTER TABLE server_metrics ADD COLUMN IF NOT EXISTS load_1 DECIMAL(8,2) DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN IF NOT EXISTS load_5 DECIMAL(8,2) DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN IF NOT EXISTS load_15 DECIMAL(8,2) DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN IF NOT EXISTS disk_used_bytes BIGINT DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN IF NOT EXISTS network_rx_total_bytes BIGINT DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN IF NOT EXISTS network_tx_total_bytes BIGINT DEFAULT 0;
ALTER TABLE server_metrics ADD COLUMN IF NOT EXISTS latency_ms INT DEFAULT 0;

-- Latest metrics are updated in-place on every poll. Keeping this hot path in
-- a one-row-per-server table avoids scanning the raw history for dashboards.
CREATE TABLE IF NOT EXISTS server_latest_metrics (
    server_id UUID PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    cpu_percent DECIMAL(5,2) DEFAULT 0,
    load_1 DECIMAL(8,2) DEFAULT 0,
    load_5 DECIMAL(8,2) DEFAULT 0,
    load_15 DECIMAL(8,2) DEFAULT 0,
    memory_used BIGINT DEFAULT 0,
    memory_total BIGINT DEFAULT 0,
    disk_used_bytes BIGINT DEFAULT 0,
    network_rx_bytes BIGINT DEFAULT 0,
    network_tx_bytes BIGINT DEFAULT 0,
    network_rx_total_bytes BIGINT DEFAULT 0,
    network_tx_total_bytes BIGINT DEFAULT 0,
    disk_rx_bytes BIGINT DEFAULT 0,
    disk_tx_bytes BIGINT DEFAULT 0,
    uptime_seconds BIGINT DEFAULT 0,
    latency_ms INT DEFAULT 0,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Tiered rollups retain useful trends without keeping every three-second
-- sample forever. Their schemas intentionally mirror server_metrics so the
-- history API can merge all tiers transparently.
CREATE TABLE IF NOT EXISTS server_metrics_1m (
    server_id UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    cpu_percent DECIMAL(5,2) DEFAULT 0,
    load_1 DECIMAL(8,2) DEFAULT 0,
    load_5 DECIMAL(8,2) DEFAULT 0,
    load_15 DECIMAL(8,2) DEFAULT 0,
    memory_used BIGINT DEFAULT 0,
    memory_total BIGINT DEFAULT 0,
    disk_used_bytes BIGINT DEFAULT 0,
    network_rx_bytes BIGINT DEFAULT 0,
    network_tx_bytes BIGINT DEFAULT 0,
    network_rx_total_bytes BIGINT DEFAULT 0,
    network_tx_total_bytes BIGINT DEFAULT 0,
    disk_rx_bytes BIGINT DEFAULT 0,
    disk_tx_bytes BIGINT DEFAULT 0,
    uptime_seconds BIGINT DEFAULT 0,
    latency_ms INT DEFAULT 0,
    sample_count INT NOT NULL DEFAULT 0,
    recorded_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (server_id, recorded_at)
);

CREATE TABLE IF NOT EXISTS server_metrics_15m (
    server_id UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    cpu_percent DECIMAL(5,2) DEFAULT 0,
    load_1 DECIMAL(8,2) DEFAULT 0,
    load_5 DECIMAL(8,2) DEFAULT 0,
    load_15 DECIMAL(8,2) DEFAULT 0,
    memory_used BIGINT DEFAULT 0,
    memory_total BIGINT DEFAULT 0,
    disk_used_bytes BIGINT DEFAULT 0,
    network_rx_bytes BIGINT DEFAULT 0,
    network_tx_bytes BIGINT DEFAULT 0,
    network_rx_total_bytes BIGINT DEFAULT 0,
    network_tx_total_bytes BIGINT DEFAULT 0,
    disk_rx_bytes BIGINT DEFAULT 0,
    disk_tx_bytes BIGINT DEFAULT 0,
    uptime_seconds BIGINT DEFAULT 0,
    latency_ms INT DEFAULT 0,
    sample_count INT NOT NULL DEFAULT 0,
    recorded_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (server_id, recorded_at)
);

-- BRIN is compact and well suited to append-only time-series data. The
-- existing btree indexes continue to serve per-server latest/range lookups.
CREATE INDEX IF NOT EXISTS idx_server_metrics_recorded_brin ON server_metrics USING BRIN (recorded_at);
CREATE INDEX IF NOT EXISTS idx_server_metrics_1m_recorded_brin ON server_metrics_1m USING BRIN (recorded_at);
CREATE INDEX IF NOT EXISTS idx_server_metrics_15m_recorded_brin ON server_metrics_15m USING BRIN (recorded_at);

-- Prevent an expensive historical backfill from running on every restart.
CREATE TABLE IF NOT EXISTS metric_maintenance_state (
    name VARCHAR(64) PRIMARY KEY,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Credential type (linux / windows)
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS credential_type VARCHAR(16) DEFAULT 'linux';

-- last_seen_at was never written; liveness comes from
-- server_latest_metrics.recorded_at instead.
ALTER TABLE servers DROP COLUMN IF EXISTS last_seen_at;
