package output

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// validConfig 返回一个含 ss + vless(reality) 两个简单节点的完整配置。
func validConfig() map[string]any {
	return map[string]any{
		"mixed-port": 7893,
		"allow-lan":  true,
		"mode":       "rule",
		"log-level":  "info",
		"ipv6":       false,
		"dns": map[string]any{
			"enable":        true,
			"listen":        "0.0.0.0:7874",
			"enhanced-mode": "fake-ip",
			"nameserver":    []any{"223.5.5.5", "119.29.29.29"},
			"fallback":      []any{"tls://8.8.8.8", "tls://1.1.1.1"},
		},
		"proxies": []map[string]any{
			{
				"name":     "ss-test",
				"type":     "ss",
				"server":   "example.com",
				"port":     8388,
				"cipher":   "aes-128-gcm",
				"password": "test-password",
				"udp":      true,
			},
			{
				"name":               "vless-reality",
				"type":               "vless",
				"server":             "example.com",
				"port":               443,
				"uuid":               "1386f85e-657b-4d6e-9d56-78badb1e1c7e",
				"network":            "tcp",
				"tls":                true,
				"udp":                true,
				"flow":               "",
				"servername":         "example.com",
				"client-fingerprint": "chrome",
				"reality-opts": map[string]any{
					// mihomo 用 base64.RawURLEncoding（无填充）解码 public-key
					"public-key": "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE",
					"short-id":   "abcdef",
				},
			},
		},
		"proxy-groups": []map[string]any{
			{"name": "手动选择", "type": "select", "proxies": []any{"DIRECT", "ss-test", "vless-reality"}},
			{"name": "自动选择", "type": "url-test", "url": "https://www.gstatic.com/generate_204", "interval": 300, "proxies": []any{"ss-test", "vless-reality"}},
			{"name": "DIRECT", "type": "direct"},
			{"name": "REJECT", "type": "reject"},
		},
		"rules": []any{"GEOIP,CN,DIRECT", "MATCH,手动选择"},
	}
}

// TestRenderValidateRoundTrip 全链路：Render → Validate 必须通过，
// 且 YAML 可被 yaml.v3 反解析回正确的 proxies 数量。
func TestRenderValidateRoundTrip(t *testing.T) {
	data, err := Render(validConfig())
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Render returned empty output")
	}
	if err := Validate(data); err != nil {
		t.Fatalf("Validate failed: %v\nrendered config:\n%s", err, data)
	}

	// 反解析确认关键内容
	var back map[string]any
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("re-parse rendered yaml failed: %v", err)
	}
	proxies, ok := back["proxies"].([]any)
	if !ok || len(proxies) != 2 {
		t.Fatalf("re-parsed proxies = %T len %d, want 2", back["proxies"], len(proxies))
	}
	if !strings.Contains(string(data), "vless-reality") || !strings.Contains(string(data), "ss-test") {
		t.Errorf("rendered yaml missing node names:\n%s", data)
	}
}

// TestValidateRejectsMalformedNode 断言坏节点（proxies 条目不是 mapping）使校验失败。
// 说明：mihomo UnmarshalRawConfig 对 proxies 做结构解析（[]map[string]any），
// 条目类型错误即拒绝——这是 YAML 语法层能拦的错误；字段级坏节点（缺必填字段/
// 非法参数）在此层放行（见 TestValidateAllowsFieldLevelBadNode），由 api 层的
// adapter.ParseProxy 节点级校验拦截。
func TestValidateRejectsMalformedNode(t *testing.T) {
	cfg := validConfig()
	cfg["proxies"] = []any{"ss://not-a-mapping"} // 类型错误：字符串条目
	data, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if err := Validate(data); err == nil {
		t.Fatal("expected Validate to fail for malformed proxy entry, but it passed")
	}
}

