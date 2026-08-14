// 规则集自动探测：拉取规则集头部字节，纯函数分析 format/behavior，
// 供管理台前端在新建/编辑规则集时一键回填下拉框。探测结果不持久化。
package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// probeMaxBytes 探测仅需规则集头部样本；512KB 足够判定且防响应膨胀。
const probeMaxBytes = 512 << 10

// probeReq / probeResp 是 POST /api/v1/rule-sets/probe 的请求/响应结构。
type probeReq struct {
	URL string `json:"url"`
}

type probeResp struct {
	URL       string   `json:"url"`
	Format    string   `json:"format"`
	Behavior  string   `json:"behavior"`
	Reason    string   `json:"reason"`
	Truncated bool     `json:"truncated"`
	Preview   []string `json:"preview"`
}

// handleProbeRuleSet 自动探测规则集 URL 的 format/behavior：
// FetchHead 拉取头部（不写缓存）→ analyzeRuleHead 纯函数判定 → 200 返回
// 探测结果与预览。
//
// 错误映射：URL 为空/结构非法 → 400；拉取失败 → 502（错误消息沿用现有
// 脱敏模式，只带 host，不含完整 URL 与凭证）。
func (s *server) handleProbeRuleSet(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSONError(w, http.StatusNotFound, "admin API not enabled")
		return
	}
	var req probeReq
	if err := decodeJSONBody(w, r, &req); err != nil {
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
	head, truncated, err := s.f.FetchHead(r.Context(), req.URL, probeMaxBytes)
	if err != nil {
		host := hostOf(req.URL)
		s.logger.Warn("probe rule set fetch failed", "host", host, "error", sanitizeErr(err, req.URL, host))
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("拉取规则集失败: %s", host))
		return
	}
	format, behavior, reason := analyzeRuleHead(head)
	writeJSON(w, http.StatusOK, probeResp{
		URL:       redactURL(req.URL),
		Format:    format,
		Behavior:  behavior,
		Reason:    reason,
		Truncated: truncated,
		Preview:   previewLines(head),
	})
}

// utf8BOM 是 UTF-8 字节序标记，分析前剥离。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// domainTokens / ipcidrTokens 是 ruleToken 分类词表（token 已大写）。
var domainTokens = map[string]bool{
	"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true,
	"DOMAIN-WILDCARD": true, "DOMAIN-REGEX": true,
}

var ipcidrTokens = map[string]bool{
	"IP-CIDR": true, "IP-CIDR6": true,
}

// analyzeRuleHead 纯函数分析规则集头部字节，返回 format / behavior 与
// 人类可读的判定理由（不依赖网络与状态，便于单测）。
//
// format：首个有效行（trim 后非空且非 "#" 开头）为 "payload:"（允许尾随
// 注释，如 "payload: # generated"）→ yaml，否则 text。behavior：统计窗口
// 只保留规则行（跳过 "payload:"、YAML 文档分隔符 "---" 等结构行，防止
// 稀释占比），扫描前 50 个规则行，domain/ipcidr 任一占比 ≥60% 且条数 ≥5
// 即判定，否则 classical；无有效行或规则行不足 5 条时 behavior 为空串
// （reason 说明原因）。
func analyzeRuleHead(head []byte) (format, behavior, reason string) {
	head = bytes.TrimPrefix(head, utf8BOM)
	valid := make([]string, 0, 64)
	for _, ln := range strings.Split(string(head), "\n") {
		if trimmed := strings.TrimSpace(ln); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			valid = append(valid, trimmed)
		}
	}
	if len(valid) == 0 {
		return "", "", "未识别到规则行"
	}
	if isPayloadLine(valid[0]) {
		format = "yaml"
	} else {
		format = "text"
	}
	// 统计窗口剔除结构行：payload: 键与 --- 文档分隔符不是规则行，计入会
	// 稀释 domain/ipcidr 占比。不处理其他 YAML 键值行（避免误伤，见交付说明）。
	rules := make([]string, 0, len(valid))
	for _, ln := range valid {
		if strings.HasPrefix(ln, "payload:") || strings.HasPrefix(ln, "---") {
			continue
		}
		rules = append(rules, ln)
	}
	// 仅扫描前 50 个规则行：探测只关心头部样本，防超大文件拖慢
	n := min(len(rules), 50)
	if n < 5 {
		return format, "", fmt.Sprintf("规则行仅 %d 条，样本不足", len(rules))
	}
	var d, i int
	for _, ln := range rules[:n] {
		tok := ruleToken(ln)
		switch {
		case domainTokens[tok]:
			d++
		case ipcidrTokens[tok] || isIPLine(tok):
			i++
		}
	}
	switch {
	case d >= 5 && d*100 >= 60*n:
		return format, "domain", fmt.Sprintf("%d/%d 行规则为 DOMAIN 系列", d, n)
	case i >= 5 && i*100 >= 60*n:
		return format, "ipcidr", fmt.Sprintf("%d/%d 行规则为 IP-CIDR 系列", i, n)
	default:
		return format, "classical", fmt.Sprintf("规则类型混合（DOMAIN %d / IP-CIDR %d / 其他 %d）", d, i, n-d-i)
	}
}

// isPayloadLine 判定一行是否为 YAML 规则列表键 "payload:"：trim 后以
// "payload:" 开头，且去掉该前缀后的剩余部分 trim 后为空或以 "#" 开头
// （容忍真实生成器输出的 "payload: # generated" 之类尾随注释）。
func isPayloadLine(ln string) bool {
	if !strings.HasPrefix(ln, "payload:") {
		return false
	}
	rest := strings.TrimSpace(ln[len("payload:"):])
	return rest == "" || strings.HasPrefix(rest, "#")
}

// ruleToken 提取单行规则的分类 token：剥离 YAML 列表前缀 "- "（输入行已
// trim），取逗号前的字段并大写（"- DOMAIN-SUFFIX,example.com" →
// "DOMAIN-SUFFIX"）。
func ruleToken(ln string) string {
	ln = strings.TrimPrefix(ln, "- ")
	if idx := strings.IndexByte(ln, ','); idx >= 0 {
		ln = ln[:idx]
	}
	return strings.ToUpper(ln)
}

// isIPLine 判定 token 是否为纯 IP/CIDR 行（无规则关键字，如 "1.2.3.4/24"、
// "2001:db8::1"），覆盖 IPv4/IPv6 前缀与单地址。
func isIPLine(tok string) bool {
	if _, err := netip.ParsePrefix(tok); err == nil {
		return true
	}
	_, err := netip.ParseAddr(tok)
	return err == nil
}

// previewLines 取原始内容前 10 行作为预览，每行截断 200 runes（防响应膨胀）；
// 内容以 "\n" 结尾（如 "a\nb\n"）时 Split 产生的尾随空串先剔除，预览末尾
// 不出现空行。
func previewLines(head []byte) []string {
	head = bytes.TrimPrefix(head, utf8BOM)
	lines := strings.Split(string(head), "\n")
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 10 {
		lines = lines[:10]
	}
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		r := []rune(ln)
		if len(r) > 200 {
			r = r[:200]
		}
		out = append(out, string(r))
	}
	return out
}
