#!/bin/bash
# 收盘后夜间研究作业验证脚本：检查 quant-research 是否在 15:30 后启动夜间作业、
# 研究子进程内存是否 <900M（不 OOM）、各步骤是否正常、是否产出候选。
# 用法：bash scripts/verify_nightly.sh
set -u
HOST=root@43.108.86.140
LOG=/tmp/nightly_verify.log
echo "===== $(date '+%F %T') 收盘后夜间作业验证 =====" | tee "$LOG"

# 1) 当前时间 + researchd 状态
echo "--- [1] researchd 状态 ---" | tee -a "$LOG"
ssh -o ConnectTimeout=30 "$HOST" "date '+%F %T'; systemctl is-active quant-research" 2>&1 | tee -a "$LOG"

# 2) 是否有研究作业在跑（research/dataload 子进程）
echo "" | tee -a "$LOG"
echo "--- [2] 研究/下载子进程 ---" | tee -a "$LOG"
ssh -o ConnectTimeout=30 "$HOST" "ps aux | grep -E 'research --db|dataload|sector-rebuild' | grep -v grep" 2>&1 | tee -a "$LOG"

# 3) 夜间作业日志（最近 40 行）
echo "" | tee -a "$LOG"
echo "--- [3] quant-research 最近日志 ---" | tee -a "$LOG"
ssh -o ConnectTimeout=30 "$HOST" "journalctl -u quant-research --since '15:30' --no-pager 2>/dev/null | tail -40" 2>&1 | tee -a "$LOG"

# 4) 候选产出 + 财务数据按报告期分布（§0925EVE-B1 升级：只打印 COUNT(*) 发现不了
#    "整季缺失"——旧续传键把季度序号错放月份位，导致每年 0930 期被永久跳过。
#    现按 end_date 打印每季 distinct ts_code 数，缺期一眼可见。
#    注意本仓教训：set -e 下 grep 计数管道必须 || true，勿引入静默中止；此处 python
#    段同理不做会非零退出的裸 grep。）
echo "" | tee -a "$LOG"
echo "--- [4] 候选产出 / 财务报告期分布 ---" | tee -a "$LOG"
ssh -o ConnectTimeout=30 "$HOST" "python3 -c \"
import sqlite3
db=sqlite3.connect('file:/var/lib/quant-trading-v2/trading.db?mode=ro', uri=True, timeout=5)
c=db.cursor()
print('候选:', c.execute('SELECT id,kind,status,ir,reason FROM research_candidates ORDER BY id').fetchall())
print('fina 总行数:', c.execute('SELECT COUNT(*) FROM fina_indicator').fetchone()[0])
# 按报告期分布：每季 distinct ts_code 数（规模基准取各期最大值，低于基准 80% 打警告）
dist = c.execute('SELECT end_date, COUNT(DISTINCT ts_code) FROM fina_indicator GROUP BY end_date ORDER BY end_date').fetchall()
scale = max([n for _, n in dist], default=0)
print('按报告期 distinct ts_code 分布（基准=%d）:' % scale)
for ed, n in dist:
    flag = '  !!警告: 该期显著偏少(疑似被跳过/未装完)' if scale and n < scale * 0.8 else ''
    print(' ', ed, n, flag)
db.close()
\"" 2>&1 | tee -a "$LOG"

# 5) 内存使用（当前峰值）
echo "" | tee -a "$LOG"
echo "--- [5] 内存使用 ---" | tee -a "$LOG"
ssh -o ConnectTimeout=30 "$HOST" "free -m | head -2; echo '--- research 子进程峰值 ---'; ps -eo rss,comm | grep -E 'research|dataload' | sort -rn | head -3" 2>&1 | tee -a "$LOG"

# 6) 任务队列排空检查（子系统统一改造）：活跃任务数 + 最近终态。
echo "" | tee -a "$LOG"
echo "--- [6] 任务队列 ---" | tee -a "$LOG"
ssh -o ConnectTimeout=30 "$HOST" "python3 << 'PYEOF'
import sqlite3
db = sqlite3.connect('file:/var/lib/quant-trading-v2/trading.db?mode=ro', uri=True, timeout=5)
c = db.cursor()
active = c.execute(\"SELECT id,type,priority,status,progress,updated_at FROM research_tasks WHERE status IN ('queued','running','paused','preempted') ORDER BY id\").fetchall()
print('活跃任务:', active if active else '无（已全部排空）')
last = c.execute(\"SELECT type,status,progress,error,updated_at FROM research_tasks ORDER BY updated_at DESC LIMIT 1\").fetchone()
print('最近终态:', last)
db.close()
PYEOF" 2>&1 | tee -a "$LOG"

echo "" | tee -a "$LOG"
echo "===== $(date '+%F %T') 验证结束 =====" | tee -a "$LOG"
