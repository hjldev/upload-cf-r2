package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// clearEnv 清空 R2_* 环境变量，避免开发机上的设置影响测试。
func clearEnv(t *testing.T) {
	for _, v := range envVars {
		t.Setenv(v.name, "")
	}
}

func TestLoadPrecedence(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"account_id":"file-acct","access_key_id":"file-id","secret_access_key":"file-secret","bucket":"file-bucket"}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("R2_BUCKET", "env-bucket")

	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccountID != "file-acct" || cfg.AccessKeyID != "file-id" {
		t.Errorf("配置文件中的值未生效: %+v", cfg)
	}
	if cfg.Bucket != "env-bucket" {
		t.Errorf("环境变量应覆盖配置文件，得到 bucket=%q", cfg.Bucket)
	}
}

func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.json")
	if _, err := Load(missing, false); err != nil {
		t.Errorf("默认配置文件不存在时不应报错: %v", err)
	}
	if _, err := Load(missing, true); err == nil {
		t.Error("显式指定的配置文件不存在时应报错")
	}
}

func TestEndpointURL(t *testing.T) {
	tests := []struct {
		cfg  Config
		want string
	}{
		{Config{AccountID: "abc"}, "https://abc.r2.cloudflarestorage.com"},
		{Config{AccountID: "abc", Jurisdiction: "eu"}, "https://abc.eu.r2.cloudflarestorage.com"},
		{Config{AccountID: "abc", Endpoint: "http://127.0.0.1:9000/"}, "http://127.0.0.1:9000"},
	}
	for _, tt := range tests {
		got, err := tt.cfg.EndpointURL()
		if err != nil || got != tt.want {
			t.Errorf("EndpointURL(%+v) = %q, %v; want %q", tt.cfg, got, err, tt.want)
		}
	}
	if _, err := (Config{}).EndpointURL(); err == nil {
		t.Error("缺少 account_id 时应报错")
	}
}

func TestValidate(t *testing.T) {
	ok := Config{AccountID: "a", AccessKeyID: "b", SecretAccessKey: "c", Bucket: "d"}
	if err := ok.Validate(); err != nil {
		t.Errorf("完整配置不应报错: %v", err)
	}
	bad := ok
	bad.SecretAccessKey = ""
	if err := bad.Validate(); err == nil {
		t.Error("缺少 secret_access_key 时应报错")
	}
}

func TestParseAccount(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	tests := []struct {
		in, wantID, wantJur string
		wantEndpoint        bool
	}{
		{id, id, "", false},
		{"  " + strings.ToUpper(id) + " ", id, "", false},
		{"https://" + id + ".r2.cloudflarestorage.com", id, "", true},
		{"https://" + id + ".eu.r2.cloudflarestorage.com/my-bucket", id, "eu", true},
		{id + ".r2.cloudflarestorage.com", id, "", true},
	}
	for _, tt := range tests {
		gotID, gotJur, fromEndpoint, err := ParseAccount(tt.in)
		if err != nil || gotID != tt.wantID || gotJur != tt.wantJur || fromEndpoint != tt.wantEndpoint {
			t.Errorf("ParseAccount(%q) = %q, %q, %v, %v; want %q, %q, %v",
				tt.in, gotID, gotJur, fromEndpoint, err, tt.wantID, tt.wantJur, tt.wantEndpoint)
		}
	}
	for _, bad := range []string{"", "abc", "https://abc.r2.cloudflarestorage.com"} {
		if _, _, _, err := ParseAccount(bad); err == nil {
			t.Errorf("ParseAccount(%q) 应报错", bad)
		}
	}
}

func TestSave(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "nested", "dir", "config.json")
	want := Config{AccountID: "a", AccessKeyID: "b", SecretAccessKey: "c", Bucket: "d", PublicURL: "https://cdn.example.com"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("读回的配置 = %+v, want %+v", got, want)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("配置文件权限 = %o, want 600", perm)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("目录中应只有配置文件，实际有 %d 个条目（临时文件未清理？）", len(entries))
	}
}
