package groups

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRegionTablesValid 校验 47 地区数据表不变量：
// emoji 非空 / iso 长度 2 / region 非空且规范名全局唯一 /
// 至少一层别名非空 / 全局别名唯一性（一别名只映射一个地区，同地区内重复允许）/
// 无单字符别名 / emoji↔iso 码位一致性。
func TestRegionTablesValid(t *testing.T) {
	if got := len(regionInfos); got != 47 {
		t.Errorf("regionInfos 地区数 = %d, want 47", got)
	}
	aliasOwner := make(map[string]int) // 小写别名 → regionInfos 下标
	regionSeen := make(map[string]int) // region 规范名 → regionInfos 下标
	for i := range regionInfos {
		ri := &regionInfos[i]
		if ri.emoji == "" {
			t.Errorf("regionInfos[%d](%s) emoji 为空", i, ri.region)
		}
		if len(ri.iso) != 2 || ri.iso < "AA" || ri.iso > "ZZ" {
			t.Errorf("regionInfos[%d](%s) iso=%q 非法", i, ri.region, ri.iso)
		}
		if ri.region == "" {
			t.Errorf("regionInfos[%d] region 为空", i)
		}
		if prev, dup := regionSeen[ri.region]; dup {
			t.Errorf("region %q 重复出现在下标 %d 与 %d（规范名必须唯一）", ri.region, prev, i)
		}
		regionSeen[ri.region] = i
		if len(ri.zh) == 0 && len(ri.py) == 0 && len(ri.en) == 0 {
			t.Errorf("regionInfos[%d](%s) 三层别名全为空", i, ri.region)
		}
		// emoji ↔ iso 码位一致性：🇭=U+1F1ED→'H'、🇰=U+1F1F0→'K'，合为 HK
		r1, r2 := emojiRegionIndicators(ri.emoji)
		if r1 == 0 || r2 == 0 || string([]rune{r1, r2}) != ri.iso {
			t.Errorf("regionInfos[%d](%s) emoji=%s 与 iso=%s 不一致", i, ri.region, ri.emoji, ri.iso)
		}
		check := func(layer, alias string) {
			if utf8.RuneCountInString(alias) < 2 {
				t.Errorf("regionInfos[%d](%s) %s 别名 %q 为单字符", i, ri.region, layer, alias)
			}
			key := strings.ToLower(alias)
			if prev, dup := aliasOwner[key]; dup && prev != i {
				t.Errorf("别名 %q 同时映射到 %s(下标%d) 与 %s(下标%d)",
					key, regionInfos[prev].region, prev, ri.region, i)
			}
			aliasOwner[key] = i
		}
		for _, a := range ri.zh {
			check("zh", a)
		}
		for _, a := range ri.py {
			check("py", a)
		}
		for _, a := range ri.en {
			check("en", a)
		}
		check("iso", ri.iso)
	}
}

// emojiRegionIndicators 从国旗 emoji 推导两个区域指示字母（🇭=U+1F1ED→'H'）；
// 非双区域指示符返回 (0, 0)。
func emojiRegionIndicators(emoji string) (rune, rune) {
	rs := []rune(emoji)
	if len(rs) != 2 || rs[0] < 0x1F1E6 || rs[0] > 0x1F1FF || rs[1] < 0x1F1E6 || rs[1] > 0x1F1FF {
		return 0, 0
	}
	return 'A' + rune(rs[0]-0x1F1E6), 'A' + rune(rs[1]-0x1F1E6)
}

