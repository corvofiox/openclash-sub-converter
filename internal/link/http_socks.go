package link

import (
	"fmt"
	"net/url"
)

// parseSocks 解析 socks5:// 链接（socks:// 前缀也接受，统一 type 为 socks5）。
//
// socks5://[user:pass@]host:port#name
//
// 条目: {name, type:socks5, server, port, username?, password?, udp}
func parseSocks(link string) (map[string]any, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("socks5: invalid url: %w", err)
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("socks5: %w", err)
	}
	entry := baseEntry(nodeName(u.Fragment, host, port), "socks5", host, port)
	entry["udp"] = true
	if u.User != nil {
		if username := u.User.Username(); username != "" {
			entry["username"] = username
		}
		if password, ok := u.User.Password(); ok {
			entry["password"] = password
		}
	}
	return entry, nil
}

// parseHTTP 解析 http:// / https:// 链接。
//
// [user:pass@]host:port#name
//
// 条目: {name, type:http, server, port, username?, password?[, tls]}
// https:// 额外输出 tls: true（mihomo http 出站支持）。
func parseHTTP(link string) (map[string]any, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("http: invalid url: %w", err)
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	entry := baseEntry(nodeName(u.Fragment, host, port), "http", host, port)
	if u.Scheme == "https" {
		entry["tls"] = true
	}
	if u.User != nil {
		if username := u.User.Username(); username != "" {
			entry["username"] = username
		}
		if password, ok := u.User.Password(); ok {
			entry["password"] = password
		}
	}
	return entry, nil
}
