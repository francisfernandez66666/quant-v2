#!/bin/bash
# quant-trading-v2 启动脚本
# usage: ./start.sh [dev|prod]

set -euo pipefail

MODE="${1:-dev}"
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$APP_DIR"

export QUANT_ADDR="${QUANT_ADDR:-:8080}"
export QUANT_DATA_DIR="${QUANT_DATA_DIR:-$HOME/.quant-trading-v2}"

# ── 可选环境变量 ──
# LLM_API_KEY          — LLM服务API Key（必填，否则新闻分析降级为关键词）
# TUSHARE_TOKEN        — Tushare token（可选，用于日线/财务数据）
# QUANT_ADDR           — HTTP监听地址（默认 :8080）
# QUANT_DATA_DIR       — 数据目录（默认 ~/.quant-trading-v2）

mkdir -p "$QUANT_DATA_DIR"

echo "=============================="
echo " quant-trading-v2"
echo " MODE:    $MODE"
echo " ADDR:    $QUANT_ADDR"
echo " DATADIR: $QUANT_DATA_DIR"
echo "=============================="

BUILD_QUANT="${BUILD_QUANT:-1}"

build() {
    echo "[*] 编译后端..."
    GONOSUMCHECK=* GONOSUMDB=* go build -o quant ./cmd/quant
    echo "[*] 后端编译完成"
}

build_web() {
    echo "[*] 编译前端..."
    cd web && npm install --silent && npm run build 2>/dev/null && cd "$APP_DIR"
    echo "[*] 前端编译完成"
}

run_dev() {
    if [ "$BUILD_QUANT" = "1" ]; then build; fi
    echo "[*] 启动后端 (dev mode)"
    export LLM_API_KEY="${LLM_API_KEY:-}"
    LOGFILE="$QUANT_DATA_DIR/quant.log"
    ./quant > "$LOGFILE" 2>&1 &
    QUANT_PID=$!
    echo "[*] 后端 PID: $QUANT_PID (日志: $LOGFILE)"
    sleep 1
    # 打印后端的最后几行启动日志
    tail -5 "$LOGFILE" 2>/dev/null | sed 's/^/  │ /'

    echo "[*] 启动前端 dev server (port 5173)..."
    cd web && npm install --silent && npm run dev &
    WEB_PID=$!

    cleanup() { kill "$QUANT_PID" "$WEB_PID" 2>/dev/null; wait; }
    trap "cleanup; exit" INT TERM
    echo "[*] 按 Ctrl+C 停止所有服务"
    echo "[*] 后端日志实时输出:"
    tail -f "$LOGFILE" &
    TAIL_PID=$!
    wait
}

run_prod() {
    if [ "$BUILD_QUANT" = "1" ]; then build; fi
    build_web
    LOGFILE="$QUANT_DATA_DIR/quant.log"
    echo "[*] 启动后端 (prod mode)"
    nohup ./quant > "$LOGFILE" 2>&1 &
    echo "[*] 后端 PID: $! (日志: $LOGFILE)"

    echo "[*] 启动前端 (port 5173)..."
    cd web && nohup npm run preview -- --port 5173 > /dev/null 2>&1 &
    echo "[*] 前端 PID: $!"
    echo "[*] 访问 http://localhost:5173"
    echo "[*] 查看后端日志: tail -f $LOGFILE"
}

case "$MODE" in
    dev) run_dev ;;
    prod) run_prod ;;
    build)
        build
        build_web
        ;;
    *)
        echo "usage: $0 [dev|prod|build]"
        exit 1
        ;;
esac
