package link

import (
	"encoding/base64"
	"reflect"
	"testing"
)

// ---------- 断言辅助 ----------

func assertStr(t *testing.T, m map[string]any, key, want string) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("entry missing key %q", key)
		return
	}
	if s, ok := got.(string); !ok || s != want {
		t.Errorf("entry[%q] = %#v, want %q", key, got, want)
	}
}

func assertInt(t *testing.T, m map[string]any, key string, want int) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("entry missing key %q", key)
		return
	}
	if i, ok := got.(int); !ok || i != want {
		t.Errorf("entry[%q] = %#v, want %d", key, got, want)
	}
}

func assertBool(t *testing.T, m map[string]any, key string, want bool) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("entry missing key %q", key)
		return
	}
	if b, ok := got.(bool); !ok || b != want {
		t.Errorf("entry[%q] = %#v, want %v", key, got, want)
	}
}

func assertAbsent(t *testing.T, m map[string]any, key string) {
	t.Helper()
	if _, ok := m[key]; ok {
		t.Errorf("entry should NOT contain key %q (got %#v)", key, m[key])
	}
}

func assertMap(t *testing.T, m map[string]any, key string, want map[string]any) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("entry missing key %q", key)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entry[%q] = %#v, want %#v", key, got, want)
	}
}

func assertList(t *testing.T, m map[string]any, key string, want []string) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("entry missing key %q", key)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entry[%q] = %#v, want %#v", key, got, want)
	}
}

// vmessLink 由字面量 JSON 构造 vmess:// 链接（黄金用例的 JSON 与字段映射是断言重点）。
func vmessLink(t *testing.T, jsonStr string) string {
	t.Helper()
	return "vmess://" + base64.StdEncoding.EncodeToString([]byte(jsonStr))
}

const testUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"

// ---------- ss:// ----------

func TestParseSS(t *testing.T) {
	t.Run("sip002", func(t *testing.T) {
		link := "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388#%E6%B5%8B%E8%AF%95%E8%8A%82%E7%82%B9"
		e, err := parseSS(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "测试节点")
		assertStr(t, e, "type", "ss")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 8388)
		assertStr(t, e, "cipher", "aes-256-gcm")
		assertStr(t, e, "password", "password")
		assertBool(t, e, "udp", true)
	})

	t.Run("legacy", func(t *testing.T) {
		link := "ss://YWVzLTI1Ni1nY206cGFzc3dvcmRAZXhhbXBsZS5jb206ODM4OA==#legacy-node"
		e, err := parseSS(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "legacy-node")
		assertStr(t, e, "type", "ss")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 8388)
		assertStr(t, e, "cipher", "aes-256-gcm")
		assertStr(t, e, "password", "password")
	})

	t.Run("default-name-is-host-port", func(t *testing.T) {
		e, err := parseSS("ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "example.com:8388")
	})

	t.Run("obfs-local-plugin", func(t *testing.T) {
		link := "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dexample.com#obfs-node"
		e, err := parseSS(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "plugin", "obfs") // obfs-local 归一为 mihomo 认识的 obfs
		assertMap(t, e, "plugin-opts", map[string]any{"mode": "http", "host": "example.com"})
	})

	t.Run("v2ray-plugin", func(t *testing.T) {
		link := "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388?plugin=v2ray-plugin%3Bmode%3Dwebsocket%3Bhost%3Dcdn.example.com%3Bpath%3D%2Fws%3Btls#v2ray-node"
		e, err := parseSS(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "plugin", "v2ray-plugin")
		assertMap(t, e, "plugin-opts", map[string]any{
			"mode": "websocket", "host": "cdn.example.com", "path": "/ws", "tls": true,
		})
	})

	t.Run("ipv6-host", func(t *testing.T) {
		e, err := parseSS("ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@[2001:db8::1]:8388#v6-node")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "server", "2001:db8::1")
		assertInt(t, e, "port", 8388)
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"ss://not-a-base64!!@example.com:443",                // userinfo 非 base64
			"ss://aGVsbG8=",                                      // 老格式解码后无 @
			"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:99999", // 端口越界
			"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com",       // 缺端口
			"ss://",                             // 空
			"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@", // 缺 host:port
		}
		for _, c := range cases {
			if _, err := parseSS(c); err == nil {
				t.Errorf("parseSS(%q) should error", c)
			}
		}
	})
}

