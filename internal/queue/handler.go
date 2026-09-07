package queue

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

// NewHandler constructs a queue Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

type queueItem struct {
	ID           string    `json:"id"`
	Position     int       `json:"position"`
	EpisodeID    string    `json:"episode_id"`
	PodcastID    string    `json:"podcast_id"`
	PodcastTitle string    `json:"podcast_title"`
	PodcastImage string    `json:"podcast_image"`
	Title        string    `json:"title"`
	AudioURL     string    `json:"audio_url"`
	Duration     *int      `json:"duration"`
	AddedAt      time.Time `json:"added_at"`
}

// List returns the user's listening queue, ordered by position.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	rows, err := h.db.Query(r.Context(), `
		SELECT q.id, q.position,
		       e.id, e.podcast_id, p.title, COALESCE(p.image_url,''),
		       e.title, e.audio_url, e.duration, q.added_at
		FROM queue q
		JOIN episodes e ON e.id = q.episode_id
		JOIN podcasts p ON p.id = e.podcast_id
		WHERE q.user_id = $1
		ORDER BY q.position ASC, q.added_at ASC`,
		userID,
	)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []queueItem{}
	for rows.Next() {
		var q queueItem
		if err := rows.Scan(
			&q.ID, &q.Position,
			&q.EpisodeID, &q.PodcastID, &q.PodcastTitle, &q.PodcastImage,
			&q.Title, &q.AudioURL, &q.Duration, &q.AddedAt,
		); err != nil {
			continue
		}
		items = append(items, q)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

type addRequest struct {
	EpisodeID string `json:"episode_id"`
}

// Add appends an episode to the end of the user's queue.
func (h *Handler) Add(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var req addRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EpisodeID == "" {
		http.Error(w, "episode_id required", http.StatusBadRequest)
		return
	}

	var id string
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO queue (user_id, episode_id, position)
		SELECT $1, $2, COALESCE(MAX(position) + 1, 0)
		FROM queue WHERE user_id = $1
		RETURNING id`,
		userID, req.EpisodeID,
	).Scan(&id)
	if err != nil {
		http.Error(w, "already in queue or episode not found", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

type reorderRequest struct {
	IDs []string `json:"ids"`
}

// Reorder sets a new queue order from a list of IDs, transactionally.
func (h *Handler) Reorder(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var req reorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		http.Error(w, "ids required", http.StatusBadRequest)
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	for i, id := range req.IDs {
		if _, err := tx.Exec(r.Context(),
			`UPDATE queue SET position = $1 WHERE id = $2 AND user_id = $3`,
			i, id, userID,
		); err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Remove deletes an entry from the user's queue.
func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)
	itemID := chi.URLParam(r, "id")

	tag, err := h.db.Exec(r.Context(),
		`DELETE FROM queue WHERE id = $1 AND user_id = $2`,
		itemID, userID,
	)
	if err != nil || tag.RowsAffected() == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
