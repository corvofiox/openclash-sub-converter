// Package api 提供订阅转换 HTTP 服务（subconverter 兼容调用习惯）与
// Web 管理台 REST API（/api/v1）。
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/metacubex/mihomo/adapter"

	"github.com/yangyu/openclash-sub-converter/internal/config"
	"github.com/yangyu/openclash-sub-converter/internal/fetcher"
	"github.com/yangyu/openclash-sub-converter/internal/groups"
	"github.com/yangyu/openclash-sub-converter/internal/link"
	"github.com/yangyu/openclash-sub-converter/internal/output"
	"github.com/yangyu/openclash-sub-converter/internal/store"
	"github.com/yangyu/openclash-sub-converter/internal/template"
	"github.com/yangyu/openclash-sub-converter/internal/transform"
	"github.com/yangyu/openclash-sub-converter/internal/webui"
)

const (
	version = "0.1.0"
	mihomo  = "v1.19.29"
)

// server 持有路由处理所需的依赖。
type server struct {
	cfg    *config.Config
	f      *fetcher.Fetcher
	st     *store.Store
	logger *slog.Logger
}

// NewServer 构建 HTTP 路由：
//
//	GET /healthz → 200 "ok"
//	GET /version → JSON {"version":"0.1.0","mihomo":"v1.19.29"}
//	GET /sub     → 订阅转换（见 handleSub 注释）
//
// st 非 nil 时额外挂载管理台 REST API（/api/v1/*，全部经 authMiddleware
// 鉴权）与 /api/v1/version；st == nil（或省略，兼容旧调用方）时仅保留公开
// 端点。管理台页面（/ 与静态资源）不鉴权：页面本身无敏感数据（数据全在
// /api/v1/*），且浏览器导航无法携带令牌头，挡在 401 后页面完全不可达。
//
// 所有请求记录方法/路径/耗时/状态日志；订阅 URL 只记录 host。
func NewServer(cfg *config.Config, f *fetcher.Fetcher, st ...*store.Store) http.Handler {
	var stPtr *store.Store
	if len(st) > 0 {
		stPtr = st[0]
	}
	s := &server{cfg: cfg, f: f, st: stPtr, logger: slog.Default()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /sub", s.handleSub)
	if stPtr != nil {
		admin := http.NewServeMux()
		admin.HandleFunc("GET /api/v1/sources", s.handleListSources)
		admin.HandleFunc("POST /api/v1/sources", s.handleCreateSource)
		admin.HandleFunc("PUT /api/v1/sources/{id}", s.handleUpdateSource)
		admin.HandleFunc("DELETE /api/v1/sources/{id}", s.handleDeleteSource)
		admin.HandleFunc("POST /api/v1/convert/preview", s.handleConvertPreview)
		admin.HandleFunc("POST /api/v1/convert/run", s.handleConvertRun)
		admin.HandleFunc("GET /api/v1/logs", s.handleListLogs)
		admin.HandleFunc("POST /api/v1/logs/{id}/retry", s.handleLogRetry)
		admin.HandleFunc("GET /api/v1/templates", s.handleListTemplates)
		admin.HandleFunc("POST /api/v1/templates", s.handleCreateTemplate)
		admin.HandleFunc("PUT /api/v1/templates/{id}", s.handleUpdateTemplate)
		admin.HandleFunc("DELETE /api/v1/templates/{id}", s.handleDeleteTemplate)
		admin.HandleFunc("POST /api/v1/templates/probe", s.handleProbeTemplate)
		admin.HandleFunc("GET /api/v1/version", s.handleVersion)
		// 未匹配的 /api/v1/* 子路径 → JSON 404（避免 Go 默认 text/plain 404）
		admin.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
			writeJSONError(w, http.StatusNotFound, "not found")
		})
		// 管理台页面与静态资源不鉴权（见函数注释）；/sub、/healthz、/version
		// 已在上面注册，ServeMux 精确路径优先，不受 / 兜底路由影响。
		mux.Handle("/", webui.Handler())
		mux.Handle("/api/v1/", s.authMiddleware(admin))
	}
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

