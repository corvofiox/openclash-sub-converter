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

	// R7：漏网之鱼组——紧跟手动选择组声明（组列表第 2 位），type=select，
	// proxies = [手动选择, ...手动选择组 proxies[1:]]（首位引用手动选择组，
	// 不含自动选择）。
	leak := findGroup(t, groups, GroupLeak)
	if leak["type"] != "select" {
		t.Errorf("漏网之鱼 type = %v, want select", leak["type"])
	}
	wantLeak := append([]string{GroupManual}, wantManual[1:]...)
	if got := proxies(leak); !reflect.DeepEqual(got, wantLeak) {
		t.Errorf("漏网之鱼 proxies = %v, want %v", got, wantLeak)
	}
	if gnames := groupNames(groups); len(gnames) < 2 || gnames[1] != GroupLeak {
		t.Errorf("漏网之鱼应在组列表第 2 位（手动选择之后），实际组序 = %v", gnames)
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
	// R1：无节点时也生成直连/拒绝组（用户明确要求）；R7：漏网之鱼组同样生成
	if got := groupNames(groups); !reflect.DeepEqual(got, []string{GroupManual, GroupLeak, GroupAuto, GroupDirect, GroupReject}) {
		t.Errorf("groups = %v, want [%s %s %s %s %s]", got, GroupManual, GroupLeak, GroupAuto, GroupDirect, GroupReject)
	}
	manual := findGroup(t, groups, GroupManual)
	if got := proxies(manual); !reflect.DeepEqual(got, []string{GroupAuto, GroupDirect, GroupReject}) {
		t.Errorf("manual proxies = %v, want [%s %s %s]", got, GroupAuto, GroupDirect, GroupReject)
	}
	// A4：空节点列表仍生成漏网之鱼，proxies = [手动选择, 直连, 拒绝]
	leak := findGroup(t, groups, GroupLeak)
	if got := proxies(leak); !reflect.DeepEqual(got, []string{GroupManual, GroupDirect, GroupReject}) {
		t.Errorf("漏网之鱼 proxies = %v, want [%s %s %s]", got, GroupManual, GroupDirect, GroupReject)
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
	// R7：漏网之鱼 = 手动选择 + 其他节点组 + 全部节点名 + 直连/拒绝（无自动选择）
	leak := findGroup(t, groups, GroupLeak)
	if got := proxies(leak); !reflect.DeepEqual(got, []string{GroupManual, GroupOther, "无 emoji 节点", "🇨🇳 大陆 01", GroupDirect, GroupReject}) {
		t.Errorf("漏网之鱼 proxies = %v, want [%s %s %s %s %s %s]", got, GroupManual, GroupOther, "无 emoji 节点", "🇨🇳 大陆 01", GroupDirect, GroupReject)
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
	// R7：漏网之鱼 = [手动选择, ...手动组 proxies[1:]]（无自动选择）
	leak := findGroup(t, groups, GroupLeak)
	wantLeak := append([]string{GroupManual}, want[1:]...)
	if got := proxies(leak); !reflect.DeepEqual(got, wantLeak) {
		t.Errorf("漏网之鱼 proxies = %v, want %v", got, wantLeak)
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
	leak = findGroup(t, groups, GroupLeak)
	wantLeak = append([]string{GroupManual}, want[1:]...)
	if got := proxies(leak); !reflect.DeepEqual(got, wantLeak) {
		t.Errorf("漏网之鱼 proxies with other = %v, want %v", got, wantLeak)
	}
}

// duplicateStrings 返回 xs 中的重复元素（测试辅助）。
func duplicateStrings(xs []string) []string {
	seen := make(map[string]bool, len(xs))
	var dups []string
	for _, x := range xs {
		if seen[x] {
			dups = append(dups, x)
		}
		seen[x] = true
	}
	return dups
}

// TestBuildGroupNameCollision（FIX-1）：节点名恰为固定组名（手动选择/漏网之鱼）或动态
// 地区组名（香港节点）时，组名优先、节点追加 " (N)" 后缀避让——输出节点名 ∪ 组名
// 无重复（mihomo 共享命名空间语义），改名节点在手动选择/漏网之鱼/自动选择/地区组/
// 其他节点组中被一致引用，其余节点不受影响。
func TestBuildGroupNameCollision(t *testing.T) {
	nodes := []map[string]any{
		node("手动选择"),     // 撞固定组名
		node("漏网之鱼"),     // 撞固定组名
		node("香港节点"),     // 撞动态地区组名（自身也归入香港地区）
		node("🇭🇰 香港 01"), // 正常节点，不受影响
		node("🇯🇵 日本 01"), // 正常节点，不受影响
	}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	// 1. 节点名 ∪ 组名 无重复（mihomo proxies 与 proxy-groups 共享命名空间）
	all := make([]string, 0, len(nodes)+len(groups))
	for _, n := range nodes {
		all = append(all, n["name"].(string))
	}
	for _, g := range groups {
		all = append(all, g["name"].(string))
	}
	if dups := duplicateStrings(all); len(dups) > 0 {
		t.Errorf("命名空间重复（节点名∪组名）: %v (all=%v)", dups, all)
	}

	// 2. 节点条目本身被改名（proxies 段与组引用一致）
	gotNames := make([]string, 0, len(nodes))
	for _, n := range nodes {
		gotNames = append(gotNames, n["name"].(string))
	}
	wantNames := []string{"手动选择 (2)", "漏网之鱼 (2)", "香港节点 (2)", "🇭🇰 香港 01", "🇯🇵 日本 01"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Errorf("nodes names = %v, want %v", gotNames, wantNames)
	}

	// 3. 各组引用与改后名一致
	manual := findGroup(t, groups, GroupManual)
	wantManual := []string{GroupAuto, "香港节点", "日本节点", GroupOther,
		"手动选择 (2)", "漏网之鱼 (2)", "香港节点 (2)", "🇭🇰 香港 01", "🇯🇵 日本 01",
		GroupDirect, GroupReject}
	if got := proxies(manual); !reflect.DeepEqual(got, wantManual) {
		t.Errorf("manual proxies = %v, want %v", got, wantManual)
	}
	leak := findGroup(t, groups, GroupLeak)
	wantLeak := append([]string{GroupManual}, wantManual[1:]...)
	if got := proxies(leak); !reflect.DeepEqual(got, wantLeak) {
		t.Errorf("leak proxies = %v, want %v", got, wantLeak)
	}
	auto := findGroup(t, groups, GroupAuto)
	wantAuto := []string{"手动选择 (2)", "漏网之鱼 (2)", "香港节点 (2)", "🇭🇰 香港 01", "🇯🇵 日本 01"}
	if got := proxies(auto); !reflect.DeepEqual(got, wantAuto) {
		t.Errorf("auto proxies = %v, want %v", got, wantAuto)
	}
	hk := findGroup(t, groups, "香港节点")
	if got := proxies(hk); !reflect.DeepEqual(got, []string{"香港节点 (2)", "🇭🇰 香港 01"}) {
		t.Errorf("香港节点 proxies = %v, want [香港节点 (2) 🇭🇰 香港 01]", got)
	}
	jp := findGroup(t, groups, "日本节点")
	if got := proxies(jp); !reflect.DeepEqual(got, []string{"🇯🇵 日本 01"}) {
		t.Errorf("日本节点 proxies = %v, want [🇯🇵 日本 01]", got)
	}
	other := findGroup(t, groups, GroupOther)
	if got := proxies(other); !reflect.DeepEqual(got, []string{"手动选择 (2)", "漏网之鱼 (2)"}) {
		t.Errorf("其他节点 proxies = %v, want [手动选择 (2) 漏网之鱼 (2)]", got)
	}

	// 4. 引用一致性：每个组引用要么是节点名/组名，要么是内置出站名
	valid := map[string]bool{"DIRECT": true, "REJECT": true}
	for _, n := range nodes {
		valid[n["name"].(string)] = true
	}
	for _, gn := range groupNames(groups) {
		valid[gn] = true
	}
	for _, g := range groups {
		for _, ref := range proxies(g) {
			if !valid[ref] {
				t.Errorf("group %v 引用 %q 不在节点名/组名集合中", g["name"], ref)
			}
		}
	}
}

// TestBuildGroupNameCollisionSuffixBump（FIX-1 边界）：改名候选 " (2)" 已被现有节点占用
// 时递增到 " (3)"，不与任何已用名冲突。
func TestBuildGroupNameCollisionSuffixBump(t *testing.T) {
	nodes := []map[string]any{
		node("香港节点"),     // 撞地区组名 → 候选 "香港节点 (2)" 已被下一节点占用 → "香港节点 (3)"
		node("香港节点 (2)"), // 普通节点，原样保留
		node("🇭🇰 香港 01"),
	}
	groups, err := Build(nodes)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	gotNames := make([]string, 0, len(nodes))
	for _, n := range nodes {
		gotNames = append(gotNames, n["name"].(string))
	}
	if !reflect.DeepEqual(gotNames, []string{"香港节点 (3)", "香港节点 (2)", "🇭🇰 香港 01"}) {
		t.Errorf("nodes names = %v, want [香港节点 (3) 香港节点 (2) 🇭🇰 香港 01]", gotNames)
	}
	auto := findGroup(t, groups, GroupAuto)
	if got := proxies(auto); !reflect.DeepEqual(got, []string{"香港节点 (3)", "香港节点 (2)", "🇭🇰 香港 01"}) {
		t.Errorf("auto proxies = %v", got)
	}
	hk := findGroup(t, groups, "香港节点")
	if got := proxies(hk); !reflect.DeepEqual(got, []string{"香港节点 (3)", "香港节点 (2)", "🇭🇰 香港 01"}) {
		t.Errorf("香港节点 proxies = %v", got)
	}
}
