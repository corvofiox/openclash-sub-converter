package transform

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func node(name string) map[string]any {
	return map[string]any{"name": name, "type": "ss"}
}

func names(nodes []map[string]any) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n["name"].(string))
	}
	return out
}

func TestApplyNoRules(t *testing.T) {
	nodes := []map[string]any{node("🇭🇰 香港 01"), node("🇯🇵 日本 01")}
	out, err := Apply(nodes, Filter{})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if !reflect.DeepEqual(names(out), []string{"🇭🇰 香港 01", "🇯🇵 日本 01"}) {
		t.Errorf("names = %v, want unchanged", names(out))
	}
}

func TestApplyExclude(t *testing.T) {
	nodes := []map[string]any{node("🇭🇰 香港 01"), node("🇭🇰 香港 02"), node("🇯🇵 日本 01")}
	out, err := Apply(nodes, Filter{Exclude: "香港"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"🇯🇵 日本 01"}) {
		t.Errorf("names = %v, want [🇯🇵 日本 01]", got)
	}
}

func TestApplyInclude(t *testing.T) {
	nodes := []map[string]any{node("🇭🇰 香港 01"), node("🇭🇰 香港 02"), node("🇯🇵 日本 01")}
	out, err := Apply(nodes, Filter{Include: "香港"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"🇭🇰 香港 01", "🇭🇰 香港 02"}) {
		t.Errorf("names = %v, want only HK nodes", got)
	}
}

func TestApplyExcludeThenInclude(t *testing.T) {
	// 先 Exclude 后 Include：先剔除香港节点，再只保留名字含 "01" 的
	nodes := []map[string]any{node("🇭🇰 香港 01"), node("🇭🇰 香港 02"), node("🇯🇵 日本 01"), node("🇯🇵 日本 02")}
	out, err := Apply(nodes, Filter{Exclude: "^🇭🇰", Include: "01"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"🇯🇵 日本 01"}) {
		t.Errorf("names = %v, want [🇯🇵 日本 01]", got)
	}
}

func TestApplyRenameAllOccurrences(t *testing.T) {
	// ReplaceAllString：所有匹配都被替换；未匹配的节点保持不变
	nodes := []map[string]any{node("香港香港 01"), node("🇯🇵 日本 01")}
	out, err := Apply(nodes, Filter{Rename: "香港/HK"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"HKHK 01", "🇯🇵 日本 01"}) {
		t.Errorf("names = %v, want [HKHK 01, 🇯🇵 日本 01]", got)
	}
	// 其他字段保持不变
	if out[0]["type"] != "ss" {
		t.Errorf("type = %v, want ss preserved", out[0]["type"])
	}
}

func TestApplyRenameDuplicateSuffix(t *testing.T) {
	// 重命名后重名：保留第一个，后续追加 " (N)"
	nodes := []map[string]any{node("香港 01"), node("香港 02"), node("香港 03")}
	out, err := Apply(nodes, Filter{Rename: ".*/A"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"A", "A (2)", "A (3)"}) {
		t.Errorf("names = %v, want [A, A (2), A (3)]", got)
	}
}

func TestApplyRenameCollidesWithExistingName(t *testing.T) {
	// 重命名后的名字与列表中已有节点名冲突也要去重
	nodes := []map[string]any{node("A"), node("A-1")}
	out, err := Apply(nodes, Filter{Rename: "A-1/A"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"A", "A (2)"}) {
		t.Errorf("names = %v, want [A, A (2)]", got)
	}
}

func TestApplyDuplicateNamesNoRename(t *testing.T) {
	// P1-2 回归：无 rename 时同名节点同样去重（首个保留原名，后续加 " (N)"），
	// 否则多源聚合重名会触发 mihomo 硬错误 "proxy X is the duplicate name"。
	nodes := []map[string]any{node("香港 01"), node("香港 01"), node("香港 01")}
	out, err := Apply(nodes, Filter{})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"香港 01", "香港 01 (2)", "香港 01 (3)"}) {
		t.Errorf("names = %v, want [香港 01, 香港 01 (2), 香港 01 (3)]", got)
	}
	// 去重后写回 map["name"]
	if out[1]["name"] != "香港 01 (2)" {
		t.Errorf("out[1].name = %v, want 香港 01 (2)", out[1]["name"])
	}
}

func TestApplyDuplicateNamesAfterRenameNoMatch(t *testing.T) {
	// rename 正则未命中时也统一去重
	nodes := []map[string]any{node("A"), node("A")}
	out, err := Apply(nodes, Filter{Rename: "不匹配/x"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"A", "A (2)"}) {
		t.Errorf("names = %v, want [A, A (2)]", got)
	}
}

func TestApplyInvalidRegex(t *testing.T) {
	cases := []struct {
		name   string
		filter Filter
	}{
		{"include", Filter{Include: "("}},
		{"exclude", Filter{Exclude: "["}},
		{"rename", Filter{Rename: "[/x"}},
		{"rename", Filter{Rename: "abc"}},
	}
	nodes := []map[string]any{node("x")}
	for _, tc := range cases {
		if _, err := Apply(nodes, tc.filter); err == nil {
			t.Errorf("%s: Apply = nil error, want error", tc.name)
		} else if !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s: error %q does not mention field name", tc.name, err)
		}
	}
}