// ---------- ssr:// ----------

func TestParseSSR(t *testing.T) {
	t.Run("golden", func(t *testing.T) {
		link := "ssr://ZXhhbXBsZS5jb206ODQ0MzphdXRoX2FlczEyOF9zaGExOmFlcy0yNTYtY2ZiOnRsczEuMl90aWNrZXRfYXV0aDpjR0Z6YzNkdmNtUQ/?remarks=5rWL6K-V&obfsparam=ZXhhbXBsZS5jb20&protoparam=ZXhhbXBsZS5jb20&group=test"
		e, err := parseSSR(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "测试")
		assertStr(t, e, "type", "ssr")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 8443)
		assertStr(t, e, "cipher", "aes-256-cfb")
		assertStr(t, e, "password", "password")
		assertStr(t, e, "protocol", "auth_aes128_sha1")
		assertStr(t, e, "obfs", "tls1.2_ticket_auth")
		assertStr(t, e, "obfs-param", "example.com")
		assertStr(t, e, "protocol-param", "example.com")
		assertBool(t, e, "udp", true)
	})

	t.Run("no-remarks-default-name", func(t *testing.T) {
		link := "ssr://ZXhhbXBsZS5jb206ODQ0MzphdXRoX2FlczEyOF9zaGExOmFlcy0yNTYtY2ZiOnRsczEuMl90aWNrZXRfYXV0aDpjR0Z6YzNkdmNtUQ"
		e, err := parseSSR(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "example.com:8443")
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"ssr://!!!not-base64", // base64 非法
			"ssr://" + base64.RawURLEncoding.EncodeToString([]byte("a:b:c:d:e")),                                 // 字段数不足
			"ssr://" + base64.RawURLEncoding.EncodeToString([]byte("example.com:abc:auth:aes:obfs:cGFzc3dvcmQ")), // 端口非法
			"ssr://" + base64.RawURLEncoding.EncodeToString([]byte("example.com:8443:auth:aes:obfs:!!!")),        // 密码非 base64
			"ssr://" + base64.RawURLEncoding.EncodeToString([]byte("example.com:8443:auth:aes:obfs")),            // 缺密码字段
		}
		for _, c := range cases {
			if _, err := parseSSR(c); err == nil {
				t.Errorf("parseSSR(%q) should error", c)
			}
		}
	})
}

// ---------- vmess:// ----------

