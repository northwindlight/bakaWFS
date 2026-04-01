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

func (a *Auth) generateToken(username string, ua string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"username": username,
		"ua":       ua,
		"exp":      now.Add(24 * time.Hour).Unix(),
		"iat":      now.Unix(),
		"abs_exp":  now.Add(7 * 24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(a.cfg.Secret))
}

func (a *Auth) VerifyToken(tokenStr string, ua string) (string, error) {
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
		tokenUA, ok := claims["ua"].(string)
		if !ok {
			return "", errors.New("无效的令牌")
		}
		if tokenUA != ua {
			return "", errors.New("错误的UA")
		}
		return username, nil

	}
	return "", errors.New("无效的令牌")
}

func (a *Auth) RefreshToken(tokenStr string) (string, error) {
	now := time.Now()
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(a.cfg.Secret), nil
	})
	if err != nil {
		return "", err
	}
	claims := token.Claims.(jwt.MapClaims)
	username := claims["username"].(string)
	ua := claims["ua"].(string)
	absExp := int64(claims["abs_exp"].(float64))

	if now.Unix() > absExp {
		return "", errors.New("token已超过绝对有效期，请重新登录")
	}

	newClaims := jwt.MapClaims{
		"username": username,
		"ua":       ua,
		"exp":      now.Add(24 * time.Hour).Unix(),
		"iat":      now.Unix(),
		"abs_exp":  absExp,
	}
	newToken := jwt.NewWithClaims(jwt.SigningMethodHS256, newClaims)
	return newToken.SignedString([]byte(a.cfg.Secret))
}

func (a *Auth) Login(user config.User, ua string) (string, error) {
	for _, u := range a.users.Users {
		if u.Username == user.Username && u.Password == user.Password {
			return a.generateToken(u.Username, ua)
		}
	}
	return "", errors.New("无效的用户名或密码")
}
