// Package config 提供服务配置的加载、默认值合并与环境变量覆盖。
package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// 默认值常量。
const (
	DefaultPort        = 25500
	DefaultUserAgent   = "clash-verge/v2.0.0"
	DefaultTimeoutSec  = 20
	DefaultCacheTTLSec = 300
	DefaultMaxBytes    = 10 * 1024 * 1024 // 10MB
	DefaultLogLevel    = "info"
	DefaultDataDir     = "./data"
)

// Config 是服务根配置。
type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Fetcher FetcherConfig `yaml:"fetcher"`
	Logging LoggingConfig `yaml:"logging"`
	// AdminToken 是管理台 API 的访问令牌，通过请求头 X-Token 或
	// Authorization: Bearer 携带。安全约定：仅通过环境变量 OSC_ADMIN_TOKEN
	// 注入，不读配置文件（yaml:"-"），避免令牌随配置文件分发泄露。
	// 空串表示不启用鉴权。
	AdminToken string `yaml:"-"`
}

// ServerConfig 是 HTTP 服务配置。
type ServerConfig struct {
	Port int `yaml:"port"`
	// DataDir 是 Web 管理台数据（订阅源/日志/模板）的持久化目录。
	DataDir string `yaml:"data_dir"`
}

// FetcherConfig 是订阅源拉取配置。
type FetcherConfig struct {
	UserAgent       string `yaml:"user_agent"`
	TimeoutSeconds  int    `yaml:"timeout_seconds"`
	CacheTTLSeconds int    `yaml:"cache_ttl_seconds"`
	MaxBytes        int64  `yaml:"max_bytes"`
}

// LoggingConfig 是日志配置。
type LoggingConfig struct {
	Level string `yaml:"level"`
}

// Default 返回全默认配置。
func Default() *Config {
	return &Config{
		Server: ServerConfig{Port: DefaultPort, DataDir: DefaultDataDir},
		Fetcher: FetcherConfig{
			UserAgent:       DefaultUserAgent,
			TimeoutSeconds:  DefaultTimeoutSec,
			CacheTTLSeconds: DefaultCacheTTLSec,
			MaxBytes:        DefaultMaxBytes,
		},
		Logging: LoggingConfig{Level: DefaultLogLevel},
	}
}

// Load 从 path 加载配置：文件不存在时返回默认配置；存在则解析 YAML 并用
// 默认值补齐零值字段，随后做基础校验。
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return cfg, nil
			}
			return nil, fmt.Errorf("read config file: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}
	}
	cfg.mergeDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// mergeDefaults 用默认值补齐零值字段。
func (c *Config) mergeDefaults() {
	d := Default()
	if c.Server.Port == 0 {
		c.Server.Port = d.Server.Port
	}
	if c.Server.DataDir == "" {
		c.Server.DataDir = d.Server.DataDir
	}
	if c.Fetcher.UserAgent == "" {
		c.Fetcher.UserAgent = d.Fetcher.UserAgent
	}
	if c.Fetcher.TimeoutSeconds == 0 {
		c.Fetcher.TimeoutSeconds = d.Fetcher.TimeoutSeconds
	}
	if c.Fetcher.CacheTTLSeconds == 0 {
		c.Fetcher.CacheTTLSeconds = d.Fetcher.CacheTTLSeconds
	}
	if c.Fetcher.MaxBytes == 0 {
		c.Fetcher.MaxBytes = d.Fetcher.MaxBytes
	}
	if c.Logging.Level == "" {
		c.Logging.Level = d.Logging.Level
	}
}

// validate 校验配置合法性：port 越界报错；负的 cache TTL 归零。
func (c *Config) validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port %d: must be in range 1-65535", c.Server.Port)
	}
	if c.Fetcher.CacheTTLSeconds < 0 {
		c.Fetcher.CacheTTLSeconds = 0
	}
	return nil
}

// ApplyEnv 用环境变量覆盖配置（12-factor，Docker 用）：
// OSC_PORT / OSC_FETCHER_UA / OSC_CACHE_TTL / OSC_LOG_LEVEL / OSC_DATA_DIR /
// OSC_ADMIN_TOKEN。
// 数值解析失败的环境变量被忽略（保持原值）。
func (c *Config) ApplyEnv() {
	if v := os.Getenv("OSC_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Server.Port = n
		}
	}
	if v := os.Getenv("OSC_DATA_DIR"); v != "" {
		c.Server.DataDir = v
	}
	if v := os.Getenv("OSC_ADMIN_TOKEN"); v != "" {
		c.AdminToken = v
	}
	if v := os.Getenv("OSC_FETCHER_UA"); v != "" {
		c.Fetcher.UserAgent = v
	}
	if v := os.Getenv("OSC_CACHE_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n < 0 {
				n = 0
			}
			c.Fetcher.CacheTTLSeconds = n
		}
	}
	if v := os.Getenv("OSC_LOG_LEVEL"); v != "" {
		c.Logging.Level = v
	}
}
