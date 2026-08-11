package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if v.Version != "0.1.0" || v.Mihomo != "v1.19.29" {
		t.Errorf("version payload = %+v, want {0.1.0 v1.19.29}", v)
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
	if !ok || len(rules) != 2 {
		t.Fatalf("rules = %T %v, want 2 entries", cfg["rules"], cfg["rules"])
	}
	if rules[0] != "GEOIP,CN,DIRECT" || rules[1] != "MATCH,手动选择" {
		t.Errorf("rules = %v, want [GEOIP,CN,DIRECT MATCH,手动选择]", rules)
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
// 原始名→组名不变且无 emoji）、组 proxies 引用与 proxies 段一致、rules[1]
// 仍为 MATCH,手动选择。
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
	for _, want := range []string{"手动选择", "自动选择", "香港节点", "日本节点", "DIRECT", "REJECT"} {
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

	// rules 兜底仍指向无 emoji 的手动选择
	rules, ok := cfg["rules"].([]any)
	if !ok || len(rules) != 2 {
		t.Fatalf("rules = %T %v, want 2 entries", cfg["rules"], cfg["rules"])
	}
	if rules[0] != "GEOIP,CN,DIRECT" || rules[1] != "MATCH,手动选择" {
		t.Errorf("rules = %v, want [GEOIP,CN,DIRECT MATCH,手动选择]", rules)
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
