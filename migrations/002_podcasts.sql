CREATE TABLE IF NOT EXISTS podcasts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rss_url      TEXT UNIQUE NOT NULL,
    title        TEXT NOT NULL,
    author       TEXT,
    image_url    TEXT,
    description  TEXT,
    last_fetched TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS episodes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    podcast_id  UUID NOT NULL REFERENCES podcasts(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    audio_url   TEXT NOT NULL,
    duration    INTEGER, -- seconds
    description TEXT,
    pub_date    TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(podcast_id, audio_url)
);

CREATE INDEX IF NOT EXISTS idx_episodes_podcast_id ON episodes(podcast_id);
CREATE INDEX IF NOT EXISTS idx_episodes_pub_date ON episodes(pub_date DESC);
