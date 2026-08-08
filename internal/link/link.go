// Package link 实现代理协议链接解析：将订阅内容（Base64 订阅 / Clash YAML 订阅 /
// 单条链接）解析为 Clash YAML proxies 条目（[]map[string]any）。
//
// 字段映射严格遵循 docs/design.md 第 4 节。
package link

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// 已知协议前缀（顺序敏感：hysteria2 必须先于 hysteria 判断）。
var schemePrefixes = []string{
	"ss://", "ssr://", "vmess://", "vless://", "trojan://",
	"hysteria2://", "hy2://", "hysteria://", "tuic://", "anytls://",
	"socks5://", "socks://", "http://", "https://",
}

// ParseSubscription 解析订阅内容，自动识别三种形态：
//
//	(a) 纯 Base64 订阅（解码后每行一个链接）
//	(b) Clash YAML 订阅（含 proxies: 段，直接产出条目）
//	(c) 单条链接 / 纯文本多行链接（逐行解析）
//
// 单行解析失败时跳过并记录 warn 日志，不中断整体；整体无法识别时返回明确错误。
func ParseSubscription(data []byte, sourceName string) ([]map[string]any, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, errors.New("empty subscription")
	}

	// (b) Clash YAML 订阅
	if isYAMLSubscription(text) {
		return parseYAMLSubscription(text)
	}

	// (c) 单条链接 / 纯文本行
	if firstMeaningfulLineHasScheme(text) {
		return parseLines(text, sourceName), nil
	}

	// (a) Base64 订阅（解码后可能是链接行或 YAML）
	if decoded, ok := tryDecodeBase64Blob(text); ok {
		if isYAMLSubscription(decoded) {
			return parseYAMLSubscription(decoded)
		}
		if firstMeaningfulLineHasScheme(decoded) {
			return parseLines(decoded, sourceName), nil
		}
	}

	return nil, errors.New("unable to recognize subscription format: not base64, not clash yaml, not proxy links")
}

// parseYAMLSubscription 解析 Clash YAML 的 proxies: 段，条目原样透传。
func parseYAMLSubscription(text string) ([]map[string]any, error) {
	var root map[string]any
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return nil, fmt.Errorf("invalid clash yaml subscription: %w", err)
	}
	raw, ok := root["proxies"]
	if !ok || raw == nil {
		return nil, errors.New("clash yaml subscription missing 'proxies' section")
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("clash yaml subscription 'proxies' is not a list")
	}
	out := make([]map[string]any, 0, len(list))
	for i, item := range list {
		m, ok := toMap(item)
		if !ok {
			return nil, fmt.Errorf("clash yaml subscription proxy #%d is not a mapping", i+1)
		}
		out = append(out, m)
	}
	return out, nil
}

// toMap 将 yaml 解码出的任意 map 形态归一为 map[string]any。
func toMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	}
	return nil, false
}

// parseLines 逐行解析链接：跳过空行与 # 注释行，单行失败 warn + 跳过。
func parseLines(text, sourceName string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		node, err := parseLink(line)
		if err != nil {
			slog.Warn("skip invalid proxy link", "source", sourceName, "error", err.Error())
			continue
		}
		out = append(out, node)
	}
	return out
}

// parseLink 按前缀分发到各协议解析函数。
func parseLink(line string) (map[string]any, error) {
	for _, prefix := range schemePrefixes {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		switch prefix {
		case "ss://":
			return parseSS(line)
		case "ssr://":
			return parseSSR(line)
		case "vmess://":
			return parseVmess(line)
		case "vless://":
			return parseVless(line)
		case "trojan://":
			return parseTrojan(line)
		case "hysteria2://", "hy2://":
			return parseHysteria2(line)
		case "hysteria://":
			return parseHysteria(line)
		case "tuic://":
			return parseTUIC(line)
		case "anytls://":
			return parseAnyTLS(line)
		case "socks5://", "socks://":
			return parseSocks(line)
		case "http://", "https://":
			return parseHTTP(line)
		}
	}
	return nil, fmt.Errorf("unsupported or unrecognized proxy link")
}

// isYAMLSubscription 粗判是否为 Clash YAML（含 proxies: 段）。
func isYAMLSubscription(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "proxies:") {
			return true
		}
	}
	return false
}

// firstMeaningfulLineHasScheme 判断首个有效行（非空、非 # 注释）是否以已知协议前缀开头。
func firstMeaningfulLineHasScheme(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return hasKnownScheme(line)
	}
	return false
}

