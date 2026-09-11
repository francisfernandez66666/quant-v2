#!/bin/bash
# 生产诊断脚本——「利空归因卖出」发版后三类症状（信号空/实盘空/首页推荐空）。
# 用法：在生产机仓库根目录或部署目录执行本脚本，产出诊断摘要。
# 不做任何修改，只读日志/接口/进程/配置。

echo "════ 1. 进程状态 ════"
pgrep -fl 'quant|quantd|go run' | head -5
echo "（若有多个进程在跑，可能有新旧实例互踩）"

echo ""
echo "════ 2. 今日关键日志扫描 ════"
# 找日志文件（按你的实际路径调整，也可把 $LOG 传进来）
LOG=${LOG:-$(ls -t nohup.out monitor_logs/*.log /var/log/quant*.log 2>/dev/null | head -1)}
echo "日志文件: $LOG"
if [ -n "$LOG" ]; then
  echo "-- 2.1 自动卖出/成交（补货/清仓）:"
  grep -E '成交回报|自动卖出|策略卖出|news_bear|autoExecute|利空(清仓|减仓|抛售)' "$LOG" | tail -10
  echo "-- 2.2 D1/LLM 降级:"
  grep -E 'LLM降级|D1 LLM 评分失败|评分失败|待重试' "$LOG" | tail -10
  echo "-- 2.3 panic/err:"
  grep -E 'panic|ERROR|err =' "$LOG" | tail -10
else
  echo "!! 未自动找到日志，请手动用 LOG=/path/to/log 重新执行"
fi

echo ""
echo "════ 3. 后端接口抽检（默认端口 8080，可 PORT=xxx 覆盖）════"
PORT=${PORT:-8080}
echo "-- 3.1 信号列表:"
curl -s "http://127.0.0.1:$PORT/api/signals" | head -c 400; echo
echo "-- 3.2 实盘持仓:"
curl -s "http://127.0.0.1:$PORT/api/real/positions" | head -c 400; echo
echo "-- 3.3 D1/推荐:"
curl -s "http://127.0.0.1:$PORT/api/d1/scores" | head -c 400; echo
curl -s "http://127.0.0.1:$PORT/api/news" | head -c 300; echo

echo ""
echo "════ 4. config 兼容性 ════"
CONFIG=${CONFIG:-config.json}
[ -f "$CONFIG" ] && python3 - "$CONFIG" <<'EOF'
import json,sys
c=json.load(open(sys.argv[1]))
rb=c.get('rules',{}).get('bear_news','<缺失>')
print("rules.bear_news =", rb)
EOF
echo "（bear_news 段缺失应自动回退默认；若为 null/畸形会打印在上方）"
echo "诊断结束"
