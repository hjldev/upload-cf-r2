// Package config 负责加载 R2 连接配置：配置文件 < 环境变量 < 命令行参数。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	AccountID       string `json:"account_id"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Bucket          string `json:"bucket"`
	// PublicURL 是存储桶绑定的自定义域名或 r2.dev 地址，用于输出可访问链接，可选。
	PublicURL string `json:"public_url,omitempty"`
	// Jurisdiction 为数据辖区，如 "eu"、"fedramp"，留空表示默认辖区。
	Jurisdiction string `json:"jurisdiction,omitempty"`
	// Endpoint 直接覆盖 S3 API 地址，设置后忽略 AccountID 与 Jurisdiction。
	Endpoint string `json:"endpoint,omitempty"`
}

// envVars 把环境变量映射到对应字段。
var envVars = []struct {
	name  string
	field func(*Config) *string
}{
	{"R2_ACCOUNT_ID", func(c *Config) *string { return &c.AccountID }},
	{"R2_ACCESS_KEY_ID", func(c *Config) *string { return &c.AccessKeyID }},
	{"R2_SECRET_ACCESS_KEY", func(c *Config) *string { return &c.SecretAccessKey }},
	{"R2_BUCKET", func(c *Config) *string { return &c.Bucket }},
	{"R2_PUBLIC_URL", func(c *Config) *string { return &c.PublicURL }},
	{"R2_JURISDICTION", func(c *Config) *string { return &c.Jurisdiction }},
	{"R2_ENDPOINT", func(c *Config) *string { return &c.Endpoint }},
}

// DefaultPath 返回 $XDG_CONFIG_HOME/r2up/config.json，未设置时为 ~/.config/r2up/config.json。
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "r2up", "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "r2up", "config.json")
}

// Load 读取配置文件并用环境变量覆盖。required 为 false 时配置文件不存在不算错误。
func Load(path string, required bool) (Config, error) {
	var cfg Config
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := json.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("解析配置文件 %s: %w", path, err)
			}
		case errors.Is(err, fs.ErrNotExist) && !required:
		default:
			return cfg, fmt.Errorf("读取配置文件: %w", err)
		}
	}
	for _, v := range envVars {
		if val, ok := os.LookupEnv(v.name); ok && val != "" {
			*v.field(&cfg) = val
		}
	}
	return cfg, nil
}

// Save 把配置写入 path，必要时创建目录。先写临时文件再重命名，
// 避免中途失败留下半截文件；文件权限为 0600（Windows 上由系统 ACL 决定）。
func Save(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json") // CreateTemp 创建的文件权限即为 0600
	if err != nil {
		return fmt.Errorf("写入配置文件: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入配置文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入配置文件: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("写入配置文件: %w", err)
	}
	return nil
}

var accountIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ParseAccount 接受账户 ID，或控制台创建令牌后显示的 S3 终结点地址
// （如 https://<账户ID>.eu.r2.cloudflarestorage.com）。
// 输入为终结点地址时 fromEndpoint 为 true，此时 jurisdiction 才有意义（空串表示默认辖区）。
func ParseAccount(s string) (accountID, jurisdiction string, fromEndpoint bool, err error) {
	s = strings.TrimSpace(s)
	accountID = s
	if strings.Contains(s, ".r2.cloudflarestorage.com") {
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		u, err := url.Parse(s)
		if err != nil {
			return "", "", false, fmt.Errorf("无法解析终结点地址: %w", err)
		}
		labels := strings.Split(strings.TrimSuffix(u.Hostname(), ".r2.cloudflarestorage.com"), ".")
		accountID = labels[0]
		if len(labels) > 1 {
			jurisdiction = labels[1]
		}
		fromEndpoint = true
	}
	accountID = strings.ToLower(accountID)
	if !accountIDPattern.MatchString(accountID) {
		return "", "", false, errors.New("账户 ID 应为 32 位十六进制字符")
	}
	return accountID, jurisdiction, fromEndpoint, nil
}

// EndpointURL 返回 R2 的 S3 兼容 API 地址。
func (c Config) EndpointURL() (string, error) {
	if c.Endpoint != "" {
		return strings.TrimRight(c.Endpoint, "/"), nil
	}
	if c.AccountID == "" {
		return "", errors.New("缺少 account_id（可设置 R2_ACCOUNT_ID 或 --account-id）")
	}
	if c.Jurisdiction != "" {
		return fmt.Sprintf("https://%s.%s.r2.cloudflarestorage.com", c.AccountID, c.Jurisdiction), nil
	}
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", c.AccountID), nil
}

// Validate 检查连接 R2 所需的字段。
func (c Config) Validate() error {
	var missing []string
	if c.AccessKeyID == "" {
		missing = append(missing, "access_key_id (R2_ACCESS_KEY_ID)")
	}
	if c.SecretAccessKey == "" {
		missing = append(missing, "secret_access_key (R2_SECRET_ACCESS_KEY)")
	}
	if c.Bucket == "" {
		missing = append(missing, "bucket (R2_BUCKET 或 --bucket)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("配置不完整，缺少: %s", strings.Join(missing, ", "))
	}
	_, err := c.EndpointURL()
	return err
}
