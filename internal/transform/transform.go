// Package transform 提供节点列表的过滤与重命名。
package transform

import (
	"fmt"
	"regexp"
	"strings"
)

// Filter 描述对节点列表的过滤/重命名规则。
type Filter struct {
	Rename  string // 格式 "<regex>/<replacement>", 用 "/" 分隔; 空串不重命名
	Include string // 节点名正则, 命中才保留; 空串不过滤
	Exclude string // 节点名正则, 命中剔除; 空串不过滤
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
			return nil, fmt.Errorf("invalid exclude regex %q: %w", f.Exclude, err)
		}
	}
	if f.Include != "" {
		includeRe, err = regexp.Compile(f.Include)
		if err != nil {
			return nil, fmt.Errorf("invalid include regex %q: %w", f.Include, err)
		}
	}

	// Rename 格式 "<regex>/<replacement>"，按第一个 "/" 分隔。
	var renameRe *regexp.Regexp
	renameRepl := ""
	if f.Rename != "" {
		idx := strings.Index(f.Rename, "/")
		if idx < 0 {
			return nil, fmt.Errorf("invalid rename rule %q: expected format <regex>/<replacement>", f.Rename)
		}
		renameRe, err = regexp.Compile(f.Rename[:idx])
		if err != nil {
			return nil, fmt.Errorf("invalid rename regex %q: %w", f.Rename[:idx], err)
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
