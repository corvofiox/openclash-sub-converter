#!/usr/bin/env bash
# 独立验证 P1-1(坏节点拦截) + P1-5(凭证脱敏) — 不依赖 Coder 自报
set -e
export PATH=/opt/data/go/bin:$PATH
cd /opt/data/projects/openclash-sub-converter
WORK=/opt/data/.verify_fix
rm -rf $WORK && mkdir -p $WORK

# 3 节点: 1 合法 ss + 1 坏 reality vless(假pbk) + 1 缺 password trojan
cat > $WORK/sub.txt <<'EOF'
ss://YWVzLTI1Ni1nY206cGFzc3dvcmQxMjM@jp1.example.com:8388#✅ 合法节点
vless://abcdef1234567890@us1.example.com:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=REALITY_PUBLIC_KEY_BASE64_32BYTES&sid=abcd1234&type=tcp#❌ 坏reality
trojan://@sg1.example.com:443?sni=sg.example.com#❌ 缺password
EOF
base64 -w0 $WORK/sub.txt > $WORK/sub.b64
python3 -m http.server 19155 --bind 127.0.0.1 --directory $WORK >/dev/null 2>&1 &
HTTP_PID=$!
export OSC_PORT=25355 OSC_LOG_LEVEL=info
go run ./cmd/server >$WORK/server.log 2>&1 &
SRV_PID=$!
for i in $(seq 1 25); do curl -s -m 1 http://127.0.0.1:25355/healthz 2>/dev/null | grep -q ok && break; sleep 1; done

ENC="http%3A%2F%2F127.0.0.1%3A19155%2Fsub.b64"
echo "=== P1-1: 混合订阅(1好+2坏) 转换结果 ==="
curl -s -w "\nHTTP=%{http_code}\n" "http://127.0.0.1:25355/sub?target=clash&url=$ENC" | sed -n '/^proxies:/,/^proxy-groups:/p' | grep ' name:'
echo "=== 服务日志 WARN(坏节点跳过) ==="
grep 'skip invalid proxy node' $WORK/server.log | head -4

echo ""
echo "=== P1-5: 含凭证 URL 错误路径 ==="
curl -s "http://127.0.0.1:25355/sub?target=clash&url=http%3A%2F%2Fuser%3Apass%40127.0.0.1%3A19999%2Fsub%3Ftoken%3DSECRET123" -w "\nHTTP=%{http_code}\n"
echo "--- 日志检查(不得出现 user:pass / SECRET123 / 完整URL) ---"
if grep -qE 'user:pass|SECRET123|sub\?token' $WORK/server.log; then echo "❌ 凭证泄露!"; grep -E 'user:pass|SECRET123' $WORK/server.log | head -3; else echo "✅ 日志无凭证"; fi

kill $SRV_PID $HTTP_PID 2>/dev/null
echo "DONE"
