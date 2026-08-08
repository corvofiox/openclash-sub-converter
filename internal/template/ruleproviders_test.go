package template

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/yangyu/openclash-sub-converter/internal/output"
)

// sampleRPs 返回一组合法的注入参数（domain + ipcidr 两种 behavior）。
func sampleRPs() []RuleProvider {
	return []RuleProvider{
		{Name: "cn-domains", URL: "https://example.com/cn.yaml", Behavior: "domain", Format: "yaml"},
		{Name: "cn-ips", URL: "https://example.com/ips.txt", Behavior: "ipcidr", Format: "text"},
	}
}

// baseRulesCfg 返回含 MATCH 行的最小配置。
func baseRulesCfg() map[string]any {
	return map[string]any{
		"rules": []any{"GEOIP,CN,DIRECT", "MATCH,🚀 手动选择"},
	}
}

// TestApplyEmptyNoop 空 rps → no-op，cfg 深比较原样。
func TestApplyEmptyNoop(t *testing.T) {
	cfg := baseRulesCfg()
	before := map[string]any{
		"rules": append([]any{}, cfg["rules"].([]any)...),
	}
	if err := ApplyRuleProviders(cfg, nil, ""); err != nil {
		t.Fatalf("空 rps: %v", err)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Errorf("空 rps 不应改动 cfg: got %+v, want %+v", cfg, before)
	}
	if err := ApplyRuleProviders(cfg, []RuleProvider{}, "🚀 手动选择"); err != nil {
		t.Fatalf("空切片 rps: %v", err)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Errorf("空切片 rps 不应改动 cfg: got %+v, want %+v", cfg, before)
	}
}

// TestInjectStructure 注入后结构正确：rule-providers 段、RULE-SET 在 MATCH 前、
// 顺序保持。
func TestInjectStructure(t *testing.T) {
	cfg := baseRulesCfg()
	if err := ApplyRuleProviders(cfg, sampleRPs(), "🚀 手动选择"); err != nil {
		t.Fatalf("ApplyRuleProviders: %v", err)
	}

	// rule-providers 段
	rps, ok := cfg["rule-providers"].(map[string]any)
	if !ok {
		t.Fatalf("rule-providers = %T, want map[string]any", cfg["rule-providers"])
	}
	if len(rps) != 2 {
		t.Fatalf("rule-providers 条数 = %d, want 2", len(rps))
	}
	entry, ok := rps["cn-domains"].(map[string]any)
	if !ok {
		t.Fatalf("entry = %T, want map[string]any", rps["cn-domains"])
	}
	wantEntry := map[string]any{
		"type":     "http",
		"url":      "https://example.com/cn.yaml",
		"behavior": "domain",
		"format":   "yaml",
		"interval": 86400,
		"path":     "./ruleset/cn-domains.yaml",
	}
	if !reflect.DeepEqual(entry, wantEntry) {
		t.Errorf("cn-domains entry = %+v, want %+v", entry, wantEntry)
	}
	ipEntry, _ := rps["cn-ips"].(map[string]any)
	if ipEntry["behavior"] != "ipcidr" || ipEntry["path"] != "./ruleset/cn-ips.yaml" {
		t.Errorf("cn-ips entry = %+v", ipEntry)
	}

	// rules：RULE-SET 按序插在 MATCH 之前
	rules, ok := cfg["rules"].([]any)
	if !ok {
		t.Fatalf("rules = %T, want []any", cfg["rules"])
	}
	wantRules := []any{
		"GEOIP,CN,DIRECT",
		"RULE-SET,cn-domains,🚀 手动选择",
		"RULE-SET,cn-ips,🚀 手动选择",
		"MATCH,🚀 手动选择",
	}
	if !reflect.DeepEqual(rules, wantRules) {
		t.Errorf("rules = %v, want %v", rules, wantRules)
	}
}

// TestNoMatchAppendsToEnd 无 MATCH 行 → RULE-SET 追加到末尾。
func TestNoMatchAppendsToEnd(t *testing.T) {
	cfg := map[string]any{"rules": []any{"DOMAIN-SUFFIX,example.com,DIRECT"}}
	if err := ApplyRuleProviders(cfg, sampleRPs(), "DIRECT"); err != nil {
		t.Fatalf("ApplyRuleProviders: %v", err)
	}
	rules := cfg["rules"].([]any)
	want := []any{
		"DOMAIN-SUFFIX,example.com,DIRECT",
		"RULE-SET,cn-domains,DIRECT",
		"RULE-SET,cn-ips,DIRECT",
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("rules = %v, want %v", rules, want)
	}
}

// TestDefaultTargetGroup targetGroup 空串 → 默认「🚀 手动选择」。
func TestDefaultTargetGroup(t *testing.T) {
	cfg := baseRulesCfg()
	if err := ApplyRuleProviders(cfg, sampleRPs(), ""); err != nil {
		t.Fatalf("ApplyRuleProviders: %v", err)
	}
	rules := cfg["rules"].([]any)
	for _, r := range rules {
		line, _ := r.(string)
		if strings.HasPrefix(line, "RULE-SET,") && !strings.HasSuffix(line, ",🚀 手动选择") {
			t.Errorf("RULE-SET 行目标组错误: %q", line)
		}
	}
}

