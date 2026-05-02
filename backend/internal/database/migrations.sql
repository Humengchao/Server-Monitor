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
    last_seen_at TIMESTAMPTZ,
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

-- Add system info columns to servers
ALTER TABLE servers ADD COLUMN IF NOT EXISTS cpu_cores INT DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS memory_total_bytes BIGINT DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS disk_total_bytes BIGINT DEFAULT 0;

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
