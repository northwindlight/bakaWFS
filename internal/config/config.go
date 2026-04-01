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
}

func (c *Config) HttpsEnabled() bool { return c.HttpsPort != PortDisabled }

func (c *Config) HttpEnabled() bool { return c.HttpPort != PortDisabled }

type User struct {
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
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

# built-in 和 internal 表示使用内置 html
html_dir:   "built-in"
temp_dir:   ".uploads"
users_file: "users.yaml"

# 并发远程下载 worker 数量
download_workers: 2
`

var defaultUsersConfig = `
users:
  - username: "baka"
    password: "baka"
`

// generateSecret 生成 16 字节（32位十六进制）随机密钥
func generateSecret() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机密钥失败: %w", err)
	}
	return hex.EncodeToString(b), nil
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

func EnsureUsersConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(defaultUsersConfig), 0644); err != nil {
		return fmt.Errorf("写入默认用户配置失败: %w", err)
	}
	return nil
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