// pipelineError 携带 HTTP 状态码的管线错误，便于统一映射与日志记录。
type pipelineError struct {
	code int
	msg  string
}

// convertResult 是一次转换管线的产物。
type convertResult struct {
	nodes     []map[string]any // transform 后的节点（name/type 等）
	groups    []map[string]any // groups.Build 产物
	cfg       map[string]any   // template.Build 产物（含规则模板注入）
	data      []byte           // 渲染后的 YAML（render=false 时为空）
	nodeCount int
}

// runPipeline 执行转换管线：拉取→解析→节点级校验→transform→groups→
// ApplyStripEmoji（strip_emoji=true 时剥离节点名 emoji，可选）→
// template（可选规则模板注入）→（render=true 时）渲染+校验。
//
// 语义分层：结构非法的源 URL 由调用方在进入本函数前校验（400）；结构合法但
// 拉取/解析失败的源记 warn 并跳过，全部失败 → 502；transform 参数错误
// （transform.ErrInvalidRegex）→ 400；其余内部错误 → 500。
func (s *server) runPipeline(r *http.Request, srcs []string, filter transform.Filter, opts template.Options, tpl *store.RuleTemplate, render bool) (*convertResult, *pipelineError) {
	ctx := r.Context()
	var allNodes []map[string]any
	var failedHosts []string
	for _, src := range srcs {
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
		return nil, &pipelineError{code: http.StatusBadGateway, msg: msg}
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
		return nil, &pipelineError{code: http.StatusBadGateway, msg: "all subscription sources failed: no valid nodes after mihomo validation"}
	}

	nodes, err := transform.Apply(allNodes, filter)
	if err != nil {
		if errors.Is(err, transform.ErrInvalidRegex) {
			// 客户端参数错误（include/exclude/rename 正则非法）→ 400
			return nil, &pipelineError{code: http.StatusBadRequest, msg: fmt.Sprintf("transform failed: %v", err)}
		}
		return nil, &pipelineError{code: http.StatusInternalServerError, msg: fmt.Sprintf("transform failed: %v", err)}
	}
	groupsList, err := groups.Build(nodes)
	if err != nil {
		return nil, &pipelineError{code: http.StatusInternalServerError, msg: fmt.Sprintf("build proxy groups failed: %v", err)}
	}
	// R2：剥离节点名 emoji（识别已在 groups.Build 基于原始名完成）；
	// strip=false 时 no-op。剥离后统一 uniqueName 去重并改写组 proxies 引用。
	transform.ApplyStripEmoji(nodes, groupsList, filter.StripEmoji)
	cfgMap, err := template.Build(nodes, groupsList, opts)
	if err != nil {
		return nil, &pipelineError{code: http.StatusInternalServerError, msg: fmt.Sprintf("build config failed: %v", err)}
	}
	// 规则模板注入：preview/run 通过 template_id 引用已启用的模板
	if tpl != nil {
		if err := template.ApplyRuleProviders(cfgMap, []template.RuleProvider{{
			Name: tpl.Name, URL: tpl.URL, Behavior: tpl.Behavior, Format: tpl.Format,
		}}, ""); err != nil {
			return nil, &pipelineError{code: http.StatusInternalServerError, msg: fmt.Sprintf("apply rule providers failed: %v", err)}
		}
	}
	res := &convertResult{nodes: nodes, groups: groupsList, cfg: cfgMap, nodeCount: len(nodes)}
	if !render {
		return res, nil
	}
	data, err := output.Render(cfgMap)
	if err != nil {
		return nil, &pipelineError{code: http.StatusInternalServerError, msg: fmt.Sprintf("render yaml failed: %v", err)}
	}
	if err := output.Validate(data); err != nil {
		s.logger.Error("generated config failed mihomo validation", "error", err)
		return nil, &pipelineError{code: http.StatusInternalServerError, msg: fmt.Sprintf("generated config failed mihomo validation: %v", err)}
	}
	res.data = data
	return res, nil
}

