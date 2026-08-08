package groups

import (
	"reflect"
	"testing"
)

func node(name string) map[string]any {
	return map[string]any{"name": name, "type": "ss"}
}

func findGroup(t *testing.T, groups []map[string]any, name string) map[string]any {
	t.Helper()
	for _, g := range groups {
		if g["name"] == name {
			return g
		}
	}
	t.Fatalf("group %q not found in %v", name, groupNames(groups))
	return nil
}

func groupNames(groups []map[string]any) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g["name"].(string))
	}
	return out
}

func proxies(g map[string]any) []string {
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

func TestBuildMixedRegions(t *testing.T) {
	nodes := []map[string]any{
		node("🇭🇰 香港 01"),
		node("🇭🇰 香港 02"),
		node("🇯🇵 日本 01"),
		node("新加坡 01"),   // 无 emoji
		node("🇨🇳 大陆 01"), // 未知 emoji（中国不在映射表）
	}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	manual := findGroup(t, groups, GroupManual)
	if manual["type"] != "select" {
		t.Errorf("manual type = %v, want select", manual["type"])
	}
	wantManual := []string{"DIRECT", GroupAuto, "🇭🇰 香港节点", "🇯🇵 日本节点", GroupOther}
	if got := proxies(manual); !reflect.DeepEqual(got, wantManual) {
		t.Errorf("manual proxies = %v, want %v", got, wantManual)
	}

	auto := findGroup(t, groups, GroupAuto)
	wantAuto := []string{"🇭🇰 香港 01", "🇭🇰 香港 02", "🇯🇵 日本 01", "新加坡 01", "🇨🇳 大陆 01"}
	if got := proxies(auto); !reflect.DeepEqual(got, wantAuto) {
		t.Errorf("auto proxies = %v, want %v", got, wantAuto)
	}

	hk := findGroup(t, groups, "🇭🇰 香港节点")
	if hk["type"] != "url-test" || hk["url"] != TestURL || hk["interval"] != TestInterval {
		t.Errorf("HK group fields = %v, want url-test %s %d", hk, TestURL, TestInterval)
	}
	if got := proxies(hk); !reflect.DeepEqual(got, []string{"🇭🇰 香港 01", "🇭🇰 香港 02"}) {
		t.Errorf("HK proxies = %v", got)
	}

	jp := findGroup(t, groups, "🇯🇵 日本节点")
	if got := proxies(jp); !reflect.DeepEqual(got, []string{"🇯🇵 日本 01"}) {
		t.Errorf("JP proxies = %v", got)
	}

	other := findGroup(t, groups, GroupOther)
	if got := proxies(other); !reflect.DeepEqual(got, []string{"新加坡 01", "🇨🇳 大陆 01"}) {
		t.Errorf("other proxies = %v", got)
	}

	direct := findGroup(t, groups, "DIRECT")
	if direct["type"] != "direct" {
		t.Errorf("DIRECT type = %v, want direct", direct["type"])
	}
	reject := findGroup(t, groups, "REJECT")
	if reject["type"] != "reject" {
		t.Errorf("REJECT type = %v, want reject", reject["type"])
	}
}

func TestBuildMoreRegions(t *testing.T) {
	nodes := []map[string]any{
		node("🇺🇸 美国 01"),
		node("🇹🇼 台湾 01"),
		node("🇸🇬 新加坡 01"),
		node("🇬🇧 英国 01"),
		node("🇷🇺 俄罗斯 01"),
	}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	for _, want := range []string{"🇺🇸 美国节点", "🇹🇼 台湾节点", "🇸🇬 新加坡节点", "🇬🇧 英国节点", "🇷🇺 俄罗斯节点"} {
		findGroup(t, groups, want)
	}
	// 无未知节点 → 不生成其他节点组
	for _, g := range groups {
		if g["name"] == GroupOther {
			t.Error("unexpected 🌐 其他节点 group")
		}
	}
}

func TestBuildDuplicateNodeNames(t *testing.T) {
	nodes := []map[string]any{
		node("🇭🇰 香港 01"),
		node("🇭🇰 香港 01"), // 重名
		node("🇯🇵 日本 01"),
	}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	hk := findGroup(t, groups, "🇭🇰 香港节点")
	if got := proxies(hk); !reflect.DeepEqual(got, []string{"🇭🇰 香港 01"}) {
		t.Errorf("HK proxies = %v, want deduped [🇭🇰 香港 01]", got)
	}
	auto := findGroup(t, groups, GroupAuto)
	if got := proxies(auto); !reflect.DeepEqual(got, []string{"🇭🇰 香港 01", "🇯🇵 日本 01"}) {
		t.Errorf("auto proxies = %v, want deduped", got)
	}
}

func TestBuildEmptyNodes(t *testing.T) {
	groups, err := Build(nil)
	if err != nil {
		t.Fatalf("Build(nil) error: %v", err)
	}
	if got := groupNames(groups); !reflect.DeepEqual(got, []string{GroupManual, GroupAuto, "DIRECT", "REJECT"}) {
		t.Errorf("groups = %v, want [%s %s DIRECT REJECT]", got, GroupManual, GroupAuto)
	}
	manual := findGroup(t, groups, GroupManual)
	if got := proxies(manual); !reflect.DeepEqual(got, []string{"DIRECT", GroupAuto}) {
		t.Errorf("manual proxies = %v, want [DIRECT %s]", got, GroupAuto)
	}
	auto := findGroup(t, groups, GroupAuto)
	if got := proxies(auto); len(got) != 0 {
		t.Errorf("auto proxies = %v, want empty", got)
	}
}

func TestBuildOnlyUnknown(t *testing.T) {
	nodes := []map[string]any{node("无 emoji 节点"), node("🇨🇳 大陆 01")}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	other := findGroup(t, groups, GroupOther)
	if got := proxies(other); !reflect.DeepEqual(got, []string{"无 emoji 节点", "🇨🇳 大陆 01"}) {
		t.Errorf("other proxies = %v", got)
	}
	// 手动选择应包含其他节点组
	manual := findGroup(t, groups, GroupManual)
	if got := proxies(manual); !reflect.DeepEqual(got, []string{"DIRECT", GroupAuto, GroupOther}) {
		t.Errorf("manual proxies = %v, want [DIRECT %s %s]", got, GroupAuto, GroupOther)
	}
}

func TestBuildSkipsNonStringName(t *testing.T) {
	nodes := []map[string]any{
		{"name": "🇭🇰 香港 01"},
		{"name": 123},
		{"name": ""},
	}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	hk := findGroup(t, groups, "🇭🇰 香港节点")
	if got := proxies(hk); !reflect.DeepEqual(got, []string{"🇭🇰 香港 01"}) {
		t.Errorf("HK proxies = %v", got)
	}
}

func TestBuildEmojiInMiddleOfName(t *testing.T) {
	// emoji 不一定在名字开头，取名字中第一个已知国旗
	nodes := []map[string]any{node("SS-🇭🇰-01")}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	findGroup(t, groups, "🇭🇰 香港节点")
}
