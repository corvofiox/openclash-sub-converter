// Package fetcher 负责订阅源的 HTTP 拉取与内存缓存。
package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yangyu/openclash-sub-converter/internal/config"
)

// cacheEntry 是缓存条目：原始 body 与拉取时刻。
type cacheEntry struct {
	data      []byte
	fetchedAt time.Time
}

// Fetcher 拉取订阅源并按 URL 缓存结果。
type Fetcher struct {
	cfg    config.FetcherConfig
	client *http.Client
	ttl    time.Duration
	mu     sync.Mutex
	cache  map[string]cacheEntry
}

// New 创建 Fetcher。cfg.TimeoutSeconds <= 0 时不设客户端超时。
func New(cfg config.FetcherConfig) *Fetcher {
	client := &http.Client{}
	if cfg.TimeoutSeconds > 0 {
		client.Timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	return &Fetcher{
		cfg:    cfg,
		client: client,
		ttl:    time.Duration(cfg.CacheTTLSeconds) * time.Second,
		cache:  make(map[string]cacheEntry),
	}
}

// Fetch 拉取 url 指向的订阅源并返回原始 body 字节。
//
// 仅允许 http/https 协议（防 file:// 等读本地文件）。结果按完整 URL 缓存，
// TTL 内命中直接返回缓存（命中不刷新 TTL）；TTL 过期后重新拉取。
// 日志只记录 host，不记录完整 URL（订阅 URL 可能含机场凭证）。
func (f *Fetcher) Fetch(ctx context.Context, urlStr string) ([]byte, error) {
	if urlStr == "" {
		return nil, fmt.Errorf("empty subscription url")
	}
	u, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("invalid subscription url: %w", sanitizeURLError(urlStr, err))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q: only http/https allowed", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid subscription url: missing host")
	}
	host := u.Host

	f.mu.Lock()
	if e, ok := f.cache[urlStr]; ok && f.ttl > 0 && time.Since(e.fetchedAt) < f.ttl {
		f.mu.Unlock()
		return e.data, nil
	}
	f.mu.Unlock()

	log.Printf("fetching subscription from %s", host)
	data, err := f.fetchOnce(ctx, urlStr)
	if err != nil {
		log.Printf("failed to fetch subscription from %s: %v", host, err)
		return nil, err
	}

	f.mu.Lock()
	f.cache[urlStr] = cacheEntry{data: data, fetchedAt: time.Now()}
	f.mu.Unlock()
	return data, nil
}

// fetchOnce 执行单次 HTTP GET，不涉及缓存。
func (f *Fetcher) fetchOnce(ctx context.Context, urlStr string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if f.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", f.cfg.UserAgent)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		// *url.Error 的 Error() 含完整请求 URL（可能带 userinfo/query 凭证），
		// 只透传内层错误并附 host，保证错误消息不泄露完整 URL。
		var uerr *url.Error
		if errors.As(err, &uerr) && uerr.Err != nil {
			return nil, fmt.Errorf("http request failed for %s: %v", urlHost(uerr.URL), uerr.Err)
		}
		return nil, fmt.Errorf("http request failed for %s: %v", urlHost(urlStr), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	max := f.cfg.MaxBytes
	if max <= 0 {
		max = config.DefaultMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("response body exceeds max_bytes limit (%d bytes)", max)
	}
	return body, nil
}

// defaultFetchHeadBytes 是 FetchHead 在 maxBytes<=0 时的兜底上限（512KB）。
// 与探测语义一致：仅需头部样本，防响应膨胀；fetcher 包不引用 api 包常量，
// 独立定义同值常量。
const defaultFetchHeadBytes = 512 << 10

// FetchHead 拉取 url 指向内容的前 maxBytes 字节（探测类用途，如规则集
// 自动探测）。超出 maxBytes 时截断返回 (data, true, nil) 而非报错——探测
// 仅需头部样本。不写缓存、不复用 f.client（独立 10s 超时客户端，避免与
// /sub 拉取的超时/缓存语义相互影响）。
//
// 安全约束：最多跟随 5 次重定向（checkRedirect），禁止 https→http 降级
// （防凭证经降级链路明文泄露）；非 http(s) scheme 由 Go 自动报错。非 200
// 状态码报错。错误消息沿用 Fetch 的脱敏模式：只含 host，不含完整 URL 与
// 凭证。
func (f *Fetcher) FetchHead(ctx context.Context, urlStr string, maxBytes int64) (data []byte, truncated bool, err error) {
	// maxBytes<=0 防御：调用方漏传时用 512KB 兜底，避免 LimitReader(1) 只读
	// 到 1 字节导致探测永远空结果。
	if maxBytes <= 0 {
		maxBytes = defaultFetchHeadBytes
	}
	if urlStr == "" {
		return nil, false, fmt.Errorf("empty subscription url")
	}
	u, err := url.Parse(urlStr)
	if err != nil {
		return nil, false, fmt.Errorf("invalid subscription url: %w", sanitizeURLError(urlStr, err))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false, fmt.Errorf("unsupported scheme %q: only http/https allowed", u.Scheme)
	}
	if u.Host == "" {
		return nil, false, fmt.Errorf("invalid subscription url: missing host")
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: checkRedirect(5),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, false, fmt.Errorf("create request: %w", err)
	}
	if f.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", f.cfg.UserAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		// 与 fetchOnce 相同的脱敏：*url.Error 的 Error() 含完整请求 URL
		// （可能带 userinfo/query 凭证），只透传内层错误并附 host。
		var uerr *url.Error
		if errors.As(err, &uerr) && uerr.Err != nil {
			return nil, false, fmt.Errorf("http request failed for %s: %v", urlHost(uerr.URL), uerr.Err)
		}
		return nil, false, fmt.Errorf("http request failed for %s: %v", urlHost(urlStr), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return body[:int(maxBytes)], true, nil
	}
	return body, false, nil
}

// checkRedirect 返回 FetchHead 的重定向策略闭包：最多跟随 maxRedirects 次
// 重定向（via 达到上限时报错），且拒绝 https→http 降级（防凭证经降级链路
// 明文泄露）。独立成包内函数便于单测直接构造 req/via 数组验证，无需真实
// TLS 服务器。
func checkRedirect(maxRedirects int) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing https to http redirect downgrade")
		}
		return nil
	}
}

// urlHost 从 URL 字符串提取 host；解析失败返回占位符（避免原始串进入日志）。
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid url>"
	}
	return u.Host
}

// sanitizeURLError 把错误文本中的完整订阅 URL 替换为 host。
//
// url.Parse / url.Error 的错误消息会包含原始或规范化后的完整 URL（userinfo 与
// query 中可能携带机场凭证），这里基于 URL 结构同时替换原始串、Go 规范化串与
// 脱敏串三种形态；URL 解析失败时用占位符替换原始串兜底。
func sanitizeURLError(rawURL string, err error) error {
	if err == nil {
		return nil
	}
	host := "<invalid url>"
	msg := err.Error()
	if u, perr := url.Parse(rawURL); perr == nil && u.Host != "" {
		host = u.Host
		for _, v := range []string{rawURL, u.String(), u.Redacted()} {
			if v != "" && v != host {
				msg = strings.ReplaceAll(msg, v, host)
			}
		}
	} else {
		msg = strings.ReplaceAll(msg, rawURL, host)
	}
	return errors.New(msg)
}
