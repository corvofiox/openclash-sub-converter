package store

import (
	"maps"
	"time"
)

// maxLogEntries 是转换日志环形上限：超出后淘汰最旧条目。
const maxLogEntries = 200

// LogEntry 是一条订阅转换日志。Error 为 nil 表示成功。
// URLRedacted 与 URLFull 分开存：API 层展示只读 Redacted，retry 时用 URLFull。
type LogEntry struct {
	ID          string         `json:"id"`
	TS          string         `json:"ts"` // RFC3339
	Kind        string         `json:"kind"`
	SourceID    string         `json:"source_id"`
	SourceName  string         `json:"source_name"`
	URLRedacted string         `json:"url_redacted"`
	URLFull     string         `json:"url_full"`
	Params      map[string]any `json:"params"`
	Status      string         `json:"status"`
	Error       *string        `json:"error"`
	NodeCount   int            `json:"node_count"`
	DurationMS  int64          `json:"duration_ms"`
}

// AppendLog 追加一条转换日志：ID 为空时自动生成 12 位十六进制 ID；TS 为空时
// 自动填当前 RFC3339 时间（调用方给了就用）。超过环形上限 200 条时淘汰最旧
// 条目，随后落盘。
//
// 内存与磁盘一致性：先构造「旧日志 + 新条目」的新切片（含淘汰逻辑），落盘
// 成功后才替换 s.logs；落盘失败返回 err 且内存态不变（与 sources/rule-sets
// 的「落盘失败回滚」语义一致）。
func (s *Store) AppendLog(e LogEntry) (LogEntry, error) {
	if e.ID == "" {
		id, err := newID()
		if err != nil {
			return LogEntry{}, err
		}
		e.ID = id
	}
	if e.TS == "" {
		e.TS = time.Now().Format(time.RFC3339)
	}
	// 浅拷贝 Params，避免调用方后续修改污染内存态
	e.Params = maps.Clone(e.Params)

	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([]LogEntry, 0, min(len(s.logs)+1, maxLogEntries))
	next = append(next, s.logs...)
	next = append(next, e)
	if len(next) > maxLogEntries {
		next = next[len(next)-maxLogEntries:]
	}
	if err := s.writeFile("logs.json", logsFile{Version: dataVersion, Logs: next}); err != nil {
		return LogEntry{}, err
	}
	s.logs = next
	return e, nil
}

// ListLogs 返回转换日志，按时间倒序（最新在前）。
// limit<=0 默认 50，上限 200；返回副本切片。
func (s *Store) ListLogs(limit int) []LogEntry {
	if limit <= 0 {
		limit = 50
	}
	if limit > maxLogEntries {
		limit = maxLogEntries
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LogEntry, 0, min(limit, len(s.logs)))
	for i := len(s.logs) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.logs[i])
	}
	return out
}

// GetLog 按 ID 查找转换日志。
func (s *Store) GetLog(id string) (LogEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.logs {
		if e.ID == id {
			return e, true
		}
	}
	return LogEntry{}, false
}
