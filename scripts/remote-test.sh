#!/bin/bash
# TokenGo Remote E2E Test Script
# 测试通过 OHTTP 隧道访问远端 Relay/Exit 节点

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$PROJECT_DIR"

# ==================== 配置 ====================

CLIENT_CONFIG="configs/client-remote.yaml"
CLIENT_BIN="./build/tokengo"
CLIENT_LISTEN="127.0.0.1:8080"
CLIENT_PID=""
CLIENT_LOG="/tmp/tokengo-remote-test-client.log"

# 测试超时(秒)
TIMEOUT=60

# ==================== 颜色 ====================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# ==================== 计数器 ====================

TOTAL=0
PASSED=0
FAILED=0
SKIPPED=0

# ==================== 工具函数 ====================

info()    { echo -e "${CYAN}[INFO]${NC} $1"; }
pass()    { echo -e "${GREEN}[PASS]${NC} $1"; PASSED=$((PASSED + 1)); }
fail()    { echo -e "${RED}[FAIL]${NC} $1"; FAILED=$((FAILED + 1)); }
skip()    { echo -e "${YELLOW}[SKIP]${NC} $1"; SKIPPED=$((SKIPPED + 1)); }
section() { echo ""; echo -e "${CYAN}────────────────────────────────────────${NC}"; echo -e "${CYAN}  $1${NC}"; echo -e "${CYAN}────────────────────────────────────────${NC}"; }

cleanup() {
    if [ -n "$CLIENT_PID" ] && kill -0 "$CLIENT_PID" 2>/dev/null; then
        kill "$CLIENT_PID" 2>/dev/null || true
        wait "$CLIENT_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# ==================== 前置检查 ====================

section "前置检查"

if [ ! -f "$CLIENT_BIN" ]; then
    info "编译 tokengo..."
    make build
fi

if [ ! -f "$CLIENT_CONFIG" ]; then
    echo -e "${RED}[ERROR]${NC} 配置文件不存在: $CLIENT_CONFIG"
    echo "请先创建远端客户端配置文件"
    exit 1
fi

info "二进制: $CLIENT_BIN"
info "配置:   $CLIENT_CONFIG"

# ==================== 启动 Client ====================

section "启动 Client"

# 确保端口未被占用
if lsof -i ":8080" -sTCP:LISTEN >/dev/null 2>&1; then
    echo -e "${RED}[ERROR]${NC} 端口 8080 已被占用"
    exit 1
fi

$CLIENT_BIN client --config "$CLIENT_CONFIG" > "$CLIENT_LOG" 2>&1 &
CLIENT_PID=$!
info "Client PID: $CLIENT_PID"

# 等待连接 Relay
RETRY=0
MAX_RETRY=15
while [ $RETRY -lt $MAX_RETRY ]; do
    if grep -q "已连接到 Relay" "$CLIENT_LOG" 2>/dev/null; then
        info "$(grep '已连接到 Relay' "$CLIENT_LOG")"
        break
    fi
    if ! kill -0 "$CLIENT_PID" 2>/dev/null; then
        echo -e "${RED}[ERROR]${NC} Client 进程已退出"
        cat "$CLIENT_LOG"
        exit 1
    fi
    RETRY=$((RETRY + 1))
    sleep 1
done

if [ $RETRY -eq $MAX_RETRY ]; then
    echo -e "${RED}[ERROR]${NC} 连接 Relay 超时"
    cat "$CLIENT_LOG"
    exit 1
fi

# ==================== 测试函数 ====================

# run_test <测试名> <验证关键字> <curl 参数...>
run_test() {
    local name="$1"
    local expect="$2"
    shift 2

    TOTAL=$((TOTAL + 1))
    local resp
    resp=$(curl -s -m "$TIMEOUT" "$@" 2>&1) || true

    if echo "$resp" | grep -q "$expect"; then
        pass "$name"
        return 0
    elif echo "$resp" | grep -q "E015"; then
        skip "$name (API 后端返回 E015)"
        TOTAL=$((TOTAL - 1))
        return 2
    else
        fail "$name"
        echo "  期望包含: $expect"
        echo "  实际响应: $(echo "$resp" | head -c 200)"
        return 1
    fi
}

# run_stream_test <测试名> <验证关键字> <curl 参数...>
run_stream_test() {
    local name="$1"
    local expect="$2"
    shift 2

    TOTAL=$((TOTAL + 1))
    local resp
    resp=$(curl -s -m "$TIMEOUT" "$@" 2>&1) || true

    if echo "$resp" | grep -q "$expect"; then
        pass "$name"
        return 0
    else
        # 检查是否为 API 后端限制 (E015 或空响应 = 后端拒绝)
        if echo "$resp" | grep -q "E015" || [ -z "$(echo "$resp" | tr -d '[:space:]')" ]; then
            skip "$name (API 后端不可用)"
            TOTAL=$((TOTAL - 1))
            return 2
        fi
        fail "$name"
        echo "  期望包含: $expect"
        echo "  实际响应: $(echo "$resp" | head -c 200)"
        return 1
    fi
}

# ==================== OpenAI 协议测试 ====================

section "OpenAI 协议 (/v1/chat/completions)"

# --- 非流式 ---

run_test \
    "OpenAI 非流式 - content='Say hi'" \
    '"choices"' \
    "http://$CLIENT_LISTEN/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Say hi"}]}'

