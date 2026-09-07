package user_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/testutil"
	"github.com/A11myt/tuipod/internal/user"
)

func setup(t *testing.T) (*user.Handler, *pgxpool.Pool, string) {
	t.Helper()
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "user@example.com", "password")
	return user.NewHandler(pool), pool, userID
}

func TestMe_Success(t *testing.T) {
	h, _, userID := setup(t)

	rr := httptest.NewRecorder()
	h.Me(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["email"] != "user@example.com" {
		t.Errorf("expected email, got %v", resp["email"])
	}
}

func TestStats_Empty(t *testing.T) {
	h, _, userID := setup(t)

	rr := httptest.NewRecorder()
	h.Stats(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["completed_episodes"].(float64) != 0 {
		t.Error("expected 0 completed episodes")
	}
	if resp["subscriptions"].(float64) != 0 {
		t.Error("expected 0 subscriptions")
	}
}

func TestStats_WithData(t *testing.T) {
	h, pool, userID := setup(t)

	var podcastID, episodeID string
	pool.QueryRow(context.Background(),
		`INSERT INTO podcasts (rss_url, title) VALUES ('https://stats-feed.example.com', 'P') RETURNING id`,
	).Scan(&podcastID)
	pool.QueryRow(context.Background(),
		`INSERT INTO episodes (podcast_id, title, audio_url) VALUES ($1, 'E', 'https://stats-feed.example.com/e.mp3') RETURNING id`,
		podcastID,
	).Scan(&episodeID)
	pool.Exec(context.Background(),
		`INSERT INTO subscriptions (user_id, podcast_id) VALUES ($1, $2)`, userID, podcastID)
	pool.Exec(context.Background(),
		`INSERT INTO progress (user_id, episode_id, position_seconds, completed) VALUES ($1, $2, 3600, true)`,
		userID, episodeID)

	rr := httptest.NewRecorder()
	h.Stats(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)

	if resp["completed_episodes"].(float64) != 1 {
		t.Errorf("expected 1 completed episode, got %v", resp["completed_episodes"])
	}
	if resp["subscriptions"].(float64) != 1 {
		t.Errorf("expected 1 subscription, got %v", resp["subscriptions"])
	}
	if resp["total_listened_seconds"].(float64) != 3600 {
		t.Errorf("expected 3600s listened, got %v", resp["total_listened_seconds"])
	}
}

func TestFeed_EmptyReturnsArray(t *testing.T) {
	h, _, userID := setup(t)

	rr := httptest.NewRecorder()
	h.Feed(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var items []any
	json.NewDecoder(rr.Body).Decode(&items)
	if items == nil {
		t.Error("expected [], got null")
	}
}
