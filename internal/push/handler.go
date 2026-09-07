package push

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

// NewHandler constructs a push Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

type registerRequest struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
}

// Register creates or updates a device token (android/ios) for the user.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)

	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		httperr.Write(w, http.StatusBadRequest, "token and platform required")
		return
	}
	if req.Platform != "android" && req.Platform != "ios" {
		httperr.Write(w, http.StatusBadRequest, "platform must be android or ios")
		return
	}

	h.db.Exec(r.Context(), `
		INSERT INTO device_tokens (user_id, token, platform)
		VALUES ($1, $2, $3)
		ON CONFLICT (token) DO UPDATE SET user_id = EXCLUDED.user_id`,
		userID, req.Token, req.Platform,
	)
	w.WriteHeader(http.StatusNoContent)
}

// Unregister removes one of the user's device tokens.
func (h *Handler) Unregister(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)
	token := chi.URLParam(r, "token")

	h.db.Exec(r.Context(),
		`DELETE FROM device_tokens WHERE token = $1 AND user_id = $2`, token, userID)
	w.WriteHeader(http.StatusNoContent)
}
