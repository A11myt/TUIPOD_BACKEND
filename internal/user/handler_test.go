package user_test

import (
	"bytes"
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

// seedEpisodeWithTranscript creates a podcast + episode (with the given
// transcript fields set) and returns the episode ID.
func seedEpisodeWithTranscript(t *testing.T, pool *pgxpool.Pool, transcriptURL, transcriptType string) string {
	t.Helper()
	podcastID := testutil.CreatePodcast(t, pool)
	episodeID := testutil.CreateEpisode(t, pool, podcastID)
	if _, err := pool.Exec(context.Background(),
		`UPDATE episodes SET transcript_url = $1, transcript_type = $2 WHERE id = $3`,
		transcriptURL, transcriptType, episodeID,
	); err != nil {
		t.Fatalf("set transcript: %v", err)
	}
	return episodeID
}

func subscribe(t *testing.T, pool *pgxpool.Pool, userID, episodeID string) {
	t.Helper()
	var podcastID string
	if err := pool.QueryRow(context.Background(),
		`SELECT podcast_id FROM episodes WHERE id = $1`, episodeID,
	).Scan(&podcastID); err != nil {
		t.Fatalf("look up podcast_id: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO subscriptions (user_id, podcast_id) VALUES ($1, $2)`, userID, podcastID,
	); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
}

// TestFeed_IncludesTranscript checks a transcript published by the feed
// (see podcast.pickTranscript) reaches this endpoint's response.
func TestFeed_IncludesTranscript(t *testing.T) {
	h, pool, userID := setup(t)
	episodeID := seedEpisodeWithTranscript(t, pool, "https://x/ep.txt", "text/plain")
	subscribe(t, pool, userID, episodeID)

	rr := httptest.NewRecorder()
	h.Feed(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var items []map[string]any
	json.NewDecoder(rr.Body).Decode(&items)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0]["transcript_url"] != "https://x/ep.txt" || items[0]["transcript_type"] != "text/plain" {
		t.Errorf("expected transcript fields, got %+v", items[0])
	}
}

func TestContinueListening_IncludesTranscript(t *testing.T) {
	h, pool, userID := setup(t)
	episodeID := seedEpisodeWithTranscript(t, pool, "https://x/ep.txt", "text/plain")
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO progress (user_id, episode_id, position_seconds, completed) VALUES ($1, $2, 30, false)`,
		userID, episodeID,
	); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	rr := httptest.NewRecorder()
	h.ContinueListening(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var items []map[string]any
	json.NewDecoder(rr.Body).Decode(&items)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0]["transcript_url"] != "https://x/ep.txt" || items[0]["transcript_type"] != "text/plain" {
		t.Errorf("expected transcript fields, got %+v", items[0])
	}
}

func TestHistory_IncludesTranscript(t *testing.T) {
	h, pool, userID := setup(t)
	episodeID := seedEpisodeWithTranscript(t, pool, "https://x/ep.txt", "text/plain")
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO progress (user_id, episode_id, position_seconds, completed) VALUES ($1, $2, 100, true)`,
		userID, episodeID,
	); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	rr := httptest.NewRecorder()
	h.History(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var items []map[string]any
	json.NewDecoder(rr.Body).Decode(&items)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0]["transcript_url"] != "https://x/ep.txt" || items[0]["transcript_type"] != "text/plain" {
		t.Errorf("expected transcript fields, got %+v", items[0])
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

func TestChangePassword_Success(t *testing.T) {
	h, _, userID := setup(t)

	body, _ := json.Marshal(map[string]string{"current_password": "password", "new_password": "newpassword123"})
	req := testutil.WithUser(httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body)), userID)
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

func TestChangePassword_WrongCurrentPassword(t *testing.T) {
	h, _, userID := setup(t)

	body, _ := json.Marshal(map[string]string{"current_password": "notthepassword", "new_password": "newpassword123"})
	req := testutil.WithUser(httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body)), userID)
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// TestChangePassword_NewPasswordTooShort is a regression test for a gap
// where ChangePassword only checked the new password was non-empty — no
// server-side floor beyond that, unlike the 8-char minimum every client's
// form enforces (trivially bypassed by calling the API directly).
func TestChangePassword_NewPasswordTooShort(t *testing.T) {
	h, _, userID := setup(t)

	body, _ := json.Marshal(map[string]string{"current_password": "password", "new_password": "short1"})
	req := testutil.WithUser(httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body)), userID)
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}