// handleSub 执行转换流水线：
//
//	fetcher.Fetch → link.ParseSubscription → adapter.ParseProxy 节点级校验
//	  （失败跳过+warn）→ transform.Apply → groups.Build
//	  → ApplyStripEmoji（strip_emoji=true 时剥离节点名 emoji）→ template.Build
//	  → output.Render → output.Validate（YAML 语法层）
//
// 参数：target 必须为 "clash"；url 必填（多个源用 | 分隔）或 src=<订阅源ID>
// 二选一（同时存在 src 优先，凭证不进 URL）；include/exclude/rename 为可选
// 正则；udp/tls13/scv/strip_emoji 取值 "true"/"1" 视为 true。
// strip_emoji=true 时在输出阶段剥离节点名中的 emoji（识别仍基于原始名）。
//
// 错误映射：参数错误 400（含非法 url 结构、src 不可用、正则非法）；所有源
// 拉取失败或校验后无有效节点 502；转换/渲染/校验失败 500。部分源失败时只
// 要有节点成功即继续，失败源记 warn 日志。错误消息脱敏：不含完整订阅 URL，
// 只含源 host。每次请求（成功/失败）记录转换日志（Kind "sub"）。
func (s *server) handleSub(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	q := r.URL.Query()
	if q.Get("target") != "clash" {
		s.logSubError(w, r, start, `invalid or missing target: only "clash" is supported`, "", "", q.Get("url"), q)
		return
	}

	// 数据源解析：src=<订阅源ID> 优先，否则 url=（多个源用 | 分隔）
	var sources []string
	var srcID, srcName, urlFull string
	if srcParam := q.Get("src"); srcParam != "" {
		if s.st == nil {
			s.logSubError(w, r, start, "src 引用的订阅源不可用", srcParam, "", "", q)
			return
		}
		src, ok := s.st.GetSource(srcParam)
		if !ok || !src.Enabled {
			s.logSubError(w, r, start, "src 引用的订阅源不可用", srcParam, "", "", q)
			return
		}
		sources = []string{src.URL}
		srcID, srcName = src.ID, src.Name
	} else {
		rawURL := q.Get("url")
		if rawURL == "" {
			s.logSubError(w, r, start, "missing required parameter: url", "", "", "", q)
			return
		}
		sources = splitSources(rawURL)
		if len(sources) == 0 {
			s.logSubError(w, r, start, "url parameter contains no subscription sources", "", "", rawURL, q)
			return
		}
		// BUG 修复：结构非法（不可解析/非 http/https/无 host）→ 400 客户端错误
		if err := validateSources(sources); err != nil {
			s.logSubError(w, r, start, err.Error(), "", "", rawURL, q)
			return
		}
		urlFull = rawURL // 临时 url= 参数才存完整 URL（retry 用）
	}

	filter := transform.Filter{
		Rename:     q.Get("rename"),
		Include:    q.Get("include"),
		Exclude:    q.Get("exclude"),
		StripEmoji: truthy(q.Get("strip_emoji")),
	}
	opts := template.Options{
		UDP:   truthy(q.Get("udp")),
		TLS13: truthy(q.Get("tls13")),
		SCV:   truthy(q.Get("scv")),
	}

	res, perr := s.runPipeline(r, sources, filter, opts, nil, true)
	s.appendLog(store.LogEntry{
		Kind:        "sub",
		SourceID:    srcID,
		SourceName:  srcName,
		URLRedacted: redactURL(urlFullOrSources(urlFull, sources)),
		URLFull:     urlFull,
		Params:      buildParams(q.Get("include"), q.Get("exclude"), q.Get("rename"), opts.UDP, opts.TLS13, opts.SCV, truthy(q.Get("strip_emoji")), ""),
		Status:      statusOf(perr),
		Error:       errOf(perr),
		NodeCount:   nodeCountOf(res, perr),
		DurationMS:  time.Since(start).Milliseconds(),
	})
	if perr != nil {
		writeJSONError(w, perr.code, perr.msg)
		return
	}

	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.data)
}

