package favorite_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/A11myt/tuipod/internal/favorite"
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

func TestAdd_Success(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "fav@example.com", "password")
	podcastID := seedPodcast(t, pool, "https://favfeed1.example.com")
	h := favorite.NewHandler(pool)

	body, _ := json.Marshal(map[string]string{"podcast_id": podcastID})
	req := testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID)
	rr := httptest.NewRecorder()
	h.Add(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAdd_Duplicate(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "favdup@example.com", "password")
	podcastID := seedPodcast(t, pool, "https://favfeed2.example.com")
	h := favorite.NewHandler(pool)

	body, _ := json.Marshal(map[string]string{"podcast_id": podcastID})
	h.Add(httptest.NewRecorder(),
		testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))

	body, _ = json.Marshal(map[string]string{"podcast_id": podcastID})
	rr := httptest.NewRecorder()
	h.Add(rr, testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

func TestList_EmptyReturnsArray(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "favlist@example.com", "password")
	h := favorite.NewHandler(pool)

	rr := httptest.NewRecorder()
	h.List(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var favs []any
	json.NewDecoder(rr.Body).Decode(&favs)
	if favs == nil {
		t.Error("expected [], got null")
	}
}

func TestRemove_Success(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "favremove@example.com", "password")
	podcastID := seedPodcast(t, pool, "https://favfeed3.example.com")
	h := favorite.NewHandler(pool)

	body, _ := json.Marshal(map[string]string{"podcast_id": podcastID})
	rr := httptest.NewRecorder()
	h.Add(rr, testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID))

	var addResp map[string]any
	json.NewDecoder(rr.Body).Decode(&addResp)
	favID := addResp["id"].(string)

	router := chi.NewRouter()
	router.Delete("/{id}", h.Remove)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, testutil.WithUser(
		httptest.NewRequest(http.MethodDelete, "/"+favID, nil), userID,
	))

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}
