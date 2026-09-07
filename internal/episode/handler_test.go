package episode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/episode"
	"github.com/A11myt/tuipod/internal/testutil"
)

func seedEpisode(t *testing.T, pool *pgxpool.Pool) (podcastID, episodeID string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO podcasts (rss_url, title) VALUES ('https://ep-feed.example.com/'||gen_random_uuid(), 'P') RETURNING id`,
	).Scan(&podcastID); err != nil {
		t.Fatalf("seed podcast: %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO episodes (podcast_id, title, audio_url) VALUES ($1, 'E1', 'https://ep-feed.example.com/e1-'||gen_random_uuid()||'.mp3') RETURNING id`,
		podcastID,
	).Scan(&episodeID); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	return
}

func TestGetProgress_NoProgress(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "prog@example.com", "password")
	_, episodeID := seedEpisode(t, pool)
	h := episode.NewHandler(pool)

	router := chi.NewRouter()
	router.Get("/{id}/progress", h.GetProgress)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, testutil.WithUser(
		httptest.NewRequest(http.MethodGet, "/"+episodeID+"/progress", nil), userID,
	))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["position_seconds"].(float64) != 0 {
		t.Error("expected position_seconds=0 for fresh episode")
	}
}

func TestUpsertProgress_And_Get(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "upsert@example.com", "password")
	_, episodeID := seedEpisode(t, pool)
	h := episode.NewHandler(pool)

	router := chi.NewRouter()
	router.Put("/{id}/progress", h.UpsertProgress)
	router.Get("/{id}/progress", h.GetProgress)

	// upsert
	body, _ := json.Marshal(map[string]any{"position_seconds": 120, "completed": false})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, testutil.WithUser(
		httptest.NewRequest(http.MethodPut, "/"+episodeID+"/progress", bytes.NewReader(body)), userID,
	))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("upsert: expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// get back
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, testutil.WithUser(
		httptest.NewRequest(http.MethodGet, "/"+episodeID+"/progress", nil), userID,
	))
	if rr.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["position_seconds"].(float64) != 120 {
		t.Errorf("expected 120, got %v", resp["position_seconds"])
	}
}

func TestGetEpisode_NotFound(t *testing.T) {
	pool := testutil.Pool(t)
	h := episode.NewHandler(pool)

	router := chi.NewRouter()
	router.Get("/{id}", h.GetEpisode)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(
		http.MethodGet, "/00000000-0000-0000-0000-000000000000", nil,
	))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}
