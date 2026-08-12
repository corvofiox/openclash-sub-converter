// 管理台 REST API / 新增行为测试：BUG 回归（非法 url 400）、src 参数、
// 转换日志、sources/templates CRUD、convert preview/run、logs retry、
// auth 中间件、正则错误 400 映射。
package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/yangyu/openclash-sub-converter/internal/config"
	"github.com/yangyu/openclash-sub-converter/internal/fetcher"
	"github.com/yangyu/openclash-sub-converter/internal/store"
)

// doJSON 执行一次任意方法/带 body/带请求头的请求。
func doJSON(h http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodeErr 解析 {"error": ...} 响应。
func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error == "" {
		t.Fatalf("body = %q, want json error message", rec.Body.String())
	}
	return e.Error
}

// createSourceViaAPI 通过管理台 API 创建订阅源，返回 JSON 里的 source 对象。
func createSourceViaAPI(t *testing.T, h http.Handler, name, rawURL string) map[string]any {
	t.Helper()
	payload := fmt.Sprintf(`{"name":%q,"url":%q}`, name, rawURL)
	rec := doJSON(h, http.MethodPost, "/api/v1/sources", payload, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create source status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Source map[string]any `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Source == nil {
		t.Fatalf("create source body = %q, want source object", rec.Body.String())
	}
	return resp.Source
}

// createTemplateViaAPI 通过管理台 API 创建规则模板，返回 JSON 里的 template 对象。
func createTemplateViaAPI(t *testing.T, h http.Handler, name, rawURL, behavior, format string) map[string]any {
	t.Helper()
	payload := fmt.Sprintf(`{"name":%q,"url":%q,"behavior":%q,"format":%q,"enabled":true}`, name, rawURL, behavior, format)
	rec := doJSON(h, http.MethodPost, "/api/v1/templates", payload, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create template status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Template map[string]any `json:"template"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Template == nil {
		t.Fatalf("create template body = %q, want template object", rec.Body.String())
	}
	return resp.Template
}

// TestSubInvalidURLReturns400 断言 BUG 修复：结构非法的 url 参数返回 400
// 而非 502（客户端错误语义分层），错误消息不含原始 URL。
func TestSubInvalidURLReturns400(t *testing.T) {
	h := newTestServer(t)
	cases := []struct {
		name string
		url  string
	}{
		{"not-a-url", "notaurl"},
		{"ftp scheme", "ftp://x"},
		{"no scheme no host", "//nohost"},
		{"mixed valid plus invalid", "http://ok.example.com/sub|notaurl"},
	}
	for _, tc := range cases {
		rec := do(h, subQuery(tc.url, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body=%s", tc.name, rec.Code, rec.Body.String())
			continue
		}
		msg := decodeErr(t, rec)
		if strings.Contains(msg, tc.url) {
			t.Errorf("%s: error message leaks raw url %q: %q", tc.name, tc.url, msg)
		}
	}
	// 结构合法但拉取失败仍为 502（语义分层不受影响）
	rec := do(h, subQuery("http://127.0.0.1:1/dead", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("structurally valid but unreachable: status = %d, want 502", rec.Code)
	}
}

// TestSubSrcParam 断言 src=<订阅源ID> 参数：正常转换 / 不存在 / 禁用 /
// 与 url 同时存在时 src 优先。
func TestSubSrcParam(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())
	s, err := st.CreateSource("机场A", src.URL, true)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	// 正常：src 转换成功且输出源节点
	rec := do(h, "/sub?target=clash&src="+s.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("src ok: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "香港-01") {
		t.Error("src ok: missing 香港-01 in output")
	}

	// 不存在 → 400
	rec = do(h, "/sub?target=clash&src=deadbeef0000")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("src missing: status = %d, want 400", rec.Code)
	}

	// 禁用 → 400
	disabled, err := st.CreateSource("禁用源", src.URL, false)
	if err != nil {
		t.Fatalf("CreateSource disabled: %v", err)
	}
	rec = do(h, "/sub?target=clash&src="+disabled.ID)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("src disabled: status = %d, want 400", rec.Code)
	}

	// src 与 url 同时存在 → src 优先（输出 src 的节点，忽略 url）
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	other := fakeSource(t, http.StatusOK, fmt.Sprintf("ss://%s@other.example.com:8388#OTHER-01\n", ui))
	vals := url.Values{}
	vals.Set("target", "clash")
	vals.Set("src", s.ID)
	vals.Set("url", other.URL)
	rec = do(h, "/sub?"+vals.Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("src priority: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "香港-01") {
		t.Errorf("src priority: missing src node 香港-01")
	}
	if strings.Contains(body, "OTHER-01") {
		t.Errorf("src priority: url node OTHER-01 should be ignored when src present")
	}
}

// TestSubLogsRecorded 断言 /sub 成功/失败都记录转换日志（Kind "sub"），
// URL 脱敏、失败带 error、响应不含 url_full。
func TestSubLogsRecorded(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())
	// 带凭证的 URL（userinfo + query token）
	credsURL := "http://user:pass@" + strings.TrimPrefix(src.URL, "http://") + "/sub?token=SECRET"
	rec := do(h, subQuery(credsURL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// 失败请求
	rec = do(h, subQuery("http://127.0.0.1:1/dead", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("fail status = %d, want 502", rec.Code)
	}

	logs := st.ListLogs(10)
	if len(logs) != 2 {
		t.Fatalf("logs len = %d, want 2", len(logs))
	}
	// 最新在前：失败条目
	failLog := logs[0]
	if failLog.Kind != "sub" || failLog.Status != "fail" || failLog.Error == nil {
		t.Errorf("fail log = %+v, want kind=sub status=fail error!=nil", failLog)
	}
	// 成功条目：节点数、脱敏 URL
	okLog := logs[1]
	if okLog.Kind != "sub" || okLog.Status != "ok" || okLog.NodeCount != 2 {
		t.Errorf("ok log = %+v, want kind=sub status=ok node_count=2", okLog)
	}
	if !strings.Contains(okLog.URLRedacted, "127.0.0.1") {
		t.Errorf("url_redacted should contain host, got %q", okLog.URLRedacted)
	}
	for _, leak := range []string{"user:pass", "token=SECRET"} {
		if strings.Contains(okLog.URLRedacted, leak) {
			t.Errorf("url_redacted leaks %q: %q", leak, okLog.URLRedacted)
		}
	}
	if okLog.URLFull == "" {
		t.Error("url= 场景应保存完整 URL 供 retry")
	}

	// 列表响应结构：不含 url_full 字段
	rec = doJSON(h, http.MethodGet, "/api/v1/logs", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("logs list status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "url_full") {
		t.Errorf("logs response must not contain url_full: %s", rec.Body.String())
	}
}

// TestLogsLimit 断言 limit 参数生效（默认 50、上限 200 由 store 兜底）。
func TestLogsLimit(t *testing.T) {
	h, st := newTestServerWithStore(t)
	for i := 0; i < 5; i++ {
		if _, err := st.AppendLog(store.LogEntry{Kind: "sub", Status: "ok"}); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	rec := doJSON(h, http.MethodGet, "/api/v1/logs?limit=2", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Logs []logEntryResp `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(resp.Logs) != 2 {
		t.Errorf("limit=2: logs len = %d, want 2", len(resp.Logs))
	}
}

// TestSourcesCRUD 覆盖 /api/v1/sources 全端点。
func TestSourcesCRUD(t *testing.T) {
	h := newTestServer(t)

	// 空列表
	rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"sources":[]`) {
		t.Errorf("empty list body = %s", rec.Body.String())
	}

	// 创建校验：缺 name / 缺 url / url 非法 → 400
	for _, body := range []string{
		`{"url":"http://x.example.com/sub"}`,
		`{"name":"a"}`,
		`{"name":"a","url":"notaurl"}`,
		`{"name":"a","url":"ftp://x"}`,
	} {
		if rec := doJSON(h, http.MethodPost, "/api/v1/sources", body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("create %s: status = %d, want 400", body, rec.Code)
		}
	}

	// 创建成功：URL 脱敏
	raw := "http://user:pass@example.com/sub?token=SECRET"
	src := createSourceViaAPI(t, h, "机场A", raw)
	id, _ := src["id"].(string)
	if id == "" {
		t.Fatal("created source missing id")
	}
	gotURL, _ := src["url"].(string)
	if !strings.Contains(gotURL, "token=xxxxx") || strings.Contains(gotURL, "SECRET") || strings.Contains(gotURL, "user:pass") {
		t.Errorf("created source url not redacted: %q", gotURL)
	}

	// 列表返回脱敏 URL
	rec = doJSON(h, http.MethodGet, "/api/v1/sources", "", nil)
	if !strings.Contains(rec.Body.String(), "token=xxxxx") || strings.Contains(rec.Body.String(), "SECRET") {
		t.Errorf("list leaks credentials: %s", rec.Body.String())
	}

	// 部分更新：改 name 与 enabled，URL 省略保留原值
	rec = doJSON(h, http.MethodPut, "/api/v1/sources/"+id, `{"name":"机场B","enabled":true}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var upd struct {
		Source map[string]any `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &upd); err != nil {
		t.Fatalf("update body not json: %v", err)
	}
	if upd.Source["name"] != "机场B" || upd.Source["enabled"] != true {
		t.Errorf("update result = %v, want name=机场B enabled=true", upd.Source)
	}
	if u, _ := upd.Source["url"].(string); !strings.Contains(u, "token=xxxxx") {
		t.Errorf("update should keep original url, got %v", upd.Source["url"])
	}

	// 更新校验：非法 url → 400；不存在的 id → 404
	if rec := doJSON(h, http.MethodPut, "/api/v1/sources/"+id, `{"url":"notaurl"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("update bad url: status = %d, want 400", rec.Code)
	}
	if rec := doJSON(h, http.MethodPut, "/api/v1/sources/deadbeef0000", `{"name":"x"}`, nil); rec.Code != http.StatusNotFound {
		t.Errorf("update missing: status = %d, want 404", rec.Code)
	}

	// 删除 → 204；再删 → 404
	rec = doJSON(h, http.MethodDelete, "/api/v1/sources/"+id, "", nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", rec.Code)
	}
	if rec := doJSON(h, http.MethodDelete, "/api/v1/sources/"+id, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing: status = %d, want 404", rec.Code)
	}
}

// TestTemplatesCRUD 覆盖 /api/v1/templates 全端点。
func TestTemplatesCRUD(t *testing.T) {
	h := newTestServer(t)

	// 创建校验
	for _, body := range []string{
		`{"url":"http://x.example.com/rules.yaml","behavior":"domain","format":"yaml"}`,
		`{"name":"t","behavior":"domain","format":"yaml"}`,
		`{"name":"t","url":"http://x.example.com/rules.yaml","behavior":"weird","format":"yaml"}`,
		`{"name":"t","url":"http://x.example.com/rules.yaml","behavior":"domain","format":"xml"}`,
	} {
		if rec := doJSON(h, http.MethodPost, "/api/v1/templates", body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("create %s: status = %d, want 400", body, rec.Code)
		}
	}

	tpl := createTemplateViaAPI(t, h, "广告拦截", "http://x.example.com/rules.yaml?token=SECRET", "domain", "yaml")
	id, _ := tpl["id"].(string)
	if id == "" {
		t.Fatal("created template missing id")
	}
	if u, _ := tpl["url"].(string); strings.Contains(u, "SECRET") || !strings.Contains(u, "token=xxxxx") {
		t.Errorf("template url not redacted: %q", tpl["url"])
	}

	// 部分更新
	rec := doJSON(h, http.MethodPut, "/api/v1/templates/"+id, `{"enabled":true,"format":"text"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var upd struct {
		Template map[string]any `json:"template"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &upd); err != nil {
		t.Fatalf("update body not json: %v", err)
	}
	if upd.Template["enabled"] != true || upd.Template["format"] != "text" {
		t.Errorf("update result = %v, want enabled=true format=text", upd.Template)
	}
	if rec := doJSON(h, http.MethodPut, "/api/v1/templates/"+id, `{"behavior":"bad"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("update bad behavior: status = %d, want 400", rec.Code)
	}
	if rec := doJSON(h, http.MethodPut, "/api/v1/templates/deadbeef0000", `{"name":"x"}`, nil); rec.Code != http.StatusNotFound {
		t.Errorf("update missing: status = %d, want 404", rec.Code)
	}

	// 列表
	rec = doJSON(h, http.MethodGet, "/api/v1/templates", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"templates"`) {
		t.Errorf("list status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// 删除
	if rec := doJSON(h, http.MethodDelete, "/api/v1/templates/"+id, "", nil); rec.Code != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", rec.Code)
	}
	if rec := doJSON(h, http.MethodDelete, "/api/v1/templates/"+id, "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing: status = %d, want 404", rec.Code)
	}
}

// TestConvertPreview 断言 preview 端点：url/source_id 两种来源、响应结构。
func TestConvertPreview(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())

	// url 来源
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"url":%q}`, src.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview url status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Nodes      []map[string]string `json:"nodes"`
		NodeCount  int                 `json:"node_count"`
		Groups     []map[string]any    `json:"groups"`
		DurationMS int64               `json:"duration_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("preview body not json: %v", err)
	}
	if resp.NodeCount != 2 || len(resp.Nodes) != 2 {
		t.Errorf("node_count = %d, nodes len = %d, want 2/2", resp.NodeCount, len(resp.Nodes))
	}
	if resp.Nodes[0]["name"] == "" || resp.Nodes[0]["type"] != "ss" {
		t.Errorf("nodes[0] = %v, want name+type=ss", resp.Nodes[0])
	}
	if len(resp.Groups) == 0 {
		t.Error("groups should be non-empty")
	}

	// source_id 来源
	s, err := st.CreateSource("机场A", src.URL, true)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_id":%q}`, s.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview source_id status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// 全部源失败 → 502
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", `{"url":"http://127.0.0.1:1/dead"}`, nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("preview all-fail status = %d, want 502", rec.Code)
	}
}

// TestConvertValidation 断言 convert 端点参数校验。
func TestConvertValidation(t *testing.T) {
	h, st := newTestServerWithStore(t)

	// 都缺 → 400
	if rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", `{}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no source: status = %d, want 400", rec.Code)
	}
	// url 非法 → 400
	if rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", `{"url":"notaurl"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad url: status = %d, want 400", rec.Code)
	}
	// source_id 不存在 → 400
	if rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", `{"source_id":"deadbeef0000"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing source_id: status = %d, want 400", rec.Code)
	}
	// source_id 禁用 → 400
	src := fakeSource(t, http.StatusOK, subBody())
	s, err := st.CreateSource("禁用源", src.URL, false)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_id":%q}`, s.ID), nil); rec.Code != http.StatusBadRequest {
		t.Errorf("disabled source_id: status = %d, want 400", rec.Code)
	}
	// 非法 JSON → 400
	if rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", `{not-json`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: status = %d, want 400", rec.Code)
	}
}

