# M1 MVP 实现方案（Design Doc）

> 决策记录：方案 A（Go 自研 + 复用 Mihomo 内核能力），Docker Compose 部署。
> 2026-08-09 spike 验证结论：`adapter.ParseProxy`（YAML 条目→节点）可独立复用；
> mihomo 不提供链接解析公共库，需自研 `internal/link`。

## 1. 架构

```
GET /sub?target=clash&url=...&include=&exclude=&rename=&config=
  → api.Server
  → fetcher.Fetch（自定义 UA / 超时 / 内存缓存 TTL / 失败跳过 / 多源 | 合并）
  → link.ParseSubscription（Base64 订阅 / Clash YAML 订阅 / 单链接）→ []map[string]any (Clash 条目)
  → adapter.ParseProxy 逐条校验（失败跳过+告警日志）→ 规范节点列表
  → transform.Filter + transform.Rename（正则 include/exclude/rename）
  → groups.Build（手动选择/自动选择/地区组 select|url-test + DIRECT/REJECT 兜底）
  → template.Merge（内置默认模板 / 外部 config 模板）
  → output.Render（YAML 序列化）
  → config.UnmarshalRawConfig 全量校验（失败则 500）
  → 200 text/yaml
```

## 2. 包结构与职责

| 包 | 职责 | 关键导出 |
|----|------|---------|
| `internal/config` | 服务配置（端口/缓存 TTL/UA/日志级别/出站代理） | `Load(path)` |
| `internal/fetcher` | HTTP 拉取订阅源，缓存 | `Fetcher.Fetch(urls []string) ([]byte, error)` |
| `internal/link` | 协议链接解析 → Clash YAML 条目 | `ParseSubscription([]byte) ([]map[string]any, error)` |
| `internal/transform` | 过滤/重命名 | `Apply(nodes, Filter{Rename, Include, Exclude})` |
| `internal/groups` | 策略组构建 | `Build(nodes) ([]map[string]any, error)` |
| `internal/template` | 模板合并 | `Merge(nodes, groups, opts) ([]byte, error)` |
| `internal/output` | YAML 输出 + mihomo 校验 | `Render(cfg) ([]byte, error)` |
| `internal/api` | HTTP 路由 | `NewServer(cfg) http.Handler` |
| `cmd/server` | main | — |

## 3. HTTP API（兼容 subconverter 调用习惯）

```
GET /sub?target=clash&url=<URLENCODE，多源用 | 分隔>&include=<regex>&exclude=<regex>&rename=<regex替换格式>&udp=true&tls13=true&scv=true&config=<模板URL>
GET /healthz           → 200 ok
GET /version           → JSON {version, mihomo}
```

- `target` 仅支持 `clash`，其余返回 400。
- `url` 必填。支持：Base64 订阅 / Clash YAML / 单条链接。
- `rename` 格式：`<regex>/<replacement>`（subconverter 风格）或纯 Go regexp `replacement` 风格：统一采用 `<regex>/<replacement>`，未匹配的节点名保持不变。
- `include`/`exclude`：Go regexp，对节点名匹配；include 命中保留，exclude 命中剔除（先 exclude 后 include）。
- `scv=true` 输出节点 `skip-cert-verify: true`；`udp=true` 输出 `udp: true`；`tls13=true` 输出 `tls13: true`（SS/Trojan 系适用字段）。

## 4. 协议链接解析映射表（internal/link 核心）

### 4.1 ss://
- SIP002: `ss://base64url(method:password)@host:port#name`；插件 `?plugin=...`
- 老格式: `ss://base64(method:password@host:port)#name`
- 条目：`{name, type:ss, server, port, cipher, password, udp}`；插件可选 `plugin`/`plugin-opts`（`obfs-local`/`simple-obfs` **归一为 `obfs`**——mihomo 只识别 `obfs`/`v2ray-plugin`/`gost-plugin`，否则混淆静默失效）

### 4.2 ssr://
- `ssr://base64url(host:port:protocol:method:obfs:base64url(pass)/?params)`，params: `remarks`、`group`、`obfsparam`、`protoparam`
- 条目：`{name, type:ssr, server, port, cipher, password, protocol, obfs, protocol-param, obfs-param, udp}`（`udp: true`——mihomo ShadowSocksROption.UDP 默认 false，SSR 机场普遍支持 UDP）

