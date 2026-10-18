CREATE TABLE IF NOT EXISTS network_checks (
 id UUID PRIMARY KEY, user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name VARCHAR(128) NOT NULL, source_server_id UUID REFERENCES servers(id) ON DELETE CASCADE,
 target_server_id UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 packets INT NOT NULL DEFAULT 5 CHECK (packets BETWEEN 1 AND 10), enabled BOOLEAN NOT NULL DEFAULT TRUE,
 loss_threshold DOUBLE PRECISION NOT NULL DEFAULT 10 CHECK (loss_threshold BETWEEN 0 AND 100),
 latency_threshold_ms DOUBLE PRECISION CHECK (latency_threshold_ms >= 0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS network_checks_user ON network_checks(user_id);
CREATE TABLE IF NOT EXISTS network_samples (
 id BIGSERIAL PRIMARY KEY, check_id UUID NOT NULL REFERENCES network_checks(id) ON DELETE CASCADE,
 recorded_at TIMESTAMPTZ NOT NULL, sample JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS network_samples_time ON network_samples(check_id, recorded_at DESC);
CREATE INDEX IF NOT EXISTS network_samples_retention ON network_samples(recorded_at);
