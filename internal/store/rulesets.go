package store

import (
	"fmt"
	"time"
)

// 规则集的 Behavior 与 Format 合法取值（mihomo rule-provider 约束）。
const (
	BehaviorDomain    = "domain"
	BehaviorIPCIDR    = "ipcidr"
	BehaviorClassical = "classical"
	FormatYAML        = "yaml"
	FormatText        = "text"
)

// RuleSet 是一个规则集（mihomo rule-provider 源）。时间字段为 RFC3339。
type RuleSet struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Behavior  string `json:"behavior"`
	Format    string `json:"format"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// RuleSetPatch 是规则集的部分更新载荷：nil 字段表示保留原值。
type RuleSetPatch struct {
	Name     *string `json:"name"`
	URL      *string `json:"url"`
	Behavior *string `json:"behavior"`
	Format   *string `json:"format"`
	Enabled  *bool   `json:"enabled"`
}

// ListRuleSets 返回规则集列表的副本切片。
func (s *Store) ListRuleSets() []RuleSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RuleSet, len(s.ruleSets))
	copy(out, s.ruleSets)
	return out
}

// GetRuleSet 按 ID 查找规则集。
func (s *Store) GetRuleSet(id string) (RuleSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rs := range s.ruleSets {
		if rs.ID == id {
			return rs, true
		}
	}
	return RuleSet{}, false
}

// CreateRuleSet 新建规则集：name/url 为空报错；behavior 必须为
// domain/ipcidr/classical，format 必须为 yaml/text，非法报错。创建成功后落盘。
func (s *Store) CreateRuleSet(name, url, behavior, format string, enabled bool) (RuleSet, error) {
	if name == "" {
		return RuleSet{}, fmt.Errorf("name 不能为空: %w", ErrInvalid)
	}
	if url == "" {
		return RuleSet{}, fmt.Errorf("url 不能为空: %w", ErrInvalid)
	}
	if err := validateBehaviorFormat(behavior, format); err != nil {
		return RuleSet{}, err
	}
	id, err := newID()
	if err != nil {
		return RuleSet{}, err
	}
	now := time.Now().Format(time.RFC3339)
	rs := RuleSet{ID: id, Name: name, URL: url, Behavior: behavior, Format: format, Enabled: enabled, CreatedAt: now, UpdatedAt: now}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ruleSets = append(s.ruleSets, rs)
	if err := s.writeFile("rulesets.json", ruleSetsFile{Version: dataVersion, RuleSets: s.ruleSets}); err != nil {
		s.ruleSets = s.ruleSets[:len(s.ruleSets)-1] // 落盘失败回滚
		return RuleSet{}, err
	}
	return rs, nil
}

// UpdateRuleSet 部分更新规则集：patch 中 nil 字段保留原值；非 nil 且指向
// 空串的 URL 报错；behavior/format 非 nil 时校验合法取值（Create/Update 都校验）。
// 规则集不存在报错。更新后 UpdatedAt 刷新并落盘。
func (s *Store) UpdateRuleSet(id string, patch RuleSetPatch) (RuleSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.ruleSets {
		if s.ruleSets[i].ID != id {
			continue
		}
		if patch.URL != nil && *patch.URL == "" {
			return RuleSet{}, fmt.Errorf("url 不能为空: %w", ErrInvalid)
		}
		behavior, format := s.ruleSets[i].Behavior, s.ruleSets[i].Format
		if patch.Behavior != nil {
			behavior = *patch.Behavior
		}
		if patch.Format != nil {
			format = *patch.Format
		}
		if err := validateBehaviorFormat(behavior, format); err != nil {
			return RuleSet{}, err
		}
		snapshot := s.ruleSets[i]
		if patch.Name != nil {
			s.ruleSets[i].Name = *patch.Name
		}
		if patch.URL != nil {
			s.ruleSets[i].URL = *patch.URL
		}
		if patch.Behavior != nil {
			s.ruleSets[i].Behavior = *patch.Behavior
		}
		if patch.Format != nil {
			s.ruleSets[i].Format = *patch.Format
		}
		if patch.Enabled != nil {
			s.ruleSets[i].Enabled = *patch.Enabled
		}
		s.ruleSets[i].UpdatedAt = time.Now().Format(time.RFC3339)
		if err := s.writeFile("rulesets.json", ruleSetsFile{Version: dataVersion, RuleSets: s.ruleSets}); err != nil {
			s.ruleSets[i] = snapshot // 落盘失败回滚本条修改
			return RuleSet{}, err
		}
		return s.ruleSets[i], nil
	}
	return RuleSet{}, fmt.Errorf("ruleset %s 不存在: %w", id, ErrNotFound)
}

// DeleteRuleSet 删除规则集；规则集不存在报错。删除成功后落盘。
func (s *Store) DeleteRuleSet(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.ruleSets {
		if s.ruleSets[i].ID != id {
			continue
		}
		snapshot := append([]RuleSet(nil), s.ruleSets...)
		s.ruleSets = append(s.ruleSets[:i], s.ruleSets[i+1:]...)
		if err := s.writeFile("rulesets.json", ruleSetsFile{Version: dataVersion, RuleSets: s.ruleSets}); err != nil {
			s.ruleSets = snapshot // 落盘失败回滚
			return err
		}
		return nil
	}
	return fmt.Errorf("ruleset %s 不存在: %w", id, ErrNotFound)
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
