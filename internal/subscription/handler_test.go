package subscription_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/subscription"
	"github.com/A11myt/tuipod/internal/testutil"
)

func seedPodcast(t *testing.T, pool *pgxpool.Pool, rssURL string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO podcasts (rss_url, title) VALUES ($1, 'Test') RETURNING id`, rssURL,
	).Scan(&id); err != nil {
		t.Fatalf("seed podcast: %v", err)
	}
	return id
}

func TestSubscribe_Success(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "sub@example.com", "password")
	podcastID := seedPodcast(t, pool, "https://feed1.example.com")
	h := subscription.NewHandler(pool)

	body, _ := json.Marshal(map[string]string{"podcast_id": podcastID})
	req := testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID)
	rr := httptest.NewRecorder()
	h.Subscribe(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubscribe_Duplicate(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "dup@example.com", "password")
	podcastID := seedPodcast(t, pool, "https://feed2.example.com")
	h := subscription.NewHandler(pool)

	body, _ := json.Marshal(map[string]string{"podcast_id": podcastID})
	h.Subscribe(httptest.NewRecorder(),
		testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))

	body, _ = json.Marshal(map[string]string{"podcast_id": podcastID})
	rr := httptest.NewRecorder()
	h.Subscribe(rr, testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

func TestList_EmptyReturnsArray(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "list@example.com", "password")
	h := subscription.NewHandler(pool)

	rr := httptest.NewRecorder()
	h.List(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var subs []any
	json.NewDecoder(rr.Body).Decode(&subs)
	if subs == nil {
		t.Error("expected [], got null")
	}
}

// TestList_IncludesLastFetchedAndFavorite guards the fields added for the
// TUI's stale-refresh-on-startup and (separately) fixing its favorite
// toggle, which called a /subscriptions/{id}/favorite endpoint that no
// longer exists now that favorites are their own resource.
func TestList_IncludesLastFetchedAndFavorite(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "listfields@example.com", "password")
	podcastID := seedPodcast(t, pool, "https://listfields.example.com")

	var subID string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO subscriptions (user_id, podcast_id) VALUES ($1, $2) RETURNING id`,
		userID, podcastID,
	).Scan(&subID); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	var favoriteID string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO favorites (user_id, podcast_id) VALUES ($1, $2) RETURNING id`,
		userID, podcastID,
	).Scan(&favoriteID); err != nil {
		t.Fatalf("seed favorite: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE podcasts SET last_fetched = NOW() WHERE id = $1`, podcastID,
	); err != nil {
		t.Fatalf("set last_fetched: %v", err)
	}

	h := subscription.NewHandler(pool)
	rr := httptest.NewRecorder()
	h.List(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var subs []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&subs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	s := subs[0]
	if s["last_fetched"] == nil {
		t.Error("expected last_fetched to be non-null")
	}
	if fav, _ := s["favorite"].(bool); !fav {
		t.Errorf("expected favorite=true, got %v", s["favorite"])
	}
	if got, _ := s["favorite_id"].(string); got != favoriteID {
		t.Errorf("expected favorite_id=%q, got %q", favoriteID, got)
	}
}

// TestList_IncludesImageValidators checks the cover image's ETag/Last-
// Modified (captured server-side during feed refresh, see
// podcast.fetchImageValidators) reaches the client — this is what lets a
// client tell whether its locally cached copy of the image is stale
// without re-downloading it.
func TestList_IncludesImageValidators(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "imagevalidators@example.com", "password")
	podcastID := seedPodcast(t, pool, "https://imagevalidators.example.com")

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO subscriptions (user_id, podcast_id) VALUES ($1, $2)`,
		userID, podcastID,
	); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE podcasts SET image_etag = $1, image_last_modified = $2 WHERE id = $3`,
		`"abc123"`, "Mon, 01 Jan 2024 00:00:00 GMT", podcastID,
	); err != nil {
		t.Fatalf("set image validators: %v", err)
	}

	h := subscription.NewHandler(pool)
	rr := httptest.NewRecorder()
	h.List(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var subs []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&subs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	s := subs[0]
	if got, _ := s["image_etag"].(string); got != `"abc123"` {
		t.Errorf("image_etag = %q, want %q", got, `"abc123"`)
	}
	if got, _ := s["image_last_modified"].(string); got != "Mon, 01 Jan 2024 00:00:00 GMT" {
		t.Errorf("image_last_modified = %q, want %q", got, "Mon, 01 Jan 2024 00:00:00 GMT")
	}
}

