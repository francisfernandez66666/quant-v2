#!/bin/bash
# ⚠⚠ 本链已于 **2026-10-11 退役**（owner 当面裁「整条停掉」，含每日本地下载那一步）。
#    launchd 侧的 com.quant.syncdata 已 bootout、plist 改名成
#    ~/Library/LaunchAgents/com.quant.syncdata.plist.retired-20261011 留证不再被加载；
#    退役后由 deploy/mac/check_mac_ops_watchdog.sh 的「腿 3：已退役调度不得复活」每天验在位性，
#    「腿 4：本地行情库新度」把"库还剩多新"变成读数（默认只读数、不判红）。
#    脚本本身保留、不删——按需手跑的入口就是这一份，理由与证据如下（别让下一个人重新推一遍）：
#      · 六个步骤在这台机的整个日志生命周期里**从未走到第 6 步**：stdout 留档里
#        "云端 max:" 0 次、"同步完成" 0 次；stderr 里
#        `sqlite3.OperationalError: unable to open database file` 21 次＝7 个交易日 × 每次 3 试，
#        launchd `last exit code = 1` 每天如此。
#      · 根因不在脚本读法，在**目标机已经改行**：默认写死的云端数据目录 /var/lib/quant-trading-v2
#        在那台机上不存在（只剩一个 8 月 13 日的 .bak-<epoch> 残骸，里面是别的应用的 auth.json/
#        messages.json），/opt 下现在的目录属于另一套服务，quant 的服务/二进制/数据一概不在。
#        全盘能找到的唯一 trading.db 是一个 4096 字节的空壳（8 月 18 日建，只有表结构）。
#      · 代价是真实的：第 2 步"本地 baostock 下载"每天照样跑，最短 37m36s、最长 6h25m，
#        跑完才在第 3 步撞墙。每天烧几小时真实算力＋零产出＋无人被告知
#        （`syncdata` 三字在 deploy/mac/*.sh 与 scripts/verify_changes.sh 里当时一次都检索不到）。
#      · 如果将来要恢复"推到某台机"的语义，先确认两件事再动这个文件：①目标机与那个库现在归谁
#        （现网广州那份库属**交易面**，引擎在用；把 Mac 的 baostock 行情整表推进去是一次跨面的
#        生产写，要走备份→推送→复核的正规通道并单独设计表集/口径/幂等/锁，不是改个路径就行）；
#        ②为什么这次是"每天自动"而不是"按需手跑"——本链退役的直接理由就是它没有产出却在自动跑。
# 历史设计说明（退役前口径，保留原文不写成后见之明）：
# 本地 baostock 每日自动下载 → 上传云端增量脚本（阶段2.1b，设计见 docs/DATA_SYNC_SCRIPT.md）。
# 背景：云端 baostock IP 被封，数据下载职责迁移到本地——本脚本收盘后依次：
#   0) 前置检查（ssh 可达 / 本地库存在 / dataload 二进制就绪）
#   1) 启动本地 pydata sidecar（若未监听）
#   2) 本地 dataload daily（baostock 断点续传）+ adjfactor（可选）
#   3) 读云端各表 MAX(trade_date/end_date)
#   4) 本地 export-delta --since <云端最早max>
#   5) scp 上传 → 云端 import-delta 幂等合入
#   6) 增量行数校验输出；失败保留 delta 文件便于重试（全流程幂等，直接重跑安全）
#
# 用法：
#   SERVER_IP=43.108.86.140 bash scripts/sync_delta_to_cloud.sh          # 手动
#   launchd/cron 定时见 docs/DATA_SYNC_SCRIPT.md 第四节                   # 定时
#
# 环境变量（含默认）：
#   SERVER_IP        云端公网 IP（必填）
#   SERVER_USER      SSH 用户（默认 root）
#   QUANT_DATA_DIR   云端数据目录（默认 /var/lib/quant-trading-v2）
#   LOCAL_DB         本地研究库路径（默认 ~/.quant-trading-v2/trading.db）
#   PYDATA_PORT      本地 pydata sidecar 端口（默认 8787）
#   CLOUD_BIN        云端 dataload 路径（默认 /opt/quant/dataload）
#   DATALOAD_BIN     本地 dataload 二进制路径（launchd 安装副本场景直指定，免编译）
#   PYDATA_SERVER    pydata server.py 路径（同上；缺省用仓库内 $ROOT/cmd/pydata/server.py）
#   ADJFACTOR_ENABLED 是否每日补复权因子（默认 0=跳过；首次补齐后基本无新数据）
set -euo pipefail