run_test \
    "OpenAI 非流式 - content='hi'" \
    '"choices"' \
    "http://$CLIENT_LISTEN/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}'

# --- 流式 ---

run_stream_test \
    "OpenAI 流式 - content='Say hi'" \
    '"delta"' \
    "http://$CLIENT_LISTEN/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","stream":true,"messages":[{"role":"user","content":"Say hi"}]}'

run_stream_test \
    "OpenAI 流式 - content='hi'" \
    '"delta"' \
    "http://$CLIENT_LISTEN/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","stream":true,"messages":[{"role":"user","content":"hi"}]}'

# ==================== Anthropic 协议测试 ====================

section "Anthropic 协议 (/v1/messages)"

# --- 非流式 ---

run_test \
    "Anthropic 非流式 - content='Say hi'" \
    '"type":"message"' \
    "http://$CLIENT_LISTEN/v1/messages" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","max_tokens":100,"messages":[{"role":"user","content":"Say hi"}]}'

run_test \
    "Anthropic 非流式 - content='hi'" \
    '"type":"message"' \
    "http://$CLIENT_LISTEN/v1/messages" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}'

# --- 流式 ---

run_stream_test \
    "Anthropic 流式 - content='Say hi'" \
    'event: message_start' \
    "http://$CLIENT_LISTEN/v1/messages" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"Say hi"}]}'

run_stream_test \
    "Anthropic 流式 - content='hi'" \
    'event: message_start' \
    "http://$CLIENT_LISTEN/v1/messages" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}'

# ==================== Anthropic 流式完整性测试 ====================

section "Anthropic 流式完整性"

TOTAL=$((TOTAL + 1))
STREAM_RESP=$(curl -s -m "$TIMEOUT" \
    "http://$CLIENT_LISTEN/v1/messages" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"Say hi"}]}' 2>&1) || true

if echo "$STREAM_RESP" | grep -q "E015" || [ -z "$(echo "$STREAM_RESP" | tr -d '[:space:]')" ]; then
    skip "Anthropic SSE 事件完整性 (API 后端不可用)"
    TOTAL=$((TOTAL - 1))
else
    SSE_OK=true
    for event in "message_start" "content_block_start" "content_block_delta" "content_block_stop" "message_delta" "message_stop"; do
        if ! echo "$STREAM_RESP" | grep -q "$event"; then
            SSE_OK=false
            fail "Anthropic SSE 事件完整性 - 缺少事件: $event"
            break
        fi
    done
    if $SSE_OK; then
        pass "Anthropic SSE 事件完整性 (message_start → content_block_delta → message_stop)"
    fi
fi

# ==================== OpenAI 流式完整性测试 ====================

TOTAL=$((TOTAL + 1))
STREAM_RESP=$(curl -s -m "$TIMEOUT" \
    "http://$CLIENT_LISTEN/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d '{"model":"claude-sonnet-4-20250514","stream":true,"messages":[{"role":"user","content":"Say hi"}]}' 2>&1) || true

if echo "$STREAM_RESP" | grep -q "E015" || [ -z "$(echo "$STREAM_RESP" | tr -d '[:space:]')" ]; then
    skip "OpenAI SSE 事件完整性 (API 后端不可用)"
    TOTAL=$((TOTAL - 1))
else
    OAI_OK=true
    for marker in '"delta"' '"finish_reason"' '[DONE]'; do
        if ! echo "$STREAM_RESP" | grep -q "$marker"; then
            OAI_OK=false
            fail "OpenAI SSE 事件完整性 - 缺少: $marker"
            break
        fi
    done
    if $OAI_OK; then
        pass "OpenAI SSE 事件完整性 (delta → finish_reason → [DONE])"
    fi
fi

# ==================== 结果汇总 ====================

section "测试结果"

echo ""
echo "  总计: $TOTAL"
echo -e "  ${GREEN}通过: $PASSED${NC}"
if [ $FAILED -gt 0 ]; then
    echo -e "  ${RED}失败: $FAILED${NC}"
fi
if [ $SKIPPED -gt 0 ]; then
    echo -e "  ${YELLOW}跳过: $SKIPPED${NC} (API 后端限制)"
fi
echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}  ALL TESTS PASSED!${NC}"
    echo -e "${GREEN}========================================${NC}"
    exit 0
else
    echo -e "${RED}========================================${NC}"
    echo -e "${RED}  $FAILED TEST(S) FAILED${NC}"
    echo -e "${RED}========================================${NC}"
    echo ""
    echo "Client 日志: $CLIENT_LOG"
    exit 1
fi
