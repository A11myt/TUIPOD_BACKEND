package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestRegister_PasswordTooShort is a regression test for a gap where Register
// only checked for a non-empty password — the 8-char minimum existed only as
// client-side `minLength` on each frontend's form, trivially bypassed by
// calling the API directly.
func TestRegister_PasswordTooShort(t *testing.T) {
	code, resp := doRegister(t, setup(t), "shortpw@example.com", "short1")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", code)
	}
	if resp["error"] != "password must be at least 8 characters" {
		t.Errorf("unexpected error message: %v", resp["error"])
	}
}

// TestRegister_PasswordTooLong guards bcrypt's own 72-byte input cap —
// GenerateFromPassword silently truncates anything longer, so without this
// check two different passwords beyond 72 bytes that share the same first 72
// bytes would hash identically.
func TestRegister_PasswordTooLong(t *testing.T) {
	code, resp := doRegister(t, setup(t), "longpw@example.com", strings.Repeat("a", 73))
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", code)
	}
	if resp["error"] != "password must be at most 72 characters" {
		t.Errorf("unexpected error message: %v", resp["error"])
	}
}

func TestLogin_Success(t *testing.T) {
	h := setup(t)
	doRegister(t, h, "login@example.com", "secretpw")

	body, _ := json.Marshal(map[string]string{"email": "login@example.com", "password": "secretpw"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	h := setup(t)
	doRegister(t, h, "wp@example.com", "correctpw")

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

// TestResetPassword_PasswordTooShort checks the length validation runs
// *before* the token lookup (an invalid token alone would also 400, which
// would make this test meaningless without asserting the specific message).
func TestResetPassword_PasswordTooShort(t *testing.T) {
	h := setup(t)
	body, _ := json.Marshal(map[string]string{
		"token":        "00000000-0000-0000-0000-000000000000",
		"new_password": "short1",
	})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ResetPassword(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["error"] != "password must be 8-72 characters" {
		t.Errorf("expected the length-validation error (checked before the token lookup), got %v", resp["error"])
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
