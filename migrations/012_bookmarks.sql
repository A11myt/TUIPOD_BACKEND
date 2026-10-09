CREATE TABLE IF NOT EXISTS bookmarks (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    episode_id       UUID NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    position_seconds INTEGER NOT NULL,
    note             TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bookmarks_user_id ON bookmarks(user_id);
CREATE INDEX IF NOT EXISTS idx_bookmarks_episode_id ON bookmarks(episode_id);
