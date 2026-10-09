package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/A11myt/tuipod/internal/httperr"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	db *pgxpool.Pool
}

// NewHandler constructs an admin Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

type platformStats struct {
	TotalUsers         int64 `json:"total_users"`
	TotalPodcasts      int64 `json:"total_podcasts"`
	TotalSubscriptions int64 `json:"total_subscriptions"`
	FreeUsers          int64 `json:"free_users"`
	BasicUsers         int64 `json:"basic_users"`
	ProUsers           int64 `json:"pro_users"`
}

// Stats returns platform-wide counts: users, podcasts, subscriptions, and users per plan.
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	var s platformStats
	err := h.db.QueryRow(r.Context(), `
		SELECT
			(SELECT COUNT(*) FROM users)                             AS total_users,
			(SELECT COUNT(*) FROM podcasts)                          AS total_podcasts,
			(SELECT COUNT(*) FROM subscriptions)                     AS total_subscriptions,
			(SELECT COUNT(*) FROM users WHERE plan = 'free')         AS free_users,
			(SELECT COUNT(*) FROM users WHERE plan = 'basic')        AS basic_users,
			(SELECT COUNT(*) FROM users WHERE plan = 'pro')          AS pro_users`,
	).Scan(&s.TotalUsers, &s.TotalPodcasts, &s.TotalSubscriptions,
		&s.FreeUsers, &s.BasicUsers, &s.ProUsers)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "db error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

type growthPoint struct {
	Date    string `json:"date"`
	Signups int64  `json:"signups"`
}

// GrowthStats returns daily signup counts for the last N days (query param
// "days", default 30, capped at 365) — one point per calendar day including
// days with zero signups, oldest first. Built for MARKETING.md's launch
// metrics (signups/day around a Show HN post, etc.).
//
// This only reports *signups*, not plan changes over time: `users` has no
// history of past `plan` values, just the current one, so a true
// free→paid-conversion-over-time series isn't derivable from the schema as
// it stands — that would need a separate plan-change/event log, not
// implemented here. See the "Admin-Stats um Zeitreihe erweitern" TODO note.
func (h *Handler) GrowthStats(w http.ResponseWriter, r *http.Request) {
	days := 30
	if q := r.URL.Query().Get("days"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT d::date, COUNT(u.id)
		FROM generate_series(CURRENT_DATE - ($1::int - 1), CURRENT_DATE, interval '1 day') AS d
		LEFT JOIN users u ON u.created_at::date = d::date
		GROUP BY d
		ORDER BY d`, days)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	points := []growthPoint{}
	for rows.Next() {
		var day time.Time
		var signups int64
		if err := rows.Scan(&day, &signups); err != nil {
			continue
		}
		points = append(points, growthPoint{Date: day.Format("2006-01-02"), Signups: signups})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(points)
}

type adminUser struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	Plan             string     `json:"plan"`
	EmailVerified    bool       `json:"email_verified"`
	IsAdmin          bool       `json:"is_admin"`
	SuspendedAt      *time.Time `json:"suspended_at"`
	StripeCustomerID *string    `json:"stripe_customer_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	Subscriptions    int64      `json:"subscriptions"`
}

// ListUsers returns up to 500 users with their subscription count, newest first.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT u.id, u.email, u.plan, u.email_verified, u.is_admin,
		       u.suspended_at, u.stripe_customer_id, u.created_at,
		       COUNT(s.id) AS subscriptions
		FROM users u
		LEFT JOIN subscriptions s ON s.user_id = u.id
		GROUP BY u.id
		ORDER BY u.created_at DESC
		LIMIT 500`)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	users := []adminUser{}
	for rows.Next() {
		var u adminUser
		if err := rows.Scan(&u.ID, &u.Email, &u.Plan, &u.EmailVerified, &u.IsAdmin,
			&u.SuspendedAt, &u.StripeCustomerID, &u.CreatedAt, &u.Subscriptions,
		); err != nil {
			continue
		}
		users = append(users, u)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

// GetUser returns details for the user identified by the "id" URL param.
func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var u adminUser
	err := h.db.QueryRow(r.Context(), `
		SELECT u.id, u.email, u.plan, u.email_verified, u.is_admin,
		       u.suspended_at, u.stripe_customer_id, u.created_at,
		       COUNT(s.id) AS subscriptions
		FROM users u
		LEFT JOIN subscriptions s ON s.user_id = u.id
		WHERE u.id = $1
		GROUP BY u.id`, id,
	).Scan(&u.ID, &u.Email, &u.Plan, &u.EmailVerified, &u.IsAdmin,
		&u.SuspendedAt, &u.StripeCustomerID, &u.CreatedAt, &u.Subscriptions,
	)
	if err != nil {
		httperr.Write(w, http.StatusNotFound, "user not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(u)
}

type setPlanRequest struct {
	Plan string `json:"plan"`
}

// SetPlan sets a user's plan to free, basic, or pro.
func (h *Handler) SetPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req setPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, http.StatusBadRequest, "invalid body")
		return
	}
	switch req.Plan {
	case "free", "basic", "pro":
	default:
		httperr.Write(w, http.StatusBadRequest, "plan must be free, basic, or pro")
		return
	}
	tag, err := h.db.Exec(r.Context(),
		`UPDATE users SET plan = $1 WHERE id = $2`, req.Plan, id)
	if err != nil || tag.RowsAffected() == 0 {
		httperr.Write(w, http.StatusNotFound, "user not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Suspend sets suspended_at on a user and revokes all of their refresh tokens in one transaction.
func (h *Handler) Suspend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(r.Context())

	tag, err := tx.Exec(r.Context(),
		`UPDATE users SET suspended_at = NOW() WHERE id = $1 AND suspended_at IS NULL`, id)
	if err != nil || tag.RowsAffected() == 0 {
		httperr.Write(w, http.StatusNotFound, "user not found or already suspended")
		return
	}
	tx.Exec(r.Context(),
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, id)

	if err := tx.Commit(r.Context()); err != nil {
		httperr.Write(w, http.StatusInternalServerError, "db error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Unsuspend clears suspended_at for a user.
func (h *Handler) Unsuspend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h.db.Exec(r.Context(), `UPDATE users SET suspended_at = NULL WHERE id = $1`, id)
	w.WriteHeader(http.StatusNoContent)
}

// DeleteUser permanently deletes a user.
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tag, err := h.db.Exec(r.Context(), `DELETE FROM users WHERE id = $1`, id)
	if err != nil || tag.RowsAffected() == 0 {
		httperr.Write(w, http.StatusNotFound, "user not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
