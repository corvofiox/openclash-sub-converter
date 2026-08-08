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
  [ -n "$HTTP_PID" ] && kill $HTTP_PID 2>/dev/null
  # go run 孤儿子进程
  for pid in $(ls /proc 2>/dev/null | grep -E '^[0-9]+$'); do
    cmdline=$(tr '\0' ' ' < /proc/$pid/cmdline 2>/dev/null | head -c 100)
    echo "$cmdline" | grep -q "go-build.*/server" && kill -9 $pid 2>/dev/null || true
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
export OSC_PORT=$SRV_PORT OSC_LOG_LEVEL=info
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

# 6. 错误路径
check "缺参数 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SRV_PORT/sub")"
check "target 错误 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SRV_PORT/sub?target=surge&url=$ENC")"
check "全部源失败 502" "502" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SRV_PORT/sub?target=clash&url=http%3A%2F%2F127.0.0.1%3A19999%2Fnope")"
check "healthz" "ok" "$(curl -s "http://127.0.0.1:$SRV_PORT/healthz")"
check "version" "1" "$(curl -s "http://127.0.0.1:$SRV_PORT/version" | grep -c mihomo)"

# 7. mihomo 全量校验产物
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
