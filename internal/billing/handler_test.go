package billing_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/A11myt/tuipod/internal/billing"
	"github.com/A11myt/tuipod/internal/testutil"
)

func setup(t *testing.T) (*billing.Handler, string) {
	t.Helper()
	pool := testutil.Pool(t)
	userID := testutil.CreateUser(t, pool, "billing@example.com", "pw")
	return billing.NewHandler(pool), userID
}

func TestBillingStatus_DefaultFree(t *testing.T) {
	h, userID := setup(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = testutil.WithUser(req, userID)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["plan"] != "free" {
		t.Errorf("expected plan=free, got %v", resp["plan"])
	}
}

func TestBillingCheckout_NoStripe(t *testing.T) {
	os.Unsetenv("STRIPE_SECRET_KEY")
	h, userID := setup(t)

	body, _ := json.Marshal(map[string]string{"plan": "basic"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req = testutil.WithUser(req, userID)
	rr := httptest.NewRecorder()
	h.Checkout(rr, req)

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 when Stripe not configured, got %d", rr.Code)
	}
}

func TestBillingPortal_NoStripe(t *testing.T) {
	os.Unsetenv("STRIPE_SECRET_KEY")
	h, userID := setup(t)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = testutil.WithUser(req, userID)
	rr := httptest.NewRecorder()
	h.Portal(rr, req)

	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 when Stripe not configured, got %d", rr.Code)
	}
}

func TestBillingCheckout_InvalidPlan(t *testing.T) {
	os.Setenv("STRIPE_SECRET_KEY", "sk_test_fake")
	t.Cleanup(func() { os.Unsetenv("STRIPE_SECRET_KEY") })
	h, userID := setup(t)

	body, _ := json.Marshal(map[string]string{"plan": "ultra"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req = testutil.WithUser(req, userID)
	rr := httptest.NewRecorder()
	h.Checkout(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestBillingWebhook_InvalidSignature(t *testing.T) {
	h, _ := setup(t)
	os.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test_secret")
	t.Cleanup(func() { os.Unsetenv("STRIPE_WEBHOOK_SECRET") })

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"id":"evt_test"}`)))
	req.Header.Set("Stripe-Signature", "t=invalid,v1=invalidsig")
	rr := httptest.NewRecorder()
	h.Webhook(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid signature, got %d", rr.Code)
	}
}