// convertRequest 是 /api/v1/convert/preview 与 /api/v1/convert/run 的 JSON 请求体。
type convertRequest struct {
	SourceID   *string `json:"source_id"` // 与 URL 二选一（同时存在 source_id 优先）
	URL        *string `json:"url"`       // 临时订阅 URL（可含 | 多源）
	Include    string  `json:"include"`
	Exclude    string  `json:"exclude"`
	Rename     string  `json:"rename"`
	UDP        *bool   `json:"udp"`
	TLS13      *bool   `json:"tls13"`
	SCV        *bool   `json:"scv"`
	StripEmoji *bool   `json:"strip_emoji"`
	TemplateID string  `json:"template_id"`
}

// handleConvert 是 convert 端点（preview/run）的公共实现。
// kind 决定日志 Kind 与响应形态：preview 返回节点/策略组摘要（不渲染 YAML），
// run 返回完整 YAML。
func (s *server) handleConvert(kind string, render bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if s.st == nil {
			writeJSONError(w, http.StatusNotFound, "admin API not enabled")
			return
		}
		var req convertRequest
		if err := decodeJSONBody(w, r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		// 数据源二选一：source_id 优先，否则 url（临时）
		var sources []string
		var srcID, srcName, urlFull string
		switch {
		case req.SourceID != nil && *req.SourceID != "":
			src, ok := s.st.GetSource(*req.SourceID)
			if !ok || !src.Enabled {
				writeJSONError(w, http.StatusBadRequest, "订阅源不存在或已禁用")
				return
			}
			sources = []string{src.URL}
			srcID, srcName = src.ID, src.Name
		case req.URL != nil && *req.URL != "":
			sources = splitSources(*req.URL)
			if len(sources) == 0 {
				writeJSONError(w, http.StatusBadRequest, "url 不包含任何订阅源")
				return
			}
			if err := validateSources(sources); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			urlFull = *req.URL
		default:
			writeJSONError(w, http.StatusBadRequest, "source_id 与 url 必须二选一")
			return
		}

		// 规则模板：template_id 非空时注入已启用的模板
		var tpl *store.RuleTemplate
		if req.TemplateID != "" {
			t, ok := s.st.GetTemplate(req.TemplateID)
			if !ok || !t.Enabled {
				writeJSONError(w, http.StatusBadRequest, "模板不存在或已禁用")
				return
			}
			tpl = &t
		}

		filter := transform.Filter{Rename: req.Rename, Include: req.Include, Exclude: req.Exclude, StripEmoji: boolVal(req.StripEmoji)}
		opts := template.Options{UDP: boolVal(req.UDP), TLS13: boolVal(req.TLS13), SCV: boolVal(req.SCV)}

		res, perr := s.runPipeline(r, sources, filter, opts, tpl, render)
		s.appendLog(store.LogEntry{
			Kind:        kind,
			SourceID:    srcID,
			SourceName:  srcName,
			URLRedacted: redactURL(urlFullOrSources(urlFull, sources)),
			URLFull:     urlFull,
			Params:      buildParams(req.Include, req.Exclude, req.Rename, opts.UDP, opts.TLS13, opts.SCV, boolVal(req.StripEmoji), req.TemplateID),
			Status:      statusOf(perr),
			Error:       errOf(perr),
			NodeCount:   nodeCountOf(res, perr),
			DurationMS:  time.Since(start).Milliseconds(),
		})
		if perr != nil {
			writeJSONError(w, perr.code, perr.msg)
			return
		}

		duration := time.Since(start).Milliseconds()
		if render {
			writeJSON(w, http.StatusOK, map[string]any{
				"yaml":        string(res.data),
				"node_count":  res.nodeCount,
				"duration_ms": duration,
			})
			return
		}
		nodes := make([]map[string]string, 0, len(res.nodes))
		for _, n := range res.nodes {
			name, _ := n["name"].(string)
			typ, _ := n["type"].(string)
			nodes = append(nodes, map[string]string{"name": name, "type": typ})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"nodes":       nodes,
			"node_count":  res.nodeCount,
			"groups":      res.groups,
			"duration_ms": duration,
		})
	}
}

