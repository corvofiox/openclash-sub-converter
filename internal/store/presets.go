package store

import (
	"fmt"
	"time"
)

// presetTemplateBaseURL 是 ACL4SSR 官方仓库 Clash 规则集的固定前缀。
const presetTemplateBaseURL = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/"

// presetTemplateDef 描述一个预置规则模板的静态字段（ID/时间戳由种入时生成）。
type presetTemplateDef struct {
	name string
	url  string
}

// presetTemplateDefs 是首次启动种入的 8 个 ACL4SSR 常用规则模板：
// 行为统一 domain、格式统一 text、默认禁用（Enabled=false），由用户在
// 管理台自行启用。种入后即为普通模板——可编辑、可删除、可改名，无特殊标记。
var presetTemplateDefs = []presetTemplateDef{
	{"广告拦截", presetTemplateBaseURL + "BanAD.list"},
	{"Netflix 视频", presetTemplateBaseURL + "Ruleset/Netflix.list"},
	{"YouTube 视频", presetTemplateBaseURL + "Ruleset/YouTube.list"},
	{"Telegram 通讯", presetTemplateBaseURL + "Ruleset/Telegram.list"},
	{"Google 服务", presetTemplateBaseURL + "Ruleset/Google.list"},
	{"Twitter 社交", presetTemplateBaseURL + "Ruleset/Twitter.list"},
	{"Apple 服务", presetTemplateBaseURL + "Ruleset/Apple.list"},
	{"Microsoft 服务", presetTemplateBaseURL + "Ruleset/Microsoft.list"},
}

// seedPresetTemplates 生成 8 个预置模板并落盘 templates.json。仅在
// store.New 首次启动（templates.json 不存在）时由 loadTemplates 调用；
// 文件已存在（哪怕空列表）、损坏恢复空态、version 不匹配空态均不触发——
// 用户删光预置后重启不会复活。
//
// ID 复用 CreateTemplate 相同的 crypto/rand 12 hex 逻辑（newID），落盘
// 复用 writeFile 原子写（临时文件 + fsync + rename，权限 0600），与
// 普通模板创建完全一致。
func (s *Store) seedPresetTemplates() error {
	now := time.Now().Format(time.RFC3339)
	templates := make([]RuleTemplate, 0, len(presetTemplateDefs))
	for _, def := range presetTemplateDefs {
		id, err := newID()
		if err != nil {
			return err
		}
		templates = append(templates, RuleTemplate{
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
	s.templates = templates
	if err := s.writeFile("templates.json", templatesFile{Version: dataVersion, Templates: templates}); err != nil {
		return fmt.Errorf("seed preset templates: %w", err)
	}
	return nil
}