// TestList_PerPodcastStats guards the stats aggregation added for the TUI's
// subs-grid "listener stats" cards: two podcasts, one with progress, one
// without, and a *third* user's progress on the same podcasts that must not
// leak in — confirms grouping is correct per podcast AND scoped per user,
// not just "does it return some numbers".
func TestList_PerPodcastStats(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "stats-owner@example.com", "password")
	otherUserID := testutil.CreateUser(t, pool, "stats-other@example.com", "password")

	podWithStats := seedPodcast(t, pool, "https://stats-a.example.com")
	podWithoutStats := seedPodcast(t, pool, "https://stats-b.example.com")

	ep1 := testutil.CreateEpisode(t, pool, podWithStats)
	ep2 := testutil.CreateEpisode(t, pool, podWithStats)
	testutil.CreateEpisode(t, pool, podWithStats) // no progress — exercises "remaining"
	testutil.CreateEpisode(t, pool, podWithoutStats)
	testutil.CreateEpisode(t, pool, podWithoutStats)

	for _, sub := range []struct{ user, podcast string }{
		{userID, podWithStats}, {userID, podWithoutStats}, {otherUserID, podWithStats},
	} {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO subscriptions (user_id, podcast_id) VALUES ($1, $2)`, sub.user, sub.podcast,
		); err != nil {
			t.Fatalf("seed subscription: %v", err)
		}
	}

	// owner: 1 completed (600s) + 1 in-progress (120s) on podWithStats
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO progress (user_id, episode_id, position_seconds, completed) VALUES ($1, $2, 600, true)`,
		userID, ep1,
	); err != nil {
		t.Fatalf("seed progress 1: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO progress (user_id, episode_id, position_seconds, completed) VALUES ($1, $2, 120, false)`,
		userID, ep2,
	); err != nil {
		t.Fatalf("seed progress 2: %v", err)
	}
	// a different user's progress on the SAME episodes — must not be counted
	// in the owner's stats
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO progress (user_id, episode_id, position_seconds, completed) VALUES ($1, $2, 9999, true)`,
		otherUserID, ep1,
	); err != nil {
		t.Fatalf("seed other-user progress: %v", err)
	}

	h := subscription.NewHandler(pool)
	rr := httptest.NewRecorder()
	h.List(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var subs []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&subs); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byPodcast := map[string]map[string]any{}
	for _, s := range subs {
		byPodcast[s["podcast_id"].(string)] = s
	}

	withStats, ok := byPodcast[podWithStats]
	if !ok {
		t.Fatalf("missing subscription for podWithStats in response")
	}
	if got := withStats["completed_episodes"].(float64); got != 1 {
		t.Errorf("expected completed_episodes=1, got %v", got)
	}
	if got := withStats["in_progress_episodes"].(float64); got != 1 {
		t.Errorf("expected in_progress_episodes=1, got %v", got)
	}
	if got := withStats["total_listened_seconds"].(float64); got != 720 {
		t.Errorf("expected total_listened_seconds=720 (600+120, NOT the other user's 9999), got %v", got)
	}
	if got := withStats["total_episodes"].(float64); got != 3 {
		t.Errorf("expected total_episodes=3 (1 completed + 1 in-progress + 1 untouched), got %v", got)
	}

	withoutStats, ok := byPodcast[podWithoutStats]
	if !ok {
		t.Fatalf("missing subscription for podWithoutStats in response")
	}
	if got := withoutStats["completed_episodes"].(float64); got != 0 {
		t.Errorf("expected completed_episodes=0 for a podcast with no progress, got %v", got)
	}
	if got := withoutStats["total_episodes"].(float64); got != 2 {
		t.Errorf("expected total_episodes=2 (episodes exist even with zero progress), got %v", got)
	}
	if got := withoutStats["total_listened_seconds"].(float64); got != 0 {
		t.Errorf("expected total_listened_seconds=0 for a podcast with no progress, got %v", got)
	}
}

func TestUnsubscribe_Success(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "unsub@example.com", "password")
	podcastID := seedPodcast(t, pool, "https://feed3.example.com")
	h := subscription.NewHandler(pool)

	body, _ := json.Marshal(map[string]string{"podcast_id": podcastID})
	rr := httptest.NewRecorder()
	h.Subscribe(rr, testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))

	var subResp map[string]any
	json.NewDecoder(rr.Body).Decode(&subResp)
	subID := subResp["id"].(string)

	router := chi.NewRouter()
	router.Delete("/{id}", h.Unsubscribe)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, testutil.WithUser(
		httptest.NewRequest(http.MethodDelete, "/"+subID, nil), userID,
	))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}