func hasKnownScheme(line string) bool {
	for _, prefix := range schemePrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// tryDecodeBase64Blob 尝试把整段文本当作 Base64（去空白、兼容标准/URL-safe、有无 padding）解码。
func tryDecodeBase64Blob(text string) (string, bool) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, text)
	if compact == "" {
		return "", false
	}
	b, err := decodeBase64(compact)
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

// decodeBase64 依次尝试 URL-safe / 标准 Base64，raw / 带 padding 四种组合。
func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty base64 data")
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		b, err := enc.DecodeString(s)
		if err == nil {
			return b, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("invalid base64 data: %w", lastErr)
}

// nodeName 生成节点名：优先 fragment（URL 解码，保留中文/emoji），否则 host:port。
func nodeName(fragment, host string, port int) string {
	if fragment != "" {
		if n, err := url.PathUnescape(fragment); err == nil && n != "" {
			return n
		}
		return fragment
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// splitHostPort 解析 host:port（兼容 IPv6 括号形式与无括号形式），端口必须为 1-65535。
func splitHostPort(hp string) (string, int, error) {
	hp = strings.TrimSpace(hp)
	if hp == "" {
		return "", 0, errors.New("empty host:port")
	}
	host, portStr, err := net.SplitHostPort(hp)
	if err != nil {
		// 兜底：无括号 IPv6 等场景，交给 url 解析
		u, uerr := url.Parse("//" + hp)
		if uerr != nil || u.Hostname() == "" {
			return "", 0, fmt.Errorf("invalid host:port %q", hp)
		}
		host, portStr = u.Hostname(), u.Port()
	}
	if host == "" {
		return "", 0, fmt.Errorf("missing host in %q", hp)
	}
	if portStr == "" {
		return "", 0, fmt.Errorf("missing port in %q", hp)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port %q", portStr)
	}
	return host, port, nil
}

// splitUserinfo 手动切出 userinfo（避免 url.User 对含 ':' 的密码截断）。
// 返回 userinfo（未解码）与去掉 userinfo 后的剩余部分。
func splitUserinfo(link string) (userinfo, rest string, err error) {
	i := strings.Index(link, "://")
	if i < 0 {
		return "", "", errors.New("missing scheme")
	}
	rest = link[i+3:]
	at := strings.Index(rest, "@")
	if at < 0 {
		return "", "", errors.New("missing userinfo")
	}
	return rest[:at], rest[at+1:], nil
}

// decodeUserinfo 对 userinfo 做百分号解码（失败则原样返回）。
func decodeUserinfo(s string) string {
	if d, err := url.PathUnescape(s); err == nil {
		return d
	}
	return s
}

// firstParam 返回 keys 中第一个非空参数值。
func firstParam(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// truthy 解析 1/true/yes/on 等布尔型参数。
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// splitList 按逗号拆分字符串列表（去空白、去空项）。
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// decodeParamOrRaw 解码 SSR 等参数值：先尝试 Base64，再尝试百分号解码，最后原样返回。
func decodeParamOrRaw(v string) string {
	if b, err := decodeBase64(v); err == nil && len(b) > 0 {
		return string(b)
	}
	if u, err := url.QueryUnescape(v); err == nil {
		return u
	}
	return v
}

// toPort 将 vmess JSON 中可能是字符串或数字的 port 转为 int。
func toPort(v any) (int, error) {
	switch t := v.(type) {
	case float64:
		p := int(t)
		if p < 1 || p > 65535 {
			return 0, fmt.Errorf("invalid port %v", v)
		}
		return p, nil
	case int:
		if t < 1 || t > 65535 {
			return 0, fmt.Errorf("invalid port %v", v)
		}
		return t, nil
	case string:
		p, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil || p < 1 || p > 65535 {
			return 0, fmt.Errorf("invalid port %q", t)
		}
		return p, nil
	}
	return 0, fmt.Errorf("invalid port type %T", v)
}

// toInt 将 vmess JSON 中可能是字符串或数字的 aid 等字段转为 int。
func toInt(v any) (int, error) {
	switch t := v.(type) {
	case float64:
		return int(t), nil
	case int:
		return t, nil
	case string:
		return strconv.Atoi(strings.TrimSpace(t))
	}
	return 0, fmt.Errorf("invalid int type %T", v)
}

// baseEntry 构造公共字段。
func baseEntry(name, typ, server string, port int) map[string]any {
	return map[string]any{
		"name":   name,
		"type":   typ,
		"server": server,
		"port":   port,
	}
}
