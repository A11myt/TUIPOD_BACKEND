package testutil

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// init sets JWT_SECRET for tests at package load time, and points
// db.RunMigrations at an absolute migrations directory.
//
// db.RunMigrations reads the relative path "migrations" by default (or
// MIGRATIONS_DIR if set) — fine for cmd/server, which always runs from the
// repo root, but `go test` runs each package's test binary with that
// package's own source directory as its working directory, not the repo
// root. Without this, `go test ./internal/<anything>/...` (and therefore
// `go test ./...`/`make test`) fails every single integration test with
// "read migrations dir \"migrations\": no such file or directory" the
// moment it's run from anywhere but TUIPOD-BACKEND/ itself directly — this
// derives the path from this file's own location instead, so it works
// regardless of which package's tests are being run or from where `go test`
// itself was invoked.
func init() {
	os.Setenv("JWT_SECRET", "test-secret-for-tuipod-tests")
	if os.Getenv("MIGRATIONS_DIR") == "" {
		_, thisFile, _, ok := runtime.Caller(0)
		if ok {
			// thisFile: <repo root>/internal/testutil/testutil.go
			repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
			os.Setenv("MIGRATIONS_DIR", filepath.Join(repoRoot, "migrations"))
		}
	}
}

// Pool connects to TEST_DATABASE_URL, runs migrations, and truncates all app
// tables. The test is skipped if TEST_DATABASE_URL is not set.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}

	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}

	if err := db.RunMigrations(context.Background(), pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	clean(pool)
	t.Cleanup(func() {
		clean(pool)
		pool.Close()
	})

	return pool
}

// clean truncates all app tables. podcasts/episodes don't reference users, so they're
// truncated explicitly alongside it, not just cascaded from it.
func clean(pool *pgxpool.Pool) {
	pool.Exec(context.Background(), `TRUNCATE users, podcasts CASCADE`)
}

// CreateUser inserts a user directly and returns their UUID.
func CreateUser(t *testing.T, pool *pgxpool.Pool, email, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 4) // cost 4 for speed
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		email, string(hash),
	).Scan(&id); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

// CreateAdminUser creates a user with is_admin = TRUE.
func CreateAdminUser(t *testing.T, pool *pgxpool.Pool, email, password string) string {
	t.Helper()
	id := CreateUser(t, pool, email, password)
	pool.Exec(context.Background(), `UPDATE users SET is_admin = TRUE WHERE id = $1`, id)
	return id
}

// CreatePodcast inserts a minimal podcast row and returns its UUID.
func CreatePodcast(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO podcasts (rss_url, title, author)
		 VALUES ('http://test.invalid/feed-'||gen_random_uuid()||'.xml', 'Test Podcast', 'Tester')
		 RETURNING id`,
	).Scan(&id); err != nil {
		t.Fatalf("create podcast: %v", err)
	}
	return id
}

// CreateEpisode inserts a minimal episode and returns its UUID.
func CreateEpisode(t *testing.T, pool *pgxpool.Pool, podcastID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO episodes (podcast_id, title, audio_url)
		 VALUES ($1, 'Test Episode', 'http://test.invalid/ep-'||gen_random_uuid()||'.mp3')
		 RETURNING id`,
		podcastID,
	).Scan(&id); err != nil {
		t.Fatalf("create episode: %v", err)
	}
	return id
}

// AccessToken generates a valid JWT access token for the given user ID.
func AccessToken(t *testing.T, userID string) string {
	t.Helper()
	token, err := auth.GenerateAccessToken(userID)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	return token
}

// WithUser injects a userID into the request context, bypassing JWT middleware.
func WithUser(r *http.Request, userID string) *http.Request {
	ctx := context.WithValue(r.Context(), auth.UserIDKey, userID)
	return r.WithContext(ctx)
}
