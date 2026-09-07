CREATE TABLE IF NOT EXISTS progress (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    episode_id       UUID NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    position_seconds INTEGER NOT NULL DEFAULT 0,
    completed        BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, episode_id)
);

CREATE INDEX IF NOT EXISTS idx_progress_user_id ON progress(user_id);
