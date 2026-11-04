# b9_metrics2.py — §B9 第二遍：把第一遍看不懂的列值改成 ASCII 转义输出，并查三件数据质量问题。
#
# 第一遍留下的三个问题（每一个都会改变结论，不能猜）：
#   ① 中文列值（side/status）经现网 GBK 控制台回传变成 '?'，判决读不出来 => 一律 ascii() 转义；
#   ② fills.market_vwap 只有 1/141 笔有值 => VWAP 时机口径在这份账上不成立，得说"覆盖不足"而不是硬算；
#   ③ fills.signal_id 与 signal_decisions.signal_id join 命中 0 => "哪个信号变成了哪笔成交"在账本上
#      目前不可回溯，这本身就是内化决策要用的事实（不是算不出收益，是算不出归因）。
# 只读：mode=ro；纯 ASCII 输出；不落任何文件。
import json
import os
import sqlite3
from collections import Counter, defaultdict

DATA = r"C:\var\lib\quant-trading-v2"
DB = os.path.join(DATA, "live.db")


def a(v):
    return ascii(v if v is not None else "(null)")


def main():
    con = sqlite3.connect("file:" + DB + "?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    cur = con.cursor()

    cur.execute("SELECT side, COUNT(*), COALESCE(SUM(amount),0), COALESCE(SUM(fee+stamp_tax),0) "
                "FROM fills GROUP BY side")
    for r in cur.fetchall():
        print("FILL_SIDE %s n=%d amount=%.2f fee=%.2f" % (a(r[0]), r[1], r[2] or 0, r[3] or 0))

    cur.execute("SELECT status, COUNT(*) FROM orders GROUP BY status ORDER BY COUNT(*) DESC")
    for r in cur.fetchall():
        print("ORDER_STATUS %s n=%d" % (a(r[0]), r[1]))

    # ② VWAP 覆盖率
    cur.execute("SELECT COUNT(*), SUM(CASE WHEN market_vwap IS NOT NULL AND market_vwap>0 THEN 1 ELSE 0 END) "
                "FROM fills")
    n, hv = cur.fetchone()
    print("VWAP_COVERAGE fills=%d with_vwap=%d" % (n, hv or 0))
    cur.execute("SELECT COUNT(*), SUM(CASE WHEN market_vwap IS NOT NULL AND market_vwap>0 THEN 1 ELSE 0 END) "
                "FROM orders")
    n, hv = cur.fetchone()
    print("VWAP_COVERAGE orders=%d with_vwap=%d" % (n, hv or 0))

    # ③ signal_id 可回溯性
    cur.execute("SELECT COUNT(*), SUM(CASE WHEN signal_id IS NULL OR signal_id='' THEN 1 ELSE 0 END) FROM fills")
    n, empty = cur.fetchone()
    print("FILL_SIGNAL_ID fills=%d empty=%d" % (n, empty or 0))
    cur.execute("SELECT DISTINCT signal_id FROM fills WHERE signal_id IS NOT NULL AND signal_id<>'' LIMIT 8")
    print("FILL_SID_SAMPLES " + " | ".join(a(r[0]) for r in cur.fetchall()))
    cur.execute("SELECT DISTINCT signal_id FROM signal_decisions WHERE signal_id IS NOT NULL LIMIT 8")
    print("DECISION_SID_SAMPLES " + " | ".join(a(r[0]) for r in cur.fetchall()))
    cur.execute("SELECT COUNT(*) FROM signal_decisions d WHERE EXISTS("
                "SELECT 1 FROM fills f WHERE f.signal_id=d.signal_id)")
    print("JOIN_STRICT n=%d" % cur.fetchone()[0])
    cur.execute("SELECT COUNT(DISTINCT d.signal_id) FROM signal_decisions d JOIN fills f "
                "ON substr(f.signal_id,1,24)=substr(d.signal_id,1,24)")
    print("JOIN_PREFIX24 n=%d" % cur.fetchone()[0])

    # ④ 按 code+day 的手工配对（不依赖 signal_id）：T 腿与波段各自的一进一出
    cur.execute("SELECT code, side, price, qty, amount, substr(traded_at,1,10), fee+stamp_tax, user_id "
                "FROM fills ORDER BY traded_at")
    by = defaultdict(lambda: {"buy_qty": 0, "buy_amt": 0.0, "sell_qty": 0, "sell_amt": 0.0, "fee": 0.0})
    days = Counter()
    for r in cur.fetchall():
        code, side = r[0], (r[1] or "")
        k = (code, r[5])
        days[r[5]] += 1
        b = by[k]
        s = "buy" if ("buy" in side.lower() or u"\u4e70" in side or u"\u5165" in side) else "sell"
        if s == "buy":
            b["buy_qty"] += r[3] or 0
            b["buy_amt"] += r[4] or 0
        else:
            b["sell_qty"] += r[3] or 0
            b["sell_amt"] += r[4] or 0
        b["fee"] += r[6] or 0
    print("FILL_DAYS distinct=%d total_rows=%d" % (len(days), sum(days.values())))
    matched = 0
    paired_pnl = 0.0
    open_qty_cost = 0.0
    for (code, day), b in sorted(by.items()):
        q = min(b["buy_qty"], b["sell_qty"])
        if q > 0:
            matched += 1
            bp = b["buy_amt"] / max(1, b["buy_qty"])
            sp = b["sell_amt"] / max(1, b["sell_qty"])
            paired_pnl += (sp - bp) * q - b["fee"]
        left = b["buy_qty"] - b["sell_qty"]
        if left > 0:
            open_qty_cost += (b["buy_amt"] / max(1, b["buy_qty"])) * left
    print("SAME_DAY_PAIRED code_days=%d pnl_net=%.2f" % (matched, paired_pnl))
    print("OPEN_LEFTOVER_BUY_COST=%.2f same_day_buy_not_sold_amount" % open_qty_cost)
    cur.execute("SELECT COUNT(*), COALESCE(SUM(fee+stamp_tax),0) FROM fills")
    print("ALL_FILLS fee_total=%.2f" % cur.fetchone()[1])

    # ⑤ T 标记与 intraday 关联（orders.intraday_t）
    cur.execute("SELECT COALESCE(intraday_t,0), COUNT(*), COALESCE(SUM(qty*price),0) FROM orders "
                "GROUP BY COALESCE(intraday_t,0)")
    for r in cur.fetchall():
        print("ORDERS_T flag=%s n=%d notional=%.2f" % (a(r[0]), r[1], r[2] or 0))

    # ⑥ 决策表的通道与状态（哪些是实盘/纸面）
    cur.execute("SELECT channel, COUNT(*) FROM signal_decisions GROUP BY channel")
    for r in cur.fetchall():
        print("DECISION_CHANNEL %s n=%d" % (a(r[0]), r[1]))
    cur.execute("SELECT stage, COUNT(*) FROM signal_decisions GROUP BY stage ORDER BY COUNT(*) DESC LIMIT 8")
    for r in cur.fetchall():
        print("DECISION_STAGE %s n=%d" % (a(r[0]), r[1]))

    # ⑦ T 回合帧结构（第一遍 rounds 类型没判对）
    for fp in sorted([os.path.join(dp, f) for dp, _, fs in os.walk(os.path.join(DATA, "accounts"))
                      for f in fs if f.startswith("t_rounds")]):
        try:
            obj = json.loads(open(fp, "rb").read().decode("utf-8", "replace"))
        except Exception as e:
            print("TFRAME err %s" % repr(e)[:80])
            continue
        rd = obj.get("rounds") if isinstance(obj, dict) else None
        print("TFRAME %s day=%s rounds_type=%s blocks=%s" % (
            os.path.basename(os.path.dirname(fp))[-8:], a(obj.get("day")), a(type(rd).__name__),
            len(obj.get("blocks") or []) if isinstance(obj, dict) else "?"))
        if isinstance(rd, dict):
            ks = sorted(rd.keys())
            print("   rounds_keys=%s" % a(ks[:12]))
            for k in ks[:3]:
                v = rd[k]
                print("   entry %s type=%s keys=%s" % (a(k), a(type(v).__name__),
                                                       a(sorted(v.keys())[:14]) if isinstance(v, dict) else ""))
        con2 = None
    con.close()
    print("B9_2_DONE")


main()