func TestParseVmess(t *testing.T) {
	t.Run("ws-tls-golden", func(t *testing.T) {
		jsonStr := `{"v":"2","ps":"vmess-test","add":"example.com","port":"443","id":"` + testUUID +
			`","aid":"0","net":"ws","type":"none","host":"cdn.example.com","path":"/path","tls":"tls","sni":"example.com","alpn":"h2,http/1.1","fp":"chrome","scy":"auto"}`
		e, err := parseVmess(vmessLink(t, jsonStr))
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "vmess-test")
		assertStr(t, e, "type", "vmess")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 443)
		assertStr(t, e, "uuid", testUUID)
		assertInt(t, e, "alterId", 0)
		assertStr(t, e, "cipher", "auto")
		assertBool(t, e, "udp", true)
		assertBool(t, e, "tls", true)
		assertStr(t, e, "network", "ws")
		assertStr(t, e, "servername", "example.com")
		assertStr(t, e, "client-fingerprint", "chrome")
		assertList(t, e, "alpn", []string{"h2", "http/1.1"})
		assertMap(t, e, "ws-opts", map[string]any{
			"path":    "/path",
			"headers": map[string]string{"Host": "cdn.example.com"},
		})
	})

	t.Run("tcp-num-port-no-tls", func(t *testing.T) {
		jsonStr := `{"v":"2","ps":"vmess-tcp","add":"example.com","port":80,"id":"` + testUUID +
			`","aid":0,"net":"tcp","type":"http","host":"example.com","path":"/","tls":"none","scy":"auto"}`
		e, err := parseVmess(vmessLink(t, jsonStr))
		if err != nil {
			t.Fatal(err)
		}
		assertInt(t, e, "port", 80)
		assertInt(t, e, "alterId", 0)
		assertStr(t, e, "network", "tcp")
		assertStr(t, e, "header", "http")
		assertAbsent(t, e, "tls")
		assertAbsent(t, e, "servername")
		assertAbsent(t, e, "ws-opts")
	})

	t.Run("http-network", func(t *testing.T) {
		jsonStr := `{"v":"2","ps":"vmess-http","add":"example.com","port":"8080","id":"` + testUUID +
			`","aid":"1","net":"http","type":"http","host":"example.com","path":"/x","tls":"tls","scy":"aes-128-gcm"}`
		e, err := parseVmess(vmessLink(t, jsonStr))
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "network", "http")
		assertInt(t, e, "alterId", 1)
		assertStr(t, e, "cipher", "aes-128-gcm")
		assertMap(t, e, "http-opts", map[string]any{
			"path":    []string{"/x"},
			"headers": map[string]string{"Host": "example.com"},
		})
	})

	t.Run("empty-network-defaults-tcp", func(t *testing.T) {
		jsonStr := `{"ps":"vmess-nonet","add":"example.com","port":"443","id":"` + testUUID + `"}`
		e, err := parseVmess(vmessLink(t, jsonStr))
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "network", "tcp")
		assertInt(t, e, "alterId", 0)
		assertStr(t, e, "cipher", "auto")
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"vmess://!!!not-base64",                            // base64 非法
			"vmess://aGVsbG8=",                                 // 解码后非 JSON
			vmessLink(t, `{"add":"example.com","port":"443"}`), // 缺 id
			vmessLink(t, `{"add":"example.com","port":"abc","id":"`+testUUID+`"}`), // 端口非法
			vmessLink(t, `{"port":"443","id":"`+testUUID+`"}`),                     // 缺 add
			vmessLink(t, `{"add":"example.com","port":70000,"id":"`+testUUID+`"}`), // 端口越界
		}
		for _, c := range cases {
			if _, err := parseVmess(c); err == nil {
				t.Errorf("parseVmess(%q) should error", c)
			}
		}
	})
}

// ---------- vless:// ----------

func TestParseVless(t *testing.T) {
	t.Run("reality-ws-golden", func(t *testing.T) {
		link := "vless://" + testUUID + "@example.com:443?encryption=none&security=reality&type=ws&host=cdn.example.com&path=%2Fws&sni=example.com&fp=chrome&pbk=publickey123&sid=abcd&spx=%2F&flow=xtls-rprx-vision#reality-node"
		e, err := parseVless(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "reality-node")
		assertStr(t, e, "type", "vless")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 443)
		assertStr(t, e, "uuid", testUUID)
		assertBool(t, e, "udp", true)
		assertStr(t, e, "network", "ws")
		assertBool(t, e, "tls", true)
		assertStr(t, e, "servername", "example.com")
		assertStr(t, e, "client-fingerprint", "chrome")
		assertStr(t, e, "flow", "xtls-rprx-vision")
		assertMap(t, e, "reality-opts", map[string]any{
			"public-key": "publickey123", "short-id": "abcd", "spider-x": "/",
		})
		assertMap(t, e, "ws-opts", map[string]any{
			"path":    "/ws",
			"headers": map[string]string{"Host": "cdn.example.com"},
		})
	})

	t.Run("plain-tcp-no-tls", func(t *testing.T) {
		link := "vless://" + testUUID + "@example.com:443?security=none&type=tcp#plain-node"
		e, err := parseVless(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "network", "tcp")
		assertAbsent(t, e, "tls")
		assertAbsent(t, e, "servername")
		assertAbsent(t, e, "reality-opts")
	})

	t.Run("tls-grpc", func(t *testing.T) {
		link := "vless://" + testUUID + "@example.com:443?security=tls&type=grpc&host=grpc.example.com&sni=example.com&alpn=h2#grpc-node"
		e, err := parseVless(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "network", "grpc")
		assertBool(t, e, "tls", true)
		assertStr(t, e, "servername", "example.com")
		assertList(t, e, "alpn", []string{"h2"})
		assertMap(t, e, "grpc-opts", map[string]any{"grpc-service-name": "grpc.example.com"})
	})

	t.Run("allow-insecure", func(t *testing.T) {
		link := "vless://" + testUUID + "@example.com:443?security=tls&sni=example.com&allowInsecure=1#scv-node"
		e, err := parseVless(link)
		if err != nil {
			t.Fatal(err)
		}
		assertBool(t, e, "skip-cert-verify", true)
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"vless://example.com:443",                       // 缺 uuid
			"vless://" + testUUID + "@example.com:notaport", // 端口非法
			"vless://" + testUUID + "@example.com",          // 缺端口
			"vless://" + testUUID + "@example.com:99999",    // 端口越界
		}
		for _, c := range cases {
			if _, err := parseVless(c); err == nil {
				t.Errorf("parseVless(%q) should error", c)
			}
		}
	})
}

