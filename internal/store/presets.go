package store

import (
	"fmt"
	"time"
)

// presetRuleSetBaseURL 是 ACL4SSR 官方仓库 Clash 规则集的固定前缀。
const presetRuleSetBaseURL = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/"

// presetRuleSetDef 描述一个预置规则集的静态字段（ID/时间戳由种入时生成）。
type presetRuleSetDef struct {
	name string
	url  string
}

// presetRuleSetDefs 是首次启动种入的 8 个 ACL4SSR 常用规则集：
// 行为统一 domain、格式统一 text、默认禁用（Enabled=false），由用户在
// 管理台自行启用。种入后即为普通规则集——可编辑、可删除、可改名，无特殊标记。
var presetRuleSetDefs = []presetRuleSetDef{
	{"广告拦截", presetRuleSetBaseURL + "BanAD.list"},
	{"Netflix 视频", presetRuleSetBaseURL + "Ruleset/Netflix.list"},
	{"YouTube 视频", presetRuleSetBaseURL + "Ruleset/YouTube.list"},
	{"Telegram 通讯", presetRuleSetBaseURL + "Ruleset/Telegram.list"},
	{"Google 服务", presetRuleSetBaseURL + "Ruleset/Google.list"},
	{"Twitter 社交", presetRuleSetBaseURL + "Ruleset/Twitter.list"},
	{"Apple 服务", presetRuleSetBaseURL + "Ruleset/Apple.list"},
	{"Microsoft 服务", presetRuleSetBaseURL + "Ruleset/Microsoft.list"},
}

// seedPresetRuleSets 生成 8 个预置规则集并落盘 rulesets.json。仅在
// store.New 首次启动（rulesets.json 与旧 templates.json 都不存在）时由
// loadRuleSets 调用；文件已存在（哪怕空列表）、损坏恢复空态、version 不匹配
// 空态均不触发——用户删光预置后重启不会复活。
//
// ID 复用 CreateRuleSet 相同的 crypto/rand 12 hex 逻辑（newID），落盘
// 复用 writeFile 原子写（临时文件 + fsync + rename，权限 0600），与
// 普通规则集创建完全一致。
func (s *Store) seedPresetRuleSets() error {
	now := time.Now().Format(time.RFC3339)
	ruleSets := make([]RuleSet, 0, len(presetRuleSetDefs))
	for _, def := range presetRuleSetDefs {
		id, err := newID()
		if err != nil {
			return err
		}
		ruleSets = append(ruleSets, RuleSet{
			ID:        id,
			Name:      def.name,
			URL:       def.url,
			Behavior:  BehaviorDomain,
			Format:    FormatText,
			Enabled:   false,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	s.ruleSets = ruleSets
	if err := s.writeFile("rulesets.json", ruleSetsFile{Version: dataVersion, RuleSets: ruleSets}); err != nil {
		return fmt.Errorf("seed preset rule sets: %w", err)
	}
	return nil
}