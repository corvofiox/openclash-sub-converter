// Package store 提供 Web 管理台的 JSON 持久化数据层。
//
// 三组数据（订阅源 sources、转换日志 logs、规则模板 templates）各自独立落盘
// 为 <name>.json（格式 {"version":1,"<name>":[...]}），共用一把读写锁：
// 读操作持 RLock，写操作持 Lock 并覆盖「内存更新 + 落盘」全过程，保证并发
// 安全与崩溃一致性。落盘采用原子写：同目录临时文件写入 + fsync + rename，
// 避免读到半成品文件。
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// dataVersion 是持久化文件格式版本号，读取时校验；不匹配视为空态。
const dataVersion = 1

// Store 是数据层核心，持有数据目录与三组内存态。
type Store struct {
	dataDir   string
	mu        sync.RWMutex
	sources   []Source
	logs      []LogEntry
	templates []RuleTemplate
}

// New 创建 Store：MkdirAll 数据目录后依次读回三份持久化文件。
// 文件不存在视为空态继续；文件损坏（JSON 解析失败）时 log warn、将原文件
// 备份为 <name>.json.bak 并以空态继续；version 不匹配时 log warn 并以空态
// 继续——均不崩溃。
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dir, err)
	}
	s := &Store{dataDir: dir}
	if err := s.loadSources(); err != nil {
		return nil, err
	}
	if err := s.loadLogs(); err != nil {
		return nil, err
	}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

// newID 生成 12 位十六进制随机 ID（crypto/rand 读 6 字节）。
func newID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// loadJSON 读取并解析一份持久化文件。返回 existed 表示文件是否存在；
// 文件不存在返回 (false, nil)（空态）；读取失败或 JSON 解析失败时 log warn
// （解析失败额外备份 .bak）并按空态继续，返回 (true, nil)。
func (s *Store) loadJSON(name string, v any) (bool, error) {
	data, err := os.ReadFile(filepath.Join(s.dataDir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		slog.Warn("store: 读取持久化文件失败，按空态继续", "file", name, "err", err)
		return true, nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		slog.Warn("store: 持久化文件损坏，备份后按空态继续", "file", name, "err", err)
		s.backup(name, data)
		return true, nil
	}
	return true, nil
}

// backup 将损坏文件原样复制为 <name>.json.bak（0600），保留现场便于排查。
func (s *Store) backup(name string, data []byte) {
	bak := filepath.Join(s.dataDir, name+".bak")
	if err := os.WriteFile(bak, data, 0o600); err != nil {
		slog.Warn("store: 备份损坏文件失败", "file", name, "bak", bak, "err", err)
	}
}

// writeFile 原子落盘：json.MarshalIndent 序列化 → 同目录临时文件写入 →
// fsync → 关闭 → rename 覆盖目标。临时文件权限 0600（os.CreateTemp 默认），
// rename 后目标文件即 0600。
func (s *Store) writeFile(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	tmp, err := os.CreateTemp(s.dataDir, "*.tmp")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", name, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后无副作用；失败时清理半成品
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp for %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp for %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp for %s: %w", name, err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dataDir, name)); err != nil {
		return fmt.Errorf("rename temp for %s: %w", name, err)
	}
	return nil
}

// sourcesFile / logsFile / templatesFile 是磁盘文件格式（version 字段预留）。
type sourcesFile struct {
	Version int      `json:"version"`
	Sources []Source `json:"sources"`
}

type logsFile struct {
	Version int        `json:"version"`
	Logs    []LogEntry `json:"logs"`
}

type templatesFile struct {
	Version   int            `json:"version"`
	Templates []RuleTemplate `json:"templates"`
}

func (s *Store) loadSources() error {
	var f sourcesFile
	existed, err := s.loadJSON("sources.json", &f)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}
	if f.Version != dataVersion {
		slog.Warn("store: sources.json 版本不匹配，按空态继续", "version", f.Version)
		return nil
	}
	s.sources = f.Sources
	return nil
}

func (s *Store) loadLogs() error {
	var f logsFile
	existed, err := s.loadJSON("logs.json", &f)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}
	if f.Version != dataVersion {
		slog.Warn("store: logs.json 版本不匹配，按空态继续", "version", f.Version)
		return nil
	}
	s.logs = f.Logs
	return nil
}

func (s *Store) loadTemplates() error {
	var f templatesFile
	existed, err := s.loadJSON("templates.json", &f)
	if err != nil {
		return err
	}
	if !existed {
		// 首次启动：templates.json 不存在 → 种入 8 个预置模板并落盘。
		// 文件已存在（含空列表）绝不种入；损坏恢复（.bak 后空态）与
		// version 不匹配空态同样不触发——预置删光后不复活。
		return s.seedPresetTemplates()
	}
	if f.Version != dataVersion {
		slog.Warn("store: templates.json 版本不匹配，按空态继续", "version", f.Version)
		return nil
	}
	s.templates = f.Templates
	return nil
}
