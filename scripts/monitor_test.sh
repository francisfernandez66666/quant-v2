#!/bin/bash
# ==============================================================================
# quant-trading-v2 实盘用户视角监视脚本
# 用途：全天候对运行中的后端发起 HTTP API 请求，模拟真实用户操作，
#       全量记录每次请求的响应、耗时、异常，自动汇总问题点和阻塞点。
#
# 用法：
#   ./scripts/monitor_test.sh                          # 前台运行，间隔30秒
#   ./scripts/monitor_test.sh http://localhost:8080 10  # 指定地址和间隔
#   nohup ./scripts/monitor_test.sh &                   # 后台运行（推荐）
#   tail -f monitor_logs/summary_*.log                  # 实时查看摘要
#
# 输出文件（monitor_logs/）：
#   summary_<时间>.log    — 周期摘要 + 实时汇总
#   raw_<时间>.log        — 全量原始日志（含请求/响应/错误）
#   problem_<时间>.log    — 问题点记录（WARN/ERROR级别）
#   blocker_<时间>.log    — 阻塞点记录（影响系统运行的严重问题）
#   timing_<时间>.csv     — 每次请求的耗时 CSV（method,path,code,ms,status）
#   final_summary_<时间>.txt — 停止时自动生成的最终测试报告
# ==============================================================================
set -o pipefail

# ---- 帮助 ----
if [ "$1" = "--help" ] || [ "$1" = "-h" ]; then
	cat <<'HELP'
quant-trading-v2 实盘用户视角监视脚本
======================================
全天候自动探测后端 HTTP API，记录所有请求/响应/耗时/异常。

用法:
  ./scripts/monitor_test.sh                        # 前台运行，间隔30秒
  ./scripts/monitor_test.sh http://localhost:8080 10  # 指定地址和间隔
  nohup ./scripts/monitor_test.sh &                # 后台运行（推荐）
  tail -f monitor_logs/summary_*.log              # 实时查看摘要

输出文件 (monitor_logs/):
  summary_<时间>.log     周期摘要（实时追加）
  raw_<时间>.log         全量原始日志
  problem_<时间>.log     问题点记录（WARN/ERROR）
  blocker_<时间>.log     阻塞点记录
  timing_<时间>.csv      每次请求耗时 (method,path,code,ms,status)
  final_summary_<时间>.txt 停止时自动生成的最终报告
HELP
	exit 0
fi

BASE_URL="${1:-http://localhost:8080}"
POLL_INTERVAL="${2:-30}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DATA_DIR="${QUANT_DATA_DIR:-$HOME/.quant-trading-v2}"
LOG_DIR="$SCRIPT_DIR/../monitor_logs"

mkdir -p "$LOG_DIR"

TIMESTAMP=$(date '+%Y%m%d_%H%M%S')
SUMMARY_LOG="$LOG_DIR/summary_${TIMESTAMP}.log"
RAW_LOG="$LOG_DIR/raw_${TIMESTAMP}.log"
PROBLEM_LOG="$LOG_DIR/problem_${TIMESTAMP}.log"
BLOCKER_LOG="$LOG_DIR/blocker_${TIMESTAMP}.log"
TIMING_LOG="$LOG_DIR/timing_${TIMESTAMP}.csv"

# ---- 统计计数器 ----
TOTAL_REQUESTS=0
SUCCESS_COUNT=0
FAIL_COUNT=0
TIMEOUT_COUNT=0
PROBLEM_COUNT=0
BLOCKER_COUNT=0
CYCLES_DONE=0

# ---- 流水线阶段跟踪 ----
PIPELINE_STAGES=(1_EngineEvaluate 2_D1Scorer 3_LaodengScore 4_SectorVerify 5_ScanLong 6_ScanShort 7_StockTracker 8_PositionAlerts 9_Dashboard 10_NShapeScorer)
PIPELINE_OK=0
PIPELINE_FAIL=0
PIPELINE_FAIL_STAGES=""

# ---- 配置 ----
USERNAME="monitor_test"
PASSWORD="monitor_pass_$(date +%s)"
TOKEN=""
server_ok=false
HAS_SHORT_ENABLED=false
LAST_DASHBOARD_JSON=""

