// Package api 提供订阅转换 HTTP 服务（subconverter 兼容调用习惯）。
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/metacubex/mihomo/adapter"

	"github.com/yangyu/openclash-sub-converter/internal/config"
	"github.com/yangyu/openclash-sub-converter/internal/fetcher"
	"github.com/yangyu/openclash-sub-converter/internal/groups"
	"github.com/yangyu/openclash-sub-converter/internal/link"
	"github.com/yangyu/openclash-sub-converter/internal/output"
	"github.com/yangyu/openclash-sub-converter/internal/template"
	"github.com/yangyu/openclash-sub-converter/internal/transform"
)

const (
	version = "0.1.0"
	mihomo  = "v1.19.29"
)

// server 持有路由处理所需的依赖。
type server struct {
	cfg    *config.Config
	f      *fetcher.Fetcher
	logger *slog.Logger
}

// NewServer 构建 HTTP 路由：
//
//	GET /healthz → 200 "ok"
//	GET /version → JSON {"version":"0.1.0","mihomo":"v1.19.29"}
//	GET /sub     → 订阅转换（见 handleSub 注释）
//
// 所有请求记录方法/路径/耗时/状态日志；订阅 URL 只记录 host。
func NewServer(cfg *config.Config, f *fetcher.Fetcher) http.Handler {
	s := &server{cfg: cfg, f: f, logger: slog.Default()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /sub", s.handleSub)
	return s.logMiddleware(mux)
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *server) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"version": version, "mihomo": mihomo})
}

// handleSub 执行转换流水线：
//
//	fetcher.Fetch → link.ParseSubscription → adapter.ParseProxy 节点级校验
//	  （失败跳过+warn）→ transform.Apply → groups.Build → template.Build
//	  → output.Render → output.Validate（YAML 语法层）
//
// 参数：target 必须为 "clash"；url 必填（多个源用 | 分隔）；include/exclude/
// rename 为可选正则；udp/tls13/scv 取值 "true"/"1" 视为 true。
//
// 错误映射：参数错误 400；所有源拉取失败或校验后无有效节点 502；转换/渲染/
// 校验失败 500。部分源失败时只要有节点成功即继续，失败源记 warn 日志。
// 错误消息脱敏：不含完整订阅 URL，只含源 host。
func (s *server) handleSub(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("target") != "clash" {
		writeJSONError(w, http.StatusBadRequest, `invalid or missing target: only "clash" is supported`)
		return
	}
	rawURL := q.Get("url")
	if rawURL == "" {
		writeJSONError(w, http.StatusBadRequest, "missing required parameter: url")
		return
	}
	sources := splitSources(rawURL)
	if len(sources) == 0 {
		writeJSONError(w, http.StatusBadRequest, "url parameter contains no subscription sources")
		return
	}
	filter := transform.Filter{
		Rename:  q.Get("rename"),
		Include: q.Get("include"),
		Exclude: q.Get("exclude"),
	}
	opts := template.Options{
		UDP:   truthy(q.Get("udp")),
		TLS13: truthy(q.Get("tls13")),
		SCV:   truthy(q.Get("scv")),
	}

	ctx := r.Context()
	var allNodes []map[string]any
	var failedHosts []string
	for _, src := range sources {
		host := hostOf(src)
		body, err := s.f.Fetch(ctx, src)
		if err != nil {
			s.logger.Warn("fetch subscription source failed", "host", host, "error", sanitizeErr(err, src, host))
			failedHosts = append(failedHosts, host)
			continue
		}
		nodes, err := link.ParseSubscription(body, host)
		if err != nil {
			s.logger.Warn("parse subscription source failed", "host", host, "error", sanitizeErr(err, src, host))
			failedHosts = append(failedHosts, host)
			continue
		}
		allNodes = append(allNodes, nodes...)
	}
	if len(allNodes) == 0 {
		msg := "all subscription sources failed: no nodes parsed"
		if len(failedHosts) > 0 {
			msg = "all subscription sources failed: " + strings.Join(failedHosts, ", ")
		}
		writeJSONError(w, http.StatusBadGateway, msg)
		return
	}

	// 节点级校验（P1-1）：link 解析出的条目（含 YAML 订阅透传条目）逐条过
	// mihomo adapter.ParseProxy——缺必填字段/非法参数/未知 type 的节点直接跳过
	// 并记 warn 日志，避免坏节点进入输出 YAML（output.Validate 只做 YAML 语法层
	// 校验，不拦截字段级坏节点）。
	valid := allNodes[:0]
	for _, mapping := range allNodes {
		if _, err := adapter.ParseProxy(mapping); err != nil {
			name, _ := mapping["name"].(string)
			s.logger.Warn("skip invalid proxy node", "name", name, "error", err.Error())
			continue
		}
		valid = append(valid, mapping)
	}
	allNodes = valid
	if len(allNodes) == 0 {
		writeJSONError(w, http.StatusBadGateway, "all subscription sources failed: no valid nodes after mihomo validation")
		return
	}

	nodes, err := transform.Apply(allNodes, filter)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("transform failed: %v", err))
		return
	}
	groupsList, err := groups.Build(nodes)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("build proxy groups failed: %v", err))
		return
	}
	cfgMap, err := template.Build(nodes, groupsList, opts)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("build config failed: %v", err))
		return
	}
	data, err := output.Render(cfgMap)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("render yaml failed: %v", err))
		return
	}
	if err := output.Validate(data); err != nil {
		s.logger.Error("generated config failed mihomo validation", "error", err)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("generated config failed mihomo validation: %v", err))
		return
	}

	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// splitSources 按 | 拆分多源 URL 参数，去空白与空项。
func splitSources(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, "|") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// hostOf 提取订阅源 host；URL 非法时返回占位符（避免把完整 URL 带进日志/错误）。
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid url>"
	}
	return u.Host
}

// sanitizeErr 将错误文本中的订阅 URL 替换为 host（脱敏）。
//
// 基于 *url.URL 结构重建：同时替换原始串、Go 规范化串与脱敏串三种形态；
// URL 解析失败时以占位符替换原始串，避免把含凭证的原始输入带进日志/错误。
func sanitizeErr(err error, rawURL, host string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if rawURL == "" {
		return msg
	}
	if host == "" {
		host = "<invalid url>"
	}
	u, perr := url.Parse(rawURL)
	if perr != nil || u.Host == "" {
		return strings.ReplaceAll(msg, rawURL, host)
	}
	for _, v := range []string{rawURL, u.String(), u.Redacted()} {
		if v != "" && v != host {
			msg = strings.ReplaceAll(msg, v, host)
		}
	}
	return msg
}

// truthy 解析 "true"/"1"（大小写不敏感，去空白）为 true。
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1":
		return true
	}
	return false
}

// writeJSONError 写 JSON 错误响应 {"error": msg}。
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// statusWriter 捕获响应状态码，供请求日志使用。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// logMiddleware 记录请求日志（方法/路径/状态/耗时），不记录 query。
func (s *server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start).Round(time.Microsecond).String(),
		)
	})
}
