// Package groups 根据节点列表构建 Clash 策略组。
package groups

import (
	"strings"
)

// 策略组常量。
const (
	TestURL      = "https://www.gstatic.com/generate_204"
	TestInterval = 300

	GroupManual = "🚀 手动选择"
	GroupAuto   = "♻️ 自动选择"
	GroupOther  = "🌐 其他节点"
)

// flagRegions 是 emoji 国旗 → 地区中文名映射表（有序切片保证确定性；
// 同名字段优先级与输出顺序都依赖此顺序）。
var flagRegions = []struct{ emoji, region string }{
	{"🇭🇰", "香港"}, {"🇲🇴", "澳门"}, {"🇹🇼", "台湾"},
	{"🇯🇵", "日本"}, {"🇰🇷", "韩国"}, {"🇸🇬", "新加坡"},
	{"🇲🇾", "马来西亚"}, {"🇹🇭", "泰国"}, {"🇻🇳", "越南"},
	{"🇮🇩", "印尼"}, {"🇵🇭", "菲律宾"}, {"🇮🇳", "印度"},
	{"🇺🇸", "美国"}, {"🇨🇦", "加拿大"}, {"🇲🇽", "墨西哥"},
	{"🇧🇷", "巴西"}, {"🇦🇺", "澳大利亚"}, {"🇳🇿", "新西兰"},
	{"🇬🇧", "英国"}, {"🇫🇷", "法国"}, {"🇩🇪", "德国"},
	{"🇳🇱", "荷兰"}, {"🇸🇪", "瑞典"}, {"🇫🇮", "芬兰"},
	{"🇳🇴", "挪威"}, {"🇩🇰", "丹麦"}, {"🇮🇹", "意大利"},
	{"🇪🇸", "西班牙"}, {"🇵🇹", "葡萄牙"}, {"🇹🇷", "土耳其"},
	{"🇷🇺", "俄罗斯"}, {"🇦🇪", "阿联酋"}, {"🇮🇱", "以色列"},
	{"🇿🇦", "南非"}, {"🇪🇬", "埃及"}, {"🇦🇷", "阿根廷"},
	{"🇨🇱", "智利"}, {"🇵🇱", "波兰"}, {"🇨🇭", "瑞士"},
	{"🇦🇹", "奥地利"}, {"🇧🇪", "比利时"}, {"🇨🇿", "捷克"},
	{"🇬🇷", "希腊"}, {"🇭🇺", "匈牙利"}, {"🇮🇪", "爱尔兰"},
	{"🇱🇺", "卢森堡"}, {"🇺🇦", "乌克兰"},
}

// regionGroup 是一个地区策略组的中间表示。
type regionGroup struct {
	emoji  string
	region string
	nodes  []string
}

// Build 根据节点列表构建策略组列表：
//
//   - "🚀 手动选择" type=select，proxies=[DIRECT, ♻️ 自动选择, <地区组名...>]；
//   - "♻️ 自动选择" type=url-test，proxies=[全部节点名]（去重）；
//   - 地区组：按节点名中的 emoji 国旗映射地区，组名 "「emoji 地区」节点" 格式，
//     type=url-test，url/interval 固定；节点数 0 的地区不生成组；
//   - 无 emoji 或未知 emoji 的节点归入 "🌐 其他节点" 组；
//   - 兜底组 "DIRECT"(type=direct) 与 "REJECT"(type=reject)。
//
// 节点名重复时去重（同一节点只进一个组）。空节点列表仍输出手动选择/自动选择/
// DIRECT/REJECT，不报错。
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

		emoji, region, ok := regionOf(name)
		if !ok {
			otherNodes = append(otherNodes, name)
			continue
		}
		rg, exists := regionByName[region]
		if !exists {
			rg = &regionGroup{emoji: emoji, region: region}
			regionByName[region] = rg
			regions = append(regions, rg)
		}
		rg.nodes = append(rg.nodes, name)
	}

	// 收集组名（地区组按首次出现顺序，其他节点组放最后）。
	manualProxies := []any{"DIRECT", GroupAuto}
	groups := make([]map[string]any, 0, 3+len(regions)+2)

	// 1. 🚀 手动选择（先收集组名，故先建地区组再填 proxies）
	autoProxies := make([]any, 0, len(allNames))
	for _, name := range allNames {
		autoProxies = append(autoProxies, name)
	}

	// 2. ♻️ 自动选择
	groups = append(groups, map[string]any{
		"name":     GroupAuto,
		"type":     "url-test",
		"url":      TestURL,
		"interval": TestInterval,
		"proxies":  autoProxies,
	})

	// 3. 地区组
	for _, rg := range regions {
		gname := rg.emoji + " " + rg.region + "节点"
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

	// 4. 🌐 其他节点
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

	// 5. 🚀 手动选择（proxies 依赖上面组名，最后填充）
	manual := map[string]any{
		"name":    GroupManual,
		"type":    "select",
		"proxies": manualProxies,
	}
	groups = append([]map[string]any{manual}, groups...)

	// 6. 兜底组
	groups = append(groups,
		map[string]any{"name": "DIRECT", "type": "direct"},
		map[string]any{"name": "REJECT", "type": "reject"},
	)
	return groups, nil
}

// regionOf 从节点名中提取第一个已知 emoji 国旗，返回其 emoji 与地区中文名。
func regionOf(name string) (emoji, region string, ok bool) {
	bestIdx := -1
	for _, fr := range flagRegions {
		if idx := strings.Index(name, fr.emoji); idx >= 0 && (bestIdx < 0 || idx < bestIdx) {
			bestIdx = idx
			emoji = fr.emoji
			region = fr.region
		}
	}
	if bestIdx < 0 {
		return "", "", false
	}
	return emoji, region, true
}
