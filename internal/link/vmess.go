package link

import (
	"encoding/json"
	"errors"
	"fmt"
)

// vmessJSON 对应 vmess:// 内嵌 JSON 的字段。
type vmessJSON struct {
	Add  string `json:"add"`
	Port any    `json:"port"`
	ID   string `json:"id"`
	PS   string `json:"ps"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  string `json:"tls"`
	SNI  string `json:"sni"`
	ALPN string `json:"alpn"`
	FP   string `json:"fp"`
	AID  any    `json:"aid"`
	SCY  string `json:"scy"`
}

// parseVmess 解析 vmess:// 链接（base64(JSON)）。
//
// 字段映射: add→server, port(字符串或数字→int), id→uuid, ps→name, net→network,
// type→header(仅 tcp/http), host→ws/http host, path→ws/http path, tls→tls,
// sni→servername, alpn→alpn, fp→client-fingerprint, aid→alterId, scy→cipher
//
// 条目: {name, type:vmess, server, port, uuid, alterId, cipher, udp, tls,
// network, ws-opts{path, headers{host}} / http-opts, servername, client-fingerprint}
func parseVmess(link string) (map[string]any, error) {
	rest := link[len("vmess://"):]
	raw, err := decodeBase64(rest)
	if err != nil {
		return nil, fmt.Errorf("vmess: invalid base64: %w", err)
	}
	var v vmessJSON
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("vmess: invalid json: %w", err)
	}
	if v.Add == "" {
		return nil, errors.New("vmess: missing server (add)")
	}
	if v.ID == "" {
		return nil, errors.New("vmess: missing uuid (id)")
	}
	port, err := toPort(v.Port)
	if err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}

	name := nodeName(v.PS, v.Add, port)
	entry := baseEntry(name, "vmess", v.Add, port)
	entry["uuid"] = v.ID

	aid := 0
	if v.AID != nil {
		if n, err := toInt(v.AID); err == nil {
			aid = n
		}
	}
	entry["alterId"] = aid

	cipher := v.SCY
	if cipher == "" {
		cipher = "auto"
	}
	entry["cipher"] = cipher
	entry["udp"] = true

	network := v.Net
	if network == "" {
		network = "tcp"
	}
	entry["network"] = network

	tlsEnabled := v.TLS == "tls" || v.TLS == "1" || v.TLS == "true"
	if tlsEnabled {
		entry["tls"] = true
		if v.SNI != "" {
			entry["servername"] = v.SNI
		}
		if v.ALPN != "" {
			entry["alpn"] = splitList(v.ALPN)
		}
	}
	if v.FP != "" {
		entry["client-fingerprint"] = v.FP
	}

	switch network {
	case "ws":
		opts := map[string]any{}
		if v.Path != "" {
			opts["path"] = v.Path
		}
		if v.Host != "" {
			opts["headers"] = map[string]string{"Host": v.Host}
		}
		if len(opts) > 0 {
			entry["ws-opts"] = opts
		}
	case "http":
		opts := map[string]any{}
		if v.Path != "" {
			opts["path"] = []string{v.Path}
		}
		if v.Host != "" {
			opts["headers"] = map[string]string{"Host": v.Host}
		}
		if len(opts) > 0 {
			entry["http-opts"] = opts
		}
	case "h2":
		opts := map[string]any{}
		if v.Host != "" {
			opts["host"] = []string{v.Host}
		}
		if v.Path != "" {
			opts["path"] = v.Path
		}
		if len(opts) > 0 {
			entry["h2-opts"] = opts
		}
	}

	// header type 仅 tcp/http 网络有意义（如 none/http）
	if v.Type != "" && v.Type != "none" && (network == "tcp" || network == "http") {
		entry["header"] = v.Type
	}

	return entry, nil
}
