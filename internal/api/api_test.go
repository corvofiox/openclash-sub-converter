package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/yangyu/openclash-sub-converter/internal/config"
	"github.com/yangyu/openclash-sub-converter/internal/fetcher"
	"github.com/yangyu/openclash-sub-converter/internal/store"
)

// newTestServer 构建被测 handler（挂载真实 store，数据落在 t.TempDir()）。
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	h, _ := newTestServerWithStore(t)
	return h
}

// newTestServerWithStore 构建 handler 并返回 store，供测试直接操作数据层。
func newTestServerWithStore(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	cfg := config.Default()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewServer(cfg, fetcher.New(cfg.Fetcher), st), st
}

// newTestServerNoStore 构建不挂载 store 的 handler（验证 /api/v1 不注册）。
func newTestServerNoStore(t *testing.T) http.Handler {
	t.Helper()
	cfg := config.Default()
	return NewServer(cfg, fetcher.New(cfg.Fetcher))
}

// newTestServerWithToken 构建带管理台令牌的 handler。
func newTestServerWithToken(t *testing.T, token string) http.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.AdminToken = token
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewServer(cfg, fetcher.New(cfg.Fetcher), st)
}

// captureLogs 将 slog 默认 logger 重定向到 buffer，返回恢复函数。
func captureLogs(t *testing.T) (*strings.Builder, func()) {
	t.Helper()
	var buf strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	return &buf, func() { slog.SetDefault(old) }
}

