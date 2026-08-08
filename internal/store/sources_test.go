package store

import (
	"strings"
	"testing"
	"time"
)

// newTestStore 用 t.TempDir() 创建 Store 并断言成功。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestCreateSource(t *testing.T) {
	s := newTestStore(t)
	src, err := s.CreateSource("机场A", "https://example.com/sub", true)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if len(src.ID) != 12 {
		t.Errorf("ID = %q, want 12 hex chars", src.ID)
	}
	if !isHex12(src.ID) {
		t.Errorf("ID = %q, want hex", src.ID)
	}
	if src.Name != "机场A" || src.URL != "https://example.com/sub" || !src.Enabled {
		t.Errorf("src = %+v, want name/url/enabled 正确", src)
	}
	if _, err := time.Parse(time.RFC3339, src.CreatedAt); err != nil {
		t.Errorf("CreatedAt = %q, want RFC3339: %v", src.CreatedAt, err)
	}
	if src.UpdatedAt != src.CreatedAt {
		t.Errorf("UpdatedAt = %q, want = CreatedAt %q", src.UpdatedAt, src.CreatedAt)
	}
	// 落盘后可读回
	if got, ok := s.GetSource(src.ID); !ok || got.ID != src.ID {
		t.Errorf("GetSource(%q) = %+v, %v; want found", src.ID, got, ok)
	}
}

func TestCreateSourceValidation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateSource("", "https://example.com", true); err == nil {
		t.Error("empty name: want error, got nil")
	}
	if _, err := s.CreateSource("x", "", true); err == nil {
		t.Error("empty url: want error, got nil")
	}
	if n := len(s.ListSources()); n != 0 {
		t.Errorf("sources = %d, want 0 (失败创建不应落库)", n)
	}
}

func TestGetSourceNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, ok := s.GetSource("nonexistent"); ok {
		t.Error("GetSource(nonexistent) = found, want not found")
	}
}

func TestListSourcesReturnsCopy(t *testing.T) {
	s := newTestStore(t)
	src, _ := s.CreateSource("a", "https://a.example.com", true)
	list := s.ListSources()
	list[0].Name = "篡改"
	if got, _ := s.GetSource(src.ID); got.Name != "a" {
		t.Errorf("内部状态被外部修改: Name = %q, want %q", got.Name, "a")
	}
}

func TestUpdateSourceNilFieldsPreserved(t *testing.T) {
	s := newTestStore(t)
	src, _ := s.CreateSource("a", "https://a.example.com", true)

	name := "b"
	updated, err := s.UpdateSource(src.ID, SourcePatch{Name: &name})
	if err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if updated.Name != "b" {
		t.Errorf("Name = %q, want %q", updated.Name, "b")
	}
	if updated.URL != "https://a.example.com" {
		t.Errorf("URL = %q, want 保留原值（nil 字段不改）", updated.URL)
	}
	if !updated.Enabled {
		t.Error("Enabled = false, want 保留原值 true")
	}
	// 落盘后可读回，且 URL/Enabled 未变
	if got, _ := s.GetSource(src.ID); got.Name != "b" || got.URL != "https://a.example.com" || !got.Enabled {
		t.Errorf("落盘回读 = %+v, want name=b url/url 原值 enabled=true", got)
	}
}

func TestUpdateSourceAllFields(t *testing.T) {
	s := newTestStore(t)
	src, _ := s.CreateSource("a", "https://a.example.com", true)
	enabled := false
	url := "https://b.example.com"
	updated, err := s.UpdateSource(src.ID, SourcePatch{URL: &url, Enabled: &enabled})
	if err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if updated.URL != url || updated.Enabled {
		t.Errorf("updated = %+v, want url=%q enabled=false", updated, url)
	}
	if updated.Name != "a" {
		t.Errorf("Name = %q, want 保留 %q", updated.Name, "a")
	}
}

func TestUpdateSourceEmptyURLError(t *testing.T) {
	s := newTestStore(t)
	src, _ := s.CreateSource("a", "https://a.example.com", true)
	empty := ""
	_, err := s.UpdateSource(src.ID, SourcePatch{URL: &empty})
	if err == nil {
		t.Fatal("URL 指向空串: want error, got nil")
	}
	// 失败更新不应改变数据
	if got, _ := s.GetSource(src.ID); got.URL != "https://a.example.com" {
		t.Errorf("URL = %q, want 保持原值", got.URL)
	}
}

func TestUpdateSourceNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.UpdateSource("nope", SourcePatch{}); err == nil {
		t.Error("UpdateSource(nonexistent): want error, got nil")
	}
}

func TestDeleteSource(t *testing.T) {
	s := newTestStore(t)
	src, _ := s.CreateSource("a", "https://a.example.com", true)
	if err := s.DeleteSource(src.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	if _, ok := s.GetSource(src.ID); ok {
		t.Error("删除后 GetSource 仍 found")
	}
	if err := s.DeleteSource(src.ID); err == nil {
		t.Error("重复删除: want error, got nil")
	}
	if err := s.DeleteSource("nope"); err == nil {
		t.Error("DeleteSource(nonexistent): want error, got nil")
	}
}

func TestSourcePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	a, _ := s1.CreateSource("a", "https://a.example.com", true)
	b, _ := s1.CreateSource("b", "https://b.example.com", false)

	// 重新打开同目录：数据仍在
	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	list := s2.ListSources()
	if len(list) != 2 {
		t.Fatalf("sources = %d, want 2", len(list))
	}
	byID := map[string]Source{}
	for _, src := range list {
		byID[src.ID] = src
	}
	if got, ok := byID[a.ID]; !ok || got.Name != "a" || got.URL != "https://a.example.com" || !got.Enabled {
		t.Errorf("a 持久化回读 = %+v", got)
	}
	if got, ok := byID[b.ID]; !ok || got.Name != "b" || got.Enabled {
		t.Errorf("b 持久化回读 = %+v", got)
	}
}

func isHex12(id string) bool {
	if len(id) != 12 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