// TestConvertRun 断言 run 端点返回完整 YAML，且 template_id 注入 rule-providers。
func TestConvertRun(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())

	rec := doJSON(h, http.MethodPost, "/api/v1/convert/run", fmt.Sprintf(`{"url":%q}`, src.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("run status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		YAML       string `json:"yaml"`
		NodeCount  int    `json:"node_count"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("run body not json: %v", err)
	}
	if resp.NodeCount != 2 {
		t.Errorf("node_count = %d, want 2", resp.NodeCount)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(resp.YAML), &cfg); err != nil {
		t.Fatalf("yaml field is not valid yaml: %v", err)
	}

	// template_id 注入
	tpl := createTemplateViaAPI(t, h, "广告拦截", "http://x.example.com/rules.yaml", "domain", "yaml")
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/run", fmt.Sprintf(`{"url":%q,"template_id":%q}`, src.URL, tpl["id"]), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("run with template status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "rule-providers") || !strings.Contains(body, "RULE-SET,") {
		t.Errorf("run with template missing rule-providers injection")
	}

	// 模板不存在 / 禁用 → 400
	if rec := doJSON(h, http.MethodPost, "/api/v1/convert/run", `{"url":"`+src.URL+`","template_id":"deadbeef0000"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing template: status = %d, want 400", rec.Code)
	}
	if rec := doJSON(h, http.MethodPut, "/api/v1/templates/"+tpl["id"].(string), `{"enabled":false}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("disable template: status = %d", rec.Code)
	}
	if rec := doJSON(h, http.MethodPost, "/api/v1/convert/run", fmt.Sprintf(`{"url":%q,"template_id":%q}`, src.URL, tpl["id"]), nil); rec.Code != http.StatusBadRequest {
		t.Errorf("disabled template: status = %d, want 400", rec.Code)
	}
}

// TestLogsRetry 断言 retry 端点：url 日志重试、source 日志重试、404/409/400。
func TestLogsRetry(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())

	// url 临时源日志 → 重试成功（Kind "preview"）
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"url":%q,"include":"香港"}`, src.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200", rec.Code)
	}
	logs := st.ListLogs(10)
	if len(logs) != 1 {
		t.Fatalf("logs len = %d, want 1", len(logs))
	}
	id := logs[0].ID
	rec = doJSON(h, http.MethodPost, "/api/v1/logs/"+id+"/retry", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		NodeCount int `json:"node_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.NodeCount != 1 {
		t.Errorf("retry result = %+v, want node_count=1 (include=香港)", resp)
	}
	logs = st.ListLogs(10)
	if len(logs) != 2 || logs[0].Kind != "preview" {
		t.Errorf("after retry: logs len = %d, newest kind = %q, want 2 / preview", len(logs), logs[0].Kind)
	}

	// 不存在的日志 → 404
	if rec := doJSON(h, http.MethodPost, "/api/v1/logs/deadbeef0000/retry", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("retry missing log: status = %d, want 404", rec.Code)
	}

	// source 日志：源被删 → 409；源被禁用 → 409
	s, err := st.CreateSource("机场A", src.URL, true)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_id":%q}`, s.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview source_id status = %d", rec.Code)
	}
	srcLogID := st.ListLogs(10)[0].ID
	if err := st.DeleteSource(s.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	if rec := doJSON(h, http.MethodPost, "/api/v1/logs/"+srcLogID+"/retry", "", nil); rec.Code != http.StatusConflict {
		t.Errorf("retry deleted source: status = %d, want 409", rec.Code)
	}

	s2, err := st.CreateSource("机场B", src.URL, true)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_id":%q}`, s2.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview source_id status = %d", rec.Code)
	}
	srcLogID = st.ListLogs(10)[0].ID
	if _, err := st.UpdateSource(s2.ID, store.SourcePatch{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable source: %v", err)
	}
	if rec := doJSON(h, http.MethodPost, "/api/v1/logs/"+srcLogID+"/retry", "", nil); rec.Code != http.StatusConflict {
		t.Errorf("retry disabled source: status = %d, want 409", rec.Code)
	}

	// 无 SourceID 且无 URLFull 的日志 → 400
	orphan, err := st.AppendLog(store.LogEntry{Kind: "sub", Status: "ok"})
	if err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if rec := doJSON(h, http.MethodPost, "/api/v1/logs/"+orphan.ID+"/retry", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("retry no-url log: status = %d, want 400", rec.Code)
	}
}

func boolPtr(b bool) *bool { return &b }

// TestAuthMiddleware 断言管理台鉴权：无 token 全开放；有 token 时
// X-Token / Authorization: Bearer 通过，未带/错误 401；/sub 永不鉴权。
func TestAuthMiddleware(t *testing.T) {
	// 无 token → 管理台开放
	open := newTestServer(t)
	if rec := doJSON(open, http.MethodGet, "/api/v1/sources", "", nil); rec.Code != http.StatusOK {
		t.Errorf("no-token open: status = %d, want 200", rec.Code)
	}

	// 有 token
	h := newTestServerWithToken(t, "s3cret")
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no header: status = %d, want 401", rec.Code)
	}
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", map[string]string{"X-Token": "wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong X-Token: status = %d, want 401", rec.Code)
	}
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", map[string]string{"X-Token": "s3cret"}); rec.Code != http.StatusOK {
		t.Errorf("correct X-Token: status = %d, want 200", rec.Code)
	}
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", map[string]string{"Authorization": "Bearer s3cret"}); rec.Code != http.StatusOK {
		t.Errorf("correct Bearer: status = %d, want 200", rec.Code)
	}
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", map[string]string{"Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong Bearer: status = %d, want 401", rec.Code)
	}
	// 401 响应为 JSON error
	rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", nil)
	if msg := decodeErr(t, rec); msg != "unauthorized" {
		t.Errorf("401 message = %q, want unauthorized", msg)
	}

	// /sub、/healthz、/version 永不鉴权（参数错误 400 而非 401）
	if rec := do(h, "/sub?target=clash"); rec.Code != http.StatusBadRequest {
		t.Errorf("/sub should not require auth: status = %d, want 400", rec.Code)
	}
	if rec := do(h, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz should not require auth: status = %d, want 200", rec.Code)
	}
	if rec := do(h, "/version"); rec.Code != http.StatusOK {
		t.Errorf("/version should not require auth: status = %d, want 200", rec.Code)
	}

	// P1-2 回归：页面与静态资源不鉴权（浏览器导航无法携带令牌头，挡 401 后
	// 管理台完全不可达）
	if rec := doJSON(h, http.MethodGet, "/", "", nil); rec.Code != http.StatusOK {
		t.Errorf("/ with token set should be public: status = %d, want 200", rec.Code)
	}
	if rec := doJSON(h, http.MethodGet, "/app.js", "", nil); rec.Code != http.StatusOK {
		t.Errorf("/app.js with token set should be public: status = %d, want 200", rec.Code)
	}
	if rec := doJSON(h, http.MethodGet, "/style.css", "", nil); rec.Code != http.StatusOK {
		t.Errorf("/style.css with token set should be public: status = %d, want 200", rec.Code)
	}

	// P3-18 回归：Bearer scheme 大小写不敏感（RFC 7235）
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", map[string]string{"Authorization": "bearer s3cret"}); rec.Code != http.StatusOK {
		t.Errorf("lowercase bearer: status = %d, want 200", rec.Code)
	}
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", map[string]string{"Authorization": "BEARER s3cret"}); rec.Code != http.StatusOK {
		t.Errorf("uppercase BEARER: status = %d, want 200", rec.Code)
	}
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", map[string]string{"Authorization": "bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("lowercase bearer wrong token: status = %d, want 401", rec.Code)
	}
}

