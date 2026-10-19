-- A panel mutation and its replay receipt commit together. A worker may lose
-- its response after committing; the action ID must never create a second rule.
CREATE TABLE IF NOT EXISTS ai_panel_alert_mutations (
    action_id UUID PRIMARY KEY REFERENCES ai_actions(id) ON DELETE CASCADE,
    result JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
