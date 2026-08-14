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
      # Go 1.26 go run 的临时构建产物在 /tmp/go-build*/b001/exe/server（readlink exe
      # 精确匹配，与 .cache 分支同规——勿放宽为 cmdline 文本/pkill 宽匹配）
      */go-build*/*/exe/server) kill -9 $pid 2>/dev/null || true ;;
    esac
  done
  rm -rf cmd/validate_tmp  # P2-4: set -e 校验失败提前退出也不残留 main.go
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
# 节点计数: 匹配 " name:" 行(排除 servername:), 覆盖 2/4 空格缩进。
# awk 按顶层键切段（段序无关）：命中起始键置 f，下一个顶层键即段尾——
# 新段序 proxy-groups 在 proxies 前，sed 相邻范围不再可靠。
NODES() { awk '/^proxies:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' "$1" | grep -c ' name:'; }
GROUPS() { awk '/^proxy-groups:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' "$1" | grep -c ' name:'; }

# 4. 基础转换
curl -s -o $WORK/out1.yaml -w "%{http_code}" "$BASE" > $WORK/code.txt
check "基础转换 HTTP 200" "200" "$(cat $WORK/code.txt)"
check "节点数 7" "7" "$(NODES $WORK/out1.yaml)"
check "组数 10（含直连/拒绝）" "10" "$(GROUPS $WORK/out1.yaml)"
# R3 段序契约：proxy-groups 在 proxies 前（mihomo 对段落顺序无要求，固定顺序保证产物确定性）
PG_LN=$(grep -n '^proxy-groups:' $WORK/out1.yaml | cut -d: -f1)
PX_LN=$(grep -n '^proxies:' $WORK/out1.yaml | cut -d: -f1)
check "段序 proxy-groups 在 proxies 前" "1" "$([ -n "$PG_LN" ] && [ -n "$PX_LN" ] && [ "$PG_LN" -lt "$PX_LN" ] && echo 1 || echo 0)"
check "手动选择组存在" "1" "$(awk '/^proxy-groups:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' $WORK/out1.yaml | grep -c '手动选择')"
check "vless reality 节点" "1" "$(grep -c 'reality-opts' $WORK/out1.yaml)"

# 4.1 R1/R2 策略组结构综合断言（计数式，不依赖输入魔数）：
# - 直连/拒绝组存在且 proxies 恰为 [DIRECT]/[REJECT]（R1）
# - 手动组顺序 = 自动选择 → 地区组名（出现序）→ 其他节点组（若有）→
#   全部节点名（输入序）→ 直连 → 拒绝；引用计数与期望一致（R2）
# - 组数 = 手动/自动/直连/拒绝 + 地区组数 + 其他节点组（若有）
cat > $WORK/check_groups.py <<'PYEOF'
import sys

path = sys.argv[1]

def section(lines, key):
    out = []
    f = False
    for l in lines:
        if l.startswith(key + ':'):
            f = True
            continue
        if f:
            if l and not l[0].isspace():
                break
            if l.strip():
                out.append(l)
    return out

def unquote(v):
    if len(v) >= 2 and v[0] == '"' and v[-1] == '"':
        return v[1:-1]
    return v

lines = open(path, encoding='utf-8').read().splitlines()
pg = section(lines, 'proxy-groups')
px = section(lines, 'proxies')
# 组条目行键序不定（interval 可能在 name 前），按缩进层级状态机解析
order, by_name = [], {}
cur, in_p = None, False
for l in pg:
    if l.startswith('  - '):            # 新组条目（2 空格）
        cur = None
        in_p = False
        body = l[4:]
        if body.startswith('name: '):
            cur = unquote(body[6:].strip())
            order.append(cur)
            by_name[cur] = []
    elif cur is None and l.startswith('    name: '):   # 组条目行的 name 键
        cur = unquote(l[10:].strip())
        order.append(cur)
        by_name[cur] = []
    elif cur is not None and l.startswith('    proxies:'):
        in_p = True
    elif in_p and cur is not None and l.startswith('      - '):
        by_name[cur].append(unquote(l.strip()[2:].strip()))
    else:
        in_p = False
node_names = []
for l in px:
    if l.startswith('  - name: '):      # 节点条目首行即 name 键
        node_names.append(unquote(l[9:].strip()))
    elif l.startswith('    name: '):    # 节点条目内 name 键（4 空格）
        node_names.append(unquote(l[10:].strip()))

fixed = ('手动选择', '自动选择', '直连', '拒绝', '其他节点')
region_names = [g for g in order if g not in fixed]
other = '其他节点' in order
expect_manual = ['自动选择'] + region_names + (['其他节点'] if other else []) + node_names + ['直连', '拒绝']
print('direct=%d' % (1 if by_name.get('直连') == ['DIRECT'] else 0))
print('reject=%d' % (1 if by_name.get('拒绝') == ['REJECT'] else 0))
print('manual_order=%d' % (1 if by_name.get('手动选择') == expect_manual else 0))
print('manual_count=%d' % (1 if len(by_name.get('手动选择', [])) == len(expect_manual) else 0))
print('group_count=%d' % (1 if len(order) == 4 + len(region_names) + (1 if other else 0) else 0))
# R3：附加组名检查（argv[2]）——专属组 proxies = [手动选择, ...手动选择组 proxies]
if len(sys.argv) > 2:
    extra = sys.argv[2]
    e = by_name.get(extra) or []
    m = by_name.get('手动选择') or []
    print('extra=%d' % (1 if len(e) == len(m)+1 and e[:1] == ['手动选择'] and e[1:] == m else 0))
PYEOF
GCHK=$(python3 $WORK/check_groups.py $WORK/out1.yaml)
check "R1 直连组存在且 proxies=[DIRECT]" "1" "$(echo "$GCHK" | grep '^direct=' | cut -d= -f2)"
check "R1 拒绝组存在且 proxies=[REJECT]" "1" "$(echo "$GCHK" | grep '^reject=' | cut -d= -f2)"
check "R1 组数=4+地区组数+其他组(计数式)" "1" "$(echo "$GCHK" | grep '^group_count=' | cut -d= -f2)"
check "R2 手动组顺序(自动→地区→节点→直连→拒绝)" "1" "$(echo "$GCHK" | grep '^manual_order=' | cut -d= -f2)"
check "R2 手动组引用计数=地区+节点+4(计数式)" "1" "$(echo "$GCHK" | grep '^manual_count=' | cut -d= -f2)"

# 4.2 R2 失败源告警三通道：1 好源 + 1 坏源 → YAML 注释 + X-Osc-Warning 头 + JSON warnings
ENC2=$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1],safe=''))" "$SUB_URL|http://127.0.0.1:19999/nope")
curl -s -D $WORK/warn_hdr.txt -o $WORK/out_warn.yaml -w "%{http_code}" "http://127.0.0.1:$SRV_PORT/sub?target=clash&url=$ENC2" > $WORK/code2.txt
check "R2 部分失败 200" "200" "$(cat $WORK/code2.txt)"
check "R2 YAML 含 OSC-WARNING 注释" "1" "$(grep -c '# \[OSC-WARNING\]' $WORK/out_warn.yaml)"
check "R2 注释含失败 host" "1" "$(grep -c '127.0.0.1:19999' $WORK/out_warn.yaml)"
check "R2 X-Osc-Warning 响应头" "1" "$(grep -ci 'x-osc-warning' $WORK/warn_hdr.txt)"
check "R2 正常源节点保留" "7" "$(NODES $WORK/out_warn.yaml)"
# 全成功 → 无注释、无头（字节级兼容）
curl -s -D $WORK/ok_hdr.txt -o $WORK/out_ok2.yaml "$BASE"
check "R2 无失败无注释" "0" "$(grep -c '# \[OSC-WARNING\]' $WORK/out_ok2.yaml)"
check "R2 无失败无告警头" "0" "$(grep -ci 'x-osc-warning' $WORK/ok_hdr.txt)"
# convert preview JSON warnings 字段
WARN_JSON=$(curl -s -X POST -H 'Content-Type: application/json' -d "{\"url\":\"$SUB_URL|http://127.0.0.1:19999/nope\"}" http://127.0.0.1:$SRV_PORT/api/v1/convert/preview)
check "R2 preview warnings 含坏源" "1" "$(echo "$WARN_JSON" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(1 if any("127.0.0.1:19999" in w for w in d.get("warnings",[])) else 0)')"

# 5. 参数
check "include=日本 剩2节点" "2" "$(curl -s "$BASE&include=%E6%97%A5%E6%9C%AC" | awk '/^proxies:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' | grep -c ' name:')"
check "exclude=日本 剩5节点" "5" "$(curl -s "$BASE&exclude=%E6%97%A5%E6%9C%AC" | awk '/^proxies:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' | grep -c ' name:')"
check "rename 生效(2节点×4处)" "8" "$(curl -s "$BASE&rename=%E6%97%A5%E6%9C%AC/JP" | grep -c 'JP0')"
# R1 rename 多规则（逗号分隔顺序执行）：日本→JP 与 香港→HK 各自命中（计数 ≥1）
check "rename 多规则 JP0≥1" "1" "$([ "$(curl -s "$BASE&rename=%E6%97%A5%E6%9C%AC/JP,%E9%A6%99%E6%B8%AF/HK" | grep -c 'JP0')" -ge 1 ] && echo 1 || echo 0)"
check "rename 多规则 HK0≥1" "1" "$([ "$(curl -s "$BASE&rename=%E6%97%A5%E6%9C%AC/JP,%E9%A6%99%E6%B8%AF/HK" | grep -c 'HK0')" -ge 1 ] && echo 1 || echo 0)"
check "scv=true 5节点有scv" "5" "$(curl -s "$BASE&scv=true" | grep -c 'skip-cert-verify: true')"
# 5.5 strip_emoji：yaml.v3 对补充平面字符（emoji）输出 \U0001Fxxx 转义，
# grep 用转义序列（旗标均在 U+1F1E6-1F1FF，前缀 \U0001F1）
# 基线：默认（开关关）proxies 段含 7 处旗标转义（证明 grep 模式有效）
check "默认节点名旗标保留(基线)" "7" "$(curl -s "$BASE" | awk '/^proxies:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' | grep -c '\\U0001F1')"
check "strip_emoji 剥离旗标emoji" "0" "$(curl -s "$BASE&strip_emoji=true" | awk '/^proxies:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' | grep -c '\\U0001F1')"
check "strip_emoji 节点数不变" "7" "$(curl -s "$BASE&strip_emoji=true" | awk '/^proxies:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' | grep -c ' name:')"
check "strip_emoji 组名无emoji且存在" "1" "$(curl -s "$BASE&strip_emoji=true" | awk '/^proxy-groups:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' | grep -c -- 'name: 香港节点')"

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

# 7.6 预置规则集种子：全新数据目录（$WORK/data_tok 每次冒烟前 rm -rf）首次启动
# 自动种入 8 个 ACL4SSR 规则集，列表 ≥8 条且含 Netflix（URL 判定，python3 JSON 解析）
TPL_SEEDED=$(curl -s -H 'X-Token: s3cret' http://127.0.0.1:$TOK_PORT/api/v1/rule-sets | python3 -c 'import json,sys;print(1 if len(json.load(sys.stdin)["rule_sets"])>=8 else 0)')
check "token: rule_sets 预置种入 ≥8 条" "1" "$TPL_SEEDED"
TPL_NETFLIX=$(curl -s -H 'X-Token: s3cret' http://127.0.0.1:$TOK_PORT/api/v1/rule-sets | python3 -c 'import json,sys;print(1 if any("Netflix" in t["url"] for t in json.load(sys.stdin)["rule_sets"]) else 0)')
check "token: rule_sets 含 Netflix 预置" "1" "$TPL_NETFLIX"

# 7.7 规则集自动探测（text/domain、yaml 混合、错误路径、鉴权）
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
  http://127.0.0.1:$SRV_PORT/api/v1/rule-sets/probe)
check "probe text/domain 200" "200" "$PROBE_DOM_CODE"
PROBE_DOM_OK=$(python3 -c 'import json;print(1 if json.load(open("'$WORK'/probe_dom.json"))["format"]=="text" and json.load(open("'$WORK'/probe_dom.json"))["behavior"]=="domain" else 0)')
check "probe text/domain format=text behavior=domain" "1" "$PROBE_DOM_OK"
curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"url\":\"http://127.0.0.1:$SRC_PORT/rules_mixed.yaml\"}" \
  http://127.0.0.1:$SRV_PORT/api/v1/rule-sets/probe > $WORK/probe_yaml.json
PROBE_YAML_FMT=$(python3 -c 'import json;print(json.load(open("'$WORK'/probe_yaml.json"))["format"])')
check "probe yaml format=yaml" "yaml" "$PROBE_YAML_FMT"
check "probe 非法 URL 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"url":"notaurl"}' http://127.0.0.1:$SRV_PORT/api/v1/rule-sets/probe)"
check "probe 未监听端口 502" "502" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"url":"http://127.0.0.1:19999/nope"}' http://127.0.0.1:$SRV_PORT/api/v1/rule-sets/probe)"
check "probe TOK 无 token 401" "401" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d "{\"url\":\"http://127.0.0.1:$SRC_PORT/rules_domain.list\"}" http://127.0.0.1:$TOK_PORT/api/v1/rule-sets/probe)"
# #10a: rules_mixed.yaml 混合均匀（3 DOMAIN / 2 IP-CIDR / 1 GEOIP，各 <60%）→ classical
PROBE_YAML_BEH=$(python3 -c 'import json;print(json.load(open("'$WORK'/probe_yaml.json"))["behavior"])')
check "probe yaml behavior=classical" "classical" "$PROBE_YAML_BEH"
# #10b: TOK 实例带正确 token（本实例 OSC_ADMIN_TOKEN=s3cret）探测 200
check "probe TOK 带正确 token 200" "200" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H 'X-Token: s3cret' -d "{\"url\":\"http://127.0.0.1:$SRC_PORT/rules_domain.list\"}" http://127.0.0.1:$TOK_PORT/api/v1/rule-sets/probe)"

# 7.8 R3/R4 规则集→专属策略组：/sub ruleset_id 单规则集/双规则集/disabled/非法
TPL1_CODE=$(curl -s -o $WORK/tpl1.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"name\":\"Netflix\",\"url\":\"http://127.0.0.1:$SRC_PORT/rules_domain.list\",\"behavior\":\"domain\",\"format\":\"text\",\"enabled\":true}" \
  http://127.0.0.1:$SRV_PORT/api/v1/rule-sets)
check "创建规则集1 Netflix 201" "201" "$TPL1_CODE"
TPL1=$(python3 -c 'import json;print(json.load(open("'$WORK'/tpl1.json"))["rule_set"]["id"])')
TPL2_CODE=$(curl -s -o $WORK/tpl2.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"name\":\"YouTube\",\"url\":\"http://127.0.0.1:$SRC_PORT/rules_mixed.yaml\",\"behavior\":\"classical\",\"format\":\"yaml\",\"enabled\":true}" \
  http://127.0.0.1:$SRV_PORT/api/v1/rule-sets)
check "创建规则集2 YouTube 201" "201" "$TPL2_CODE"
TPL2=$(python3 -c 'import json;print(json.load(open("'$WORK'/tpl2.json"))["rule_set"]["id"])')

# 单规则集：专属组（proxies=[手动选择,...手动选择组]）+ RULE-SET,Netflix,Netflix 在列表最前（GEOIP/MATCH 前）+ rule-providers 段
curl -s -o $WORK/out_tpl1.yaml -w "%{http_code}" "$BASE&ruleset_id=$TPL1" > $WORK/code_tpl1.txt
check "R3 单规则集 /sub 200" "200" "$(cat $WORK/code_tpl1.txt)"
check "R3 单规则集 rule-providers 段" "1" "$(grep -c '^rule-providers:' $WORK/out_tpl1.yaml)"
check "R3 专属组 Netflix 存在" "1" "$(awk '/^proxy-groups:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' $WORK/out_tpl1.yaml | grep -c -- '- name: Netflix')"
check "R3 专属组 proxies=[手动选择,...手动组]" "1" "$(python3 $WORK/check_groups.py $WORK/out_tpl1.yaml Netflix | grep '^extra=' | cut -d= -f2)"
RS_LN=$(grep -n -- '- RULE-SET,Netflix,Netflix' $WORK/out_tpl1.yaml | cut -d: -f1 | head -1)
GEO_LN=$(grep -n -- '- GEOIP,CN,DIRECT' $WORK/out_tpl1.yaml | cut -d: -f1 | head -1)
MCH_LN=$(grep -n -- '- MATCH,手动选择' $WORK/out_tpl1.yaml | cut -d: -f1 | head -1)
check "R3 RULE-SET,Netflix,Netflix 在 GEOIP/MATCH 前" "1" "$([ -n "$RS_LN" ] && [ -n "$GEO_LN" ] && [ -n "$MCH_LN" ] && [ "$RS_LN" -lt "$GEO_LN" ] && [ "$RS_LN" -lt "$MCH_LN" ] && echo 1 || echo 0)"

# 双规则集（逗号分隔）：两个专属组 + 两条 RULE-SET + rule-providers 2 条
curl -s -o $WORK/out_tpl2.yaml -w "%{http_code}" "$BASE&ruleset_id=$TPL1,$TPL2" > $WORK/code_tpl2.txt
check "R4 双规则集 /sub 200" "200" "$(cat $WORK/code_tpl2.txt)"
check "R4 专属组 Netflix 存在" "1" "$(awk '/^proxy-groups:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' $WORK/out_tpl2.yaml | grep -c -- '- name: Netflix')"
check "R4 专属组 YouTube 存在" "1" "$(awk '/^proxy-groups:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' $WORK/out_tpl2.yaml | grep -c -- '- name: YouTube')"
check "R4 rule-providers 2 条" "2" "$(grep -c 'path: ./ruleset/' $WORK/out_tpl2.yaml)"
check "R4 RULE-SET 2 条" "2" "$(grep -c -- '- RULE-SET,' $WORK/out_tpl2.yaml)"
check "R4 专属组 proxies=[手动选择,...手动组] Netflix" "1" "$(python3 $WORK/check_groups.py $WORK/out_tpl2.yaml Netflix | grep '^extra=' | cut -d= -f2)"
check "R4 专属组 proxies=[手动选择,...手动组] YouTube" "1" "$(python3 $WORK/check_groups.py $WORK/out_tpl2.yaml YouTube | grep '^extra=' | cut -d= -f2)"

# 7.9 修复轮回归：同名规则集 400（P1-2）/ 规则集名撞内置出站名（P1-1）/
# 重复 ruleset_id 去重（P2-2）/ 规则集名含逗号换行 400（P2-1）
TPL3_CODE=$(curl -s -o $WORK/tpl3.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"name":"Netflix","url":"http://127.0.0.1:'$SRC_PORT'/rules_domain.list","behavior":"domain","format":"text","enabled":true}' \
  http://127.0.0.1:$SRV_PORT/api/v1/rule-sets)
check "创建同名规则集3 Netflix 201（store 允许同名）" "201" "$TPL3_CODE"
TPL3=$(python3 -c 'import json;print(json.load(open("'$WORK'/tpl3.json"))["rule_set"]["id"])')
check "P1-2 同名规则集 /sub 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' "$BASE&ruleset_id=$TPL1,$TPL3")"
TPL4_CODE=$(curl -s -o $WORK/tpl4.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"name":"DIRECT","url":"http://127.0.0.1:'$SRC_PORT'/rules_domain.list","behavior":"domain","format":"text","enabled":true}' \
  http://127.0.0.1:$SRV_PORT/api/v1/rule-sets)
check "创建规则集4 DIRECT 201" "201" "$TPL4_CODE"
TPL4=$(python3 -c 'import json;print(json.load(open("'$WORK'/tpl4.json"))["rule_set"]["id"])')
curl -s -o $WORK/out_tpl4.yaml -w "%{http_code}" "$BASE&ruleset_id=$TPL4" > $WORK/code_tpl4.txt
check "P1-1 规则集名撞内置 DIRECT /sub 200" "200" "$(cat $WORK/code_tpl4.txt)"
check "P1-1 专属组 DIRECT(规则集) 存在" "1" "$(awk '/^proxy-groups:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' $WORK/out_tpl4.yaml | grep -c -- '- name: DIRECT(规则集)')"
check "P1-1 RULE-SET,DIRECT,DIRECT(规则集)" "1" "$(grep -c -- '- RULE-SET,DIRECT,DIRECT(规则集)' $WORK/out_tpl4.yaml)"
curl -s -o $WORK/out_dup.yaml -w "%{http_code}" "$BASE&ruleset_id=$TPL1,$TPL1" > $WORK/code_dup.txt
check "P2-2 重复 ruleset_id /sub 200" "200" "$(cat $WORK/code_dup.txt)"
check "P2-2 重复 id 去重 rule-providers 1 条" "1" "$(grep -c 'path: ./ruleset/' $WORK/out_dup.yaml)"
check "P2-1 规则集名含逗号 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"name":"Netflix,cn","url":"http://127.0.0.1:'$SRC_PORT'/rules_domain.list","behavior":"domain","format":"text"}' http://127.0.0.1:$SRV_PORT/api/v1/rule-sets)"
check "P2-1 规则集名含换行 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"name":"a\nb","url":"http://127.0.0.1:'$SRC_PORT'/rules_domain.list","behavior":"domain","format":"text"}' http://127.0.0.1:$SRV_PORT/api/v1/rule-sets)"


# 7.10 R4 数据源多选聚合：src 逗号多值 / 无效 ID 400 / src+url 混合 /
# convert source_ids 系列 / 日志逗号多值 / retry 多源恢复
SRC2_CODE=$(curl -s -o $WORK/create2.json -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"name\":\"冒烟源2\",\"url\":\"$SUB_URL\",\"enabled\":true}" \
  http://127.0.0.1:$SRV_PORT/api/v1/sources)
check "R4 创建订阅源2 201" "201" "$SRC2_CODE"
SRC2_ID=$(python3 -c 'import json;print(json.load(open("'$WORK'/create2.json"))["source"]["id"])')
check "R4 src=ID1,ID2 聚合 200" "200" "$(curl -s -o $WORK/out_src2.yaml -w '%{http_code}' \
  "http://127.0.0.1:$SRV_PORT/sub?target=clash&src=$SRC_ID,$SRC2_ID")"
check "R4 src 多值节点数 14" "14" "$(NODES $WORK/out_src2.yaml)"
check "R4 src 无效 ID 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' \
  "http://127.0.0.1:$SRV_PORT/sub?target=clash&src=$SRC_ID,deadbeef0000")"
check "R4 src 纯逗号 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' \
  "http://127.0.0.1:$SRV_PORT/sub?target=clash&src=%2C%2C")"
curl -s -o $WORK/out_mix.yaml -w "%{http_code}" \
  "http://127.0.0.1:$SRV_PORT/sub?target=clash&src=$SRC_ID,$SRC2_ID&url=$ENC" > $WORK/code_mix.txt
check "R4 src+url 混合 200" "200" "$(cat $WORK/code_mix.txt)"
check "R4 src+url 混合节点数 21" "21" "$(NODES $WORK/out_mix.yaml)"
SRCIDS_JSON=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"source_ids\":[\"$SRC_ID\",\"$SRC2_ID\"]}" http://127.0.0.1:$SRV_PORT/api/v1/convert/preview)
check "R4 convert source_ids 聚合 node_count 14" "14" "$(echo "$SRCIDS_JSON" | python3 -c 'import json,sys;print(json.load(sys.stdin)["node_count"])')"
check "R4 日志 source_id 逗号多值" "1" "$(curl -s 'http://127.0.0.1:'$SRV_PORT'/api/v1/logs?limit=1' | python3 -c "import json,sys;print(1 if json.load(sys.stdin)['logs'][0]['source_id']=='$SRC_ID,$SRC2_ID' else 0)")"
MS_LOG_ID=$(curl -s 'http://127.0.0.1:'$SRV_PORT'/api/v1/logs?limit=1' | python3 -c 'import json,sys;print(json.load(sys.stdin)["logs"][0]["id"])')
check "R4 retry 多源日志 200" "200" "$(curl -s -o $WORK/retry_ms.json -w '%{http_code}' -X POST \
  http://127.0.0.1:$SRV_PORT/api/v1/logs/$MS_LOG_ID/retry)"
check "R4 retry 多源 node_count 14" "14" "$(python3 -c 'import json;print(json.load(open("'$WORK'/retry_ms.json"))["node_count"])')"
check "R4 convert source_id+source_ids 并存 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"source_id\":\"$SRC_ID\",\"source_ids\":[\"$SRC2_ID\"]}" http://127.0.0.1:$SRV_PORT/api/v1/convert/preview)"
check "R4 convert 全空 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{}' http://127.0.0.1:$SRV_PORT/api/v1/convert/preview)"
MIX_JSON=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"source_ids\":[\"$SRC_ID\"],\"url\":\"$SUB_URL\"}" http://127.0.0.1:$SRV_PORT/api/v1/convert/preview)
check "R4 convert source_ids+url 混合 node_count 14" "14" "$(echo "$MIX_JSON" | python3 -c 'import json,sys;print(json.load(sys.stdin)["node_count"])')"
# disabled / 不存在 / 多值任一非法 → 400；无规则集输出无 rule-providers 段（A7）
curl -s -o /dev/null -w '%{http_code}' -X PUT -H 'Content-Type: application/json' \
  -d '{"enabled":false}' http://127.0.0.1:$SRV_PORT/api/v1/rule-sets/$TPL1 > $WORK/code_dis.txt
check "禁用规则集1 200" "200" "$(cat $WORK/code_dis.txt)"
check "R4 disabled 规则集 /sub 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' "$BASE&ruleset_id=$TPL1")"
check "R4 不存在规则集 /sub 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' "$BASE&ruleset_id=deadbeef0000")"
check "R4 多值含非法 /sub 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' "$BASE&ruleset_id=$TPL2,deadbeef0000")"
check "R3 无规则集无 rule-providers 段(A7)" "0" "$(grep -c '^rule-providers:' $WORK/out1.yaml)"

# 8. mihomo 全量校验产物（无规则集 / 单规则集 / 双规则集）
# P2-4: set -e 下校验命令失败会提前退出——先清旧残留再建目录，cleanup trap 兜底，
# 防止 cmd/validate_tmp 残留 main.go 污染仓库（后续 go build ./... 会当包编译）。
rm -rf cmd/validate_tmp
mkdir -p cmd/validate_tmp
cat > cmd/validate_tmp/main.go <<EOF
package main
import ("fmt"; "os"; mihomoconfig "github.com/metacubex/mihomo/config")
func main() {
    for _, p := range os.Args[1:] {
        data, err := os.ReadFile(p)
        if err != nil { fmt.Println("READ_FAIL:", err); os.Exit(1) }
        cfg, err := mihomoconfig.UnmarshalRawConfig(data)
        if err != nil { fmt.Println("VALIDATE_FAIL:", err); os.Exit(1) }
        fmt.Printf("VALIDATE_OK %s mixed-port=%d mode=%s\n", p, cfg.MixedPort, cfg.Mode)
    }
}
EOF
VOUT=$(go run ./cmd/validate_tmp $WORK/out1.yaml $WORK/out_tpl1.yaml $WORK/out_tpl2.yaml 2>&1)
rm -rf cmd/validate_tmp
check "mihomo 校验 3 产物" "3" "$(echo "$VOUT" | grep -c VALIDATE_OK)"

echo "=============================="
echo "冒烟结果: PASS=$PASS FAIL=$FAIL (服务端口 $SRV_PORT 源端口 $SRC_PORT)"
echo "产物保留: $WORK"
[ $FAIL -eq 0 ] && echo "ALL GREEN" || echo "HAS FAILURES"
exit $FAIL
