package link

import (
	"errors"
	"fmt"
	"net/url"
)

// parseAnyTLS 解析 anytls:// 链接。
//
// anytls://pass@host:port?sni=&allowInsecure=1&alpn=#name
//
// 字段映射: sni→sni; allowInsecure=1→skip-cert-verify:true; alpn→alpn
func parseAnyTLS(link string) (map[string]any, error) {
	userinfo, rest, err := splitUserinfo(link)
	if err != nil {
		return nil, fmt.Errorf("anytls: %w", err)
	}
	password := decodeUserinfo(userinfo)
	if password == "" {
		return nil, errors.New("anytls: missing password")
	}

	u, err := url.Parse("//" + rest)
	if err != nil {
		return nil, fmt.Errorf("anytls: invalid url: %w", err)
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("anytls: %w", err)
	}
	q := u.Query()

	entry := baseEntry(nodeName(u.Fragment, host, port), "anytls", host, port)
	entry["password"] = password
	entry["udp"] = true // 与 vless/ss 一致：显式声明 udp 支持（mihomo 默认 true，显式消除版本差异）

	if v := q.Get("sni"); v != "" {
		entry["sni"] = v
	}
	if truthy(q.Get("allowInsecure")) || truthy(q.Get("allow_insecure")) {
		entry["skip-cert-verify"] = true
	}
	if v := q.Get("alpn"); v != "" {
		entry["alpn"] = splitList(v)
	}
	return entry, nil
}
