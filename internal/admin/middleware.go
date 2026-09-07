package admin

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/httperr"
)

// Middleware requires the authenticated user to have is_admin = TRUE.
func Middleware(db *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := auth.UserIDFromCtx(r)
			var isAdmin bool
			if err := db.QueryRow(r.Context(),
				`SELECT is_admin FROM users WHERE id = $1`, userID,
			).Scan(&isAdmin); err != nil || !isAdmin {
				httperr.Write(w, http.StatusForbidden, "admin access required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