// TestInvalidRegex400 断言正则编译错误映射 400（客户端参数错误）。
func TestInvalidRegex400(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())

	for _, extra := range []map[string]string{
		{"include": "("},
		{"exclude": "("},
		{"rename": "(/x"},       // rename 正则编译失败
		{"rename": "nopattern"}, // rename 缺 / 分隔符
	} {
		rec := do(h, subQuery(src.URL, extra))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("sub %v: status = %d, want 400; body=%s", extra, rec.Code, rec.Body.String())
		}
	}
	// convert preview 同样映射 400
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"url":%q,"include":"("}`, src.URL), nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("preview invalid include: status = %d, want 400", rec.Code)
	}
}

// TestAdminVersion 断言 /api/v1/version 与 /version 同源。
func TestAdminVersion(t *testing.T) {
	h := newTestServer(t)
	rec := doJSON(h, http.MethodGet, "/api/v1/version", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var v struct {
		Version string `json:"version"`
		Mihomo  string `json:"mihomo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if v.Version != "0.1.0" || v.Mihomo != "v1.19.29" {
		t.Errorf("version payload = %+v, want {0.1.0 v1.19.29}", v)
	}
}

// TestNoStoreAdminRoutesUnregistered 断言 st == nil 时不挂载 /api/v1。
func TestNoStoreAdminRoutesUnregistered(t *testing.T) {
	h := newTestServerNoStore(t)
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("no-store /api/v1/sources: status = %d, want 404", rec.Code)
	}
}

