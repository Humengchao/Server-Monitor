-- Channel configuration (including webhook URLs) is encrypted with the
-- application's encryption key. Delivery rows contain no credentials.
CREATE TABLE IF NOT EXISTS notification_channels (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL,
    type VARCHAR(16) NOT NULL CHECK (type IN ('telegram','wecom','dingtalk','email','webhook')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    config_encrypted TEXT NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(id,user_id)
);
CREATE INDEX IF NOT EXISTS notification_channels_user ON notification_channels(user_id,created_at,id);

CREATE TABLE IF NOT EXISTS alert_rule_channels (
    rule_id UUID NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    channel_id UUID NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
    PRIMARY KEY(rule_id,channel_id)
);

CREATE TABLE IF NOT EXISTS notification_deliveries (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_id UUID REFERENCES notification_channels(id) ON DELETE SET NULL,
    channel_name VARCHAR(128) NOT NULL,
    channel_type VARCHAR(16) NOT NULL,
    event_id BIGINT REFERENCES alert_events(id) ON DELETE SET NULL,
    rule_id UUID REFERENCES alert_rules(id) ON DELETE SET NULL,
    server_id UUID REFERENCES servers(id) ON DELETE SET NULL,
    source_key TEXT NOT NULL DEFAULT '',
    transition VARCHAR(16) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending','sending','retry','succeeded','failed','canceled')),
    attempts INT NOT NULL DEFAULT 0,
    lease_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attempt_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,
    last_error VARCHAR(256) NOT NULL DEFAULT '',
    retry_of BIGINT REFERENCES notification_deliveries(id) ON DELETE SET NULL
);
ALTER TABLE notification_deliveries ADD COLUMN IF NOT EXISTS source_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS notification_delivery_episode
    ON notification_deliveries(event_id,channel_id,transition) WHERE retry_of IS NULL AND event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS notification_delivery_user_page ON notification_deliveries(user_id,id DESC);
CREATE INDEX IF NOT EXISTS notification_delivery_due ON notification_deliveries(next_attempt_at,id)
    WHERE status IN ('pending','retry','sending');
CREATE INDEX IF NOT EXISTS notification_delivery_source_order ON notification_deliveries(channel_id,source_key,rule_id,server_id,(COALESCE(retry_of,id)),id)
    WHERE status IN ('pending','retry','sending');
