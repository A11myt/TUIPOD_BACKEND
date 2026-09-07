package queue_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/A11myt/tuipod/internal/queue"
	"github.com/A11myt/tuipod/internal/testutil"
)

func TestQueue_AddListRemove(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "queue@example.com", "pw")
	podID := testutil.CreatePodcast(t, pool)
	epID := testutil.CreateEpisode(t, pool, podID)
	h := queue.NewHandler(pool)

	// Add
	body, _ := json.Marshal(map[string]string{"episode_id": epID})
	req := testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID)
	rr := httptest.NewRecorder()
	h.Add(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("Add: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var addResp map[string]any
	json.NewDecoder(rr.Body).Decode(&addResp)
	itemID := addResp["id"].(string)

	// List
	req2 := testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID)
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("List: expected 200, got %d", rr2.Code)
	}
	var items []map[string]any
	json.NewDecoder(rr2.Body).Decode(&items)
	if len(items) != 1 {
		t.Fatalf("expected 1 queue item, got %d", len(items))
	}

	// Remove
	r := chi.NewRouter()
	r.Delete("/{id}", h.Remove)
	req3 := testutil.WithUser(httptest.NewRequest(http.MethodDelete, "/"+itemID, nil), userID)
	rr3 := httptest.NewRecorder()
	r.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusNoContent {
		t.Fatalf("Remove: expected 204, got %d", rr3.Code)
	}
}

func TestQueue_Reorder(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "reorder@example.com", "pw")
	podID := testutil.CreatePodcast(t, pool)
	ep1 := testutil.CreateEpisode(t, pool, podID)
	ep2 := testutil.CreateEpisode(t, pool, podID)
	h := queue.NewHandler(pool)

	addEp := func(epID string) string {
		body, _ := json.Marshal(map[string]string{"episode_id": epID})
		req := testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID)
		rr := httptest.NewRecorder()
		h.Add(rr, req)
		var resp map[string]any
		json.NewDecoder(rr.Body).Decode(&resp)
		return resp["id"].(string)
	}

	id1 := addEp(ep1)
	id2 := addEp(ep2)

	// reorder: put id2 first, id1 second
	body, _ := json.Marshal(map[string][]string{"ids": {id2, id1}})
	req := testutil.WithUser(httptest.NewRequest(http.MethodPut, "/reorder", bytes.NewReader(body)), userID)
	rr := httptest.NewRecorder()
	h.Reorder(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("Reorder: expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// verify positions
	var pos1, pos2 int
	pool.QueryRow(context.Background(), `SELECT position FROM queue WHERE id = $1`, id2).Scan(&pos1)
	pool.QueryRow(context.Background(), `SELECT position FROM queue WHERE id = $1`, id1).Scan(&pos2)
	if pos1 != 0 {
		t.Errorf("expected id2 at position 0, got %d", pos1)
	}
	if pos2 != 1 {
		t.Errorf("expected id1 at position 1, got %d", pos2)
	}
}

func TestQueue_AddDuplicate(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "dupqueue@example.com", "pw")
	podID := testutil.CreatePodcast(t, pool)
	epID := testutil.CreateEpisode(t, pool, podID)
	h := queue.NewHandler(pool)

	add := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"episode_id": epID})
		req := testutil.WithUser(httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), userID)
		rr := httptest.NewRecorder()
		h.Add(rr, req)
		return rr
	}

	if rr := add(); rr.Code != http.StatusCreated {
		t.Fatalf("first add: expected 201, got %d", rr.Code)
	}
	if rr := add(); rr.Code != http.StatusConflict {
		t.Fatalf("duplicate add: expected 409, got %d", rr.Code)
	}
}
