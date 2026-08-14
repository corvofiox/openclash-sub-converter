package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeedPresetsOnFirstStart 首次启动（空目录）→ 种入 8 个预置规则集，
// 名称/URL/behavior/format/enabled 字段全部正确，ID 为 12 位 hex。
func TestSeedPresetsOnFirstStart(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ruleSets := s.ListRuleSets()
	if len(ruleSets) != len(presetRuleSetDefs) {
		t.Fatalf("ruleSets = %d, want %d", len(ruleSets), len(presetRuleSetDefs))
	}
	byName := map[string]RuleSet{}
	for _, rs := range ruleSets {
		if len(rs.ID) != 12 || !isHex12(rs.ID) {
			t.Errorf("ID = %q, want 12 hex chars", rs.ID)
		}
		if rs.Behavior != BehaviorDomain || rs.Format != FormatText {
			t.Errorf("%s: behavior=%q format=%q, want domain/text", rs.Name, rs.Behavior, rs.Format)
		}
		if rs.Enabled {
			t.Errorf("%s: enabled=true, want 默认禁用", rs.Name)
		}
		if rs.CreatedAt == "" || rs.UpdatedAt == "" {
			t.Errorf("%s: 时间戳为空", rs.Name)
		}
		byName[rs.Name] = rs
	}
	// 与定义逐条对照（名称 + 完整 URL 前缀）
	for _, def := range presetRuleSetDefs {
		rs, ok := byName[def.name]
		if !ok {
			t.Errorf("缺少预置规则集 %q", def.name)
			continue
		}
		if rs.URL != def.url {
			t.Errorf("%s: url = %q, want %q", def.name, rs.URL, def.url)
		}
		if !strings.HasPrefix(rs.URL, presetRuleSetBaseURL) {
			t.Errorf("%s: url 缺少 ACL4SSR 前缀: %q", def.name, rs.URL)
		}
	}
	// 8 个 ID 互不重复
	seen := map[string]bool{}
	for _, rs := range ruleSets {
		if seen[rs.ID] {
			t.Errorf("ID 重复: %s", rs.ID)
		}
		seen[rs.ID] = true
	}
}

// TestSeedPresetsIdempotent 二次启动（文件已存在）→ 不重复种入、不覆盖用户修改。
func TestSeedPresetsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	first := s1.ListRuleSets()
	if len(first) != 8 {
		t.Fatalf("首次 ruleSets = %d, want 8", len(first))
	}
	// 用户修改一个预置：改名 + 启用
	name := "Netflix 改"
	enabled := true
	updated, err := s1.UpdateRuleSet(first[0].ID, RuleSetPatch{Name: &name, Enabled: &enabled})
	if err != nil {
		t.Fatalf("UpdateRuleSet: %v", err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	second := s2.ListRuleSets()
	if len(second) != 8 {
		t.Fatalf("二次启动 ruleSets = %d, want 8（不得重复种入）", len(second))
	}
	// 用户修改保留，ID 稳定
	got, ok := s2.GetRuleSet(first[0].ID)
	if !ok {
		t.Fatal("二次启动后预置 ID 丢失")
	}
	if got.Name != "Netflix 改" || !got.Enabled {
		t.Errorf("用户修改被覆盖: %+v", got)
	}
	if updated.URL != got.URL {
		t.Errorf("URL 变化: %q → %q", updated.URL, got.URL)
	}
}

// TestSeedPresetsNoResurrection 用户删除全部规则集后重启 → 不复活（空列表不种入）。
func TestSeedPresetsNoResurrection(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	for _, rs := range s1.ListRuleSets() {
		if err := s1.DeleteRuleSet(rs.ID); err != nil {
			t.Fatalf("DeleteRuleSet(%s): %v", rs.ID, err)
		}
	}
	if n := len(s1.ListRuleSets()); n != 0 {
		t.Fatalf("删光后 ruleSets = %d, want 0", n)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	if n := len(s2.ListRuleSets()); n != 0 {
		t.Errorf("重启后 ruleSets = %d, want 0（删光不复活）", n)
	}
}

// TestSeedPresetsCorruptNoSeed 损坏恢复路径（.bak 备份后空态）不触发种入。
func TestSeedPresetsCorruptNoSeed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rulesets.json"), []byte("{broken!!!"), 0o600); err != nil {
		t.Fatalf("写坏文件: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New 遇损坏文件不应报错: %v", err)
	}
	if n := len(s.ListRuleSets()); n != 0 {
		t.Errorf("损坏恢复后 ruleSets = %d, want 0（不种入）", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "rulesets.json.bak")); err != nil {
		t.Errorf("损坏文件未备份: %v", err)
	}
	// 恢复后仍可正常创建规则集（后续写入不受影响）
	if _, err := s.CreateRuleSet("cn", "https://e.com/cn.yaml", "domain", "yaml", true); err != nil {
		t.Errorf("损坏恢复后 CreateRuleSet: %v", err)
	}
}

// TestSeedPresetsVersionMismatchNoSeed version 不匹配空态不触发种入。
func TestSeedPresetsVersionMismatchNoSeed(t *testing.T) {
	dir := t.TempDir()
	data := `{"version":99,"rulesets":[]}`
	if err := os.WriteFile(filepath.Join(dir, "rulesets.json"), []byte(data), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if n := len(s.ListRuleSets()); n != 0 {
		t.Errorf("version 不匹配后 ruleSets = %d, want 0（不种入）", n)
	}
}

// TestSeedPresetsAreOrdinary 预置规则集是普通规则集：可 Update（改名/启用）、
// 可 Delete，无特殊标记。
func TestSeedPresetsAreOrdinary(t *testing.T) {
	s := newTestStore(t)
	ruleSets := s.ListRuleSets()
	if len(ruleSets) != 8 {
		t.Fatalf("ruleSets = %d, want 8", len(ruleSets))
	}
	target := ruleSets[0]

	name := "自定名"
	behavior := "classical"
	updated, err := s.UpdateRuleSet(target.ID, RuleSetPatch{Name: &name, Behavior: &behavior})
	if err != nil {
		t.Fatalf("UpdateRuleSet: %v", err)
	}
	if updated.Name != "自定名" || updated.Behavior != "classical" {
		t.Errorf("updated = %+v, want 更新生效", updated)
	}
	if updated.URL != target.URL {
		t.Errorf("URL 不应被 Update 清空: %q", updated.URL)
	}

	if err := s.DeleteRuleSet(target.ID); err != nil {
		t.Fatalf("DeleteRuleSet: %v", err)
	}
	if _, ok := s.GetRuleSet(target.ID); ok {
		t.Error("删除后仍 found")
	}
	if n := len(s.ListRuleSets()); n != 7 {
		t.Errorf("删除后 ruleSets = %d, want 7", n)
	}
}

// TestSeedPresetsFileFormat 落盘权限 0600、JSON 格式与现有数据文件一致
// （{"version":1,"rulesets":[...]}，MarshalIndent 美化，字段齐全）。
func TestSeedPresetsFileFormat(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir); err != nil {
		t.Fatalf("New: %v", err)
	}
	path := filepath.Join(dir, "rulesets.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat rulesets.json: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("rulesets.json mode = %v, want 0600", perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 rulesets.json: %v", err)
	}
	var f ruleSetsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if f.Version != dataVersion {
		t.Errorf("version = %d, want %d", f.Version, dataVersion)
	}
	if len(f.RuleSets) != 8 {
		t.Errorf("ruleSets = %d, want 8", len(f.RuleSets))
	}
	for _, rs := range f.RuleSets {
		if rs.ID == "" || rs.Name == "" || rs.URL == "" || rs.CreatedAt == "" || rs.UpdatedAt == "" {
			t.Errorf("字段缺失: %+v", rs)
		}
	}
	// 磁盘 JSON 与普通 CRUD 落盘同构：version 字段存在且美化缩进
	if !strings.Contains(string(raw), `"version": 1`) {
		t.Errorf("缺少 version:1 字段: %s", raw)
	}
	if !strings.Contains(string(raw), `"rulesets"`) {
		t.Errorf("缺少 rulesets 段: %s", raw)
	}
}

