package transform

import (
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