// ---------- M3 代码审查修复回归测试 ----------

// TestNewServerExplicitNilStore（P2-3）显式传 nil store：不得注册 /api/v1
// 路由（旧代码判断变参切片导致注册后 handler nil 解引用 panic），
// /api/v1/sources 返回 404 且公开端点不受影响。
func TestNewServerExplicitNilStore(t *testing.T) {
	cfg := config.Default()
	h := NewServer(cfg, fetcher.New(cfg.Fetcher), nil) // 显式 nil
	if rec := doJSON(h, http.MethodGet, "/api/v1/sources", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("explicit nil store: /api/v1/sources status = %d, want 404（不得 panic）", rec.Code)
	}
	if rec := do(h, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("explicit nil store: /healthz status = %d, want 200", rec.Code)
	}
	if rec := do(h, "/version"); rec.Code != http.StatusOK {
		t.Errorf("explicit nil store: /version status = %d, want 200", rec.Code)
	}
}

// TestLogRetryMultiSourceURL（P2-4）URLFull 含 | 的多源日志 retry：先拆分
// 再校验（旧代码把含 | 的原始串直接交给 runPipeline → 502），retry 必须 200
// 且新日志 URLRedacted/URLFull 正确。
func TestLogRetryMultiSourceURL(t *testing.T) {
	h, st := newTestServerWithStore(t)
	srcA := fakeSource(t, http.StatusOK, subBody())
	srcB := fakeSource(t, http.StatusOK, subBody())
	multi := srcA.URL + "|" + srcB.URL

	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"url":%q}`, multi), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("multi-source preview status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	logs := st.ListLogs(10)
	if len(logs) == 0 || logs[0].URLFull != multi {
		t.Fatalf("logs[0].URLFull = %q, want %q", logs[0].URLFull, multi)
	}
	target := logs[0]

	rec = doJSON(h, http.MethodPost, "/api/v1/logs/"+target.ID+"/retry", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("multi-source retry status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		NodeCount int `json:"node_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.NodeCount != 4 {
		t.Errorf("retry node_count = %+v, want 4（两源各 2 节点）", resp)
	}
	// P3-7：新日志 URLRedacted 重新脱敏（含两个源 host），URLFull 保留原多源串
	latest := st.ListLogs(10)[0]
	hostA := strings.TrimPrefix(srcA.URL, "http://")
	hostB := strings.TrimPrefix(srcB.URL, "http://")
	if !strings.Contains(latest.URLRedacted, hostA) || !strings.Contains(latest.URLRedacted, hostB) {
		t.Errorf("retry URLRedacted = %q, want 含 %q 与 %q", latest.URLRedacted, hostA, hostB)
	}
	if latest.URLFull != multi {
		t.Errorf("retry URLFull = %q, want %q", latest.URLFull, multi)
	}
}

