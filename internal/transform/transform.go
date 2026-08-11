// Package transform 提供节点列表的过滤与重命名。
package transform

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidRegex 标识过滤/重命名参数中的客户端错误：include/exclude 正则
// 编译失败、rename 规则格式非法或 rename 正则编译失败。API 层用
// errors.Is(err, ErrInvalidRegex) 将这类错误映射为 HTTP 400（参数错误），
// 其余管线错误保持 500。
var ErrInvalidRegex = errors.New("invalid regex")

// Filter 描述对节点列表的过滤/重命名规则。
type Filter struct {
	Rename  string // 格式 "<regex>/<replacement>", 用 "/" 分隔; 空串不重命名
	Include string // 节点名正则, 命中才保留; 空串不过滤
	Exclude string // 节点名正则, 命中剔除; 空串不过滤
	// StripEmoji 为 true 时在输出阶段（groups.Build 之后）剥离节点名中的
	// emoji 字符。不在 Apply 内执行：地区识别（groups.regionOf）必须基于
	// 原始节点名，剥离统一放在 groups.Build 之后（见 ApplyStripEmoji）。
	StripEmoji bool
}

// Apply 依次执行 Exclude → Include → Rename，返回处理后的节点列表。
//
//   - 先 Exclude 后 Include；重命名用 regexp.ReplaceAllString（替换所有匹配），
//     重命名后的名字写回 map["name"]。
//   - 输出节点名统一去重（含无 rename 分支）：首个保留原名，后续同名追加
//     " (2)"/" (3)" 序号后缀（mihomo 对重名节点硬性报错，多源聚合必防）。
//   - 空节点列表合法，返回空切片不报错。
//   - 任一正则编译失败返回带清晰消息的错误。
func Apply(nodes []map[string]any, f Filter) ([]map[string]any, error) {
	var excludeRe, includeRe *regexp.Regexp
	var err error
	if f.Exclude != "" {
		excludeRe, err = regexp.Compile(f.Exclude)
		if err != nil {
			return nil, fmt.Errorf("invalid exclude regex %q: %w: %w", f.Exclude, err, ErrInvalidRegex)
		}
	}
	if f.Include != "" {
		includeRe, err = regexp.Compile(f.Include)
		if err != nil {
			return nil, fmt.Errorf("invalid include regex %q: %w: %w", f.Include, err, ErrInvalidRegex)
		}
	}

	// Rename 格式 "<regex>/<replacement>"，按第一个 "/" 分隔。
	var renameRe *regexp.Regexp
	renameRepl := ""
	if f.Rename != "" {
		idx := strings.Index(f.Rename, "/")
		if idx < 0 {
			return nil, fmt.Errorf("invalid rename rule %q: expected format <regex>/<replacement>: %w", f.Rename, ErrInvalidRegex)
		}
		renameRe, err = regexp.Compile(f.Rename[:idx])
		if err != nil {
			return nil, fmt.Errorf("invalid rename regex %q: %w: %w", f.Rename[:idx], err, ErrInvalidRegex)
		}
		renameRepl = f.Rename[idx+1:]
	}

	out := make([]map[string]any, 0, len(nodes))
	claimed := make(map[string]bool) // 已占用输出名
	counters := make(map[string]int) // 每个基础名的重名计数器
	for _, n := range nodes {
		name, ok := n["name"].(string)
		if !ok || name == "" {
			continue
		}
		if excludeRe != nil && excludeRe.MatchString(name) {
			continue
		}
		if includeRe != nil && !includeRe.MatchString(name) {
			continue
		}
		finalName := name
		if renameRe != nil {
			finalName = renameRe.ReplaceAllString(name, renameRepl)
		}
		// 统一去重：无论是否 rename，同名节点都走 uniqueName（首个保留原名，
		// 后续加 " (N)" 后缀）；rename 未命中时 uniqueName 返回原名，行为不变。
		finalName = uniqueName(finalName, claimed, counters)
		if finalName != name {
			n["name"] = finalName // 重命名/去重后的名字写回 map["name"]
		}
		out = append(out, n)
	}
	return out, nil
}

