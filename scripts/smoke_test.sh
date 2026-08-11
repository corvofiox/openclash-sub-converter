#!/usr/bin/env bash
# openclash-sub-converter 端到端冒烟测试 v2
# 随机端口 + 健康检查轮询 + 产物保留 + trap 清理
set -e
export PATH=/opt/data/go/bin:$PATH
cd "$(dirname "$0")/.."

SRV_PORT=$((25000 + RANDOM % 500))
SRC_PORT=$((19000 + RANDOM % 500))
WORK=/opt/data/smoke_artifacts
rm -rf $WORK && mkdir -p $WORK

cleanup() {
  [ -n "$SRV_PID" ] && kill $SRV_PID 2>/dev/null
  [ -n "$TOK_PID" ] && kill $TOK_PID 2>/dev/null
  [ -n "$HTTP_PID" ] && kill $HTTP_PID 2>/dev/null
  # go run 孤儿子进程（进程可能已退出，先判断 cmdline 可读再读，避免竞态警告）
  # 孤儿 server 二进制在 go build 缓存里（go run 的子进程，PPid=1 不随父死），
  # 用 readlink exe 匹配缓存路径——避免匹配调用方 cmdline 文本导致自杀（之前 head -c 100
  # 截断导致匹配失败残留；后改 cmdline grep 又误杀含模式文本的调用方自身，两次教训）
  for pid in $(ls /proc 2>/dev/null | grep -E '^[0-9]+$'); do
    exe=$(readlink /proc/$pid/exe 2>/dev/null) || continue
    case "$exe" in
      */.cache/go-build/*/server) kill -9 $pid 2>/dev/null || true ;;
    esac
  done
}
trap cleanup EXIT

# 1. 构造订阅源
cat > $WORK/sub_plain.txt <<'EOF'
ss://YWVzLTI1Ni1nY206cGFzc3dvcmQxMjM@jp1.example.com:8388#🇯🇵 日本01 东京
ss://YWVzLTI1Ni1nY206cGFzc3dvcmQxMjM@jp2.example.com:8388#🇯🇵 日本02 大阪
vless://abcdef1234567890@us1.example.com:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE&sid=abcd1234&type=tcp#🇺🇸 美国01 优选
trojan://trojanpass@sg1.example.com:443?sni=sg.example.com&type=ws&path=%2Fws&host=sg.example.com#🇸🇬 新加坡01 落地
hysteria2://hypass@hk2.example.com:443?sni=hk.example.com&insecure=1#🇭🇰 香港02 三网
tuic://abcdef1234567890@tw1.example.com:443?password=tpass&sni=tw.example.com&congestion_control=bbr#🇹🇼 台湾01 低延迟
anytls://anypass@kr1.example.com:443?sni=kr.example.com#🇰🇷 韩国01 首尔
EOF
base64 -w0 $WORK/sub_plain.txt > $WORK/sub_b64.txt

# 2. 本地 HTTP 订阅源
python3 -m http.server $SRC_PORT --bind 127.0.0.1 --directory $WORK >/dev/null 2>&1 &
HTTP_PID=$!
sleep 1

# 3. 起转换服务（健康检查轮询替代固定 sleep）
export OSC_PORT=$SRV_PORT OSC_LOG_LEVEL=info OSC_DATA_DIR=$WORK/data
go run ./cmd/server >$WORK/server.log 2>&1 &
SRV_PID=$!
for i in $(seq 1 30); do
  if curl -s -m 1 "http://127.0.0.1:$SRV_PORT/healthz" 2>/dev/null | grep -q ok; then
    echo "服务就绪 (${i}s)"; break
  fi
  if ! kill -0 $SRV_PID 2>/dev/null; then echo "服务进程已退出!"; cat $WORK/server.log; exit 1; fi
  sleep 1
done
if ! curl -s -m 1 "http://127.0.0.1:$SRV_PORT/healthz" | grep -q ok; then
  echo "服务 30s 未就绪"; cat $WORK/server.log; exit 1
fi

SUB_URL="http://127.0.0.1:$SRC_PORT/sub_b64.txt"
ENC=$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1],safe=''))" "$SUB_URL")
BASE="http://127.0.0.1:$SRV_PORT/sub?target=clash&url=$ENC"

PASS=0; FAIL=0
check() {
  if [ "$2" = "$3" ]; then PASS=$((PASS+1)); echo "✅ $1 (=$2)";
  else FAIL=$((FAIL+1)); echo "❌ $1 (期望 $2 实际 $3)"; fi
}
# 节点计数: 匹配 " name:" 行(排除 servername:), 覆盖 2/4 空格缩进
NODES() { sed -n '/^proxies:/,/^proxy-groups:/p' "$1" | grep -c ' name:'; }
GROUPS() { sed -n '/^proxy-groups:/,/^rules:/p' "$1" | grep -c ' name:'; }

# 4. 基础转换
curl -s -o $WORK/out1.yaml -w "%{http_code}" "$BASE" > $WORK/code.txt
check "基础转换 HTTP 200" "200" "$(cat $WORK/code.txt)"
check "节点数 7" "7" "$(NODES $WORK/out1.yaml)"
check "组数 10" "10" "$(GROUPS $WORK/out1.yaml)"
check "手动选择组存在" "1" "$(sed -n '/^proxy-groups:/,/^rules:/p' $WORK/out1.yaml | grep -c '手动选择')"
check "vless reality 节点" "1" "$(grep -c 'reality-opts' $WORK/out1.yaml)"

# 5. 参数
check "include=日本 剩2节点" "2" "$(curl -s "$BASE&include=%E6%97%A5%E6%9C%AC" | sed -n '/^proxies:/,/^proxy-groups:/p' | grep -c ' name:')"
check "exclude=日本 剩5节点" "5" "$(curl -s "$BASE&exclude=%E6%97%A5%E6%9C%AC" | sed -n '/^proxies:/,/^proxy-groups:/p' | grep -c ' name:')"
check "rename 生效(2节点×3处)" "6" "$(curl -s "$BASE&rename=%E6%97%A5%E6%9C%AC/JP" | grep -c 'JP0')"
check "scv=true 5节点有scv" "5" "$(curl -s "$BASE&scv=true" | grep -c 'skip-cert-verify: true')"
# 5.5 strip_emoji：yaml.v3 对补充平面字符（emoji）输出 \U0001Fxxx 转义，
# grep 用转义序列（旗标均在 U+1F1E6-1F1FF，前缀 \U0001F1）
# 基线：默认（开关关）proxies 段含 7 处旗标转义（证明 grep 模式有效）
check "默认节点名旗标保留(基线)" "7" "$(curl -s "$BASE" | sed -n '/^proxies:/,/^proxy-groups:/p' | grep -c '\\U0001F1')"
check "strip_emoji 剥离旗标emoji" "0" "$(curl -s "$BASE&strip_emoji=true" | sed -n '/^proxies:/,/^proxy-groups:/p' | grep -c '\\U0001F1')"
check "strip_emoji 节点数不变" "7" "$(curl -s "$BASE&strip_emoji=true" | sed -n '/^proxies:/,/^proxy-groups:/p' | grep -c ' name:')"
check "strip_emoji 组名无emoji且存在" "1" "$(curl -s "$BASE&strip_emoji=true" | sed -n '/^proxy-groups:/,/^rules:/p' | grep -c -- 'name: 香港节点')"

# 6. 错误路径
check "缺参数 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SRV_PORT/sub")"
check "target 错误 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SRV_PORT/sub?target=surge&url=$ENC")"
check "全部源失败 502" "502" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SRV_PORT/sub?target=clash&url=http%3A%2F%2F127.0.0.1%3A19999%2Fnope")"
check "healthz" "ok" "$(curl -s "http://127.0.0.1:$SRV_PORT/healthz")"
check "version" "1" "$(curl -s "http://127.0.0.1:$SRV_PORT/version" | grep -c mihomo)"

# 6.5 P1-1 回归：前端生成订阅链接用 URLSearchParams 单次编码（不预编码），
# 生成链接必须 200——双重编码曾导致 100% 400
QSTR=$(python3 -c "import urllib.parse,sys;print(urllib.parse.urlencode({'target':'clash','url':sys.argv[1]}))" "$SUB_URL")
check "前端链接(URLSearchParams 单次编码) /sub 200" "200" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SRV_PORT/sub?$QSTR")"

# 7. 管理台 API 全链路（webui 页面 + 订阅源 CRUD + 转换 + 日志 + 重试）
check "管理台首页 HTML 含标题" "1" "$(curl -s http://127.0.0.1:$SRV_PORT/ | grep -c '<h1>订阅转换管理台</h1>')"
check "管理台静态资源 app.js" "200" "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$SRV_PORT/app.js)"
check "sources 空列表" "1" "$(curl -s http://127.0.0.1:$SRV_PORT/api/v1/sources | grep -c '\"sources\":\[\]')"
CREATE_CODE=$(curl -s -o $WORK/create.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"name\":\"冒烟源\",\"url\":\"$SUB_URL\",\"enabled\":true}" \
  http://127.0.0.1:$SRV_PORT/api/v1/sources)
check "创建订阅源 201" "201" "$CREATE_CODE"
SRC_ID=$(python3 -c 'import json;print(json.load(open("'$WORK'/create.json"))["source"]["id"])')
[ -n "$SRC_ID" ] && check "创建返回含 id" "1" "1" || check "创建返回含 id" "1" "0"
check "更新订阅源 200" "200" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H 'Content-Type: application/json' \
  -d '{"name":"冒烟源改名"}' http://127.0.0.1:$SRV_PORT/api/v1/sources/$SRC_ID)"
PREVIEW=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"source_id\":\"$SRC_ID\"}" http://127.0.0.1:$SRV_PORT/api/v1/convert/preview)
check "preview node_count>0" "1" "$(echo "$PREVIEW" | python3 -c 'import json,sys;print(1 if json.load(sys.stdin)["node_count"]>0 else 0)')"
RUNY=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"source_id\":\"$SRC_ID\"}" http://127.0.0.1:$SRV_PORT/api/v1/convert/run)
check "run yaml 含 proxies" "1" "$(echo "$RUNY" | python3 -c 'import json,sys;print(1 if "proxies:" in json.load(sys.stdin)["yaml"] else 0)')"
check "logs ≥2 条" "1" "$(curl -s 'http://127.0.0.1:'$SRV_PORT'/api/v1/logs?limit=50' | python3 -c 'import json,sys;print(1 if len(json.load(sys.stdin)["logs"])>=2 else 0)')"
LOG_ID=$(curl -s 'http://127.0.0.1:'$SRV_PORT'/api/v1/logs?limit=1' | python3 -c 'import json,sys;print(json.load(sys.stdin)["logs"][0]["id"])')
check "log retry 200" "200" "$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  http://127.0.0.1:$SRV_PORT/api/v1/logs/$LOG_ID/retry)"
check "sub src=ID 200" "200" "$(curl -s -o $WORK/out_src.yaml -w '%{http_code}' \
  "http://127.0.0.1:$SRV_PORT/sub?target=clash&src=$SRC_ID")"
check "sub src=ID 节点数 7" "7" "$(NODES $WORK/out_src.yaml)"
check "BUG回归 url=notaurl 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' \
  'http://127.0.0.1:'$SRV_PORT'/sub?target=clash&url=notaurl')"

# 7.5 P3-17g OSC_ADMIN_TOKEN 场景：管理 API 需令牌；页面/静态资源/sub 不鉴权
TOK_PORT=$((SRV_PORT + 1))
OSC_PORT=$TOK_PORT OSC_DATA_DIR=$WORK/data_tok OSC_ADMIN_TOKEN=s3cret \
  go run ./cmd/server >$WORK/server_tok.log 2>&1 &
TOK_PID=$!
for i in $(seq 1 30); do
  if curl -s -m 1 "http://127.0.0.1:$TOK_PORT/healthz" 2>/dev/null | grep -q ok; then
    break
  fi
  sleep 1
done
if ! curl -s -m 1 "http://127.0.0.1:$TOK_PORT/healthz" | grep -q ok; then
  echo "token 服务 30s 未就绪"; cat $WORK/server_tok.log; exit 1
fi
check "token: / 页面无 token 200（页面不鉴权）" "200" "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$TOK_PORT/)"
check "token: /app.js 无 token 200（静态资源不鉴权）" "200" "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$TOK_PORT/app.js)"
check "token: /api/v1/sources 无 token 401" "401" "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$TOK_PORT/api/v1/sources)"
check "token: /api/v1/sources 带 X-Token 200" "200" "$(curl -s -o /dev/null -w '%{http_code}' -H 'X-Token: s3cret' http://127.0.0.1:$TOK_PORT/api/v1/sources)"
check "token: /sub 无 token 不 401（参数错误 400）" "400" "$(curl -s -o /dev/null -w '%{http_code}' 'http://127.0.0.1:'$TOK_PORT'/sub?target=clash')"

# 7.6 预置模板种子：全新数据目录（$WORK/data_tok 每次冒烟前 rm -rf）首次启动
# 自动种入 8 个 ACL4SSR 模板，列表 ≥8 条且含 Netflix（URL 判定，python3 JSON 解析）
TPL_SEEDED=$(curl -s -H 'X-Token: s3cret' http://127.0.0.1:$TOK_PORT/api/v1/templates | python3 -c 'import json,sys;print(1 if len(json.load(sys.stdin)["templates"])>=8 else 0)')
check "token: templates 预置种入 ≥8 条" "1" "$TPL_SEEDED"
TPL_NETFLIX=$(curl -s -H 'X-Token: s3cret' http://127.0.0.1:$TOK_PORT/api/v1/templates | python3 -c 'import json,sys;print(1 if any("Netflix" in t["url"] for t in json.load(sys.stdin)["templates"]) else 0)')
check "token: templates 含 Netflix 预置" "1" "$TPL_NETFLIX"

# 7.7 规则模板自动探测（text/domain、yaml 混合、错误路径、鉴权）
cat > $WORK/rules_domain.list <<'EOF'
DOMAIN-SUFFIX,example.com
DOMAIN-SUFFIX,google.com
DOMAIN-KEYWORD,facebook.com
DOMAIN,apple.com
DOMAIN-SUFFIX,netflix.com
DOMAIN-WILDCARD,*.youtube.com
DOMAIN-REGEX,^[a-z]+\.cdn$
EOF
cat > $WORK/rules_mixed.yaml <<'EOF'
payload:
  - DOMAIN-SUFFIX,example.com
  - IP-CIDR,1.2.3.0/24
  - DOMAIN-KEYWORD,google.com
  - GEOIP,CN
  - DOMAIN,apple.com
  - IP-CIDR6,2001:db8::/32
EOF
PROBE_DOM_CODE=$(curl -s -o $WORK/probe_dom.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"url\":\"http://127.0.0.1:$SRC_PORT/rules_domain.list\"}" \
  http://127.0.0.1:$SRV_PORT/api/v1/templates/probe)
check "probe text/domain 200" "200" "$PROBE_DOM_CODE"
PROBE_DOM_OK=$(python3 -c 'import json;print(1 if json.load(open("'$WORK'/probe_dom.json"))["format"]=="text" and json.load(open("'$WORK'/probe_dom.json"))["behavior"]=="domain" else 0)')
check "probe text/domain format=text behavior=domain" "1" "$PROBE_DOM_OK"
curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"url\":\"http://127.0.0.1:$SRC_PORT/rules_mixed.yaml\"}" \
  http://127.0.0.1:$SRV_PORT/api/v1/templates/probe > $WORK/probe_yaml.json
PROBE_YAML_FMT=$(python3 -c 'import json;print(json.load(open("'$WORK'/probe_yaml.json"))["format"])')
check "probe yaml format=yaml" "yaml" "$PROBE_YAML_FMT"
check "probe 非法 URL 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"url":"notaurl"}' http://127.0.0.1:$SRV_PORT/api/v1/templates/probe)"
check "probe 未监听端口 502" "502" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"url":"http://127.0.0.1:19999/nope"}' http://127.0.0.1:$SRV_PORT/api/v1/templates/probe)"
check "probe TOK 无 token 401" "401" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d "{\"url\":\"http://127.0.0.1:$SRC_PORT/rules_domain.list\"}" http://127.0.0.1:$TOK_PORT/api/v1/templates/probe)"
# #10a: rules_mixed.yaml 混合均匀（3 DOMAIN / 2 IP-CIDR / 1 GEOIP，各 <60%）→ classical
PROBE_YAML_BEH=$(python3 -c 'import json;print(json.load(open("'$WORK'/probe_yaml.json"))["behavior"])')
check "probe yaml behavior=classical" "classical" "$PROBE_YAML_BEH"
# #10b: TOK 实例带正确 token（本实例 OSC_ADMIN_TOKEN=s3cret）探测 200
check "probe TOK 带正确 token 200" "200" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H 'X-Token: s3cret' -d "{\"url\":\"http://127.0.0.1:$SRC_PORT/rules_domain.list\"}" http://127.0.0.1:$TOK_PORT/api/v1/templates/probe)"

# 8. mihomo 全量校验产物
mkdir -p cmd/validate_tmp
cat > cmd/validate_tmp/main.go <<EOF
package main
import ("fmt"; "os"; mihomoconfig "github.com/metacubex/mihomo/config")
func main() {
    data, err := os.ReadFile("$WORK/out1.yaml")
    if err != nil { fmt.Println("READ_FAIL:", err); os.Exit(1) }
    cfg, err := mihomoconfig.UnmarshalRawConfig(data)
    if err != nil { fmt.Println("VALIDATE_FAIL:", err); os.Exit(1) }
    fmt.Printf("VALIDATE_OK mixed-port=%d mode=%s\n", cfg.MixedPort, cfg.Mode)
}
EOF
VOUT=$(go run ./cmd/validate_tmp 2>&1 | tail -1)
rm -rf cmd/validate_tmp
check "mihomo 校验" "1" "$(echo "$VOUT" | grep -c VALIDATE_OK)"

echo "=============================="
echo "冒烟结果: PASS=$PASS FAIL=$FAIL (服务端口 $SRV_PORT 源端口 $SRC_PORT)"
echo "产物保留: $WORK"
[ $FAIL -eq 0 ] && echo "ALL GREEN" || echo "HAS FAILURES"
exit $FAIL
