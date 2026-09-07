package auth

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const UserIDKey contextKey = "userID"

// Middleware reads the Authorization: Bearer header, validates the access token,
// and stashes the user ID in the request context.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		claims, err := ValidateToken(strings.TrimPrefix(header, "Bearer "))
		if err != nil || claims.TokenType != "access" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), UserIDKey, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserIDFromCtx reads the authenticated user ID from the request context.
func UserIDFromCtx(r *http.Request) string {
	id, _ := r.Context().Value(UserIDKey).(string)
	return id
}
