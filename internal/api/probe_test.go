// 规则集自动探测测试：analyzeRuleHead / previewLines 纯函数判定 +
// /api/v1/rule-sets/probe handler 级行为（httptest 本地规则源 + 错误映射
// + 鉴权）。
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 纯函数：analyzeRuleHead ----------

func TestAnalyzeRuleHeadFormat(t *testing.T) {
	cases := []struct {
		name string
		head string
		want string
	}{
		{"yaml payload 首行", "payload:\n  - DOMAIN-SUFFIX,a.com\n", "yaml"},
		{"yaml 带 BOM", "\xef\xbb\xbfpayload:\n  - DOMAIN,a.com\n", "yaml"},
		{"yaml 首行注释后 payload", "# comment\npayload:\n  - DOMAIN,a.com\n", "yaml"},
		{"yaml payload 尾随注释", "payload: # generated\n  - DOMAIN-SUFFIX,a.com\n", "yaml"},
		{"text payload 非注释后缀", "payload: some-other-key\nDOMAIN,a.com\n", "text"},
		{"text 规则列表", "DOMAIN-SUFFIX,a.com\nDOMAIN,b.com\n", "text"},
		{"text 首行注释", "# header\nDOMAIN,a.com\n", "text"},
	}
	for _, tc := range cases {
		format, _, _ := analyzeRuleHead([]byte(tc.head))
		if format != tc.want {
			t.Errorf("%s: format = %q, want %q", tc.name, format, tc.want)
		}
	}
}

func TestAnalyzeRuleHeadBehavior(t *testing.T) {
	cases := []struct {
		name    string
		head    string
		wantBeh string
		wantSub string // reason 必须包含的子串（空串不检查）
	}{
		{"domain 全 DOMAIN 系列", strings.Repeat("DOMAIN-SUFFIX,a.com\n", 6), "domain", "6/6"},
		{"domain 含 YAML 列表前缀", strings.Repeat("  - DOMAIN-SUFFIX,a.com\n", 6), "domain", "6/6"},
		{"ipcidr IP-CIDR 系列", strings.Repeat("IP-CIDR,10.0.0.0/8\n", 6), "ipcidr", "6/6"},
		{"ipcidr 纯 IP 行 IPv4", "1.2.3.4/24\n10.0.0.0/8\n192.168.0.0/16\n172.16.0.0/12\n8.8.8.8\n9.9.9.9\n", "ipcidr", "6/6"},
		{"ipcidr 纯 IP 行 IPv6", "2001:db8::/32\n2001:db8::1\nfe80::/10\nfd00::/8\n::1\n2001:4860:4860::8888\n", "ipcidr", "6/6"},
		{"classical 混合", "GEOIP,CN\nMATCH\nDOMAIN-SUFFIX,a.com\nIP-CIDR,1.1.1.0/24\nPROCESS-NAME,sshd\nRULE-SET,https://x\n", "classical", ""},
		{"classical 其他未知类型", strings.Repeat("SOME-NEW-RULE,x\n", 6), "classical", ""},
	}
	for _, tc := range cases {
		_, beh, reason := analyzeRuleHead([]byte(tc.head))
		if beh != tc.wantBeh {
			t.Errorf("%s: behavior = %q, want %q (reason=%q)", tc.name, beh, tc.wantBeh, reason)
			continue
		}
		if tc.wantSub != "" && !strings.Contains(reason, tc.wantSub) {
			t.Errorf("%s: reason = %q, want contains %q", tc.name, reason, tc.wantSub)
		}
	}
}

// TestAnalyzeRuleHeadRatioBoundary 混合比例边界：低于 60% 不判定（classical），
// 恰好/超过 60% 判定。用 49 个有效行构造 29/49≈59% 与 30/49≈61%。
func TestAnalyzeRuleHeadRatioBoundary(t *testing.T) {
	below := "MATCH\n" + strings.Repeat("DOMAIN-SUFFIX,a.com\n", 29) + strings.Repeat("GEOIP,CN\n", 19)
	_, beh, _ := analyzeRuleHead([]byte(below))
	if beh != "classical" {
		t.Errorf("29/49 (≈59%%): behavior = %q, want classical", beh)
	}
	at := strings.Repeat("DOMAIN-SUFFIX,a.com\n", 30) + strings.Repeat("GEOIP,CN\n", 19)
	_, beh, reason := analyzeRuleHead([]byte(at))
	if beh != "domain" {
		t.Errorf("30/49 (≈61%%): behavior = %q, want domain", beh)
	}
	if !strings.Contains(reason, "30/49") {
		t.Errorf("reason = %q, want contains 30/49", reason)
	}
}

