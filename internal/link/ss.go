package link

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// parseSS 解析 ss:// 链接。
//
// SIP002: ss://base64url(method:password)@host:port#name[?plugin=...]
// 老格式: ss://base64(method:password@host:port)#name
//
// 条目: {name, type:ss, server, port, cipher, password, udp, plugin?, plugin-opts?}
func parseSS(link string) (map[string]any, error) {
	rest := strings.TrimPrefix(link, "ss://")

	// fragment（节点名）
	name := ""
	if i := strings.Index(rest, "#"); i >= 0 {
		name = rest[i+1:]
		rest = rest[:i]
	}
	// query（plugin 参数）
	query := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		query = rest[i+1:]
		rest = rest[:i]
	}

	var method, password, server string
	var port int

	if at := strings.Index(rest, "@"); at >= 0 {
		// SIP002: base64url(method:password)@host:port
		cred, err := decodeBase64(rest[:at])
		if err != nil {
			return nil, fmt.Errorf("ss: invalid userinfo base64: %w", err)
		}
		mp := string(cred)
		colon := strings.Index(mp, ":")
		if colon < 0 {
			return nil, errors.New("ss: missing ':' in method:password")
		}
		method, password = mp[:colon], mp[colon+1:]
		server, port, err = splitHostPort(rest[at+1:])
		if err != nil {
			return nil, fmt.Errorf("ss: %w", err)
		}
	} else {
		// 老格式: base64(method:password@host:port)
		raw, err := decodeBase64(rest)
		if err != nil {
			return nil, fmt.Errorf("ss: invalid legacy base64: %w", err)
		}
		s := string(raw)
		at := strings.LastIndex(s, "@")
		if at < 0 {
			return nil, errors.New("ss: legacy link missing '@'")
		}
		mp := s[:at]
		colon := strings.Index(mp, ":")
		if colon < 0 {
			return nil, errors.New("ss: missing ':' in method:password")
		}
		method, password = mp[:colon], mp[colon+1:]
		server, port, err = splitHostPort(s[at+1:])
		if err != nil {
			return nil, fmt.Errorf("ss: %w", err)
		}
	}

	if method == "" || password == "" {
		return nil, errors.New("ss: empty method or password")
	}

	entry := baseEntry(nodeName(name, server, port), "ss", server, port)
	entry["cipher"] = method
	entry["password"] = password
	entry["udp"] = true

	if query != "" {
		applySSPlugin(entry, query)
	}
	return entry, nil
}

// applySSPlugin 解析 ?plugin= 参数（obfs-local / simple-obfs / v2ray-plugin 等），
// 写入 plugin 与 plugin-opts，字段对齐 mihomo 的 shadowsocks 插件结构。
// 注意：mihomo 只识别 plugin == "obfs" / "v2ray-plugin" / "gost-plugin"
// （adapter/outbound/shadowsocks.go），obfs-local / simple-obfs 归一为 "obfs"。
func applySSPlugin(entry map[string]any, query string) {
	q, err := url.QueryUnescape(query)
	if err != nil {
		q = query
	}
	q = strings.TrimPrefix(q, "plugin=")
	q = strings.TrimPrefix(q, "plugin%3D")
	parts := strings.Split(q, ";")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return
	}
	plugin := strings.TrimSpace(parts[0])

	params := map[string]string{}
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		if v == "" {
			v = "1"
		}
		params[strings.TrimSpace(k)] = v
	}

	opts := map[string]any{}
	switch plugin {
	case "obfs-local", "simple-obfs":
		// 归一为 mihomo 认识的 "obfs"（否则混淆静默失效）
		plugin = "obfs"
		// 参数名: obfs / obfs-host（SIP002 惯例）
		if v := params["obfs"]; v != "" {
			opts["mode"] = v
		}
		if v := params["obfs-host"]; v != "" {
			opts["host"] = v
		}
	case "v2ray-plugin":
		if v := params["mode"]; v != "" {
			opts["mode"] = v
		}
		if v := params["host"]; v != "" {
			opts["host"] = v
		}
		if v := params["path"]; v != "" {
			opts["path"] = v
		}
		if truthy(params["tls"]) {
			opts["tls"] = true
		}
		if truthy(params["mux"]) {
			opts["mux"] = true
		}
	default:
		// 未知插件：键值对原样透传
		for k, v := range params {
			opts[k] = v
		}
	}
	// 归一化后的 plugin 名写回条目（obfs-local/simple-obfs → obfs）
	entry["plugin"] = plugin
	if len(opts) > 0 {
		entry["plugin-opts"] = opts
	}
}
