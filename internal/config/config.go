package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// PortDisabled 表示该端口已关闭，配置为 -1 即可。
const PortDisabled = -1

type Config struct {
	Address         string `yaml:"address"`
	HttpsPort       int    `yaml:"https_port"`
	HttpPort        int    `yaml:"http_port"`
	Secret          string `yaml:"secret"`
	CertPath        string `yaml:"cert_path"`
	KeyPath         string `yaml:"key_path"`
	DirPath         string `yaml:"file_dir"`
	UsersPath       string `yaml:"users_file"`
	HtmlDir         string `yaml:"html_dir"`
	TempDir         string `yaml:"temp_dir"`
	DownloadWorkers int    `yaml:"download_workers"`
	AuditLogPath    string `yaml:"audit_log"`
	CorsEnabled     bool   `yaml:"cors_enabled"`
	AuthMode        bool   `yaml:"auth_mode"`
}

func (c *Config) HttpsEnabled() bool { return c.HttpsPort != PortDisabled }

func (c *Config) HttpEnabled() bool { return c.HttpPort != PortDisabled }

func (c *Config) HtmlEnabled() bool { return c.HtmlDir != "" }

// Role 取值：admin（可读写）/ guest（只读）。空或未知一律按 guest 处理（最小权限默认）。
// Password 保留 json tag：登录请求体要反序列化进来；响应永远返回 jwtClaims，不回显本结构体，故不泄露。
type User struct {
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
	Role     string `yaml:"role" json:"-"`
}

type UsersConfig struct {
	Users []User `yaml:"users"`
}

// defaultConfigTemplate 是首次启动时写入磁盘的模板，%%SECRET%% 会被随机密钥替换
var defaultConfigTemplate = `# baka-file-server 配置文件

address: "0.0.0.0"

# 端口设为 -1 表示关闭该协议。两者同时开启时，HTTP 会重定向到 HTTPS。
https_port: -1
http_port:  8080

# JWT 签名密钥
secret: "%%SECRET%%"

cert_path: "certificate.crt"
key_path:  "private.key"

file_dir:   "files"

# built-in / internal = 内置前端，留空禁用前端（纯 API 模式）
html_dir:   "built-in"
temp_dir:   ".uploads"
users_file: "users.yaml"

# 并发远程下载 worker 数量
download_workers: 2

# 审计日志路径，留空则关闭
audit_log: ""

# 跨域支持，默认关闭。开启后前端可跨域下载文件，文件下载强制 Content-Disposition: attachment
cors_enabled: false

# 鉴权模式。true = 所有接口（含浏览/下载）均需登录；false = 开放模式（仅写操作需登录）
auth_mode: false
`

// defaultUsersConfig 首次启动写入。
// baka = 共享只读账号（guest），用于挡匿名/爬虫，口令可半公开传播。
// admin = 可写账号，口令首次启动随机生成（%%ADMIN_PASSWORD%% 占位）。
var defaultUsersConfig = `
users:
  - username: "baka"
    password: "baka"
    role: "guest"
  - username: "admin"
    password: "%%ADMIN_PASSWORD%%"
    role: "admin"
`

// generateSecret 生成 16 字节（32位十六进制）随机密钥，用于 JWT 签名。
func generateSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机密钥失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// pwAlphabet 去掉了易混淆字符（0/O/1/l/I），方便人工抄写。
const pwAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// generatePassword 生成 8 位随机口令，供 admin 账号首启使用（人要手敲，不必太长）。
func generatePassword() (string, error) {
	const n = 8
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机口令失败: %w", err)
	}
	out := make([]byte, n)
	for i, c := range b {
		out[i] = pwAlphabet[int(c)%len(pwAlphabet)]
	}
	return string(out), nil
}

// LoadYAML 读取并解析指定路径的 YAML 文件。
func LoadYAML[T any](path string) (T, error) {
	var cfg T
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("读取文件失败: %w", err)
	}
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("解析文件失败: %w", err)
	}
	return cfg, nil
}

func EnsureConfig(path string) error {
	secret, err := generateSecret()
	if err != nil {
		return err
	}
	content := strings.ReplaceAll(defaultConfigTemplate, "%%SECRET%%", secret)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("写入默认配置失败: %w", err)
	}
	return nil
}

// EnsureUsersConfig 在用户配置不存在时写入默认配置，并为 admin 生成随机口令。
// 返回生成的 admin 口令（供调用方打印一次）；文件已存在则返回空串（保持现有逻辑：不动已有文件）。
func EnsureUsersConfig(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return "", nil
	}
	adminPw, err := generatePassword()
	if err != nil {
		return "", err
	}
	content := strings.ReplaceAll(defaultUsersConfig, "%%ADMIN_PASSWORD%%", adminPw)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("写入默认用户配置失败: %w", err)
	}
	return adminPw, nil
}

func (c *Config) Validate() error {
	if c.Secret == "" {
		return fmt.Errorf("config.yaml 中 secret 不能为空，请设置一个随机字符串")
	}
	if c.HttpsPort == c.HttpPort {
		return fmt.Errorf("https_port 和 http_port 不能使用相同端口，或者同时关闭")
	}
	if c.DownloadWorkers <= 0 {
		c.DownloadWorkers = 1
	}
	return nil
}
