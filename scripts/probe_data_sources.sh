#!/bin/bash
# 只读连通性探测：从当前主机逐个请求本项目依赖的全部大陆行情/资讯数据源，
# 输出 HTTP 状态码 + 首字节/总耗时 + 是否疑似被地域风控。
# 用途：首尔(或其他海外)服务器上线前，判定新浪/东财/同花顺/腾讯/财联社是否可达。
# English: read-only connectivity probe against every mainland-China data source the app
# depends on, printing HTTP status + latency + a geo-block suspicion flag. Run it ON the
# target server BEFORE launch to decide whether a mainland relay/proxy is required.
set -u

# 统一浏览器请求头（与 internal/data 各客户端保持一致，绕过 CDN 无头请求风控）
# English: browser-like headers matching internal/data clients, to bypass headless-request filters.
UA="Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.6099.144 Mobile Safari/537.36"
ACCEPT="application/json, text/plain, */*"
ACCEPT_HTML="text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

# 结果汇总：最多 6 个来源，超时视为 FAIL。内置 1 次重试以规避瞬时空回复误报。
# probe <标签> <URL> [Referer] [Accept]
probe() {
    local label="$1" url="$2" ref="${3:-}" accept="${4:-$ACCEPT}"
    local curl_args=(-sS -o /dev/null -m 12 -w "%{http_code} %{time_total}" -H "User-Agent: $UA" -H "Accept: $accept")
    if [ -n "$ref" ]; then curl_args+=(-H "Referer: $ref"); fi
    local out code t rc
    out=$(curl "${curl_args[@]}" "$url" 2>/dev/null); rc=$?
    if [ "$rc" = "52" ]; then
        # curl 52 = empty reply from server（CDN 边缘直接断开，非超时）
        out=$(curl "${curl_args[@]}" "$url" 2>/dev/null); rc=$?   # 重试一次
    fi
    [ -z "$out" ] && out="000 0"
    code="${out%% *}" t="${out##* }"
    local verdict="OK"
    case "$code" in
        200|206|301|302) verdict="OK" ;;
        403|404|451|503) verdict="BLOCKED(疑被地域/UA风控)" ;;
        000)
            if [ "$rc" = "52" ]; then verdict="EMPTY-REPLY(CDN直断)"
            else verdict="TIMEOUT/不可达"; fi ;;
        *) verdict="异常($code)" ;;
    esac
    printf "  %-22s HTTP=%-4s 耗时=%-6ss  %s\n" "$label" "$code" "$t" "$verdict"
}

echo "=============================================================="
echo " 量化系统数据源连通性探测 (运行于: $(uname -s)/$(hostname 2>/dev/null))"
echo " 时间: $(date '+%F %T %Z')"
echo "=============================================================="

echo ""
echo "[1/6] 新浪 实时行情 (hq.sinajs.cn)"
probe "新浪-实时行情" "https://hq.sinajs.cn/list=sh600519" "https://finance.sina.com.cn"

echo ""
echo "[2/6] 新浪 日K线 (money.finance.sina.com.cn)"
probe "新浪-日K线" "https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData?symbol=sh600519&scale=240&ma=no&datalen=10"

echo ""
echo "[3/6] 新浪 新闻快讯 (feed.mix.sina.com.cn)"
probe "新浪-快讯" "https://feed.mix.sina.com.cn/api/roll/get?pageid=153&lid=2516&knum=5"

echo ""
echo "[4/6] 东财 实时行情+涨停池 (push2.eastmoney.com)"
probe "东财-实时行情" "https://push2.eastmoney.com/api/qt/stock/get?secid=1.600519&fields=f43,f44,f45,f46,f47,f48,f49,f50,f51,f52,f55,f57,f58,f60,f62,f116,f117,f167,f168,f169,f170,f171,f292" "https://quote.eastmoney.com/"
probe "东财-板块成分股" "https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=10&po=1&np=1&fs=m:90+t:2&fields=f12,f14,f2,f3" "https://quote.eastmoney.com/"
probe "东财-涨停池" "https://push2ex.eastmoney.com/getTopicZTPool?ut=7eea3edcaed734bea9cbfc24409ed989&dpt=wz.ztzt&Pageindex=0&pagesize=10&sort=fbt:asc" "https://quote.eastmoney.com/"

echo ""
echo "[5/6] 东财 数据中心(龙虎榜/IPO) (datacenter-web.eastmoney.com)"
probe "东财-数据中心" "https://datacenter-web.eastmoney.com/api/data/v1/get?reportName=RPTA_APP_IPOAPPLY&columns=ALL&pageNumber=1&pageSize=5&sortTypes=-1&sortColumns=LISTING_DATE" "https://data.eastmoney.com/"

echo ""
echo "[6/6] 同花顺 板块名单+成分股 (q.10jqka.com.cn / d.10jqka.com.cn)"
probe "同花顺-板块名单" "https://q.10jqka.com.cn/thshy/" "https://q.10jqka.com.cn/" "$ACCEPT_HTML"
probe "同花顺-实时行情" "https://d.10jqka.com.cn/v2/realhead/hs_600519/last.js" "https://q.10jqka.com.cn/" "$ACCEPT_HTML"

echo ""
echo "[7] 腾讯 K线降级源 (ifzq.gtimg.cn / web.ifzq.gtimg.cn)"
probe "腾讯-日K线" "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=sh600519,day,,,10,qfq"
probe "腾讯-分钟K线" "https://ifzq.gtimg.cn/appstock/app/kline/mkline?param=sh600519,m5,,10"

echo ""
echo "[8] 财联社 电报 (www.cls.cn)"
probe "财联社-电报" "https://www.cls.cn/telegraph" "" "$ACCEPT_HTML"

echo ""
echo "=============================================================="
echo " 判定建议:"
echo "   - 全部 OK 且耗时 <1s  → 可直接部署, 无需中转"
echo "   - EMPTY-REPLY/TIMEOUT 且本地也复现 → 该源 CDN 本身不稳, 依赖降级链兜底"
echo "   - 海外出现 BLOCKED/TIMEOUT 而本地正常 → 需境内中转代理"
echo "   - 延迟普遍 >2s → 建议在数据层加 HTTP 代理(经境内转发)"
echo "=============================================================="