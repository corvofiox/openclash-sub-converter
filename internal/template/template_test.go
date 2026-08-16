package template

import (
	"reflect"
	"testing"

	"github.com/yangyu/openclash-sub-converter/internal/groups"
)

// testGroups 是 Build 测试用的最小策略组列表。
func testGroups() []map[string]any {
	return []map[string]any{
		{"name": "手动选择", "type": "select", "proxies": []any{"DIRECT", "自动选择", "香港节点"}},
		{"name": "自动选择", "type": "url-test", "url": "https://www.gstatic.com/generate_204", "interval": 300},
		{"name": "DIRECT", "type": "direct"},
	}
}

// TestBuildBasic 断言默认结构完整且字段值正确。
func TestBuildBasic(t *testing.T) {
	nodes := []map[string]any{
		{"name": "🇭🇰 香港-01", "type": "ss", "server": "example.com", "port": 8388},
		{"name": "🇯🇵 日本-01", "type": "vless", "server": "example.com", "port": 443},
	}
	cfg, err := Build(nodes, testGroups(), Options{})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	// 顶层字段
	if v := cfg["mixed-port"]; v != 7893 {
		t.Errorf("mixed-port = %v, want 7893", v)
	}
	if v := cfg["allow-lan"]; v != true {
		t.Errorf("allow-lan = %v, want true", v)
	}
	if v := cfg["mode"]; v != "rule" {
		t.Errorf("mode = %v, want rule", v)
	}
	if v := cfg["log-level"]; v != "info" {
		t.Errorf("log-level = %v, want info", v)
	}
	if v := cfg["ipv6"]; v != false {
		t.Errorf("ipv6 = %v, want false", v)
	}

	// DNS 结构
	dns, ok := cfg["dns"].(map[string]any)
	if !ok {
		t.Fatalf("dns is %T, want map[string]any", cfg["dns"])
	}
	if dns["enable"] != true {
		t.Errorf("dns.enable = %v, want true", dns["enable"])
	}
	if dns["listen"] != "0.0.0.0:7874" {
		t.Errorf("dns.listen = %v, want 0.0.0.0:7874", dns["listen"])
	}
	if dns["enhanced-mode"] != "fake-ip" {
		t.Errorf("dns.enhanced-mode = %v, want fake-ip", dns["enhanced-mode"])
	}
	if !reflect.DeepEqual(dns["nameserver"], []any{"223.5.5.5", "119.29.29.29"}) {
		t.Errorf("dns.nameserver = %v", dns["nameserver"])
	}
	if !reflect.DeepEqual(dns["fallback"], []any{"tls://8.8.8.8", "tls://1.1.1.1"}) {
		t.Errorf("dns.fallback = %v", dns["fallback"])
	}

	// proxies / proxy-groups 原样透传（浅拷贝）
	proxies, ok := cfg["proxies"].([]map[string]any)
	if !ok {
		t.Fatalf("proxies is %T, want []map[string]any", cfg["proxies"])
	}
	if len(proxies) != 2 {
		t.Fatalf("len(proxies) = %d, want 2", len(proxies))
	}
	if proxies[0]["name"] != "🇭🇰 香港-01" || proxies[1]["name"] != "🇯🇵 日本-01" {
		t.Errorf("proxy names = %v, %v", proxies[0]["name"], proxies[1]["name"])
	}
	groups, ok := cfg["proxy-groups"].([]map[string]any)
	if !ok || len(groups) != 3 {
		t.Fatalf("proxy-groups = %T len=%d, want []map[string]any len 3", cfg["proxy-groups"], len(groups))
	}

	// rules（R8）：OpenCodeRule 第 1 条 → GEOIP,CN,DIRECT → MATCH 兜底漏网之鱼
	rules, ok := cfg["rules"].([]any)
	if !ok || len(rules) != 3 {
		t.Fatalf("rules = %T %v, want 3 entries", cfg["rules"], cfg["rules"])
	}
	if rules[0] != OpenCodeRule || rules[1] != "GEOIP,CN,DIRECT" || rules[2] != "MATCH,漏网之鱼" {
		t.Errorf("rules = %v, want [%s GEOIP,CN,DIRECT MATCH,漏网之鱼]", rules, OpenCodeRule)
	}

	// 默认选项下不新增任何字段
	if _, ok := proxies[0]["udp"]; ok {
		t.Error("udp should not be set when opts.UDP is false")
	}
	if _, ok := proxies[1]["tls13"]; ok {
		t.Error("tls13 should not be set when opts.TLS13 is false")
	}
}