// do 执行一次请求并返回 recorder。
func do(h http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// fakeSource 返回一个返回固定 body 的假订阅源。
func fakeSource(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// subBody 构造一个含两个 ss 节点（🇭🇰/🇯🇵）的订阅文本。
func subBody() string {
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	return fmt.Sprintf("ss://%s@example.com:8388#🇭🇰 香港-01\nss://%s@example.com:8388#🇯🇵 日本-01\n", ui, ui)
}

// subQuery 拼接 /sub 查询串（url 自动编码，多源用 | 分隔）。
func subQuery(urls string, extra map[string]string) string {
	vals := url.Values{}
	vals.Set("target", "clash")
	vals.Set("url", urls)
	for k, v := range extra {
		vals.Set(k, v)
	}
	return "/sub?" + vals.Encode()
}

// TestHealthz 断言 /healthz 返回 200 ok。
func TestHealthz(t *testing.T) {
	h := newTestServer(t)
	rec := do(h, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

// TestVersion 断言 /version 返回 JSON 版本信息。
func TestVersion(t *testing.T) {
	h := newTestServer(t)
	rec := do(h, "/version")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var v struct {
		Version string `json:"version"`
		Mihomo  string `json:"mihomo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if v.Version != "0.3.1" || v.Mihomo != "v1.19.29" {
		t.Errorf("version payload = %+v, want {0.3.1 v1.19.29}", v)
	}
}

// TestSubParamErrors 断言参数缺失/非法返回 400 + JSON error。
func TestSubParamErrors(t *testing.T) {
	h := newTestServer(t)
	cases := []string{
		"/sub",
		"/sub?target=surge&url=http://example.com/sub",
		"/sub?target=clash",
		"/sub?target=clash&url=%7C%7C", // 拆分后无有效源
	}
	for _, target := range cases {
		rec := do(h, target)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, rec.Code)
			continue
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: Content-Type = %q, want application/json", target, ct)
		}
		var e struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error == "" {
			t.Errorf("%s: body = %q, want json error message", target, rec.Body.String())
		}
	}
}

// TestSubSuccess 断言假源（Base64 订阅）全流程转换成功，输出可解析的 Clash YAML。
func TestSubSuccess(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	rec := do(h, subQuery(src.URL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/yaml; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/yaml; charset=utf-8", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	// R2 无失败源：不输出告警注释、不设置告警头（字节级兼容基线）
	if strings.Contains(rec.Body.String(), "# [OSC-WARNING]") {
		t.Errorf("no-failure response should not contain OSC-WARNING comment:\n%s", rec.Body.String())
	}
	if h := rec.Header().Get("X-Osc-Warning"); h != "" {
		t.Errorf("no-failure response should not set X-Osc-Warning, got %q", h)
	}

	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	proxies, ok := cfg["proxies"].([]any)
	if !ok || len(proxies) != 2 {
		t.Fatalf("proxies = %T len %d, want 2", cfg["proxies"], len(proxies))
	}
	// 注意：yaml.v3 输出对 emoji 用 \U 转义，故断言解析后的值而非原始文本
	names := map[string]bool{}
	for _, p := range proxies {
		m, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("proxy entry = %T, want map", p)
		}
		names[m["name"].(string)] = true
	}
	if !names["🇭🇰 香港-01"] || !names["🇯🇵 日本-01"] {
		t.Errorf("proxy names = %v, want 🇭🇰 香港-01 / 🇯🇵 日本-01", names)
	}
	groups, ok := cfg["proxy-groups"].([]any)
	if !ok {
		t.Fatalf("proxy-groups = %T, want list", cfg["proxy-groups"])
	}
	groupNames := map[string]bool{}
	for _, g := range groups {
		m := g.(map[string]any)
		groupNames[m["name"].(string)] = true
	}
	// 策略组构建生效
	if !groupNames["手动选择"] || !groupNames["香港节点"] || !groupNames["日本节点"] {
		t.Errorf("proxy group names = %v, want 手动选择 / 香港节点 / 日本节点", groupNames)
	}
	// 规则注入
	rules, ok := cfg["rules"].([]any)
	if !ok || len(rules) != 3 {
		t.Fatalf("rules = %T %v, want 3 entries（含内置 gfw）", cfg["rules"], cfg["rules"])
	}
	if rules[0] != "RULE-SET,gfw,手动选择" || rules[1] != "GEOIP,CN,DIRECT" || rules[2] != "MATCH,漏网之鱼" {
		t.Errorf("rules = %v, want [RULE-SET,gfw,手动选择 GEOIP,CN,DIRECT MATCH,漏网之鱼]", rules)
	}
	// A1：rule-providers.gfw 字段契约（url/behavior/format/interval）
	rps2, _ := cfg["rule-providers"].(map[string]any)
	gfwEntry, ok := rps2["gfw"].(map[string]any)
	if !ok {
		t.Fatalf("rule-providers 缺内置 gfw，实际 %v", rps2)
	}
	if gfwEntry["url"] != "https://raw.githubusercontent.com/Loyalsoldier/clash-rules/release/gfw.txt" ||
		gfwEntry["behavior"] != "domain" || gfwEntry["format"] != "yaml" || gfwEntry["interval"] != 86400 {
		t.Errorf("gfw entry = %v", gfwEntry)
	}
}

// TestSubAllSourcesFail 断言所有源失败返回 502 + JSON（错误只含 host）。
func TestSubAllSourcesFail(t *testing.T) {
	h := newTestServer(t)
	bad := fakeSource(t, http.StatusInternalServerError, "boom")
	rec := do(h, subQuery(bad.URL, nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("body not json: %v (%s)", err, rec.Body.String())
	}
	if !strings.Contains(e.Error, "127.0.0.1") {
		t.Errorf("error should mention source host, got %q", e.Error)
	}
	if strings.Contains(e.Error, bad.URL) {
		t.Errorf("error leaks full subscription url: %q", e.Error)
	}
}

// TestSubPartialFailure 断言部分源失败时继续转换（有节点成功则 200）。
func TestSubPartialFailure(t *testing.T) {
	h := newTestServer(t)
	bad := fakeSource(t, http.StatusInternalServerError, "boom")
	good := fakeSource(t, http.StatusOK, subBody())
	rec := do(h, subQuery(good.URL+"|"+bad.URL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	proxies, ok := cfg["proxies"].([]any)
	if !ok || len(proxies) != 2 {
		t.Fatalf("proxies = %T len %d, want 2 (from good source)", cfg["proxies"], len(proxies))
	}
	// R2 三通道：YAML 顶部注释 + X-Osc-Warning 响应头（只含失败 host，不含完整 URL）
	body := rec.Body.String()
	if !strings.HasPrefix(body, "# [OSC-WARNING]") {
		t.Errorf("response should start with OSC-WARNING comment:\n%s", body)
	}
	badHost := strings.TrimPrefix(bad.URL, "http://")
	if !strings.Contains(body, "# [OSC-WARNING] "+badHost) {
		t.Errorf("comment should mention failed host %q:\n%s", badHost, body)
	}
	if strings.Contains(body, bad.URL) {
		t.Errorf("comment leaks full failed url %q:\n%s", bad.URL, body)
	}
	if h := rec.Header().Get("X-Osc-Warning"); h != badHost {
		t.Errorf("X-Osc-Warning = %q, want %q", h, badHost)
	}
}

// TestSubIncludeExclude 断言 include/exclude 正则生效。
func TestSubIncludeExclude(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())

	// include=香港 只保留香港节点
	rec := do(h, subQuery(src.URL, map[string]string{"include": "香港"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("include: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "香港-01") {
		t.Error("include: missing 香港-01")
	}
	if strings.Contains(rec.Body.String(), "日本-01") {
		t.Error("include: 日本-01 should be filtered out")
	}

	// exclude=日本 剔除日本节点
	rec = do(h, subQuery(src.URL, map[string]string{"exclude": "日本"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("exclude: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "香港-01") {
		t.Error("exclude: missing 香港-01")
	}
	if strings.Contains(rec.Body.String(), "日本-01") {
		t.Error("exclude: 日本-01 should be filtered out")
	}

	// exclude 过滤掉全部节点 → 输出空 proxies 仍 200（模板层合法）
	rec = do(h, subQuery(src.URL, map[string]string{"exclude": ".*"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("exclude-all: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "香港-01") {
		t.Error("exclude-all: node should be filtered out")
	}
}

// TestSubOptions 断言 udp/tls13/scv 参数生效。
func TestSubOptions(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	rec := do(h, subQuery(src.URL, map[string]string{"udp": "true", "tls13": "1", "scv": "true"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "udp: true") {
		t.Errorf("udp: true missing:\n%s", body)
	}
	// ss 节点应带 tls13；scv 不适用于 ss（仅 vmess/vless/trojan/hy2/tuic/anytls）
	if !strings.Contains(body, "tls13: true") {
		t.Errorf("tls13: true missing:\n%s", body)
	}
	if strings.Contains(body, "skip-cert-verify: true") {
		t.Errorf("skip-cert-verify should not apply to ss nodes:\n%s", body)
	}
}

// TestSubRename 断言 rename=<regex>/<repl> 生效。
func TestSubRename(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	rec := do(h, subQuery(src.URL, map[string]string{"rename": "香港-01/港岛"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "港岛") {
		t.Errorf("rename result missing:\n%s", body)
	}
	if strings.Contains(body, "香港-01") {
		t.Errorf("original name should be replaced:\n%s", body)
	}
}

// TestSubSkipsInvalidNodes 断言 P1-1 节点级校验生效：字段级坏节点
// （reality public-key 非 base64 的 vless、缺 password 的 trojan）被跳过，
// 有效节点保留，HTTP 200。
func TestSubSkipsInvalidNodes(t *testing.T) {
	h := newTestServer(t)
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	const uuid = "b831381d-6324-4d53-ad4f-8cda48b30811"
	body := fmt.Sprintf(
		"ss://%s@example.com:8388#valid-ss\n"+
			"vless://%s@example.com:443?security=reality&pbk=not-base64!!!&sid=abcd#bad-reality\n"+
			"trojan://example.com:443#no-password\n", ui, uuid)
	src := fakeSource(t, http.StatusOK, body)
	rec := do(h, subQuery(src.URL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	proxies, ok := cfg["proxies"].([]any)
	if !ok || len(proxies) != 1 {
		t.Fatalf("proxies = %T len %d, want 1 (bad nodes skipped)", cfg["proxies"], len(proxies))
	}
	if name := proxies[0].(map[string]any)["name"]; name != "valid-ss" {
		t.Errorf("remaining proxy = %v, want valid-ss", name)
	}
}

// TestSubSkipsInvalidYAMLNodes 断言 YAML 订阅透传条目同样走 ParseProxy 节点级校验。
func TestSubSkipsInvalidYAMLNodes(t *testing.T) {
	h := newTestServer(t)
	const uuid = "b831381d-6324-4d53-ad4f-8cda48b30811"
	yamlSub := "proxies:\n" +
		"  - name: yaml-valid\n    type: ss\n    server: example.com\n    port: 8388\n    cipher: aes-256-gcm\n    password: x\n" +
		"  - name: yaml-bad-reality\n    type: vless\n    server: example.com\n    port: 443\n    uuid: " + uuid + "\n    network: tcp\n    tls: true\n    reality-opts:\n      public-key: not-base64!!!\n"
	src := fakeSource(t, http.StatusOK, yamlSub)
	rec := do(h, subQuery(src.URL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	proxies, ok := cfg["proxies"].([]any)
	if !ok || len(proxies) != 1 {
		t.Fatalf("proxies = %T len %d, want 1 (yaml bad node skipped)", cfg["proxies"], len(proxies))
	}
	if name := proxies[0].(map[string]any)["name"]; name != "yaml-valid" {
		t.Errorf("remaining proxy = %v, want yaml-valid", name)
	}
}

// TestSubErrorDoesNotLeakCredentials 断言 P1-5 脱敏：订阅 URL 中的 userinfo/query
// 凭证（user:pass / token=SECRET）不出现在日志与错误响应中。
func TestSubErrorDoesNotLeakCredentials(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	h := newTestServer(t)
	// 指向已关闭端口（连接必失败），URL 携带 userinfo 与 query 凭证
	credsURL := "http://user:pass@127.0.0.1:1/sub?token=SECRET"
	rec := do(h, subQuery(credsURL, nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	// 响应体只含 host
	if body := rec.Body.String(); strings.Contains(body, "user:pass") || strings.Contains(body, "token=SECRET") || strings.Contains(body, credsURL) {
		t.Errorf("response leaks credentials: %s", body)
	}
	// 日志只含 host
	logs := buf.String()
	if !strings.Contains(logs, "127.0.0.1:1") {
		t.Errorf("log should mention source host 127.0.0.1:1:\n%s", logs)
	}
	for _, leak := range []string{"user:pass", "token=SECRET", credsURL} {
		if strings.Contains(logs, leak) {
			t.Errorf("log leaks %q:\n%s", leak, logs)
		}
	}
}

// TestSubStripEmoji 断言 strip_emoji=true：节点名剥离旗标 emoji（识别仍基于
// 原始名→组名不变且无 emoji）、组 proxies 引用与 proxies 段一致、内置 gfw
// 规则注入 + MATCH 兜底指向漏网之鱼。
func TestSubStripEmoji(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	rec := do(h, subQuery(src.URL, map[string]string{"strip_emoji": "true"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	proxies, ok := cfg["proxies"].([]any)
	if !ok || len(proxies) != 2 {
		t.Fatalf("proxies = %T len %d, want 2", cfg["proxies"], len(proxies))
	}
	names := map[string]bool{}
	for _, p := range proxies {
		names[p.(map[string]any)["name"].(string)] = true
	}
	if !names["香港-01"] || !names["日本-01"] {
		t.Errorf("proxy names = %v, want 香港-01 / 日本-01（旗标已剥离）", names)
	}
	if names["🇭🇰 香港-01"] || names["🇯🇵 日本-01"] {
		t.Errorf("proxy names = %v, emoji 名不应存在", names)
	}

	// 组名无 emoji 且引用一致：每条组 proxies 引用都落在
	// 节点名 ∪ 组名 ∪ {DIRECT, REJECT} 内
	groups, ok := cfg["proxy-groups"].([]any)
	if !ok {
		t.Fatalf("proxy-groups = %T, want list", cfg["proxy-groups"])
	}
	groupNames := map[string]bool{}
	for _, g := range groups {
		groupNames[g.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"手动选择", "自动选择", "香港节点", "日本节点"} {
		if !groupNames[want] {
			t.Errorf("group names = %v, want 含 %s", groupNames, want)
		}
	}
	validRefs := map[string]bool{"DIRECT": true, "REJECT": true}
	for n := range names {
		validRefs[n] = true
	}
	for gn := range groupNames {
		validRefs[gn] = true
	}
	for _, g := range groups {
		m := g.(map[string]any)
		refs, _ := m["proxies"].([]any)
		for _, ref := range refs {
			s, ok := ref.(string)
			if !ok {
				t.Errorf("group %v proxies 条目非字符串: %v", m["name"], ref)
				continue
			}
			if !validRefs[s] {
				t.Errorf("group %v 引用 %q 在节点名/组名集合中不存在", m["name"], s)
			}
		}
	}
	// 自动选择组 proxies 全部为节点名（无组名混入）
	auto := findGroupByName(t, groups, "自动选择")
	for _, ref := range auto["proxies"].([]any) {
		if !names[ref.(string)] {
			t.Errorf("自动选择 引用 %q 不是节点名", ref)
		}
	}

	// rules：内置 gfw 规则集 → GEOIP,CN,DIRECT → MATCH 兜底指向漏网之鱼
	rules, ok := cfg["rules"].([]any)
	if !ok || len(rules) != 3 {
		t.Fatalf("rules = %T %v, want 3 entries（含内置 gfw）", cfg["rules"], cfg["rules"])
	}
	if rules[0] != "RULE-SET,gfw,手动选择" || rules[1] != "GEOIP,CN,DIRECT" || rules[2] != "MATCH,漏网之鱼" {
		t.Errorf("rules = %v, want [RULE-SET,gfw,手动选择 GEOIP,CN,DIRECT MATCH,漏网之鱼]", rules)
	}
}

// findGroupByName 按组名查找策略组（测试辅助）。
func findGroupByName(t *testing.T, groups []any, name string) map[string]any {
	t.Helper()
	for _, g := range groups {
		m := g.(map[string]any)
		if m["name"] == name {
			return m
		}
	}
	t.Fatalf("group %q not found in %v", name, groups)
	return nil
}

// findGroupByNameMaps 同 findGroupByName，但输入为 JSON 反序列化得到的
// []map[string]any（preview/retry 响应的 groups 字段）。
func findGroupByNameMaps(t *testing.T, groups []map[string]any, name string) map[string]any {
	t.Helper()
	for _, g := range groups {
		if g["name"] == name {
			return g
		}
	}
	t.Fatalf("group %q not found in %v", name, groups)
	return nil
}

// TestSubStripEmojiDupNames 断言重名场景（验收 9）：输入 🇭🇰 香港01 与 香港01，
// strip_emoji=true 后输出名唯一（香港01 / 香港01 (2)）、地区组 proxies 引用与
// proxies 段一致，且产物通过 mihomo output.Validate（重名会被 mihomo 拒绝）。
func TestSubStripEmojiDupNames(t *testing.T) {
	h := newTestServer(t)
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	body := fmt.Sprintf("ss://%s@example.com:8388#🇭🇰 香港01\nss://%s@example.com:8388#香港01\n", ui, ui)
	src := fakeSource(t, http.StatusOK, body)
	rec := do(h, subQuery(src.URL, map[string]string{"strip_emoji": "true"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（mihomo 校验必须通过）; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	proxies, _ := cfg["proxies"].([]any)
	if len(proxies) != 2 {
		t.Fatalf("proxies len = %d, want 2", len(proxies))
	}
	names := map[string]bool{}
	for _, p := range proxies {
		names[p.(map[string]any)["name"].(string)] = true
	}
	if !names["香港01"] || !names["香港01 (2)"] {
		t.Errorf("proxy names = %v, want 香港01 / 香港01 (2)", names)
	}
	if len(names) != 2 {
		t.Errorf("proxy names 应唯一（mihomo 拒绝重名），got %v", names)
	}
	// 香港组 proxies 引用与 proxies 段一致
	groups, _ := cfg["proxy-groups"].([]any)
	for _, g := range groups {
		m := g.(map[string]any)
		if m["name"] != "香港节点" {
			continue
		}
		refs, _ := m["proxies"].([]any)
		got := map[string]bool{}
		for _, r := range refs {
			got[r.(string)] = true
		}
		if !got["香港01"] || !got["香港01 (2)"] {
			t.Errorf("香港节点 proxies = %v, want 香港01 / 香港01 (2)", got)
		}
	}
}

// TestSubRenameMultiRule 断言 rename 多规则（逗号分隔）经 /sub 全链路生效，
// 且任一规则非法 → 整体 400（不部分生效）。
func TestSubRenameMultiRule(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())

	// 两条规则各自命中：日本-01→JP-01，香港-01→HK-01
	rec := do(h, subQuery(src.URL, map[string]string{"rename": "日本-01/JP01,香港-01/HK01"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "JP01") || !strings.Contains(body, "HK01") {
		t.Errorf("rename 多规则未全部生效:\n%s", body)
	}
	if strings.Contains(body, "日本-01") || strings.Contains(body, "香港-01") {
		t.Errorf("rename 多规则后原节点名残留:\n%s", body)
	}

	// 第二条规则非法（缺 "/"）→ 整体 400
	rec = do(h, subQuery(src.URL, map[string]string{"rename": "日本-01/JP01,香港-01"}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid multi-rule rename: status = %d, want 400", rec.Code)
	}
}

// TestConvertWarnings 断言 R2 convert 双路径（preview 与 run）JSON 带 warnings
// 数组：部分失败时含失败 host（脱敏），全成功时为空数组 []；且响应头带 X-Osc-Warning。
func TestConvertWarnings(t *testing.T) {
	h := newTestServer(t)
	bad := fakeSource(t, http.StatusInternalServerError, "boom")
	good := fakeSource(t, http.StatusOK, subBody())
	badHost := strings.TrimPrefix(bad.URL, "http://")

	// preview：1 好 + 1 坏 → warnings 1 项含坏 host；头含坏 host
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview",
		fmt.Sprintf(`{"url":%q}`, good.URL+"|"+bad.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var pv struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil {
		t.Fatalf("preview body not json: %v", err)
	}
	if len(pv.Warnings) != 1 || !strings.Contains(pv.Warnings[0], badHost) {
		t.Errorf("preview warnings = %v, want 1 项含 %q", pv.Warnings, badHost)
	}
	if strings.Contains(pv.Warnings[0], bad.URL) {
		t.Errorf("preview warnings leaks full url: %q", pv.Warnings[0])
	}
	if h := rec.Header().Get("X-Osc-Warning"); h != badHost {
		t.Errorf("preview X-Osc-Warning = %q, want %q", h, badHost)
	}

	// run：同样带 warnings 与头，且 yaml 含注释
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/run",
		fmt.Sprintf(`{"url":%q}`, good.URL+"|"+bad.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("run status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var run struct {
		YAML     string   `json:"yaml"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("run body not json: %v", err)
	}
	if len(run.Warnings) != 1 || !strings.Contains(run.Warnings[0], badHost) {
		t.Errorf("run warnings = %v, want 1 项含 %q", run.Warnings, badHost)
	}
	if !strings.HasPrefix(run.YAML, "# [OSC-WARNING]") || !strings.Contains(run.YAML, badHost) {
		t.Errorf("run yaml 缺 OSC-WARNING 注释:\n%s", run.YAML)
	}
	if h := rec.Header().Get("X-Osc-Warning"); h != badHost {
		t.Errorf("run X-Osc-Warning = %q, want %q", h, badHost)
	}

	// 全成功 → warnings 为空数组 []（非 null）、无头
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"url":%q}`, good.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview ok status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"warnings":[]`) {
		t.Errorf("preview ok warnings should be empty array, body=%s", rec.Body.String())
	}
	if h := rec.Header().Get("X-Osc-Warning"); h != "" {
		t.Errorf("preview ok should not set X-Osc-Warning, got %q", h)
	}
}

// ---------- R3/R4：规则集 → 专属策略组 + /sub 多 ruleset_id ----------

// findRuleSetIndex 返回 rules 中指定 RULE-SET 行的下标；不存在返回 -1。
func findRuleSetIndex(rules []any, line string) int {
	for i, r := range rules {
		if r == line {
			return i
		}
	}
	return -1
}

// TestSubRuleSetSingle（R3 验收 A3）：/sub?ruleset_id=<id> 单规则集——
// rule-providers 含该规则集、专属策略组（select，proxies=[手动选择,...手动选择组]）、
// RULE-SET,<规则集名>,<规则集名> 在规则列表最前（GEOIP 之前）。
func TestSubRuleSetSingle(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	tpl := createRuleSetViaAPI(t, h, "Netflix", "https://x.example.com/nf.yaml", "domain", "yaml")
	rec := do(h, subQuery(src.URL, map[string]string{"ruleset_id": tpl["id"].(string)}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	rps, ok := cfg["rule-providers"].(map[string]any)
	if !ok || len(rps) != 2 {
		t.Fatalf("rule-providers = %T %v, want 2 entries（规则集 + 内置 gfw）", cfg["rule-providers"], cfg["rule-providers"])
	}
	if _, ok := rps["Netflix"]; !ok {
		t.Errorf("rule-providers 缺 Netflix，实际 %v", rps)
	}
	if _, ok := rps["gfw"]; !ok {
		t.Errorf("rule-providers 缺内置 gfw，实际 %v", rps)
	}
	groups, _ := cfg["proxy-groups"].([]any)
	ng := findGroupByName(t, groups, "Netflix")
	if ng["type"] != "select" {
		t.Errorf("Netflix group type = %v, want select", ng["type"])
	}
	// 改动 2：专属组 proxies = [手动选择, ...手动选择组 proxies]：首位引用「手动
	// 选择」组（用户可在专属组内跟随手动选择），其后为手动组 proxies 的深拷贝
	manual := findGroupByName(t, groups, "手动选择")
	mp := manual["proxies"].([]any)
	np := ng["proxies"].([]any)
	if len(np) != len(mp)+1 || np[0] != "手动选择" || !reflect.DeepEqual(np[1:], mp) {
		t.Errorf("Netflix proxies = %v, want [手动选择]+手动选择组一致 %v", np, mp)
	}
	rules, _ := cfg["rules"].([]any)
	rsIdx := findRuleSetIndex(rules, "RULE-SET,Netflix,Netflix")
	geoipIdx := findRuleSetIndex(rules, "GEOIP,CN,DIRECT")
	matchIdx := findRuleSetIndex(rules, "MATCH,漏网之鱼")
	if rsIdx < 0 || geoipIdx < 0 || matchIdx < 0 || rsIdx > geoipIdx || rsIdx > matchIdx {
		t.Errorf("rules = %v, want RULE-SET,Netflix,Netflix 在列表最前（GEOIP 之前）", rules)
	}
}

// TestSubRuleSetMulti（R3 验收 A4 + R4）：逗号分隔双规则集 → 两个专属组、
// 两条 RULE-SET 各自指向、rule-providers 含两者。
func TestSubRuleSetMulti(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	t1 := createRuleSetViaAPI(t, h, "Netflix", "https://x.example.com/nf.yaml", "domain", "yaml")
	t2 := createRuleSetViaAPI(t, h, "YouTube", "https://x.example.com/yt.txt", "classical", "text")
	ids := t1["id"].(string) + "," + t2["id"].(string)
	rec := do(h, subQuery(src.URL, map[string]string{"ruleset_id": ids}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	rps, ok := cfg["rule-providers"].(map[string]any)
	if !ok || len(rps) != 3 {
		t.Fatalf("rule-providers = %T %v, want 3 entries（双规则集 + 内置 gfw）", cfg["rule-providers"], cfg["rule-providers"])
	}
	for _, name := range []string{"Netflix", "YouTube", "gfw"} {
		if _, ok := rps[name]; !ok {
			t.Errorf("rule-providers 缺 %s", name)
		}
	}
	groups, _ := cfg["proxy-groups"].([]any)
	manual := findGroupByName(t, groups, "手动选择")
	mp := manual["proxies"].([]any)
	for _, name := range []string{"Netflix", "YouTube"} {
		g := findGroupByName(t, groups, name)
		if g["type"] != "select" {
			t.Errorf("%s group type = %v, want select", name, g["type"])
		}
		// P3：专属组 proxies 首位引用「手动选择」组，其后与手动组 proxies 一致
		if gp, ok := g["proxies"].([]any); !ok || len(gp) != len(mp)+1 || gp[0] != "手动选择" || !reflect.DeepEqual(gp[1:], mp) {
			t.Errorf("%s proxies = %v, want [手动选择]+手动选择组一致 %v", name, g["proxies"], mp)
		}
	}
	rules, _ := cfg["rules"].([]any)
	geoipIdx := findRuleSetIndex(rules, "GEOIP,CN,DIRECT")
	matchIdx := findRuleSetIndex(rules, "MATCH,漏网之鱼")
	for _, line := range []string{"RULE-SET,Netflix,Netflix", "RULE-SET,YouTube,YouTube"} {
		idx := findRuleSetIndex(rules, line)
		if idx < 0 || idx > geoipIdx || idx > matchIdx {
			t.Errorf("rules 缺 %q 或在 GEOIP/MATCH 后：%v", line, rules)
		}
	}
}

// TestSubRuleSetInvalid（R3 验收 A6）：规则集不存在/disabled → 400
// 「规则集不存在或已禁用」；多值中任一非法 → 400；无 store 实例同样 400。
func TestSubRuleSetInvalid(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())

	// 不存在
	rec := do(h, subQuery(src.URL, map[string]string{"ruleset_id": "deadbeef0000"}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "规则集不存在或已禁用") {
		t.Errorf("missing ruleset: status = %d body=%s, want 400 规则集不存在或已禁用", rec.Code, rec.Body.String())
	}

	// disabled
	tpl := createRuleSetViaAPI(t, h, "Netflix", "https://x.example.com/nf.yaml", "domain", "yaml")
	if rec := doJSON(h, http.MethodPut, "/api/v1/rule-sets/"+tpl["id"].(string), `{"enabled":false}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("disable ruleset: status = %d", rec.Code)
	}
	rec = do(h, subQuery(src.URL, map[string]string{"ruleset_id": tpl["id"].(string)}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "规则集不存在或已禁用") {
		t.Errorf("disabled ruleset: status = %d body=%s, want 400 规则集不存在或已禁用", rec.Code, rec.Body.String())
	}

	// 多值中任一非法 → 400（第一个合法、第二个不存在）
	tpl2 := createRuleSetViaAPI(t, h, "YouTube", "https://x.example.com/yt.txt", "classical", "text")
	rec = do(h, subQuery(src.URL, map[string]string{"ruleset_id": tpl2["id"].(string) + ",deadbeef0000"}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "规则集不存在或已禁用") {
		t.Errorf("multi with invalid: status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}

	// 无 store 实例（/sub 公开端点）+ ruleset_id → 400（不 panic）
	h2 := newTestServerNoStore(t)
	rec = do(h2, subQuery(src.URL, map[string]string{"ruleset_id": "x"}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no-store ruleset_id: status = %d, want 400", rec.Code)
	}
}

// TestSubRuleSetNameConflict（R3 验收 A5，P1-2/P1-1 修复后重写）：规则集名与地区组
// 重名 → 「(规则集)」后缀，RULE-SET 目标指向最终唯一组名；节点名 X 与 X(规则集) 并存
// 时规则集名 X 得「X(规则集)2」递增。同名规则集（不同 id）场景已被 P1-2 改为 400
// （见 TestSubRuleSetDuplicateNameRejected），递增路径改由节点名占用触发。
func TestSubRuleSetNameConflict(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody()) // 生成「香港节点」/「日本节点」地区组
	tpl := createRuleSetViaAPI(t, h, "香港节点", "https://x.example.com/hk.yaml", "domain", "yaml")
	rec := do(h, subQuery(src.URL, map[string]string{"ruleset_id": tpl["id"].(string)}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	groups, _ := cfg["proxy-groups"].([]any)
	findGroupByName(t, groups, "香港节点(规则集)") // 冲突 → 后缀生效
	rules, _ := cfg["rules"].([]any)
	if idx := findRuleSetIndex(rules, "RULE-SET,香港节点,香港节点(规则集)"); idx < 0 {
		t.Errorf("rules 缺 RULE-SET,香港节点,香港节点(规则集)：%v", rules)
	}

	// 第二个场景（P1-1 + 「(规则集)2」递增）：节点名 X 与 X(规则集) 并存、规则集名 X
	// 撞两者 → 专属组名「X(规则集)2」（同名规则集已被 P1-2 拦截为 400，递增路径只能
	// 靠节点/组名占用 X(规则集) 触达）。
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	body := fmt.Sprintf("ss://%s@example.com:8388#香港-01"+"\n"+"ss://%s@example.com:8388#香港-01(规则集)"+"\n", ui, ui)
	src2 := fakeSource(t, http.StatusOK, body)
	tpl2 := createRuleSetViaAPI(t, h, "香港-01", "https://x.example.com/hk2.yaml", "domain", "yaml")
	rec = do(h, subQuery(src2.URL, map[string]string{"ruleset_id": tpl2["id"].(string)}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	cfg = map[string]any{}
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	groups, _ = cfg["proxy-groups"].([]any)
	findGroupByName(t, groups, "香港-01(规则集)2")
	rules, _ = cfg["rules"].([]any)
	if idx := findRuleSetIndex(rules, "RULE-SET,香港-01,香港-01(规则集)2"); idx < 0 {
		t.Errorf("rules 缺 RULE-SET,香港-01,香港-01(规则集)2：%v", rules)
	}
}

// TestSubRuleSetDuplicateNameRejected（P1-2）：两个不同 id 但同名规则集 → 400
// 「规则集名称冲突」（rule-providers map 键覆盖会静默丢 URL）；/sub 与 convert
// 入口同样拦截。
func TestSubRuleSetDuplicateNameRejected(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	t1 := createRuleSetViaAPI(t, h, "Netflix", "https://x.example.com/nf.yaml", "domain", "yaml")
	t2 := createRuleSetViaAPI(t, h, "Netflix", "https://x.example.com/nf2.yaml", "domain", "yaml")
	ids := t1["id"].(string) + "," + t2["id"].(string)

	rec := do(h, subQuery(src.URL, map[string]string{"ruleset_id": ids}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "规则集名称冲突") {
		t.Errorf("/sub duplicate name: status = %d body=%s, want 400 规则集名称冲突", rec.Code, rec.Body.String())
	}
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/run", fmt.Sprintf(`{"url":%q,"ruleset_id":%q}`, src.URL, ids), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "规则集名称冲突") {
		t.Errorf("convert duplicate name: status = %d body=%s, want 400 规则集名称冲突", rec.Code, rec.Body.String())
	}
}

// TestSubRuleSetNameConflictsNode（P1-1）：规则集名与节点最终名重名（节点名恰为
// Netflix）或等于内置出站名 DIRECT → 专属组名加「(规则集)」后缀、RULE-SET 指向
// 后缀名、节点引用不受影响（否则 mihomo duplicate name 拒绝加载，而语法层
// output.Validate 放行）。
func TestSubRuleSetNameConflictsNode(t *testing.T) {
	h := newTestServer(t)
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	body := fmt.Sprintf("ss://%s@example.com:8388#Netflix"+"\n"+"ss://%s@example.com:8388#🇯🇵 日本-01"+"\n", ui, ui)
	src := fakeSource(t, http.StatusOK, body)
	t1 := createRuleSetViaAPI(t, h, "Netflix", "https://x.example.com/nf.yaml", "domain", "yaml")
	t2 := createRuleSetViaAPI(t, h, "DIRECT", "https://x.example.com/direct.yaml", "domain", "yaml")
	ids := t1["id"].(string) + "," + t2["id"].(string)
	rec := do(h, subQuery(src.URL, map[string]string{"ruleset_id": ids}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	groups, _ := cfg["proxy-groups"].([]any)
	// 撞节点名 → 后缀；专属组 proxies = [手动选择, ...手动选择组]（含组引用，不受影响）
	ng := findGroupByName(t, groups, "Netflix(规则集)")
	manual := findGroupByName(t, groups, "手动选择")
	mp := manual["proxies"].([]any)
	np := ng["proxies"].([]any)
	if len(np) != len(mp)+1 || np[0] != "手动选择" || !reflect.DeepEqual(np[1:], mp) {
		t.Errorf("Netflix(规则集) proxies = %v, want [手动选择]+手动选择组一致 %v", np, mp)
	}
	// 撞内置出站名 DIRECT → 后缀
	findGroupByName(t, groups, "DIRECT(规则集)")
	rules, _ := cfg["rules"].([]any)
	geoipIdx := findRuleSetIndex(rules, "GEOIP,CN,DIRECT")
	matchIdx := findRuleSetIndex(rules, "MATCH,漏网之鱼")
	for _, line := range []string{"RULE-SET,Netflix,Netflix(规则集)", "RULE-SET,DIRECT,DIRECT(规则集)"} {
		idx := findRuleSetIndex(rules, line)
		if idx < 0 || idx > geoipIdx || idx > matchIdx {
			t.Errorf("rules 缺 %q 或在 GEOIP/MATCH 后：%v", line, rules)
		}
	}
}

// TestSubRuleSetDedupID（P2-2）：ruleset_id 重复 id（a,a）→ 去重只保留一个，
// 产物仅一个专属组 + 一条 RULE-SET（避免同一规则集生成两份）。
func TestSubRuleSetDedupID(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	tpl := createRuleSetViaAPI(t, h, "Netflix", "https://x.example.com/nf.yaml", "domain", "yaml")
	id := tpl["id"].(string)
	rec := do(h, subQuery(src.URL, map[string]string{"ruleset_id": id + "," + id}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("response is not valid yaml: %v", err)
	}
	rps, ok := cfg["rule-providers"].(map[string]any)
	if !ok || len(rps) != 2 {
		t.Errorf("rule-providers = %T %v, want 2 entries（重复 id 去重 + 内置 gfw）", cfg["rule-providers"], cfg["rule-providers"])
	}
	if _, ok := rps["gfw"]; !ok {
		t.Errorf("rule-providers 缺内置 gfw，实际 %v", rps)
	}
	groups, _ := cfg["proxy-groups"].([]any)
	if n := countGroupNames(groups, "Netflix"); n != 1 {
		t.Errorf("Netflix 专属组数量 = %d, want 1", n)
	}
	rules, _ := cfg["rules"].([]any)
	if n := countRuleLines(rules, "RULE-SET,Netflix,Netflix"); n != 1 {
		t.Errorf("RULE-SET,Netflix,Netflix 数量 = %d, want 1", n)
	}
}

// countGroupNames 统计 groups 中名为 name 的组数量。
func countGroupNames(groups []any, name string) int {
	n := 0
	for _, g := range groups {
		if m, ok := g.(map[string]any); ok && m["name"] == name {
			n++
		}
	}
	return n
}

// countRuleLines 统计 rules 中与 line 完全相等的行数。
func countRuleLines(rules []any, line string) int {
	n := 0
	for _, r := range rules {
		if r == line {
			n++
		}
	}
	return n
}

// TestConvertRuleSetMulti（R4）：convert/run 与 preview 的 ruleset_id
// 逗号分隔多值 → 两个专属组 + rule-providers 两条；任一非法 → 400。
func TestConvertRuleSetMulti(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	t1 := createRuleSetViaAPI(t, h, "Netflix", "https://x.example.com/nf.yaml", "domain", "yaml")
	t2 := createRuleSetViaAPI(t, h, "YouTube", "https://x.example.com/yt.txt", "classical", "text")
	ids := t1["id"].(string) + "," + t2["id"].(string)

	rec := doJSON(h, http.MethodPost, "/api/v1/convert/run", fmt.Sprintf(`{"url":%q,"ruleset_id":%q}`, src.URL, ids), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("run status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		YAML string `json:"yaml"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("run body not json: %v", err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(resp.YAML), &cfg); err != nil {
		t.Fatalf("yaml field not valid: %v", err)
	}
	rps, _ := cfg["rule-providers"].(map[string]any)
	if len(rps) != 3 {
		t.Errorf("rule-providers len = %d, want 3（双规则集 + 内置 gfw）", len(rps))
	}
	if _, ok := rps["gfw"]; !ok {
		t.Errorf("rule-providers 缺内置 gfw，实际 %v", rps)
	}
	groups, _ := cfg["proxy-groups"].([]any)
	findGroupByName(t, groups, "Netflix")
	findGroupByName(t, groups, "YouTube")

	// preview 同支持多值
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"url":%q,"ruleset_id":%q}`, src.URL, ids), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var pv struct {
		Groups []map[string]any `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil {
		t.Fatalf("preview body not json: %v", err)
	}
	findGroupByNameMaps(t, pv.Groups, "Netflix")
	findGroupByNameMaps(t, pv.Groups, "YouTube")

	// 多值任一非法 → 400
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/run", fmt.Sprintf(`{"url":%q,"ruleset_id":%q}`, src.URL, t1["id"].(string)+",deadbeef0000"), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "规则集不存在或已禁用") {
		t.Errorf("multi invalid: status = %d body=%s, want 400 规则集不存在或已禁用", rec.Code, rec.Body.String())
	}
}

// TestLogRetryRuleSetMulti（R3 验收 A9）：retry 保留多 ruleset_id 并注入。
func TestLogRetryRuleSetMulti(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())
	t1 := createRuleSetViaAPI(t, h, "Netflix", "https://x.example.com/nf.yaml", "domain", "yaml")
	t2 := createRuleSetViaAPI(t, h, "YouTube", "https://x.example.com/yt.txt", "classical", "text")
	ids := t1["id"].(string) + "," + t2["id"].(string)

	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"url":%q,"ruleset_id":%q}`, src.URL, ids), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	logs := st.ListLogs(10)
	if len(logs) != 1 {
		t.Fatalf("logs len = %d, want 1", len(logs))
	}
	if got, _ := logs[0].Params["ruleset_id"].(string); got != ids {
		t.Errorf("log params ruleset_id = %q, want %q", got, ids)
	}
	rec = doJSON(h, http.MethodPost, "/api/v1/logs/"+logs[0].ID+"/retry", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// 重试产物含两个专属组
	var resp struct {
		Groups []map[string]any `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("retry body not json: %v", err)
	}
	findGroupByNameMaps(t, resp.Groups, "Netflix")
	findGroupByNameMaps(t, resp.Groups, "YouTube")
	// 新日志 Params 保留多值 ruleset_id（entry.Params 原样透传）
	logs = st.ListLogs(10)
	if got, _ := logs[0].Params["ruleset_id"].(string); got != ids {
		t.Errorf("retry log params ruleset_id = %q, want %q", got, ids)
	}
}

// ---------- R4：数据源多选聚合 ----------

// proxyNamesOrdered 解析 YAML 产物并返回 proxies 节点名（按产物顺序）。
func proxyNamesOrdered(t *testing.T, data []byte) []string {
	t.Helper()
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("not valid yaml: %v", err)
	}
	proxies, ok := cfg["proxies"].([]any)
	if !ok {
		t.Fatalf("proxies = %T, want list", cfg["proxies"])
	}
	names := make([]string, 0, len(proxies))
	for _, p := range proxies {
		names = append(names, p.(map[string]any)["name"].(string))
	}
	return names
}

// TestSubMultiSource（R4 验收 A1）：src=a,b 逗号多值 → 两源节点聚合输出；
// src=单 ID 与旧行为一致（单源输出）。
func TestSubMultiSource(t *testing.T) {
	h, st := newTestServerWithStore(t)
	srcA := fakeSource(t, http.StatusOK, subBody()) // 2 节点（香港/日本）
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	srcB := fakeSource(t, http.StatusOK, fmt.Sprintf("ss://%s@example.com:8388#🇺🇸 美国-01\n", ui)) // 1 节点
	a, err := st.CreateSource("机场A", srcA.URL, true)
	if err != nil {
		t.Fatalf("CreateSource A: %v", err)
	}
	b, err := st.CreateSource("机场B", srcB.URL, true)
	if err != nil {
		t.Fatalf("CreateSource B: %v", err)
	}

	// 多值聚合
	rec := do(h, "/sub?target=clash&src="+a.ID+","+b.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("multi src: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	names := proxyNamesOrdered(t, rec.Body.Bytes())
	if len(names) != 3 {
		t.Fatalf("multi src proxies = %v, want 3 节点（两源聚合）", names)
	}
	want := map[string]bool{"🇭🇰 香港-01": true, "🇯🇵 日本-01": true, "🇺🇸 美国-01": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("multi src 意外节点 %q，聚合结果 %v", n, names)
		}
	}

	// 单 ID 兼容（旧行为不变）
	rec = do(h, "/sub?target=clash&src="+a.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("single src: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	names = proxyNamesOrdered(t, rec.Body.Bytes())
	if len(names) != 2 {
		t.Fatalf("single src proxies = %v, want 2 节点", names)
	}

	// 重复 ID 合并一次（a,a → 只有 a 的节点）
	rec = do(h, "/sub?target=clash&src="+a.ID+","+a.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("dup src: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	names = proxyNamesOrdered(t, rec.Body.Bytes())
	if len(names) != 2 {
		t.Fatalf("dup src proxies = %v, want 2 节点（重复 ID 合并）", names)
	}
}

// TestSubSrcInvalidID（R4 验收 A2）：任一 ID 不存在/禁用 → 400 且消息含该 ID；
// 纯逗号/空参 → 400；空段被去空（a,,b 正常聚合）。
func TestSubSrcInvalidID(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())
	a, err := st.CreateSource("机场A", src.URL, true)
	if err != nil {
		t.Fatalf("CreateSource A: %v", err)
	}
	dis, err := st.CreateSource("禁用源", src.URL, false)
	if err != nil {
		t.Fatalf("CreateSource disabled: %v", err)
	}

	// 多值中任一不存在 → 400 且消息含 badID
	rec := do(h, "/sub?target=clash&src="+a.ID+",deadbeef0000")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "deadbeef0000") {
		t.Errorf("multi with missing id: status = %d body=%s, want 400 含 deadbeef0000", rec.Code, rec.Body.String())
	}
	// 单值不存在 → 400 含 ID
	rec = do(h, "/sub?target=clash&src=deadbeef0000")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "deadbeef0000") {
		t.Errorf("missing id: status = %d body=%s, want 400 含 deadbeef0000", rec.Code, rec.Body.String())
	}
	// 禁用 → 400 含 ID
	rec = do(h, "/sub?target=clash&src="+dis.ID)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), dis.ID) {
		t.Errorf("disabled id: status = %d body=%s, want 400 含 %s", rec.Code, rec.Body.String(), dis.ID)
	}
	// 纯逗号（无任何 ID）→ 400
	rec = do(h, "/sub?target=clash&src=%2C%2C")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("pure comma: status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// 空段去空：a,,b 与 a,b 等价
	rec = do(h, "/sub?target=clash&src="+a.ID+",,")
	if rec.Code != http.StatusOK {
		t.Errorf("trailing empty segment: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// 无 store 实例 → 400（原消息不带 ID）
	h2 := newTestServerNoStore(t)
	rec = do(h2, "/sub?target=clash&src=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no-store src: status = %d, want 400", rec.Code)
	}
}

// TestSubSrcPlusURL（R4 验收 A3）：src=a,b & url=... → 混合聚合，已存源在前、
// url 源在后（不再互斥）。
func TestSubSrcPlusURL(t *testing.T) {
	h, st := newTestServerWithStore(t)
	srcA := fakeSource(t, http.StatusOK, subBody()) // 香港/日本
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	srcB := fakeSource(t, http.StatusOK, fmt.Sprintf("ss://%s@example.com:8388#🇺🇸 美国-01\n", ui))
	srcU := fakeSource(t, http.StatusOK, fmt.Sprintf("ss://%s@example.com:8388#🇩🇪 德国-01\n", ui))
	a, err := st.CreateSource("机场A", srcA.URL, true)
	if err != nil {
		t.Fatalf("CreateSource A: %v", err)
	}
	b, err := st.CreateSource("机场B", srcB.URL, true)
	if err != nil {
		t.Fatalf("CreateSource B: %v", err)
	}

	vals := url.Values{}
	vals.Set("target", "clash")
	vals.Set("src", a.ID+","+b.ID)
	vals.Set("url", srcU.URL)
	rec := do(h, "/sub?"+vals.Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("src+url mixed: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	names := proxyNamesOrdered(t, rec.Body.Bytes())
	want := []string{"🇭🇰 香港-01", "🇯🇵 日本-01", "🇺🇸 美国-01", "🇩🇪 德国-01"}
	if len(names) != len(want) {
		t.Fatalf("mixed proxies = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("mixed 顺序 = %v, want %v（已存源在前、url 源在后）", names, want)
			break
		}
	}
}

// TestConvertSourceIDs（R4 验收 A4）：source_ids=[a,b] 聚合；source_id 单值兼容；
// 并存 → 400；全空/空数组 → 400；空数组 + url 正常；任一非法 → 400。
func TestConvertSourceIDs(t *testing.T) {
	h, st := newTestServerWithStore(t)
	srcA := fakeSource(t, http.StatusOK, subBody())
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	srcB := fakeSource(t, http.StatusOK, fmt.Sprintf("ss://%s@example.com:8388#🇺🇸 美国-01\n", ui))
	a, err := st.CreateSource("机场A", srcA.URL, true)
	if err != nil {
		t.Fatalf("CreateSource A: %v", err)
	}
	b, err := st.CreateSource("机场B", srcB.URL, true)
	if err != nil {
		t.Fatalf("CreateSource B: %v", err)
	}

	// source_ids 数组多值 → 聚合
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_ids":[%q,%q]}`, a.ID, b.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("source_ids preview: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var pv struct {
		NodeCount int `json:"node_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil || pv.NodeCount != 3 {
		t.Errorf("source_ids preview node_count = %+v, want 3", pv)
	}

	// source_id 单值兼容
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_id":%q}`, a.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("source_id preview: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	pv = struct {
		NodeCount int `json:"node_count"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil || pv.NodeCount != 2 {
		t.Errorf("source_id preview node_count = %+v, want 2", pv)
	}

	// source_id 与 source_ids 并存 → 400
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_id":%q,"source_ids":[%q]}`, a.ID, b.ID), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "source_id 与 source_ids 不能同时指定") {
		t.Errorf("both specified: status = %d body=%s, want 400 并存拦截", rec.Code, rec.Body.String())
	}

	// 全空 / 空数组 → 400
	for _, body := range []string{`{}`, `{"source_ids":[]}`, `{"source_id":""}`} {
		rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("empty source %s: status = %d, want 400", body, rec.Code)
		}
	}

	// 空数组视为未提供 → 可与 url 正常使用
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_ids":[],"url":%q}`, srcA.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty array + url: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	pv = struct {
		NodeCount int `json:"node_count"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil || pv.NodeCount != 2 {
		t.Errorf("empty array + url node_count = %+v, want 2", pv)
	}

	// source_ids 任一非法 → 400（消息含 badID）
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_ids":[%q,"deadbeef0000"]}`, a.ID), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "deadbeef0000") {
		t.Errorf("source_ids with missing: status = %d body=%s, want 400 含 deadbeef0000", rec.Code, rec.Body.String())
	}
}

// TestConvertSourceIDsPlusURL（R4 验收 A4 混合）：source_ids + url → 混合聚合
// （已存源在前、url 源在后），run 与 preview 均支持。
func TestConvertSourceIDsPlusURL(t *testing.T) {
	h, st := newTestServerWithStore(t)
	srcA := fakeSource(t, http.StatusOK, subBody()) // 香港/日本
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	srcU := fakeSource(t, http.StatusOK, fmt.Sprintf("ss://%s@example.com:8388#🇩🇪 德国-01\n", ui))
	a, err := st.CreateSource("机场A", srcA.URL, true)
	if err != nil {
		t.Fatalf("CreateSource A: %v", err)
	}

	rec := doJSON(h, http.MethodPost, "/api/v1/convert/run", fmt.Sprintf(`{"source_ids":[%q],"url":%q}`, a.ID, srcU.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("run mixed: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		YAML string `json:"yaml"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("run body not json: %v", err)
	}
	names := proxyNamesOrdered(t, []byte(resp.YAML))
	want := []string{"🇭🇰 香港-01", "🇯🇵 日本-01", "🇩🇪 德国-01"}
	if len(names) != len(want) {
		t.Fatalf("run mixed proxies = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("run mixed 顺序 = %v, want %v（已存源在前、url 源在后）", names, want)
			break
		}
	}

	// preview 同支持混合
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_ids":[%q],"url":%q}`, a.ID, srcU.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview mixed: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var pv struct {
		NodeCount int `json:"node_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil || pv.NodeCount != 3 {
		t.Errorf("preview mixed node_count = %+v, want 3", pv)
	}
}

// TestLogRetryMultiSource（R4 验收 A5）：日志 SourceID/SourceName 逗号多值；
// 多源日志与混合（source_ids+url）日志 retry 成功；任一源已删 → 409
// 「订阅源已删除」；已禁用 → 409「订阅源已禁用」。
func TestLogRetryMultiSource(t *testing.T) {
	h, st := newTestServerWithStore(t)
	srcA := fakeSource(t, http.StatusOK, subBody()) // 2 节点
	ui := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password"))
	srcB := fakeSource(t, http.StatusOK, fmt.Sprintf("ss://%s@example.com:8388#🇺🇸 美国-01\n", ui)) // 1 节点
	srcU := fakeSource(t, http.StatusOK, fmt.Sprintf("ss://%s@example.com:8388#🇩🇪 德国-01\n", ui))
	a, err := st.CreateSource("机场A", srcA.URL, true)
	if err != nil {
		t.Fatalf("CreateSource A: %v", err)
	}
	b, err := st.CreateSource("机场B", srcB.URL, true)
	if err != nil {
		t.Fatalf("CreateSource B: %v", err)
	}
	multiIDs := a.ID + "," + b.ID

	// 多源 preview → 日志 SourceID/SourceName 逗号多值 → retry 成功（3 节点）
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_ids":[%q,%q]}`, a.ID, b.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("multi preview: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	logs := st.ListLogs(10)
	if len(logs) == 0 {
		t.Fatal("no logs after preview")
	}
	if logs[0].SourceID != multiIDs {
		t.Errorf("log SourceID = %q, want %q（逗号多值）", logs[0].SourceID, multiIDs)
	}
	if logs[0].SourceName != "机场A,机场B" {
		t.Errorf("log SourceName = %q, want 机场A,机场B", logs[0].SourceName)
	}
	retry := doJSON(h, http.MethodPost, "/api/v1/logs/"+logs[0].ID+"/retry", "", nil)
	if retry.Code != http.StatusOK {
		t.Fatalf("multi retry: status = %d, want 200; body=%s", retry.Code, retry.Body.String())
	}
	var resp struct {
		NodeCount int `json:"node_count"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &resp); err != nil || resp.NodeCount != 3 {
		t.Errorf("multi retry node_count = %+v, want 3", resp)
	}
	// 新日志保留逗号多值
	logs = st.ListLogs(10)
	if logs[0].SourceID != multiIDs {
		t.Errorf("retry log SourceID = %q, want %q", logs[0].SourceID, multiIDs)
	}

	// 混合日志（source_ids + url）→ retry 成功（3 节点），URLFull 保留 url 部分
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_ids":[%q],"url":%q}`, a.ID, srcU.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("mixed preview: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	logs = st.ListLogs(10)
	if logs[0].SourceID != a.ID || logs[0].URLFull != srcU.URL {
		t.Errorf("mixed log = SourceID %q URLFull %q, want %q / %q", logs[0].SourceID, logs[0].URLFull, a.ID, srcU.URL)
	}
	retry = doJSON(h, http.MethodPost, "/api/v1/logs/"+logs[0].ID+"/retry", "", nil)
	if retry.Code != http.StatusOK {
		t.Fatalf("mixed retry: status = %d, want 200; body=%s", retry.Code, retry.Body.String())
	}
	resp = struct {
		NodeCount int `json:"node_count"`
	}{}
	if err := json.Unmarshal(retry.Body.Bytes(), &resp); err != nil || resp.NodeCount != 3 {
		t.Errorf("mixed retry node_count = %+v, want 3", resp)
	}

	// 任一源已删 → 409 订阅源已删除
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_ids":[%q,%q]}`, a.ID, b.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview for delete-case: status = %d", rec.Code)
	}
	delLogID := st.ListLogs(10)[0].ID
	if err := st.DeleteSource(b.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	retry = doJSON(h, http.MethodPost, "/api/v1/logs/"+delLogID+"/retry", "", nil)
	if retry.Code != http.StatusConflict || !strings.Contains(retry.Body.String(), "订阅源已删除") {
		t.Errorf("retry deleted source: status = %d body=%s, want 409 订阅源已删除", retry.Code, retry.Body.String())
	}

	// 任一源已禁用 → 409 订阅源已禁用（b 在上一步已删，重新创建）
	b, err = st.CreateSource("机场B", srcB.URL, true)
	if err != nil {
		t.Fatalf("CreateSource B (recreate): %v", err)
	}
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"source_ids":[%q,%q]}`, a.ID, b.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview for disable-case: status = %d", rec.Code)
	}
	disLogID := st.ListLogs(10)[0].ID
	if _, err := st.UpdateSource(b.ID, store.SourcePatch{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable source: %v", err)
	}
	retry = doJSON(h, http.MethodPost, "/api/v1/logs/"+disLogID+"/retry", "", nil)
	if retry.Code != http.StatusConflict || !strings.Contains(retry.Body.String(), "订阅源已禁用") {
		t.Errorf("retry disabled source: status = %d body=%s, want 409 订阅源已禁用", retry.Code, retry.Body.String())
	}
}

// TestLogRetryDuplicateSourceID（P2）日志 SourceID 含重复 ID（如 a,a）时 retry
// 按去重后的源数量执行——与 /sub、convert 的 resolveSourceIDs 合并语义一致，
// 不能把同一源拉两次。
func TestLogRetryDuplicateSourceID(t *testing.T) {
	h, st := newTestServerWithStore(t)
	srcA := fakeSource(t, http.StatusOK, subBody()) // 2 节点
	a, err := st.CreateSource("机场A", srcA.URL, true)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	dupIDs := a.ID + "," + a.ID
	if _, err := st.AppendLog(store.LogEntry{Kind: "convert", SourceID: dupIDs, Status: "ok"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	logID := st.ListLogs(10)[0].ID

	retry := doJSON(h, http.MethodPost, "/api/v1/logs/"+logID+"/retry", "", nil)
	if retry.Code != http.StatusOK {
		t.Fatalf("dedup retry: status = %d, want 200; body=%s", retry.Code, retry.Body.String())
	}
	var resp struct {
		NodeCount int `json:"node_count"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &resp); err != nil || resp.NodeCount != 2 {
		t.Errorf("dedup retry node_count = %+v, want 2（重复 ID 只拉一次）", resp)
	}
	// 重复 ID 的日志本身不因去重而改写（原样保留，与新日志字段无关）
	latest := st.ListLogs(10)[0]
	if latest.SourceID != dupIDs {
		t.Errorf("retry log SourceID = %q, want %q（原样保留）", latest.SourceID, dupIDs)
	}
}

// ---------- R7：内置 GFW 规则集 + 漏网之鱼兜底 ----------

// TestSubGFWDefault（R7 A1/A2/A5）：缺省与显式 gfw=true 均注入内置 gfw
// （rule-providers.gfw 字段契约 + RULE-SET,gfw,手动选择 在 GEOIP 之前），
// MATCH 兜底指向「漏网之鱼」；gfw=false 无 gfw 注入但兜底不变。
func TestSubGFWDefault(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	for _, extra := range []map[string]string{nil, {"gfw": "true"}} {
		rec := do(h, subQuery(src.URL, extra))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var cfg map[string]any
		if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
			t.Fatalf("not valid yaml: %v", err)
		}
		rps, ok := cfg["rule-providers"].(map[string]any)
		if !ok || len(rps) != 1 {
			t.Fatalf("rule-providers = %T %v, want 1 entry（内置 gfw）", cfg["rule-providers"], cfg["rule-providers"])
		}
		g := rps["gfw"].(map[string]any)
		if g["type"] != "http" || g["url"] != "https://raw.githubusercontent.com/Loyalsoldier/clash-rules/release/gfw.txt" ||
			g["behavior"] != "domain" || g["format"] != "yaml" || g["interval"] != 86400 || g["path"] != "./ruleset/gfw.yaml" {
			t.Errorf("gfw provider entry = %v", g)
		}
		rules, _ := cfg["rules"].([]any)
		geoipIdx := findRuleSetIndex(rules, "GEOIP,CN,DIRECT")
		gfwIdx := findRuleSetIndex(rules, "RULE-SET,gfw,手动选择")
		matchIdx := findRuleSetIndex(rules, "MATCH,漏网之鱼")
		if gfwIdx < 0 || gfwIdx > geoipIdx || gfwIdx > matchIdx || matchIdx < geoipIdx {
			t.Errorf("rules = %v, want gfw 在 GEOIP 前、MATCH,漏网之鱼 最后", rules)
		}
	}
}

// TestSubGFWOff（R7 A5）：gfw=false（/sub query）不注入 gfw——无 rule-providers
// 段、无 RULE-SET,gfw 行；规则列表回到 [GEOIP,CN,DIRECT, MATCH,漏网之鱼]。
func TestSubGFWOff(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	rec := do(h, subQuery(src.URL, map[string]string{"gfw": "false"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("not valid yaml: %v", err)
	}
	if _, ok := cfg["rule-providers"]; ok {
		t.Errorf("gfw=false 不应有 rule-providers 段：%v", cfg["rule-providers"])
	}
	rules, _ := cfg["rules"].([]any)
	if len(rules) != 2 || rules[0] != "GEOIP,CN,DIRECT" || rules[1] != "MATCH,漏网之鱼" {
		t.Errorf("rules = %v, want [GEOIP,CN,DIRECT MATCH,漏网之鱼]", rules)
	}
}

// TestSubGFWNameConflict（R7 A6）：用户自建规则集名为 gfw 且默认注入内置 GFW
// → 400「规则集名称冲突」（不静默覆盖）；gfw=false 时用户自己的 gfw 规则集可正常使用。
func TestSubGFWNameConflict(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	tpl := createRuleSetViaAPI(t, h, "gfw", "https://x.example.com/gfw.yaml", "domain", "yaml")
	rec := do(h, subQuery(src.URL, map[string]string{"ruleset_id": tpl["id"].(string)}))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "规则集名称冲突") || !strings.Contains(rec.Body.String(), "内置 GFW") {
		t.Errorf("status = %d body=%s, want 400 规则集名称冲突（与内置 GFW 同级）", rec.Code, rec.Body.String())
	}
	// gfw=false 关闭内置 → 用户规则集正常注入
	rec = do(h, subQuery(src.URL, map[string]string{"ruleset_id": tpl["id"].(string), "gfw": "false"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("gfw=false 时应 200，实际 %d; body=%s", rec.Code, rec.Body.String())
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("not valid yaml: %v", err)
	}
	rps, _ := cfg["rule-providers"].(map[string]any)
	if len(rps) != 1 {
		t.Errorf("rule-providers = %v, want 1 entry（用户 gfw 规则集）", rps)
	}
}

// TestConvertGFWOff（R7 A5/A7 JSON 路径）：convert/run 缺省输出含 gfw；
// gfw:false 无 rule-providers 与 RULE-SET,gfw；preview 不受影响（200）。
func TestConvertGFWOff(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, subBody())
	// 缺省 = 内置 gfw 注入
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/run", fmt.Sprintf(`{"url":%q}`, src.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("default run: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var def struct {
		YAML string `json:"yaml"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &def); err != nil {
		t.Fatalf("run body not json: %v", err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(def.YAML), &cfg); err != nil {
		t.Fatalf("yaml not valid: %v", err)
	}
	if _, ok := cfg["rule-providers"].(map[string]any)["gfw"]; !ok {
		t.Errorf("default convert/run 应含内置 gfw: %v", cfg["rule-providers"])
	}
	// gfw:false → 无 gfw
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/run", fmt.Sprintf(`{"url":%q,"gfw":false}`, src.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("gfw=false run: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	def.YAML = ""
	if err := json.Unmarshal(rec.Body.Bytes(), &def); err != nil {
		t.Fatalf("run body not json: %v", err)
	}
	cfg = map[string]any{}
	if err := yaml.Unmarshal([]byte(def.YAML), &cfg); err != nil {
		t.Fatalf("yaml not valid: %v", err)
	}
	if _, ok := cfg["rule-providers"]; ok {
		t.Errorf("gfw=false 不应有 rule-providers: %v", cfg["rule-providers"])
	}
	if rules, _ := cfg["rules"].([]any); len(rules) != 2 || rules[1] != "MATCH,漏网之鱼" {
		t.Errorf("rules = %v, want [GEOIP MATCH,漏网之鱼]", cfg["rules"])
	}
	// preview 带 gfw:false 200（开关不影响 preview 自身）
	rec = doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"url":%q,"gfw":false}`, src.URL), nil)
	if rec.Code != http.StatusOK {
		t.Errorf("preview gfw=false: status = %d; body=%s", rec.Code, rec.Body.String())
	}
}

// TestLogRetryGFW（R7 A5 重放）：preview gfw:false → 日志 Params 记录 gfw=false，
// retry 200；旧日志（无 gfw 键，RulesetID 指向名为 gfw 的规则集）→ retry 400 规则集
// 名称冲突——证明缺省开语义在重放路径成立。
func TestLogRetryGFW(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())
	// gfw=false 显式：preview 成功、日志 Params.gfw=false、retry 成功
	rec := doJSON(h, http.MethodPost, "/api/v1/convert/preview", fmt.Sprintf(`{"url":%q,"gfw":false}`, src.URL), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	log := st.ListLogs(10)[0]
	if v, _ := log.Params["gfw"].(bool); v != false {
		t.Errorf("log params gfw = %v, want false（gfw=false 显式）", log.Params["gfw"])
	}
	retry := doJSON(h, http.MethodPost, "/api/v1/logs/"+log.ID+"/retry", "", nil)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry gfw=false: status = %d; body=%s", retry.Code, retry.Body.String())
	}
	// 旧日志无 gfw 键 + 用户规则集名为 gfw → retry 缺省开触发名称冲突 400
	tpl := createRuleSetViaAPI(t, h, "gfw", "https://x.example.com/gfw.yaml", "domain", "yaml")
	if _, err := st.AppendLog(store.LogEntry{Kind: "convert", URLFull: src.URL, Params: map[string]any{"ruleset_id": tpl["id"].(string)}}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	oldLog := st.ListLogs(10)[0]
	retry = doJSON(h, http.MethodPost, "/api/v1/logs/"+oldLog.ID+"/retry", "", nil)
	if retry.Code != http.StatusBadRequest || !strings.Contains(retry.Body.String(), "规则集名称冲突") {
		t.Errorf("retry old-log default-on: status = %d body=%s, want 400 规则集名称冲突", retry.Code, retry.Body.String())
	}
}

// TestLogRetryGFWBackfill（FIX-3）：旧日志缺 gfw 键 → retry 成功后的新日志 Params
// 补记实际生效值 gfw=true（而非原样缺键透传）；旧日志显式 gfw=false → 新日志保持
// false（不覆盖显式值）。
func TestLogRetryGFWBackfill(t *testing.T) {
	h, st := newTestServerWithStore(t)
	src := fakeSource(t, http.StatusOK, subBody())

	// 旧日志缺 gfw 键（R7 之前产生的日志）→ retry 按缺省 true 执行，新日志补记
	if _, err := st.AppendLog(store.LogEntry{Kind: "convert", URLFull: src.URL, Params: map[string]any{}}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	oldLog := st.ListLogs(10)[0]
	if _, ok := oldLog.Params["gfw"]; ok {
		t.Fatalf("前置条件: 旧日志不应有 gfw 键, got %v", oldLog.Params["gfw"])
	}
	retry := doJSON(h, http.MethodPost, "/api/v1/logs/"+oldLog.ID+"/retry", "", nil)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry: status = %d; body=%s", retry.Code, retry.Body.String())
	}
	newLog := st.ListLogs(10)[0]
	if v, ok := newLog.Params["gfw"].(bool); !ok || v != true {
		t.Errorf("新日志 params gfw = %v (ok=%v), want true（缺省补记）", newLog.Params["gfw"], ok)
	}

	// 显式 gfw=false 的旧日志 → 重放后新日志保持 false
	if _, err := st.AppendLog(store.LogEntry{Kind: "convert", URLFull: src.URL, Params: map[string]any{"gfw": false}}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	retry = doJSON(h, http.MethodPost, "/api/v1/logs/"+st.ListLogs(10)[0].ID+"/retry", "", nil)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry gfw=false: status = %d; body=%s", retry.Code, retry.Body.String())
	}
	if v, _ := st.ListLogs(10)[0].Params["gfw"].(bool); v != false {
		t.Errorf("新日志 params gfw = %v, want false（显式值保留）", st.ListLogs(10)[0].Params["gfw"])
	}
}
