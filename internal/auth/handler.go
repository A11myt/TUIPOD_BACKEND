package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/httperr"
	"github.com/A11myt/tuipod/internal/mailer"
	"golang.org/x/crypto/bcrypt"
)

type Handler struct {
	db     *pgxpool.Pool
	mailer *mailer.Mailer
}

// NewHandler constructs an auth Handler backed by the given DB pool and mailer.
func NewHandler(db *pgxpool.Pool, m *mailer.Mailer) *Handler {
	return &Handler{db: db, mailer: m}
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ID           string `json:"id"`
	Email        string `json:"email"`
}

// newAuthResponse issues an access+refresh token pair, persists the refresh JTI, and builds the response.
func (h *Handler) newAuthResponse(ctx context.Context, id, email string) (authResponse, error) {
	access, err := GenerateAccessToken(id)
	if err != nil {
		return authResponse{}, fmt.Errorf("access token: %w", err)
	}
	refresh, jti, err := GenerateRefreshToken(id)
	if err != nil {
		return authResponse{}, fmt.Errorf("refresh token: %w", err)
	}

	if _, err := h.db.Exec(ctx, `
		INSERT INTO refresh_tokens (jti, user_id, expires_at)
		VALUES ($1, $2, NOW() + INTERVAL '30 days')`, jti, id,
	); err != nil {
		return authResponse{}, fmt.Errorf("store refresh token: %w", err)
	}

	return authResponse{AccessToken: access, RefreshToken: refresh, ID: id, Email: email}, nil
}

