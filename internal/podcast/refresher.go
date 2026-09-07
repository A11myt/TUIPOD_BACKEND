package podcast

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/push"
)

type Refresher struct {
	db       *pgxpool.Pool
	notifier *push.Notifier
}

// NewRefresher constructs a background podcast Refresher.
func NewRefresher(db *pgxpool.Pool, n *push.Notifier) *Refresher {
	return &Refresher{db: db, notifier: n}
}

// Run refreshes all subscribed podcasts immediately, then on every interval tick.
// Stops when ctx is cancelled.
func (rf *Refresher) Run(ctx context.Context, interval time.Duration) {
	rf.refreshAll(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rf.refreshAll(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// refreshAll fetches all distinct subscribed podcasts and refreshes them one by one.
func (rf *Refresher) refreshAll(ctx context.Context) {
	rows, err := rf.db.Query(ctx, `
		SELECT DISTINCT p.id, p.rss_url
		FROM podcasts p
		JOIN subscriptions s ON s.podcast_id = p.id`)
	if err != nil {
		slog.Error("refresher: query failed", "err", err)
		return
	}
	defer rows.Close()

	type entry struct{ id, rssURL string }
	var podcasts []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.rssURL); err != nil {
			continue
		}
		podcasts = append(podcasts, e)
	}
	rows.Close()

	for _, p := range podcasts {
		if ctx.Err() != nil {
			return
		}
		if err := rf.refreshOne(ctx, p.id, p.rssURL); err != nil {
			slog.Error("refresh failed", "rss_url", p.rssURL, "err", err)
		}
	}

	slog.Info("refresh cycle done", "count", len(podcasts))
}

// refreshOne refreshes a single feed and kicks off subscriber notification asynchronously.
func (rf *Refresher) refreshOne(ctx context.Context, podcastID, rssURL string) error {
	if _, err := UpsertFeed(ctx, rf.db, rssURL); err != nil {
		return err
	}

	// notify subscribers about new episodes added in the last refresh window
	go rf.notifySubscribers(context.Background(), podcastID)
	return nil
}

// notifySubscribers checks for episodes added in the last hour and pushes a
// notification to every subscriber's registered devices.
func (rf *Refresher) notifySubscribers(ctx context.Context, podcastID string) {
	// find episodes inserted in the last hour
	rows, err := rf.db.Query(ctx, `
		SELECT e.title, p.title
		FROM episodes e
		JOIN podcasts p ON p.id = e.podcast_id
		WHERE e.podcast_id = $1
		  AND e.created_at > NOW() - INTERVAL '1 hour'
		LIMIT 1`,
		podcastID,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	if !rows.Next() {
		return
	}
	var epTitle, podTitle string
	rows.Scan(&epTitle, &podTitle)
	rows.Close()

	// collect device tokens of all subscribers
	tokenRows, err := rf.db.Query(ctx, `
		SELECT dt.token
		FROM device_tokens dt
		JOIN subscriptions s ON s.user_id = dt.user_id
		WHERE s.podcast_id = $1`,
		podcastID,
	)
	if err != nil {
		return
	}
	defer tokenRows.Close()

	var tokens []string
	for tokenRows.Next() {
		var t string
		if tokenRows.Scan(&t) == nil {
			tokens = append(tokens, t)
		}
	}
	if len(tokens) == 0 {
		return
	}

	rf.notifier.SendToUser(ctx, tokens, podTitle, epTitle, map[string]string{
		"podcast_id": podcastID,
		"type":       "new_episode",
	})
}