// TestAnalyzeRuleHeadEmptyOrShort 空文件/全注释/规则行不足 5 条 → behavior 空。
func TestAnalyzeRuleHeadEmptyOrShort(t *testing.T) {
	cases := []struct {
		name    string
		head    string
		want    string // reason 期望子串
		wantFmt bool   // format 是否应为非空（无有效行时为 ""）
	}{
		{"空文件", "", "未识别到规则行", false},
		{"全注释", "# a\n# b\n\n# c\n", "未识别到规则行", false},
		{"仅 3 条规则行", "DOMAIN-SUFFIX,a.com\nDOMAIN,b.com\nDOMAIN-KEYWORD,c.com\n", "样本不足", true},
	}
	for _, tc := range cases {
		format, beh, reason := analyzeRuleHead([]byte(tc.head))
		if beh != "" {
			t.Errorf("%s: behavior = %q, want empty", tc.name, beh)
		}
		if !strings.Contains(reason, tc.want) {
			t.Errorf("%s: reason = %q, want contains %q", tc.name, reason, tc.want)
		}
		if tc.wantFmt && format == "" {
			t.Errorf("%s: format = %q, want non-empty", tc.name, format)
		}
		if !tc.wantFmt && format != "" {
			t.Errorf("%s: format = %q, want empty（无有效行无法判定 format）", tc.name, format)
		}
	}
}

// TestAnalyzeRuleHeadScanCap 前 50 行上限：60 行中前 50 全 DOMAIN、后 10 行
// GEOIP——若扫描全部 60 行则 50/60≈83% 仍判 domain，无法区分；用反例：
// 前 50 行 DOMAIN、后 50 行 GEOIP（共 100 行），扫描全部则 50/100=50%
// 判 classical，只扫前 50 则 50/50 判 domain。
func TestAnalyzeRuleHeadScanCap(t *testing.T) {
	head := strings.Repeat("DOMAIN-SUFFIX,a.com\n", 50) + strings.Repeat("GEOIP,CN\n", 50)
	_, beh, reason := analyzeRuleHead([]byte(head))
	if beh != "domain" {
		t.Errorf("100 行（前 50 DOMAIN）: behavior = %q, want domain（前 50 行上限生效）", beh)
	}
	if !strings.Contains(reason, "50/50") {
		t.Errorf("reason = %q, want contains 50/50", reason)
	}
}

// TestAnalyzeRuleHeadStructuralLinesSkipped yaml 文件的 payload: 键与 ---
// 文档分隔符不计入统计窗口：占比不被稀释，样本不足判断也只看规则行数。
func TestAnalyzeRuleHeadStructuralLinesSkipped(t *testing.T) {
	rules := "  - DOMAIN-SUFFIX,a.com\n  - DOMAIN,b.com\n  - DOMAIN-KEYWORD,c.com\n" +
		"  - DOMAIN-WILDCARD,*.x.com\n  - DOMAIN-REGEX,^a$\n  - DOMAIN-SUFFIX,d.com\n"
	// 6 条 DOMAIN 规则 + payload: 键：若 payload: 计入窗口则 6/7≈86% 仍判
	// domain，无法区分；用 reason "6/6" 证明窗口只含规则行
	format, beh, reason := analyzeRuleHead([]byte("payload:\n" + rules))
	if format != "yaml" {
		t.Errorf("format = %q, want yaml", format)
	}
	if beh != "domain" {
		t.Errorf("behavior = %q, want domain（payload: 键不应稀释占比）", beh)
	}
	if !strings.Contains(reason, "6/6") {
		t.Errorf("reason = %q, want contains 6/6（窗口应只含 6 条规则行）", reason)
	}
	// --- 分隔符同样跳过；首行是 --- 时 format 判 text（已知简化：真实生成器
	// 不以 --- 开头，见交付说明）
	format, beh, reason = analyzeRuleHead([]byte("---\npayload:\n" + rules))
	if beh != "domain" {
		t.Errorf("带 --- : behavior = %q, want domain", beh)
	}
	if !strings.Contains(reason, "6/6") {
		t.Errorf("带 --- : reason = %q, want contains 6/6", reason)
	}
	if format != "text" {
		t.Errorf("带 --- : format = %q, want text（首行 --- 属已知简化）", format)
	}
	// 只有 payload: 键 + 1 条规则：规则行不足 5 条 → 样本不足（payload: 不算规则行）
	_, beh, reason = analyzeRuleHead([]byte("payload:\n  - DOMAIN,a.com\n"))
	if beh != "" {
		t.Errorf("1 条规则: behavior = %q, want empty", beh)
	}
	if !strings.Contains(reason, "1 条") {
		t.Errorf("1 条规则: reason = %q, want contains 1 条", reason)
	}
}