// TestValidateAllowsFieldLevelBadNode 说明性用例（P2-4）：字段层坏节点
// （reality public-key 非 base64、缺 password 的 trojan 等）在 output 层
// （UnmarshalRawConfig）不被拦截——它只做 YAML 结构解析，不执行节点级语义校验。
// 节点级校验由 api 层 adapter.ParseProxy 完成（见 internal/api 的节点过滤）。
func TestValidateAllowsFieldLevelBadNode(t *testing.T) {
	cfg := validConfig()
	cfg["proxies"] = []map[string]any{
		{
			"name": "bad-reality", "type": "vless", "server": "example.com", "port": 443,
			"uuid": "1386f85e-657b-4d6e-9d56-78badb1e1c7e", "network": "tcp", "tls": true,
			"reality-opts": map[string]any{"public-key": "not-base64!!!"},
		},
	}
	data, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if err := Validate(data); err != nil {
		t.Fatalf("Validate should NOT reject field-level bad node (语法层不校验节点语义): %v", err)
	}
}

// TestValidateRejectsMalformedRules 断言 rules 条目类型错误使校验失败。
func TestValidateRejectsMalformedRules(t *testing.T) {
	cfg := validConfig()
	cfg["rules"] = []any{"GEOIP,CN,DIRECT", []any{"nested"}} // 序列条目，非字符串
	data, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if err := Validate(data); err == nil {
		t.Fatal("expected Validate to fail for malformed rules entry, but it passed")
	}
}

// TestValidateRejectsInvalidYAML 断言非法 YAML 使校验失败。
func TestValidateRejectsInvalidYAML(t *testing.T) {
	if err := Validate([]byte("proxies: [unclosed")); err == nil {
		t.Fatal("expected Validate to fail for invalid yaml, but it passed")
	}
}

// TestRenderKeyOrder 断言渲染产物顶层键序（R3）：dns < proxy-groups < proxies < rules，
// 即 proxy-groups 段在 proxies 段之前；rule-providers 注入后位于 rules 之前。
func TestRenderKeyOrder(t *testing.T) {
	keyPos := func(data []byte) map[string]int {
		t.Helper()
		var root yaml.Node
		if err := yaml.Unmarshal(data, &root); err != nil {
			t.Fatalf("unmarshal rendered yaml: %v", err)
		}
		if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
			t.Fatalf("root = kind %d content %d, want document with 1 child", root.Kind, len(root.Content))
		}
		m := root.Content[0]
		if m.Kind != yaml.MappingNode {
			t.Fatalf("root child = kind %d, want mapping", m.Kind)
		}
		pos := make(map[string]int)
		for i := 0; i+1 < len(m.Content); i += 2 {
			pos[m.Content[i].Value] = i / 2
		}
		return pos
	}
	order := []string{"mixed-port", "allow-lan", "mode", "log-level", "ipv6", "dns", "proxy-groups", "proxies", "rules"}
	pos := keyPos(mustRender(t, validConfig()))
	for i := 1; i < len(order); i++ {
		prev, ok1 := pos[order[i-1]]
		cur, ok2 := pos[order[i]]
		if !ok1 || !ok2 {
			t.Fatalf("key %q missing from rendered output, pos=%v", order[i], pos)
		}
		if prev >= cur {
			t.Errorf("key order: %q (pos %d) 应位于 %q (pos %d) 之前", order[i-1], prev, order[i], cur)
		}
	}

	// rule-providers 注入后位于 proxy-groups 与 rules 之间（rules 之前）
	cfg := validConfig()
	cfg["rule-providers"] = map[string]any{
		"cn": map[string]any{"type": "http", "url": "https://example.com/cn.yaml", "interval": 86400},
	}
	pos = keyPos(mustRender(t, cfg))
	if pos["proxy-groups"] >= pos["rule-providers"] || pos["rule-providers"] >= pos["rules"] {
		t.Errorf("rule-providers 应位于 proxy-groups 与 rules 之间: %v", pos)
	}

	// 兜底：topKeyOrder 未覆盖的键按字典序追加在末尾
	cfg = validConfig()
	cfg["zzz-extra"] = "x"
	cfg["aaa-extra"] = "y"
	pos = keyPos(mustRender(t, cfg))
	if pos["aaa-extra"] <= pos["rules"] || pos["zzz-extra"] <= pos["aaa-extra"] {
		t.Errorf("未覆盖键应字典序追加在末尾: %v", pos)
	}
}

func mustRender(t *testing.T, cfg map[string]any) []byte {
	t.Helper()
	data, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	return data
}
