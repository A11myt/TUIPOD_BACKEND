package episode

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/A11myt/tuipod/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	db *pgxpool.Pool
}

// NewHandler constructs an episode Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

type episodeResponse struct {
	ID          string     `json:"id"`
	PodcastID   string     `json:"podcast_id"`
	Title       string     `json:"title"`
	AudioURL    string     `json:"audio_url"`
	Duration    *int       `json:"duration"`
	Description string     `json:"description"`
	PubDate     *time.Time `json:"pub_date"`
}

type episodeSearchResult struct {
	ID           string     `json:"id"`
	PodcastID    string     `json:"podcast_id"`
	PodcastTitle string     `json:"podcast_title"`
	Title        string     `json:"title"`
	AudioURL     string     `json:"audio_url"`
	Duration     *int       `json:"duration"`
	PubDate      *time.Time `json:"pub_date"`
}

// Search does a full-text (ILIKE) search over episode titles, scoped to the user's subscriptions.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q required", http.StatusBadRequest)
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT e.id, e.podcast_id, p.title, e.title, e.audio_url, e.duration, e.pub_date
		FROM episodes e
		JOIN podcasts p ON p.id = e.podcast_id
		JOIN subscriptions s ON s.podcast_id = e.podcast_id AND s.user_id = $1
		WHERE e.title ILIKE $2
		ORDER BY e.pub_date DESC
		LIMIT 50`,
		userID, "%"+q+"%",
	)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	results := []episodeSearchResult{}
	for rows.Next() {
		var e episodeSearchResult
		if err := rows.Scan(&e.ID, &e.PodcastID, &e.PodcastTitle, &e.Title, &e.AudioURL, &e.Duration, &e.PubDate); err != nil {
			continue
		}
		results = append(results, e)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// GetEpisode returns a single episode by ID.
func (h *Handler) GetEpisode(w http.ResponseWriter, r *http.Request) {
	episodeID := chi.URLParam(r, "id")

	var e episodeResponse
	err := h.db.QueryRow(r.Context(), `
		SELECT id, podcast_id, title, audio_url, duration, COALESCE(description,''), pub_date
		FROM episodes WHERE id = $1`,
		episodeID,
	).Scan(&e.ID, &e.PodcastID, &e.Title, &e.AudioURL, &e.Duration, &e.Description, &e.PubDate)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(e)
}

type progressRequest struct {
	PositionSeconds int  `json:"position_seconds"`
	Completed       bool `json:"completed"`
}

type progressResponse struct {
	EpisodeID       string `json:"episode_id"`
	PositionSeconds int    `json:"position_seconds"`
	Completed       bool   `json:"completed"`
}

// GetProgress returns listening progress (position, completed) for an episode,
// or a zero state if nothing has been saved yet.
func (h *Handler) GetProgress(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)
	episodeID := chi.URLParam(r, "id")

	var p progressResponse
	p.EpisodeID = episodeID

	err := h.db.QueryRow(r.Context(),
		`SELECT position_seconds, completed FROM progress WHERE user_id = $1 AND episode_id = $2`,
		userID, episodeID,
	).Scan(&p.PositionSeconds, &p.Completed)
	if err != nil {
		// no progress yet — return zero state
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

// UpsertProgress saves or updates listening progress for an episode.
func (h *Handler) UpsertProgress(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)
	episodeID := chi.URLParam(r, "id")

	var req progressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	_, err := h.db.Exec(r.Context(), `
		INSERT INTO progress (user_id, episode_id, position_seconds, completed, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (user_id, episode_id) DO UPDATE
		  SET position_seconds = EXCLUDED.position_seconds,
		      completed = EXCLUDED.completed,
		      updated_at = NOW()`,
		userID, episodeID, req.PositionSeconds, req.Completed,
	)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
