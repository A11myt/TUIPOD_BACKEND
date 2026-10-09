package podcast_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/A11myt/tuipod/internal/podcast"
	"github.com/A11myt/tuipod/internal/testutil"
)

// TestMain disables the SSRF guard's private-network block for this
// package's tests — every one of them fetches feeds from an
// httptest.NewServer, which always listens on loopback, and the guard
// (safe_client.go) would otherwise correctly refuse to connect to it, the
// same way it refuses to in production. See
// podcast.AllowPrivateNetworksForTests's doc comment.
func TestMain(m *testing.M) {
	podcast.AllowPrivateNetworksForTests = true
	os.Exit(m.Run())
}

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

// untypedEnclosureRSS has one episode whose <enclosure> omits the type
// attribute — some real podcast hosts do this on otherwise-valid feeds.
const untypedEnclosureRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
<channel>
  <title>Untyped Enclosure Podcast</title>
  <description>A test podcast</description>
  <item>
    <title>Episode Without Type Attr</title>
    <enclosure url="%s/ep1.mp3" length="0"/>
    <pubDate>Mon, 01 Jan 2024 00:00:00 +0000</pubDate>
  </item>
</channel>
</rss>`

func fakeUntypedEnclosureFeed(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ep1.mp3" {
			w.WriteHeader(http.StatusOK)
			return
		}
		fmt.Fprintf(w, untypedEnclosureRSS, srv.URL)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFetch_EnclosureWithoutTypeAttrStillImported guards against a real bug:
// episodes were silently skipped whenever <enclosure> had no (or an empty)
// type attribute, since the enclosure's URL is the audio file by RSS
// convention regardless of that attribute being set.
func TestFetch_EnclosureWithoutTypeAttrStillImported(t *testing.T) {
	feed := fakeUntypedEnclosureFeed(t)
	h := setup(t)

	body, _ := json.Marshal(map[string]string{"url": feed.URL + "/feed.rss"})
	rr := httptest.NewRecorder()
	h.Fetch(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	podcastID := resp["id"].(string)

	router := chi.NewRouter()
	router.Get("/{id}/episodes", h.GetEpisodes)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/"+podcastID+"/episodes", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var episodes []map[string]any
	json.NewDecoder(rr.Body).Decode(&episodes)
	if len(episodes) != 1 {
		t.Fatalf("expected 1 episode imported despite missing enclosure type attr, got %d", len(episodes))
	}
}

func setup(t *testing.T) *podcast.Handler {
	t.Helper()
	return podcast.NewHandler(testutil.Pool(t))
}

// imageRSS is minimalRSS plus a channel <image>, whose URL the test server
// resolves and answers HEAD requests for with ETag/Last-Modified — for
// exercising fetchImageValidators, which minimalRSS's imageless feed can't.
const imageRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
<channel>
  <title>Test Podcast With Image</title>
  <description>A test podcast</description>
  <image><url>%[1]s/cover.jpg</url></image>
  <item>
    <title>Episode One</title>
    <enclosure url="%[1]s/ep1.mp3" type="audio/mpeg" length="0"/>
    <pubDate>Mon, 01 Jan 2024 00:00:00 +0000</pubDate>
  </item>
</channel>
</rss>`

