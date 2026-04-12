package utils

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTManager JWT令牌管理器
type JWTManager struct {
	secret     []byte
	expireTime time.Duration
}

// Claims JWT声明
type Claims struct {
	Username string `json:"sub"`
	jwt.RegisteredClaims
}

// NewJWTManager 创建JWT管理器
func NewJWTManager(secret string, expireHours int) *JWTManager {
	return &JWTManager{
		secret:     []byte(secret),
		expireTime: time.Duration(expireHours) * time.Hour,
	}
}

// SignToken 生成JWT令牌
func (jm *JWTManager) SignToken(username string) (string, error) {
	now := time.Now()
	claims := Claims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(jm.expireTime)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jm.secret)
}

// VerifyToken 验证JWT令牌并返回用户名
func (jm *JWTManager) VerifyToken(tokenString string) (string, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jm.secret, nil
	})

	if err != nil {
		return "", err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims.Username, nil
	}

	return "", fmt.Errorf("invalid token")
}
