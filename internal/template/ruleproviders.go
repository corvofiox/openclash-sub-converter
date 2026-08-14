package template

import (
	"errors"
	"fmt"
	"strings"
)

// RuleProvider 描述一个待注入的 mihomo rule-provider。
// Behavior 取值：domain | ipcidr | classical；Format 取值：yaml | text。
// TargetGroup 是 RULE-SET 行的目标策略组名；空串 = 默认「手动选择」
// （R3：规则集专属组场景每个 rp 显式指定自己的专属组）。
type RuleProvider struct {
	Name        string
	URL         string
	Behavior    string
	Format      string
	TargetGroup string
}

// ruleProviderInterval 是 rule-provider 的刷新间隔（秒，86400 = 每日）。
const ruleProviderInterval = 86400

// defaultTargetGroup 是未指定 targetGroup 时的默认策略组名。
const defaultTargetGroup = "手动选择"

// ValidateRuleProviderName 校验 rule-provider 名称：非空，且拒绝含 /、\、
// 或 .. 的路径穿越名称（名称会拼进输出 YAML 的 rule-provider path，
// 如 ./ruleset/<Name>.yaml）。
//
// 导出让 API/store 层在创建/更新规则集前校验（400），避免路径穿越
// 拖到转换管线里才报错（500）。
func ValidateRuleProviderName(name string) error {
	if name == "" {
		return errors.New("rule provider name 不能为空")
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return errors.New("invalid rule provider name")
	}
	// P2-1：逗号会把 RULE-SET 规则行拆成 4 段（mihomo 语义层拒绝），换行/回车/
	// 控制字符会破坏 YAML 行结构（规则行换行）——两者都会让 output.Validate 语法层
	// 放行而 mihomo 加载失败，必须在创建/更新规则集时拦截（400）。
	if strings.ContainsAny(name, ",\n\r") || hasControlChar(name) {
		return errors.New("rule provider name 不能包含逗号/换行等特殊字符")
	}
	return nil
}

// hasControlChar 检测 ASCII 控制字符（码点 < 0x20：\t \n \r 等）。
func hasControlChar(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
			return true
		}
	}
	return false
}

// ApplyRuleProviders 将 rule-providers 段注入 Build 返回的完整配置 map，并在
// cfg["rules"]（[]any，元素为 string）的最前面按 rps 顺序插入
// RULE-SET,<rp.Name>,<rp.TargetGroup>（位于 GEOIP,CN,DIRECT 之前，保证规则集
// 流量优先匹配，不被国内直连规则先行截断）。
//
//   - rps 为空 → 直接返回 nil（no-op，cfg 原样）。
//   - 每个 rp 的 TargetGroup 为空串时用默认值「手动选择」。
//   - cfg["rule-providers"] 是整体覆盖：多规则集场景必须一次调用传全部 rp，
//     严禁逐次调用（后一次会覆盖前一次注入的段）。
//   - 每个 rp 生成一个 http 型 rule-provider entry：
//     {"type":"http","url":...,"behavior":...,"format":...,"interval":86400,
//     "path":"./ruleset/<Name>.yaml"}。
//   - Name 非法（空/含 /、\ 或 ..）→ 返回 error（见 ValidateRuleProviderName）。
//
// 返回的 error 由调用方映射为 HTTP 500。
func ApplyRuleProviders(cfg map[string]any, rps []RuleProvider) error {
	if len(rps) == 0 {
		return nil
	}

	providers := make(map[string]any, len(rps))
	ruleSets := make([]any, 0, len(rps))
	for _, rp := range rps {
		if err := validateRuleProvider(rp); err != nil {
			return err
		}
		// P1-2 防御层：同名 rule-provider 会互相覆盖 map 键（URL 静默丢失），
		// 防止未来其他调用路径重蹈覆辙（调用方应在上游拦截并返回 400）。
		if _, dup := providers[rp.Name]; dup {
			return fmt.Errorf("rule provider 名称重复: %s", rp.Name)
		}
		target := rp.TargetGroup
		if target == "" {
			target = defaultTargetGroup
		}
		providers[rp.Name] = map[string]any{
			"type":     "http",
			"url":      rp.URL,
			"behavior": rp.Behavior,
			"format":   rp.Format,
			"interval": ruleProviderInterval,
			"path":     "./ruleset/" + rp.Name + ".yaml",
		}
		ruleSets = append(ruleSets, "RULE-SET,"+rp.Name+","+target)
	}
	cfg["rule-providers"] = providers

	rulesAny, ok := cfg["rules"]
	if !ok {
		return fmt.Errorf("cfg 缺少 rules 字段")
	}
	rules, ok := rulesAny.([]any)
	if !ok {
		return fmt.Errorf("cfg[\"rules\"] 类型必须为 []any，实际为 %T", rulesAny)
	}

	// RULE-SET 统一插到规则列表最前面（在 GEOIP,CN,DIRECT 之前），
	// 多条保持 rps 顺序；无 MATCH 行同样插最前。
	out := make([]any, 0, len(rules)+len(ruleSets))
	out = append(out, ruleSets...)
	out = append(out, rules...)
	cfg["rules"] = out
	return nil
}

// validateRuleProvider 校验单个 rule-provider 注入参数：
// Name 走 ValidateRuleProviderName；URL 非空；behavior/format 合法。
func validateRuleProvider(rp RuleProvider) error {
	if err := ValidateRuleProviderName(rp.Name); err != nil {
		return err
	}
	if rp.URL == "" {
		return errors.New("rule provider url 不能为空")
	}
	switch rp.Behavior {
	case "domain", "ipcidr", "classical":
	default:
		return errors.New("behavior 必须为 domain/ipcidr/classical")
	}
	switch rp.Format {
	case "yaml", "text":
	default:
		return errors.New("format 必须为 yaml/text")
	}
	return nil
}