// fakeFeedWithImage starts an httptest server serving imageRSS, whose cover
// image responds to HEAD with the given ETag/Last-Modified.
func fakeFeedWithImage(t *testing.T, etag, lastModified string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ep1.mp3":
			w.WriteHeader(http.StatusOK)
		case "/cover.jpg":
			if etag != "" {
				w.Header().Set("ETag", etag)
			}
			if lastModified != "" {
				w.Header().Set("Last-Modified", lastModified)
			}
			w.WriteHeader(http.StatusOK)
		default:
			fmt.Fprintf(w, imageRSS, srv.URL)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFetch_CapturesImageValidators checks that Fetch stores the cover
// image's ETag/Last-Modified (via fetchImageValidators) on the podcast row
// — the signal clients use to tell whether a locally cached copy of the
// image is stale without re-downloading it.
func TestFetch_CapturesImageValidators(t *testing.T) {
	feed := fakeFeedWithImage(t, `"abc123"`, "Mon, 01 Jan 2024 00:00:00 GMT")
	pool := testutil.Pool(t)
	h := podcast.NewHandler(pool)

	body, _ := json.Marshal(map[string]string{"url": feed.URL + "/feed.rss"})
	rr := httptest.NewRecorder()
	h.Fetch(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	podcastID := resp["id"].(string)

	var etag, lastMod string
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(image_etag,''), COALESCE(image_last_modified,'') FROM podcasts WHERE id = $1`,
		podcastID,
	).Scan(&etag, &lastMod); err != nil {
		t.Fatalf("query podcast: %v", err)
	}
	if etag != `"abc123"` {
		t.Errorf("image_etag = %q, want %q", etag, `"abc123"`)
	}
	if lastMod != "Mon, 01 Jan 2024 00:00:00 GMT" {
		t.Errorf("image_last_modified = %q, want %q", lastMod, "Mon, 01 Jan 2024 00:00:00 GMT")
	}
}

// TestFetch_NoImageValidatorsIsNotFatal checks a feed with no cover image
// (or an image host that doesn't send ETag/Last-Modified) still imports
// successfully — fetchImageValidators is explicitly best-effort.
func TestFetch_NoImageValidatorsIsNotFatal(t *testing.T) {
	feed := fakeFeed(t) // minimalRSS — no <image> at all
	h := setup(t)

	body, _ := json.Marshal(map[string]string{"url": feed.URL + "/feed.rss"})
	rr := httptest.NewRecorder()
	h.Fetch(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

// transcriptRSS has one episode with two <podcast:transcript> tags (VTT and
// plain text) — used to check pickTranscript's preference (plain text
// wins) actually reaches the DB via Fetch, not just in isolation.
const transcriptRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:podcast="https://podcastindex.org/namespace/1.0">
<channel>
  <title>Transcript Podcast</title>
  <description>A test podcast</description>
  <item>
    <title>Episode One</title>
    <enclosure url="%[1]s/ep1.mp3" type="audio/mpeg" length="0"/>
    <pubDate>Mon, 01 Jan 2024 00:00:00 +0000</pubDate>
    <podcast:transcript url="%[1]s/ep1.vtt" type="text/vtt" language="en"/>
    <podcast:transcript url="%[1]s/ep1.txt" type="text/plain" language="en"/>
  </item>
</channel>
</rss>`

func fakeFeedWithTranscript(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ep1.mp3" {
			w.WriteHeader(http.StatusOK)
			return
		}
		fmt.Fprintf(w, transcriptRSS, srv.URL)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFetch_PicksPreferredTranscriptFormat checks Fetch stores the
// preferred transcript (plain text over VTT, see pickTranscript) when a
// feed's <podcast:transcript> lists more than one format for an episode.
func TestFetch_PicksPreferredTranscriptFormat(t *testing.T) {
	feed := fakeFeedWithTranscript(t)
	pool := testutil.Pool(t)
	h := podcast.NewHandler(pool)

	body, _ := json.Marshal(map[string]string{"url": feed.URL + "/feed.rss"})
	rr := httptest.NewRecorder()
	h.Fetch(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	podcastID := resp["id"].(string)

	var url, typ string
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(transcript_url,''), COALESCE(transcript_type,'') FROM episodes WHERE podcast_id = $1`,
		podcastID,
	).Scan(&url, &typ); err != nil {
		t.Fatalf("query episode: %v", err)
	}
	if typ != "text/plain" {
		t.Errorf("transcript_type = %q, want text/plain (preferred over vtt)", typ)
	}
	if !strings.HasSuffix(url, "/ep1.txt") {
		t.Errorf("transcript_url = %q, want the .txt one", url)
	}
}

// TestFetch_NoTranscriptIsNotFatal checks a feed with no
// <podcast:transcript> tags at all (the common case today) still imports
// fine, with empty transcript fields.
func TestFetch_NoTranscriptIsNotFatal(t *testing.T) {
	feed := fakeFeed(t) // minimalRSS — no transcript tags
	pool := testutil.Pool(t)
	h := podcast.NewHandler(pool)

	body, _ := json.Marshal(map[string]string{"url": feed.URL + "/feed.rss"})
	rr := httptest.NewRecorder()
	h.Fetch(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var url, typ string
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(transcript_url,''), COALESCE(transcript_type,'') FROM episodes WHERE podcast_id = (SELECT id FROM podcasts ORDER BY created_at DESC LIMIT 1)`,
	).Scan(&url, &typ); err != nil {
		t.Fatalf("query episode: %v", err)
	}
	if url != "" || typ != "" {
		t.Errorf("expected empty transcript fields, got url=%q type=%q", url, typ)
	}
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

const growingRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
<channel>
  <title>Growing Podcast</title>
  <description>A test podcast</description>
  %s
</channel>
</rss>`

const oldItemXML = `<item>
    <title>Old Episode</title>
    <enclosure url="%s/ep-old.mp3" type="audio/mpeg" length="0"/>
    <pubDate>Mon, 01 Jan 2024 00:00:00 +0000</pubDate>
  </item>`

const newItemXML = `<item>
    <title>New Episode</title>
    <enclosure url="%s/ep-new.mp3" type="audio/mpeg" length="0"/>
    <pubDate>Tue, 02 Jan 2024 00:00:00 +0000</pubDate>
  </item>`

// TestFetch_RefreshOnlyAddsGenuinelyNewEpisodes covers the cutoff optimization:
// a second Fetch of a feed that gained one new episode (item order matches
// real RSS convention, newest-first) must end up with BOTH the pre-existing
// old episode and the new one — the old one must not be lost, and the new
// one must not be skipped, despite most of the loop now being short-circuited
// by the pub_date cutoff.
func TestFetch_RefreshOnlyAddsGenuinelyNewEpisodes(t *testing.T) {
	var includeNew bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ep-old.mp3" || r.URL.Path == "/ep-new.mp3" {
			w.WriteHeader(http.StatusOK)
			return
		}
		items := fmt.Sprintf(oldItemXML, srv.URL)
		if includeNew {
			items = fmt.Sprintf(newItemXML, srv.URL) + items
		}
		fmt.Fprintf(w, growingRSS, items)
	}))
	t.Cleanup(srv.Close)
	h := setup(t)

	body, _ := json.Marshal(map[string]string{"url": srv.URL + "/feed.rss"})
	rr := httptest.NewRecorder()
	h.Fetch(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("first fetch: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	podcastID := resp["id"].(string)

	includeNew = true
	rr = httptest.NewRecorder()
	h.Fetch(rr, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	router := chi.NewRouter()
	router.Get("/{id}/episodes", h.GetEpisodes)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/"+podcastID+"/episodes", nil))
	var episodes []map[string]any
	json.NewDecoder(rr.Body).Decode(&episodes)
	if len(episodes) != 2 {
		t.Fatalf("expected 2 episodes after refresh (1 pre-existing + 1 new), got %d: %+v", len(episodes), episodes)
	}
	titles := map[string]bool{}
	for _, e := range episodes {
		titles[e["title"].(string)] = true
	}
	if !titles["Old Episode"] || !titles["New Episode"] {
		t.Errorf("expected both Old Episode and New Episode present, got %v", titles)
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