// TestPreviewLines 预览：仅取前 10 行；超长行按 200 runes 截断；尾随换行
// 不产生空串行。
func TestPreviewLines(t *testing.T) {
	long := "DOMAIN-SUFFIX," + strings.Repeat("a", 300) + ".com"
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		sb.WriteString(long)
		sb.WriteString("\n")
	}
	got := previewLines([]byte(sb.String()))
	if len(got) != 10 {
		t.Fatalf("len(preview) = %d, want 10", len(got))
	}
	if r := []rune(got[0]); len(r) != 200 {
		t.Errorf("first line runes = %d, want 200（超长行截断）", len(r))
	}
	// BOM 不应进入预览首行
	bommed := previewLines([]byte("\xef\xbb\xbfpayload:\n  - DOMAIN,a.com\n"))
	if strings.HasPrefix(bommed[0], "\xef\xbb\xbf") {
		t.Errorf("preview first line still has BOM: %q", bommed[0])
	}
	// 尾随 "\n" 不产生空串行（"a\nb\n" → ["a","b"]，多行空串同样剔除）
	tail := previewLines([]byte("a\nb\n"))
	if len(tail) != 2 || tail[0] != "a" || tail[1] != "b" {
		t.Errorf(`previewLines("a\nb\n") = %q, want [a b]`, tail)
	}
	tail2 := previewLines([]byte("a\nb\n\n"))
	if len(tail2) != 2 || tail2[1] != "b" {
		t.Errorf(`previewLines("a\nb\n\n") = %q, want [a b]`, tail2)
	}
	// 空输入：空切片 / 空字符串 / 仅换行 → 空列表（不 panic、无空串元素）
	for _, in := range [][]byte{nil, []byte(""), []byte("\n")} {
		got := previewLines(in)
		if len(got) != 0 {
			t.Errorf("previewLines(%q) = %q, want empty", in, got)
		}
	}
}

// ---------- handler：/api/v1/rule-sets/probe ----------

// doProbe 向 probe 端点 POST 一次 JSON body，返回 recorder。
func doProbe(h http.Handler, body string) *httptest.ResponseRecorder {
	return doJSON(h, http.MethodPost, "/api/v1/rule-sets/probe", body, nil)
}

// decodeProbe 解析 probe 成功响应。
func decodeProbe(t *testing.T, rec *httptest.ResponseRecorder) probeResp {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp probeResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body = %q, unmarshal: %v", rec.Body.String(), err)
	}
	return resp
}

func TestProbeRuleSetYamlDomain(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK,
		"payload:\n"+
			"  - DOMAIN-SUFFIX,example.com\n"+
			"  - DOMAIN-KEYWORD,google.com\n"+
			"  - DOMAIN,apple.com\n"+
			"  - DOMAIN-SUFFIX,netflix.com\n"+
			"  - DOMAIN-WILDCARD,*.youtube.com\n"+
			"  - DOMAIN-REGEX,^[a-z]+\\.cdn$\n")
	resp := decodeProbe(t, doProbe(h, fmt.Sprintf(`{"url":%q}`, src.URL)))
	if resp.Format != "yaml" {
		t.Errorf("format = %q, want yaml", resp.Format)
	}
	if resp.Behavior != "domain" {
		t.Errorf("behavior = %q, want domain", resp.Behavior)
	}
	if resp.Truncated {
		t.Error("truncated = true, want false")
	}
	if len(resp.Preview) == 0 || !strings.Contains(resp.Preview[0], "payload:") {
		t.Errorf("preview = %v, want first line payload:", resp.Preview)
	}
}

func TestProbeRuleSetTextIPCidr(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK,
		"1.2.3.0/24\nIP-CIDR,10.0.0.0/8\nIP-CIDR6,2001:db8::/32\n"+
			"2001:db8::1\nIP-CIDR,192.168.0.0/16\nIP-CIDR6,fe80::/10\n")
	resp := decodeProbe(t, doProbe(h, fmt.Sprintf(`{"url":%q}`, src.URL)))
	if resp.Format != "text" {
		t.Errorf("format = %q, want text", resp.Format)
	}
	if resp.Behavior != "ipcidr" {
		t.Errorf("behavior = %q, want ipcidr", resp.Behavior)
	}
}