// TestRegionOfMatrix 识别矩阵（方案第 8 节 B）：每层正例、emoji+文字混合、
// 防误判反例、英文单词同形、优先级冲突、城市映射。want 为空串表示识别失败
// （应进 "其他节点"）。
func TestRegionOfMatrix(t *testing.T) {
	tests := []struct {
		name string
		want string // "emoji 地区"；"" = 识别失败
	}{
		// L1 emoji 正例
		{"🇭🇰 香港 01", "🇭🇰 香港"},
		{"SS-🇭🇰-01", "🇭🇰 香港"},
		{"🇯🇵 日本 01", "🇯🇵 日本"},
		// L2 中文正例（含繁体/异名）
		{"香港01", "🇭🇰 香港"},
		{"臺灣01", "🇹🇼 台湾"},
		{"日本东京1", "🇯🇵 日本"},
		{"澳洲-01", "🇦🇺 澳大利亚"},
		{"澳門節點", "🇲🇴 澳门"},
		// L3 拼音正例
		{"xianggang-01", "🇭🇰 香港"},
		{"taiwan02", "🇹🇼 台湾"},
		{"aomen-01", "🇲🇴 澳门"},
		// L4 英文正例（含习惯写法/多词短语）
		{"Hong Kong 1", "🇭🇰 香港"},
		{"HongKong01", "🇭🇰 香港"},
		{"hongkong02", "🇭🇰 香港"},
		{"Tokyo-2", "🇯🇵 日本"},
		{"United States 01", "🇺🇸 美国"},
		{"Korea-01", "🇰🇷 韩国"},
		{"UK-01", "🇬🇧 英国"},
		// L5 ISO 正例（大小写不敏感）
		{"HK-01", "🇭🇰 香港"},
		{"hk01", "🇭🇰 香港"},
		{"US_LA", "🇺🇸 美国"},
		{"JP2", "🇯🇵 日本"},
		{"TW-01", "🇹🇼 台湾"},
		{"GB-01", "🇬🇧 英国"},
		// emoji+文字混合（同地区/跨地区）
		{"🇭🇰香港", "🇭🇰 香港"},
		{"🇭🇰香港 美国节点", "🇭🇰 香港"},
		// 防误判反例（"正确地区而非其他"的标注特例）
		{"Australia-01", "🇦🇺 澳大利亚"}, // us@1 前为字母 a → 阻断；en 别名命中
		{"MUSIC-01", ""},
		{"business01", ""},
		{"focus-01", ""},
		{"custom-01", ""},
		{"status-01", ""},
		{"CHINA-01", ""},
		{"code-01", ""},
		{"delete-01", ""},
		{"jpeg-01", ""},
		{"hukou-01", ""},
		{"canada-01", "🇨🇦 加拿大"}, // ca@0 后为字母 n → 阻断；en 别名命中
		{"RUSSIA-01", "🇷🇺 俄罗斯"}, // us@1 前为字母 S → 阻断；en 别名命中
		{"usa-01", "🇺🇸 美国"},     // us@0 后为字母 a → 阻断；en 别名命中
		// 边界逻辑直接锁定（ISO 边界命中 vs 字母前缀阻断）
		{"ca-01", "🇨🇦 加拿大"}, // ca@0 独立 token → 边界命中
		{"xca-01", ""},      // ca@1 前为字母 x → 阻断，不误判
		// 英文单词与 ISO 同形
		{"No.1 香港 01", "🇭🇰 香港"}, // no 在 denylist；香港 中文命中
		{"My-01", "🇲🇾 马来西亚"},    // my 独立 token = 地区代码（文档化接受）
		{"in-01", "🇮🇳 印度"},      // in 独立 token = 地区代码（文档化接受）
		// 优先级/冲突：(字节位置, 层序) 字典序最小
		{"香港台湾专线", "🇭🇰 香港"},
		{"台湾香港专线", "🇹🇼 台湾"},
		{"美国🇭🇰节点", "🇺🇸 美国"}, // 位置优先：美国@0 < emoji@6
		{"HK 香港 01", "🇭🇰 香港"},
		// 前缀别名同层同位置冲突：更长别名胜（印度尼西亚 > 印度），与表序无关
		{"印度尼西亚01", "🇮🇩 印尼"},
		{"印度01", "🇮🇳 印度"},
		// 城市别名 → 地区
		{"Tokyo-2", "🇯🇵 日本"},
		{"大阪01", "🇯🇵 日本"},
		{"seoul-01", "🇰🇷 韩国"},
		{"洛杉矶-01", "🇺🇸 美国"},
		{"纽约 01", "🇺🇸 美国"},
		{"frankfurt-01", "🇩🇪 德国"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			emoji, region, ok := regionOf(tt.name)
			if tt.want == "" {
				if ok {
					t.Errorf("regionOf(%q) = %s %s, want 识别失败", tt.name, emoji, region)
				}
				return
			}
			if !ok || emoji+" "+region != tt.want {
				t.Errorf("regionOf(%q) = (%q, %q, ok=%v), want %q", tt.name, emoji, region, ok, tt.want)
			}
		})
	}
}