// TestLogRetryRecomputesURLRedacted（P3-7）源 URL 已更新后 retry：新日志
// URLRedacted 必须反映当前源 URL（旧代码复用旧日志值，展示与实际不符）。
func TestLogRetryRecomputesURLRedacted(t *testing.T) {
	h, st := newTestServerWithStore(t)
	srcA := fakeSource(t, http.StatusOK, subBody())
	srcB := fakeSource(t, http.StatusOK, subBody())
	s, err := st.CreateSource("机场A", srcA.URL, true)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_id":%q}`, s.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200", rec.Code)
	}
	logID := st.ListLogs(10)[0].ID

	// 更新源 URL（换 host）
	newURL := srcB.URL
	if _, err := st.UpdateSource(s.ID, store.SourcePatch{URL: &newURL}); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	rec = doJSON(h, http.MethodPost, "/api/v1/logs/"+logID+"/retry", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	latest := st.ListLogs(10)[0]
	hostA := strings.TrimPrefix(srcA.URL, "http://")
	hostB := strings.TrimPrefix(srcB.URL, "http://")
	if strings.Contains(latest.URLRedacted, hostA) {
		t.Errorf("retry URLRedacted = %q, 不应再含旧源 host %q（复用旧日志值 bug）", latest.URLRedacted, hostA)
	}
	if !strings.Contains(latest.URLRedacted, hostB) {
		t.Errorf("retry URLRedacted = %q, 应含当前源 host %q", latest.URLRedacted, hostB)
	}
}

