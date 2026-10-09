package user

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/A11myt/tuipod/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type feedItem struct {
	ID           string     `json:"id"`
	PodcastID    string     `json:"podcast_id"`
	PodcastTitle string     `json:"podcast_title"`
	PodcastImage string     `json:"podcast_image"`
	Title        string     `json:"title"`
	AudioURL     string     `json:"audio_url"`
	Duration     *int       `json:"duration"`
	Description  string     `json:"description"`
	PubDate      *time.Time `json:"pub_date"`
	PositionSecs int        `json:"position_seconds"`
	Completed    bool       `json:"completed"`
	// TranscriptURL/TranscriptType — see podcast.episodeResponse's doc
	// comment on the same fields.
	TranscriptURL  string `json:"transcript_url,omitempty"`
	TranscriptType string `json:"transcript_type,omitempty"`
}

type Handler struct {
	db *pgxpool.Pool
}

// NewHandler constructs a user Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

type userResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// Feed returns the latest 100 episodes from all subscribed podcasts, with playback progress.
func (h *Handler) Feed(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	rows, err := h.db.Query(r.Context(), `
		SELECT e.id, e.podcast_id, p.title, COALESCE(p.image_url,''),
		       e.title, e.audio_url, e.duration, COALESCE(e.description,''), e.pub_date,
		       COALESCE(pr.position_seconds, 0), COALESCE(pr.completed, false),
		       COALESCE(e.transcript_url,''), COALESCE(e.transcript_type,'')
		FROM episodes e
		JOIN podcasts p ON p.id = e.podcast_id
		JOIN subscriptions s ON s.podcast_id = e.podcast_id AND s.user_id = $1
		LEFT JOIN progress pr ON pr.episode_id = e.id AND pr.user_id = $1
		ORDER BY e.pub_date DESC NULLS LAST
		LIMIT 100`,
		userID,
	)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []feedItem{}
	for rows.Next() {
		var f feedItem
		if err := rows.Scan(
			&f.ID, &f.PodcastID, &f.PodcastTitle, &f.PodcastImage,
			&f.Title, &f.AudioURL, &f.Duration, &f.Description, &f.PubDate,
			&f.PositionSecs, &f.Completed,
			&f.TranscriptURL, &f.TranscriptType,
		); err != nil {
			continue
		}
		items = append(items, f)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

type continueItem struct {
	ID             string    `json:"id"`
	PodcastID      string    `json:"podcast_id"`
	PodcastTitle   string    `json:"podcast_title"`
	PodcastImage   string    `json:"podcast_image"`
	Title          string    `json:"title"`
	AudioURL       string    `json:"audio_url"`
	Duration       *int      `json:"duration"`
	PositionSecs   int       `json:"position_seconds"`
	LastListened   time.Time `json:"last_listened"`
	TranscriptURL  string    `json:"transcript_url,omitempty"`
	TranscriptType string    `json:"transcript_type,omitempty"`
}

// ContinueListening returns in-progress episodes, most recently listened first.
func (h *Handler) ContinueListening(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	rows, err := h.db.Query(r.Context(), `
		SELECT e.id, e.podcast_id, p.title, COALESCE(p.image_url,''),
		       e.title, e.audio_url, e.duration,
		       pr.position_seconds, pr.updated_at,
		       COALESCE(e.transcript_url,''), COALESCE(e.transcript_type,'')
		FROM progress pr
		JOIN episodes e ON e.id = pr.episode_id
		JOIN podcasts p ON p.id = e.podcast_id
		WHERE pr.user_id = $1
		  AND pr.position_seconds > 0
		  AND pr.completed = false
		ORDER BY pr.updated_at DESC
		LIMIT 50`,
		userID,
	)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []continueItem{}
	for rows.Next() {
		var c continueItem
		if err := rows.Scan(
			&c.ID, &c.PodcastID, &c.PodcastTitle, &c.PodcastImage,
			&c.Title, &c.AudioURL, &c.Duration,
			&c.PositionSecs, &c.LastListened,
			&c.TranscriptURL, &c.TranscriptType,
		); err != nil {
			continue
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

type historyItem struct {
	ID             string    `json:"id"`
	PodcastID      string    `json:"podcast_id"`
	PodcastTitle   string    `json:"podcast_title"`
	PodcastImage   string    `json:"podcast_image"`
	Title          string    `json:"title"`
	AudioURL       string    `json:"audio_url"`
	Duration       *int      `json:"duration"`
	CompletedAt    time.Time `json:"completed_at"`
	TranscriptURL  string    `json:"transcript_url,omitempty"`
	TranscriptType string    `json:"transcript_type,omitempty"`
}

// History returns completed episodes, most recently completed first.
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	rows, err := h.db.Query(r.Context(), `
		SELECT e.id, e.podcast_id, p.title, COALESCE(p.image_url,''),
		       e.title, e.audio_url, e.duration, pr.updated_at,
		       COALESCE(e.transcript_url,''), COALESCE(e.transcript_type,'')
		FROM progress pr
		JOIN episodes e ON e.id = pr.episode_id
		JOIN podcasts p ON p.id = e.podcast_id
		WHERE pr.user_id = $1
		  AND pr.completed = true
		ORDER BY pr.updated_at DESC
		LIMIT 100`,
		userID,
	)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []historyItem{}
	for rows.Next() {
		var h historyItem
		if err := rows.Scan(
			&h.ID, &h.PodcastID, &h.PodcastTitle, &h.PodcastImage,
			&h.Title, &h.AudioURL, &h.Duration, &h.CompletedAt,
			&h.TranscriptURL, &h.TranscriptType,
		); err != nil {
			continue
		}
		items = append(items, h)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

type statsResponse struct {
	CompletedEpisodes    int64 `json:"completed_episodes"`
	InProgressEpisodes   int64 `json:"in_progress_episodes"`
	TotalListenedSeconds int64 `json:"total_listened_seconds"`
	Subscriptions        int64 `json:"subscriptions"`
}

// Stats aggregates listening stats (completed/in-progress/total time/subscription count)
// in a single query.
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var s statsResponse
	err := h.db.QueryRow(r.Context(), `
		WITH pr AS (
			SELECT
				COUNT(*) FILTER (WHERE completed = true)                        AS completed_episodes,
				COUNT(*) FILTER (WHERE position_seconds > 0 AND completed = false) AS in_progress,
				COALESCE(SUM(position_seconds), 0)                              AS total_listened_seconds
			FROM progress WHERE user_id = $1
		),
		sub AS (
			SELECT COUNT(*) AS subscriptions FROM subscriptions WHERE user_id = $1
		)
		SELECT pr.completed_episodes, pr.in_progress, pr.total_listened_seconds, sub.subscriptions
		FROM pr, sub`,
		userID,
	).Scan(&s.CompletedEpisodes, &s.InProgressEpisodes, &s.TotalListenedSeconds, &s.Subscriptions)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword updates the password after verifying the current one, then revokes
// all of the user's refresh tokens.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var req passwordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NewPassword == "" {
		http.Error(w, "current_password and new_password required", http.StatusBadRequest)
		return
	}
	if len(req.NewPassword) < 8 || len(req.NewPassword) > 72 {
		http.Error(w, "password must be 8-72 characters", http.StatusBadRequest)
		return
	}

	var hash string
	if err := h.db.QueryRow(r.Context(),
		`SELECT password_hash FROM users WHERE id = $1`, userID,
	).Scan(&hash); err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.CurrentPassword)); err != nil {
		http.Error(w, "invalid current password", http.StatusUnauthorized)
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if _, err := h.db.Exec(r.Context(),
		`UPDATE users SET password_hash = $1 WHERE id = $2`, string(newHash), userID,
	); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	h.db.Exec(r.Context(),
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, userID)

	w.WriteHeader(http.StatusNoContent)
}

// Delete removes the caller's own account.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	if _, err := h.db.Exec(r.Context(),
		`DELETE FROM users WHERE id = $1`, userID,
	); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Me returns basic profile data for the logged-in user.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var u userResponse
	err := h.db.QueryRow(r.Context(),
		`SELECT id, email, created_at FROM users WHERE id = $1`,
		userID,
	).Scan(&u.ID, &u.Email, &u.CreatedAt)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(u)
}