# ---- 颜色 ----
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# ==============================================================================
# 日志函数
# ==============================================================================
log_summary()  { echo -e "${CYAN}[SUMMARY]${NC} $*" | tee -a "$SUMMARY_LOG"; echo "[SUMMARY] $*" >> "$RAW_LOG"; }
log_ok()       { echo -e "${GREEN}[OK]${NC} $*" | tee -a "$RAW_LOG"; }
log_warn()     { echo -e "${YELLOW}[WARN]${NC} $*" | tee -a "$RAW_LOG"; echo "[WARN] $*" >> "$PROBLEM_LOG"; ((PROBLEM_COUNT++)); }
log_error()    { echo -e "${RED}[ERROR]${NC} $*" | tee -a "$RAW_LOG"; echo "[ERROR] $*" >> "$PROBLEM_LOG"; ((PROBLEM_COUNT++)); }
log_blocker()  { echo -e "${RED}[BLOCKER]${NC} $*" | tee -a "$RAW_LOG"; echo "[BLOCKER] $*" >> "$BLOCKER_LOG"; echo "[BLOCKER] $*" >> "$PROBLEM_LOG"; ((BLOCKER_COUNT++)); }
log_full()     { echo "$*" >> "$RAW_LOG"; echo "$*" >> "$SUMMARY_LOG"; }
log_sep()      { echo "-----" >> "$RAW_LOG"; echo "-----" >> "$SUMMARY_LOG"; }

# ==============================================================================
# curl 包装：记录耗时，检查 HTTP 状态
# ==============================================================================
do_curl() {
	local method="$1"
	local path="$2"
	local expect_code="${3:-200}"
	local body="${4:-}"
	local desc="$5"

	local url="${BASE_URL}${path}"
	local start_time end_time http_code resp_body http_code_raw duration

	start_time=$(python3 -c "import time; print(int(time.time()*1000))" 2>/dev/null || echo 0)
	((TOTAL_REQUESTS++))

	if [ "$method" = "GET" ]; then
		if [ -n "$TOKEN" ]; then
			http_code_raw=$(curl -s -o "$TMP_RES" -w "%{http_code}" --max-time 10 \
				-H "Authorization: $TOKEN" -H "Content-Type: application/json" "$url" 2>/dev/null)
		else
			http_code_raw=$(curl -s -o "$TMP_RES" -w "%{http_code}" --max-time 10 \
				-H "Content-Type: application/json" "$url" 2>/dev/null)
		fi
	elif [ "$method" = "POST" ] || [ "$method" = "PUT" ]; then
		if [ -n "$TOKEN" ]; then
			http_code_raw=$(curl -s -o "$TMP_RES" -w "%{http_code}" --max-time 10 \
				-H "Authorization: $TOKEN" -H "Content-Type: application/json" \
				-d "$body" "$url" 2>/dev/null)
		else
			http_code_raw=$(curl -s -o "$TMP_RES" -w "%{http_code}" --max-time 10 \
				-H "Content-Type: application/json" \
				-d "$body" "$url" 2>/dev/null)
		fi
	elif [ "$method" = "DELETE" ]; then
		if [ -n "$TOKEN" ]; then
			http_code_raw=$(curl -s -o "$TMP_RES" -w "%{http_code}" --max-time 10 \
				-X DELETE -H "Authorization: $TOKEN" -H "Content-Type: application/json" "$url" 2>/dev/null)
		else
			http_code_raw=$(curl -s -o "$TMP_RES" -w "%{http_code}" --max-time 10 \
				-X DELETE -H "Content-Type: application/json" "$url" 2>/dev/null)
		fi
	fi
	end_time=$(python3 -c "import time; print(int(time.time()*1000))" 2>/dev/null || echo 0)

	if [ -z "$http_code_raw" ] || [ "$http_code_raw" = "000" ]; then
		http_code=000
		resp_body="(curl失败/超时)"
		duration=-1
		((TIMEOUT_COUNT++))
		log_error "请求超时或失败: $method $path — $desc"
		echo "$TIMESTAMP,$method,$path,$http_code,$duration,FAIL,$desc" >> "$TIMING_LOG"
		return 1
	fi

	if [ "$start_time" != "0" ] && [ "$end_time" != "0" ]; then
		duration=$(( end_time - start_time ))
	else
		duration=0
	fi

	http_code=$http_code_raw
	resp_body=$(cat "$TMP_RES" 2>/dev/null | head -c 2000)

	echo "$TIMESTAMP,$method,$path,$http_code,${duration}ms,$([ "$http_code" -eq "$expect_code" ] && echo OK || echo FAIL),$desc" >> "$TIMING_LOG"

	if [ "$http_code" -eq "$expect_code" ]; then
		log_ok "[${http_code}][${duration}ms] $method $path — $desc"
		((SUCCESS_COUNT++))
		echo "$resp_body" >> "$RAW_LOG"
		return 0
	else
		((FAIL_COUNT++))
		log_error "[${http_code}][${duration}ms] $method $path (期望 $expect_code) — $desc"
		echo "  响应: $resp_body" >> "$RAW_LOG"
		# 如果 5xx 或连接失败，标记为 blocker
		if [ "$http_code" -ge 500 ] || [ "$http_code" = "000" ]; then
			log_blocker "服务端错误: $method $path → $http_code"
		fi
		# 401 说明 token 失效
		if [ "$http_code" -eq 401 ] && [ "$path" != "/auth/login" ] && [ "$path" != "/setup" ]; then
			log_blocker "Token 失效，需要重新登录"
		fi
		return 1
	fi
}