func TestApplyEmptyNodes(t *testing.T) {
	out, err := Apply(nil, Filter{Rename: "x/y", Include: "z", Exclude: "w"})
	if err != nil {
		t.Fatalf("Apply(nil) error: %v", err)
	}
	if out == nil || len(out) != 0 {
		t.Errorf("Apply(nil) = %v, want empty non-nil slice", out)
	}
}

func TestApplySkipsNonStringName(t *testing.T) {
	nodes := []map[string]any{
		{"name": "ok", "type": "ss"},
		{"name": 123, "type": "ss"},
		{"name": "", "type": "ss"},
	}
	out, err := Apply(nodes, Filter{})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"ok"}) {
		t.Errorf("names = %v, want [ok]", got)
	}
}

// TestStripEmoji 断言 emoji 剥离粒度：仅 emoji 字符（旗标/符号/VS16/ZWJ/键帽），
// 保留空格与 |/｜ 分隔符；无 emoji 时字节不变；纯 emoji 名保留原名（防空名）。
func TestStripEmoji(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"🇭🇰 香港 01", "香港 01"},           // 旗标剥离，空格保留
		{"🇭🇰 香港 01 ｜ 电信", "香港 01 ｜ 电信"}, // 全角｜分隔符保留
		{"🇭🇰🇺🇸 香港", "香港"},               // 双旗标
		{"☀️ 香港", "香港"},                 // 2600 杂项符号 + VS16
		{"👨‍👩‍👧‍👦 香港", "香港"},            // ZWJ 序列整体剥离
		{"1️⃣ 香港", "1 香港"},              // 键帽剥离后残留数字
		{"香港01", "香港01"},                // 无 emoji → 字节不变
		{"香港  01", "香港  01"},            // 无 emoji → 双空格不折叠（字节不变）
		{"🇭🇰 香港  01", "香港 01"},          // 有 emoji → 剥离后折叠连续空白
		{"🇭🇰", "🇭🇰"},                    // 纯 emoji → 保留原名（防空名）
	}
	for _, tc := range cases {
		if got := StripEmoji(tc.name); got != tc.want {
			t.Errorf("StripEmoji(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// groupProxies 提取策略组 proxies 为字符串切片（测试辅助）。
func groupProxies(g map[string]any) []string {
	raw, ok := g["proxies"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		out = append(out, p.(string))
	}
	return out
}

// TestApplyStripEmoji 断言剥离编排：strip=false no-op；strip=true 时全部节点
// 统一 uniqueName 去重、组 proxies 按 renameMap 全量改写、引用与节点名一致。
func TestApplyStripEmoji(t *testing.T) {
	t.Run("strip=false no-op", func(t *testing.T) {
		nodes := []map[string]any{node("🇭🇰 香港01"), node("香港01")}
		groups := []map[string]any{
			{"name": "手动选择", "type": "select", "proxies": []any{"DIRECT", "🇭🇰 香港01", "香港01"}},
		}
		if rm := ApplyStripEmoji(nodes, groups, false); rm != nil {
			t.Errorf("strip=false renameMap = %v, want nil", rm)
		}
		if got := names(nodes); !reflect.DeepEqual(got, []string{"🇭🇰 香港01", "香港01"}) {
			t.Errorf("strip=false nodes mutated: %v", got)
		}
		if got := groupProxies(groups[0]); !reflect.DeepEqual(got, []string{"DIRECT", "🇭🇰 香港01", "香港01"}) {
			t.Errorf("strip=false group proxies mutated: %v", got)
		}
	})

	t.Run("strip=true 重名去重", func(t *testing.T) {
		nodes := []map[string]any{node("🇭🇰 香港01"), node("香港01")}
		groups := []map[string]any{
			{"name": "手动选择", "type": "select", "proxies": []any{"DIRECT", "🇭🇰 香港01", "香港01"}},
		}
		rm := ApplyStripEmoji(nodes, groups, true)
		if got := names(nodes); !reflect.DeepEqual(got, []string{"香港01", "香港01 (2)"}) {
			t.Errorf("names = %v, want [香港01 香港01 (2)]", got)
		}
		if rm["🇭🇰 香港01"] != "香港01" || rm["香港01"] != "香港01 (2)" {
			t.Errorf("renameMap = %v, want 🇭🇰 香港01→香港01, 香港01→香港01 (2)", rm)
		}
		if got := groupProxies(groups[0]); !reflect.DeepEqual(got, []string{"DIRECT", "香港01", "香港01 (2)"}) {
			t.Errorf("group proxies = %v, want rewritten [DIRECT 香港01 香港01 (2)]", got)
		}
	})

	t.Run("顺序颠倒 未剥离节点被挤号", func(t *testing.T) {
		nodes := []map[string]any{node("香港01"), node("🇭🇰 香港01")}
		groups := []map[string]any{
			{"name": "手动选择", "type": "select", "proxies": []any{"DIRECT", "香港01", "🇭🇰 香港01"}},
		}
		rm := ApplyStripEmoji(nodes, groups, true)
		if got := names(nodes); !reflect.DeepEqual(got, []string{"香港01", "香港01 (2)"}) {
			t.Errorf("names = %v, want [香港01 香港01 (2)]", got)
		}
		if _, hit := rm["香港01"]; hit {
			t.Errorf("renameMap 不应包含未改名的 香港01: %v", rm)
		}
		if rm["🇭🇰 香港01"] != "香港01 (2)" {
			t.Errorf("renameMap = %v, want 🇭🇰 香港01→香港01 (2)", rm)
		}
		if got := groupProxies(groups[0]); !reflect.DeepEqual(got, []string{"DIRECT", "香港01", "香港01 (2)"}) {
			t.Errorf("group proxies = %v", got)
		}
	})

	t.Run("组 proxies 与节点名集合一致", func(t *testing.T) {
		nodes := []map[string]any{node("🇭🇰 香港01"), node("🇯🇵 日本01"), node("香港01")}
		groups := []map[string]any{
			{"name": "手动选择", "type": "select", "proxies": []any{"DIRECT", "🇭🇰 香港01", "🇯🇵 日本01", "香港01"}},
			{"name": "自动选择", "type": "url-test", "proxies": []any{"🇭🇰 香港01", "🇯🇵 日本01", "香港01"}},
			{"name": "DIRECT", "type": "direct"},
		}
		ApplyStripEmoji(nodes, groups, true)
		set := map[string]bool{}
		for _, n := range nodes {
			set[n["name"].(string)] = true
		}
		if !set["香港01"] || !set["香港01 (2)"] || !set["日本01"] {
			t.Errorf("node set = %v, want 香港01/香港01 (2)/日本01", set)
		}
		for _, g := range groups {
			for _, p := range groupProxies(g) {
				if p == "DIRECT" {
					continue
				}
				if !set[p] {
					t.Errorf("group %v proxies entry %q 不在节点名集合 %v", g["name"], p, set)
				}
			}
		}
	})
}

// TestApplyRenameMultiRule 断言多条 rename 规则（逗号分隔）各自命中对应节点，
// 未命中的节点保持原名。
func TestApplyRenameMultiRule(t *testing.T) {
	nodes := []map[string]any{node("香港 01"), node("台湾 01"), node("日本 01")}
	out, err := Apply(nodes, Filter{Rename: "^香港/HK,^台湾/TW"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"HK 01", "TW 01", "日本 01"}) {
		t.Errorf("names = %v, want [HK 01 TW 01 日本 01]", got)
	}
}

// TestApplyRenameMultiRuleSequential 断言多条规则顺序执行：前一条的输出是后一条的输入。
func TestApplyRenameMultiRuleSequential(t *testing.T) {
	nodes := []map[string]any{node("日本 01"), node("香港 01")}
	out, err := Apply(nodes, Filter{Rename: "日本/JP,^JP/NIP"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	// "日本 01" 先变 "JP 01"，再被 "^JP" 命中变 "NIP 01"
	if got := names(out); !reflect.DeepEqual(got, []string{"NIP 01", "香港 01"}) {
		t.Errorf("names = %v, want [NIP 01 香港 01]", got)
	}
}

// TestApplyRenameMultiRuleTrailingComma 断言尾逗号/空段被跳过（"日本/JP," 合法）。
func TestApplyRenameMultiRuleTrailingComma(t *testing.T) {
	nodes := []map[string]any{node("日本 01"), node("香港 01")}
	out, err := Apply(nodes, Filter{Rename: "日本/JP,"})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if got := names(out); !reflect.DeepEqual(got, []string{"JP 01", "香港 01"}) {
		t.Errorf("names = %v, want [JP 01 香港 01]", got)
	}
}

// TestApplyRenameMultiRuleInvalid 断言任一规则非法 → 整体 ErrInvalidRegex（不部分生效），
// 且错误消息指向出错的规则原文（part 而非整个参数）。
func TestApplyRenameMultiRuleInvalid(t *testing.T) {
	nodes := []map[string]any{node("x")}
	cases := []struct {
		rename  string
		wantMsg string
	}{
		{"日本/JP,香港", `invalid rename rule "香港"`}, // 第二条缺 "/"，消息带规则原文
		{"日本/JP,[/x", "invalid rename regex"},    // 第二条正则编译失败
	}
	for _, tc := range cases {
		_, err := Apply(nodes, Filter{Rename: tc.rename})
		if err == nil {
			t.Errorf("Apply(%q) = nil error, want ErrInvalidRegex", tc.rename)
			continue
		}
		if !errors.Is(err, ErrInvalidRegex) {
			t.Errorf("Apply(%q) error = %v, want ErrInvalidRegex", tc.rename, err)
		}
		if !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("Apply(%q) error %q 未包含 %q", tc.rename, err, tc.wantMsg)
		}
	}
}