// TestTemplateNameTraversalRejected（P2-5）模板 Name 路径穿越在 API 层被拒：
// 创建/更新返回 400，列表不出现穿越名。
func TestTemplateNameTraversalRejected(t *testing.T) {
	h := newTestServer(t)
	for _, name := range []string{"../evil", "a/b", `a\b`, "..", "x/../y"} {
		body := fmt.Sprintf(`{"name":%q,"url":"http://x.example.com/rules.yaml","behavior":"domain","format":"yaml"}`, name)
		rec := doJSON(h, http.MethodPost, "/api/v1/templates", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create name=%q: status = %d, want 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
	tpl := createTemplateViaAPI(t, h, "正常模板", "http://x.example.com/rules.yaml", "domain", "yaml")
	rec := doJSON(h, http.MethodPut, "/api/v1/templates/"+tpl["id"].(string), `{"name":"../evil"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update traversal name: status = %d, want 400", rec.Code)
	}
	// 列表不出现穿越名
	rec = doJSON(h, http.MethodGet, "/api/v1/templates", "", nil)
	if strings.Contains(rec.Body.String(), "evil") {
		t.Errorf("templates list contains traversal name: %s", rec.Body.String())
	}
}

// TestTemplateNameSpecialCharsRejected（P2-1）：模板名含逗号/换行/回车/控制字符
// → 创建/更新 400（这些字符会拆碎 RULE-SET 规则行或破坏 YAML 行结构，mihomo
// 语义层拒绝而语法层 output.Validate 放行）。
func TestTemplateNameSpecialCharsRejected(t *testing.T) {
	h := newTestServer(t)
	for _, name := range []string{"Netflix,cn", "a\nb", "a\rb", "a\tb"} {
		body := fmt.Sprintf(`{"name":%q,"url":"http://x.example.com/rules.yaml","behavior":"domain","format":"yaml"}`, name)
		rec := doJSON(h, http.MethodPost, "/api/v1/templates", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create name=%q: status = %d, want 400; body=%s", name, rec.Code, rec.Body.String())
			continue
		}
		if msg := decodeErr(t, rec); !strings.Contains(msg, "不能包含逗号/换行") {
			t.Errorf("create name=%q: err = %q, want 含 不能包含逗号/换行", name, msg)
		}
	}
	tpl := createTemplateViaAPI(t, h, "正常模板", "http://x.example.com/rules.yaml", "domain", "yaml")
	rec := doJSON(h, http.MethodPut, "/api/v1/templates/"+tpl["id"].(string), `{"name":"a,b"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update comma name: status = %d, want 400", rec.Code)
	}
}

// TestStoreIOErrorMapsTo500（P2-6）落盘 IO 错误（数据目录被破坏）→ 500
// 「internal error」，而非 404/400；删除失败回滚内存。
func TestStoreIOErrorMapsTo500(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	src, err := st.CreateSource("a", "http://x.example.com/sub", true)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	cfg := config.Default()
	h := NewServer(cfg, fetcher.New(cfg.Fetcher), st)

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	rec := doJSON(h, http.MethodDelete, "/api/v1/sources/"+src.ID, "", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("delete with broken disk: status = %d, want 500（而非 404）; body=%s", rec.Code, rec.Body.String())
	}
	if msg := decodeErr(t, rec); msg != "internal error" {
		t.Errorf("500 message = %q, want internal error（不暴露内部细节）", msg)
	}
	if _, ok := st.GetSource(src.ID); !ok {
		t.Error("DeleteSource 落盘失败后内存应保留该源（回滚）")
	}
}

// TestSubParamErrorsLogged（P3-8）400 参数错误分支也记录转换日志
// （Kind=sub, Status=fail），URL 脱敏、无 url 时为空、src 场景填 SourceID。
func TestSubParamErrorsLogged(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())
	// P2-3：参数错误日志同样记录 template_id（与成功路径一致）
	do(h, "/sub?target=surge&url="+url.QueryEscape(src.URL)+"&template_id=xyz123") // target 错 + template_id
	do(h, "/sub")                                                                  // 缺 url
	do(h, "/sub?target=surge&url="+url.QueryEscape(src.URL))                       // target 错
	do(h, "/sub?target=clash&src=deadbeef0000")                                    // src 不可用
	do(h, "/sub?target=clash&url=notaurl")                                         // url 非法

	logs := st.ListLogs(10)
	if len(logs) != 5 {
		t.Fatalf("logs len = %d, want 5（参数错误也记日志）", len(logs))
	}
	for i, e := range logs {
		if e.Kind != "sub" || e.Status != "fail" || e.Error == nil {
			t.Errorf("logs[%d] = %+v, want kind=sub status=fail error!=nil", i, e)
		}
	}
	// 最新在前：logs[0]=url 非法（URLFull 记录原始串），logs[1]=src 不可用
	// （SourceID 填请求的 src），logs[2]=target 错（URLRedacted 含 host），
	// logs[3]=缺 url（URLRedacted 为空，不 panic）
	if logs[0].URLFull != "notaurl" {
		t.Errorf("logs[0].URLFull = %q, want notaurl", logs[0].URLFull)
	}
	if logs[1].SourceID != "deadbeef0000" {
		t.Errorf("logs[1].SourceID = %q, want deadbeef0000", logs[1].SourceID)
	}
	if logs[1].URLRedacted != "" {
		t.Errorf("logs[1].URLRedacted = %q, want 空（无 url 参数）", logs[1].URLRedacted)
	}
	if !strings.Contains(logs[2].URLRedacted, "127.0.0.1") {
		t.Errorf("logs[2].URLRedacted = %q, want 含 host", logs[2].URLRedacted)
	}
	if logs[3].URLRedacted != "" {
		t.Errorf("logs[3].URLRedacted = %q, want 空（无 url 参数）", logs[3].URLRedacted)
	}
	// logs[4] = 最早请求（target 错 + template_id）：Params 必须带 template_id
	if tid, _ := logs[4].Params["template_id"].(string); tid != "xyz123" {
		t.Errorf("logs[4].Params[template_id] = %q, want xyz123", tid)
	}
}

// TestRedactURL（P3-9）userinfo 整体掩码、敏感参数变体、大小写不敏感、
// 多源混合与非法源丢弃。
func TestRedactURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantSub string
		notWant []string
	}{
		{
			name:    "userinfo 用户名+密码整体掩码",
			in:      "https://user:pass@host.example/sub",
			wantSub: "https://xxxxx@host.example/sub",
			notWant: []string{"user", "pass@"},
		},
		{
			name:    "userinfo 仅用户名也掩码",
			in:      "https://user@host.example/sub",
			wantSub: "xxxxx@host.example",
			notWant: []string{"user@"},
		},
		{
			name:    "敏感参数变体与大小写",
			in:      "https://host.example/sub?token=SECRET&PASSWD=pwd&Apikey=k&Signature=sig&sign=s&credential=c&auth=a&password=p",
			wantSub: "token=xxxxx",
			notWant: []string{"SECRET", "pwd", "Apikey=k", "Signature=sig", "sign=s", "credential=c", "auth=a", "password=p"},
		},
		{
			name:    "多源混合（非法源丢弃）",
			in:      "https://a.example/sub?token=T1|https://b.example/sub?password=P2|http://%zz",
			wantSub: "a.example/sub?token=xxxxx",
			notWant: []string{"T1", "P2", "%zz"},
		},
		{
			name:    "无敏感信息原样保留",
			in:      "https://plain.example/sub?foo=bar",
			wantSub: "foo=bar",
			notWant: []string{"xxxxx"},
		},
	}
	for _, tc := range cases {
		got := redactURL(tc.in)
		if !strings.Contains(got, tc.wantSub) {
			t.Errorf("%s: redactURL(%q) = %q, want contains %q", tc.name, tc.in, got, tc.wantSub)
		}
		for _, nw := range tc.notWant {
			if strings.Contains(got, nw) {
				t.Errorf("%s: redactURL(%q) = %q, should not contain %q", tc.name, tc.in, got, nw)
			}
		}
	}
	// 空串与全空源不 panic
	if got := redactURL(""); got != "" {
		t.Errorf("redactURL(\"\") = %q, want empty", got)
	}
	if got := redactURL("| |"); got != "" {
		t.Errorf("redactURL(\"| |\") = %q, want empty", got)
	}
}

