# b9_metrics.py — §B9 账本测算：T 腿与"盘中时效"到底贡献多少钱（现网只读，经 stdin 执行，不落文件）。
#
# 为什么用 VWAP 溢价而不是"假设收盘执行"：`fills` 里已经落库了每笔成交当日的市场 VWAP
# （`market_vwap`）与本笔相对它的溢价（`fill_vwap_premium_pct`），这是系统自己算好、有出处的事实；
# "如果改成尾盘成交会怎样"需要重放行情，属于模型假设，不能拿来当决策依据。
#
# 三条纪律：① 一律 `mode=ro` URI，无任何写路径；② 输出纯 ASCII（现网 GBK 控制台，中文会变 `?`，
# 11-02 两次假象的根因）；③ 样本量必须自己印出来 —— 只有 5 个交易日的账，不足以支撑"砍掉一条腿"的结论时，
# 报告必须说"样本不足"，不许把小样本读数当判决。
#
# English: read-only B9 ledger metrics over live.db: T-round P&L, timing value measured against the
# recorded market VWAP, signal-to-fill latency, and blocked-decision tally. Prints its own sample size.
import json
import os
import sqlite3
from collections import defaultdict

DATA = r"C:\var\lib\quant-trading-v2"
DB = os.path.join(DATA, "live.db")


def ro(db):
    con = sqlite3.connect("file:" + db + "?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    return con


def main():
    con = ro(DB)
    cur = con.cursor()
    cur.execute("SELECT MIN(traded_at), MAX(traded_at), COUNT(*) FROM fills")
    lo, hi, nfill = cur.fetchone()
    print("SPAN fills %s .. %s rows=%d" % (lo, hi, nfill))

    # 1) 总量与费用
    cur.execute("SELECT side, COUNT(*), COALESCE(SUM(amount),0), COALESCE(SUM(fee),0), "
                "COALESCE(SUM(stamp_tax),0) FROM fills GROUP BY side")
    tot_fee = 0.0
    for r in cur.fetchall():
        fee = (r[3] or 0) + (r[4] or 0)
        tot_fee += fee
        print("FILLS %s n=%d amount=%.2f fee=%.2f" % (r[0], r[1], r[2] or 0, fee))

    # 2) 执行时机质量：Σ(成交价 - 当日市场VWAP) * qty，正=买贵/卖好，按买卖与 T/非T 分账
    cur.execute("SELECT side, COALESCE(SUM((price - market_vwap) * qty),0), COUNT(*) "
                "FROM fills WHERE market_vwap IS NOT NULL AND market_vwap > 0 GROUP BY side")
    timing_total = 0.0
    have_vwap = 0
    for r in cur.fetchall():
        sign = 1.0 if r[0] == "buy" else -1.0   # 买贵是成本，卖好是收益
        contrib = sign * (r[1] or 0)
        timing_total += contrib
        have_vwap += r[2]
        print("TIMING side=%s benefit=%.2f n=%d  (benefit>0 = timing paid)" % (r[0], contrib, r[2]))
    print("TIMING total_benefit=%.2f 覆盖率=%d/%d" % (timing_total, have_vwap, nfill))

    # T 与非 T 分开：orders.intraday_t 标记按 order_id 关联
    cur.execute("SELECT COALESCE(o.intraday_t,0), COUNT(*), "
                "COALESCE(SUM((f.price - f.market_vwap) * f.qty),0) FROM fills f "
                "LEFT JOIN orders o ON o.order_id = f.order_id "
                "WHERE f.market_vwap IS NOT NULL AND f.market_vwap > 0 GROUP BY COALESCE(o.intraday_t,0)")
    for r in cur.fetchall():
        print("TIMING_BY_T intraday_t=%s n=%d raw=%.2f" % (r[0], r[1], r[2] or 0))

    # 3) T 回合账（帧文件）：完成/在途/亏损分布
    for fp in sorted([os.path.join(dp, f) for dp, _, fs in os.walk(os.path.join(DATA, "accounts"))
                      for f in fs if f.startswith("t_rounds")]):
        try:
            obj = json.loads(open(fp, "rb").read().decode("utf-8", "replace"))
        except Exception as e:
            print("TFRAME read_err %s %s" % (fp, repr(e)[:80]))
            continue
        rounds = obj.get("rounds") if isinstance(obj, dict) else None
        print("TFRAME %s day=%s rounds=%s cost=%s funds=%s" % (
            os.path.basename(os.path.dirname(fp))[-8:], obj.get("day") if isinstance(obj, dict) else "?",
            len(rounds) if isinstance(rounds, list) else "?",
            (obj.get("cost") if isinstance(obj, dict) else "?"),
            "yes" if isinstance(obj, dict) and obj.get("funds") else "no"))
        if isinstance(rounds, list) and rounds:
            done = [r for r in rounds if str(r.get("status", "")) == "done"]
            pnl = [float(r.get("pnl", 0) or 0) for r in done]
            print("   TROUND done=%d/%d pnl_sum=%.2f win=%d loss=%d" % (
                len(done), len(rounds), sum(pnl), len([x for x in pnl if x > 0]), len([x for x in pnl if x < 0])))
            k = sorted(rounds[-1].keys())
            print("   TROUND_KEYS %s" % ",".join(k)[:300])

    # 4) 信号 -> 成交延迟（秒），以及"当天没成交的信号"占比
    cur.execute("SELECT COUNT(*), SUM(CASE WHEN f.traded_at IS NOT NULL THEN 1 ELSE 0 END) "
                "FROM signal_decisions d LEFT JOIN fills f ON f.signal_id = d.signal_id")
    nsig, nfilled = cur.fetchone()
    print("SIGNAL n=%d filled_via_signal_id=%d unmatched=%d" % (nsig, nfilled or 0, nsig - (nfilled or 0)))

    cur.execute("SELECT capped_by, COUNT(*) FROM signal_decisions GROUP BY capped_by ORDER BY COUNT(*) DESC")
    for r in cur.fetchall():
        print("CAPPED_BY %s n=%d" % (r[0] if r[0] else "(空=未拦)", r[1]))
    cur.execute("SELECT day, SUM(CASE WHEN missing_key_input=1 THEN 1 ELSE 0 END), COUNT(*) "
                "FROM signal_decisions GROUP BY day ORDER BY day")
    for r in cur.fetchall():
        print("BLIND day=%s missing_key_rows=%s/%s" % (r[0], r[1], r[2]))

    # 5) 状态分布：撤单中/已报 这些需要人收摊的形态
    cur.execute("SELECT status, COUNT(*) FROM orders GROUP BY status ORDER BY COUNT(*) DESC")
    for r in cur.fetchall():
        print("ORDER_STATUS %s n=%d" % (r[0], r[1]))
    cur.execute("SELECT COUNT(*) FROM orders WHERE status IN ('撤单中','已报')")
    print("STUCK_CANDIDATES n=%d" % cur.fetchone()[0])
    con.close()
    print("B9_METRICS_DONE sample_days_note=只有数个交易日，小样本要如实标注")


main()