// handleConvertPreview / handleConvertRun 是 convert 端点（见 handleConvert）。
func (s *server) handleConvertPreview(w http.ResponseWriter, r *http.Request) {
	s.handleConvert("preview", false)(w, r)
}

func (s *server) handleConvertRun(w http.ResponseWriter, r *http.Request) {
	s.handleConvert("run", true)(w, r)
}

// handleLogRetry 用日志中的源（SourceID 取最新 URL，否则用 URLFull）与参数
// 重跑 preview 管线，成功时追加一条 Kind "preview" 的新日志。
func (s *server) handleLogRetry(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	entry, ok := s.st.GetLog(r.PathValue("id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "日志不存在")
		return
	}

	var sources []string
	var rawURL, srcID, srcName string
	switch {
	case entry.SourceID != "":
		src, ok := s.st.GetSource(entry.SourceID)
		if !ok {
			writeJSONError(w, http.StatusConflict, "订阅源已删除")
			return
		}
		if !src.Enabled {
			writeJSONError(w, http.StatusConflict, "订阅源已禁用")
			return
		}
		rawURL, srcID, srcName = src.URL, src.ID, src.Name
		sources = []string{rawURL}
	case entry.URLFull != "":
		// 多源临时 URL：与 /sub、convert 入口一致，先拆分再校验（非法 → 400），
		// 避免把含 | 的原始串直接交给 runPipeline（逐源 Fetch 前不拆分 → 502）。
		sources = splitSources(entry.URLFull)
		if len(sources) == 0 {
			writeJSONError(w, http.StatusBadRequest, "临时 URL 不可重试")
			return
		}
		if err := validateSources(sources); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		rawURL = entry.URLFull
	default:
		writeJSONError(w, http.StatusBadRequest, "临时 URL 不可重试")
		return
	}

	filter := transform.Filter{
		Rename:     strParam(entry.Params, "rename"),
		Include:    strParam(entry.Params, "include"),
		Exclude:    strParam(entry.Params, "exclude"),
		StripEmoji: boolParam(entry.Params, "strip_emoji"), // 旧日志无此键→false（默认关）
	}
	opts := template.Options{
		UDP:   boolParam(entry.Params, "udp"),
		TLS13: boolParam(entry.Params, "tls13"),
		SCV:   boolParam(entry.Params, "scv"),
	}
	var tpl *store.RuleTemplate
	if tplID := strParam(entry.Params, "template_id"); tplID != "" {
		t, ok := s.st.GetTemplate(tplID)
		if !ok || !t.Enabled {
			writeJSONError(w, http.StatusBadRequest, "模板不存在或已禁用")
			return
		}
		tpl = &t
	}

	res, perr := s.runPipeline(r, sources, filter, opts, tpl, false)
	s.appendLog(store.LogEntry{
		Kind:        "preview",
		SourceID:    srcID,
		SourceName:  srcName,
		URLRedacted: redactURL(rawURL), // 重新脱敏：源 URL 已更新时展示与实际一致
		URLFull:     entry.URLFull,     // 保留原临时 URL（src 场景为空）
		Params:      entry.Params,
		Status:      statusOf(perr),
		Error:       errOf(perr),
		NodeCount:   nodeCountOf(res, perr),
		DurationMS:  time.Since(start).Milliseconds(),
	})
	if perr != nil {
		writeJSONError(w, perr.code, perr.msg)
		return
	}
	nodes := make([]map[string]string, 0, len(res.nodes))
	for _, n := range res.nodes {
		name, _ := n["name"].(string)
		typ, _ := n["type"].(string)
		nodes = append(nodes, map[string]string{"name": name, "type": typ})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes":       nodes,
		"node_count":  res.nodeCount,
		"groups":      res.groups,
		"duration_ms": time.Since(start).Milliseconds(),
	})
}

