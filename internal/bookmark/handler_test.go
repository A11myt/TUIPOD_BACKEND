package bookmark_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/A11myt/tuipod/internal/bookmark"
	"github.com/A11myt/tuipod/internal/testutil"
)

func TestCreate_Success(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "bm-create@example.com", "password")
	podcastID := testutil.CreatePodcast(t, pool)
	episodeID := testutil.CreateEpisode(t, pool, podcastID)
	h := bookmark.NewHandler(pool)

	body, _ := json.Marshal(map[string]any{"episode_id": episodeID, "position_seconds": 90, "note": "good bit"})
	req := testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreate_MissingEpisodeID(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "bm-missing@example.com", "password")
	h := bookmark.NewHandler(pool)

	body, _ := json.Marshal(map[string]any{"position_seconds": 10})
	rr := httptest.NewRecorder()
	h.Create(rr, testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestCreate_UnknownEpisode(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "bm-unknown@example.com", "password")
	h := bookmark.NewHandler(pool)

	body, _ := json.Marshal(map[string]any{"episode_id": "00000000-0000-0000-0000-000000000000", "position_seconds": 10})
	rr := httptest.NewRecorder()
	h.Create(rr, testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a nonexistent episode, got %d", rr.Code)
	}
}

// TestList_IncludesEpisodeAndPodcastInfo checks the response carries enough
// context (title, audio URL, duration, podcast name/image) for a client to
// jump straight into playback — the whole reason it's a joined query
// instead of just returning the bare bookmarks table.
func TestList_IncludesEpisodeAndPodcastInfo(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "bm-list@example.com", "password")
	podcastID := testutil.CreatePodcast(t, pool)
	episodeID := testutil.CreateEpisode(t, pool, podcastID)
	if _, err := pool.Exec(context.Background(),
		`UPDATE episodes SET transcript_url = $1, transcript_type = $2 WHERE id = $3`,
		"https://x/ep.txt", "text/plain", episodeID,
	); err != nil {
		t.Fatalf("set transcript: %v", err)
	}
	h := bookmark.NewHandler(pool)

	body, _ := json.Marshal(map[string]any{"episode_id": episodeID, "position_seconds": 42, "note": "check this"})
	h.Create(httptest.NewRecorder(),
		testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))

	rr := httptest.NewRecorder()
	h.List(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var bookmarks []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&bookmarks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(bookmarks) != 1 {
		t.Fatalf("expected 1 bookmark, got %d", len(bookmarks))
	}
	b := bookmarks[0]
	if b["episode_id"] != episodeID {
		t.Errorf("episode_id = %v, want %v", b["episode_id"], episodeID)
	}
	if b["title"] != "Test Episode" {
		t.Errorf("title = %v, want %q", b["title"], "Test Episode")
	}
	if b["podcast_title"] != "Test Podcast" {
		t.Errorf("podcast_title = %v, want %q", b["podcast_title"], "Test Podcast")
	}
	if got, _ := b["position_seconds"].(float64); got != 42 {
		t.Errorf("position_seconds = %v, want 42", b["position_seconds"])
	}
	if b["note"] != "check this" {
		t.Errorf("note = %v, want %q", b["note"], "check this")
	}
	if b["transcript_url"] != "https://x/ep.txt" || b["transcript_type"] != "text/plain" {
		t.Errorf("expected transcript fields, got transcript_url=%v transcript_type=%v", b["transcript_url"], b["transcript_type"])
	}
	if b["audio_url"] == "" || b["audio_url"] == nil {
		t.Error("expected a non-empty audio_url")
	}
}

// TestList_ScopedToUser checks one user's bookmarks never leak into
// another's list, even on the same episode.
func TestList_ScopedToUser(t *testing.T) {
	pool := testutil.Pool(t)
	userA := testutil.CreateUser(t, pool, "bm-user-a@example.com", "password")
	userB := testutil.CreateUser(t, pool, "bm-user-b@example.com", "password")
	podcastID := testutil.CreatePodcast(t, pool)
	episodeID := testutil.CreateEpisode(t, pool, podcastID)
	h := bookmark.NewHandler(pool)

	body, _ := json.Marshal(map[string]any{"episode_id": episodeID, "position_seconds": 5})
	h.Create(httptest.NewRecorder(),
		testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userA))

	rr := httptest.NewRecorder()
	h.List(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userB))
	var bookmarks []any
	json.NewDecoder(rr.Body).Decode(&bookmarks)
	if len(bookmarks) != 0 {
		t.Errorf("expected user B to see 0 bookmarks, got %d", len(bookmarks))
	}
}

func TestList_EmptyReturnsArray(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "bm-empty@example.com", "password")
	h := bookmark.NewHandler(pool)

	rr := httptest.NewRecorder()
	h.List(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var bookmarks []any
	json.NewDecoder(rr.Body).Decode(&bookmarks)
	if bookmarks == nil {
		t.Error("expected [], got null")
	}
}

func TestDelete_Success(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "bm-delete@example.com", "password")
	podcastID := testutil.CreatePodcast(t, pool)
	episodeID := testutil.CreateEpisode(t, pool, podcastID)
	h := bookmark.NewHandler(pool)

	body, _ := json.Marshal(map[string]any{"episode_id": episodeID, "position_seconds": 5})
	rr := httptest.NewRecorder()
	h.Create(rr, testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))
	var createResp map[string]any
	json.NewDecoder(rr.Body).Decode(&createResp)
	bookmarkID := createResp["id"].(string)

	router := chi.NewRouter()
	router.Delete("/{id}", h.Delete)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, testutil.WithUser(httptest.NewRequest(http.MethodDelete, "/"+bookmarkID, nil), userID))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

// TestDelete_WrongUserNotFound checks a user can't delete someone else's
// bookmark just by knowing its ID.
func TestDelete_WrongUserNotFound(t *testing.T) {
	pool := testutil.Pool(t)
	owner := testutil.CreateUser(t, pool, "bm-owner@example.com", "password")
	other := testutil.CreateUser(t, pool, "bm-other@example.com", "password")
	podcastID := testutil.CreatePodcast(t, pool)
	episodeID := testutil.CreateEpisode(t, pool, podcastID)
	h := bookmark.NewHandler(pool)

	body, _ := json.Marshal(map[string]any{"episode_id": episodeID, "position_seconds": 5})
	rr := httptest.NewRecorder()
	h.Create(rr, testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), owner))
	var createResp map[string]any
	json.NewDecoder(rr.Body).Decode(&createResp)
	bookmarkID := createResp["id"].(string)

	router := chi.NewRouter()
	router.Delete("/{id}", h.Delete)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, testutil.WithUser(httptest.NewRequest(http.MethodDelete, "/"+bookmarkID, nil), other))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when deleting another user's bookmark, got %d", rr.Code)
	}
}