// ---------- trojan:// ----------

func TestParseTrojan(t *testing.T) {
	t.Run("ws-golden", func(t *testing.T) {
		link := "trojan://password123@example.com:443?sni=example.com&type=ws&path=%2Fws&host=cdn.example.com&allowInsecure=1#trojan-ws"
		e, err := parseTrojan(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "trojan-ws")
		assertStr(t, e, "type", "trojan")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 443)
		assertStr(t, e, "password", "password123")
		assertStr(t, e, "sni", "example.com") // mihomo TrojanOption tag 是 sni，不是 servername
		assertStr(t, e, "network", "ws")
		assertBool(t, e, "skip-cert-verify", true)
		assertMap(t, e, "ws-opts", map[string]any{
			"path":    "/ws",
			"headers": map[string]string{"Host": "cdn.example.com"},
		})
	})

	t.Run("grpc", func(t *testing.T) {
		link := "trojan://password123@example.com:443?type=grpc&host=grpc.example.com#trojan-grpc"
		e, err := parseTrojan(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "network", "grpc")
		assertMap(t, e, "grpc-opts", map[string]any{"grpc-service-name": "grpc.example.com"})
	})

	t.Run("plain-default-tcp", func(t *testing.T) {
		e, err := parseTrojan("trojan://password123@example.com:443#plain")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "network", "tcp")
		assertAbsent(t, e, "ws-opts")
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"trojan://example.com:443",           // 缺密码
			"trojan://pass@example.com",          // 缺端口
			"trojan://pass@example.com:99999",    // 端口越界
			"trojan://pass@example.com:notaport", // 端口非法
		}
		for _, c := range cases {
			if _, err := parseTrojan(c); err == nil {
				t.Errorf("parseTrojan(%q) should error", c)
			}
		}
	})
}

// ---------- hysteria2:// ----------

func TestParseHysteria2(t *testing.T) {
	t.Run("golden", func(t *testing.T) {
		link := "hysteria2://password@example.com:443?sni=example.com&insecure=1&obfs=salamander&obfs-password=obfspw&alpn=h3&up=100mbps&down=200mbps#hy2-node"
		e, err := parseHysteria2(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "hy2-node")
		assertStr(t, e, "type", "hysteria2")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 443)
		assertStr(t, e, "password", "password")
		assertStr(t, e, "sni", "example.com")
		assertBool(t, e, "skip-cert-verify", true)
		assertStr(t, e, "obfs", "salamander")
		assertStr(t, e, "obfs-password", "obfspw")
		assertList(t, e, "alpn", []string{"h3"})
		assertStr(t, e, "up", "100mbps")
		assertStr(t, e, "down", "200mbps")
	})

	t.Run("hy2-prefix", func(t *testing.T) {
		e, err := parseHysteria2("hy2://password@example.com:443?sni=example.com#hy2-prefix")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "type", "hysteria2")
		assertStr(t, e, "sni", "example.com")
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"hysteria2://password@example.com:badport", // 端口非法
			"hysteria2://password@example.com",         // 缺端口
			"hysteria2://example.com:443",              // 缺 userinfo
		}
		for _, c := range cases {
			if _, err := parseHysteria2(c); err == nil {
				t.Errorf("parseHysteria2(%q) should error", c)
			}
		}
	})
}

