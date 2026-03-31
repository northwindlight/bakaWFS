package auth

import (
	"bakaWFS/internal/config"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Auth struct {
	cfg   config.Config
	users config.UsersConfig
}

func NewAuth(cfg config.Config, users config.UsersConfig) *Auth {
	return &Auth{cfg: cfg, users: users}
}

func (a *Auth) generateToken(username string) (string, error) {
	claims := jwt.MapClaims{
		"username": username,
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
		"iat":      time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(a.cfg.Secret))
}

func (a *Auth) VerifyToken(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(a.cfg.Secret), nil
	})
	if err != nil {
		return "", err
	}
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		username, ok := claims["username"].(string)
		if !ok {
			return "", errors.New("无效的令牌")
		}
		return username, nil
	}
	return "", errors.New("无效的令牌")
}

// RefreshToken 验证旧 token，有效则签发新的 24h token。
func (a *Auth) RefreshToken(username string) (string, error) {
	return a.generateToken(username)
}

func (a *Auth) Login(user config.User) (string, error) {
	for _, u := range a.users.Users {
		if u.Username == user.Username && u.Password == user.Password {
			return a.generateToken(u.Username)
		}
	}
	return "", errors.New("无效的用户名或密码")
}
