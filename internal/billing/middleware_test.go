package billing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A11myt/tuipod/internal/billing"
	"github.com/A11myt/tuipod/internal/testutil"
)

func TestRequirePlan_Allowed(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "plan-ok@example.com", "password")
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET plan = 'basic' WHERE id = $1`, userID); err != nil {
		t.Fatalf("set plan: %v", err)
	}

	called := false
	mw := billing.RequirePlan(pool, "basic", "pro")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if !called {
		t.Fatal("expected next handler to be called")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestRequirePlan_Blocked(t *testing.T) {
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "plan-blocked@example.com", "password")
	// default plan is 'free' — not in the allowed set

	called := false
	mw := billing.RequirePlan(pool, "basic", "pro")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, testutil.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), userID))

	if called {
		t.Fatal("expected next handler NOT to be called")
	}
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", rr.Code)
	}
}