// ---------- 订阅源 CRUD ----------

func (s *server) handleListSources(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	sources := s.st.ListSources()
	out := make([]sourceResp, 0, len(sources))
	for _, src := range sources {
		out = append(out, toSourceResp(src))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": out})
}

type sourceReq struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled *bool  `json:"enabled"`
}

// sourceResp 是订阅源的对外响应结构：URL 一律脱敏后返回。
type sourceResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toSourceResp(src store.Source) sourceResp {
	return sourceResp{
		ID: src.ID, Name: src.Name, URL: redactURL(src.URL), Enabled: src.Enabled,
		CreatedAt: src.CreatedAt, UpdatedAt: src.UpdatedAt,
	}
}

func (s *server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	var req sourceReq
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name 不能为空")
		return
	}
	if req.URL == "" {
		writeJSONError(w, http.StatusBadRequest, "url 不能为空")
		return
	}
	if err := validateSources([]string{req.URL}); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	src, err := s.st.CreateSource(req.Name, req.URL, boolVal(req.Enabled))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"source": toSourceResp(src)})
}

func (s *server) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	var patch store.SourcePatch
	if err := decodeJSONBody(w, r, &patch); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if patch.URL != nil && *patch.URL != "" {
		if err := validateSources([]string{*patch.URL}); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	src, err := s.st.UpdateSource(r.PathValue("id"), patch)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": toSourceResp(src)})
}