# ==============================================================================
# 阶段1: 服务可用性检查 + 初始化
# ==============================================================================
init_server() {
	log_summary "═══════════════════════════════════════════════"
	log_summary "  监视测试启动 | 后端: $BASE_URL | 间隔: ${POLL_INTERVAL}s"
	log_summary "  日志目录: $LOG_DIR"
	log_summary "═══════════════════════════════════════════════"
	log_full ""
	log_full "系统启动时间: $(date '+%Y-%m-%d %H:%M:%S')"
	log_full ""

	# 1a. 检查后端是否存活（/setup 和 /auth/login 都不需要鉴权）
	log_summary "[阶段1a] 服务可用性检查"
	local health_ok=false
	# /setup 返回 200 表示服务运行中（不论是否初始化）
	if curl -s --max-time 5 -o /dev/null -w "%{http_code}" "${BASE_URL}/setup" 2>/dev/null | grep -q '2'; then
		health_ok=true
	elif curl -s --max-time 5 -o /dev/null -w "%{http_code}" "${BASE_URL}/auth/login" 2>/dev/null | grep -q '2'; then
		health_ok=true
	fi
	if [ "$health_ok" = true ]; then
		server_ok=true
		log_summary "  后端服务存活 ✓"
	else
		log_blocker "后端服务 ${BASE_URL} 不可达！请先启动后端。"
		log_blocker "  启动命令: cd $(dirname "$SCRIPT_DIR") && ./start.sh dev"
		return 1
	fi

	# 1b. 检测是否需要首次设置
	log_summary "[阶段1b] 系统初始化检查"
	local setup_resp
	setup_resp=$(curl -s --max-time 5 "${BASE_URL}/setup" 2>/dev/null)
	local init_status
	init_status=$(echo "$setup_resp" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('initialized',''))" 2>/dev/null)

	if [ "$init_status" = "false" ] || [ -z "$init_status" ]; then
		log_summary "  系统未初始化，执行首次设置..."
		do_curl "POST" "/setup" 200 \
			"{\"username\":\"${USERNAME}\",\"password\":\"${PASSWORD}\",\"llm_api_url\":\"\",\"llm_api_key\":\"\"}" \
			"首次设置管理员账户"

		if [ "$http_code" -eq 200 ] || [ "$http_code" -eq 201 ]; then
			TOKEN=$(echo "$resp_body" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))" 2>/dev/null)
			log_summary "  首次设置完成，Token 已获取"
		else
			log_blocker "首次设置失败，无法继续"
			return 1
		fi
	else
		log_summary "  系统已初始化，尝试登录..."
		# 尝试默认管理员
		local login_resp
		for cred in '{"username":"admin","password":"admin123"}' "{\"username\":\"${USERNAME}\",\"password\":\"${PASSWORD}\"}"; do
			login_resp=$(curl -s --max-time 5 -X POST "${BASE_URL}/auth/login" \
				-H "Content-Type: application/json" -d "$cred" 2>/dev/null)
			TOKEN=$(echo "$login_resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))" 2>/dev/null)
			if [ -n "$TOKEN" ]; then
				log_summary "  登录成功: $(echo "$login_resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('account',''))" 2>/dev/null)"
				break
			fi
			USERNAME="${cred%%:*}"
		done

		if [ -z "$TOKEN" ]; then
			# 创建临时账户
			log_summary "  尝试创建临时账户..."
			local tmp_resp
			tmp_resp=$(curl -s --max-time 5 -X POST "${BASE_URL}/auth/temp" \
				-H "Content-Type: application/json" 2>/dev/null)
			TOKEN=$(echo "$tmp_resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))" 2>/dev/null)
			if [ -z "$TOKEN" ]; then
				log_blocker "所有认证方式均失败，无法继续"
				return 1
			fi
			log_summary "  临时账户已创建"
		fi
	fi

	log_summary "  Token: ${TOKEN:0:16}..."
	log_full ""
	return 0
}