// TestBuildOptions 断言 UDP/TLS13/SCV 按类型正确应用与覆盖。
func TestBuildOptions(t *testing.T) {
	nodes := []map[string]any{
		{"name": "n-ss", "type": "ss", "server": "example.com", "port": 1},
		{"name": "n-trojan", "type": "trojan", "server": "example.com", "port": 2, "skip-cert-verify": false},
		{"name": "n-http", "type": "http", "server": "example.com", "port": 3},
		{"name": "n-vmess", "type": "vmess", "server": "example.com", "port": 4, "skip-cert-verify": true},
		{"name": "n-vless", "type": "vless", "server": "example.com", "port": 5},
		{"name": "n-hy2", "type": "hysteria2", "server": "example.com", "port": 6},
		{"name": "n-tuic", "type": "tuic", "server": "example.com", "port": 7},
		{"name": "n-anytls", "type": "anytls", "server": "example.com", "port": 8},
		{"name": "n-socks5", "type": "socks5", "server": "example.com", "port": 9},
		{"name": "n-ssr", "type": "ssr", "server": "example.com", "port": 10},
		{"name": "n-hysteria", "type": "hysteria", "server": "example.com", "port": 11},
	}
	cfg, err := Build(nodes, testGroups(), Options{UDP: true, TLS13: true, SCV: true})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	proxies := cfg["proxies"].([]map[string]any)
	byName := make(map[string]map[string]any)
	for _, p := range proxies {
		byName[p["name"].(string)] = p
	}

	// UDP 应用到全部节点
	for _, p := range proxies {
		if p["udp"] != true {
			t.Errorf("%s: udp = %v, want true", p["name"], p["udp"])
		}
	}

	// tls13 仅 ss/trojan/http
	for _, name := range []string{"n-ss", "n-trojan", "n-http"} {
		if byName[name]["tls13"] != true {
			t.Errorf("%s: tls13 = %v, want true", name, byName[name]["tls13"])
		}
	}
	for _, name := range []string{"n-vmess", "n-vless", "n-hy2", "n-tuic", "n-anytls", "n-socks5", "n-ssr", "n-hysteria"} {
		if _, ok := byName[name]["tls13"]; ok {
			t.Errorf("%s: tls13 should be ignored, got %v", name, byName[name]["tls13"])
		}
	}

	// skip-cert-verify 仅 vmess/vless/trojan/hysteria2/tuic/anytls
	for _, name := range []string{"n-vmess", "n-vless", "n-trojan", "n-hy2", "n-tuic", "n-anytls"} {
		if byName[name]["skip-cert-verify"] != true {
			t.Errorf("%s: skip-cert-verify = %v, want true", name, byName[name]["skip-cert-verify"])
		}
	}
	for _, name := range []string{"n-ss", "n-http", "n-socks5", "n-ssr", "n-hysteria"} {
		if _, ok := byName[name]["skip-cert-verify"]; ok {
			t.Errorf("%s: skip-cert-verify should be ignored, got %v", name, byName[name]["skip-cert-verify"])
		}
	}

	// SCV 覆盖已存在的值（trojan false→true 已在上方断言；vmess true 保持 true）
	if byName["n-vmess"]["skip-cert-verify"] != true {
		t.Errorf("n-vmess: existing skip-cert-verify should be overwritten to true")
	}

	// 传入的原始节点 map 不被修改（浅拷贝语义）
	if nodes[1]["skip-cert-verify"] != false {
		t.Error("Build mutated caller's node map")
	}
	if _, ok := nodes[0]["udp"]; ok {
		t.Error("Build mutated caller's node map (udp)")
	}
}

// TestBuildEmptyNodes 断言空节点列表合法且输出空 proxies。
func TestBuildEmptyNodes(t *testing.T) {
	cfg, err := Build(nil, testGroups(), Options{})
	if err != nil {
		t.Fatalf("Build with nil nodes returned error: %v", err)
	}
	proxies, ok := cfg["proxies"].([]map[string]any)
	if !ok {
		t.Fatalf("proxies is %T, want []map[string]any", cfg["proxies"])
	}
	if len(proxies) != 0 {
		t.Errorf("len(proxies) = %d, want 0", len(proxies))
	}

	cfg2, err := Build([]map[string]any{}, testGroups(), Options{UDP: true})
	if err != nil {
		t.Fatalf("Build with empty slice returned error: %v", err)
	}
	if p := cfg2["proxies"].([]map[string]any); len(p) != 0 {
		t.Errorf("len(proxies) = %d, want 0", len(p))
	}
}

// TestBuildEmptyName 断言空 name 节点被拒绝。
func TestBuildEmptyName(t *testing.T) {
	cases := []map[string]any{
		{"name": "", "type": "ss", "server": "example.com", "port": 1},
		{"type": "ss", "server": "example.com", "port": 1}, // 缺 name
	}
	for i, n := range cases {
		if _, err := Build([]map[string]any{n}, testGroups(), Options{}); err == nil {
			t.Errorf("case %d: expected error for node without name", i)
		}
	}
}

// TestBuiltinGFWConstants 钉住内置 GFW 规则集常量（R7 A1 数据源契约）：
// 名称 gfw、Loyalsoldier/clash-rules release 分支 gfw.txt、behavior=domain、format=yaml。
func TestBuiltinGFWConstants(t *testing.T) {
	if BuiltinGFWName != "gfw" {
		t.Errorf("BuiltinGFWName = %q, want %q", BuiltinGFWName, "gfw")
	}
	if BuiltinGFWURL != "https://raw.githubusercontent.com/Loyalsoldier/clash-rules/release/gfw.txt" {
		t.Errorf("BuiltinGFWURL = %q, want release/gfw.txt", BuiltinGFWURL)
	}
	if BuiltinGFWBehavior != "domain" || BuiltinGFWFormat != "yaml" {
		t.Errorf("BuiltinGFWBehavior/Format = %q/%q, want domain/yaml", BuiltinGFWBehavior, BuiltinGFWFormat)
	}
	// 兜底规则行指向「漏网之鱼」组（R7 A2）
	if FinalRule != "MATCH,漏网之鱼" {
		t.Errorf("FinalRule = %q, want MATCH,漏网之鱼", FinalRule)
	}
	// R8：OpenCodeRule 契约——规则行第 1 条 + 跨包一致性（组名与 groups.GroupOpenCode
	// 同步，防漂移；OpenCode 规则须指向存在的组，否则 mihomo 拒绝加载）
	if OpenCodeRule != "DOMAIN-SUFFIX,opencode.ai,OpenCode" {
		t.Errorf("OpenCodeRule = %q, want DOMAIN-SUFFIX,opencode.ai,OpenCode", OpenCodeRule)
	}
	if OpenCodeRule != "DOMAIN-SUFFIX,opencode.ai,"+groups.GroupOpenCode {
		t.Errorf("OpenCodeRule = %q 与 groups.GroupOpenCode = %q 不一致", OpenCodeRule, groups.GroupOpenCode)
	}
}