// ---------- hysteria:// (v1) ----------

func TestParseHysteria(t *testing.T) {
	t.Run("golden", func(t *testing.T) {
		link := "hysteria://example.com:443?auth=secret&up=50mbps&down=100mbps&insecure=1&sni=example.com&obfs=xplus#hysteria-v1"
		e, err := parseHysteria(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "hysteria-v1")
		assertStr(t, e, "type", "hysteria")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 443)
		assertStr(t, e, "auth-str", "secret")
		assertStr(t, e, "up", "50mbps")
		assertStr(t, e, "down", "100mbps")
		assertBool(t, e, "skip-cert-verify", true)
		assertStr(t, e, "sni", "example.com")
		assertStr(t, e, "obfs", "xplus")
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"hysteria://example.com",          // 缺端口
			"hysteria://example.com:99999",    // 端口越界
			"hysteria://example.com:notaport", // 端口非法
		}
		for _, c := range cases {
			if _, err := parseHysteria(c); err == nil {
				t.Errorf("parseHysteria(%q) should error", c)
			}
		}
	})
}

// ---------- tuic:// ----------

func TestParseTUIC(t *testing.T) {
	t.Run("golden", func(t *testing.T) {
		link := "tuic://" + testUUID + "@example.com:443?password=pass123&sni=example.com&congestion_control=bbr&udp_relay_mode=native&allow_insecure=1&alpn=h3#tuic-node"
		e, err := parseTUIC(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "tuic-node")
		assertStr(t, e, "type", "tuic")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 443)
		assertStr(t, e, "uuid", testUUID)
		assertStr(t, e, "password", "pass123")
		assertStr(t, e, "sni", "example.com")
		assertStr(t, e, "congestion-controller", "bbr")
		assertStr(t, e, "udp-relay-mode", "native")
		assertBool(t, e, "skip-cert-verify", true)
		assertList(t, e, "alpn", []string{"h3"})
	})

	t.Run("defaults", func(t *testing.T) {
		e, err := parseTUIC("tuic://" + testUUID + "@example.com:443#tuic-default")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "congestion-controller", "bbr")
		assertStr(t, e, "udp-relay-mode", "native")
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"tuic://example.com:443",                    // 缺 uuid
			"tuic://" + testUUID + "@example.com:bad",   // 端口非法
			"tuic://" + testUUID + "@example.com:99999", // 端口越界
		}
		for _, c := range cases {
			if _, err := parseTUIC(c); err == nil {
				t.Errorf("parseTUIC(%q) should error", c)
			}
		}
	})
}

// ---------- anytls:// ----------

func TestParseAnyTLS(t *testing.T) {
	t.Run("golden", func(t *testing.T) {
		link := "anytls://password@example.com:443?sni=example.com&allowInsecure=1&alpn=h2,http/1.1#anytls-node"
		e, err := parseAnyTLS(link)
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "anytls-node")
		assertStr(t, e, "type", "anytls")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 443)
		assertStr(t, e, "password", "password")
		assertBool(t, e, "udp", true) // R1: anytls 与 vless/ss 一致显式声明 udp
		assertStr(t, e, "sni", "example.com")
		assertBool(t, e, "skip-cert-verify", true)
		assertList(t, e, "alpn", []string{"h2", "http/1.1"})
	})

	t.Run("no-scv-still-udp", func(t *testing.T) {
		// allowInsecure 缺省/为 0 时无 skip-cert-verify，udp: true 必须仍在（A1）
		e, err := parseAnyTLS("anytls://password@example.com:443?sni=example.com#no-scv")
		if err != nil {
			t.Fatal(err)
		}
		assertBool(t, e, "udp", true)
		assertAbsent(t, e, "skip-cert-verify")
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"anytls://example.com:443",          // 缺密码
			"anytls://pass@example.com:badport", // 端口非法
			"anytls://pass@example.com",         // 缺端口
		}
		for _, c := range cases {
			if _, err := parseAnyTLS(c); err == nil {
				t.Errorf("parseAnyTLS(%q) should error", c)
			}
		}
	})
}

// ---------- socks5:// ----------

