// Package template 负责将节点列表与策略组组装为完整 Clash 配置。
//
// 内置默认模板结构（docs/design.md 第 6 节）：mixed-port/allow-lan/mode/
// log-level/ipv6/dns/proxy-groups/proxies/rules。
package template

import (
	"fmt"
)

// Options 是应用到每个节点的输出选项。
type Options struct {
	UDP   bool // 节点输出 udp: true
	TLS13 bool // 节点输出 tls13: true（仅 ss/trojan/http 系适用，其他类型忽略）
	SCV   bool // 节点输出 skip-cert-verify: true（vmess/vless/trojan/hysteria2/tuic/anytls 适用，已有该字段的覆盖）
}

// 默认模板常量（与 docs/design.md 第 6 节一致）。
const (
	MixedPort = 7893
	AllowLan  = true
	Mode      = "rule"
	LogLevel  = "info"
	IPv6      = false
	DNSListen = "0.0.0.0:7874"
	// FinalRule 是规则列表的兜底 MATCH 行（R7：指向「漏网之鱼」组，不再直指手动选择）。
	FinalRule = "MATCH,漏网之鱼"

	// R7：内置 GFW 规则集（固定名 gfw，默认启用；经 gfw=false 关闭）。
	// 来源 Loyalsoldier/clash-rules release 分支 gfw.txt，mihomo rule-provider
	// 每日自动轮询（interval 86400）随在线仓库同步更新，无需自建更新逻辑。
	BuiltinGFWName     = "gfw"
	BuiltinGFWURL      = "https://raw.githubusercontent.com/Loyalsoldier/clash-rules/release/gfw.txt"
	BuiltinGFWBehavior = "domain"
	BuiltinGFWFormat   = "yaml"
)

// tls13Types 是 tls13 字段适用的代理类型。
var tls13Types = map[string]bool{"ss": true, "trojan": true, "http": true}

// scvTypes 是 skip-cert-verify 字段适用的代理类型。
var scvTypes = map[string]bool{
	"vmess": true, "vless": true, "trojan": true,
	"hysteria2": true, "tuic": true, "anytls": true,
}

// Build 组装完整 Clash 配置 map。
//
//   - 返回结构：mixed-port、allow-lan、mode、log-level、ipv6、dns、proxy-groups、
//     proxy-groups、rules（"GEOIP,CN,DIRECT" 与 "MATCH,漏网之鱼"）。
//   - opts 应用到每个节点：UDP→udp:true；TLS13→仅 ss/trojan/http 输出
//     tls13:true；SCV→vmess/vless/trojan/hysteria2/tuic/anytls 输出
//     skip-cert-verify:true（已存在的值被覆盖）。
//   - 节点 name 必须非空，否则报错；空节点列表合法（输出空 proxies）。
//   - 不改动传入的节点 map（浅拷贝后应用选项）。
func Build(nodes []map[string]any, groups []map[string]any, opts Options) (map[string]any, error) {
	proxies := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		name, _ := n["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("proxy node missing non-empty name")
		}
		clone := make(map[string]any, len(n)+3)
		for k, v := range n {
			clone[k] = v
		}
		applyOptions(clone, opts)
		proxies = append(proxies, clone)
	}

	cfg := map[string]any{
		"mixed-port":   MixedPort,
		"allow-lan":    AllowLan,
		"mode":         Mode,
		"log-level":    LogLevel,
		"ipv6":         IPv6,
		"dns":          defaultDNS(),
		"proxies":      proxies,
		"proxy-groups": groups,
		"rules":        []any{"GEOIP,CN,DIRECT", FinalRule},
	}
	return cfg, nil
}

// defaultDNS 返回内置默认 DNS 配置。
func defaultDNS() map[string]any {
	return map[string]any{
		"enable":        true,
		"listen":        DNSListen,
		"enhanced-mode": "fake-ip",
		"nameserver":    []any{"223.5.5.5", "119.29.29.29"},
		"fallback":      []any{"tls://8.8.8.8", "tls://1.1.1.1"},
	}
}

// applyOptions 按 Options 修改单个节点条目。
func applyOptions(n map[string]any, opts Options) {
	if opts.UDP {
		n["udp"] = true
	}
	typ, _ := n["type"].(string)
	if opts.TLS13 && tls13Types[typ] {
		n["tls13"] = true
	}
	if opts.SCV && scvTypes[typ] {
		n["skip-cert-verify"] = true
	}
}
