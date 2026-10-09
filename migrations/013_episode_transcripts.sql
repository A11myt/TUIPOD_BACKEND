-- transcript_url/transcript_type come from the feed's own
-- <podcast:transcript> tag (Podcasting 2.0 namespace) when present — we
-- never generate transcripts ourselves, only surface what the podcast
-- already publishes. transcript_type is the tag's MIME type attribute
-- (text/plain, text/vtt, application/srt, text/html, ...), picked by
-- preference when a feed lists more than one (see podcast.pickTranscript).
ALTER TABLE episodes ADD COLUMN IF NOT EXISTS transcript_url TEXT;
ALTER TABLE episodes ADD COLUMN IF NOT EXISTS transcript_type TEXT;
