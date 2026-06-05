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

// normalizeRole 将空或未知角色统一收敛为 guest（最小权限默认）。
func normalizeRole(role string) string {
	if role == "admin" {
		return "admin"
	}
	return "guest"
}

func (a *Auth) generateToken(username, ua, role string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"username": username,
		"ua":       ua,
		"role":     normalizeRole(role),
		"exp":      now.Add(24 * time.Hour).Unix(),
		"iat":      now.Unix(),
		"abs_exp":  now.Add(7 * 24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(a.cfg.Secret))
}

// VerifyToken 校验令牌，返回 (username, role, error)。
// role claim 缺失视为无效令牌（不做向后兼容，旧令牌需重新登录）。
func (a *Auth) VerifyToken(tokenStr string, ua string) (string, string, error) {
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(a.cfg.Secret), nil
	})
	if err != nil {
		return "", "", err
	}
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		username, ok := claims["username"].(string)
		if !ok {
			return "", "", errors.New("无效的令牌")
		}
		tokenUA, ok := claims["ua"].(string)
		if !ok {
			return "", "", errors.New("无效的令牌")
		}
		role, ok := claims["role"].(string)
		if !ok {
			return "", "", errors.New("无效的令牌")
		}
		if tokenUA != ua {
			return "", "", errors.New("错误的UA")
		}
		return username, normalizeRole(role), nil

	}
	return "", "", errors.New("无效的令牌")
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
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", errors.New("无效的令牌")
	}
	username, ok := claims["username"].(string)
	if !ok {
		return "", errors.New("无效的令牌")
	}
	ua, ok := claims["ua"].(string)
	if !ok {
		return "", errors.New("无效的令牌")
	}
	role, ok := claims["role"].(string)
	if !ok {
		return "", errors.New("无效的令牌")
	}
	absExpF, ok := claims["abs_exp"].(float64)
	if !ok {
		return "", errors.New("令牌缺少 abs_exp")
	}
	absExp := int64(absExpF)

	if now.Unix() > absExp {
		return "", errors.New("token已超过绝对有效期，请重新登录")
	}

	// 透传旧 role，禁止续签时自升级
	newClaims := jwt.MapClaims{
		"username": username,
		"ua":       ua,
		"role":     normalizeRole(role),
		"exp":      now.Add(24 * time.Hour).Unix(),
		"iat":      now.Unix(),
		"abs_exp":  absExp,
	}
	newToken := jwt.NewWithClaims(jwt.SigningMethodHS256, newClaims)
	return newToken.SignedString([]byte(a.cfg.Secret))
}

// Login 校验凭证并签发令牌，返回 (token, role, error)。
func (a *Auth) Login(user config.User, ua string) (string, string, error) {
	for _, u := range a.users.Users {
		if u.Username == user.Username && u.Password == user.Password {
			role := normalizeRole(u.Role)
			token, err := a.generateToken(u.Username, ua, role)
			return token, role, err
		}
	}
	return "", "", errors.New("无效的用户名或密码")
}