func TestParseSocks(t *testing.T) {
	t.Run("with-auth", func(t *testing.T) {
		e, err := parseSocks("socks5://user:pass@example.com:1080#socks5-node")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "socks5-node")
		assertStr(t, e, "type", "socks5")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 1080)
		assertStr(t, e, "username", "user")
		assertStr(t, e, "password", "pass")
		assertBool(t, e, "udp", true)
	})

	t.Run("no-auth", func(t *testing.T) {
		e, err := parseSocks("socks5://example.com:1080#socks5-plain")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "type", "socks5")
		assertAbsent(t, e, "username")
		assertAbsent(t, e, "password")
	})

	t.Run("socks-prefix", func(t *testing.T) {
		e, err := parseSocks("socks://example.com:1080#socks-prefix")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "type", "socks5")
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"socks5://example.com",       // 缺端口
			"socks5://example.com:99999", // 端口越界
			"socks5://example.com:bad",   // 端口非法
			"socks5://",                  // 空
		}
		for _, c := range cases {
			if _, err := parseSocks(c); err == nil {
				t.Errorf("parseSocks(%q) should error", c)
			}
		}
	})
}

// ---------- http:// / https:// ----------

func TestParseHTTP(t *testing.T) {
	t.Run("with-auth", func(t *testing.T) {
		e, err := parseHTTP("http://user:pass@example.com:8080#http-node")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "name", "http-node")
		assertStr(t, e, "type", "http")
		assertStr(t, e, "server", "example.com")
		assertInt(t, e, "port", 8080)
		assertStr(t, e, "username", "user")
		assertStr(t, e, "password", "pass")
	})

	t.Run("https-sets-tls", func(t *testing.T) {
		e, err := parseHTTP("https://example.com:8443#https-node")
		if err != nil {
			t.Fatal(err)
		}
		assertStr(t, e, "type", "http")
		assertBool(t, e, "tls", true)
	})

	t.Run("invalid", func(t *testing.T) {
		cases := []string{
			"http://example.com:bad",    // 端口非法
			"http://example.com",        // 缺端口
			"https://example.com:99999", // 端口越界
		}
		for _, c := range cases {
			if _, err := parseHTTP(c); err == nil {
				t.Errorf("parseHTTP(%q) should error", c)
			}
		}
	})
}

// ---------- ParseSubscription ----------

const subBase64 = "c3M6Ly9ZV1Z6TFRJMU5pMW5ZMjA2Y0dGemMzZHZjbVFAZXhhbXBsZS5jb206ODM4OCNzdWItc3MKdHJvamFuOi8vcGFzczEyM0BleGFtcGxlLmNvbTo0NDMjc3ViLXRyb2phbgojIHRoaXMgaXMgYSBjb21tZW50CndlaXJkOi8vdW5rbm93bi1zY2hlbWVAZXhhbXBsZS5jb206MQ=="

