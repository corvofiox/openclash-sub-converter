package store

import (
	"strings"
	"testing"
)

func TestCreateRuleSet(t *testing.T) {
	s := newTestStore(t)
	rs, err := s.CreateRuleSet("cn-domains", "https://example.com/cn.yaml", "domain", "yaml", true)
	if err != nil {
		t.Fatalf("CreateRuleSet: %v", err)
	}
	if len(rs.ID) != 12 || !isHex12(rs.ID) {
		t.Errorf("ID = %q, want 12 hex chars", rs.ID)
	}
	if rs.Name != "cn-domains" || rs.URL != "https://example.com/cn.yaml" ||
		rs.Behavior != "domain" || rs.Format != "yaml" || !rs.Enabled {
		t.Errorf("rs = %+v", rs)
	}
	if got, ok := s.GetRuleSet(rs.ID); !ok || got.ID != rs.ID {
		t.Errorf("GetRuleSet(%q) 未找到", rs.ID)
	}
}

func TestCreateRuleSetValidation(t *testing.T) {
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
		_, err := s.CreateRuleSet(tc.name, tc.url, tc.behavior, tc.format, true)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("CreateRuleSet(%q,%q,%q,%q) 期望成功: %v", tc.name, tc.url, tc.behavior, tc.format, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("CreateRuleSet(%q,%q,%q,%q) 期望报错 %q, got nil", tc.name, tc.url, tc.behavior, tc.format, tc.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("err = %q, want 包含 %q", err.Error(), tc.wantErr)
		}
	}
	// 8 个预置 + 1 条合法基线入库（非法用例全部被拒）
	if n := len(s.ListRuleSets()); n != 9 {
		t.Errorf("ruleSets = %d, want 9（8 预置 + 仅合法基线入库）", n)
	}
}

func TestUpdateRuleSet(t *testing.T) {
	s := newTestStore(t)
	rs, _ := s.CreateRuleSet("cn", "https://e.com/cn.yaml", "domain", "yaml", true)

	name := "cn-v2"
	behavior := "classical"
	updated, err := s.UpdateRuleSet(rs.ID, RuleSetPatch{Name: &name, Behavior: &behavior})
	if err != nil {
		t.Fatalf("UpdateRuleSet: %v", err)
	}
	if updated.Name != "cn-v2" || updated.Behavior != "classical" {
		t.Errorf("updated = %+v, want name/behavior 更新", updated)
	}
	if updated.URL != "https://e.com/cn.yaml" || updated.Format != "yaml" || !updated.Enabled {
		t.Errorf("nil 字段应保留: url=%q format=%q enabled=%v", updated.URL, updated.Format, updated.Enabled)
	}

	// 非法 behavior 报错且不落库
	bad := "regexp"
	if _, err := s.UpdateRuleSet(rs.ID, RuleSetPatch{Behavior: &bad}); err == nil {
		t.Error("非法 behavior: want error, got nil")
	}
	if got, _ := s.GetRuleSet(rs.ID); got.Behavior != "classical" {
		t.Errorf("失败更新后 behavior = %q, want 保持 classical", got.Behavior)
	}

	// 空 URL 报错
	empty := ""
	if _, err := s.UpdateRuleSet(rs.ID, RuleSetPatch{URL: &empty}); err == nil {
		t.Error("URL 指向空串: want error, got nil")
	}

	// 不存在
	if _, err := s.UpdateRuleSet("nope", RuleSetPatch{}); err == nil {
		t.Error("UpdateRuleSet(nonexistent): want error, got nil")
	}
}

func TestDeleteRuleSet(t *testing.T) {
	s := newTestStore(t)
	rs, _ := s.CreateRuleSet("cn", "https://e.com/cn.yaml", "domain", "yaml", true)
	if err := s.DeleteRuleSet(rs.ID); err != nil {
		t.Fatalf("DeleteRuleSet: %v", err)
	}
	if _, ok := s.GetRuleSet(rs.ID); ok {
		t.Error("删除后仍 found")
	}
	if err := s.DeleteRuleSet(rs.ID); err == nil {
		t.Error("重复删除: want error, got nil")
	}
}

func TestRuleSetPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	rs, err := s1.CreateRuleSet("cn", "https://e.com/cn.yaml", "ipcidr", "text", false)
	if err != nil {
		t.Fatalf("CreateRuleSet: %v", err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	got, ok := s2.GetRuleSet(rs.ID)
	if !ok {
		t.Fatal("重新打开后规则集丢失")
	}
	if got.Name != "cn" || got.Behavior != "ipcidr" || got.Format != "text" || got.Enabled {
		t.Errorf("回读 = %+v", got)
	}
}