func (s *server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	if err := s.st.DeleteSource(r.PathValue("id")); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- 转换日志 ----------

// logEntryResp 是日志的对外响应结构：剔除 URLFull（完整订阅 URL 仅内部
// retry 使用，永不通过 API 返回）。
type logEntryResp struct {
	ID          string         `json:"id"`
	TS          string         `json:"ts"`
	Kind        string         `json:"kind"`
	SourceID    string         `json:"source_id"`
	SourceName  string         `json:"source_name"`
	URLRedacted string         `json:"url_redacted"`
	Params      map[string]any `json:"params"`
	Status      string         `json:"status"`
	Error       *string        `json:"error"`
	NodeCount   int            `json:"node_count"`
	DurationMS  int64          `json:"duration_ms"`
}

func toLogEntryResp(e store.LogEntry) logEntryResp {
	return logEntryResp{
		ID: e.ID, TS: e.TS, Kind: e.Kind, SourceID: e.SourceID, SourceName: e.SourceName,
		URLRedacted: e.URLRedacted, Params: e.Params, Status: e.Status, Error: e.Error,
		NodeCount: e.NodeCount, DurationMS: e.DurationMS,
	}
}

func (s *server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	logs := s.st.ListLogs(limit)
	out := make([]logEntryResp, 0, len(logs))
	for _, e := range logs {
		out = append(out, toLogEntryResp(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": out})
}

// ---------- 规则模板 CRUD ----------

func (s *server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	templates := s.st.ListTemplates()
	out := make([]templateResp, 0, len(templates))
	for _, t := range templates {
		out = append(out, toTemplateResp(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

type templateReq struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Behavior string `json:"behavior"`
	Format   string `json:"format"`
	Enabled  *bool  `json:"enabled"`
}

// templateResp 是规则模板的对外响应结构：URL 脱敏后返回。
type templateResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Behavior  string `json:"behavior"`
	Format    string `json:"format"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toTemplateResp(t store.RuleTemplate) templateResp {
	return templateResp{
		ID: t.ID, Name: t.Name, URL: redactURL(t.URL), Behavior: t.Behavior, Format: t.Format,
		Enabled: t.Enabled, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func (s *server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	var req templateReq
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 名称路径穿越校验（会拼进输出 YAML 的 rule-provider path）→ 400，
	// 避免拖到转换管线才报错
	if err := template.ValidateRuleProviderName(req.Name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.URL == "" {
		writeJSONError(w, http.StatusBadRequest, "url 不能为空")
		return
	}
	if err := validateSources([]string{req.URL}); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := s.st.CreateTemplate(req.Name, req.URL, req.Behavior, req.Format, boolVal(req.Enabled))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"template": toTemplateResp(t)})
}

func (s *server) handleUpdateTemplate(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	var patch store.TemplatePatch
	if err := decodeJSONBody(w, r, &patch); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if patch.Name != nil {
		if err := template.ValidateRuleProviderName(*patch.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if patch.URL != nil && *patch.URL != "" {
		if err := validateSources([]string{*patch.URL}); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	t, err := s.st.UpdateTemplate(r.PathValue("id"), patch)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": toTemplateResp(t)})
}

func (s *server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	if err := s.st.DeleteTemplate(r.PathValue("id")); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- 鉴权中间件 ----------

// authMiddleware 保护管理台端点：AdminToken 为空时全部开放（默认本地部署）；
// 否则要求请求携带 X-Token 请求头或 Authorization: Bearer 请求头（值均为
// 管理台令牌），用 crypto/subtle.ConstantTimeCompare 做常量时间比较，失败
// 返回 401。 /sub、/healthz、/version 永不鉴权（不经过本中间件）。
func (s *server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-Token")
		if provided == "" {
			// RFC 7235 允许 auth-scheme 大小写不敏感：bearer/BEARER/Bearer 等价
			if ah := r.Header.Get("Authorization"); len(ah) >= 7 && strings.EqualFold(ah[:7], "bearer ") {
				provided = strings.TrimSpace(ah[7:])
			}
		}
		if provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.AdminToken)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
	})
}

// ---------- 工具函数 ----------

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

// validateSources 校验订阅源 URL 结构：必须可解析、scheme 为 http/https 且
// 带 host。结构非法 → 客户端错误（HTTP 400）；结构合法但拉取失败由管线
// 映射 502。错误消息不包含原始 URL（脱敏）。
func validateSources(sources []string) error {
	for _, raw := range sources {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("invalid subscription url: only http/https with host allowed")
		}
	}
	return nil
}

// sensitiveQueryParams 是订阅 URL query 中的敏感参数名（大小写不敏感），
// 日志/API 响应中值一律替换为 "xxxxx"。
var sensitiveQueryParams = map[string]bool{
	"token": true, "access_token": true, "api_key": true, "key": true,
	"secret": true, "password": true, "pswd": true, "passwd": true,
	"auth": true, "signature": true, "sign": true, "credential": true,
	"credentials": true, "apikey": true,
}

// redactURL 对订阅 URL 做脱敏：userinfo（用户名+密码）整体掩码为 xxxxx@
// （https://user:pass@host → https://xxxxx@host），query 中敏感参数
// （token/access_token/api_key/key/secret/password/pswd/passwd/auth/
// signature/sign/credential/credentials/apikey，大小写不敏感）的值替换为
// "xxxxx"。支持 "|" 分隔的多源串（逐源脱敏后重新拼接）；解析失败的源丢弃。
func redactURL(raw string) string {
	parts := strings.Split(raw, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if r := redactSingleURL(p); r != "" {
			out = append(out, r)
		}
	}
	return strings.Join(out, "|")
}

func redactSingleURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	for k := range q {
		if sensitiveQueryParams[strings.ToLower(k)] {
			q.Set(k, "xxxxx")
		}
	}
	u.RawQuery = q.Encode()
	// userinfo 整体掩码（含用户名）：u.Redacted() 只掩密码，用户名仍会泄露
	if u.User != nil {
		u.User = url.User("xxxxx")
	}
	return u.Redacted()
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

// boolVal 解引用 *bool；nil 视为 false。
func boolVal(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// urlFullOrSources 取日志用 URL：有临时 url= 参数时用它（含 | 多源），
// 否则回退到 sources 拼接（src= 场景，仅脱敏展示用）。
func urlFullOrSources(urlFull string, sources []string) string {
	if urlFull != "" {
		return urlFull
	}
	return strings.Join(sources, "|")
}

// buildParams 组装日志 Params（template_id 仅非空时写入）。
func buildParams(include, exclude, rename string, udp, tls13, scv, stripEmoji bool, templateID string) map[string]any {
	m := map[string]any{
		"include": include, "exclude": exclude, "rename": rename,
		"udp": udp, "tls13": tls13, "scv": scv, "strip_emoji": stripEmoji,
	}
	if templateID != "" {
		m["template_id"] = templateID
	}
	return m
}

// strParam / boolParam 从日志 Params 读取参数（缺失/类型不符取零值）。
func strParam(p map[string]any, k string) string {
	v, _ := p[k].(string)
	return v
}

func boolParam(p map[string]any, k string) bool {
	v, _ := p[k].(bool)
	return v
}

// statusOf / errOf / nodeCountOf 从管线结果构造日志字段。
func statusOf(perr *pipelineError) string {
	if perr != nil {
		return "fail"
	}
	return "ok"
}

func errOf(perr *pipelineError) *string {
	if perr == nil {
		return nil
	}
	msg := perr.msg // 管线错误消息已在生成时脱敏（不含完整订阅 URL）
	return &msg
}

func nodeCountOf(res *convertResult, perr *pipelineError) int {
	if perr != nil || res == nil {
		return 0
	}
	return res.nodeCount
}

// appendLog 追加转换日志；store 落盘失败只 warn，不影响转换主流程。
func (s *server) appendLog(e store.LogEntry) {
	if s.st == nil {
		return
	}
	if _, err := s.st.AppendLog(e); err != nil {
		s.logger.Warn("append conversion log failed", "error", err)
	}
}

// logSubError 记录 /sub 参数错误日志（Kind=sub, Status=fail）并写 400 响应，
// 保证「每次请求（成功/失败）都记录转换日志」的语义在参数错误分支同样成立。
// 错误消息已脱敏（不含完整订阅 URL）；rawURL 为空（无 url 参数）时
// URLRedacted/URLFull 为空；src 场景把请求的 src ID 记入 SourceID。
func (s *server) logSubError(w http.ResponseWriter, r *http.Request, start time.Time, msg, srcID, srcName, rawURL string, q url.Values) {
	errMsg := msg
	s.appendLog(store.LogEntry{
		Kind:        "sub",
		SourceID:    srcID,
		SourceName:  srcName,
		URLRedacted: redactURL(rawURL),
		URLFull:     rawURL,
		Params:      buildParams(q.Get("include"), q.Get("exclude"), q.Get("rename"), truthy(q.Get("udp")), truthy(q.Get("tls13")), truthy(q.Get("scv")), truthy(q.Get("strip_emoji")), ""),
		Status:      "fail",
		Error:       &errMsg,
		DurationMS:  time.Since(start).Milliseconds(),
	})
	writeJSONError(w, http.StatusBadRequest, msg)
}

// strPtr 返回字符串指针（日志 Error 字段用）。
func strPtr(s string) *string { return &s }

// decodeJSONBody 限制请求体 1MB（MaxBytesReader）后解码 JSON；超限或
// JSON 非法返回描述性 error（由调用方映射 400）。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return errors.New("request body too large")
		}
		return errors.New("invalid json body")
	}
	return nil
}

// writeStoreError 统一映射 store 层错误：ErrNotFound → 404、ErrInvalid → 400、
// 其余（磁盘 IO/序列化等内部错误）→ 500——真实错误记日志，响应只给通用消息
// 「internal error」，避免把磁盘路径等内部信息暴露给客户端。
func (s *server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrInvalid):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		s.logger.Error("store operation failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
	}
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError 写 JSON 错误响应 {"error": msg}。
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
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
