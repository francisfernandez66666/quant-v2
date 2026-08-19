#!/bin/bash
# 本地全链路回测脚本：对研究库中发现的战法候选（因子组合）在历史数据上做全链路回测，
# 验证其在"板块涨停潮事件"上的前瞻超额表现，评估战法是否真正可盈利。
#
# 流程：
#   1) 确认研究库存在
#   2) 重建板块历史 sector-rebuild（板块共振数据，供回测事件合成）
#   3) 读取候选因子组合（research_candidates 的 kind=factor）
#   4) 对每个候选组合跑 replay chain 全链路回测，输出 JSON/HTML 报告
#   5) 汇总各组合的超额/命中率
#
# 用法：bash /Users/zhangzifei/Desktop/quant-trading-v2/scripts/run_backtest_local.sh
#   环境变量 QUANT_DB 可覆盖研究库路径
set -u

ROOT=/Users/zhangzifei/Desktop/quant-trading-v2
DB="${QUANT_DB:-/Users/zhangzifei/.quant-trading-v2/trading.db}"
RESEARCH="$ROOT"/cmd/replay  # replay chain 入口（用 go run 或预编译二进制）
START="${QUANT_BT_START:-20230801}"   # 近3年回测
END=$(date +%Y%m%d)
HORIZON="${QUANT_BT_HORIZON:-5}"       # 前瞻天数
OUTDIR="${QUANT_BT_OUT:-/tmp/bt_local}"
LOG=/tmp/backtest_local.log

echo "===== $(date '+%F %T') 本地全链路回测开始 区间 $START~$END =====" | tee -a "$LOG"

# 0) 检查研究库
if [ ! -f "$DB" ]; then
    echo "研究库不存在: $DB（请先下载云端 trading.db）" | tee -a "$LOG"
    exit 1
fi
echo "研究库: $DB" | tee -a "$LOG"
python3 -c "import sqlite3; db=sqlite3.connect('file:$DB?mode=ro',uri=True,timeout=5); c=db.cursor(); print('daily:', c.execute('SELECT COUNT(*) FROM daily').fetchone()[0]); print('ready:', c.execute('SELECT COUNT(DISTINCT ts_code) FROM daily').fetchone()[0]); print('fina:', c.execute('SELECT COUNT(*) FROM fina_indicator').fetchone()[0]); print('cands:', c.execute('SELECT COUNT(*) FROM research_candidates').fetchone()[0]); db.close()" | tee -a "$LOG"

# 1) 重建板块历史（供回测事件合成；用 research sector-rebuild）
echo "" | tee -a "$LOG"
echo "----- [1] 重建板块历史 sector-rebuild -----" | tee -a "$LOG"
if [ -f /tmp/research-local ]; then
    /tmp/research-local --db "$DB" sector-rebuild --start "$START" --end "$END" >> "$LOG" 2>&1
    echo "sector-rebuild 退出码: $?" | tee -a "$LOG"
else
    echo "（跳过：未找到 research-local 二进制，板块历史需已存在）" | tee -a "$LOG"
fi

# 2) 读取候选因子组合
echo "" | tee -a "$LOG"
echo "----- [2] 读取候选因子组合 -----" | tee -a "$LOG"
# 从 research_candidates 读取 kind=factor 的 factors 字段（JSON 数组）
# 用 python 提取，生成"组合名|因子列表"清单
COMBOS=$(python3 -c "
import sqlite3, json
db=sqlite3.connect('file:$DB?mode=ro',uri=True,timeout=5)
c=db.cursor()
rows=c.execute(\"SELECT id,factors,reason FROM research_candidates WHERE kind='factor' ORDER BY id\").fetchall()
for cid, factors_raw, reason in rows:
    try:
        factors=json.loads(factors_raw)
        print('%d|%s' % (cid, ','.join(factors)))
    except Exception:
        pass
db.close()
")
if [ -z "$COMBOS" ]; then
    echo "无 factor 候选，使用默认因子组合（EP_ttm,BP,ROE,YoyNetProfit,SUE,Mom20,STO20）" | tee -a "$LOG"
    COMBOS="default|EP_ttm,BP,ROE,YoyNetProfit,SUE,Mom20,STO20"
fi
echo "$COMBOS" | tee -a "$LOG"

# 3) 对每个组合跑 replay chain 全链路回测
mkdir -p "$OUTDIR"
echo "" | tee -a "$LOG"
echo "----- [3] 全链路回测 -----" | tee -a "$LOG"
while IFS='|' read -r cid factors; do
    name="${cid:-default}"
    echo "" | tee -a "$LOG"
    echo "--- 组合 #$name ($factors) ---" | tee -a "$LOG"
    (cd "$ROOT" && go run ./cmd/replay --db "$DB" --start "$START" --end "$END" \
        --horizon "$HORIZON" --top-k 5 --min-stocks 10 \
        --factors "$factors" --out "$OUTDIR/bt_$name" chain) >> "$LOG" 2>&1
    echo "回测 #$name 退出码: $?" | tee -a "$LOG"
done <<< "$COMBOS"

# 4) 汇总
echo "" | tee -a "$LOG"
echo "----- [4] 回测汇总 -----" | tee -a "$LOG"
# 从各 report.json 提取 5 日超额与命中率
for f in "$OUTDIR"/bt_*/report.json; do
    [ -f "$f" ] || continue
    name=$(basename "$(dirname "$f")")
    python3 -c "
import json
try:
    r=json.load(open('$f'))
    ae=r.get('avg_excess',{})
    oh=r.get('overall_hit',{})
    print('%s: 事件=%d 选股=%d 5日超额=%s 5日命中=%s' % ('$name', r.get('total_events'), r.get('total_picks'), ae.get('5','-'), oh.get('5','-')))
except Exception as e:
    print('$name: 解析失败 %s' % e)
" | tee -a "$LOG"
done

echo "" | tee -a "$LOG"
echo "===== $(date '+%F %T') 本地全链路回测结束 =====" | tee -a "$LOG"