### 4.3 vmess://
- `vmess://base64(json)`，字段：`add→server, port, id→uuid, ps→name, net→network, type→(header), host→(ws/http host), path, tls, sni→servername, alpn, fp→client-fingerprint, aid→alterId, scy→cipher`
- 条目：`{name, type:vmess, server, port, uuid, alterId, cipher, udp, tls, network, ws-opts{path, headers{host}} / http-opts, servername, client-fingerprint}`

### 4.4 vless://（含 Reality）
- `vless://uuid@host:port?encryption=none&security=reality|tls|none&sni=&fp=&pbk=&sid=&spx=&flow=&type=tcp|ws|grpc&host=&path=&alpn=&allowInsecure=1#name`
- 条目：`{name, type:vless, server, port, uuid, network, tls, udp, flow, servername, reality-opts{public-key, short-id, spider-x}, client-fingerprint, ws-opts{...}, skip-cert-verify}`

### 4.5 trojan://
- `trojan://pass@host:port?security=tls&sni=&type=ws|grpc|tcp&host=&path=&allowInsecure=1#name`
- 条目：`{name, type:trojan, server, port, password, sni, network, ws-opts/grpc-opts, skip-cert-verify}`（mihomo TrojanOption tag 是 `sni` 非 servername——输出 servername 会被静默忽略并回退 server）

### 4.6 hysteria2://（hy2://）
- `hysteria2://pass@host:port?sni=&insecure=1&obfs=&obfs-password=&alpn=&up=&down=#name`
- 条目：`{name, type:hysteria2, server, port, password, sni, skip-cert-verify, obfs, obfs-password, up, down, alpn}`

### 4.7 hysteria://（v1，尽力支持）
- `hysteria://host:port?auth=&up=&down=&insecure=1&sni=&obfs=#name`
- 条目：`{name, type:hysteria, server, port, auth-str, up, down, skip-cert-verify, sni}`

### 4.8 tuic://
- `tuic://uuid@host:port?password=&sni=&congestion_control=bbr&udp_relay_mode=native&allow_insecure=1&alpn=#name`
- 条目：`{name, type:tuic, server, port, uuid, password, congestion-controller, udp-relay-mode, skip-cert-verify, sni, alpn}`

### 4.9 anytls://
- `anytls://pass@host:port?sni=&allowInsecure=1#name`
- 条目：`{name, type:anytls, server, port, password, sni, skip-cert-verify}`

### 4.10 socks5:// / http://（直连代理）
- `socks5://user:pass@host:port#name` → `{name, type:socks5, server, port, username, password, udp}`
- `http://user:pass@host:port#name` → `{name, type:http, server, port, username, password}`

### 4.11 订阅容器
- Base64 订阅：解码后按行解析（自动识别 `ss://` 等前缀；行首 `#` 注释跳过）
- Clash YAML 订阅：解析 `proxies:` 段直接产出条目（无需再经链接解析）
- 单链接：直接当一行处理
- 错误行：跳过并记 warn 日志（不中断整体）

## 5. 策略组构建（internal/groups）

```
[🚀 手动选择] type=select, proxies=[DIRECT, 自动选择, 各地区组...]
[♻️ 自动选择] type=url-test, url=<测速URL>, interval=300, proxies=[全部节点]
[🌍 地区组]  按节点名首 emoji 国旗映射（🇭🇰→香港, 🇯🇵→日本, 🇸🇬→新加坡, 🇺🇸→美国, 🇹🇼→台湾, 🇰🇷→韩国, ...）,
             type=url-test, 组名「🇭🇰 香港节点」，proxies=[该地区节点]
[未识别地区] 放入「🌐 其他节点」组
兜底组：[DIRECT] [REJECT]（内置）
```
- emoji → 地区名映射表内置常量（覆盖主要国旗），映射表可被模板覆盖。
- 组名格式固定 `「<emoji> <地区>节点」`（命名契约 v1），后续 M2 命名引擎接管。
- 节点数 0 的地区不生成组。

## 6. 模板合并（internal/template）

