package store

import (
	"fmt"
	"time"
)

// 规则模板的 Behavior 与 Format 合法取值（mihomo rule-provider 约束）。
const (
	BehaviorDomain    = "domain"
	BehaviorIPCIDR    = "ipcidr"
	BehaviorClassical = "classical"
	FormatYAML        = "yaml"
	FormatText        = "text"
)

// RuleTemplate 是一个规则模板（mihomo rule-provider 源）。时间字段为 RFC3339。
type RuleTemplate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Behavior  string `json:"behavior"`
	Format    string `json:"format"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// TemplatePatch 是规则模板的部分更新载荷：nil 字段表示保留原值。
type TemplatePatch struct {
	Name     *string `json:"name"`
	URL      *string `json:"url"`
	Behavior *string `json:"behavior"`
	Format   *string `json:"format"`
	Enabled  *bool   `json:"enabled"`
}

// ListTemplates 返回规则模板列表的副本切片。
func (s *Store) ListTemplates() []RuleTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RuleTemplate, len(s.templates))
	copy(out, s.templates)
	return out
}

// GetTemplate 按 ID 查找规则模板。
func (s *Store) GetTemplate(id string) (RuleTemplate, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.templates {
		if t.ID == id {
			return t, true
		}
	}
	return RuleTemplate{}, false
}

// CreateTemplate 新建规则模板：name/url 为空报错；behavior 必须为
// domain/ipcidr/classical，format 必须为 yaml/text，非法报错。创建成功后落盘。
func (s *Store) CreateTemplate(name, url, behavior, format string, enabled bool) (RuleTemplate, error) {
	if name == "" {
		return RuleTemplate{}, fmt.Errorf("name 不能为空: %w", ErrInvalid)
	}
	if url == "" {
		return RuleTemplate{}, fmt.Errorf("url 不能为空: %w", ErrInvalid)
	}
	if err := validateBehaviorFormat(behavior, format); err != nil {
		return RuleTemplate{}, err
	}
	id, err := newID()
	if err != nil {
		return RuleTemplate{}, err
	}
	now := time.Now().Format(time.RFC3339)
	t := RuleTemplate{ID: id, Name: name, URL: url, Behavior: behavior, Format: format, Enabled: enabled, CreatedAt: now, UpdatedAt: now}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = append(s.templates, t)
	if err := s.writeFile("templates.json", templatesFile{Version: dataVersion, Templates: s.templates}); err != nil {
		s.templates = s.templates[:len(s.templates)-1] // 落盘失败回滚
		return RuleTemplate{}, err
	}
	return t, nil
}

// UpdateTemplate 部分更新规则模板：patch 中 nil 字段保留原值；非 nil 且指向
// 空串的 URL 报错；behavior/format 非 nil 时校验合法取值（Create/Update 都校验）。
// 模板不存在报错。更新后 UpdatedAt 刷新并落盘。
func (s *Store) UpdateTemplate(id string, patch TemplatePatch) (RuleTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.templates {
		if s.templates[i].ID != id {
			continue
		}
		if patch.URL != nil && *patch.URL == "" {
			return RuleTemplate{}, fmt.Errorf("url 不能为空: %w", ErrInvalid)
		}
		behavior, format := s.templates[i].Behavior, s.templates[i].Format
		if patch.Behavior != nil {
			behavior = *patch.Behavior
		}
		if patch.Format != nil {
			format = *patch.Format
		}
		if err := validateBehaviorFormat(behavior, format); err != nil {
			return RuleTemplate{}, err
		}
		snapshot := s.templates[i]
		if patch.Name != nil {
			s.templates[i].Name = *patch.Name
		}
		if patch.URL != nil {
			s.templates[i].URL = *patch.URL
		}
		if patch.Behavior != nil {
			s.templates[i].Behavior = *patch.Behavior
		}
		if patch.Format != nil {
			s.templates[i].Format = *patch.Format
		}
		if patch.Enabled != nil {
			s.templates[i].Enabled = *patch.Enabled
		}
		s.templates[i].UpdatedAt = time.Now().Format(time.RFC3339)
		if err := s.writeFile("templates.json", templatesFile{Version: dataVersion, Templates: s.templates}); err != nil {
			s.templates[i] = snapshot // 落盘失败回滚本条修改
			return RuleTemplate{}, err
		}
		return s.templates[i], nil
	}
	return RuleTemplate{}, fmt.Errorf("template %s 不存在: %w", id, ErrNotFound)
}

// DeleteTemplate 删除规则模板；模板不存在报错。删除成功后落盘。
func (s *Store) DeleteTemplate(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.templates {
		if s.templates[i].ID != id {
			continue
		}
		snapshot := append([]RuleTemplate(nil), s.templates...)
		s.templates = append(s.templates[:i], s.templates[i+1:]...)
		if err := s.writeFile("templates.json", templatesFile{Version: dataVersion, Templates: s.templates}); err != nil {
			s.templates = snapshot // 落盘失败回滚
			return err
		}
		return nil
	}
	return fmt.Errorf("template %s 不存在: %w", id, ErrNotFound)
}

// validateBehaviorFormat 校验 rule-provider 的 behavior/format 合法取值。
func validateBehaviorFormat(behavior, format string) error {
	switch behavior {
	case BehaviorDomain, BehaviorIPCIDR, BehaviorClassical:
	default:
		return fmt.Errorf("behavior 必须为 domain/ipcidr/classical: %w", ErrInvalid)
	}
	switch format {
	case FormatYAML, FormatText:
	default:
		return fmt.Errorf("format 必须为 yaml/text: %w", ErrInvalid)
	}
	return nil
}