// TestAdminUnknownPathJSON404（P3-11）/api/v1 未注册子路径返回 JSON 404
// （旧行为是 Go 默认 text/plain 404）。
func TestAdminUnknownPathJSON404(t *testing.T) {
	h := newTestServer(t)
	rec := doJSON(h, http.MethodGet, "/api/v1/nonexistent", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json（非 text/plain 默认 404）", ct)
	}
	if msg := decodeErr(t, rec); msg != "not found" {
		t.Errorf("404 message = %q, want not found", msg)
	}
}

// TestRequestBodyTooLarge（P3-12）请求体超过 1MB → 400「request body too large」。
func TestRequestBodyTooLarge(t *testing.T) {
	h := newTestServer(t)
	big := strings.Repeat("a", 2<<20) // 2MB > 1MB 限制
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"sources create", http.MethodPost, "/api/v1/sources", `{"name":"x","url":"http://x.example.com/sub","pad":"` + big + `"}`},
		{"convert preview", http.MethodPost, "/api/v1/convert/preview", `{"url":"http://x.example.com/sub","pad":"` + big + `"}`},
	} {
		rec := doJSON(h, tc.method, tc.path, tc.body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, rec.Code)
			continue
		}
		if msg := decodeErr(t, rec); msg != "request body too large" {
			t.Errorf("%s: message = %q, want request body too large", tc.name, msg)
		}
	}
}

