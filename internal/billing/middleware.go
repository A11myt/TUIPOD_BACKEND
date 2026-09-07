package billing

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/httperr"
)

// RequirePlan requires the authenticated user's plan to be one of the given plans
// (e.g. RequirePlan(db, "basic", "pro") for Sync-gated routes, RequirePlan(db, "pro")
// for Pro-only routes). Responds 402 Payment Required otherwise.
func RequirePlan(db *pgxpool.Pool, plans ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(plans))
	for _, p := range plans {
		allowed[p] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := auth.UserIDFromCtx(r)
			var plan string
			if err := db.QueryRow(r.Context(),
				`SELECT plan FROM users WHERE id = $1`, userID,
			).Scan(&plan); err != nil || !allowed[plan] {
				httperr.Write(w, http.StatusPaymentRequired, "upgrade required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
