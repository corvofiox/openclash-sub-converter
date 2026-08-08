package store

import (
	"fmt"
	"time"
)

// Source 是一个订阅源。时间字段为 RFC3339 字符串。
type Source struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// SourcePatch 是部分更新载荷：nil 字段表示保留原值。
// 约定：URL 仅当非 nil 且 *URL != "" 时才更新；非 nil 且指向空串 → 报错。
type SourcePatch struct {
	Name    *string `json:"name"`
	URL     *string `json:"url"`
	Enabled *bool   `json:"enabled"`
}

// ListSources 返回订阅源列表的副本切片（调用方修改不影响内部状态）。
func (s *Store) ListSources() []Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Source, len(s.sources))
	copy(out, s.sources)
	return out
}

// GetSource 按 ID 查找订阅源。
func (s *Store) GetSource(id string) (Source, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, src := range s.sources {
		if src.ID == id {
			return src, true
		}
	}
	return Source{}, false
}

// CreateSource 新建订阅源：ID 为 12 位十六进制随机值；name/url 为空报错；
// CreatedAt/UpdatedAt 取当前 RFC3339 时间；创建成功后落盘。
func (s *Store) CreateSource(name, url string, enabled bool) (Source, error) {
	if name == "" {
		return Source{}, fmt.Errorf("name 不能为空: %w", ErrInvalid)
	}
	if url == "" {
		return Source{}, fmt.Errorf("url 不能为空: %w", ErrInvalid)
	}
	id, err := newID()
	if err != nil {
		return Source{}, err
	}
	now := time.Now().Format(time.RFC3339)
	src := Source{ID: id, Name: name, URL: url, Enabled: enabled, CreatedAt: now, UpdatedAt: now}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sources = append(s.sources, src)
	if err := s.writeFile("sources.json", sourcesFile{Version: dataVersion, Sources: s.sources}); err != nil {
		// 落盘失败回滚内存，保持内存与磁盘一致
		s.sources = s.sources[:len(s.sources)-1]
		return Source{}, err
	}
	return src, nil
}

// UpdateSource 部分更新订阅源：patch 中 nil 字段保留原值；非 nil 且指向
// 空串的 URL 报错「url 不能为空」；源不存在报错。更新后 UpdatedAt 刷新并落盘。
func (s *Store) UpdateSource(id string, patch SourcePatch) (Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sources {
		if s.sources[i].ID != id {
			continue
		}
		if patch.URL != nil && *patch.URL == "" {
			return Source{}, fmt.Errorf("url 不能为空: %w", ErrInvalid)
		}
		snapshot := s.sources[i]
		if patch.Name != nil {
			s.sources[i].Name = *patch.Name
		}
		if patch.URL != nil {
			s.sources[i].URL = *patch.URL
		}
		if patch.Enabled != nil {
			s.sources[i].Enabled = *patch.Enabled
		}
		s.sources[i].UpdatedAt = time.Now().Format(time.RFC3339)
		if err := s.writeFile("sources.json", sourcesFile{Version: dataVersion, Sources: s.sources}); err != nil {
			s.sources[i] = snapshot // 落盘失败回滚本条修改
			return Source{}, err
		}
		return s.sources[i], nil
	}
	return Source{}, fmt.Errorf("source %s 不存在: %w", id, ErrNotFound)
}

// DeleteSource 删除订阅源；源不存在报错。删除成功后落盘。
func (s *Store) DeleteSource(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sources {
		if s.sources[i].ID != id {
			continue
		}
		snapshot := append([]Source(nil), s.sources...)
		s.sources = append(s.sources[:i], s.sources[i+1:]...)
		if err := s.writeFile("sources.json", sourcesFile{Version: dataVersion, Sources: s.sources}); err != nil {
			s.sources = snapshot // 落盘失败回滚
			return err
		}
		return nil
	}
	return fmt.Errorf("source %s 不存在: %w", id, ErrNotFound)
}
