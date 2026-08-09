package fetcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// ---------- FetchHead ----------

func TestFetchHeadTruncates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 100)))
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})
	data, truncated, err := f.FetchHead(ctx(), srv.URL, 10)
	if err != nil {
		t.Fatalf("FetchHead error: %v", err)
	}
	if !truncated {
		t.Error("truncated = false, want true（超出 maxBytes 应截断而非报错）")
	}
	if len(data) != 10 {
		t.Errorf("len(data) = %d, want 10", len(data))
	}
}

func TestFetchHeadNoTruncate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("small-body"))
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})
	data, truncated, err := f.FetchHead(ctx(), srv.URL, 1024)
	if err != nil {
		t.Fatalf("FetchHead error: %v", err)
	}
	if truncated {
		t.Error("truncated = true, want false")
	}
	if string(data) != "small-body" {
		t.Errorf("body = %q, want small-body", data)
	}
}

func TestFetchHeadNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})
	_, _, err := f.FetchHead(ctx(), srv.URL, 1024)
	if err == nil {
		t.Fatal("FetchHead = nil error, want non-200 error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not mention status 500", err)
	}
}

// TestFetchHeadDoesNotWriteCache 先 FetchHead 后 Fetch：FetchHead 不得写缓存，
// Fetch 必须重新拉取全量。
func TestFetchHeadDoesNotWriteCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("full-body-123"))
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 3600, MaxBytes: 1024})
	head, truncated, err := f.FetchHead(ctx(), srv.URL, 8)
	if err != nil {
		t.Fatalf("FetchHead error: %v", err)
	}
	if !truncated || string(head) != "full-bod" {
		t.Errorf("head = %q truncated=%v, want full-bod true（13 字节 > maxBytes 8）", head, truncated)
	}
	body, err := f.Fetch(ctx(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if string(body) != "full-body-123" {
		t.Errorf("Fetch body = %q, want full-body-123（FetchHead 不应污染缓存）", body)
	}
	if hits.Load() != 2 {
		t.Errorf("server hits = %d, want 2（FetchHead 必须真正发起请求且不写缓存）", hits.Load())
	}
}

// TestFetchHeadRedirectLimit 重定向环最多跟随 5 次后报错。
func TestFetchHeadRedirectLimit(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL, http.StatusFound)
	}))
	defer srv.Close()

	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})
	_, _, err := f.FetchHead(ctx(), srv.URL, 1024)
	if err == nil {
		t.Fatal("FetchHead redirect loop = nil error, want redirect limit error")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error %q does not mention redirect", err)
	}
}

// TestFetchHeadRejectsNonHTTP 与 Fetch 一致拒绝非 http(s) scheme。
func TestFetchHeadRejectsNonHTTP(t *testing.T) {
	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/sub", "", "://bad-url"} {
		if _, _, err := f.FetchHead(ctx(), u, 1024); err == nil {
			t.Errorf("FetchHead(%q) = nil error, want error", u)
		}
	}
}

// TestCheckRedirectDowngrade 重定向策略纯函数（无需真实 TLS 服务器）：
// https→http 降级拒绝、同 scheme/升级放行。
func TestCheckRedirectDowngrade(t *testing.T) {
	fn := checkRedirect(5)
	viaHTTPS := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "tls.example.com"}}}

	// https → http 降级：拒绝
	reqHTTP := &http.Request{URL: &url.URL{Scheme: "http", Host: "plain.example.com"}}
	if err := fn(reqHTTP, viaHTTPS); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Errorf("https→http: err = %v, want downgrade 拒绝错误", err)
	}
	// https → https：放行
	reqHTTPS := &http.Request{URL: &url.URL{Scheme: "https", Host: "tls2.example.com"}}
	if err := fn(reqHTTPS, viaHTTPS); err != nil {
		t.Errorf("https→https: err = %v, want nil", err)
	}
	// http → https 升级：放行
	viaHTTP := []*http.Request{{URL: &url.URL{Scheme: "http", Host: "plain.example.com"}}}
	if err := fn(reqHTTPS, viaHTTP); err != nil {
		t.Errorf("http→https upgrade: err = %v, want nil", err)
	}
}

