package podcast_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/A11myt/tuipod/internal/podcast"
	"github.com/A11myt/tuipod/internal/testutil"
)

const minimalRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
<channel>
  <title>Test Podcast</title>
  <description>A test podcast</description>
  <item>
    <title>Episode One</title>
    <enclosure url="%s/ep1.mp3" type="audio/mpeg" length="0"/>
    <itunes:duration>1:00:00</itunes:duration>
    <pubDate>Mon, 01 Jan 2024 00:00:00 +0000</pubDate>
  </item>
</channel>
</rss>`

// fakeFeed starts an httptest server that serves a minimal RSS feed.
func fakeFeed(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ep1.mp3" {
			w.WriteHeader(http.StatusOK)
			return
		}
		fmt.Fprintf(w, minimalRSS, srv.URL)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setup(t *testing.T) *podcast.Handler {
	t.Helper()
	return podcast.NewHandler(testutil.Pool(t))
}

func TestFetch_Success(t *testing.T) {
	feed := fakeFeed(t)
	h := setup(t)

	body, _ := json.Marshal(map[string]string{"url": feed.URL + "/feed.rss"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Fetch(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["id"] == "" {
		t.Error("expected podcast id in response")
	}
}

func TestFetch_Update_Returns200(t *testing.T) {
	feed := fakeFeed(t)
	h := setup(t)

	body, _ := json.Marshal(map[string]string{"url": feed.URL + "/feed.rss"})
	// first fetch → 201
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Fetch(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first fetch: expected 201, got %d", rr.Code)
	}

	// second fetch → 200
	body, _ = json.Marshal(map[string]string{"url": feed.URL + "/feed.rss"})
	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr = httptest.NewRecorder()
	h.Fetch(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second fetch: expected 200, got %d", rr.Code)
	}
}

func TestFetch_InvalidURL(t *testing.T) {
	h := setup(t)
	body := bytes.NewBufferString(`{"url":"http://127.0.0.1:1/doesnotexist"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()
	h.Fetch(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestGetPodcast_NotFound(t *testing.T) {
	h := setup(t)
	r := chi.NewRouter()
	r.Get("/{id}", h.GetPodcast)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/00000000-0000-0000-0000-000000000000", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestGetEpisodes_EmptyList(t *testing.T) {
	feed := fakeFeed(t)
	h := setup(t)

	// fetch podcast first to get an ID
	body, _ := json.Marshal(map[string]string{"url": feed.URL + "/feed.rss"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Fetch(rr, req)
	var podcast map[string]any
	json.NewDecoder(rr.Body).Decode(&podcast)
	podcastID := podcast["id"].(string)

	r := chi.NewRouter()
	r.Get("/{id}/episodes", h.GetEpisodes)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/"+podcastID+"/episodes", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	// should return an array, not null
	var episodes []any
	json.NewDecoder(rr.Body).Decode(&episodes)
	if episodes == nil {
		t.Error("expected array, got null")
	}
}