# ==============================================================================
# 阶段2: 核心 API 轮询
# ==============================================================================
do_cycle() {
	local cycle_errors=0

	# ---- 2a. 做空状态 ----
	do_curl "GET" "/api/short/status" 200 "" "做空开关状态" || ((cycle_errors++))
	local short_enabled
	short_enabled=$(cat "$TMP_RES" | python3 -c "import sys,json; print(json.load(sys.stdin).get('short_enabled',False))" 2>/dev/null)
	HAS_SHORT_ENABLED=$short_enabled

	# ---- 2b. 看板 ----
	do_curl "GET" "/api/dashboard" 200 "" "主看板数据" || ((cycle_errors++))
	local dash_news dash_hot dash_bear dash_signals
	dash_news=$(cat "$TMP_RES" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('news_events',[])))" 2>/dev/null)
	dash_hot=$(cat "$TMP_RES" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('hot_sectors',[])))" 2>/dev/null)
	dash_bear=$(cat "$TMP_RES" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('bear_sectors',[])))" 2>/dev/null)
	dash_signals=$(cat "$TMP_RES" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('final_signals',[])))" 2>/dev/null)
	log_ok "  Dashboard 概要: 新闻=${dash_news} 利好板块=${dash_hot} 利空板块=${dash_bear} 最终信号=${dash_signals}"
	LAST_DASHBOARD_JSON=$(cat "$TMP_RES")

	# ---- 2c. 信号 ----
	do_curl "GET" "/api/signals" 200 "" "战法信号列表" || ((cycle_errors++))

	# ---- 2d. 告警 ----
	do_curl "GET" "/api/alerts" 200 "" "持仓告警" || ((cycle_errors++))

	# ---- 2e. 持仓 ----
	do_curl "GET" "/api/positions" 200 "" "持仓列表" || ((cycle_errors++))
	local pos_count
	pos_count=$(cat "$TMP_RES" | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('positions',[])))" 2>/dev/null)
	log_ok "  持仓数: ${pos_count}"

	# ---- 2f. 热板块 ----
	do_curl "GET" "/api/sector/hot" 200 "" "热板块" || ((cycle_errors++))

	# ---- 2g. 快照 ----
	do_curl "GET" "/api/snapshot" 200 "" "全量快照" || ((cycle_errors++))

	# ---- 2h. 热点快照 ----
	do_curl "GET" "/api/snapshot/hot" 200 "" "热点快照" || ((cycle_errors++))

	# ---- 2i. 新闻 ----
	do_curl "GET" "/api/news" 200 "" "新闻列表" || ((cycle_errors++))

	# ---- 2j. 自选股 ----
	do_curl "GET" "/api/watchlist" 200 "" "自选股列表" || ((cycle_errors++))

	# ---- 2k. 状态 ----
	do_curl "GET" "/api/status" 200 "" "系统状态" || ((cycle_errors++))

	# ---- 2l. 策略配置 ----
	do_curl "GET" "/api/config/strategy" 200 "" "战法配置" || ((cycle_errors++))

	# ---- 2m. 评估明细 ----
	do_curl "GET" "/api/evaluations" 200 "" "战法评估明细" || ((cycle_errors++))

	# ---- 2n. LLM 调试 ----
	do_curl "GET" "/api/llm-debug" 200 "" "LLM调试信息" || ((cycle_errors++))

	# =====================================================================
	# 全流程 Go 管道测试（每 5 个周期执行一次，耗时约 10s）
	# =====================================================================
	if [ $((CYCLES_DONE % 5)) -eq 1 ]; then
		run_pipeline_test || ((cycle_errors++))
	fi

	# =====================================================================
	# 写操作（每10个周期做一次）
	# =====================================================================
	if [ $((CYCLES_DONE % 10)) -eq 0 ]; then

		# ---- 3a. 做空切换 ----
		if [ "$HAS_SHORT_ENABLED" = "True" ]; then
			do_curl "POST" "/api/short/toggle" 200 '{"enabled":false}' "做空关闭" || ((cycle_errors++))
			do_curl "POST" "/api/short/toggle" 200 '{"enabled":true}' "做空开启" || ((cycle_errors++))
		fi

		# ---- 3b. 自选股操作 ----
		local wl_before_code wl_before_count wl_after_count
		wl_before_count=$(cat "$TMP_RES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null)
		wl_before_code=$(cat "$TMP_RES" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d[0].get('code','') if d else '')" 2>/dev/null)

		do_curl "POST" "/api/watchlist" 200 "{\"code\":\"000001\",\"name\":\"平安银行\"}" "自选添加" || ((cycle_errors++))
		do_curl "DELETE" "/api/watchlist" 200 "{\"code\":\"000001\"}" "自选删除(平安银行)" || ((cycle_errors++))

		# ---- 3c. 持仓操作（创建/平仓） ----
		if [ "$pos_count" -eq 0 ] 2>/dev/null; then
			local pos_id
			do_curl "POST" "/api/positions" 201 \
				'{"code":"600519","name":"贵州茅台","direction":"做多","strategy":"Dragon","entry_price":1500,"take_profit_pct":10,"stop_loss_pct":5}' \
				"创建测试持仓" || ((cycle_errors++))
			pos_id=$(cat "$TMP_RES" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null)
			if [ -n "$pos_id" ]; then
				log_ok "  测试持仓已创建: $pos_id"
				# 稍后平仓
				sleep 1
				do_curl "POST" "/api/positions/${pos_id}/exit" 200 '{"exit_price":1650}' "平仓测试持仓" || ((cycle_errors++))
				log_ok "  测试持仓已平仓: $pos_id"
			fi
		fi
	fi

	# =====================================================================
	# 健康检查
	# =====================================================================
	do_curl "GET" "/api/health" 200 "" "健康检查" || { log_blocker "健康检查失败，后端可能已崩溃"; return 1; }

	log_sep
	return $cycle_errors
}

