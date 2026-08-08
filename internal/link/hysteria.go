package link

import (
	"fmt"
	"net/url"
)

// parseHysteria2 解析 hysteria2://（hy2://）链接。
//
// hysteria2://pass@host:port?sni=&insecure=1&obfs=&obfs-password=&alpn=&up=&down=#name
//
// 字段映射: sni→sni; insecure=1→skip-cert-verify:true; obfs→obfs;
// obfs-password→obfs-password; alpn→alpn; up/down→up/down（原样字符串，如 "100mbps"）
func parseHysteria2(link string) (map[string]any, error) {
	userinfo, rest, err := splitUserinfo(link)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: %w", err)
	}
	password := decodeUserinfo(userinfo)

	u, err := url.Parse("//" + rest)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: invalid url: %w", err)
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: %w", err)
	}
	q := u.Query()

	entry := baseEntry(nodeName(u.Fragment, host, port), "hysteria2", host, port)
	entry["password"] = password

	if v := q.Get("sni"); v != "" {
		entry["sni"] = v
	}
	if truthy(q.Get("insecure")) || truthy(q.Get("allowInsecure")) {
		entry["skip-cert-verify"] = true
	}
	if v := q.Get("obfs"); v != "" {
		entry["obfs"] = v
	}
	if v := q.Get("obfs-password"); v != "" {
		entry["obfs-password"] = v
	}
	if v := q.Get("alpn"); v != "" {
		entry["alpn"] = splitList(v)
	}
	if v := q.Get("up"); v != "" {
		entry["up"] = v
	}
	if v := q.Get("down"); v != "" {
		entry["down"] = v
	}
	return entry, nil
}

// parseHysteria 解析 hysteria://（v1）链接。
//
// hysteria://host:port?auth=&up=&down=&insecure=1&sni=&obfs=#name
//
// 字段映射: auth→auth-str; up/down→up/down; insecure=1→skip-cert-verify:true;
// sni→sni; obfs→obfs
func parseHysteria(link string) (map[string]any, error) {
	rest := link[len("hysteria://"):]
	u, err := url.Parse("//" + rest)
	if err != nil {
		return nil, fmt.Errorf("hysteria: invalid url: %w", err)
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("hysteria: %w", err)
	}
	q := u.Query()

	entry := baseEntry(nodeName(u.Fragment, host, port), "hysteria", host, port)

	if v := q.Get("auth"); v != "" {
		entry["auth-str"] = v
	}
	if v := q.Get("up"); v != "" {
		entry["up"] = v
	}
	if v := q.Get("down"); v != "" {
		entry["down"] = v
	}
	if truthy(q.Get("insecure")) || truthy(q.Get("allowInsecure")) {
		entry["skip-cert-verify"] = true
	}
	if v := q.Get("sni"); v != "" {
		entry["sni"] = v
	}
	if v := q.Get("obfs"); v != "" {
		entry["obfs"] = v
	}
	return entry, nil
}