// TestBuildMultiSourceGroups 端到端：文字+ISO+emoji 三种线索归入同一香港组，
// 城市别名归入日本/美国/德国组，无标识节点进其他组；GroupManual 组名顺序与
// GroupAuto 全量去重不变。
func TestBuildMultiSourceGroups(t *testing.T) {
	nodes := []map[string]any{
		node("香港01"),
		node("HK-01"),
		node("🇭🇰 香港 02"),
		node("Tokyo-2"),
		node("Australia-01"),
		node("无标识节点"),
	}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	// 文字+ISO+emoji 共组：香港组 3 节点
	hk := findGroup(t, groups, "香港节点")
	if got := proxies(hk); !reflect.DeepEqual(got, []string{"香港01", "HK-01", "🇭🇰 香港 02"}) {
		t.Errorf("HK proxies = %v, want 3 节点共组", got)
	}
	jp := findGroup(t, groups, "日本节点")
	if got := proxies(jp); !reflect.DeepEqual(got, []string{"Tokyo-2"}) {
		t.Errorf("JP proxies = %v", got)
	}
	au := findGroup(t, groups, "澳大利亚节点")
	if got := proxies(au); !reflect.DeepEqual(got, []string{"Australia-01"}) {
		t.Errorf("AU proxies = %v", got)
	}
	other := findGroup(t, groups, GroupOther)
	if got := proxies(other); !reflect.DeepEqual(got, []string{"无标识节点"}) {
		t.Errorf("other proxies = %v", got)
	}

	// GroupManual 组名顺序：地区组按首次出现，其他节点恒最后
	manual := findGroup(t, groups, GroupManual)
	// R2：手动组 = 自动选择 + 地区组名（出现序）+ 其他节点组 + 全部节点名（输入顺序）+ 直连组 + 拒绝组
	wantManual := []string{GroupAuto, "香港节点", "日本节点", "澳大利亚节点", GroupOther, "香港01", "HK-01", "🇭🇰 香港 02", "Tokyo-2", "Australia-01", "无标识节点", GroupDirect, GroupReject}
	if got := proxies(manual); !reflect.DeepEqual(got, wantManual) {
		t.Errorf("manual proxies = %v, want %v", got, wantManual)
	}

	// R8：OpenCode 组存在且顺序正确（= [手动选择, ...手动组全量]），
	// 全局组序 = [手动选择, 自动选择, 地区组..., 其他节点?, OpenCode, 漏网之鱼, 直连, 拒绝]
	opencode := findGroup(t, groups, GroupOpenCode)
	wantOpenCode := append([]string{GroupManual}, wantManual...)
	if got := proxies(opencode); !reflect.DeepEqual(got, wantOpenCode) {
		t.Errorf("OpenCode proxies = %v, want %v", got, wantOpenCode)
	}
	gnames := groupNames(groups)
	wantOrder := []string{GroupManual, GroupAuto, "香港节点", "日本节点", "澳大利亚节点", GroupOther, GroupOpenCode, GroupLeak, GroupDirect, GroupReject}
	if !reflect.DeepEqual(gnames, wantOrder) {
		t.Errorf("组序 = %v, want %v", gnames, wantOrder)
	}

	// GroupAuto 全量去重不变
	auto := findGroup(t, groups, GroupAuto)
	wantAuto := []string{"香港01", "HK-01", "🇭🇰 香港 02", "Tokyo-2", "Australia-01", "无标识节点"}
	if got := proxies(auto); !reflect.DeepEqual(got, wantAuto) {
		t.Errorf("auto proxies = %v, want %v", got, wantAuto)
	}
}
