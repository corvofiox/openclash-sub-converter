# M1 MVP 实现方案（Design Doc）

> 决策记录：方案 A（Go 自研 + 复用 Mihomo 内核能力），Docker Compose 部署。
> 2026-08-09 spike 验证结论：`adapter.ParseProxy`（YAML 条目→节点）可独立复用；
> mihomo 不提供链接解析公共库，需自研 `internal/link`。

## 1. 架构

```
GET /sub?target=clash&url=...&include=&exclude=&rename=&strip_emoji=&config=
  → api.Server
  → fetcher.Fetch（自定义 UA / 超时 / 内存缓存 TTL / 失败跳过 / 多源 | 合并）
  → link.ParseSubscription（Base64 订阅 / Clash YAML 订阅 / 单链接）→ []map[string]any (Clash 条目)
  → adapter.ParseProxy 逐条校验（失败跳过+告警日志）→ 规范节点列表
  → transform.Filter + transform.Rename（正则 include/exclude/rename）
  → groups.Build（手动选择/自动选择/地区组/直连/拒绝组）→ 模板专属组追加（见 5）
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
GET /sub?target=clash&url=<URLENCODE，多源用 | 分隔>&include=<regex>&exclude=<regex>&rename=<regex替换格式>&udp=true&tls13=true&scv=true&strip_emoji=true&template_id=<id1,id2>&config=<模板URL>
GET /healthz           → 200 ok
GET /version           → JSON {version, mihomo}
```

- `target` 仅支持 `clash`，其余返回 400。
- `url` 必填。支持：Base64 订阅 / Clash YAML / 单条链接。
- `rename` 格式：`<regex>/<replacement>`，未匹配的节点名保持不变；多条规则用逗号分隔（`日本/JP,香港/HK`）按顺序执行（前一条的输出是后一条的输入）。已知限制：正则内的字面逗号会被拆分，不支持转义。
- `include`/`exclude`：Go regexp，对节点名匹配；include 命中保留，exclude 命中剔除（先 exclude 后 include）。
- `scv=true` 输出节点 `skip-cert-verify: true`；`udp=true` 输出 `udp: true`；`tls13=true` 输出 `tls13: true`（SS/Trojan 系适用字段）。
- `strip_emoji=true`（默认关）：输出阶段剥离节点名中的 emoji 字符（旗标/符号/VS16/ZWJ/键帽），保留空格与 `|` `｜` 等分隔符；地区识别始终基于原始节点名（与开关无关），剥离后重名自动追加序号并同步改写组引用。

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
[手动选择] type=select, proxies=[DIRECT, 自动选择, 各地区组...]
[自动选择] type=url-test, url=<测速URL>, interval=300, proxies=[全部节点]
[地区组]  按节点名识别地区（多源线索：emoji 国旗 / 中文(含繁体/城市) / 拼音 / 英文(含城市) / ISO 双字母，
           取名字中第一个地区线索；🇭🇰/香港/HK-01→香港, 🇯🇵/日本/Tokyo-2→日本, 🇸🇬/新加坡/SG-01→新加坡...），
           type=url-test, 组名「香港节点」，proxies=[该地区节点]
[未识别地区] 无任何地区线索的节点放入「其他节点」组
内置出站：[DIRECT] [REJECT]（Clash 内置出站，手动选择组 proxies 与 rules 中直接引用即可，
        不生成空组声明——空组会被 mihomo 判非法：'use' or 'proxies' missing）
```
- emoji/中文/拼音/英文/ISO 五层别名表内置常量（47 地区，含无歧义城市名），一别名只映射一地区，全局唯一性有测试强制。
- 组名格式固定 `「<地区>节点」`（无 emoji，命名契约 v1），后续 M2 命名引擎接管。
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
proxy-groups: <groups>
proxies: <nodes>
rules:
  - GEOIP,CN,DIRECT
  - MATCH,手动选择
```
外部 `config` 参数：支持 subconverter 风格 `.ini` 模板的**最小子集**（`[custom]` 段的 include/exclude/rename 忽略——由 URL 参数控制；仅读取分组/规则部分会复杂化，M1 不做），M1 仅支持 URL 参数方式。`config` 参数在 M1 返回 501 或忽略（设计：忽略 + warn 日志，README 注明 M2 支持）。