// uniqueName 为重名分配 " (N)" 序号后缀，并登记占用。
func uniqueName(name string, claimed map[string]bool, counters map[string]int) string {
	c := counters[name] + 1
	counters[name] = c
	candidate := name
	if c > 1 {
		candidate = fmt.Sprintf("%s (%d)", name, c)
	}
	for claimed[candidate] {
		c++
		counters[name] = c
		candidate = fmt.Sprintf("%s (%d)", name, c)
	}
	claimed[candidate] = true
	return candidate
}

// emojiRe 匹配节点名中的 emoji 字符：杂项符号与图形符号 U+2600–27BF（☀♻⚡等）、
// 补充箭头/符号 U+2B00–2BFF（⭐⬆等）、表情符号/旗标/补充图形 U+1F000–1FAFF
// （含区域指示符旗标 🇭🇰、表情、麻将牌等）、变体选择符 VS16 U+FE0F、
// ZWJ U+200D、键帽 U+20E3。U+2190-21FF 箭头与 U+25A0-25FF 几何图形属文本符号，
// 不在范围内（保留）。Go regexp 支持 \x{hhhh} 写法（raw string 中反斜杠原样保留）。
var emojiRe = regexp.MustCompile(`[\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{1F000}-\x{1FAFF}\x{FE0F}\x{200D}\x{20E3}]`)

// StripEmoji 剥离 name 中的 emoji 字符；无 emoji 时返回原串（字节级不变）。
// 有 emoji 时：剥离 → strings.Fields+Join(" ") 折叠连续空白并去首尾；
// 结果为空（纯 emoji 名）→ 返回原名（防空名，mihomo 拒绝空 name）。
func StripEmoji(name string) string {
	if !emojiRe.MatchString(name) {
		return name
	}
	stripped := emojiRe.ReplaceAllString(name, "")
	if joined := strings.Join(strings.Fields(stripped), " "); joined != "" {
		return joined
	}
	return name
}

// ApplyStripEmoji 在 groups.Build 之后调用（strip=false 时 no-op）：
//
//  1. 对每个节点：newName = StripEmoji(name)；对**全部节点**（含未剥离者）按序
//     跑 uniqueName(newName, claimed, counters) 统一去重（复用小写函数
//     uniqueName）；finalName != name 时写回 n["name"]，并记
//     renameMap[oldName] = finalName。
//  2. 改写 groups：每个组 proxies（[]any of string）条目命中 renameMap 则替换
//     （手动组引用的是组名常量/地区组名，非节点名，天然不命中）。
//
// 顺序保证：识别（groups.Build/regionOf）用原始名 → 剥离在输出阶段 → 去重在剥离后。
// 返回 renameMap 便于测试断言。
func ApplyStripEmoji(nodes []map[string]any, groups []map[string]any, strip bool) map[string]string {
	if !strip {
		return nil
	}
	renameMap := make(map[string]string)
	claimed := make(map[string]bool) // 已占用输出名
	counters := make(map[string]int) // 每个基础名的重名计数器
	for _, n := range nodes {
		name, ok := n["name"].(string)
		if !ok || name == "" {
			continue
		}
		finalName := uniqueName(StripEmoji(name), claimed, counters)
		if finalName != name {
			n["name"] = finalName
			renameMap[name] = finalName
		}
	}
	if len(renameMap) == 0 {
		return renameMap
	}
	for _, g := range groups {
		raw, ok := g["proxies"].([]any)
		if !ok {
			continue
		}
		for i, p := range raw {
			s, ok := p.(string)
			if !ok {
				continue
			}
			if mapped, hit := renameMap[s]; hit {
				raw[i] = mapped
			}
		}
	}
	return renameMap
}
