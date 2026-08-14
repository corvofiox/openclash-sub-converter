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
	version = "0.3.0"
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
		admin.HandleFunc("GET /api/v1/rule-sets", s.handleListRuleSets)
		admin.HandleFunc("POST /api/v1/rule-sets", s.handleCreateRuleSet)
		admin.HandleFunc("PUT /api/v1/rule-sets/{id}", s.handleUpdateRuleSet)
		admin.HandleFunc("DELETE /api/v1/rule-sets/{id}", s.handleDeleteRuleSet)
		admin.HandleFunc("POST /api/v1/rule-sets/probe", s.handleProbeRuleSet)
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
	nodes       []map[string]any // transform 后的节点（name/type 等）
	groups      []map[string]any // groups.Build 产物
	cfg         map[string]any   // template.Build 产物（含规则集注入）
	data        []byte           // 渲染后的 YAML（render=false 时为空，失败源告警注释已 prepend）
	nodeCount   int
	warnings    []string // 失败源告警（"host: error" 预格式化，sanitizeErr 已脱敏）；无失败时为空数组
	failedHosts []string // 失败源 host 列表（响应头 X-Osc-Warning 用，不含 error）
}

// runPipeline 执行转换管线：拉取→解析→节点级校验→transform→groups→
// ApplyStripEmoji（strip_emoji=true 时剥离节点名 emoji，可选）→
// template（可选规则集注入）→（render=true 时）渲染+校验。
//
// 语义分层：结构非法的源 URL 由调用方在进入本函数前校验（400）；结构合法但
// 拉取/解析失败的源记 warn 并跳过，全部失败 → 502；transform 参数错误
// （transform.ErrInvalidRegex）→ 400；其余内部错误 → 500。
func (s *server) runPipeline(r *http.Request, srcs []string, filter transform.Filter, opts template.Options, ruleSets []store.RuleSet, render bool) (*convertResult, *pipelineError) {
	ctx := r.Context()
	var allNodes []map[string]any
	failedHosts := make([]string, 0)
	warnings := make([]string, 0) // R2：失败源告警（"host: error"），空数组保证 JSON 输出 []
	for _, src := range srcs {
		host := hostOf(src)
		body, err := s.f.Fetch(ctx, src)
		if err != nil {
			wmsg := sanitizeErr(err, src, host)
			s.logger.Warn("fetch subscription source failed", "host", host, "error", wmsg)
			failedHosts = append(failedHosts, host)
			warnings = append(warnings, host+": "+wmsg)
			continue
		}
		nodes, err := link.ParseSubscription(body, host)
		if err != nil {
			wmsg := sanitizeErr(err, src, host)
			s.logger.Warn("parse subscription source failed", "host", host, "error", wmsg)
			failedHosts = append(failedHosts, host)
			warnings = append(warnings, host+": "+wmsg)
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
	// R3 规则集专属策略组：必须先于 template.Build 追加到 groupsList（Build 会
	// 把组列表固化进 cfgMap["proxy-groups"]，事后 append 不生效）。
	// 组 = select，proxies = [手动选择, ...手动选择组 proxies]：首位引用「手动
	// 选择」组（用户可在专属组内跟随手动选择），其后为「手动选择」组 proxies 的
	// 深拷贝（自动选择/地区组/其他节点/直连/拒绝等组引用，避免共享底层数组）；
	// 组名与已有组（手动选择/自动选择/地区组/其他节点/直连/拒绝/已加规则集组）
	// 冲突时加「(规则集)」后缀递增。RULE-SET,<规则集名>,<最终组名> 行在
	// cfgMap 构建后由 ApplyRuleProviders 一次性注入（cfg["rule-providers"]
	// 整体覆盖，多规则集严禁逐次调用）。
	var rps []template.RuleProvider
	if len(ruleSets) > 0 {
		// P1-2：同名规则集（不同 id）会让 rule-providers map 键互相覆盖、前者
		// URL 静默丢失（两个专属组的 RULE-SET 都引用后者规则集）→ 400 拒绝整个
		// 请求。store 无名称唯一性约束，这里在构造 rps 前显式拦截。
		seenNames := make(map[string]bool, len(ruleSets))
		for _, rs := range ruleSets {
			if seenNames[rs.Name] {
				return nil, &pipelineError{code: http.StatusBadRequest, msg: fmt.Sprintf("规则集名称冲突: %s(存在同名规则集)", rs.Name)}
			}
			seenNames[rs.Name] = true
		}
		usedNames := make(map[string]bool, len(groupsList)+len(ruleSets))
		for _, g := range groupsList {
			if n, ok := g["name"].(string); ok {
				usedNames[n] = true
			}
		}
		// P1-1：专属组名还必须避开节点最终名与 mihomo 内置出站名 DIRECT/REJECT——
		// 组名与节点重名或等于内置出站名时 mihomo 报 duplicate name 拒绝加载，而
		// output.Validate 只做语法层校验会放行（静默交付坏配置）。nodes 在此处已是
		// strip_emoji + uniqueName 去重后的最终名，与 proxies 段一致。
		for _, n := range nodes {
			if name, ok := n["name"].(string); ok {
				usedNames[name] = true
			}
		}
		usedNames["DIRECT"] = true
		usedNames["REJECT"] = true
		rps = make([]template.RuleProvider, 0, len(ruleSets))
		for _, rs := range ruleSets {
			gname := uniqueRuleSetGroupName(rs.Name, usedNames)
			usedNames[gname] = true
			// 专属组 proxies = [手动选择, ...手动选择组 proxies]：首位引用「手动
			// 选择」组（groupsList[0]，Build 保证首元素是手动选择组），用户可在
			// 专属组内跟随手动选择；其后复制手动组 proxies（自动选择/地区组/
			// 直连/拒绝等组引用，深拷贝避免共享底层数组）。复制在 ApplyStripEmoji
			// 之后，节点名已是最终名。
			var proxies []any
			if len(groupsList) > 0 {
				if src, ok := groupsList[0]["proxies"].([]any); ok {
					proxies = append([]any{groups.GroupManual}, src...)
				}
			}
			if proxies == nil {
				// 防御性回退：groupsList 为空或缺 proxies → 手动选择 + 全节点名 + 直连 + 拒绝
				proxies = make([]any, 0, len(nodes)+3)
				proxies = append(proxies, groups.GroupManual)
				for _, n := range nodes {
					proxies = append(proxies, n["name"])
				}
				proxies = append(proxies, groups.GroupDirect, groups.GroupReject)
			}
			groupsList = append(groupsList, map[string]any{
				"name": gname, "type": "select", "proxies": proxies,
			})
			rps = append(rps, template.RuleProvider{
				Name: rs.Name, URL: rs.URL, Behavior: rs.Behavior, Format: rs.Format,
				TargetGroup: gname,
			})
		}
	}
	cfgMap, err := template.Build(nodes, groupsList, opts)
	if err != nil {
		return nil, &pipelineError{code: http.StatusInternalServerError, msg: fmt.Sprintf("build config failed: %v", err)}
	}
	if len(rps) > 0 {
		if err := template.ApplyRuleProviders(cfgMap, rps); err != nil {
			return nil, &pipelineError{code: http.StatusInternalServerError, msg: fmt.Sprintf("apply rule providers failed: %v", err)}
		}
	}
	res := &convertResult{nodes: nodes, groups: groupsList, cfg: cfgMap, nodeCount: len(nodes), warnings: warnings, failedHosts: failedHosts}
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
	// R2：失败源告警注释 prepend 到最终 data 字节（YAML 顶部、mixed-port 之前）。
	// 严禁改 cfgMap/yaml.Node 结构（段序保序依赖 cfgMap 结构不变）；注释不参与
	// YAML 解析（output.Validate 已在无注释数据上通过）。换行替换防多行错误破坏注释。
	if len(warnings) > 0 {
		var sb strings.Builder
		for _, w := range warnings {
			sb.WriteString("# [OSC-WARNING] ")
			sb.WriteString(strings.ReplaceAll(w, "\n", " "))
			sb.WriteString("\n")
		}
		data = append([]byte(sb.String()), data...)
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
// 参数：target 必须为 "clash"；数据源 = src=<ID1,ID2>（逗号多值，重复 ID 合并）
// 与/或 url=（多个源用 | 分隔），二者可混合聚合（已存源在前、url 源在后，
// 凭证不进 URL）；include/exclude/rename 为可选正则；udp/tls13/scv/
// strip_emoji 取值 "true"/"1" 视为 true。
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

	// R4 数据源解析：src=<ID1,ID2> 逗号多值（重复 ID 合并一次），可与 url=
	// （多个源用 | 分隔）混合聚合——已存源在前、临时 url 源在后；两者皆缺 → 400。
	// 任一 src ID 不存在或禁用 → 400 且消息含该 ID（s.st==nil 时保持原消息）。
	var sources []string
	var srcID, srcName, urlFull string
	if srcParam := q.Get("src"); srcParam != "" {
		if s.st == nil {
			s.logSubError(w, r, start, "src 引用的订阅源不可用", srcParam, "", "", q)
			return
		}
		ids := splitIDs(srcParam)
		if len(ids) == 0 {
			s.logSubError(w, r, start, "src 参数未包含任何订阅源 ID", srcParam, "", "", q)
			return
		}
		urls, idsOut, names, badID, ok := resolveSourceIDs(s.st, ids)
		if !ok {
			msg := "src 引用的订阅源不可用"
			if badID != "" {
				msg += ": " + badID
			}
			s.logSubError(w, r, start, msg, srcParam, "", "", q)
			return
		}
		sources = append(sources, urls...)
		srcID, srcName = idsOut, names // 逗号多值（顺序 = 参数顺序，重复已合并）
	}
	if rawURL := q.Get("url"); rawURL != "" {
		urlSrcs := splitSources(rawURL)
		if len(urlSrcs) == 0 {
			s.logSubError(w, r, start, "url parameter contains no subscription sources", srcID, srcName, rawURL, q)
			return
		}
		// BUG 修复：结构非法（不可解析/非 http/https/无 host）→ 400 客户端错误
		if err := validateSources(urlSrcs); err != nil {
			s.logSubError(w, r, start, err.Error(), srcID, srcName, rawURL, q)
			return
		}
		sources = append(sources, urlSrcs...)
		urlFull = rawURL // 临时 url= 参数才存完整 URL（retry 用）
	}
	if len(sources) == 0 {
		s.logSubError(w, r, start, "missing required parameter: src or url", "", "", "", q)
		return
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

	// R4：ruleset_id 逗号分隔多值；任一规则集不存在或禁用 → 400
	ruleSets, perr := s.resolveRuleSets(q.Get("ruleset_id"))
	if perr != nil {
		s.logSubError(w, r, start, perr.msg, srcID, srcName, urlFull, q)
		return
	}
	res, perr := s.runPipeline(r, sources, filter, opts, ruleSets, true)
	s.appendLog(store.LogEntry{
		Kind:        "sub",
		SourceID:    srcID,
		SourceName:  srcName,
		URLRedacted: redactURL(urlFullOrSources(urlFull, sources)),
		URLFull:     urlFull,
		Params:      buildParams(q.Get("include"), q.Get("exclude"), q.Get("rename"), opts.UDP, opts.TLS13, opts.SCV, truthy(q.Get("strip_emoji")), q.Get("ruleset_id")),
		Status:      statusOf(perr),
		Error:       errOf(perr),
		NodeCount:   nodeCountOf(res, perr),
		DurationMS:  time.Since(start).Milliseconds(),
	})
	if perr != nil {
		writeJSONError(w, perr.code, perr.msg)
		return
	}

	// R2：存在失败源时设置告警头（只放 host，不放 error）；无失败不设（响应字节级不变）
	if len(res.failedHosts) > 0 {
		w.Header().Set("X-Osc-Warning", strings.Join(res.failedHosts, ", "))
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.data)
}

// convertRequest 是 /api/v1/convert/preview 与 /api/v1/convert/run 的 JSON 请求体。
type convertRequest struct {
	SourceID   *string  `json:"source_id"`  // 单值兼容（与 source_ids 并存 → 400）
	SourceIDs  []string `json:"source_ids"` // 数组多值；空数组视为未提供
	URL        *string  `json:"url"`        // 临时订阅 URL（可含 | 多源），可与上两者混合
	Include    string   `json:"include"`
	Exclude    string   `json:"exclude"`
	Rename     string   `json:"rename"`
	UDP        *bool    `json:"udp"`
	TLS13      *bool    `json:"tls13"`
	SCV        *bool    `json:"scv"`
	StripEmoji *bool   `json:"strip_emoji"`
	RuleSetID  string  `json:"ruleset_id"`
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

		// R4 数据源解析：source_ids（数组多值）与 source_id（单值兼容）二选一，
		// 可与 url（临时）混合聚合——已存源在前、url 源在后；全部为空 → 400。
		var sources []string
		var srcID, srcName, urlFull string
		sid := ""
		if req.SourceID != nil {
			sid = *req.SourceID
		}
		sids := make([]string, 0, len(req.SourceIDs))
		for _, id := range req.SourceIDs {
			if id = strings.TrimSpace(id); id != "" {
				sids = append(sids, id)
			}
		}
		if sid != "" && len(sids) > 0 {
			writeJSONError(w, http.StatusBadRequest, "source_id 与 source_ids 不能同时指定")
			return
		}
		if sid != "" {
			sids = []string{sid}
		}
		if len(sids) > 0 {
			urls, idsOut, names, badID, ok := resolveSourceIDs(s.st, sids)
			if !ok {
				msg := "订阅源不存在或已禁用"
				if badID != "" {
					msg += ": " + badID
				}
				writeJSONError(w, http.StatusBadRequest, msg)
				return
			}
			sources = append(sources, urls...)
			srcID, srcName = idsOut, names
		}
		if req.URL != nil && *req.URL != "" {
			urlSrcs := splitSources(*req.URL)
			if len(urlSrcs) == 0 {
				writeJSONError(w, http.StatusBadRequest, "url 不包含任何订阅源")
				return
			}
			if err := validateSources(urlSrcs); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			sources = append(sources, urlSrcs...)
			urlFull = *req.URL
		}
		if len(sources) == 0 {
			writeJSONError(w, http.StatusBadRequest, "请指定 source_id/source_ids 或 url")
			return
		}

		// 规则集：ruleset_id 逗号分隔多值；任一规则集不存在或禁用 → 400
		ruleSets, perr := s.resolveRuleSets(req.RuleSetID)
		if perr != nil {
			writeJSONError(w, perr.code, perr.msg)
			return
		}

		filter := transform.Filter{Rename: req.Rename, Include: req.Include, Exclude: req.Exclude, StripEmoji: boolVal(req.StripEmoji)}
		opts := template.Options{UDP: boolVal(req.UDP), TLS13: boolVal(req.TLS13), SCV: boolVal(req.SCV)}

		res, perr := s.runPipeline(r, sources, filter, opts, ruleSets, render)
		s.appendLog(store.LogEntry{
			Kind:        kind,
			SourceID:    srcID,
			SourceName:  srcName,
			URLRedacted: redactURL(urlFullOrSources(urlFull, sources)),
			URLFull:     urlFull,
			Params:      buildParams(req.Include, req.Exclude, req.Rename, opts.UDP, opts.TLS13, opts.SCV, boolVal(req.StripEmoji), req.RuleSetID),
			Status:      statusOf(perr),
			Error:       errOf(perr),
			NodeCount:   nodeCountOf(res, perr),
			DurationMS:  time.Since(start).Milliseconds(),
		})
		if perr != nil {
			writeJSONError(w, perr.code, perr.msg)
			return
		}

		// R2：存在失败源时设置告警头（只放 host）；JSON 响应带 warnings 数组
		if len(res.failedHosts) > 0 {
			w.Header().Set("X-Osc-Warning", strings.Join(res.failedHosts, ", "))
		}
		duration := time.Since(start).Milliseconds()
		if render {
			writeJSON(w, http.StatusOK, map[string]any{
				"yaml":        string(res.data),
				"node_count":  res.nodeCount,
				"duration_ms": duration,
				"warnings":    res.warnings,
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
			"warnings":    res.warnings,
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

	// R4 多源恢复：SourceID（逗号多值）逐 ID 校验后与 URLFull（临时 url 部分）
	// 顺序合并——已存源在前、url 源在后；任一源已删/禁用 → 409；两者皆缺 → 400。
	var sources []string
	var rawURL string
	if entry.SourceID != "" {
		// 重复 ID 去重：与 /sub、convert 的 resolveSourceIDs 合并语义一致
		// （日志 src=a,a → 同一源只拉一次）；仍保留「已删除/已禁用」两种 409 消息
		seen := make(map[string]bool)
		for _, id := range splitIDs(entry.SourceID) {
			if seen[id] {
				continue
			}
			seen[id] = true
			src, ok := s.st.GetSource(id)
			if !ok {
				writeJSONError(w, http.StatusConflict, "订阅源已删除")
				return
			}
			if !src.Enabled {
				writeJSONError(w, http.StatusConflict, "订阅源已禁用")
				return
			}
			sources = append(sources, src.URL)
		}
	}
	if entry.URLFull != "" {
		// 多源临时 URL：与 /sub、convert 入口一致，先拆分再校验（非法 → 400），
		// 避免把含 | 的原始串直接交给 runPipeline（逐源 Fetch 前不拆分 → 502）。
		urlSrcs := splitSources(entry.URLFull)
		if len(urlSrcs) == 0 {
			writeJSONError(w, http.StatusBadRequest, "临时 URL 不可重试")
			return
		}
		if err := validateSources(urlSrcs); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		sources = append(sources, urlSrcs...)
		rawURL = entry.URLFull
	}
	if len(sources) == 0 {
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
	var ruleSets []store.RuleSet
	if rsID := strParam(entry.Params, "ruleset_id"); rsID != "" {
		var perr *pipelineError
		if ruleSets, perr = s.resolveRuleSets(rsID); perr != nil {
			writeJSONError(w, perr.code, perr.msg)
			return
		}
	}

	res, perr := s.runPipeline(r, sources, filter, opts, ruleSets, false)
	s.appendLog(store.LogEntry{
		Kind:        "preview",
		SourceID:    entry.SourceID, // 逗号多值原样保留
		SourceName:  entry.SourceName,
		URLRedacted: redactURL(urlFullOrSources(rawURL, sources)), // 重新脱敏：源 URL 已更新时展示与实际一致
		URLFull:     entry.URLFull,                                // 保留原临时 URL（src 场景为空）
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
		"warnings":    res.warnings,
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

// ---------- 规则集 CRUD ----------

func (s *server) handleListRuleSets(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	ruleSets := s.st.ListRuleSets()
	out := make([]ruleSetResp, 0, len(ruleSets))
	for _, rs := range ruleSets {
		out = append(out, toRuleSetResp(rs))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule_sets": out})
}

type ruleSetReq struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Behavior string `json:"behavior"`
	Format   string `json:"format"`
	Enabled  *bool  `json:"enabled"`
}

// ruleSetResp 是规则集的对外响应结构：URL 脱敏后返回。
type ruleSetResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Behavior  string `json:"behavior"`
	Format    string `json:"format"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toRuleSetResp(rs store.RuleSet) ruleSetResp {
	return ruleSetResp{
		ID: rs.ID, Name: rs.Name, URL: redactURL(rs.URL), Behavior: rs.Behavior, Format: rs.Format,
		Enabled: rs.Enabled, CreatedAt: rs.CreatedAt, UpdatedAt: rs.UpdatedAt,
	}
}

func (s *server) handleCreateRuleSet(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	var req ruleSetReq
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
	rs, err := s.st.CreateRuleSet(req.Name, req.URL, req.Behavior, req.Format, boolVal(req.Enabled))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rule_set": toRuleSetResp(rs)})
}

func (s *server) handleUpdateRuleSet(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	var patch store.RuleSetPatch
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
	rs, err := s.st.UpdateRuleSet(r.PathValue("id"), patch)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule_set": toRuleSetResp(rs)})
}

func (s *server) handleDeleteRuleSet(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	if err := s.st.DeleteRuleSet(r.PathValue("id")); err != nil {
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

// uniqueRuleSetGroupName 为规则集专属策略组分配唯一组名：与已有组名（手动选择/
// 自动选择/地区组/其他节点/直连/拒绝/已添加的规则集组）冲突时追加「(规则集)」
// 后缀，仍冲突则递增「(规则集)2」「(规则集)3」……直到唯一。调用方须在 used
// 中登记新名。
func uniqueRuleSetGroupName(base string, used map[string]bool) string {
	if !used[base] {
		return base
	}
	cand := base + "(规则集)"
	for i := 2; used[cand]; i++ {
		cand = base + "(规则集)" + strconv.Itoa(i)
	}
	return cand
}

// resolveRuleSets 解析逗号分隔的 ruleset_id 列表并逐个校验（存在且启用）；
// 任一不存在或 disabled → 400「规则集不存在或已禁用」。空串 → 返回 nil（无规则集）。
// handleSub 在 st==nil（未挂载 store）时传 ruleset_id 同样按不存在处理。
func (s *server) resolveRuleSets(raw string) ([]store.RuleSet, *pipelineError) {
	if raw == "" {
		return nil, nil
	}
	if s.st == nil {
		return nil, &pipelineError{code: http.StatusBadRequest, msg: "规则集不存在或已禁用"}
	}
	ids := strings.Split(raw, ",")
	ruleSets := make([]store.RuleSet, 0, len(ids))
	// P2-2：重复 ruleset_id（如 a,a）去重只保留第一个，避免同一规则集生成两份
	// 专属组与 RULE-SET。
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		rs, ok := s.st.GetRuleSet(id)
		if !ok || !rs.Enabled {
			return nil, &pipelineError{code: http.StatusBadRequest, msg: "规则集不存在或已禁用"}
		}
		ruleSets = append(ruleSets, rs)
	}
	return ruleSets, nil
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

// splitIDs 按逗号拆分 ID 列表，去空白与空项（与 ruleset_id 解析一致）。
func splitIDs(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// resolveSourceIDs 逐个校验订阅源 ID（存在且启用），返回按参数顺序的 URL
// 列表与逗号拼接的 ID/名称（重复 ID 合并一次）。任一不存在或禁用 →
// ok=false 且 badID 为该 ID；st == nil 时 badID 为空（调用方保持原错误消息）。
// handleSub 与 handleConvert 共用；handleSub 中 s.st==nil 检查保持在调用前。
func resolveSourceIDs(st *store.Store, ids []string) (urls []string, idList, nameList, badID string, ok bool) {
	if st == nil {
		return nil, "", "", "", false
	}
	seen := make(map[string]bool, len(ids))
	idsOut := make([]string, 0, len(ids))
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		src, found := st.GetSource(id)
		if !found || !src.Enabled {
			return nil, "", "", id, false
		}
		urls = append(urls, src.URL)
		idsOut = append(idsOut, src.ID)
		names = append(names, src.Name)
	}
	return urls, strings.Join(idsOut, ","), strings.Join(names, ","), "", true
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

// buildParams 组装日志 Params（ruleset_id 仅非空时写入）。
func buildParams(include, exclude, rename string, udp, tls13, scv, stripEmoji bool, ruleSetID string) map[string]any {
	m := map[string]any{
		"include": include, "exclude": exclude, "rename": rename,
		"udp": udp, "tls13": tls13, "scv": scv, "strip_emoji": stripEmoji,
	}
	if ruleSetID != "" {
		m["ruleset_id"] = ruleSetID
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
		Params:      buildParams(q.Get("include"), q.Get("exclude"), q.Get("rename"), truthy(q.Get("udp")), truthy(q.Get("tls13")), truthy(q.Get("scv")), truthy(q.Get("strip_emoji")), q.Get("ruleset_id")),
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
