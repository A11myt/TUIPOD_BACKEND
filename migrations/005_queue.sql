CREATE TABLE IF NOT EXISTS queue (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    episode_id UUID NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL DEFAULT 0,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, episode_id)
);

CREATE INDEX IF NOT EXISTS idx_queue_user_id_position ON queue(user_id, position);
