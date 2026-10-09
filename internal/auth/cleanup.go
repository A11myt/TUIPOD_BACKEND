package auth

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunTokenCleanup deletes expired refresh/password-reset/email-verification
// tokens immediately, then once per interval until ctx is cancelled.
//
// Nothing else in this codebase ever prunes these tables — every login, and
// (per Refresh below) every single token *refresh*, inserts a new
// refresh_tokens row without deleting the old one. Left alone this table
// grows without bound for as long as the app has active users: a single
// continuously-active session mints a new refresh token roughly every 15
// minutes, since that's the access token's lifetime and all three clients
// (TUI/App/Web) transparently refresh on a 401 — see this repo's CLAUDE.md
// "Session lifetime" note. Meant to be run from cmd/worker alongside the
// podcast refresher, not cmd/server — same "exactly one process doing this
// on a schedule" reasoning, not because running it from multiple replicas
// would be unsafe (a DELETE ... WHERE expires_at < NOW() is naturally
// idempotent).
func RunTokenCleanup(ctx context.Context, pool *pgxpool.Pool, interval time.Duration) {
	cleanupExpiredTokens(ctx, pool)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cleanupExpiredTokens(ctx, pool)
		case <-ctx.Done():
			return
		}
	}
}

// RunOnceForTests runs a single cleanup pass and returns — exported only so
// this package's tests can exercise cleanupExpiredTokens without looping
// forever on a ticker the way RunTokenCleanup does.
func RunOnceForTests(ctx context.Context, pool *pgxpool.Pool) {
	cleanupExpiredTokens(ctx, pool)
}

// cleanupExpiredTokens deletes rows from the three token tables whose
// expires_at has already passed. Revoked-but-not-yet-expired rows are left
// alone on purpose — expires_at is the only signal every one of these
// tables shares, and a revoked refresh token is still useful audit trail
// until it would have expired anyway.
func cleanupExpiredTokens(ctx context.Context, pool *pgxpool.Pool) {
	deleteExpired(ctx, pool, "refresh_tokens")
	deleteExpired(ctx, pool, "password_resets")
	deleteExpired(ctx, pool, "email_verifications")
}

// deleteExpired runs `DELETE FROM <table> WHERE expires_at < NOW()` — table
// is always one of the three fixed string literals above, never
// user-input, so building the query this way isn't a SQL-injection risk.
func deleteExpired(ctx context.Context, pool *pgxpool.Pool, table string) {
	tag, err := pool.Exec(ctx, `DELETE FROM `+table+` WHERE expires_at < NOW()`)
	if err != nil {
		slog.Error("token cleanup failed", "table", table, "err", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("token cleanup", "table", table, "deleted", n)
	}
}
