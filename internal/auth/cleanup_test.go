package auth_test

import (
	"context"
	"testing"

	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/testutil"
)

// TestRunTokenCleanup_DeletesOnlyExpired seeds one expired and one
// not-yet-expired row per token table, runs a single cleanup pass, and
// checks only the expired ones are gone — a regression test for the gap
// where nothing ever pruned these tables at all (see cleanup.go's doc
// comment).
func TestRunTokenCleanup_DeletesOnlyExpired(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "cleanup@example.com", "password")

	seed := func(table string) {
		t.Helper()
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO `+table+` (user_id, expires_at) VALUES ($1, NOW() - INTERVAL '1 hour')`,
			userID,
		); err != nil {
			t.Fatalf("seed expired %s: %v", table, err)
		}
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO `+table+` (user_id, expires_at) VALUES ($1, NOW() + INTERVAL '1 hour')`,
			userID,
		); err != nil {
			t.Fatalf("seed unexpired %s: %v", table, err)
		}
	}
	for _, table := range []string{"refresh_tokens", "password_resets", "email_verifications"} {
		seed(table)
	}

	auth.RunOnceForTests(context.Background(), pool)

	for _, table := range []string{"refresh_tokens", "password_resets", "email_verifications"} {
		var count int
		if err := pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM `+table+` WHERE user_id = $1`, userID,
		).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("%s: expected 1 row left (the unexpired one), got %d", table, count)
		}
	}
}
