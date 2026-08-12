#!/usr/bin/env bash
# P1-1(坏节点拦截) + P1-5(凭证脱敏) 回归验证
# 随机端口避免固定端口冲突; 服务启动检测失败即 abort; trap 清理 go-run 孤儿
set -e
export PATH=/opt/data/go/bin:$PATH
cd /opt/data/projects/openclash-sub-converter

SRC_PORT=$((19000 + RANDOM % 900))
SRV_PORT=$((25000 + RANDOM % 900))
WORK=/opt/data/.verify_fix
rm -rf $WORK && mkdir -p $WORK

cleanup() {
  # 杀 go-run 孤儿编译产物（精确匹配，避免 pkill -f 自杀）
  python3 - "$WORK" <<'EOF'
import os, signal, sys
me = os.getpid()
work = sys.argv[1]
for pid in os.listdir('/proc'):
    if not pid.isdigit() or int(pid) == me:
        continue
    try:
        cmdline = open(f'/proc/{pid}/cmdline').read().replace('\0', ' ')
    except OSError:
        continue
    if 'go-build' in cmdline and cmdline.rstrip().endswith('/server'):
        try: os.kill(int(pid), signal.SIGKILL)
        except OSError: pass
for pid in os.listdir('/proc'):
    if not pid.isdigit() or int(pid) == me:
        continue
    try:
        cmdline = open(f'/proc/{pid}/cmdline').read().replace('\0', ' ')
    except OSError:
        continue
    if 'http.server' in cmdline and work in cmdline:
        try: os.kill(int(pid), signal.SIGKILL)
        except OSError: pass
EOF
}
trap cleanup EXIT

# 3 节点: 1 合法 ss + 1 坏 reality vless(假pbk) + 1 缺 password trojan
cat > $WORK/sub.txt <<'EOF'
ss://YWVzLTI1Ni1nY206cGFzc3dvcmQxMjM@jp1.example.com:8388#✅ 合法节点
vless://abcdef1234567890@us1.example.com:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=REALITY_PUBLIC_KEY_BASE64_32BYTES&sid=abcd1234&type=tcp#❌ 坏reality
trojan://@sg1.example.com:443?sni=sg.example.com#❌ 缺password
EOF
base64 -w0 $WORK/sub.txt > $WORK/sub.b64
python3 -m http.server $SRC_PORT --bind 127.0.0.1 --directory $WORK >/dev/null 2>&1 &
HTTP_PID=$!

export OSC_PORT=$SRV_PORT OSC_LOG_LEVEL=info
go run ./cmd/server >$WORK/server.log 2>&1 &
SRV_PID=$!

# 启动检测: healthz 轮询 + server.log 非空（bind 失败会立刻出现在日志）
READY=0
for i in $(seq 1 25); do
  if curl -s -m 1 http://127.0.0.1:$SRV_PORT/healthz 2>/dev/null | grep -q ok; then READY=1; break; fi
  sleep 1
done
if [ "$READY" != "1" ]; then
  echo "❌ 服务启动失败"; cat $WORK/server.log; exit 1
fi

ENC="http%3A%2F%2F127.0.0.1%3A$SRC_PORT%2Fsub.b64"
echo "=== P1-1: 混合订阅(1好+2坏) 转换结果 ==="
curl -s -w "\nHTTP=%{http_code}\n" "http://127.0.0.1:$SRV_PORT/sub?target=clash&url=$ENC" | awk '/^proxies:/{f=1;next} /^[a-z][a-z0-9-]*:/{if(f)exit} f' | grep ' name:'
echo "=== 服务日志 WARN(坏节点跳过) ==="
grep 'skip invalid proxy node' $WORK/server.log | head -4
grep -c 'skip invalid proxy node' $WORK/server.log

echo ""
echo "=== P1-5: 含凭证 URL 错误路径 ==="
curl -s "http://127.0.0.1:$SRV_PORT/sub?target=clash&url=http%3A%2F%2Fuser%3Apass%40127.0.0.1%3A19999%2Fsub%3Ftoken%3DSECRET123" -w "\nHTTP=%{http_code}\n"
echo "--- 日志检查(不得出现 user:pass / SECRET123 / 完整URL) ---"
if grep -qE 'user:pass|SECRET123|sub\?token' $WORK/server.log; then echo "❌ 凭证泄露!"; grep -E 'user:pass|SECRET123' $WORK/server.log | head -3; exit 1; else echo "✅ 日志无凭证"; fi
echo "DONE"