// TestCheckRedirectLimit 重定向次数上限纯函数：via 达到 5 跳后拒绝，4 跳放行。
func TestCheckRedirectLimit(t *testing.T) {
	fn := checkRedirect(5)
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "h.example.com"}}
	via := make([]*http.Request, 5)
	for i := range via {
		via[i] = &http.Request{URL: &url.URL{Scheme: "https", Host: "h.example.com"}}
	}
	if err := fn(req, via[:4]); err != nil {
		t.Errorf("4 via: err = %v, want nil（第 5 跳才到上限）", err)
	}
	if err := fn(req, via); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Errorf("5 via: err = %v, want redirect 上限错误", err)
	}
}

// TestFetchHeadErrorRedactsCredentials FetchHead 凭证脱敏：URL 带 userinfo/
// query 凭证时，错误消息只含 host，不含 user:secret 与 token=SECRET。
// 连接拒绝路径走 *url.Error 分支（与 Fetch 的 P1-5 回归同款），非 200 响应
// 路径同样不得泄露。
func TestFetchHeadErrorRedactsCredentials(t *testing.T) {
	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})

	// 连接拒绝：先起 server 再关闭，保证端口确定且连接必被拒绝
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()

	credsURL := "http://user:secret@" + hostPort + "/sub?token=SECRET"
	_, _, err := f.FetchHead(ctx(), credsURL, 1024)
	if err == nil {
		t.Fatal("FetchHead = nil error, want connection refused error")
	}
	msg := err.Error()
	if !strings.Contains(msg, hostPort) {
		t.Errorf("error should mention host %s, got %q", hostPort, msg)
	}
	for _, leak := range []string{"user:secret", "secret", "token=SECRET", credsURL} {
		if strings.Contains(msg, leak) {
			t.Errorf("error leaks %q: %q", leak, msg)
		}
	}

	// 非 200 响应路径（错误消息同样不含凭证）
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv2.Close()
	credsURL2 := "http://user:secret@" + strings.TrimPrefix(srv2.URL, "http://") + "/x?token=SECRET"
	_, _, err = f.FetchHead(ctx(), credsURL2, 1024)
	if err == nil {
		t.Fatal("FetchHead(500) = nil error, want non-200 error")
	}
	for _, leak := range []string{"user:secret", "secret", "token=SECRET", credsURL2} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("non-200 error leaks %q: %q", leak, err.Error())
		}
	}
}

// TestFetchHeadZeroMaxBytesUsesDefault maxBytes<=0 防御：小 body 不截断返回
// 全量；大 body（>512KB）截断到 defaultFetchHeadBytes——证明默认上限生效，
// 而非 LimitReader(0+1) 只读到 1 字节返回空。
func TestFetchHeadZeroMaxBytesUsesDefault(t *testing.T) {
	f := New(config.FetcherConfig{TimeoutSeconds: 5, CacheTTLSeconds: 60, MaxBytes: 1024})

	small := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("small-body"))
	}))
	defer small.Close()
	data, truncated, err := f.FetchHead(ctx(), small.URL, 0)
	if err != nil {
		t.Fatalf("FetchHead(maxBytes=0) error: %v", err)
	}
	if truncated || string(data) != "small-body" {
		t.Errorf("maxBytes=0: data=%q truncated=%v, want full small-body false", data, truncated)
	}

	line := "DOMAIN-SUFFIX," + strings.Repeat("a", 80) + ".example.com\n" // ~110 字节
	big := strings.Repeat(line, 6000)                                     // ~660KB > 512KB
	bigSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer bigSrv.Close()
	data, truncated, err = f.FetchHead(ctx(), bigSrv.URL, 0)
	if err != nil {
		t.Fatalf("FetchHead(big, maxBytes=0) error: %v", err)
	}
	if !truncated {
		t.Error("big body: truncated = false, want true（默认 512KB 上限应截断）")
	}
	if len(data) != defaultFetchHeadBytes {
		t.Errorf("big body: len(data) = %d, want %d（默认上限生效）", len(data), defaultFetchHeadBytes)
	}
}
