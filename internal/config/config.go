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

// defaultConfig 是首次启动时写入磁盘的模板，所有字段都有合理默认值。
// Secret 留空，提示用户必须手动填写。
var defaultConfig = `# baka-file-server 配置文件
# 首次启动自动生成，请按需修改

address: "0.0.0.0"
port: 443

# JWT 签名密钥，必须修改为随机字符串，留空则启动失败
secret: ""

cert_path: "certificate.crt"
key_path:  "private.key"

file_dir:   "files"
html_dir:   "html"
temp_dir:   ".uploads"
users_file: "users.yaml"

# 并发远程下载 worker 数量
download_workers: 2
`

var defaultUsersConfig = `# 用户列表，密码建议使用哈希值
users:
  - username: "admin"
    password: "changeme"
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

// EnsureConfig 检查 path 是否存在，不存在则写入默认配置并返回错误提示用户编辑。
// 调用方应在收到 ErrCreatedDefault 后打印提示并退出，让用户填完再启动。
func EnsureConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // 已存在，不覆盖
	}
	if err := os.WriteFile(path, []byte(defaultConfig), 0644); err != nil {
		return fmt.Errorf("写入默认配置失败: %w", err)
	}
	return ErrCreatedDefault
}

// EnsureUsersConfig 同上，针对 users 文件。
func EnsureUsersConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(defaultUsersConfig), 0644); err != nil {
		return fmt.Errorf("写入默认用户配置失败: %w", err)
	}
	return ErrCreatedDefault
}

// Validate 检查加载后的配置是否满足最低要求。
func (c *Config) Validate() error {
	if c.Secret == "" {
		return fmt.Errorf("config.yaml 中 secret 不能为空，请设置一个随机字符串")
	}
	if c.DownloadWorkers <= 0 {
		c.DownloadWorkers = 1 // 兼容旧配置文件没有此字段的情况
	}
	return nil
}

// ErrCreatedDefault 表示配置文件不存在、已自动创建默认版本，用户需要编辑后重启。
type createdDefaultError struct{}

func (e createdDefaultError) Error() string {
	return "配置文件不存在，已自动创建默认配置，请编辑后重新启动"
}

var ErrCreatedDefault error = createdDefaultError{}
