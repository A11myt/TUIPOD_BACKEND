package favorite

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/httperr"
)

type Handler struct {
	db *pgxpool.Pool
}

// NewHandler constructs a favorite Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

type favoriteRequest struct {
	PodcastID string `json:"podcast_id"`
}

type favoriteResponse struct {
	ID        string `json:"id"`
	PodcastID string `json:"podcast_id"`
	Title     string `json:"title"`
	ImageURL  string `json:"image_url"`
}

// List returns all of the user's favorited podcasts, newest first.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	rows, err := h.db.Query(r.Context(), `
		SELECT f.id, p.id, p.title, COALESCE(p.image_url,'')
		FROM favorites f
		JOIN podcasts p ON p.id = f.podcast_id
		WHERE f.user_id = $1
		ORDER BY f.created_at DESC`,
		userID,
	)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	favs := []favoriteResponse{}
	for rows.Next() {
		var f favoriteResponse
		if err := rows.Scan(&f.ID, &f.PodcastID, &f.Title, &f.ImageURL); err != nil {
			continue
		}
		favs = append(favs, f)
	}
	if err := rows.Err(); err != nil {
		httperr.Write(w, http.StatusInternalServerError, "db error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(favs)
}

// Add favorites a podcast for the user.
func (h *Handler) Add(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var req favoriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PodcastID == "" {
		httperr.Write(w, http.StatusBadRequest, "podcast_id required")
		return
	}

	var id string
	err := h.db.QueryRow(r.Context(),
		`INSERT INTO favorites (user_id, podcast_id) VALUES ($1, $2) RETURNING id`,
		userID, req.PodcastID,
	).Scan(&id)
	if err != nil {
		httperr.Write(w, http.StatusConflict, "already favorited or podcast not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// Remove un-favorites a podcast.
func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)
	favID := chi.URLParam(r, "id")

	tag, err := h.db.Exec(r.Context(),
		`DELETE FROM favorites WHERE id = $1 AND user_id = $2`,
		favID, userID,
	)
	if err != nil || tag.RowsAffected() == 0 {
		httperr.Write(w, http.StatusNotFound, "not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
