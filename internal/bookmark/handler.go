package bookmark

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/httperr"
)

type Handler struct {
	db *pgxpool.Pool
}

// NewHandler constructs a bookmark Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

type createRequest struct {
	EpisodeID       string `json:"episode_id"`
	PositionSeconds int    `json:"position_seconds"`
	Note            string `json:"note"`
}

// bookmarkResponse carries enough episode/podcast context (title, audio
// URL, duration, podcast name/image) for a client to jump straight into
// playback at PositionSeconds without a second round trip to look the
// episode up — mirrors ContinueItem/HistoryItem's shape for the same reason.
type bookmarkResponse struct {
	ID              string    `json:"id"`
	EpisodeID       string    `json:"episode_id"`
	PodcastID       string    `json:"podcast_id"`
	PodcastTitle    string    `json:"podcast_title"`
	PodcastImage    string    `json:"podcast_image"`
	Title           string    `json:"title"`
	AudioURL        string    `json:"audio_url"`
	Duration        *int      `json:"duration"`
	PositionSeconds int       `json:"position_seconds"`
	Note            string    `json:"note"`
	CreatedAt       time.Time `json:"created_at"`
	TranscriptURL   string    `json:"transcript_url,omitempty"`
	TranscriptType  string    `json:"transcript_type,omitempty"`
}

// List returns all of the user's bookmarks, newest first, joined with
// enough episode/podcast info to render and play from without another
// request.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	rows, err := h.db.Query(r.Context(), `
		SELECT b.id, b.episode_id, p.id, p.title, COALESCE(p.image_url,''),
		       e.title, e.audio_url, e.duration, b.position_seconds, COALESCE(b.note,''), b.created_at,
		       COALESCE(e.transcript_url,''), COALESCE(e.transcript_type,'')
		FROM bookmarks b
		JOIN episodes e ON e.id = b.episode_id
		JOIN podcasts p ON p.id = e.podcast_id
		WHERE b.user_id = $1
		ORDER BY b.created_at DESC`,
		userID,
	)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	bookmarks := []bookmarkResponse{}
	for rows.Next() {
		var b bookmarkResponse
		if err := rows.Scan(&b.ID, &b.EpisodeID, &b.PodcastID, &b.PodcastTitle, &b.PodcastImage,
			&b.Title, &b.AudioURL, &b.Duration, &b.PositionSeconds, &b.Note, &b.CreatedAt,
			&b.TranscriptURL, &b.TranscriptType,
		); err != nil {
			continue
		}
		bookmarks = append(bookmarks, b)
	}
	if err := rows.Err(); err != nil {
		httperr.Write(w, http.StatusInternalServerError, "db error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(bookmarks)
}

// Create adds a bookmark at a position in an episode, with an optional note.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EpisodeID == "" || req.PositionSeconds < 0 {
		httperr.Write(w, http.StatusBadRequest, "episode_id and a non-negative position_seconds required")
		return
	}

	var id string
	err := h.db.QueryRow(r.Context(),
		`INSERT INTO bookmarks (user_id, episode_id, position_seconds, note) VALUES ($1, $2, $3, NULLIF($4, '')) RETURNING id`,
		userID, req.EpisodeID, req.PositionSeconds, req.Note,
	).Scan(&id)
	if err != nil {
		httperr.Write(w, http.StatusBadRequest, "episode not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// Delete removes a bookmark.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)
	id := chi.URLParam(r, "id")

	tag, err := h.db.Exec(r.Context(),
		`DELETE FROM bookmarks WHERE id = $1 AND user_id = $2`,
		id, userID,
	)
	if err != nil || tag.RowsAffected() == 0 {
		httperr.Write(w, http.StatusNotFound, "not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
