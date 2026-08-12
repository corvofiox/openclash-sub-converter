// Package output 负责 Clash 配置的 YAML 渲染与 mihomo YAML 语法层校验。
package output

import (
	"bytes"
	"fmt"
	"sort"

	mihomoconfig "github.com/metacubex/mihomo/config"
	"gopkg.in/yaml.v3"
)

// topKeyOrder 定义输出 YAML 顶层键的固定顺序。mihomo 对段落顺序无要求，
// 但固定顺序保证产物确定性（便于 diff 与契约测试）。rule-providers 由
// template.ApplyRuleProviders 在 Render 前注入 cfg，仅规则模板启用时存在。
var topKeyOrder = []string{
	"mixed-port", "allow-lan", "mode", "log-level", "ipv6",
	"dns", "proxy-groups", "proxies", "rule-providers", "rules",
}

// Render 将配置 map 序列化为 YAML 字节流（yaml.v3，SetIndent(2)）。
// 顶层键按 topKeyOrder 顺序输出（proxy-groups 在 proxies 前）；topKeyOrder
// 未覆盖的键（防御未来新增）按字典序追加到末尾，保证确定性输出。
// 嵌套值经 yaml.Node.Encode 递归编码，与直接序列化 map 的输出逐字节一致。
func Render(cfg map[string]any) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	emit := func(k string, v any) error {
		kn := &yaml.Node{Kind: yaml.ScalarNode, Value: k}
		var vn yaml.Node
		if err := vn.Encode(v); err != nil {
			return fmt.Errorf("render yaml: encode %q: %w", k, err)
		}
		root.Content = append(root.Content, kn, &vn)
		return nil
	}
	seen := make(map[string]bool, len(cfg))
	for _, k := range topKeyOrder {
		v, ok := cfg[k]
		if !ok {
			continue
		}
		seen[k] = true
		if err := emit(k, v); err != nil {
			return nil, err
		}
	}
	// 兜底：topKeyOrder 未覆盖的键按字典序追加。
	// 注意：兜底键经 yaml.Node 裸标量发射（Tag 空、Style 0），不经过 map 编码路径 stringv
	// 的 resolve 复查——若未来出现非 plain 安全键（数字/布尔/空串开头等，如 "123"/"true"），
	// 裸标量会与 map 路径的引号风格产生分歧，严格 YAML 解析器可能将其解析为 int/bool 键。
	// 当前不可达（template.Build 只产出 topKeyOrder 覆盖的键 + rule-providers）；若未来引入
	// 此类兜底键，须显式设置 Tag:"!!str" + Style: yaml.DoubleQuotedStyle（或不在本函数引入）。
	var rest []string
	for k := range cfg {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		if err := emit(k, cfg[k]); err != nil {
			return nil, err
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("render yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close yaml encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// Validate 用 mihomo config.UnmarshalRawConfig 对完整配置做 YAML 语法层校验
// （age 解密 + YAML 反序列化到 RawConfig，不执行 parseProxies/ParseProxy 节点级
// 语义校验），返回其错误（原样）。字段级坏节点（缺必填字段/非法参数）在此层
// 不会被拦截——节点级语义校验由 api 层 adapter.ParseProxy 完成（见 internal/api
// 的节点过滤），本函数只兜底 YAML 结构错误。
func Validate(data []byte) error {
	_, err := mihomoconfig.UnmarshalRawConfig(data)
	if err != nil {
		return fmt.Errorf("mihomo config validation failed: %w", err)
	}
	return nil
}
