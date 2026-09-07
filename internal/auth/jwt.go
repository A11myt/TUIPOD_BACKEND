package auth

import (
	"crypto/rand"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID    string `json:"user_id"`
	TokenType string `json:"token_type"` // "access" | "refresh"
	JTI       string `json:"jti,omitempty"`
	jwt.RegisteredClaims
}

// GenerateAccessToken signs a short-lived (15 min) access token for userID.
func GenerateAccessToken(userID string) (string, error) {
	return signToken(userID, "access", "", 15*time.Minute)
}

// GenerateRefreshToken returns the signed JWT and the JTI that must be stored in DB.
func GenerateRefreshToken(userID string) (token, jti string, err error) {
	jti, err = newJTI()
	if err != nil {
		return "", "", fmt.Errorf("generate jti: %w", err)
	}
	token, err = signToken(userID, "refresh", jti, 30*24*time.Hour)
	return token, jti, err
}

// signToken builds the claims and signs the JWT with HS256, using the JWT_SECRET env var.
func signToken(userID, tokenType, jti string, ttl time.Duration) (string, error) {
	claims := Claims{
		UserID:    userID,
		TokenType: tokenType,
		JTI:       jti,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(os.Getenv("JWT_SECRET")))
}

// ValidateToken parses and validates a JWT, rejecting anything not signed with HMAC.
func ValidateToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(os.Getenv("JWT_SECRET")), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// newJTI generates a random UUID-like identifier for a refresh token.
func newJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