规则注入：内置最小规则集（GEOIP,CN,DIRECT + MATCH 兜底）。选中规则模板时注入
`rule-providers` 段（http 型 provider，path `./ruleset/<模板名>.yaml`）并把
`RULE-SET,<模板名>,<专属组名>` 插在第一条 MATCH 前；`rule-providers` 为整体覆盖
段，多模板一次调用全部注入（严禁逐次调用互相覆盖）。

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
- `internal/groups`: 多源识别分组（emoji/中文/拼音/英文/ISO）、零节点组跳过。
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

---

# M3 Web 管理台（实现方案）

> 决策记录：同端口同 Handler 挂载（不引入独立管理端口/进程）+ `go:embed` 内嵌
> 原生 HTML/CSS/JS 单页（零外部依赖）+ JSON 文件持久化（`internal/store`）。
> 2026-08-09 落地。

## M3-1 架构

```
浏览器（管理台单页，5 个 Tab）
  │  fetch /api/v1/*（带 X-Token）
  ▼
api.NewServer（同一 25500 端口）
  ├─ GET /sub、/healthz、/version    —— 永不鉴权（OpenClash 侧拉取用）
  ├─ /api/v1/*  ── authMiddleware ── store CRUD / convert / logs
  └─ /          ── webui.Handler()（go:embed 静态资源，不鉴权）
```

- **同端口同 Handler**：管理台与转换接口共用 25500 端口与进程，避免多端口暴露面；
  `cmd/server` 单进程无内部通信。
- **`go:embed`**：`internal/webui` 把 `static/`（index.html + style.css + app.js，
  合计 <60KB）编进二进制；`fs.Sub` 切根到 `static/` 后交给 `http.FileServer`，
  路径 `/` 自动命中 index.html，资源以 `/app.js` 相对根引用（不用 StripPrefix）。
  前端零框架、零 CDN、零外链，离线可用。
- **ServeMux 优先级**：Go 1.22+ mux 精确路径优先，`/` 兜底路由不影响已注册的
  `/sub`、`/healthz`、`/version`；未命中静态路径（如 /favicon.ico）由 FileServer
  返回 404；未匹配的 `/api/v1/*` 子路径返回 JSON 404。
- **页面不鉴权**：`/` 与静态资源不进 authMiddleware——页面本身无敏感数据（数据
  全在 `/api/v1/*`），且浏览器导航无法携带令牌头，若页面被挡在 401 后管理台
  将完全不可达（死锁）。API 的 401 由前端 `apiFetch` 弹令牌框兜底。
- **数据层**：`internal/store` 三组 JSON（sources/logs/templates.json）落盘在
  `server.data_dir`（默认 `./data`，env `OSC_DATA_DIR` 覆盖）；原子写
  （临时文件 + fsync + rename）+ 写锁，崩溃不产生半成品文件。

## M3-2 端点清单（/api/v1，全部经 authMiddleware）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /api/v1/sources | 订阅源列表（URL 脱敏） |
| POST / PUT / DELETE | /api/v1/sources[/{id}] | 订阅源 CRUD（PUT 为部分更新，URL 留空不变） |
| POST | /api/v1/convert/preview | 转换预览：返回 nodes（name/type）+ groups + 耗时，不渲染 YAML |
| POST | /api/v1/convert/run | 完整转换：返回渲染后 YAML + node_count + 耗时 |
| GET | /api/v1/logs?limit= | 转换日志（时间倒序，上限 200 条环形） |
| POST | /api/v1/logs/{id}/retry | 用日志记录源与参数重跑 preview 管线 |
| GET/POST/PUT/DELETE | /api/v1/templates[/{id}] | 规则模板 CRUD（behavior: domain/ipcidr/classical，format: yaml/text） |
| GET | /api/v1/version | 版本信息（同公开 /version，但需鉴权） |

- convert 请求体：`source_id` 与 `url`（临时，可 `|` 多源）二选一，`source_id`
  优先；`include/exclude/rename` 正则、`udp/tls13/scv` 布尔、`template_id`
  可选（逗号分隔多值，任一模板不存在/禁用 → 400）。规则模板注入 = 把模板 URL
  写进输出 YAML 的 `rule-providers` 并为每个模板生成专属策略组，规则集由
  OpenClash 侧拉取，本服务不拉取不校验规则内容。
- 日志响应剔除 `url_full`（完整临时 URL 仅内部 retry 使用，永不外泄）。

## M3-3 前端（internal/webui/static）

