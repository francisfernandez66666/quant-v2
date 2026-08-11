#!/bin/bash
# quant-trading-v2 启动脚本
# usage: ./start.sh [dev|prod]
#   dev    — 开发模式：编译后端 + 启动后端与前端 dev server（Ctrl+C 一并停止）
#   prod   - 生产模式：编译前后端，nohup 后台运行
#   build  — 仅编译前后端，不启动

set -euo pipefail

# 运行模式：默认 dev
MODE="${1:-dev}"
# 项目根目录（脚本所在目录），后续所有命令基于此切换工作目录
APP_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$APP_DIR"

# 默认环境变量：HTTP 监听地址 + 数据目录
export QUANT_ADDR="${QUANT_ADDR:-:8080}"
export QUANT_DATA_DIR="${QUANT_DATA_DIR:-$HOME/.quant-trading-v2}"

# ── 可选环境变量 ──
# LLM_API_KEY          — LLM服务API Key（必填，否则新闻分析降级为关键词）
# TUSHARE_TOKEN        — Tushare token（可选，用于日线/财务数据）
# QUANT_ADDR           — HTTP监听地址（默认 :8080）
# QUANT_DATA_DIR       — 数据目录（默认 ~/.quant-trading-v2）

# 确保数据目录存在（后端持久化文件存放于此）
mkdir -p "$QUANT_DATA_DIR"

# find_free_port 端口探测：从 base 开始找第一个未被监听的端口（最多尝试 50 个），
# 用于前后端端口被占用时自动切换到下一个空闲端口，避免 bind 冲突直接报错。
find_free_port() {
    local base="${1:-8080}"
    local i port
    for ((i = 0; i < 50; i++)); do
        port=$((base + i))
        if ! lsof -iTCP:"$port" -sTCP:LISTEN -n -P >/dev/null 2>&1; then
            echo "$port"
            return 0
        fi
    done
    echo "$base"
}

echo "=============================="
echo " quant-trading-v2"
echo " MODE:    $MODE"
echo " ADDR:    $QUANT_ADDR"
echo " DATADIR: $QUANT_DATA_DIR"
echo "=============================="

# BUILD_QUANT：是否编译后端（设为 0 可跳过编译直接运行已有二进制）
BUILD_QUANT="${BUILD_QUANT:-1}"

# build 编译后端二进制（输出到项目根目录 ./quant）
build() {
    echo "[*] 编译后端..."
    GONOSUMCHECK=* GONOSUMDB=* go build -o quant ./cmd/quant
    echo "[*] 后端编译完成"
}

# build_web 安装前端依赖并执行生产构建
build_web() {
    echo "[*] 编译前端..."
    cd web && npm install --silent && npm run build 2>/dev/null && cd "$APP_DIR"
    echo "[*] 前端编译完成"
}

# run_dev 开发模式：编译后端 → 后台启动后端 → 启动前端 dev server，
# 并在 Ctrl+C 时统一清理所有子进程
run_dev() {
    if [ "$BUILD_QUANT" = "1" ]; then build; fi
    # 端口占用自动顺延：后端默认 8080、前端默认 5173，被占用则换下一个空闲端口
    BACKEND_PORT="$(find_free_port 8080)"
    FRONTEND_PORT="$(find_free_port 5173)"
    export QUANT_ADDR=":$BACKEND_PORT"
    export VITE_BACKEND_PORT="$BACKEND_PORT"

    echo "[*] 启动后端 (dev mode, 端口 :$BACKEND_PORT)"
    export LLM_API_KEY="${LLM_API_KEY:-}"
    LOGFILE="$QUANT_DATA_DIR/quant.log"
    # 后台启动后端，日志重定向到 quant.log
    ./quant > "$LOGFILE" 2>&1 &
    QUANT_PID=$!
    echo "[*] 后端 PID: $QUANT_PID (日志: $LOGFILE)"
    sleep 1
    # 打印后端的最后几行启动日志
    # 用 awk 而非 sed 前缀多字节字符：macOS BSD sed 在 UTF-8 下处理含 NUL/非法字节的日志行会触发
    # "Assertion failed: (advance > 0), function substitute" 崩溃，awk 按字节处理不受影响。
    tail -5 "$LOGFILE" 2>/dev/null | awk '{print "  │ " $0}'

    echo "[*] 启动前端 dev server (端口 $FRONTEND_PORT, 代理后端 :$BACKEND_PORT)"
    cd web && npm install --silent && npm run dev -- --port "$FRONTEND_PORT" &
    WEB_PID=$!

    # 退出时杀掉前后端进程并等待回收，避免残留
    cleanup() { kill "$QUANT_PID" "$WEB_PID" 2>/dev/null; wait; }
    trap "cleanup; exit" INT TERM
    echo "[*] 访问 http://localhost:$FRONTEND_PORT"
    echo "[*] 按 Ctrl+C 停止所有服务"
    echo "[*] 后端日志实时输出:"
    tail -f "$LOGFILE" &
    TAIL_PID=$!
    wait
}

# run_prod 生产模式：编译前后端，nohup 后台常驻运行并打印 PID
run_prod() {
    if [ "$BUILD_QUANT" = "1" ]; then build; fi
    build_web
    LOGFILE="$QUANT_DATA_DIR/quant.log"
    BACKEND_PORT="$(find_free_port 8080)"
    FRONTEND_PORT="$(find_free_port 5173)"
    export QUANT_ADDR=":$BACKEND_PORT"

    echo "[*] 启动后端 (prod mode, 端口 :$BACKEND_PORT)"
    # nohup 脱离终端运行，即使 SSH 断开也不受影响
    nohup ./quant > "$LOGFILE" 2>&1 &
    echo "[*] 后端 PID: $! (日志: $LOGFILE)"

    echo "[*] 启动前端 preview (端口 $FRONTEND_PORT)"
    # 前端使用构建产物 preview 模式（默认端口 5173，被占用时自动顺延）
    cd web && nohup npm run preview -- --port "$FRONTEND_PORT" > /dev/null 2>&1 &
    echo "[*] 前端 PID: $!"
    echo "[*] 访问 http://localhost:$FRONTEND_PORT"
    echo "[*] 查看后端日志: tail -f $LOGFILE"
}

# 按模式分发
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
