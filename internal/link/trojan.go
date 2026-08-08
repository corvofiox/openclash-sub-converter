package link

import (
	"errors"
	"fmt"
	"net/url"
)

// parseTrojan 解析 trojan:// 链接。
//
// trojan://pass@host:port?security=tls&sni=&type=ws|grpc|tcp&host=&path=&allowInsecure=1#name
//
// 字段映射: sni→sni; type→network; host/path→ws-opts(ws) /
// grpc-opts.grpc-service-name(grpc); alpn→alpn; allowInsecure=1→skip-cert-verify:true
//
// 条目: {name, type:trojan, server, port, password, sni, network,
// ws-opts/grpc-opts, skip-cert-verify}
func parseTrojan(link string) (map[string]any, error) {
	userinfo, rest, err := splitUserinfo(link)
	if err != nil {
		return nil, fmt.Errorf("trojan: %w", err)
	}
	password := decodeUserinfo(userinfo)
	if password == "" {
		return nil, errors.New("trojan: missing password")
	}

	u, err := url.Parse("//" + rest)
	if err != nil {
		return nil, fmt.Errorf("trojan: invalid url: %w", err)
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("trojan: %w", err)
	}
	q := u.Query()

	entry := baseEntry(nodeName(u.Fragment, host, port), "trojan", host, port)
	entry["password"] = password

	network := firstParam(q, "type", "network")
	if network == "" {
		network = "tcp"
	}
	entry["network"] = network

	if v := q.Get("sni"); v != "" {
		// mihomo TrojanOption 的 tag 是 proxy:"sni,omitempty"（adapter/outbound/trojan.go），
		// 输出 servername 会被静默忽略并回退 server，必须用 sni。
		entry["sni"] = v
	}
	if truthy(q.Get("allowInsecure")) || truthy(q.Get("allow_insecure")) {
		entry["skip-cert-verify"] = true
	}
	if v := q.Get("alpn"); v != "" {
		entry["alpn"] = splitList(v)
	}

	switch network {
	case "ws":
		opts := map[string]any{}
		if v := q.Get("path"); v != "" {
			opts["path"] = v
		}
		if v := q.Get("host"); v != "" {
			opts["headers"] = map[string]string{"Host": v}
		}
		if len(opts) > 0 {
			entry["ws-opts"] = opts
		}
	case "grpc":
		opts := map[string]any{}
		if v := firstParam(q, "host", "serviceName", "service-name"); v != "" {
			opts["grpc-service-name"] = v
		}
		if len(opts) > 0 {
			entry["grpc-opts"] = opts
		}
	}

	return entry, nil
}
