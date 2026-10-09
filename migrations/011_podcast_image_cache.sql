-- image_etag/image_last_modified are the HTTP validators (If-None-Match /
-- If-Modified-Since) captured from the podcast's cover image URL, so
-- clients (the TUI) can tell whether a cached copy of the image is still
-- current without downloading it again — either header may be NULL if the
-- image host doesn't send one.
ALTER TABLE podcasts ADD COLUMN IF NOT EXISTS image_etag TEXT;
ALTER TABLE podcasts ADD COLUMN IF NOT EXISTS image_last_modified TEXT;
