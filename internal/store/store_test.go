package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestNewCreatesDirAndEmpty 空目录 → sources/logs 空态；ruleSets 种入 8 个
// 预置（首次启动种子）；目录不存在时自动创建。
func TestNewCreatesDirAndEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("数据目录未创建: %v", err)
	}
	if n := len(s.ListSources()); n != 0 {
		t.Errorf("sources = %d, want 0", n)
	}
	if n := len(s.ListLogs(10)); n != 0 {
		t.Errorf("logs = %d, want 0", n)
	}
	if n := len(s.ListRuleSets()); n != 8 {
		t.Errorf("ruleSets = %d, want 8（首次启动种入预置）", n)
	}
	// 首次启动只落盘 rulesets.json（预置种子）；sources/logs 空态不落盘
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "rulesets.json" {
		t.Errorf("新目录文件 = %v, want 仅 rulesets.json", entries)
	}
}

// TestCorruptedFileRecovery 坏 JSON → New 不报错、数据空态、存在 .bak 备份。
func TestCorruptedFileRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sources.json"), []byte("{not-json!!!"), 0o600); err != nil {
		t.Fatalf("写坏文件: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New 遇到损坏文件不应报错: %v", err)
	}
	if n := len(s.ListSources()); n != 0 {
		t.Errorf("sources = %d, want 空态", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "sources.json.bak")); err != nil {
		t.Errorf("损坏文件未备份为 .bak: %v", err)
	}
	// 损坏恢复后仍可正常写入
	if _, err := s.CreateSource("a", "https://a.example.com", true); err != nil {
		t.Errorf("损坏恢复后写入失败: %v", err)
	}
}

// TestVersionMismatchRecovery version != 1 → warn + 空态，不崩溃。
func TestVersionMismatchRecovery(t *testing.T) {
	dir := t.TempDir()
	data := `{"version":99,"sources":[{"id":"x","name":"a","url":"https://a.com"}]}`
	if err := os.WriteFile(filepath.Join(dir, "sources.json"), []byte(data), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New 遇到 version 不匹配不应报错: %v", err)
	}
	if n := len(s.ListSources()); n != 0 {
		t.Errorf("sources = %d, want 空态", n)
	}
}

// TestAllFilesRoundTrip 三组数据同目录往返：sources/logs/ruleSets 一并持久化。
func TestAllFilesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	src, _ := s1.CreateSource("机场A", "https://a.example.com", true)
	logEntry, _ := s1.AppendLog(LogEntry{Kind: "convert", SourceID: src.ID, Status: "ok", NodeCount: 3})
	rs, _ := s1.CreateRuleSet("cn", "https://e.com/cn.yaml", "domain", "yaml", true)

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	if got, ok := s2.GetSource(src.ID); !ok || got.Name != "机场A" {
		t.Errorf("source 回读失败: %+v, %v", got, ok)
	}
	if got, ok := s2.GetLog(logEntry.ID); !ok || got.SourceID != src.ID {
		t.Errorf("log 回读失败: %+v, %v", got, ok)
	}
	if got, ok := s2.GetRuleSet(rs.ID); !ok || got.Behavior != "domain" {
		t.Errorf("ruleSet 回读失败: %+v, %v", got, ok)
	}
	// 磁盘格式校验：version 字段存在且为 1（MarshalIndent 美化输出）
	raw, err := os.ReadFile(filepath.Join(dir, "sources.json"))
	if err != nil {
		t.Fatalf("读 sources.json: %v", err)
	}
	if !strings.Contains(string(raw), `"version": 1`) {
		t.Errorf("sources.json 缺少 version:1 字段: %s", raw)
	}
	if !strings.Contains(string(raw), `"sources"`) {
		t.Errorf("sources.json 缺少 sources 段: %s", raw)
	}
}

// TestConcurrentWrites 并发写（20 个 CreateSource goroutine）+ 并发读
// （ListSources/ListLogs 循环），配合 go test -race 验证锁粒度正确。
func TestConcurrentWrites(t *testing.T) {
	s := newTestStore(t)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			url := fmt.Sprintf("https://s%d.example.com/sub", i)
			if _, err := s.CreateSource(fmt.Sprintf("src-%d", i), url, true); err != nil {
				t.Errorf("CreateSource #%d: %v", i, err)
			}
			if _, err := s.AppendLog(LogEntry{Kind: "convert", SourceID: "s", Status: "ok"}); err != nil {
				t.Errorf("AppendLog #%d: %v", i, err)
			}
		}(i)
	}

	stop := make(chan struct{})
	var readWG sync.WaitGroup
	readWG.Add(1)
	go func() {
		defer readWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = s.ListSources()
				_ = s.ListLogs(10)
				_ = s.ListRuleSets()
			}
		}
	}()

	wg.Wait()
	close(stop)
	readWG.Wait()

	if got := len(s.ListSources()); got != 20 {
		t.Errorf("sources = %d, want 20", got)
	}
	if got := len(s.ListLogs(200)); got != 20 {
		t.Errorf("logs = %d, want 20", got)
	}
	// 并发写后落盘一致：重开目录仍 20 条
	s2, err := New(s.dataDir)
	if err != nil {
		t.Fatalf("重开目录: %v", err)
	}
	if got := len(s2.ListSources()); got != 20 {
		t.Errorf("重开后 sources = %d, want 20", got)
	}
}

// TestDeleteSourceWriteFailure（P2-6）落盘 IO 失败：错误分类不是 ErrNotFound
// （区别于「源不存在」），且内存回滚（源仍可查到）。
func TestDeleteSourceWriteFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	src, err := s.CreateSource("a", "https://a.example.com", true)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	// 破坏数据目录 → CreateTemp 失败（IO 错误）
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	err = s.DeleteSource(src.ID)
	if err == nil {
		t.Fatal("DeleteSource: want error on broken data dir, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want 非 ErrNotFound（IO 错误分类，API 应映射 500）", err)
	}
	if _, ok := s.GetSource(src.ID); !ok {
		t.Error("DeleteSource 落盘失败后内存应保留该源（回滚）")
	}
}

// TestLogFileMode0600（P3-17e）日志文件含凭证 URL，落盘权限必须 0600。
func TestLogFileMode0600(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.AppendLog(LogEntry{Kind: "sub", Status: "ok", URLFull: "https://user:pass@secret.example/sub?token=abc"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "logs.json"))
	if err != nil {
		t.Fatalf("stat logs.json: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("logs.json mode = %v, want 0600", perm)
	}
}
