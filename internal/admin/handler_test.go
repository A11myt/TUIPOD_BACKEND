package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A11myt/tuipod/internal/admin"
	"github.com/A11myt/tuipod/internal/testutil"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setup(t *testing.T) (*admin.Handler, *pgxpool.Pool, string) {
	t.Helper()
	pool := testutil.Pool(t)
	adminID := testutil.CreateAdminUser(t, pool, "admin@example.com", "adminpass")
	return admin.NewHandler(pool), pool, adminID
}

func TestAdminStats(t *testing.T) {
	h, _, adminID := setup(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = testutil.WithUser(req, adminID)
	rr := httptest.NewRecorder()
	h.Stats(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if _, ok := resp["total_users"]; !ok {
		t.Error("expected total_users in response")
	}
}

func TestAdminListUsers(t *testing.T) {
	h, pool, adminID := setup(t)
	testutil.CreateUser(t, pool, "regular@example.com", "pw")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = testutil.WithUser(req, adminID)
	rr := httptest.NewRecorder()
	h.ListUsers(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var users []map[string]any
	json.NewDecoder(rr.Body).Decode(&users)
	if len(users) < 2 {
		t.Errorf("expected at least 2 users, got %d", len(users))
	}
}

func TestAdminGetUser(t *testing.T) {
	h, pool, adminID := setup(t)
	userID := testutil.CreateUser(t, pool, "target@example.com", "pw")

	r := chi.NewRouter()
	r.Get("/{id}", h.GetUser)

	req := httptest.NewRequest(http.MethodGet, "/"+userID, nil)
	req = testutil.WithUser(req, adminID)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var user map[string]any
	json.NewDecoder(rr.Body).Decode(&user)
	if user["email"] != "target@example.com" {
		t.Errorf("unexpected email: %v", user["email"])
	}
}

func TestAdminGetUser_NotFound(t *testing.T) {
	h, _, adminID := setup(t)

	r := chi.NewRouter()
	r.Get("/{id}", h.GetUser)

	req := httptest.NewRequest(http.MethodGet, "/00000000-0000-0000-0000-000000000000", nil)
	req = testutil.WithUser(req, adminID)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestAdminSetPlan(t *testing.T) {
	h, pool, adminID := setup(t)
	userID := testutil.CreateUser(t, pool, "plantest@example.com", "pw")

	r := chi.NewRouter()
	r.Put("/{id}/plan", h.SetPlan)

	body, _ := json.Marshal(map[string]string{"plan": "basic"})
	req := httptest.NewRequest(http.MethodPut, "/"+userID+"/plan", bytes.NewReader(body))
	req = testutil.WithUser(req, adminID)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// verify in DB
	var plan string
	pool.QueryRow(t.Context(), `SELECT plan FROM users WHERE id = $1`, userID).Scan(&plan)
	if plan != "basic" {
		t.Errorf("expected plan=basic, got %s", plan)
	}
}

func TestAdminSetPlan_InvalidPlan(t *testing.T) {
	h, pool, adminID := setup(t)
	userID := testutil.CreateUser(t, pool, "badplan@example.com", "pw")

	r := chi.NewRouter()
	r.Put("/{id}/plan", h.SetPlan)

	body, _ := json.Marshal(map[string]string{"plan": "enterprise"})
	req := httptest.NewRequest(http.MethodPut, "/"+userID+"/plan", bytes.NewReader(body))
	req = testutil.WithUser(req, adminID)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestAdminSuspendAndUnsuspend(t *testing.T) {
	h, pool, adminID := setup(t)
	userID := testutil.CreateUser(t, pool, "suspend@example.com", "pw")

	r := chi.NewRouter()
	r.Post("/{id}/suspend", h.Suspend)
	r.Post("/{id}/unsuspend", h.Unsuspend)

	// suspend
	req := httptest.NewRequest(http.MethodPost, "/"+userID+"/suspend", nil)
	req = testutil.WithUser(req, adminID)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("suspend: expected 204, got %d", rr.Code)
	}

	// verify suspended
	var suspendedAt *time.Time
	if err := pool.QueryRow(t.Context(), `SELECT suspended_at FROM users WHERE id = $1`, userID).Scan(&suspendedAt); err != nil {
		t.Fatalf("query suspended_at: %v", err)
	}
	if suspendedAt == nil {
		t.Error("expected suspended_at to be set")
	}

	// unsuspend
	req2 := httptest.NewRequest(http.MethodPost, "/"+userID+"/unsuspend", nil)
	req2 = testutil.WithUser(req2, adminID)
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusNoContent {
		t.Fatalf("unsuspend: expected 204, got %d", rr2.Code)
	}

	// verify unsuspended
	pool.QueryRow(t.Context(), `SELECT suspended_at FROM users WHERE id = $1`, userID).Scan(&suspendedAt)
	if suspendedAt != nil {
		t.Error("expected suspended_at to be NULL after unsuspend")
	}
}

func TestAdminDeleteUser(t *testing.T) {
	h, pool, adminID := setup(t)
	userID := testutil.CreateUser(t, pool, "delete@example.com", "pw")

	r := chi.NewRouter()
	r.Delete("/{id}", h.DeleteUser)

	req := httptest.NewRequest(http.MethodDelete, "/"+userID, nil)
	req = testutil.WithUser(req, adminID)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}

	// verify gone
	var count int
	pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM users WHERE id = $1`, userID).Scan(&count)
	if count != 0 {
		t.Error("user should be deleted")
	}
}
