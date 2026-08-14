// Package groups 根据节点列表构建 Clash 策略组（按 emoji/中文/拼音/英文/ISO 代码
// 识别节点地区并分入同名地区组）。
package groups

import (
	"strings"
)

// 策略组常量。
const (
	TestURL      = "https://www.gstatic.com/generate_204"
	TestInterval = 300

	GroupManual = "手动选择"
	GroupAuto   = "自动选择"
	GroupOther  = "其他节点"

	// R1：直连/拒绝策略组（type=select，proxies 分别为 [DIRECT]/[REJECT]，
	// 供手动选择组与规则集专属组作为快捷切换引用）。
	GroupDirect = "直连"
	GroupReject = "拒绝"
)

// regionGroup 是一个地区策略组的中间表示。
type regionGroup struct {
	region string
	nodes  []string
}

// Build 根据节点列表构建策略组列表（组声明顺序）：
//
//  1. "手动选择" type=select（proxies 依赖其余组名，最后填充）；
//  2. "自动选择" type=url-test，proxies=[全部节点名]（去重）；
//  3. 地区组：按节点名识别地区（emoji/中文/拼音/英文/ISO 代码五层线索，取名字中
//     第一个地区线索），组名 "<地区>节点" 格式，type=url-test，
//     url/interval 固定；节点数 0 的地区不生成组；
//  4. 无任何地区线索或无法识别的节点归入 "其他节点" 组（无此类节点则不生成）；
//  5. "直连"/"拒绝"：type=select，proxies 分别为 [DIRECT]/[REJECT]
//     （R1；DIRECT/REJECT 是 Clash 内置出站，组非空故合法）。
//
// 手动选择组 proxies 顺序（R2）：
// [自动选择, <地区组名...按出现序>, 其他节点组(若存在), <全部去重节点名...>,
// 直连组, 拒绝组]——不再直接引用裸 DIRECT/REJECT，由「直连」/「拒绝」组承载。
//
// 节点名重复时去重（同一节点只进一个组）。空节点列表仍输出手动选择/自动选择/
// 直连/拒绝（用户明确要求无节点时也生成直连/拒绝组），不报错。
func Build(nodes []map[string]any) ([]map[string]any, error) {
	seenName := make(map[string]bool)
	var allNames []string

	var regions []*regionGroup
	regionByName := make(map[string]*regionGroup)
	var otherNodes []string

	for _, n := range nodes {
		name, ok := n["name"].(string)
		if !ok || name == "" || seenName[name] {
			continue
		}
		seenName[name] = true
		allNames = append(allNames, name)

		_, region, ok := regionOf(name)
		if !ok {
			otherNodes = append(otherNodes, name)
			continue
		}
		rg, exists := regionByName[region]
		if !exists {
			rg = &regionGroup{region: region}
			regionByName[region] = rg
			regions = append(regions, rg)
		}
		rg.nodes = append(rg.nodes, name)
	}

	// 手动选择组 proxies（R2 顺序）：自动选择 → 地区组名（按出现序）→
	// 其他节点组（若存在）→ 全部去重节点名 → 直连组 → 拒绝组。
	manualProxies := []any{GroupAuto}
	groups := make([]map[string]any, 0, 5+len(regions)) // 手动+自动+直连+拒绝+地区组

	// 1. 手动选择（先收集组名，故先建地区组再填 proxies）
	autoProxies := make([]any, 0, len(allNames))
	for _, name := range allNames {
		autoProxies = append(autoProxies, name)
	}

	// 2. 自动选择
	groups = append(groups, map[string]any{
		"name":     GroupAuto,
		"type":     "url-test",
		"url":      TestURL,
		"interval": TestInterval,
		"proxies":  autoProxies,
	})

	// 3. 地区组
	for _, rg := range regions {
		gname := rg.region + "节点"
		manualProxies = append(manualProxies, gname)
		proxies := make([]any, 0, len(rg.nodes))
		for _, name := range rg.nodes {
			proxies = append(proxies, name)
		}
		groups = append(groups, map[string]any{
			"name":     gname,
			"type":     "url-test",
			"url":      TestURL,
			"interval": TestInterval,
			"proxies":  proxies,
		})
	}

	// 4. 其他节点
	if len(otherNodes) > 0 {
		manualProxies = append(manualProxies, GroupOther)
		proxies := make([]any, 0, len(otherNodes))
		for _, name := range otherNodes {
			proxies = append(proxies, name)
		}
		groups = append(groups, map[string]any{
			"name":     GroupOther,
			"type":     "url-test",
			"url":      TestURL,
			"interval": TestInterval,
			"proxies":  proxies,
		})
	}

	// 5. 直连/拒绝（R1：其他节点组之后声明；DIRECT/REJECT 为 Clash 内置出站，
	// 组非空故合法，mihomo 校验通过）
	for _, name := range allNames {
		manualProxies = append(manualProxies, name)
	}
	manualProxies = append(manualProxies, GroupDirect, GroupReject)
	groups = append(groups,
		map[string]any{"name": GroupDirect, "type": "select", "proxies": []any{"DIRECT"}},
		map[string]any{"name": GroupReject, "type": "select", "proxies": []any{"REJECT"}},
	)

	// 6. 手动选择（proxies 依赖上面组名，最后填充）
	manual := map[string]any{
		"name":    GroupManual,
		"type":    "select",
		"proxies": manualProxies,
	}
	groups = append([]map[string]any{manual}, groups...)

	return groups, nil
}

