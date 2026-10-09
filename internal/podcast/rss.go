package podcast

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mmcdole/gofeed"
)

// fetchImageValidators does a best-effort HEAD request against imageURL to
// capture its ETag/Last-Modified — clients (the TUI) use these to tell
// whether a locally cached copy of the podcast's cover image is still
// current without re-downloading it. Failure (network error, non-2xx
// response, no such headers on the response) isn't an error for the
// caller: it just means clients won't have a freshness signal for this
// podcast, the same way a blank image_url isn't fatal for the show itself.
// Runs with its own short timeout so a slow/unresponsive image host can't
// meaningfully delay a feed refresh.
func fetchImageValidators(ctx context.Context, imageURL string) (etag, lastModified string) {
	if imageURL == "" {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, imageURL, nil)
	if err != nil {
		return "", ""
	}
	resp, err := safeHTTPClient.Do(req) // SSRF guard — imageURL comes from the fetched feed's own body
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", ""
	}
	return resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
}

// pickTranscript picks the best available <podcast:transcript> tag
// (Podcasting 2.0 namespace) for an episode, when a feed lists more than
// one — different languages and/or formats are common. gofeed has no
// typed support for this namespace, so it lands in the generic extension
// map under the "podcast" prefix (the namespace's de facto standard
// prefix, which is what feeds actually declare in practice). Preference
// order favors whatever a client can display with the least extra work:
// plain text first, then subtitle formats with timing a client can strip,
// then HTML, then whatever's left. Returns ("", "") if the feed has none —
// we only ever surface a transcript the podcast already publishes, never
// generate one ourselves.
func pickTranscript(item *gofeed.Item) (url, mimeType string) {
	if item.Extensions == nil {
		return "", ""
	}
	list := item.Extensions["podcast"]["transcript"]
	if len(list) == 0 {
		return "", ""
	}
	preference := []string{"text/plain", "text/vtt", "application/srt", "application/x-subrip", "text/html"}
	for _, want := range preference {
		for _, t := range list {
			if t.Attrs["url"] != "" && strings.EqualFold(t.Attrs["type"], want) {
				return t.Attrs["url"], t.Attrs["type"]
			}
		}
	}
	for _, t := range list {
		if t.Attrs["url"] != "" {
			return t.Attrs["url"], t.Attrs["type"]
		}
	}
	return "", ""
}

// UpsertFeed fetches an RSS URL, upserts the podcast and all episodes, and returns the podcast ID.
func UpsertFeed(ctx context.Context, db *pgxpool.Pool, rssURL string) (podcastID string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	fp := gofeed.NewParser()
	fp.Client = safeHTTPClient // SSRF guard — see safe_client.go
	feed, err := fp.ParseURLWithContext(rssURL, ctx)
	if err != nil {
		return "", fmt.Errorf("parse feed: %w", err)
	}

	imageURL := ""
	if feed.Image != nil {
		imageURL = feed.Image.URL
	}
	author := ""
	if len(feed.Authors) > 0 {
		author = feed.Authors[0].Name
	}
	imageEtag, imageLastMod := fetchImageValidators(ctx, imageURL)

	err = db.QueryRow(ctx, `
		INSERT INTO podcasts (rss_url, title, author, image_url, description, last_fetched, image_etag, image_last_modified)
		VALUES ($1, $2, $3, $4, $5, NOW(), $6, $7)
		ON CONFLICT (rss_url) DO UPDATE
		  SET title               = EXCLUDED.title,
		      author              = EXCLUDED.author,
		      image_url           = EXCLUDED.image_url,
		      description         = EXCLUDED.description,
		      last_fetched        = NOW(),
		      image_etag          = EXCLUDED.image_etag,
		      image_last_modified = EXCLUDED.image_last_modified
		RETURNING id`,
		rssURL, feed.Title, author, imageURL, feed.Description, imageEtag, imageLastMod,
	).Scan(&podcastID)
	if err != nil {
		return "", fmt.Errorf("upsert podcast: %w", err)
	}

	// cutoff = the newest pub_date we already have for this podcast (nil for
	// a brand-new one). Refreshing a long-running podcast otherwise means
	// re-attempting an INSERT for every episode in its entire history on
	// every single refresh — cheap individually (ON CONFLICT DO NOTHING),
	// but that's still one DB round trip per episode, hundreds of them, for
	// what's typically 0-2 genuinely new episodes. Items strictly older than
	// cutoff are skipped in-memory before ever touching the DB. Uses "older
	// than", never "at or before" or an early break: feed item order isn't
	// contractually guaranteed newest-first even though it conventionally
	// is, so this only skips episodes provably already known, never risks
	// missing one.
	var cutoff *time.Time
	if err := db.QueryRow(ctx,
		`SELECT MAX(pub_date) FROM episodes WHERE podcast_id = $1`, podcastID,
	).Scan(&cutoff); err != nil {
		cutoff = nil // best-effort — fall back to processing everything
	}

	for _, item := range feed.Items {
		// The enclosure IS the audio file by RSS/podcast convention — take the
		// first one regardless of its type attribute. Some hosts omit/blank
		// that attribute on enclosures (seen in the wild on otherwise-valid
		// feeds), and requiring it non-empty silently dropped those episodes —
		// this is the same fix as internal/podcast/handler.go's Fetch.
		audioURL := ""
		if len(item.Enclosures) > 0 {
			audioURL = item.Enclosures[0].URL
		}
		if audioURL == "" {
			continue
		}

		var pubDate *time.Time
		if item.PublishedParsed != nil {
			pubDate = item.PublishedParsed
		}
		if cutoff != nil && pubDate != nil && pubDate.Before(*cutoff) {
			continue
		}

		var durSecs *int
		if item.ITunesExt != nil {
			durSecs = parseDurationSecs(item.ITunesExt.Duration)
		}
		transcriptURL, transcriptType := pickTranscript(item)

		if _, err := db.Exec(ctx, `
			INSERT INTO episodes (podcast_id, title, audio_url, duration, description, pub_date, transcript_url, transcript_type)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (podcast_id, audio_url) DO NOTHING`,
			podcastID, item.Title, audioURL, durSecs, item.Description, pubDate, transcriptURL, transcriptType,
		); err != nil {
			slog.Warn("episode insert failed", "podcast_id", podcastID, "title", item.Title, "err", err)
		}
	}

	return podcastID, nil
}