内置默认模板（无 config 参数时）：
```yaml
mixed-port: 7893
allow-lan: true
mode: rule
log-level: info
ipv6: false
dns:
  enable: true
  listen: 0.0.0.0:7874
  enhanced-mode: fake-ip
  nameserver: [223.5.5.5, 119.29.29.29]
  fallback: [tls://8.8.8.8, tls://1.1.1.1]
proxies: <nodes>
proxy-groups: <groups>
rules:
  - GEOIP,CN,DIRECT
  - MATCH,🚀 手动选择
```
外部 `config` 参数：支持 subconverter 风格 `.ini` 模板的**最小子集**（`[custom]` 段的 include/exclude/rename 忽略——由 URL 参数控制；仅读取分组/规则部分会复杂化，M1 不做），M1 仅支持 URL 参数方式。`config` 参数在 M1 返回 501 或忽略（设计：忽略 + warn 日志，README 注明 M2 支持）。

规则注入：M1 内置最小规则集（GEOIP,CN,DIRECT + MATCH 兜底），rule-provider 引用放 M2。

## 7. 输出校验（internal/output）

1. `yaml.v3` 序列化。
2. 构造完整 `RawConfig` → `config.UnmarshalRawConfig` 校验（**YAML 语法层**：age 解密 + 反序列化，不执行节点级语义校验），失败返回 500 + 错误详情（脱敏）。
3. 节点级语义校验在 api 层完成（§1 第 4 步 `adapter.ParseProxy` 逐条校验，失败跳过+告警）——本层只兜底 YAML 结构错误。
4. Content-Type: `text/yaml; charset=utf-8`。

## 8. 服务配置（internal/config）

```yaml
# config/config.yaml
server:
  port: 25500
fetcher:
  user_agent: "clash-verge/v2.0.0"   # 默认 UA，可被 URL 参数覆盖
  timeout_seconds: 20
  cache_ttl_seconds: 300
  max_bytes: 10485760                 # 单源 10MB 上限
logging:
  level: info
```
env 覆盖：`OSC_PORT`、`OSC_FETCHER_UA`、`OSC_CACHE_TTL`、`OSC_LOG_LEVEL`（12-factor，Docker 用）。

## 9. 安全约束（M1）

- 日志/错误消息**脱敏**：订阅 URL 只记录 host 与参数名，不记录 query 值（凭证在 url 参数里）。
- 无鉴权（内网自用）；`/sub` 的 `url` 仅允许 http/https 协议（防 file:// 读本地）。
- 响应头 `Cache-Control: no-store`。

## 10. Docker 化

```dockerfile
# 多阶段
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /osc ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /osc /usr/local/bin/osc
WORKDIR /app
EXPOSE 25500
ENTRYPOINT ["osc"]
```

```yaml
# docker-compose.yml
services:
  openclash-sub-converter:
    build: .
    container_name: openclash-sub-converter
    restart: unless-stopped
    ports: ["25500:25500"]
    environment:
      - OSC_PORT=25500
      - OSC_LOG_LEVEL=info
    volumes:
      - ./config:/app/config:ro
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:25500/healthz"]
      interval: 30s
      timeout: 5s
      retries: 3
```

## 11. 测试策略

- `internal/link`: 每协议 ≥1 个黄金用例（真实格式链接→期望条目字段），非法输入用例。
- `internal/transform`: include/exclude/rename 组合用例。
- `internal/groups`: emoji 分组、零节点组跳过。
- `internal/output`: 渲染→mihomo 校验通过；坏节点被剔除。
- `internal/api`: httptest + httptest.Server 假订阅源（Base64 + YAML 两种），断言 200/400/500、Content-Type。
- 端到端冒烟：`go run ./cmd/server` 起服务 → curl /sub 真实转换 → mihomo 校验。

## 12. 明确不做（M1 边界）

- ❌ Web 界面（M3）
- ❌ 命名契约引擎/契约校验（M2）
- ❌ rule-provider / proxy-providers 输出（M2/M4）
- ❌ subconverter .ini 模板完整兼容（M2 起）
- ❌ 出站代理拉取订阅源（M2，需凭据决策）
- ❌ Surge/QuanX 等目标格式
- ❌ 鉴权（M3 随 Web 界面）