// TestLegacyTemplatesMigration（R5）旧版 templates.json 存在且 rulesets.json
// 不存在 → 内存态采用旧数据（不种入预置、不改写旧文件）；首次写操作才落盘
// 为 rulesets.json（新格式），旧文件保留。
func TestLegacyTemplatesMigration(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"version":1,"templates":[{"id":"abc123def456","name":"旧规则","url":"https://e.com/old.yaml","behavior":"domain","format":"text","enabled":true,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(dir, "templates.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("写旧文件: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ruleSets := s.ListRuleSets()
	if len(ruleSets) != 1 || ruleSets[0].Name != "旧规则" || ruleSets[0].URL != "https://e.com/old.yaml" || !ruleSets[0].Enabled {
		t.Fatalf("迁移后 ruleSets = %+v, want 旧数据原样", ruleSets)
	}
	// 旧文件保留、新文件未生成（未主动改写）
	if _, err := os.Stat(filepath.Join(dir, "templates.json")); err != nil {
		t.Errorf("旧 templates.json 应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rulesets.json")); !os.IsNotExist(err) {
		t.Errorf("迁移阶段不应落盘 rulesets.json: %v", err)
	}
	// 首次写操作落盘为 rulesets.json（新格式），且不触发预置种入
	if _, err := s.CreateRuleSet("新规则", "https://e.com/new.yaml", "domain", "yaml", true); err != nil {
		t.Fatalf("CreateRuleSet: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "rulesets.json"))
	if err != nil {
		t.Fatalf("读 rulesets.json: %v", err)
	}
	var f ruleSetsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if len(f.RuleSets) != 2 {
		t.Errorf("rulesets.json 内容 = %d 条, want 2（旧 1 + 新 1）", len(f.RuleSets))
	}
	// 旧文件仍在
	if _, err := os.Stat(filepath.Join(dir, "templates.json")); err != nil {
		t.Errorf("写入后旧 templates.json 仍应保留: %v", err)
	}
	// 重启后从 rulesets.json 读（旧文件不再参与）
	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	if n := len(s2.ListRuleSets()); n != 2 {
		t.Errorf("重启后 ruleSets = %d, want 2", n)
	}
}

// TestLegacyTemplatesMigrationNoSeed 旧 templates.json 存在 → 即使旧文件为空
// 列表也不种入预置（迁移路径与空态区分开）。
func TestLegacyTemplatesMigrationNoSeed(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"version":1,"templates":[]}`
	if err := os.WriteFile(filepath.Join(dir, "templates.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("写旧文件: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if n := len(s.ListRuleSets()); n != 0 {
		t.Errorf("旧空文件迁移后 ruleSets = %d, want 0（不种入）", n)
	}
}