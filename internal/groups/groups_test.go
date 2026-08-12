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
		node("新加坡 01"),   // 无 emoji 但文字识别 → 新加坡组
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
	// R2：手动组 = 自动选择 + 地区组名（出现序）+ 其他节点组 + 全部节点名（输入顺序）+ 直连组 + 拒绝组
	wantManual := []string{GroupAuto, "香港节点", "日本节点", "新加坡节点", GroupOther, "🇭🇰 香港 01", "🇭🇰 香港 02", "🇯🇵 日本 01", "新加坡 01", "🇨🇳 大陆 01", GroupDirect, GroupReject}
	if got := proxies(manual); !reflect.DeepEqual(got, wantManual) {
		t.Errorf("manual proxies = %v, want %v", got, wantManual)
	}

	auto := findGroup(t, groups, GroupAuto)
	wantAuto := []string{"🇭🇰 香港 01", "🇭🇰 香港 02", "🇯🇵 日本 01", "新加坡 01", "🇨🇳 大陆 01"}
	if got := proxies(auto); !reflect.DeepEqual(got, wantAuto) {
		t.Errorf("auto proxies = %v, want %v", got, wantAuto)
	}

	hk := findGroup(t, groups, "香港节点")
	if hk["type"] != "url-test" || hk["url"] != TestURL || hk["interval"] != TestInterval {
		t.Errorf("HK group fields = %v, want url-test %s %d", hk, TestURL, TestInterval)
	}
	if got := proxies(hk); !reflect.DeepEqual(got, []string{"🇭🇰 香港 01", "🇭🇰 香港 02"}) {
		t.Errorf("HK proxies = %v", got)
	}

	jp := findGroup(t, groups, "日本节点")
	if got := proxies(jp); !reflect.DeepEqual(got, []string{"🇯🇵 日本 01"}) {
		t.Errorf("JP proxies = %v", got)
	}

	sg := findGroup(t, groups, "新加坡节点")
	if got := proxies(sg); !reflect.DeepEqual(got, []string{"新加坡 01"}) {
		t.Errorf("SG proxies = %v", got)
	}

	other := findGroup(t, groups, GroupOther)
	if got := proxies(other); !reflect.DeepEqual(got, []string{"🇨🇳 大陆 01"}) {
		t.Errorf("other proxies = %v", got)
	}

	// R1：直连/拒绝组存在（type=select，proxies 恰为 [DIRECT]/[REJECT]），
	// 声明位置在其他节点组之后（组列表尾部）
	direct := findGroup(t, groups, GroupDirect)
	if direct["type"] != "select" || !reflect.DeepEqual(proxies(direct), []string{"DIRECT"}) {
		t.Errorf("直连 group = %v, want type=select proxies=[DIRECT]", direct)
	}
	reject := findGroup(t, groups, GroupReject)
	if reject["type"] != "select" || !reflect.DeepEqual(proxies(reject), []string{"REJECT"}) {
		t.Errorf("拒绝 group = %v, want type=select proxies=[REJECT]", reject)
	}
	names := groupNames(groups)
	if names[len(names)-1] != GroupReject || names[len(names)-2] != GroupDirect {
		t.Errorf("直连/拒绝应在组列表尾部（其他节点组之后），实际尾部 = %v", names[len(names)-2:])
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
	for _, want := range []string{"美国节点", "台湾节点", "新加坡节点", "英国节点", "俄罗斯节点"} {
		findGroup(t, groups, want)
	}
	// 无未知节点 → 不生成其他节点组
	for _, g := range groups {
		if g["name"] == GroupOther {
			t.Error("unexpected 其他节点 group")
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
	hk := findGroup(t, groups, "香港节点")
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
	// R1：无节点时也生成直连/拒绝组（用户明确要求）
	if got := groupNames(groups); !reflect.DeepEqual(got, []string{GroupManual, GroupAuto, GroupDirect, GroupReject}) {
		t.Errorf("groups = %v, want [%s %s %s %s]", got, GroupManual, GroupAuto, GroupDirect, GroupReject)
	}
	manual := findGroup(t, groups, GroupManual)
	if got := proxies(manual); !reflect.DeepEqual(got, []string{GroupAuto, GroupDirect, GroupReject}) {
		t.Errorf("manual proxies = %v, want [%s %s %s]", got, GroupAuto, GroupDirect, GroupReject)
	}
	auto := findGroup(t, groups, GroupAuto)
	if got := proxies(auto); len(got) != 0 {
		t.Errorf("auto proxies = %v, want empty", got)
	}
	if got := proxies(findGroup(t, groups, GroupDirect)); !reflect.DeepEqual(got, []string{"DIRECT"}) {
		t.Errorf("直连 proxies = %v, want [DIRECT]", got)
	}
	if got := proxies(findGroup(t, groups, GroupReject)); !reflect.DeepEqual(got, []string{"REJECT"}) {
		t.Errorf("拒绝 proxies = %v, want [REJECT]", got)
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
	// R2：手动组 = 自动选择 + 其他节点组 + 全部节点名 + 直连/拒绝
	if got := proxies(manual); !reflect.DeepEqual(got, []string{GroupAuto, GroupOther, "无 emoji 节点", "🇨🇳 大陆 01", GroupDirect, GroupReject}) {
		t.Errorf("manual proxies = %v, want [%s %s %s %s]", got, GroupAuto, GroupOther, GroupDirect, GroupReject)
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
	hk := findGroup(t, groups, "香港节点")
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
	findGroup(t, groups, "香港节点")
}

// TestBuildManualAllNodesOrder（R2 验收）：3 节点（港/日/美 各 1）手动选择组
// proxies = [自动选择, 3 地区组名（出现序）, 3 节点名（输入顺序）, 直连组, 拒绝组]。
func TestBuildManualAllNodesOrder(t *testing.T) {
	nodes := []map[string]any{
		node("🇭🇰 香港-x"),
		node("🇯🇵 日本-y"),
		node("🇺🇸 美国-z"),
	}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	manual := findGroup(t, groups, GroupManual)
	want := []string{GroupAuto, "香港节点", "日本节点", "美国节点", "🇭🇰 香港-x", "🇯🇵 日本-y", "🇺🇸 美国-z", GroupDirect, GroupReject}
	if got := proxies(manual); !reflect.DeepEqual(got, want) {
		t.Errorf("manual proxies = %v, want %v", got, want)
	}
	// 含不可识别节点时：其他节点组插在地区组名之后、节点名之前
	nodes = append(nodes, node("unknown-node"))
	groups, err = Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	manual = findGroup(t, groups, GroupManual)
	want = []string{GroupAuto, "香港节点", "日本节点", "美国节点", GroupOther, "🇭🇰 香港-x", "🇯🇵 日本-y", "🇺🇸 美国-z", "unknown-node", GroupDirect, GroupReject}
	if got := proxies(manual); !reflect.DeepEqual(got, want) {
		t.Errorf("manual proxies with other = %v, want %v", got, want)
	}
}
