package fetcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yangyu/openclash-sub-converter/internal/config"
)

func ctx() context.Context { return context.Background() }

func TestFetchSuccessCacheAndUA(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.Header.Get("User-Agent"); got != "test-ua/1.0" {
			t.Errorf("User-Agent = %q, want test-ua/1.0", got)
		}
		w.Write([]byte("payload-123"))
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{
		UserAgent:       "test-ua/1.0",
		TimeoutSeconds:  5,
		CacheTTLSeconds: 3600,
		MaxBytes:        1024,
	})
	b1, err := f.Fetch(ctx(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch #1 error: %v", err)
	}
	if string(b1) != "payload-123" {
		t.Errorf("body = %q, want payload-123", b1)
	}
	b2, err := f.Fetch(ctx(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch #2 error: %v", err)
	}
	if string(b2) != "payload-123" {
		t.Errorf("cached body = %q, want payload-123", b2)
	}
	if hits.Load() != 1 {
		t.Errorf("server hits = %d, want 1 (second fetch should hit cache)", hits.Load())
	}
}

func TestFetchCacheTTLZeroAlwaysRefetch(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("x"))
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 0, MaxBytes: 1024})
	if _, err := f.Fetch(ctx(), srv.URL); err != nil {
		t.Fatalf("Fetch #1 error: %v", err)
	}
	if _, err := f.Fetch(ctx(), srv.URL); err != nil {
		t.Fatalf("Fetch #2 error: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("server hits = %d, want 2 (TTL=0 must never hit cache)", hits.Load())
	}
}

func TestFetchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
		w.Write([]byte("late"))
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 1, CacheTTLSeconds: 60, MaxBytes: 1024})
	start := time.Now()
	_, err := f.Fetch(ctx(), srv.URL)
	if err == nil {
		t.Fatal("Fetch = nil error, want timeout error")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("Fetch took %v, want < 4s (client timeout not applied?)", elapsed)
	}
}

func TestFetchTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("a", 100)))
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 10})
	_, err := f.Fetch(ctx(), srv.URL)
	if err == nil {
		t.Fatal("Fetch = nil error, want max_bytes error")
	}
	if !strings.Contains(err.Error(), "max_bytes") {
		t.Errorf("error %q does not mention max_bytes", err)
	}
}

func TestFetchRejectsNonHTTP(t *testing.T) {
	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})
	for _, u := range []string{
		"file:///etc/passwd",
		"ftp://example.com/sub",
		"gopher://example.com/x",
		"://bad-url",
		"",
	} {
		if _, err := f.Fetch(ctx(), u); err == nil {
			t.Errorf("Fetch(%q) = nil error, want error", u)
		}
	}
}

func TestFetchErrorRedactsCredentials(t *testing.T) {
	// P1-5 回归：*url.Error 的 Error() 含完整请求 URL（userinfo/query 可携带机场
	// 凭证），Fetch 返回的错误必须只含 host，不含 user:pass 与 token=SECRET。
	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})

	// 先起一个 server 再关闭，保证端口确定且连接必被拒绝
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()

	credsURL := "http://user:pass@" + hostPort + "/sub?token=SECRET"
	_, err := f.Fetch(ctx(), credsURL)
	if err == nil {
		t.Fatal("Fetch = nil error, want connection refused error")
	}
	msg := err.Error()
	if !strings.Contains(msg, hostPort) {
		t.Errorf("error should mention source host %s, got %q", hostPort, msg)
	}
	for _, leak := range []string{"user:pass", "token=SECRET", credsURL} {
		if strings.Contains(msg, leak) {
			t.Errorf("error leaks %q: %q", leak, msg)
		}
	}

	// URL 解析失败路径同样脱敏（url.Parse 错误文本含原始 URL）
	badURL := "http://user:pass@" + hostPort + "/sub?token=SECRET%zz"
	_, err = f.Fetch(ctx(), badURL)
	if err == nil {
		t.Fatal("Fetch = nil error, want url parse error")
	}
	msg = err.Error()
	for _, leak := range []string{"user:pass", "token=SECRET", badURL} {
		if strings.Contains(msg, leak) {
			t.Errorf("parse error leaks %q: %q", leak, msg)
		}
	}
}

func TestFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})
	_, err := f.Fetch(ctx(), srv.URL)
	if err == nil {
		t.Fatal("Fetch = nil error, want non-200 error")
	}
}

func TestFetchContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 30, CacheTTLSeconds: 60, MaxBytes: 1024})
	cctx, cancel := context.WithCancel(ctx())
	cancel() // 立即取消
	if _, err := f.Fetch(cctx, srv.URL); err == nil {
		t.Error("Fetch(cancelled ctx) = nil error, want error")
	}
}
