package podcast

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mmcdole/gofeed"
)

// UpsertFeed fetches an RSS URL, upserts the podcast and all episodes, and returns the podcast ID.
func UpsertFeed(ctx context.Context, db *pgxpool.Pool, rssURL string) (podcastID string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	fp := gofeed.NewParser()
	feed, err := fp.ParseURLWithContext(rssURL, ctx)
	if err != nil {
		return "", fmt.Errorf("parse feed: %w", err)
	}

	imageURL := ""
	if feed.Image != nil {
		imageURL = feed.Image.URL
	}
	author := ""
	if feed.Author != nil {
		author = feed.Author.Name
	}

	err = db.QueryRow(ctx, `
		INSERT INTO podcasts (rss_url, title, author, image_url, description, last_fetched)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (rss_url) DO UPDATE
		  SET title        = EXCLUDED.title,
		      author       = EXCLUDED.author,
		      image_url    = EXCLUDED.image_url,
		      description  = EXCLUDED.description,
		      last_fetched = NOW()
		RETURNING id`,
		rssURL, feed.Title, author, imageURL, feed.Description,
	).Scan(&podcastID)
	if err != nil {
		return "", fmt.Errorf("upsert podcast: %w", err)
	}

	for _, item := range feed.Items {
		audioURL := ""
		for _, enc := range item.Enclosures {
			if enc.Type != "" {
				audioURL = enc.URL
				break
			}
		}
		if audioURL == "" {
			continue
		}

		var pubDate *time.Time
		if item.PublishedParsed != nil {
			pubDate = item.PublishedParsed
		}
		var durSecs *int
		if item.ITunesExt != nil {
			durSecs = parseDurationSecs(item.ITunesExt.Duration)
		}

		db.Exec(ctx, `
			INSERT INTO episodes (podcast_id, title, audio_url, duration, description, pub_date)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (podcast_id, audio_url) DO NOTHING`,
			podcastID, item.Title, audioURL, durSecs, item.Description, pubDate,
		)
	}

	return podcastID, nil
}
