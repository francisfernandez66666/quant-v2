#!/bin/bash
# 补数据 + 自动研究 主控脚本（后台一次跑完，不占用交互资源）：
#   1) 等待财务装载（load_finance.py）完成
#   2) 确认行业分类已补（patch_industry.py，若未执行则补）
#   3) 重建板块历史 sector-rebuild
#   4) 因子战法自动发现 discover-factors
#   5) 形态战法自动发现 discover-patterns
#   6) 汇总候选
# 用法：nohup bash /Users/zhangzifei/Desktop/quant-trading-v2/scripts/run_auto_research_full.sh > /tmp/auto_full.log 2>&1 &
set -u

ROOT=/Users/zhangzifei/Desktop/quant-trading-v2
DB=/Users/zhangzifei/.quant-trading-v2/trading.db
RESEARCH=/tmp/research-local
POOL=/tmp/research_pool.txt
START=20230801
END=$(date +%Y%m%d)
LOG=/tmp/auto_full.log

echo "===== $(date '+%F %T') 补数据+自动研究 开始 =====" | tee -a "$LOG"

# 1) 等财务装载完成（load_finance.py 在跑则等，跑完才继续）
echo "" | tee -a "$LOG"
echo "----- [等待] 财务装载完成 -----" | tee -a "$LOG"
while ps aux | grep "load_finance.py" | grep -v grep >/dev/null 2>&1; do
    sleep 20
done
echo "$(date '+%F %T') 财务装载进程已结束，校验数据…" | tee -a "$LOG"
python3 -c "
import sqlite3
db=sqlite3.connect('$DB')
print('fina_indicator 行数:', db.execute('SELECT COUNT(*) FROM fina_indicator').fetchone()[0])
print('income 行数:', db.execute('SELECT COUNT(*) FROM income').fetchone()[0])
print('有行业 stocks:', db.execute('SELECT COUNT(*) FROM stocks WHERE industry IS NOT NULL AND industry!=\'\'').fetchone()[0])
db.close()
" | tee -a "$LOG"

# 2) 补行业分类（若 stocks.industry 仍为空则执行 patch_industry）
echo "" | tee -a "$LOG"
echo "----- [检查/补] 行业分类 -----" | tee -a "$LOG"
python3 - "$DB" <<'PY' | tee -a "$LOG"
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
n = db.execute("SELECT COUNT(*) FROM stocks WHERE industry IS NOT NULL AND industry!=''").fetchone()[0]
print("当前有行业 stocks:", n)
db.close()
PY
if ! python3 -c "import sqlite3,sys; print(sqlite3.connect('$DB').execute(\"SELECT COUNT(*) FROM stocks WHERE industry IS NOT NULL AND industry!=''\").fetchone()[0])" | grep -q "^[1-9]"; then
    echo "行业为空，执行 patch_industry.py…" | tee -a "$LOG"
    python3 "$ROOT/scripts/patch_industry.py" "$DB" | tee -a "$LOG"
fi

# 3) 重建板块历史（近3年）
echo "" | tee -a "$LOG"
echo "----- [1/4] sector-rebuild 重建板块历史 -----" | tee -a "$LOG"
"$RESEARCH" --db "$DB" sector-rebuild --start "$START" --end "$END" >> "$LOG" 2>&1
echo "sector-rebuild 退出码: $?" | tee -a "$LOG"

# 4) 因子战法自动发现
echo "" | tee -a "$LOG"
echo "----- [2/4] discover-factors 因子战法自动发现 -----" | tee -a "$LOG"
"$RESEARCH" --db "$DB" discover-factors \
  --start "$START" --end "$END" \
  --h 5 --min-stocks 20 --max-factors 8 --split 0.7 \
  --min-ir 0.3 --min-days 30 \
  >> "$LOG" 2>&1
echo "discover-factors 退出码: $?" | tee -a "$LOG"

# 5) 形态战法自动发现
echo "" | tee -a "$LOG"
echo "----- [3/4] discover-patterns 形态战法自动发现 -----" | tee -a "$LOG"
"$RESEARCH" --db "$DB" discover-patterns \
  --start "$START" --end "$END" \
  --h 5 --min-trigger 20 --min-excess 0.01 --split 0.7 \
  >> "$LOG" 2>&1
echo "discover-patterns 退出码: $?" | tee -a "$LOG"

# 6) 汇总候选
echo "" | tee -a "$LOG"
echo "----- [4/4] 候选汇总 -----" | tee -a "$LOG"
"$RESEARCH" --db "$DB" list >> "$LOG" 2>&1

echo "" | tee -a "$LOG"
echo "===== $(date '+%F %T') 补数据+自动研究 结束 =====" | tee -a "$LOG"