// TestInvalidNameRejected Name 含路径穿越字符 → error「invalid rule provider name」。
func TestInvalidNameRejected(t *testing.T) {
	badNames := []string{"../evil", "a/b", `a\b`, "a..b", "..", "x/../y"}
	for _, name := range badNames {
		cfg := baseRulesCfg()
		rps := []RuleProvider{{Name: name, URL: "https://e.com/x.yaml", Behavior: "domain", Format: "yaml"}}
		err := ApplyRuleProviders(cfg, rps, "")
		if err == nil {
			t.Errorf("Name %q: want error, got nil", name)
			continue
		}
		if !strings.Contains(err.Error(), "invalid rule provider name") {
			t.Errorf("Name %q: err = %q, want 含 invalid rule provider name", name, err)
		}
		// 报错后 cfg 不应被污染
		if _, ok := cfg["rule-providers"]; ok {
			t.Errorf("Name %q 报错后 cfg 仍被注入 rule-providers", name)
		}
	}
}

// TestValidateRuleProviderName（P2-5）导出校验函数：空名/路径穿越拒绝，
// 合法名通过（API/store 层在创建/更新模板前调用）。
func TestValidateRuleProviderName(t *testing.T) {
	for _, bad := range []string{"", "../evil", "a/b", `a\b`, "a..b", "..", "x/../y"} {
		if err := ValidateRuleProviderName(bad); err == nil {
			t.Errorf("ValidateRuleProviderName(%q) = nil, want error", bad)
		}
	}
	for _, good := range []string{"广告拦截", "cn-domains", "a.b", "abc"} {
		if err := ValidateRuleProviderName(good); err != nil {
			t.Errorf("ValidateRuleProviderName(%q) = %v, want nil", good, err)
		}
	}
}

// TestValidationErrors Name/URL 空与 behavior/format 非法 → error。
func TestValidationErrors(t *testing.T) {
	cases := []struct {
		rp      RuleProvider
		wantSub string
	}{
		{RuleProvider{Name: "", URL: "https://e.com", Behavior: "domain", Format: "yaml"}, "name"},
		{RuleProvider{Name: "x", URL: "", Behavior: "domain", Format: "yaml"}, "url"},
		{RuleProvider{Name: "x", URL: "https://e.com", Behavior: "regexp", Format: "yaml"}, "behavior"},
		{RuleProvider{Name: "x", URL: "https://e.com", Behavior: "domain", Format: "mrs"}, "format"},
		{RuleProvider{Name: "x", URL: "https://e.com", Behavior: "classical", Format: ""}, "format"},
	}
	for _, tc := range cases {
		err := ApplyRuleProviders(baseRulesCfg(), []RuleProvider{tc.rp}, "")
		if err == nil {
			t.Errorf("%+v: want error, got nil", tc.rp)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%+v: err = %q, want 含 %q", tc.rp, err, tc.wantSub)
		}
	}
}

// TestRenderValidateRoundTrip 产物完整性：注入后 cfg 渲染为 YAML，经 mihomo
// config.UnmarshalRawConfig 校验通过（参考 internal/output 的渲染+校验写法）。
func TestRenderValidateRoundTrip(t *testing.T) {
	nodes := []map[string]any{
		{
			"name": "ss-test", "type": "ss", "server": "example.com", "port": 8388,
			"cipher": "aes-128-gcm", "password": "test-password",
		},
	}
	groups := []map[string]any{
		{"name": "🚀 手动选择", "type": "select", "proxies": []any{"ss-test"}},
		{"name": "DIRECT", "type": "direct"},
	}
	cfg, err := Build(nodes, groups, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rps := []RuleProvider{
		{Name: "cn-domains", URL: "https://example.com/cn.yaml", Behavior: "domain", Format: "yaml"},
		{Name: "cn-ips", URL: "https://example.com/ips.txt", Behavior: "ipcidr", Format: "text"},
		{Name: "global", URL: "https://example.com/g.txt", Behavior: "classical", Format: "text"},
	}
	if err := ApplyRuleProviders(cfg, rps, ""); err != nil {
		t.Fatalf("ApplyRuleProviders: %v", err)
	}
	data, err := output.Render(cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := output.Validate(data); err != nil {
		t.Fatalf("mihomo UnmarshalRawConfig 校验失败: %v\n渲染结果:\n%s", err, data)
	}
	// 渲染产物包含 rule-providers 与 RULE-SET。
	// 注意：yaml.v3 会把非 ASCII 字符（🚀）转义为 \U0001F680，因此这里只断言
	// RULE-SET 行的 ASCII 前缀与 rule-providers 段结构。
	s := string(data)
	for _, want := range []string{"rule-providers:", "RULE-SET,cn-domains,", "./ruleset/cn-domains.yaml", "interval: 86400"} {
		if !strings.Contains(s, want) {
			t.Errorf("渲染产物缺少 %q:\n%s", want, s)
		}
	}
	// 反解析确认 RULE-SET 行完整（含默认目标组，转义形式 \U0001F680）
	var back map[string]any
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("re-parse rendered yaml: %v", err)
	}
	// Build 自带 2 条规则（GEOIP + MATCH），注入 3 条 RULE-SET 后共 5 条，
	// RULE-SET 完整行位于 GEOIP 之后、MATCH 之前。
	rules, ok := back["rules"].([]any)
	if !ok || len(rules) != 5 {
		t.Fatalf("re-parsed rules = %T len %d, want []any len 5", back["rules"], len(rules))
	}
	if rules[1].(string) != "RULE-SET,cn-domains,🚀 手动选择" {
		t.Errorf("rules[1] = %q, want RULE-SET 完整行", rules[1])
	}
	if rules[4].(string) != "MATCH,🚀 手动选择" {
		t.Errorf("rules[4] = %q, want MATCH 在最后", rules[4])
	}
}