// TestConvertStripEmoji 断言 convert 端点 strip_emoji:true：preview 返回的
// 节点名无旗标 emoji、组 proxies 引用一致；run 返回的 YAML 节点名同样无
// emoji 且可解析。不传该字段=默认关（节点名原样保留）。
func TestConvertStripEmoji(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())

	// preview：strip_emoji:true → 节点名无旗标，组引用一致
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview",
		fmt.Sprintf(`{"url":%q,"strip_emoji":true}`, src.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview strip status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Nodes  []map[string]string `json:"nodes"`
		Groups []map[string]any    `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("preview body not json: %v", err)
	}
	names := map[string]bool{}
	for _, n := range resp.Nodes {
		names[n["name"]] = true
	}
	if !names["香港-01"] || !names["日本-01"] || names["🇭🇰 香港-01"] || names["🇯🇵 日本-01"] {
		t.Errorf("preview nodes = %v, want 香港-01/日本-01 且无旗标", names)
	}
	if len(resp.Groups) == 0 {
		t.Fatal("preview groups empty")
	}
	// retry：日志 Params 记录了 strip_emoji=true，重试后节点名仍无 emoji（验收 7）
	logs := st.ListLogs(10)
	if len(logs) == 0 {
		t.Fatal("no conversion logs after preview")
	}
	rec = doJSON(h, http.MethodPost, "/api/v1/logs/"+logs[0].ID+"/retry", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var retryResp struct {
		Nodes     []map[string]string `json:"nodes"`
		NodeCount int                 `json:"node_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &retryResp); err != nil {
		t.Fatalf("retry body not json: %v", err)
	}
	if retryResp.NodeCount != 2 {
		t.Errorf("retry node_count = %d, want 2", retryResp.NodeCount)
	}
	for _, n := range retryResp.Nodes {
		if strings.Contains(n["name"], "🇭🇰") || strings.Contains(n["name"], "🇯🇵") {
			t.Errorf("retry 节点名 %q 仍含旗标（Params 未记录 strip_emoji?）", n["name"])
		}
	}
	groupNames := map[string]bool{}
	for _, g := range resp.Groups {
		groupNames[g["name"].(string)] = true
	}
	if !groupNames["手动选择"] || !groupNames["香港节点"] || !groupNames["日本节点"] {
		t.Errorf("preview group names = %v, want 手动选择/香港节点/日本节点", groupNames)
	}
	validRefs := map[string]bool{"DIRECT": true, "REJECT": true}
	for n := range names {
		validRefs[n] = true
	}
	for gn := range groupNames {
		validRefs[gn] = true
	}
	for _, g := range resp.Groups {
		refs, _ := g["proxies"].([]any)
		for _, ref := range refs {
			if s, ok := ref.(string); ok && !validRefs[s] {
				t.Errorf("group %v 引用 %q 不存在", g["name"], s)
			}
		}
	}

	// run：strip_emoji:true → YAML 节点名无旗标
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/run",
		fmt.Sprintf(`{"url":%q,"strip_emoji":true}`, src.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("run strip status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var runResp struct {
		YAML string `json:"yaml"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &runResp); err != nil {
		t.Fatalf("run body not json: %v", err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(runResp.YAML), &cfg); err != nil {
		t.Fatalf("run yaml not valid: %v", err)
	}
	proxies, _ := cfg["proxies"].([]any)
	if len(proxies) != 2 {
		t.Fatalf("run proxies len = %d, want 2", len(proxies))
	}
	for _, p := range proxies {
		name := p.(map[string]any)["name"].(string)
		if strings.Contains(name, "🇭🇰") || strings.Contains(name, "🇯🇵") {
			t.Errorf("run proxy name %q 仍含旗标", name)
		}
	}
}
