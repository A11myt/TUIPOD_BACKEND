package subscription

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/httperr"
	"github.com/A11myt/tuipod/internal/podcast"
)

type Handler struct {
	db *pgxpool.Pool
}

// NewHandler constructs a subscription Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

type subRequest struct {
	PodcastID string `json:"podcast_id"`
}

type subResponse struct {
	ID          string     `json:"id"`
	PodcastID   string     `json:"podcast_id"`
	Title       string     `json:"title"`
	ImageURL    string     `json:"image_url"`
	LastFetched *time.Time `json:"last_fetched"`
	Favorite    bool       `json:"favorite"`
	FavoriteID  *string    `json:"favorite_id,omitempty"`
	// ImageEtag/ImageLastModified are the cover image's HTTP validators
	// (captured server-side during feed refresh, see podcast.fetchImageValidators)
	// so a client can tell whether its own locally cached copy of the image
	// is stale without downloading it again.
	ImageEtag         string `json:"image_etag,omitempty"`
	ImageLastModified string `json:"image_last_modified,omitempty"`
	// Per-podcast listening stats for the current user — same shape as the
	// global GET /me/stats, just grouped by podcast instead of summed across
	// all of them, so clients (the TUI's subs grid cards) don't need an
	// extra round trip per podcast to show "how much have I listened to this show".
	CompletedEpisodes    int64 `json:"completed_episodes"`
	InProgressEpisodes   int64 `json:"in_progress_episodes"`
	TotalListenedSeconds int64 `json:"total_listened_seconds"`
	// TotalEpisodes is the podcast's whole known episode count (not scoped to
	// this user, unlike the stats above) — lets a client show "12/45 · 33 left".
	TotalEpisodes int64 `json:"total_episodes"`
}

// List returns all of the user's subscriptions, newest first. Includes
// last_fetched (so clients can decide when a feed needs refreshing),
// favorite/favorite_id (favorites are their own resource — internal/favorite
// — not a column on subscriptions, so this is a LEFT JOIN; favorite_id is
// what DELETE /me/favorites/{id} expects, not the podcast or subscription id),
// and per-podcast listening stats (LEFT JOIN a GROUP BY podcast_id subquery
// over progress, scoped to this user — COALESCEd to 0 for a podcast with no
// progress rows yet).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	rows, err := h.db.Query(r.Context(), `
		SELECT s.id, p.id, p.title, COALESCE(p.image_url,''), p.last_fetched, f.id,
		       COALESCE(stats.completed, 0), COALESCE(stats.in_progress, 0), COALESCE(stats.total_seconds, 0),
		       COALESCE(ep_counts.total, 0), COALESCE(p.image_etag,''), COALESCE(p.image_last_modified,'')
		FROM subscriptions s
		JOIN podcasts p ON p.id = s.podcast_id
		LEFT JOIN favorites f ON f.podcast_id = p.id AND f.user_id = s.user_id
		LEFT JOIN (
			SELECT e.podcast_id,
			       COUNT(*) FILTER (WHERE pr.completed = true)                          AS completed,
			       COUNT(*) FILTER (WHERE pr.position_seconds > 0 AND pr.completed = false) AS in_progress,
			       COALESCE(SUM(pr.position_seconds), 0)                                AS total_seconds
			FROM progress pr
			JOIN episodes e ON e.id = pr.episode_id
			WHERE pr.user_id = $1
			GROUP BY e.podcast_id
		) stats ON stats.podcast_id = p.id
		LEFT JOIN (
			SELECT podcast_id, COUNT(*) AS total FROM episodes GROUP BY podcast_id
		) ep_counts ON ep_counts.podcast_id = p.id
		WHERE s.user_id = $1
		ORDER BY s.created_at DESC`,
		userID,
	)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	subs := []subResponse{}
	for rows.Next() {
		var s subResponse
		if err := rows.Scan(&s.ID, &s.PodcastID, &s.Title, &s.ImageURL, &s.LastFetched, &s.FavoriteID,
			&s.CompletedEpisodes, &s.InProgressEpisodes, &s.TotalListenedSeconds, &s.TotalEpisodes,
			&s.ImageEtag, &s.ImageLastModified,
		); err != nil {
			continue
		}
		s.Favorite = s.FavoriteID != nil
		subs = append(subs, s)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(subs)
}

// Subscribe subscribes the user to a podcast. No subscription-count limit — Sync
// itself (this endpoint requires plan basic/pro, gated in main.go) is the paid feature.
func (h *Handler) Subscribe(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var req subRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PodcastID == "" {
		httperr.Write(w, http.StatusBadRequest, "podcast_id required")
		return
	}

	var id string
	err := h.db.QueryRow(r.Context(),
		`INSERT INTO subscriptions (user_id, podcast_id) VALUES ($1, $2) RETURNING id`,
		userID, req.PodcastID,
	).Scan(&id)
	if err != nil {
		httperr.Write(w, http.StatusConflict, "already subscribed or podcast not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// opmlImport is the minimal OPML structure needed for parsing imported files.
type opmlImport struct {
	XMLName xml.Name `xml:"opml"`
	Body    struct {
		Outlines []struct {
			Text   string `xml:"text,attr"`
			XMLUrl string `xml:"xmlUrl,attr"`
			// nested groups (e.g. from Pocket Casts)
			Outlines []struct {
				Text   string `xml:"text,attr"`
				XMLUrl string `xml:"xmlUrl,attr"`
			} `xml:"outline"`
		} `xml:"outline"`
	} `xml:"body"`
}

// ImportOPML parses an uploaded OPML file (including one level of nested groups),
// fetches/upserts each feed, and subscribes the user to it.
func (h *Handler) ImportOPML(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var doc opmlImport
	if err := xml.NewDecoder(r.Body).Decode(&doc); err != nil {
		http.Error(w, "invalid OPML: "+err.Error(), http.StatusBadRequest)
		return
	}

	// collect all feed URLs (flat + one level of nesting)
	var feedURLs []string
	for _, o := range doc.Body.Outlines {
		if o.XMLUrl != "" {
			feedURLs = append(feedURLs, o.XMLUrl)
		}
		for _, nested := range o.Outlines {
			if nested.XMLUrl != "" {
				feedURLs = append(feedURLs, nested.XMLUrl)
			}
		}
	}
	if len(feedURLs) == 0 {
		http.Error(w, "no feed URLs found in OPML", http.StatusBadRequest)
		return
	}

	imported, skipped := 0, 0
	for _, feedURL := range feedURLs {
		podcastID, err := podcast.UpsertFeed(r.Context(), h.db, feedURL)
		if err != nil {
			skipped++
			continue
		}
		if _, err := h.db.Exec(r.Context(),
			`INSERT INTO subscriptions (user_id, podcast_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			userID, podcastID,
		); err != nil {
			skipped++
			continue
		}
		imported++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"imported": imported, "skipped": skipped})
}