func TestProbeRuleSetClassical(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK,
		"GEOIP,CN\nMATCH\nDOMAIN-SUFFIX,a.com\nIP-CIDR,1.1.1.0/24\nPROCESS-NAME,sshd\nRULE-SET,https://x\n")
	resp := decodeProbe(t, doProbe(h, fmt.Sprintf(`{"url":%q}`, src.URL)))
	if resp.Behavior != "classical" {
		t.Errorf("behavior = %q, want classical", resp.Behavior)
	}
}

// TestProbeRuleSetRedactsURL 响应 URL 脱敏：query token 不泄露。
func TestProbeRuleSetRedactsURL(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, "DOMAIN-SUFFIX,a.com\n")
	raw := src.URL + "/sub?token=SECRET"
	resp := decodeProbe(t, doProbe(h, fmt.Sprintf(`{"url":%q}`, raw)))
	if strings.Contains(resp.URL, "SECRET") {
		t.Errorf("resp.URL leaks token: %q", resp.URL)
	}
	if !strings.Contains(resp.URL, "xxxxx") {
		t.Errorf("resp.URL = %q, want masked token xxxxx", resp.URL)
	}
}

func TestProbeRuleSetErrors(t *testing.T) {
	h := newTestServer(t)
	// URL 为空 → 400
	if rec := doProbe(h, `{"url":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty url: status = %d, want 400", rec.Code)
	}
	// 结构非法 → 400
	if rec := doProbe(h, `{"url":"notaurl"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid url: status = %d, want 400", rec.Code)
	}
	// JSON 非法 → 400
	if rec := doProbe(h, `{"url":`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: status = %d, want 400", rec.Code)
	}
	// 结构合法但未监听端口 → 502，错误消息只带 host（不含完整 URL）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := srv.URL
	srv.Close()
	rec := doProbe(h, fmt.Sprintf(`{"url":%q}`, dead))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("dead port: status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	msg := decodeErr(t, rec)
	if !strings.Contains(msg, strings.TrimPrefix(dead, "http://")) {
		t.Errorf("502 message should mention host, got %q", msg)
	}
	if strings.Contains(msg, "http://") {
		t.Errorf("502 message leaks full url: %q", msg)
	}
}

// TestProbeRuleSetAllComments 全注释规则集：200 + behavior 空 + preview 非空。
func TestProbeRuleSetAllComments(t *testing.T) {
	h := newTestServer(t)
	src := fakeSource(t, http.StatusOK, "# comment one\n# comment two\n\n# last\n")
	resp := decodeProbe(t, doProbe(h, fmt.Sprintf(`{"url":%q}`, src.URL)))
	if resp.Behavior != "" {
		t.Errorf("behavior = %q, want empty", resp.Behavior)
	}
	if resp.Format != "" {
		t.Errorf("format = %q, want empty", resp.Format)
	}
	if len(resp.Preview) == 0 {
		t.Error("preview empty, want non-empty")
	}
	if !strings.Contains(resp.Reason, "未识别到规则行") {
		t.Errorf("reason = %q, want 未识别到规则行", resp.Reason)
	}
}

// TestProbeRuleSetTruncated 超 512KB 规则集：200 + truncated=true，截断后
// 头部仍可判定 domain。
func TestProbeRuleSetTruncated(t *testing.T) {
	h := newTestServer(t)
	line := "DOMAIN-SUFFIX," + strings.Repeat("a", 80) + ".example.com\n" // ~110 字节
	big := strings.Repeat(line, 6000)                                     // ~660KB > 512KB
	src := fakeSource(t, http.StatusOK, big)
	resp := decodeProbe(t, doProbe(h, fmt.Sprintf(`{"url":%q}`, src.URL)))
	if !resp.Truncated {
		t.Error("truncated = false, want true")
	}
	if resp.Behavior != "domain" {
		t.Errorf("behavior = %q, want domain（截断后头部仍可判定）", resp.Behavior)
	}
}

// TestProbeRuleSetAuth 带令牌实例无 token → 401。
func TestProbeRuleSetAuth(t *testing.T) {
	h := newTestServerWithToken(t, "s3cret")
	rec := doProbe(h, `{"url":"http://127.0.0.1:1/x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestProbeRuleSetNoStore st==nil 时不注册 /api/v1 → 404。
func TestProbeRuleSetNoStore(t *testing.T) {
	h := newTestServerNoStore(t)
	rec := doProbe(h, `{"url":"http://127.0.0.1:1/x"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
