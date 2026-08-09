package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeedPresetsOnFirstStart 首次启动（空目录）→ 种入 8 个预置模板，
// 名称/URL/behavior/format/enabled 字段全部正确，ID 为 12 位 hex。
func TestSeedPresetsOnFirstStart(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tpls := s.ListTemplates()
	if len(tpls) != len(presetTemplateDefs) {
		t.Fatalf("templates = %d, want %d", len(tpls), len(presetTemplateDefs))
	}
	byName := map[string]RuleTemplate{}
	for _, tp := range tpls {
		if len(tp.ID) != 12 || !isHex12(tp.ID) {
			t.Errorf("ID = %q, want 12 hex chars", tp.ID)
		}
		if tp.Behavior != BehaviorDomain || tp.Format != FormatText {
			t.Errorf("%s: behavior=%q format=%q, want domain/text", tp.Name, tp.Behavior, tp.Format)
		}
		if tp.Enabled {
			t.Errorf("%s: enabled=true, want 默认禁用", tp.Name)
		}
		if tp.CreatedAt == "" || tp.UpdatedAt == "" {
			t.Errorf("%s: 时间戳为空", tp.Name)
		}
		byName[tp.Name] = tp
	}
	// 与定义逐条对照（名称 + 完整 URL 前缀）
	for _, def := range presetTemplateDefs {
		tp, ok := byName[def.name]
		if !ok {
			t.Errorf("缺少预置模板 %q", def.name)
			continue
		}
		if tp.URL != def.url {
			t.Errorf("%s: url = %q, want %q", def.name, tp.URL, def.url)
		}
		if !strings.HasPrefix(tp.URL, presetTemplateBaseURL) {
			t.Errorf("%s: url 缺少 ACL4SSR 前缀: %q", def.name, tp.URL)
		}
	}
	// 8 个 ID 互不重复
	seen := map[string]bool{}
	for _, tp := range tpls {
		if seen[tp.ID] {
			t.Errorf("ID 重复: %s", tp.ID)
		}
		seen[tp.ID] = true
	}
}

// TestSeedPresetsIdempotent 二次启动（文件已存在）→ 不重复种入、不覆盖用户修改。
func TestSeedPresetsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	first := s1.ListTemplates()
	if len(first) != 8 {
		t.Fatalf("首次 templates = %d, want 8", len(first))
	}
	// 用户修改一个预置：改名 + 启用
	name := "Netflix 改"
	enabled := true
	updated, err := s1.UpdateTemplate(first[0].ID, TemplatePatch{Name: &name, Enabled: &enabled})
	if err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	second := s2.ListTemplates()
	if len(second) != 8 {
		t.Fatalf("二次启动 templates = %d, want 8（不得重复种入）", len(second))
	}
	// 用户修改保留，ID 稳定
	got, ok := s2.GetTemplate(first[0].ID)
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

// TestSeedPresetsNoResurrection 用户删除全部模板后重启 → 不复活（空列表不种入）。
func TestSeedPresetsNoResurrection(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	for _, tp := range s1.ListTemplates() {
		if err := s1.DeleteTemplate(tp.ID); err != nil {
			t.Fatalf("DeleteTemplate(%s): %v", tp.ID, err)
		}
	}
	if n := len(s1.ListTemplates()); n != 0 {
		t.Fatalf("删光后 templates = %d, want 0", n)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	if n := len(s2.ListTemplates()); n != 0 {
		t.Errorf("重启后 templates = %d, want 0（删光不复活）", n)
	}
}

// TestSeedPresetsCorruptNoSeed 损坏恢复路径（.bak 备份后空态）不触发种入。
func TestSeedPresetsCorruptNoSeed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "templates.json"), []byte("{broken!!!"), 0o600); err != nil {
		t.Fatalf("写坏文件: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New 遇损坏文件不应报错: %v", err)
	}
	if n := len(s.ListTemplates()); n != 0 {
		t.Errorf("损坏恢复后 templates = %d, want 0（不种入）", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "templates.json.bak")); err != nil {
		t.Errorf("损坏文件未备份: %v", err)
	}
	// 恢复后仍可正常创建模板（后续写入不受影响）
	if _, err := s.CreateTemplate("cn", "https://e.com/cn.yaml", "domain", "yaml", true); err != nil {
		t.Errorf("损坏恢复后 CreateTemplate: %v", err)
	}
}

// TestSeedPresetsVersionMismatchNoSeed version 不匹配空态不触发种入。
func TestSeedPresetsVersionMismatchNoSeed(t *testing.T) {
	dir := t.TempDir()
	data := `{"version":99,"templates":[]}`
	if err := os.WriteFile(filepath.Join(dir, "templates.json"), []byte(data), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if n := len(s.ListTemplates()); n != 0 {
		t.Errorf("version 不匹配后 templates = %d, want 0（不种入）", n)
	}
}

// TestSeedPresetsAreOrdinary 预置模板是普通模板：可 Update（改名/启用）、
// 可 Delete，无特殊标记。
func TestSeedPresetsAreOrdinary(t *testing.T) {
	s := newTestStore(t)
	tpls := s.ListTemplates()
	if len(tpls) != 8 {
		t.Fatalf("templates = %d, want 8", len(tpls))
	}
	target := tpls[0]

	name := "自定名"
	behavior := "classical"
	updated, err := s.UpdateTemplate(target.ID, TemplatePatch{Name: &name, Behavior: &behavior})
	if err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}
	if updated.Name != "自定名" || updated.Behavior != "classical" {
		t.Errorf("updated = %+v, want 更新生效", updated)
	}
	if updated.URL != target.URL {
		t.Errorf("URL 不应被 Update 清空: %q", updated.URL)
	}

	if err := s.DeleteTemplate(target.ID); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if _, ok := s.GetTemplate(target.ID); ok {
		t.Error("删除后仍 found")
	}
	if n := len(s.ListTemplates()); n != 7 {
		t.Errorf("删除后 templates = %d, want 7", n)
	}
}

// TestSeedPresetsFileFormat 落盘权限 0600、JSON 格式与现有数据文件一致
// （{"version":1,"templates":[...]}，MarshalIndent 美化，字段齐全）。
func TestSeedPresetsFileFormat(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir); err != nil {
		t.Fatalf("New: %v", err)
	}
	path := filepath.Join(dir, "templates.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat templates.json: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("templates.json mode = %v, want 0600", perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 templates.json: %v", err)
	}
	var f templatesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if f.Version != dataVersion {
		t.Errorf("version = %d, want %d", f.Version, dataVersion)
	}
	if len(f.Templates) != 8 {
		t.Errorf("templates = %d, want 8", len(f.Templates))
	}
	for _, tp := range f.Templates {
		if tp.ID == "" || tp.Name == "" || tp.URL == "" || tp.CreatedAt == "" || tp.UpdatedAt == "" {
			t.Errorf("字段缺失: %+v", tp)
		}
	}
	// 磁盘 JSON 与普通 CRUD 落盘同构：version 字段存在且美化缩进
	if !strings.Contains(string(raw), `"version": 1`) {
		t.Errorf("缺少 version:1 字段: %s", raw)
	}
	if !strings.Contains(string(raw), `"templates"`) {
		t.Errorf("缺少 templates 段: %s", raw)
	}
}