单页 5 个 Tab + 顶部栏（标题 / GET /version 版本号 / 鉴权状态徽标）：

1. **订阅源**：表格（名称/脱敏 URL/启用开关/编辑/删除，删除需 confirm()）；
   新增/编辑共用内联表单，编辑时 URL 留空表示不变（列表只回脱敏 URL）；
   启用开关 change 即 PUT。
2. **订阅转换**：已启用源下拉（或「临时 URL」折叠展开时禁用下拉）；
   include/exclude/rename + scv/udp/tls13 + 模板多选 checkbox（仅 enabled，
   勾选结果 `join(',')` 赋 `template_id`）；「预览节点」渲染节点/
   策略组滚动区与耗时；「生成订阅链接」用 `window.location.protocol` +
   `window.location.host` 拼 `/sub?target=clash&src=<id>|url=<enc>`（url 只经
   URLSearchParams 单次编码，绝不预编码，否则服务端解码后仍带 %XX → 400）
   供 OpenClash 填订阅地址，一键复制；
   「查看完整 YAML」输出 textarea + 复制。
3. **转换日志**：时间/来源/参数摘要（include/exclude/rename 有值才显示）/
   状态/错误消息/节点数/耗时；失败行红色 +「重试」（POST retry 成功即刷新）。
4. **规则模板**：表格 + CRUD 表单（behavior/format 下拉），说明文字注明
   「OpenClash 侧拉取规则集，本服务仅注入 URL 到输出 YAML 的 rule-providers」。
5. **认证**：令牌输入/清除；所有请求走统一 `apiFetch`：localStorage
   `osc_token` → `X-Token` 头；收到 401 弹令牌输入框，保存后自动重试原请求
   （连续 401 ≥2 次停止弹窗，防 token 错误时无限递归；弹窗前先关旧弹窗）。

## M3-4 数据文件

`<data_dir>/{sources,logs,templates}.json`，格式 `{"version":1,"<name>":[...]}`；
version 不匹配视为空态（warn），损坏文件备份为 `.json.bak` 后以空态继续——
均不崩溃。Docker 部署时 `./data:/app/data` 挂载持久化。

### 预置模板（首次启动种子）

`templates.json` 不存在时（首次启动），自动种入 8 个 ACL4SSR 常用规则模板：
广告拦截（BanAD.list）、Netflix、YouTube、Telegram、Google、Twitter、Apple、
Microsoft（Ruleset/ 系列），URL 指向 ACL4SSR 官方 Clash 规则集
（`https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/`），
统一 behavior=domain、format=text、默认禁用（Enabled=false），ID 与落盘
复用普通模板创建逻辑（crypto/rand 12 hex + 原子写 0600）。种入后即为
**普通模板**：可编辑、可删除、可改名，无任何特殊标记。仅当文件不存在时
种入——文件已存在（哪怕空列表）、损坏恢复空态、version 不匹配空态均不
触发，用户删光预置后重启不会复活。

## M3-5 认证

- 令牌来自 env `OSC_ADMIN_TOKEN`（不读配置文件，避免随配置分发泄露）；
  空串 = 不鉴权（内网默认）。
- 中间件要求 `X-Token` 或 `Authorization: Bearer <token>`，常量时间比较
  （crypto/subtle，scheme 大小写不敏感），失败 401。
- 仅 `/api/v1/*` 受保护；`/` 页面与静态资源、`/sub`、`/healthz`、`/version`
  保持公开（页面无敏感数据且浏览器导航无法携带令牌头；OpenClash 拉订阅
  不携带令牌）。

## M3-6 已知限制

- **SSRF**：`/sub?url=` 与管理台临时 URL 允许任意 http/https 目标（同 M2 决策），
  内网地址也可被拉取；管理台本身有鉴权可设，但 `/sub` 公开。
- **令牌默认不设**：NAS 内网默认 `OSC_ADMIN_TOKEN` 留空即无鉴权，生产外网
  暴露需自行设置。
- **多实例不支持**：JSON 文件无跨进程锁/同步，多副本部署会互相覆盖；数据
  目录须独占。
- 日志上限 200 条环形淘汰；retry 只对 source_id 或临时 URL 的日志可用
  （URLFull 为空且源被删/禁用时 409/400）。
- 前端无浏览器侧路由刷新（单页 + Tab 切换），刷新回到订阅源页。
