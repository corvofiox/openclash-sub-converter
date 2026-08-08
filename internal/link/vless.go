package link

import (
	"errors"
	"fmt"
	"net/url"
)

// parseVless 解析 vless:// 链接（含 Reality）。
//
// vless://uuid@host:port?encryption=none&security=reality|tls|none&sni=&fp=&pbk=
// &sid=&spx=&flow=&type=tcp|ws|grpc&host=&path=&alpn=&allowInsecure=1#name
//
// 字段映射: security(reality|tls)→tls:true; type→network; host→ws-opts.headers.Host
// (ws) / grpc-opts.grpc-service-name(grpc); path→ws-opts.path; sni→servername;
// fp→client-fingerprint; pbk/sid/spx→reality-opts.public-key/short-id/spider-x;
// flow→flow; alpn→alpn; allowInsecure=1→skip-cert-verify:true
func parseVless(link string) (map[string]any, error) {
	userinfo, rest, err := splitUserinfo(link)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}
	uuid := decodeUserinfo(userinfo)
	if uuid == "" {
		return nil, errors.New("vless: missing uuid")
	}

	u, err := url.Parse("//" + rest)
	if err != nil {
		return nil, fmt.Errorf("vless: invalid url: %w", err)
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}
	q := u.Query()

	entry := baseEntry(nodeName(u.Fragment, host, port), "vless", host, port)
	entry["uuid"] = uuid
	entry["udp"] = true

	network := firstParam(q, "type", "net")
	if network == "" {
		network = "tcp"
	}
	entry["network"] = network

	security := q.Get("security")
	tlsEnabled := security == "reality" || security == "tls"
	if tlsEnabled {
		entry["tls"] = true
	}
	// encryption 参数仅作兼容，忽略（vless 加密由 tls/reality 决定）

	if truthy(q.Get("allowInsecure")) {
		entry["skip-cert-verify"] = true
	}
	if v := q.Get("flow"); v != "" {
		entry["flow"] = v
	}
	if tlsEnabled {
		if v := q.Get("sni"); v != "" {
			entry["servername"] = v
		}
		if v := q.Get("fp"); v != "" {
			entry["client-fingerprint"] = v
		}
		if v := q.Get("alpn"); v != "" {
			entry["alpn"] = splitList(v)
		}
	}
	if security == "reality" {
		ropts := map[string]any{}
		if v := q.Get("pbk"); v != "" {
			ropts["public-key"] = v
		}
		if v := q.Get("sid"); v != "" {
			ropts["short-id"] = v
		}
		if v := q.Get("spx"); v != "" {
			ropts["spider-x"] = v
		}
		if len(ropts) > 0 {
			entry["reality-opts"] = ropts
		}
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
