# OpenClash 订阅转换工具

自托管、全容器化（Docker Compose）、面向 OpenClash 深度定制的订阅转换服务。
多机场订阅聚合 → 节点过滤/重命名 → 策略组构建 → 输出经 mihomo 全量校验的 Clash YAML。

## 简介

OpenClash 订阅转换工具是一个轻量级 HTTP 服务（Go 单二进制，无数据库）：

- **多源聚合**：一次请求拉取多个机场订阅（`|` 分隔），自动识别 Base64 订阅 / Clash YAML / 单条协议链接（ss、ssr、vmess、vless、trojan、hysteria2、hysteria、tuic、anytls、socks5、http）
- **可控命名**：`include` / `exclude` / `rename` 正则过滤与重命名，节点名/策略组名完全可控，与 OpenClash 自定义规则命名契约对齐
- **策略组自动构建**：手动选择 / 自动选择 / 按 emoji/中文/拼音/英文/ISO 代码自动识别地区并分组（**组名不带 emoji**，如「香港节点」）/ DIRECT、REJECT 内置出站直接引用（不生成空组声明）
- **输出可靠**：YAML 渲染后调用 mihomo `config.UnmarshalRawConfig` 全量校验，保证 OpenClash 可直接消费
- **安全**：订阅 URL 凭证不出现在日志与错误消息中（只记 host）；响应 `Cache-Control: no-store`

> 复用 mihomo（GPL-3.0）作为校验与解析内核；本项目自研协议链接解析（`internal/link`）。

## 快速开始（Docker Compose）

```bash
docker compose up -d --build
curl http://127.0.0.1:25500/healthz   # → ok
```

- 监听端口：`25500`（可用环境变量 `OSC_PORT` 覆盖）
- 配置文件挂载：`./config:/app/config:ro`（修改后需重启容器生效）
- 健康检查：`wget /healthz`，失败自动重启（`restart: unless-stopped`）

本地直接运行：

```bash
go run ./cmd/server            # 读取 ./config/config.yaml（缺失则用默认值）
```

## API 文档

### `GET /healthz`

健康检查。返回 `200`，body 为 `ok`。

### `GET /version`

版本信息。返回 `200` + JSON：

```json
{"version":"0.1.0","mihomo":"v1.19.29"}
```

### `GET /sub` — 订阅转换（兼容 subconverter 调用习惯）

```
GET /sub?target=clash&url=<URLENCODE>&include=&exclude=&rename=&udp=&tls13=&scv=&strip_emoji=
```

| 参数 | 必填 | 说明 |
|------|------|------|
| `target` | 是 | 仅支持 `clash`，其他值返回 400 |
| `url` | 是 | 订阅源地址（http/https）；多源用 `\|` 分隔（URL 中编码为 `%7C`）；任一源失败不影响其他源，全部失败返回 502 |
| `include` | 否 | Go 正则，节点名命中才保留 |
| `exclude` | 否 | Go 正则，节点名命中剔除（先 exclude 后 include） |
| `rename` | 否 | `<regex>/<replacement>` 重命名；多条用 `,` 分隔按顺序执行（前一条输出为后一条输入），如 `日本/JP,香港/HK`；正则内字面逗号会被拆分（不支持转义） |
| `udp` | 否 | `true`/`1` 时节点输出 `udp: true` |
| `tls13` | 否 | `true`/`1` 时 ss/trojan/http 节点输出 `tls13: true` |
| `scv` | 否 | `true`/`1` 时 vmess/vless/trojan/hysteria2/tuic/anytls 节点输出 `skip-cert-verify: true` |
| `strip_emoji` | 否 | `true`/`1` 时节点名剥离 emoji（旗标/符号/VS16/ZWJ；保留空格与分隔符；剥离后重名自动加序号；识别仍基于原始名） |

成功响应：`200`，`Content-Type: text/yaml; charset=utf-8`，`Cache-Control: no-store`，body 为完整 Clash YAML（mixed-port 7893、allow-lan、fake-ip DNS、proxy-groups、proxies、GEOIP,CN,DIRECT + MATCH 规则，proxy-groups 段在 proxies 段之前）。

示例（OpenClash 配置订阅 URL 可直接填此链接）：

```
http://192.168.10.10:25500/sub?target=clash&url=https%3A%2F%2Fexample.com%2Fsub1%7Chttps%3A%2F%2Fexample.com%2Fsub2&include=%E9%A6%99%E6%B8%AF&udp=true
```

错误响应均为 JSON：`{"error":"..."}`

| 状态码 | 场景 |
|--------|------|
| 400 | 参数缺失/非法（target、url、正则等） |
| 502 | 所有订阅源拉取/解析失败（错误消息只含源 host） |
| 500 | 转换、渲染或 mihomo 校验失败 |

## 开发说明

```bash
export PATH=/opt/data/go/bin:$PATH   # Go 1.26.5
go build ./...
go test ./internal/...
go run ./cmd/server
```

包结构：

```
internal/link        协议链接解析 → Clash 条目（自研，Base64 订阅 / YAML 订阅 / 单链接）
internal/fetcher     HTTP 拉取订阅源（自定义 UA、超时、内存缓存 TTL、10MB 上限）
internal/transform   节点过滤/重命名（include/exclude/rename 正则）
internal/groups      策略组构建（手动/自动/地区组；DIRECT/REJECT 内置出站直接引用）
internal/template    配置组装（默认模板 + udp/tls13/scv 选项）
internal/output      YAML 渲染 + mihomo config.UnmarshalRawConfig 全量校验
internal/api         HTTP 路由（slog 请求日志、错误脱敏）
cmd/server           服务入口（优雅关闭，SIGINT/SIGTERM 5s 超时）
```

配置项（`config/config.yaml`，均有默认值，环境变量可覆盖）：

| 配置 | 环境变量 | 默认值 | 说明 |
|------|----------|--------|------|
| `server.port` | `OSC_PORT` | `25500` | 监听端口 |
| `fetcher.user_agent` | `OSC_FETCHER_UA` | `clash-verge/v2.0.0` | 拉取订阅的 UA |
| `fetcher.timeout_seconds` | — | `20` | 单源超时 |
| `fetcher.cache_ttl_seconds` | `OSC_CACHE_TTL` | `300` | 订阅缓存 TTL |
| `fetcher.max_bytes` | — | `10485760` | 单源响应上限 |
| `logging.level` | `OSC_LOG_LEVEL` | `info` | debug/info/warn/error |

## M1 边界（暂不支持）

- Web 界面、鉴权（M3 规划）
- rule-provider / proxy-providers 输出、subconverter `.ini` 模板（M2 规划）
- 出站代理拉取订阅源（M2 规划）
- Surge / QuanX 等其他目标格式

## 文档

- [需求分析报告](docs/需求分析报告.md)
- [M1 实现方案](docs/design.md)