SERVER_IP="${SERVER_IP:?请设置 SERVER_IP（云端公网 IP）}"
SERVER_USER="${SERVER_USER:-root}"
QUANT_DATA_DIR="${QUANT_DATA_DIR:-/var/lib/quant-trading-v2}"
LOCAL_DB="${LOCAL_DB:-$HOME/.quant-trading-v2/trading.db}"
PYDATA_PORT="${PYDATA_PORT:-8787}"
CLOUD_BIN="${CLOUD_BIN:-/opt/quant/dataload}"
ADJFACTOR_ENABLED="${ADJFACTOR_ENABLED:-0}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DELTA_TMP="${DELTA_TMP:-/tmp/delta_quant}"
TODAY=$(date +%Y%m%d)
DELTA_FILE="$DELTA_TMP/delta_$TODAY.jsonl.gz"
SSH="ssh -o ConnectTimeout=30 -o StrictHostKeyChecking=accept-new $SERVER_USER@$SERVER_IP"
SCP="scp -o ConnectTimeout=30 -o StrictHostKeyChecking=accept-new"

log() { echo "[$(date '+%F %T')] $*"; }

# remote_retry 远程操作重试包装：云端夜间研究满负荷时 sshd 会瞬时 banner 超时
# （今日两度实遇——预检过了、中途读库仍可能撞上），3 次 × 30s；全败才返回非零，
# 让 set -e 终止（delta 已保留，直接重跑安全）。
# English: retry wrapper for every remote call — sshd banner timeouts recur while the nightly
# research saturates the box; 3 tries x 30s before giving up (idempotent rerun-safe).
remote_retry() {
    local desc="$1"; shift
    local attempt
    for attempt in 1 2 3; do
        if "$@"; then
            return 0
        fi
        log "远程操作失败(第${attempt}/3次): ${desc}，30s 后重试..."
        sleep 30
    done
    log "远程操作连续 3 次失败: ${desc}"
    return 1
}

# ── 0. 前置检查 ──
log "[0/6] 前置检查..."
[ -f "$LOCAL_DB" ] || { log "本地库不存在: $LOCAL_DB"; exit 1; }
# SSH 预检带重试：云端夜间研究满负荷时 sshd 可能瞬时超时（今日实遇），3 次×20s
# English: SSH precheck with retries — sshd can time out transiently while the nightly research
# saturates the box (seen live); 3 tries × 20s.
SSH_OK=0
for i in 1 2 3; do
    if $SSH "echo ok" >/dev/null 2>&1; then SSH_OK=1; break; fi
    log "SSH 第${i}次不可达，20s 后重试..."
    sleep 20
done
[ "$SSH_OK" = "1" ] || { log "SSH 连续 3 次不可达: $SERVER_USER@$SERVER_IP"; exit 1; }

# dataload 二进制：DATALOAD_BIN 环境变量优先（launchd agent 场景：运行时副本在
# Application Support 下，不依赖仓库/Go），其次 PATH、/tmp/dataload-local，最后现场编译。
# English: dataload resolution — DATALOAD_BIN wins (agent scenario), then PATH,
# /tmp/dataload-local, and finally an in-repo go build as last resort.
DATALOAD="${DATALOAD_BIN:-}"
if [ -z "$DATALOAD" ]; then
    DATALOAD="$(command -v dataload || true)"
fi
[ -z "$DATALOAD" ] && [ -x /tmp/dataload-local ] && DATALOAD=/tmp/dataload-local
if [ -z "$DATALOAD" ]; then
    log "编译本地 dataload..."
    DATALOAD=/tmp/dataload-local
    (cd "$ROOT" && go build -o "$DATALOAD" ./cmd/dataload)
fi

mkdir -p "$DELTA_TMP"

# ── 1. 本地 pydata sidecar（未监听则启动）──
log "[1/6] 检查本地 pydata sidecar (:${PYDATA_PORT})..."
if ! lsof -iTCP:"$PYDATA_PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    log "启动 pydata sidecar..."
    nohup python3 "${PYDATA_SERVER:-$ROOT/cmd/pydata/server.py}" --host 127.0.0.1 --port "$PYDATA_PORT" \
        >"$DELTA_TMP/pydata.log" 2>&1 &
    for i in $(seq 1 30); do
        curl -s --max-time 2 "http://127.0.0.1:${PYDATA_PORT}/health" >/dev/null 2>&1 && break
        sleep 1
    done
    curl -s --max-time 2 "http://127.0.0.1:${PYDATA_PORT}/health" >/dev/null || { log "pydata 启动失败，见 $DELTA_TMP/pydata.log"; exit 1; }