# ==============================================================================
# 阶段3: Go 全流程管道测试（10 阶段）
# ==============================================================================
run_pipeline_test() {
	local project_dir
	project_dir="$(dirname "$SCRIPT_DIR")"
	local pipeline_log="$LOG_DIR/pipeline_${TIMESTAMP}.log"

	log_summary "[PIPELINE] 开始全流程 10 阶段测试 (go test)..."

	local t0 t1 elapsed
	t0=$(python3 -c "import time; print(int(time.time()*1000))")

	cd "$project_dir" && go test -v -run TestMonitorPipelineStages -count=1 -timeout 120s ./cmd/quant/ > "$pipeline_log" 2>&1
	local exit_code=$?

	t1=$(python3 -c "import time; print(int(time.time()*1000))")
	elapsed=$(( t1 - t0 ))

	log_ok "  go test 完成: exit=${exit_code} 耗时=${elapsed}ms"

	# 解析 STAGE 行
	local stage_pass=0
	local stage_fail=0
	local fail_stages=""

	while IFS= read -r line; do
		if echo "$line" | grep -q '^.*\[STAGE\]'; then
			# [STAGE] 1_EngineEvaluate|PASS|1051ms|hot=...
			local stage_line stage_name stage_status stage_ms stage_detail
			stage_line=$(echo "$line" | sed 's/.*\[STAGE\] //')
			stage_name=$(echo "$stage_line" | cut -d'|' -f1)
			stage_status=$(echo "$stage_line" | cut -d'|' -f2)
			stage_ms=$(echo "$stage_line" | cut -d'|' -f3)
			stage_detail=$(echo "$stage_line" | cut -d'|' -f4-)

			if [ "$stage_status" = "PASS" ]; then
				log_ok "  [STAGE] ${stage_name} | ${stage_status} | ${stage_ms} | ${stage_detail}"
				((stage_pass++))
			else
				log_error "  [STAGE] ${stage_name} | ${stage_status} | ${stage_ms} | ${stage_detail}"
				((stage_fail++))
				[ -n "$fail_stages" ] && fail_stages="${fail_stages},"
				fail_stages="${fail_stages}${stage_name}"
			fi
		fi
	done < "$pipeline_log"

	# 解析 SUMMARY 行
	local summary_line
	summary_line=$(grep '\[SUMMARY\]' "$pipeline_log" | grep -i '全流程' | tail -1)
	if [ -n "$summary_line" ]; then
		log_summary "  ${summary_line#*\[SUMMARY\] }"
	fi

	PIPELINE_OK=$stage_pass
	PIPELINE_FAIL=$stage_fail
	PIPELINE_FAIL_STAGES=$fail_stages

	if [ "$stage_fail" -gt 0 ]; then
		log_warn "  流水线: ${stage_pass} PASS, ${stage_fail} FAIL (阶段: ${fail_stages})"
		return 1
	else
		log_ok "  流水线: ${stage_pass}/${stage_pass} 全部通过 ✓"
		return 0
	fi
}

