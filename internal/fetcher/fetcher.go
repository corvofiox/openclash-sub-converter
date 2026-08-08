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
