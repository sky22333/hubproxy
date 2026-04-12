package utils

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSignToken_Success(t *testing.T) {
	jm := NewJWTManager("test-secret", 1)
	token, err := jm.SignToken("testuser")

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if token == "" {
		t.Fatal("Expected non-empty token")
	}
}

func TestSignToken_EmptyUsername(t *testing.T) {
	jm := NewJWTManager("test-secret", 1)
	token, err := jm.SignToken("")

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if token == "" {
		t.Fatal("Expected non-empty token even with empty username")
	}
}

func TestVerifyToken_Valid(t *testing.T) {
	jm := NewJWTManager("test-secret", 1)
	token, _ := jm.SignToken("testuser")

	username, err := jm.VerifyToken(token)

	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if username != "testuser" {
		t.Fatalf("Expected username 'testuser', got '%s'", username)
	}
}

func TestVerifyToken_Expired(t *testing.T) {
	// Create token with very short expiration
	jm := NewJWTManager("test-secret", 0) // 0 hours = immediate expiration
	
	// Manually create an expired token
	now := time.Now()
	claims := Claims{
		Username: "testuser",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now.Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(now.Add(-1 * time.Hour)), // Expired 1 hour ago
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString([]byte("test-secret"))

	_, err := jm.VerifyToken(tokenString)

	if err == nil {
		t.Fatal("Expected error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Expected 'expired' error, got %v", err)
	}
}

func TestVerifyToken_InvalidSignature(t *testing.T) {
	jm1 := NewJWTManager("secret1", 1)
	jm2 := NewJWTManager("secret2", 1)

	token, _ := jm1.SignToken("testuser")
	_, err := jm2.VerifyToken(token)

	if err == nil {
		t.Fatal("Expected error for invalid signature, got nil")
	}
}

func TestVerifyToken_Malformed(t *testing.T) {
	jm := NewJWTManager("test-secret", 1)
	_, err := jm.VerifyToken("not-a-valid-jwt-token")

	if err == nil {
		t.Fatal("Expected error for malformed token, got nil")
	}
}