type opmlExport struct {
	XMLName xml.Name   `xml:"opml"`
	Version string     `xml:"version,attr"`
	Head    opmlHead   `xml:"head"`
	Body    opmlBody   `xml:"body"`
}

type opmlHead struct {
	Title string `xml:"title"`
}

type opmlBody struct {
	Outlines []opmlOutline `xml:"outline"`
}

type opmlOutline struct {
	Text   string `xml:"text,attr"`
	Type   string `xml:"type,attr"`
	XMLUrl string `xml:"xmlUrl,attr"`
}

// ExportOPML exports all of the user's subscriptions as a downloadable OPML file.
func (h *Handler) ExportOPML(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	rows, err := h.db.Query(r.Context(), `
		SELECT p.title, p.rss_url
		FROM subscriptions s
		JOIN podcasts p ON p.id = s.podcast_id
		WHERE s.user_id = $1
		ORDER BY p.title`,
		userID,
	)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	doc := opmlExport{
		Version: "2.0",
		Head:    opmlHead{Title: "TUIPOD Subscriptions"},
	}
	for rows.Next() {
		var title, rssURL string
		if err := rows.Scan(&title, &rssURL); err != nil {
			continue
		}
		doc.Body.Outlines = append(doc.Body.Outlines, opmlOutline{
			Text:   title,
			Type:   "rss",
			XMLUrl: rssURL,
		})
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		http.Error(w, "xml error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="tuipod.opml"`)
	fmt.Fprintf(w, "%s\n%s\n", xml.Header, out)
}

// Unsubscribe removes a subscription.
func (h *Handler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)
	subID := chi.URLParam(r, "id")

	tag, err := h.db.Exec(r.Context(),
		`DELETE FROM subscriptions WHERE id = $1 AND user_id = $2`,
		subID, userID,
	)
	if err != nil || tag.RowsAffected() == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
