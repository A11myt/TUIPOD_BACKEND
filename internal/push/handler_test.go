package push_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/A11myt/tuipod/internal/push"
	"github.com/A11myt/tuipod/internal/testutil"
)

func TestRegisterDeviceToken(t *testing.T) {
	pool := testutil.Pool(t)
	h := push.NewHandler(pool)
	userID := testutil.CreateUser(t, pool, "push@example.com", "pw")

	body, _ := json.Marshal(map[string]string{"token": "fcm-token-abc123", "platform": "android"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req = testutil.WithUser(req, userID)
	rr := httptest.NewRecorder()
	h.Register(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	var count int
	pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM device_tokens WHERE user_id = $1 AND token = 'fcm-token-abc123'`, userID,
	).Scan(&count)
	if count != 1 {
		t.Error("expected device token to be stored")
	}
}

func TestRegisterDeviceToken_Idempotent(t *testing.T) {
	pool := testutil.Pool(t)
	h := push.NewHandler(pool)
	userID := testutil.CreateUser(t, pool, "push2@example.com", "pw")

	doRegister := func() {
		body, _ := json.Marshal(map[string]string{"token": "same-token", "platform": "ios"})
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req = testutil.WithUser(req, userID)
		rr := httptest.NewRecorder()
		h.Register(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d", rr.Code)
		}
	}

	doRegister()
	doRegister() // second call must not error

	var count int
	pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM device_tokens WHERE token = 'same-token'`,
	).Scan(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 token row, got %d", count)
	}
}

func TestRegisterDeviceToken_InvalidPlatform(t *testing.T) {
	pool := testutil.Pool(t)
	h := push.NewHandler(pool)
	userID := testutil.CreateUser(t, pool, "push3@example.com", "pw")

	body, _ := json.Marshal(map[string]string{"token": "tok", "platform": "windows"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req = testutil.WithUser(req, userID)
	rr := httptest.NewRecorder()
	h.Register(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid platform, got %d", rr.Code)
	}
}

func TestUnregisterDeviceToken(t *testing.T) {
	pool := testutil.Pool(t)
	h := push.NewHandler(pool)
	userID := testutil.CreateUser(t, pool, "push4@example.com", "pw")

	// insert token directly
	pool.Exec(context.Background(),
		`INSERT INTO device_tokens (user_id, token, platform) VALUES ($1, 'delete-me', 'android')`, userID)

	r := chi.NewRouter()
	r.Delete("/{token}", h.Unregister)

	req := httptest.NewRequest(http.MethodDelete, "/delete-me", nil)
	req = testutil.WithUser(req, userID)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}

	var count int
	pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM device_tokens WHERE token = 'delete-me'`,
	).Scan(&count)
	if count != 0 {
		t.Error("expected token to be deleted")
	}
}
