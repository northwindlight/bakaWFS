package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Address         string `yaml:"address"`
	Port            int    `yaml:"port"`
	Secret          string `yaml:"secret"`
	CertPath        string `yaml:"cert_path"`
	KeyPath         string `yaml:"key_path"`
	DirPath         string `yaml:"file_dir"`
	UsersPath       string `yaml:"users_file"`
	HtmlDir         string `yaml:"html_dir"`
	TempDir         string `yaml:"temp_dir"`
	DownloadWorkers int    `yaml:"download_workers"`
}

type User struct {
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
}

type UsersConfig struct {
	Users []User `yaml:"users"`
}

// defaultConfig 是首次启动时写入磁盘的模板
var defaultConfig = `# baka-file-server 配置文件

address: "0.0.0.0"
port: 443

# JWT 签名密钥
secret: ""

cert_path: "certificate.crt"
key_path:  "private.key"

file_dir:   "files"

#built-in和internal表示使用内置html
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
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(defaultConfig), 0644); err != nil {
		return fmt.Errorf("写入默认配置失败: %w", err)
	}
	return ErrCreatedDefault
}

func EnsureUsersConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(defaultUsersConfig), 0644); err != nil {
		return fmt.Errorf("写入默认用户配置失败: %w", err)
	}
	return ErrCreatedDefault
}

func (c *Config) Validate() error {
	if c.Secret == "" {
		return fmt.Errorf("config.yaml 中 secret 不能为空，请设置一个随机字符串")
	}
	if c.DownloadWorkers <= 0 {
		c.DownloadWorkers = 1
	}
	return nil
}

type createdDefaultError struct{}

func (e createdDefaultError) Error() string {
	return "配置文件不存在，已自动创建默认配置，请编辑后重新启动"
}

var ErrCreatedDefault error = createdDefaultError{}
