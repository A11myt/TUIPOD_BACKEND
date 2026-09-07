package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/mailer"
	"github.com/A11myt/tuipod/internal/testutil"
)

func setup(t *testing.T) *auth.Handler {
	t.Helper()
	return auth.NewHandler(testutil.Pool(t), mailer.New())
}

func doRegister(t *testing.T, h *auth.Handler, email, password string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Register(rr, req)
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	return rr.Code, resp
}

func TestRegister_Success(t *testing.T) {
	code, resp := doRegister(t, setup(t), "user@example.com", "password123")
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	if resp["access_token"] == "" || resp["refresh_token"] == "" {
		t.Error("expected tokens in response")
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	h := setup(t)
	doRegister(t, h, "dup@example.com", "password123")
	code, _ := doRegister(t, h, "dup@example.com", "password123")
	if code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", code)
	}
}

func TestRegister_MissingFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"email":""}`))
	rr := httptest.NewRecorder()
	setup(t).Register(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestLogin_Success(t *testing.T) {
	h := setup(t)
	doRegister(t, h, "login@example.com", "secret")

	body, _ := json.Marshal(map[string]string{"email": "login@example.com", "password": "secret"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	h := setup(t)
	doRegister(t, h, "wp@example.com", "correct")

	body, _ := json.Marshal(map[string]string{"email": "wp@example.com", "password": "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestRefresh_Success(t *testing.T) {
	h := setup(t)
	_, resp := doRegister(t, h, "refresh@example.com", "password")
	refreshToken := resp["refresh_token"].(string)

	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRefresh_AccessTokenRejected(t *testing.T) {
	h := setup(t)
	_, resp := doRegister(t, h, "at@example.com", "password")
	accessToken := resp["access_token"].(string)

	body, _ := json.Marshal(map[string]string{"refresh_token": accessToken})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestLogout_RevokesToken(t *testing.T) {
	h := setup(t)
	_, resp := doRegister(t, h, "logout@example.com", "password")
	refreshToken := resp["refresh_token"].(string)

	// logout
	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Logout(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}

	// refresh with revoked token must fail
	body2, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	req2 := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body2))
	rr2 := httptest.NewRecorder()
	h.Refresh(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after logout, got %d", rr2.Code)
	}
}

func TestLogout_InvalidTokenStillReturns204(t *testing.T) {
	h := setup(t)
	body, _ := json.Marshal(map[string]string{"refresh_token": "invalid.token.value"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Logout(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for invalid token, got %d", rr.Code)
	}
}

func TestForgotPassword_UnknownEmailReturns204(t *testing.T) {
	h := setup(t)
	body, _ := json.Marshal(map[string]string{"email": "nobody@example.com"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ForgotPassword(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 (no info leakage), got %d", rr.Code)
	}
}

func TestResetPassword_InvalidToken(t *testing.T) {
	h := setup(t)
	body, _ := json.Marshal(map[string]string{
		"token":        "00000000-0000-0000-0000-000000000000",
		"new_password": "newpassword123",
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ResetPassword(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestVerifyEmail_InvalidToken(t *testing.T) {
	h := setup(t)
	req := httptest.NewRequest(http.MethodGet, "/?token=00000000-0000-0000-0000-000000000000", nil)
	rr := httptest.NewRecorder()
	h.VerifyEmail(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestLogin_SuspendedAccount(t *testing.T) {
	pool := testutil.Pool(t)
	h := auth.NewHandler(pool, mailer.New())
	doRegister(t, h, "suspended@example.com", "password")
	pool.Exec(context.Background(), `UPDATE users SET suspended_at = NOW() WHERE email = 'suspended@example.com'`)

	body, _ := json.Marshal(map[string]string{"email": "suspended@example.com", "password": "password"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for suspended account, got %d", rr.Code)
	}
}
