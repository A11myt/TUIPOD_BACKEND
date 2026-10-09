package podcast

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mmcdole/gofeed"
)

// parseDurationSecs converts iTunes duration strings ("H:MM:SS", "MM:SS", or plain seconds) to seconds.
func parseDurationSecs(s string) *int {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ":")
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil
		}
		total = total*60 + n
	}
	return &total
}

type Handler struct {
	db *pgxpool.Pool
}

// NewHandler constructs a podcast Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

type fetchRequest struct {
	URL string `json:"url"`
}

type podcastResponse struct {
	ID          string `json:"id"`
	RSSUrl      string `json:"rss_url"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	ImageURL    string `json:"image_url"`
	Description string `json:"description"`
}

// Fetch parses an RSS URL and upserts the podcast + episodes into the DB.
func (h *Handler) Fetch(w http.ResponseWriter, r *http.Request) {
	var req fetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}

	fp := gofeed.NewParser()
	fp.Client = safeHTTPClient // SSRF guard — see safe_client.go
	feed, err := fp.ParseURLWithContext(req.URL, r.Context())
	if err != nil {
		http.Error(w, "could not parse feed: "+err.Error(), http.StatusBadRequest)
		return
	}

	imageURL := ""
	if feed.Image != nil {
		imageURL = feed.Image.URL
	}

	author := ""
	if len(feed.Authors) > 0 {
		author = feed.Authors[0].Name
	}
	imageEtag, imageLastMod := fetchImageValidators(r.Context(), imageURL)

	var podcastID string
	var inserted bool
	err = h.db.QueryRow(r.Context(), `
		INSERT INTO podcasts (rss_url, title, author, image_url, description, last_fetched, image_etag, image_last_modified)
		VALUES ($1, $2, $3, $4, $5, NOW(), $6, $7)
		ON CONFLICT (rss_url) DO UPDATE
		  SET title = EXCLUDED.title,
		      author = EXCLUDED.author,
		      image_url = EXCLUDED.image_url,
		      description = EXCLUDED.description,
		      last_fetched = NOW(),
		      image_etag = EXCLUDED.image_etag,
		      image_last_modified = EXCLUDED.image_last_modified
		RETURNING id, (xmax = 0)`,
		req.URL, feed.Title, author, imageURL, feed.Description, imageEtag, imageLastMod,
	).Scan(&podcastID, &inserted)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// cutoff = newest pub_date already stored for this podcast (nil if this
	// is a brand-new subscribe, or a prior fetch stored none). The TUI's "R"
	// hits this same endpoint, so this is what makes a repeat refresh of a
	// long-running podcast cheap: items strictly older than cutoff never
	// reach the DB at all, instead of one INSERT-then-ignore round trip per
	// episode in the entire back-catalog. "Older than", never "at or
	// before" or an early break — feed order isn't contractually guaranteed
	// newest-first, so this only skips episodes provably already known.
	var cutoff *time.Time
	if err := h.db.QueryRow(r.Context(),
		`SELECT MAX(pub_date) FROM episodes WHERE podcast_id = $1`, podcastID,
	).Scan(&cutoff); err != nil {
		cutoff = nil // best-effort — fall back to processing everything
	}

	for _, item := range feed.Items {
		// The enclosure IS the audio file by RSS/podcast convention — take the
		// first one regardless of its type attribute. Some hosts omit/blank
		// that attribute on enclosures (seen in the wild on otherwise-valid
		// feeds), and requiring it non-empty silently dropped those episodes.
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

		if _, err := h.db.Exec(r.Context(), `
			INSERT INTO episodes (podcast_id, title, audio_url, duration, description, pub_date, transcript_url, transcript_type)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (podcast_id, audio_url) DO NOTHING`,
			podcastID, item.Title, audioURL, durSecs, item.Description, pubDate, transcriptURL, transcriptType,
		); err != nil {
			slog.Warn("episode insert failed", "podcast_id", podcastID, "title", item.Title, "err", err)
		}
	}

	p := podcastResponse{
		ID:          podcastID,
		RSSUrl:      req.URL,
		Title:       feed.Title,
		Author:      author,
		ImageURL:    imageURL,
		Description: feed.Description,
	}
	w.Header().Set("Content-Type", "application/json")
	if inserted {
		w.WriteHeader(http.StatusCreated)
	}
	json.NewEncoder(w).Encode(p)
}

// GetPodcast returns podcast details by ID.
func (h *Handler) GetPodcast(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var p podcastResponse
	err := h.db.QueryRow(r.Context(),
		`SELECT id, rss_url, title, COALESCE(author,''), COALESCE(image_url,''), COALESCE(description,'') FROM podcasts WHERE id = $1`,
		id,
	).Scan(&p.ID, &p.RSSUrl, &p.Title, &p.Author, &p.ImageURL, &p.Description)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

type episodeResponse struct {
	ID          string     `json:"id"`
	PodcastID   string     `json:"podcast_id"`
	Title       string     `json:"title"`
	AudioURL    string     `json:"audio_url"`
	Duration    *int       `json:"duration"`
	Description string     `json:"description"`
	PubDate     *time.Time `json:"pub_date"`
	// TranscriptURL/TranscriptType come from the feed's own
	// <podcast:transcript> tag, if it has one — see podcast.pickTranscript.
	// Empty when the podcast doesn't publish one; never generated by us.
	TranscriptURL  string `json:"transcript_url,omitempty"`
	TranscriptType string `json:"transcript_type,omitempty"`
}

// Search proxies the iTunes podcast search API.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q required", http.StatusBadRequest)
		return
	}

	apiURL := "https://itunes.apple.com/search?media=podcast&limit=20&term=" + url.QueryEscape(q)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, apiURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "search failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var itunes struct {
		Results []struct {
			FeedURL        string `json:"feedUrl"`
			CollectionName string `json:"collectionName"`
			ArtistName     string `json:"artistName"`
			ArtworkURL600  string `json:"artworkUrl600"`
			TrackCount     int    `json:"trackCount"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&itunes); err != nil {
		http.Error(w, "parse error", http.StatusInternalServerError)
		return
	}

	type searchResult struct {
		FeedURL      string `json:"feed_url"`
		Title        string `json:"title"`
		Author       string `json:"author"`
		ImageURL     string `json:"image_url"`
		EpisodeCount int    `json:"episode_count"`
	}
	results := make([]searchResult, 0, len(itunes.Results))
	for _, item := range itunes.Results {
		if item.FeedURL == "" {
			continue
		}
		results = append(results, searchResult{
			FeedURL:      item.FeedURL,
			Title:        item.CollectionName,
			Author:       item.ArtistName,
			ImageURL:     item.ArtworkURL600,
			EpisodeCount: item.TrackCount,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// Refresh re-fetches the RSS feed for an existing podcast and upserts new episodes.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var rssURL string
	if err := h.db.QueryRow(r.Context(),
		`SELECT rss_url FROM podcasts WHERE id = $1`, id,
	).Scan(&rssURL); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if _, err := UpsertFeed(r.Context(), h.db, rssURL); err != nil {
		http.Error(w, "could not refresh feed: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetEpisodes returns a podcast's episodes, paginated via limit/offset query params.
func (h *Handler) GetEpisodes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	limit, offset := 50, 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT id, podcast_id, title, audio_url, duration, COALESCE(description,''), pub_date,
		       COALESCE(transcript_url,''), COALESCE(transcript_type,'')
		FROM episodes WHERE podcast_id = $1
		ORDER BY pub_date DESC LIMIT $2 OFFSET $3`,
		id, limit, offset,
	)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	episodes := []episodeResponse{}
	for rows.Next() {
		var e episodeResponse
		if err := rows.Scan(&e.ID, &e.PodcastID, &e.Title, &e.AudioURL, &e.Duration, &e.Description, &e.PubDate,
			&e.TranscriptURL, &e.TranscriptType); err != nil {
			continue
		}
		episodes = append(episodes, e)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(episodes)
}
