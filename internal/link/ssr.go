package link

import (
	"fmt"
	"strconv"
	"strings"
)

// parseSSR 解析 ssr:// 链接。
//
// ssr://base64url(host:port:protocol:method:obfs:base64url(pass)/?params)
// params: remarks(节点名, base64url) / group / obfsparam / protoparam
//
// 条目: {name, type:ssr, server, port, cipher, password, protocol, obfs,
// protocol-param, obfs-param, udp}
func parseSSR(link string) (map[string]any, error) {
	rest := strings.TrimPrefix(link, "ssr://")

	// 切出明文 /?params（obfsparam/protoparam 里可能含 ':'，必须先切；base64 字符集不含 '?'，安全）
	params := ""
	if i := strings.Index(rest, "/?"); i >= 0 {
		params = rest[i+2:]
		rest = rest[:i]
	}

	raw, err := decodeBase64(rest)
	if err != nil {
		return nil, fmt.Errorf("ssr: invalid base64: %w", err)
	}
	s := string(raw)

	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return nil, fmt.Errorf("ssr: expected 6 colon-separated fields (host:port:protocol:method:obfs:pass), got %d", len(parts))
	}
	host, portStr, protocol, method, obfs, passB64 := parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("ssr: invalid port %q", portStr)
	}
	if host == "" {
		return nil, fmt.Errorf("ssr: empty server host")
	}

	passBytes, err := decodeBase64(passB64)
	if err != nil {
		return nil, fmt.Errorf("ssr: invalid password base64: %w", err)
	}

	entry := baseEntry(nodeName("", host, port), "ssr", host, port)
	entry["cipher"] = method
	entry["password"] = string(passBytes)
	entry["protocol"] = protocol
	entry["obfs"] = obfs
	// SSR 机场普遍支持 UDP（mihomo ShadowSocksROption.UDP tag proxy:"udp,omitempty"）
	entry["udp"] = true

	if params != "" {
		for _, pair := range strings.Split(params, "&") {
			k, v, _ := strings.Cut(pair, "=")
			if k == "" {
				continue
			}
			switch k {
			case "remarks":
				entry["name"] = nodeName(decodeParamOrRaw(v), host, port)
			case "obfsparam":
				entry["obfs-param"] = decodeParamOrRaw(v)
			case "protoparam":
				entry["protocol-param"] = decodeParamOrRaw(v)
			}
			// group 参数仅作分组标识，条目中不输出（mihomo ssr 无此字段）
		}
	}
	return entry, nil
}
