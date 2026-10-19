CREATE TABLE IF NOT EXISTS ai_tasks (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    server_id UUID NOT NULL,
    server_name TEXT NOT NULL,
    server_type TEXT NOT NULL,
    provider_id UUID NOT NULL,
    provider_name TEXT NOT NULL,
    model TEXT NOT NULL,
    request_id UUID NOT NULL,
    prompt TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    error TEXT NOT NULL DEFAULT '',
    messages JSONB NOT NULL DEFAULT '[]',
    target_fingerprint TEXT NOT NULL,
    provider_fingerprint TEXT NOT NULL,
    worker_id TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    steps INTEGER NOT NULL DEFAULT 0,
    prompt_tokens BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, request_id)
);
CREATE INDEX IF NOT EXISTS idx_ai_tasks_owner ON ai_tasks(user_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_ai_tasks_queue ON ai_tasks(status,created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_tasks_active_server ON ai_tasks(server_id)
    WHERE status IN ('queued','running','awaiting_approval','awaiting_input','cancel_requested');

CREATE TABLE IF NOT EXISTS ai_actions (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES ai_tasks(id) ON DELETE CASCADE,
    call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    arguments JSONB NOT NULL,
    summary TEXT NOT NULL,
    command TEXT NOT NULL DEFAULT '',
    requires_approval BOOLEAN NOT NULL DEFAULT FALSE,
    risk_reason TEXT NOT NULL DEFAULT '',
    mutating BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT NOT NULL,
    output TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    exit_code INTEGER,
    unknown BOOLEAN NOT NULL DEFAULT FALSE,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    backup_path TEXT NOT NULL DEFAULT '',
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    UNIQUE(task_id,call_id)
);
CREATE INDEX IF NOT EXISTS idx_ai_actions_task ON ai_actions(task_id,created_at,id);
CREATE TABLE IF NOT EXISTS ai_events (
    id BIGSERIAL PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES ai_tasks(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    content TEXT NOT NULL,
    action_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ai_events_task ON ai_events(task_id,id);
CREATE TABLE IF NOT EXISTS ai_task_messages (
    task_id UUID NOT NULL REFERENCES ai_tasks(id) ON DELETE CASCADE,
    request_id UUID NOT NULL,
    message_hash TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(task_id,request_id)
);
-- Old accepted requests did not retain a digest. Their empty marker remains
-- idempotent: return the existing task without appending or executing anything.
ALTER TABLE ai_task_messages ADD COLUMN IF NOT EXISTS message_hash TEXT NOT NULL DEFAULT '';