// Register creates a new account (bcrypt cost 12), sends a verification email asynchronously,
// and returns a token pair.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Email == "" || req.Password == "" {
		httperr.Write(w, http.StatusBadRequest, "email and password required")
		return
	}
	// Server-side floor, not just the clients' UI-level `minLength=8` —
	// direct API calls bypass client-side validation entirely. 72 is
	// bcrypt's own input cap (GenerateFromPassword silently truncates
	// anything longer, which would otherwise mean e.g. "password"+50 random
	// chars and "password"+51 different random chars hash identically).
	if len(req.Password) < 8 {
		httperr.Write(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if len(req.Password) > 72 {
		httperr.Write(w, http.StatusBadRequest, "password must be at most 72 characters")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "internal error")
		return
	}

	var id string
	if err := h.db.QueryRow(r.Context(),
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		req.Email, string(hash),
	).Scan(&id); err != nil {
		httperr.Write(w, http.StatusConflict, "email already in use")
		return
	}

	// send verification email (non-blocking — failure doesn't abort registration)
	go h.sendVerificationEmail(context.Background(), id, req.Email)

	resp, err := h.newAuthResponse(r.Context(), id, req.Email)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// Login verifies email/password, rejects suspended accounts, and returns a new token pair.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, http.StatusBadRequest, "invalid body")
		return
	}

	var id, hash, email string
	var verified bool
	var suspendedAt *time.Time
	if err := h.db.QueryRow(r.Context(),
		`SELECT id, password_hash, email, email_verified, suspended_at FROM users WHERE email = $1`, req.Email,
	).Scan(&id, &hash, &email, &verified, &suspendedAt); err != nil {
		httperr.Write(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		httperr.Write(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if suspendedAt != nil {
		httperr.Write(w, http.StatusForbidden, "account suspended")
		return
	}

	resp, err := h.newAuthResponse(r.Context(), id, email)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh validates the refresh token against the JWT and DB (JTI not revoked/expired)
// and issues a new token pair.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		httperr.Write(w, http.StatusBadRequest, "refresh_token required")
		return
	}

	claims, err := ValidateToken(req.RefreshToken)
	if err != nil || claims.TokenType != "refresh" {
		httperr.Write(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	// verify JTI is in DB and not revoked
	var count int
	if err := h.db.QueryRow(r.Context(), `
		SELECT COUNT(*) FROM refresh_tokens
		WHERE jti = $1 AND revoked_at IS NULL AND expires_at > NOW()`,
		claims.JTI,
	).Scan(&count); err != nil || count == 0 {
		httperr.Write(w, http.StatusUnauthorized, "token revoked or expired")
		return
	}

	var email string
	if err := h.db.QueryRow(r.Context(),
		`SELECT email FROM users WHERE id = $1`, claims.UserID,
	).Scan(&email); err != nil {
		httperr.Write(w, http.StatusUnauthorized, "user not found")
		return
	}

	resp, err := h.newAuthResponse(r.Context(), claims.UserID, email)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Logout revokes the given refresh token (JTI). Always responds 204, even for an invalid
// token, to avoid leaking information.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		httperr.Write(w, http.StatusBadRequest, "refresh_token required")
		return
	}

	claims, err := ValidateToken(req.RefreshToken)
	if err != nil || claims.TokenType != "refresh" || claims.JTI == "" {
		// even invalid tokens get a 204 — no information leakage
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.db.Exec(r.Context(),
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE jti = $1`, claims.JTI)

	w.WriteHeader(http.StatusNoContent)
}

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

// ForgotPassword creates a password reset token and emails it. Always responds 204
// regardless of whether the email exists, to avoid leaking account existence.
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req forgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		httperr.Write(w, http.StatusBadRequest, "email required")
		return
	}

	var userID, email string
	if err := h.db.QueryRow(r.Context(),
		`SELECT id, email FROM users WHERE email = $1`, req.Email,
	).Scan(&userID, &email); err != nil {
		// always 204 — don't leak whether email exists
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var token string
	if err := h.db.QueryRow(r.Context(), `
		INSERT INTO password_resets (user_id, expires_at)
		VALUES ($1, NOW() + INTERVAL '1 hour')
		RETURNING token`,
		userID,
	).Scan(&token); err != nil {
		httperr.Write(w, http.StatusInternalServerError, "internal error")
		return
	}

	webURL := os.Getenv("WEB_URL")
	body := fmt.Sprintf(
		"Reset your TUIPOD password:\n\n%s/reset-password?token=%s\n\nThis link expires in 1 hour.",
		webURL, token,
	)
	go h.mailer.Send(email, "Reset your TUIPOD password", body)

	w.WriteHeader(http.StatusNoContent)
}

type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ResetPassword sets a new password given a valid reset token, then revokes all of the
// user's refresh tokens.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" || req.NewPassword == "" {
		httperr.Write(w, http.StatusBadRequest, "token and new_password required")
		return
	}
	if len(req.NewPassword) < 8 || len(req.NewPassword) > 72 {
		httperr.Write(w, http.StatusBadRequest, "password must be 8-72 characters")
		return
	}

	var userID string
	if err := h.db.QueryRow(r.Context(), `
		SELECT user_id FROM password_resets
		WHERE token = $1 AND used_at IS NULL AND expires_at > NOW()`,
		req.Token,
	).Scan(&userID); err != nil {
		httperr.Write(w, http.StatusBadRequest, "invalid or expired token")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		httperr.Write(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.db.Exec(r.Context(),
		`UPDATE users SET password_hash = $1 WHERE id = $2`, string(hash), userID)
	h.db.Exec(r.Context(),
		`UPDATE password_resets SET used_at = NOW() WHERE token = $1`, req.Token)
	// revoke all existing refresh tokens
	h.db.Exec(r.Context(),
		`UPDATE refresh_tokens SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, userID)

	w.WriteHeader(http.StatusNoContent)
}

// VerifyEmail marks the email verified for the token passed in the query string.
func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		httperr.Write(w, http.StatusBadRequest, "token required")
		return
	}

	var userID string
	if err := h.db.QueryRow(r.Context(), `
		SELECT user_id FROM email_verifications
		WHERE token = $1 AND used_at IS NULL AND expires_at > NOW()`,
		token,
	).Scan(&userID); err != nil {
		httperr.Write(w, http.StatusBadRequest, "invalid or expired token")
		return
	}

	h.db.Exec(r.Context(), `UPDATE users SET email_verified = TRUE WHERE id = $1`, userID)
	h.db.Exec(r.Context(),
		`UPDATE email_verifications SET used_at = NOW() WHERE token = $1`, token)

	w.WriteHeader(http.StatusNoContent)
}

// sendVerificationEmail creates a verification token and emails it; called as a goroutine from Register.
func (h *Handler) sendVerificationEmail(ctx context.Context, userID, email string) {
	var token string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO email_verifications (user_id, expires_at)
		VALUES ($1, NOW() + INTERVAL '24 hours')
		RETURNING token`,
		userID,
	).Scan(&token); err != nil {
		return
	}

	webURL := os.Getenv("WEB_URL")
	body := fmt.Sprintf(
		"Welcome to TUIPOD! Verify your email:\n\n%s/verify-email?token=%s\n\nThis link expires in 24 hours.",
		webURL, token,
	)
	h.mailer.Send(email, "Verify your TUIPOD email", body)
}