fi
PYURL="http://127.0.0.1:${PYDATA_PORT}"

# ── 2. 本地增量下载（baostock 断点续传：库里最新日之后才拉）──
log "[2/6] 本地 dataload daily..."
"$DATALOAD" --db "$LOCAL_DB" --pyurl "$PYURL" daily || { log "daily 失败（保留现场，重跑幂等）"; exit 1; }
if [ "$ADJFACTOR_ENABLED" = "1" ]; then
    log "[2/6] 本地 dataload adjfactor..."
    "$DATALOAD" --db "$LOCAL_DB" --pyurl "$PYURL" adjfactor || log "adjfactor 失败（不阻断，下次再补）"
fi

# ── 3. 读云端各表 max 日期 ──
log "[3/6] 读云端各表 max 日期..."
CLOUD_MAX_JSON=$(remote_retry "读云端各表 max" $SSH "python3 << 'PYEOF'
import sqlite3, json
db = sqlite3.connect('file:${QUANT_DATA_DIR}/trading.db?mode=ro', uri=True, timeout=10)
out = {}
for t, c in [('daily','trade_date'),('adj_factor','trade_date'),('daily_basic','trade_date'),
             ('stk_limit','trade_date'),('index_daily','trade_date'),
             ('fina_indicator','end_date'),('income','end_date'),('cashflow','end_date')]:
    try:
        out[t] = db.execute(f'SELECT COALESCE(MAX({c}),\"\") FROM {t}').fetchone()[0]
    except Exception:
        out[t] = ''
db.close()
print(json.dumps(out))
PYEOF")
log "云端 max: $CLOUD_MAX_JSON"

# 取全部表的最早 max 作为 since（保证每张表都覆盖到缺口）；空表回退 START_DATE
START_DATE="${START_DATE:-20200101}"
SINCE=$(python3 -c "
import json, sys
d = json.loads('''$CLOUD_MAX_JSON''')
vals = [v for v in d.values() if v]
print(min(vals) if vals else '$START_DATE')
")

# ── 4. 本地导出增量 ──
log "[4/6] 导出增量 (since=$SINCE)..."
"$DATALOAD" --db "$LOCAL_DB" export-delta --since "$SINCE" --out "$DELTA_FILE" || { log "export 失败"; exit 1; }
SIZE=$(du -h "$DELTA_FILE" | cut -f1 | tr -d ' ')
log "delta 文件: $DELTA_FILE ($SIZE)"

# 无增量（文件只有元数据两行且体积很小也照传——幂等无害；这里仅日志提示）
if grep -q "完成：0 行" "$DELTA_TMP/.last_export" 2>/dev/null; then
    log "提示: 上次导出为 0 行"
fi

# ── 5. 上传 + 云端导入 ──
log "[5/6] 上传并云端导入..."
remote_retry "scp 上传 delta" $SCP "$DELTA_FILE" "$SERVER_USER@$SERVER_IP:/tmp/" \
    || { log "scp 失败（delta 已保留: ${DELTA_FILE}）"; exit 1; }
remote_retry "云端 import-delta" $SSH "$CLOUD_BIN --db ${QUANT_DATA_DIR}/trading.db import-delta --file /tmp/$(basename "$DELTA_FILE")" \
    || { log "云端 import 失败（delta 已保留: ${DELTA_FILE}）"; exit 1; }
remote_retry "清理云端临时文件" $SSH "rm -f /tmp/$(basename "$DELTA_FILE")"

# ── 6. 校验输出 ──
log "[6/6] 校验..."
AFTER=$(remote_retry "同步后校验读云端 max" $SSH "python3 << 'PYEOF'
import sqlite3, json
db = sqlite3.connect('file:${QUANT_DATA_DIR}/trading.db?mode=ro', uri=True, timeout=10)
out = {}
for t, c in [('daily','trade_date'),('adj_factor','trade_date'),('daily_basic','trade_date'),
             ('stk_limit','trade_date'),('index_daily','trade_date')]:
    out[t] = db.execute(f'SELECT COALESCE(MAX({c}),\"\") FROM {t}').fetchone()[0]
db.close()
print(json.dumps(out))
PYEOF")
log "同步后云端 max: $AFTER"
rm -f "$DELTA_TMP"/delta_*.jsonl.gz.tmp
# 保留最近 3 份 delta 文件
ls -t "$DELTA_TMP"/delta_*.jsonl.gz 2>/dev/null | tail -n +4 | xargs rm -f 2>/dev/null || true
log "✅ 同步完成 since=$SINCE delta=$DELTA_FILE($SIZE)"