func TestParseSubscription(t *testing.T) {
	t.Run("base64-subscription", func(t *testing.T) {
		nodes, err := ParseSubscription([]byte(subBase64), "test")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 2 {
			t.Fatalf("got %d nodes, want 2", len(nodes))
		}
		assertStr(t, nodes[0], "name", "sub-ss")
		assertStr(t, nodes[0], "type", "ss")
		assertStr(t, nodes[1], "name", "sub-trojan")
		assertStr(t, nodes[1], "type", "trojan")
	})

	t.Run("yaml-subscription", func(t *testing.T) {
		yaml := `mixed-port: 7890
mode: rule
proxies:
  - name: yaml-ss
    type: ss
    server: example.com
    port: 8388
    cipher: aes-256-gcm
    password: secret
    udp: true
  - name: yaml-vless
    type: vless
    server: example.com
    port: 443
    uuid: ` + testUUID + `
    network: ws
    tls: true
    ws-opts:
      path: /ws
      headers:
        Host: cdn.example.com
`
		nodes, err := ParseSubscription([]byte(yaml), "test")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 2 {
			t.Fatalf("got %d nodes, want 2", len(nodes))
		}
		assertStr(t, nodes[0], "name", "yaml-ss")
		assertStr(t, nodes[0], "type", "ss")
		assertInt(t, nodes[0], "port", 8388)
		assertStr(t, nodes[1], "name", "yaml-vless")
		assertStr(t, nodes[1], "type", "vless")
		wsOpts, ok := nodes[1]["ws-opts"].(map[string]any)
		if !ok {
			t.Fatalf("ws-opts not a map: %#v", nodes[1]["ws-opts"])
		}
		if wsOpts["path"] != "/ws" {
			t.Errorf("ws-opts.path = %v, want /ws", wsOpts["path"])
		}
	})

	t.Run("single-link", func(t *testing.T) {
		link := "vless://" + testUUID + "@example.com:443?security=tls&type=ws&path=%2Fws#single"
		nodes, err := ParseSubscription([]byte(link), "test")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 1 {
			t.Fatalf("got %d nodes, want 1", len(nodes))
		}
		assertStr(t, nodes[0], "name", "single")
		assertStr(t, nodes[0], "type", "vless")
	})

	t.Run("plain-text-lines", func(t *testing.T) {
		text := "# comment line\n\nss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388#plain-ss\ntrojan://pass@example.com:443#plain-trojan\n"
		nodes, err := ParseSubscription([]byte(text), "test")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 2 {
			t.Fatalf("got %d nodes, want 2", len(nodes))
		}
		assertStr(t, nodes[0], "name", "plain-ss")
		assertStr(t, nodes[1], "name", "plain-trojan")
	})

	t.Run("unrecognized-input", func(t *testing.T) {
		cases := []string{
			"",
			"   \n\t ",
			"hello world this is not a subscription",
			"mixed-port: 7890\nmode: rule", // 是 YAML 但没有 proxies 段
			"proxies: hello",               // proxies 不是列表
		}
		for _, c := range cases {
			if _, err := ParseSubscription([]byte(c), "test"); err == nil {
				t.Errorf("ParseSubscription(%q) should error", c)
			}
		}
	})

	t.Run("unknown-scheme-line-skipped", func(t *testing.T) {
		// 首行是合法链接，后续未知协议行被跳过且不报错
		text := "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388#ok\nweird://foo@bar:1"
		nodes, err := ParseSubscription([]byte(text), "test")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 1 {
			t.Fatalf("got %d nodes, want 1", len(nodes))
		}
		assertStr(t, nodes[0], "name", "ok")
	})

	t.Run("base64-yaml-subscription", func(t *testing.T) {
		// base64 包裹的 Clash YAML 也支持
		yaml := "proxies:\n  - name: b64yaml\n    type: ss\n    server: example.com\n    port: 8388\n    cipher: aes-256-gcm\n    password: x\n"
		blob := base64.StdEncoding.EncodeToString([]byte(yaml))
		nodes, err := ParseSubscription([]byte(blob), "test")
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 1 {
			t.Fatalf("got %d nodes, want 1", len(nodes))
		}
		assertStr(t, nodes[0], "name", "b64yaml")
	})
}

// 防回归：确认所有协议前缀都能被 ParseSubscription 识别（不依赖 base64 形态）。
func TestSchemeDispatch(t *testing.T) {
	links := []string{
		"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388",
		"ssr://ZXhhbXBsZS5jb206ODQ0MzphdXRoX2FlczEyOF9zaGExOmFlcy0yNTYtY2ZiOnRsczEuMl90aWNrZXRfYXV0aDpjR0Z6YzNkdmNtUQ",
		"vmess://aGVsbG8=",
		"vless://" + testUUID + "@example.com:443",
		"trojan://pass@example.com:443",
		"hysteria2://pass@example.com:443",
		"hy2://pass@example.com:443",
		"hysteria://example.com:443",
		"tuic://" + testUUID + "@example.com:443",
		"anytls://pass@example.com:443",
		"socks5://example.com:1080",
		"http://example.com:8080",
		"https://example.com:8443",
	}
	for _, l := range links {
		if !hasKnownScheme(l) {
			t.Errorf("hasKnownScheme(%q) = false", l)
		}
	}
	// 大小写不应误判（前缀匹配大小写敏感）
	if hasKnownScheme("SS://x") {
		t.Error("hasKnownScheme should be case-sensitive")
	}
}
