package store

import (
	"strings"
	"testing"
)

func TestCreateTemplate(t *testing.T) {
	s := newTestStore(t)
	tpl, err := s.CreateTemplate("cn-domains", "https://example.com/cn.yaml", "domain", "yaml", true)
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if len(tpl.ID) != 12 || !isHex12(tpl.ID) {
		t.Errorf("ID = %q, want 12 hex chars", tpl.ID)
	}
	if tpl.Name != "cn-domains" || tpl.URL != "https://example.com/cn.yaml" ||
		tpl.Behavior != "domain" || tpl.Format != "yaml" || !tpl.Enabled {
		t.Errorf("tpl = %+v", tpl)
	}
	if got, ok := s.GetTemplate(tpl.ID); !ok || got.ID != tpl.ID {
		t.Errorf("GetTemplate(%q) 未找到", tpl.ID)
	}
}

func TestCreateTemplateValidation(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name     string
		url      string
		behavior string
		format   string
		wantErr  string
	}{
		{"", "https://e.com", "domain", "yaml", "name"},
		{"x", "", "domain", "yaml", "url"},
		{"x", "https://e.com", "bad", "yaml", "behavior"},
		{"x", "https://e.com", "domain", "text", ""},      // 合法基线
		{"x", "https://e.com", "ipcidr", "mrs", "format"}, // mrs 不在本工具约束内
		{"x", "https://e.com", "classical", "", "format"}, // 空 format 非法
	}
	for _, tc := range cases {
		_, err := s.CreateTemplate(tc.name, tc.url, tc.behavior, tc.format, true)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("CreateTemplate(%q,%q,%q,%q) 期望成功: %v", tc.name, tc.url, tc.behavior, tc.format, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("CreateTemplate(%q,%q,%q,%q) 期望报错 %q, got nil", tc.name, tc.url, tc.behavior, tc.format, tc.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("err = %q, want 包含 %q", err.Error(), tc.wantErr)
		}
	}
	// 8 个预置 + 1 条合法基线入库（非法用例全部被拒）
	if n := len(s.ListTemplates()); n != 9 {
		t.Errorf("templates = %d, want 9（8 预置 + 仅合法基线入库）", n)
	}
}

func TestUpdateTemplate(t *testing.T) {
	s := newTestStore(t)
	tpl, _ := s.CreateTemplate("cn", "https://e.com/cn.yaml", "domain", "yaml", true)

	name := "cn-v2"
	behavior := "classical"
	updated, err := s.UpdateTemplate(tpl.ID, TemplatePatch{Name: &name, Behavior: &behavior})
	if err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}
	if updated.Name != "cn-v2" || updated.Behavior != "classical" {
		t.Errorf("updated = %+v, want name/behavior 更新", updated)
	}
	if updated.URL != "https://e.com/cn.yaml" || updated.Format != "yaml" || !updated.Enabled {
		t.Errorf("nil 字段应保留: url=%q format=%q enabled=%v", updated.URL, updated.Format, updated.Enabled)
	}

	// 非法 behavior 报错且不落库
	bad := "regexp"
	if _, err := s.UpdateTemplate(tpl.ID, TemplatePatch{Behavior: &bad}); err == nil {
		t.Error("非法 behavior: want error, got nil")
	}
	if got, _ := s.GetTemplate(tpl.ID); got.Behavior != "classical" {
		t.Errorf("失败更新后 behavior = %q, want 保持 classical", got.Behavior)
	}

	// 空 URL 报错
	empty := ""
	if _, err := s.UpdateTemplate(tpl.ID, TemplatePatch{URL: &empty}); err == nil {
		t.Error("URL 指向空串: want error, got nil")
	}

	// 不存在
	if _, err := s.UpdateTemplate("nope", TemplatePatch{}); err == nil {
		t.Error("UpdateTemplate(nonexistent): want error, got nil")
	}
}

func TestDeleteTemplate(t *testing.T) {
	s := newTestStore(t)
	tpl, _ := s.CreateTemplate("cn", "https://e.com/cn.yaml", "domain", "yaml", true)
	if err := s.DeleteTemplate(tpl.ID); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if _, ok := s.GetTemplate(tpl.ID); ok {
		t.Error("删除后仍 found")
	}
	if err := s.DeleteTemplate(tpl.ID); err == nil {
		t.Error("重复删除: want error, got nil")
	}
}

func TestTemplatePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	tpl, err := s1.CreateTemplate("cn", "https://e.com/cn.yaml", "ipcidr", "text", false)
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	got, ok := s2.GetTemplate(tpl.ID)
	if !ok {
		t.Fatal("重新打开后模板丢失")
	}
	if got.Name != "cn" || got.Behavior != "ipcidr" || got.Format != "text" || got.Enabled {
		t.Errorf("回读 = %+v", got)
	}
}
