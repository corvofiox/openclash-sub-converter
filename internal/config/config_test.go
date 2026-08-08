package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// 文件不存在 → 返回默认配置，不报错
	cfg, err := Load(filepath.Join(t.TempDir(), "no-such-config.yaml"))
	if err != nil {
		t.Fatalf("Load(nonexistent) error: %v", err)
	}
	if cfg.Server.Port != DefaultPort {
		t.Errorf("port = %d, want %d", cfg.Server.Port, DefaultPort)
	}
	if cfg.Fetcher.UserAgent != DefaultUserAgent {
		t.Errorf("user_agent = %q, want %q", cfg.Fetcher.UserAgent, DefaultUserAgent)
	}
	if cfg.Fetcher.TimeoutSeconds != DefaultTimeoutSec {
		t.Errorf("timeout_seconds = %d, want %d", cfg.Fetcher.TimeoutSeconds, DefaultTimeoutSec)
	}
	if cfg.Fetcher.CacheTTLSeconds != DefaultCacheTTLSec {
		t.Errorf("cache_ttl_seconds = %d, want %d", cfg.Fetcher.CacheTTLSeconds, DefaultCacheTTLSec)
	}
	if cfg.Fetcher.MaxBytes != DefaultMaxBytes {
		t.Errorf("max_bytes = %d, want %d", cfg.Fetcher.MaxBytes, DefaultMaxBytes)
	}
	if cfg.Logging.Level != DefaultLogLevel {
		t.Errorf("level = %q, want %q", cfg.Logging.Level, DefaultLogLevel)
	}
}

func TestLoadYAMLMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "server:\n  port: 8080\nfetcher:\n  timeout_seconds: 5\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Fetcher.TimeoutSeconds != 5 {
		t.Errorf("timeout_seconds = %d, want 5", cfg.Fetcher.TimeoutSeconds)
	}
	// 未写出的字段用默认值
	if cfg.Fetcher.UserAgent != DefaultUserAgent {
		t.Errorf("user_agent = %q, want default %q", cfg.Fetcher.UserAgent, DefaultUserAgent)
	}
	if cfg.Fetcher.CacheTTLSeconds != DefaultCacheTTLSec {
		t.Errorf("cache_ttl_seconds = %d, want default %d", cfg.Fetcher.CacheTTLSeconds, DefaultCacheTTLSec)
	}
	if cfg.Fetcher.MaxBytes != DefaultMaxBytes {
		t.Errorf("max_bytes = %d, want default %d", cfg.Fetcher.MaxBytes, DefaultMaxBytes)
	}
	if cfg.Logging.Level != DefaultLogLevel {
		t.Errorf("level = %q, want default %q", cfg.Logging.Level, DefaultLogLevel)
	}
}

func TestLoadInvalidPort(t *testing.T) {
	dir := t.TempDir()
	// port=0 是零值会被默认值合并为 25500（合法），非零越界值必须报错
	for _, port := range []int{-1, 65536, 99999} {
		path := filepath.Join(dir, "config.yaml")
		yaml := "server:\n  port: " + itoa(port) + "\n"
		if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("Load(port=%d) = nil error, want error", port)
		} else if !strings.Contains(err.Error(), "port") {
			t.Errorf("Load(port=%d) error %q does not mention port", port, err)
		}
	}
	// 边界合法值
	path := filepath.Join(dir, "ok.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("Load(port=1) error: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestLoadNegativeCacheTTLClamped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "fetcher:\n  cache_ttl_seconds: -10\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Fetcher.CacheTTLSeconds != 0 {
		t.Errorf("cache_ttl_seconds = %d, want 0 (clamped)", cfg.Fetcher.CacheTTLSeconds)
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server: [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load(malformed) = nil error, want error")
	}
}

func TestLoadReadError(t *testing.T) {
	if _, err := Load("/dev/null/definitely-not-a-file"); err == nil {
		t.Error("Load(unreadable path) = nil error, want error")
	}
}

func TestApplyEnv(t *testing.T) {
	t.Setenv("OSC_PORT", "9999")
	t.Setenv("OSC_FETCHER_UA", "custom-ua/9.9")
	t.Setenv("OSC_CACHE_TTL", "42")
	t.Setenv("OSC_LOG_LEVEL", "debug")
	cfg := Default()
	cfg.ApplyEnv()
	if cfg.Server.Port != 9999 {
		t.Errorf("port = %d, want 9999", cfg.Server.Port)
	}
	if cfg.Fetcher.UserAgent != "custom-ua/9.9" {
		t.Errorf("user_agent = %q, want custom-ua/9.9", cfg.Fetcher.UserAgent)
	}
	if cfg.Fetcher.CacheTTLSeconds != 42 {
		t.Errorf("cache_ttl_seconds = %d, want 42", cfg.Fetcher.CacheTTLSeconds)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("level = %q, want debug", cfg.Logging.Level)
	}
}

func TestApplyEnvInvalidValuesIgnored(t *testing.T) {
	t.Setenv("OSC_PORT", "not-a-number")
	t.Setenv("OSC_CACHE_TTL", "abc")
	cfg := Default()
	cfg.ApplyEnv()
	if cfg.Server.Port != DefaultPort {
		t.Errorf("port = %d, want default %d (invalid env ignored)", cfg.Server.Port, DefaultPort)
	}
	if cfg.Fetcher.CacheTTLSeconds != DefaultCacheTTLSec {
		t.Errorf("cache_ttl_seconds = %d, want default %d", cfg.Fetcher.CacheTTLSeconds, DefaultCacheTTLSec)
	}
}

func TestApplyEnvNegativeTTLClamped(t *testing.T) {
	t.Setenv("OSC_CACHE_TTL", "-5")
	cfg := Default()
	cfg.ApplyEnv()
	if cfg.Fetcher.CacheTTLSeconds != 0 {
		t.Errorf("cache_ttl_seconds = %d, want 0", cfg.Fetcher.CacheTTLSeconds)
	}
}
