package link

import (
	"errors"
	"fmt"
	"net/url"
)

// parseTUIC 解析 tuic:// 链接。
//
// tuic://uuid@host:port?password=&sni=&congestion_control=bbr&udp_relay_mode=native&allow_insecure=1&alpn=#name
//
// 字段映射: password→password; sni→sni; congestion_control→congestion-controller
// (默认 bbr); udp_relay_mode→udp-relay-mode(默认 native);
// allow_insecure=1→skip-cert-verify:true; alpn→alpn
func parseTUIC(link string) (map[string]any, error) {
	userinfo, rest, err := splitUserinfo(link)
	if err != nil {
		return nil, fmt.Errorf("tuic: %w", err)
	}
	uuid := decodeUserinfo(userinfo)
	if uuid == "" {
		return nil, errors.New("tuic: missing uuid")
	}

	u, err := url.Parse("//" + rest)
	if err != nil {
		return nil, fmt.Errorf("tuic: invalid url: %w", err)
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("tuic: %w", err)
	}
	q := u.Query()

	entry := baseEntry(nodeName(u.Fragment, host, port), "tuic", host, port)
	entry["uuid"] = uuid

	if v := q.Get("password"); v != "" {
		entry["password"] = v
	}
	if v := q.Get("sni"); v != "" {
		entry["sni"] = v
	}
	cc := firstParam(q, "congestion_control", "congestion-control")
	if cc == "" {
		cc = "bbr"
	}
	entry["congestion-controller"] = cc
	urm := firstParam(q, "udp_relay_mode", "udp-relay-mode")
	if urm == "" {
		urm = "native"
	}
	entry["udp-relay-mode"] = urm
	if truthy(q.Get("allow_insecure")) || truthy(q.Get("allowInsecure")) {
		entry["skip-cert-verify"] = true
	}
	if v := q.Get("alpn"); v != "" {
		entry["alpn"] = splitList(v)
	}
	return entry, nil
}