# ==============================================================================
# 阶段4: 阻塞点 / 问题点检测
# ==============================================================================
check_for_issues() {
	local tmp="$TMP_RES"

	# 检查 Dashboard 中是否有异常字段
	if [ -n "$LAST_DASHBOARD_JSON" ]; then
		local err_field
		err_field=$(echo "$LAST_DASHBOARD_JSON" | python3 -c "
import sys, json
d = json.load(sys.stdin)
issues = []
if 'error' in d: issues.append(f\"api_error={d['error']}\")
print('; '.join(issues))" 2>/dev/null)
		if [ -n "$err_field" ]; then
			log_warn "Dashboard 返回错误: $err_field"
		fi

		# 检查是否有 blocked 个股
		local blocked_count
		blocked_count=$(echo "$LAST_DASHBOARD_JSON" | python3 -c "
import sys, json
d = json.load(sys.stdin)
b = d.get('l1_blocked', {})
print(len(b))" 2>/dev/null)
		if [ "$blocked_count" -gt 0 ] 2>/dev/null; then
			log_warn "L1 拦截个股数: $blocked_count"
		fi
	fi
}

# ==============================================================================
# 阶段4: 启动后端（如需）
# ==============================================================================
ensure_server() {
	if curl -s --max-time 3 "${BASE_URL}/api/health" >/dev/null 2>&1; then
		return 0
	fi
	log_summary "后端未运行，尝试自动启动..."
	local quant_bin
	quant_bin="$(dirname "$SCRIPT_DIR")/quant"
	if [ -x "$quant_bin" ]; then
		export QUANT_DATA_DIR="$DATA_DIR"
		nohup "$quant_bin" > "$LOG_DIR/server_${TIMESTAMP}.log" 2>&1 &
		local srv_pid=$!
		log_summary "后端已启动: PID=$srv_pid，等待就绪..."
		for i in $(seq 1 30); do
			sleep 1
			if curl -s --max-time 2 "${BASE_URL}/api/health" >/dev/null 2>&1; then
				log_summary "后端就绪 (${i}s)"
				return 0
			fi
		done
		log_blocker "后端启动超时，请手动启动"
		return 1
	else
		log_blocker "未找到后端二进制: $quant_bin"
		log_blocker "请先在项目根目录执行: go build -o quant ./cmd/quant"
		return 1
	fi
}

# ==============================================================================
# 主循环
# ==============================================================================
cleanup() {
	log_full ""
	log_summary "═══════════════════════════════════════════════"
	log_summary "  监视测试结束 | $(date '+%Y-%m-%d %H:%M:%S')"
	log_summary "  运行周期: ${CYCLES_DONE} | 总请求: ${TOTAL_REQUESTS}"
	log_summary "  成功: ${SUCCESS_COUNT} | 失败: ${FAIL_COUNT} | 超时: ${TIMEOUT_COUNT}"
	log_summary "  问题点: ${PROBLEM_COUNT} | 阻塞点: ${BLOCKER_COUNT}"
	log_summary "  流水线阶段: ${PIPELINE_OK} PASS, ${PIPELINE_FAIL} FAIL"
	if [ -n "$PIPELINE_FAIL_STAGES" ]; then
		log_summary "  流水线失败阶段: ${PIPELINE_FAIL_STAGES}"
	fi
	log_summary "═══════════════════════════════════════════════"

	# 写出最终汇总
	cat > "$LOG_DIR/final_summary_${TIMESTAMP}.txt" <<-EOF
	quant-trading-v2 实盘监视测试报告
	=================================
	测试时间: $(date '+%Y-%m-%d %H:%M:%S')
	运行时长: 从 ${TIMESTAMP} 起
	后端地址: ${BASE_URL}
	周期数:   ${CYCLES_DONE}
	总请求:   ${TOTAL_REQUESTS}
	成功:     ${SUCCESS_COUNT}
	失败:     ${FAIL_COUNT}
	超时:     ${TIMEOUT_COUNT}
	问题点:   ${PROBLEM_COUNT}
	阻塞点:   ${BLOCKER_COUNT}
	流水线OK: ${PIPELINE_OK}
	流水线FAIL: ${PIPELINE_FAIL}
	流水线失败阶段: ${PIPELINE_FAIL_STAGES:-无}
	=================================
	EOF
	if [ -f "$PROBLEM_LOG" ]; then
		echo "" >> "$LOG_DIR/final_summary_${TIMESTAMP}.txt"
		echo "--- 问题点明细 ---" >> "$LOG_DIR/final_summary_${TIMESTAMP}.txt"
		cat "$PROBLEM_LOG" >> "$LOG_DIR/final_summary_${TIMESTAMP}.txt"
	fi
	if [ -f "$BLOCKER_LOG" ]; then
		echo "" >> "$LOG_DIR/final_summary_${TIMESTAMP}.txt"
		echo "--- 阻塞点明细 ---" >> "$LOG_DIR/final_summary_${TIMESTAMP}.txt"
		cat "$BLOCKER_LOG" >> "$LOG_DIR/final_summary_${TIMESTAMP}.txt"
	fi
	log_summary "最终报告已写入: $LOG_DIR/final_summary_${TIMESTAMP}.txt"
	exit 0
}

trap cleanup EXIT INT TERM

# ==============================================================================
# 启动
# ==============================================================================
TMP_RES=$(mktemp)
TMP_RES2=$(mktemp)

# 确认后端运行
ensure_server || exit 1

# 初始化和登录
init_server || exit 1

log_summary ""
log_summary "═══════════════════════════════════════════════"
log_summary "  进入监视循环，每 ${POLL_INTERVAL} 秒一轮"
log_summary "═══════════════════════════════════════════════"
log_full ""

START_TIME=$(date +%s)
while true; do
	TIMESTAMP=$(date '+%H:%M:%S')
	CYCLE_START=$(python3 -c "import time; print(int(time.time()*1000))")
	((CYCLES_DONE++))

	log_summary "[周期 #${CYCLES_DONE}] $(date '+%Y-%m-%d %H:%M:%S')"

	# 执行一轮测试
	do_cycle
	cycle_exit=$?

	# 问题检测
	check_for_issues

	# 周期汇总
	cycle_end=$(python3 -c "import time; print(int(time.time()*1000))") 2>/dev/null
	elapsed=$(( cycle_end - CYCLE_START )) 2>/dev/null
	log_summary "[周期 #${CYCLES_DONE}] 完成 | 耗时 ${elapsed}ms | 累积: ${SUCCESS_COUNT}✓ ${FAIL_COUNT}✗ ${TIMEOUT_COUNT}⏱ ${BLOCKER_COUNT}🚫"

	# 检查是否到交易结束 (15:30 后不再频繁请求)
	current_hour=$(date '+%H' 2>/dev/null)
	current_hour=${current_hour:-12}
	if [ "$current_hour" -ge 15 ] 2>/dev/null && [ "$current_hour" -lt 18 ] 2>/dev/null; then
		log_summary "盘后时段，延长间隔至 120 秒"
		sleep 120
	elif [ "$current_hour" -ge 18 ] 2>/dev/null || [ "$current_hour" -lt 9 ] 2>/dev/null; then
		log_summary "非交易时段，延长间隔至 300 秒"
		sleep 300
	else
		sleep "$POLL_INTERVAL"
	fi
done