// regionOf 从节点名中提取"第一个地区线索"（emoji/中文/拼音/英文/ISO 代码，
// 按字节位置最靠前者胜出；同位置高显式度层胜出；同位置同层别名更长者胜），
// 返回命中地区的规范 emoji 与中文名——与命中别名/层无关，故混合 emoji/文字/ISO
// 命名的节点自然并入同名组。识别不了返回 ok=false。
func regionOf(name string) (emoji, region string, ok bool) {
	lower := strings.ToLower(name) // ASCII 小写等长，字节索引与 name 一致
	bestIdx, bestLayer, bestAliasLen := -1, 0, 0
	var best *regionInfo
	for i := range regionInfos {
		ri := &regionInfos[i]
		if idx := strings.Index(name, ri.emoji); idx >= 0 && better(idx, layerEmoji, len(ri.emoji), bestIdx, bestLayer, bestAliasLen) {
			best, bestIdx, bestLayer, bestAliasLen = ri, idx, layerEmoji, len(ri.emoji)
		}
		for _, a := range ri.zh { // 中文（原串子串，无边界）
			if idx := strings.Index(name, a); idx >= 0 && better(idx, layerZH, len(a), bestIdx, bestLayer, bestAliasLen) {
				best, bestIdx, bestLayer, bestAliasLen = ri, idx, layerZH, len(a)
			}
		}
		for _, a := range ri.py { // 拼音（小写串 + token 边界）
			if idx := tokenIndex(lower, a); idx >= 0 && better(idx, layerPY, len(a), bestIdx, bestLayer, bestAliasLen) {
				best, bestIdx, bestLayer, bestAliasLen = ri, idx, layerPY, len(a)
			}
		}
		for _, a := range ri.en { // 英文（小写串 + token 边界，含多词短语）
			if idx := tokenIndex(lower, a); idx >= 0 && better(idx, layerEN, len(a), bestIdx, bestLayer, bestAliasLen) {
				best, bestIdx, bestLayer, bestAliasLen = ri, idx, layerEN, len(a)
			}
		}
		iso := strings.ToLower(ri.iso) // iso 字段为大写，须小写化后在小写串上匹配
		if iso != isoDenyNo {          // ISO（小写串 + token 边界；"no" 入 denylist）
			if idx := tokenIndex(lower, iso); idx >= 0 && better(idx, layerISO, len(iso), bestIdx, bestLayer, bestAliasLen) {
				best, bestIdx, bestLayer, bestAliasLen = ri, idx, layerISO, len(iso)
			}
		}
	}
	if best == nil {
		return "", "", false
	}
	return best.emoji, best.region, true
}
