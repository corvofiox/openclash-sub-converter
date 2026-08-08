package store

import (
	"os"
	"testing"
	"time"
)

func TestAppendLogAutoFields(t *testing.T) {
	s := newTestStore(t)
	e, err := s.AppendLog(LogEntry{Kind: "convert", Status: "ok"})
	if err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if len(e.ID) != 12 || !isHex12(e.ID) {
		t.Errorf("ID = %q, want 12 hex chars", e.ID)
	}
	if _, err := time.Parse(time.RFC3339, e.TS); err != nil {
		t.Errorf("TS = %q, want RFC3339 自动填充: %v", e.TS, err)
	}
	if e.Kind != "convert" || e.Status != "ok" {
		t.Errorf("e = %+v, want kind/status 保留", e)
	}
	if got, ok := s.GetLog(e.ID); !ok || got.ID != e.ID {
		t.Errorf("GetLog(%q) 未找到", e.ID)
	}
}

func TestAppendLogPreservesProvidedIDTS(t *testing.T) {
	s := newTestStore(t)
	given := LogEntry{ID: "deadbeefcafe", TS: "2026-01-01T00:00:00Z", Kind: "convert"}
	e, err := s.AppendLog(given)
	if err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if e.ID != given.ID || e.TS != given.TS {
		t.Errorf("e = %+v, want 保留调用方 ID/TS", e)
	}
}

func TestListLogsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ts := []string{"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z"}
	for _, tsv := range ts {
		if _, err := s.AppendLog(LogEntry{ID: tsv, TS: tsv, Kind: "convert"}); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	list := s.ListLogs(10)
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	wantOrder := []string{ts[2], ts[1], ts[0]}
	for i, want := range wantOrder {
		if list[i].ID != want {
			t.Errorf("list[%d].ID = %q, want %q（最新在前）", i, list[i].ID, want)
		}
	}
}

func TestListLogsLimitSemantics(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 60; i++ {
		if _, err := s.AppendLog(LogEntry{ID: itoaID(i), Kind: "convert"}); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	// limit<=0 默认 50
	if got := len(s.ListLogs(0)); got != 50 {
		t.Errorf("ListLogs(0) = %d, want 50", got)
	}
	if got := len(s.ListLogs(-1)); got != 50 {
		t.Errorf("ListLogs(-1) = %d, want 50", got)
	}
	// 上限 200
	if got := len(s.ListLogs(500)); got != 60 {
		t.Errorf("ListLogs(500) = %d, want 60（总条数不足上限）", got)
	}
	if got := len(s.ListLogs(10)); got != 10 {
		t.Errorf("ListLogs(10) = %d, want 10", got)
	}
}

func TestLogRingBufferEviction(t *testing.T) {
	s := newTestStore(t)
	firstID := ""
	for i := 0; i < 210; i++ {
		id := itoaID(i)
		if i == 0 {
			firstID = id
		}
		if _, err := s.AppendLog(LogEntry{ID: id, Kind: "convert"}); err != nil {
			t.Fatalf("AppendLog #%d: %v", i, err)
		}
	}
	list := s.ListLogs(200)
	if len(list) != 200 {
		t.Fatalf("ListLogs(200) = %d, want 200（环形上限）", len(list))
	}
	// 最旧的（第 1 条）被淘汰
	for _, e := range list {
		if e.ID == firstID {
			t.Fatalf("最旧条目 %q 未被淘汰", firstID)
		}
	}
	// 最新一条仍在（且在最前）
	if list[0].ID != itoaID(209) {
		t.Errorf("list[0].ID = %q, want 最新 %q", list[0].ID, itoaID(209))
	}
	// GetLog 也查不到已淘汰条目
	if _, ok := s.GetLog(firstID); ok {
		t.Error("被淘汰条目仍可通过 GetLog 查到")
	}
	// 再追加一条仍稳定在 200
	if _, err := s.AppendLog(LogEntry{ID: "lastone0001", Kind: "convert"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if got := len(s.ListLogs(200)); got != 200 {
		t.Errorf("追加后 len = %d, want 200", got)
	}
}

func TestLogPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	e, err := s1.AppendLog(LogEntry{
		Kind: "convert", SourceID: "src1", SourceName: "机场A",
		URLRedacted: "https://***.com", URLFull: "https://secret.example.com/sub?token=abc",
		Params: map[string]any{"udp": true}, Status: "ok", NodeCount: 5, DurationMS: 123,
	})
	if err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	got, ok := s2.GetLog(e.ID)
	if !ok {
		t.Fatal("重新打开后日志丢失")
	}
	if got.URLRedacted != "https://***.com" || got.URLFull != "https://secret.example.com/sub?token=abc" {
		t.Errorf("URL 字段回读 = %q / %q", got.URLRedacted, got.URLFull)
	}
	if got.NodeCount != 5 || got.DurationMS != 123 || got.Status != "ok" || got.Error != nil {
		t.Errorf("got = %+v", got)
	}
	if got.Params["udp"] != true {
		t.Errorf("Params 回读 = %+v", got.Params)
	}
}

// TestAppendLogRollbackOnWriteFailure（P3-15）落盘失败：AppendLog 返回 err
// 且内存态不变（旧代码先改内存后落盘，失败时内存与磁盘不一致）。
func TestAppendLogRollbackOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.AppendLog(LogEntry{Kind: "convert", Status: "ok"}); err != nil {
		t.Fatalf("AppendLog #1: %v", err)
	}
	// 破坏数据目录 → CreateTemp 失败
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := s.AppendLog(LogEntry{Kind: "convert", Status: "ok"}); err == nil {
		t.Error("AppendLog on broken data dir: want error, got nil")
	}
	if got := len(s.ListLogs(10)); got != 1 {
		t.Errorf("ListLogs = %d, want 1（失败追加不改内存态）", got)
	}
}

// itoaID 生成测试用稳定 ID（与业务 newID 无关）。
func itoaID(i int) string {
	digits := "0123456789abcdef"
	out := make([]byte, 12)
	for k := 11; k >= 0; k-- {
		out[k] = digits[i%16]
		i /= 16
	}
	return string(out)
}
