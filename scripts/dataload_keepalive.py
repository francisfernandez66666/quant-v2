# -*- coding: utf-8 -*-
# dataload_keepalive.py §dataload 盘后保活（每日计划任务一锤子：拉齐即退出，不常驻）。
#
# 职责（计划任务每天 17:10 触发一次，交易时段绝不自跑）：
#   1. 检测 daily / index_daily / ths_limit_up_daily 是否已到最近交易日；
#      已齐 → 立即退出（幂等，当日重复触发零成本）；
#   2. 缺则补：baostock 日线增量 → 指数日线 → hithink 涨停池（当日）
#      → hithink daily-k-10d（ths_daily 主源增量）；
#   3. 幂等（全部 INSERT OR REPLACE / upsert），未拉齐则 10 分钟一轮至多 8 轮后退出，
#      次日计划任务兜底。
#
# 状态与日志：C:\opt\quant\dataload_keepalive.log（追加）。
import os, sqlite3, subprocess, sys, time, datetime, urllib.request, csv, io as _io

# 路径按环境变量派生（QUANT_DATA_DIR=数据目录、QUANT_BIN_DIR=二进制目录，缺省同生产）。
_DATA_DIR = os.environ.get("QUANT_DATA_DIR", r"C:\var\lib\quant-trading-v2")
_BIN_DIR = os.environ.get("QUANT_BIN_DIR", r"C:\opt\quant")
DB = os.path.join(_DATA_DIR, "trading.db")
DATALOAD = os.path.join(_BIN_DIR, "dataload.exe")
PYDATA = "http://127.0.0.1:8787"
LOG = os.path.join(_BIN_DIR, "dataload_keepalive.log")
IDX = [("sh.000300", "000300.SH"), ("sh.000905", "000905.SH"), ("sh.000852", "000852.SH")]


def log(msg):
    line = "%s %s" % (datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S"), msg)
    with io_open(LOG, "a") as f:
        f.write(line + "\n")
    try:
        sys.stdout.write(line + "\n"); sys.stdout.flush()
    except Exception:
        pass


def io_open(path, mode):
    return open(path, mode, encoding="utf-8", errors="replace")


def run(cmd, timeout=7200):
    # 执行一条 dataload 命令，返回 (rc, 末行输出)。
    try:
        p = subprocess.run(cmd, capture_output=True, timeout=timeout)
        out = (p.stdout + p.stderr).decode("utf-8", "replace").strip().splitlines()
        return p.returncode, (out[-1][:160] if out else "")
    except Exception as e:
        return -1, repr(e)[:160]


def latest(table, col="trade_date"):
    try:
        c = sqlite3.connect(DB, timeout=30)
        r = c.execute("SELECT MAX(%s) FROM %s" % (col, table)).fetchone()[0]
        c.close()
        return str(r or "")
    except Exception as e:
        log("query %s err: %r" % (table, e))
        return ""


def last_trade_date():
    # 最近工作日（近似交易日；节假日 baostock 无当日数据 → 补数落空但无害）。
    d = datetime.date.today()
    while d.weekday() >= 5:
        d -= datetime.timedelta(days=1)
    return d.strftime("%Y%m%d")


def fetch_index(start_iso, end_iso):
    # 指数日线直补（pydata sidecar，CSV 协议与 Go BaostockClient 对齐）。
    import urllib.error
    total = 0
    for code, name in IDX:
        url = "%s/index_kline?code=%s&start=%s&end=%s&adjust=3" % (PYDATA, code, start_iso, end_iso)
        try:
            text = urllib.request.urlopen(url, timeout=60).read().decode("utf-8", "replace")
        except Exception as e:
            log("index %s fetch err %r" % (name, e)); continue
        if text.startswith("error:"):
            log("index %s srv err %s" % (name, text[:80])); continue
        c = sqlite3.connect(DB, timeout=60)
        for r in csv.DictReader(_io.StringIO(text)):
            td = str(r.get("date", "")).replace("-", "")
            if not td: continue
            def fv(k):
                try: return float(r.get(k) or 0)
                except Exception: return 0.0
            c.execute("INSERT OR REPLACE INTO index_daily (ts_code,trade_date,open,high,low,close,pre_close,change,pct_chg,vol,amount) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                      (name, td, fv("open"), fv("high"), fv("low"), fv("close"), fv("preclose"), fv("pctchg"), fv("pctchg"), fv("volume") / 100.0, fv("amount")))
            total += 1
        c.commit(); c.close()
    return total


def one_round(target):
    # 单轮补数：返回 True 表示目标表全部达到 target（yyyyMMdd）。
    # 三张判定表全新鲜时零外部调用直接返回（不触发任何拉取）。
    core = ("daily", "index_daily", "ths_limit_up_daily")
    if all(latest(t) >= target for t in core):
        return True
    ok = True
    d0 = datetime.datetime.strptime(target, "%Y%m%d").date() - datetime.timedelta(days=15)
    if latest("daily") < target:
        rc, msg = run([DATALOAD, "-db", DB, "-start", d0.strftime("%Y%m%d"),
                       "-end", time.strftime("%Y%m%d"), "daily"])
        log("daily rc=%d %s" % (rc, msg))
    if latest("daily") < target:
        ok = False
    start_iso, end_iso = d0.strftime("%Y-%m-%d"), datetime.date.today().strftime("%Y-%m-%d")
    if latest("index_daily") < target:
        n = fetch_index(start_iso, end_iso)
        log("index_daily wrote %d rows (max=%s)" % (n, latest("index_daily")))
        if latest("index_daily") < target:
            ok = False
    if latest("ths_limit_up_daily") < target:
        rc, msg = run([DATALOAD, "-db", DB, "hithink-sync", "--kind", "pools", "--date", target])
        log("pools %s rc=%d %s" % (target, rc, msg))
        if latest("ths_limit_up_daily") < target:
            ok = False
    # ths_daily（主源近10日 dump）：仅当自身落后才尝试，且不阻塞完成判定（dump 有时效）。
    if latest("ths_daily") < target:
        rc, msg = run([DATALOAD, "-db", DB, "hithink-sync", "--kind", "daily-k-10d"])
        log("ths daily-k-10d rc=%d %s" % (rc, msg))
    return ok


def main():
    log("keepalive run start pid=%d" % os.getpid())
    target = last_trade_date()
    done = False
    for rnd in range(8):
        if one_round(target):
            log("all fresh up to %s (round %d) — exit" % (target, rnd + 1))
            done = True
            break
        time.sleep(600)
    if not done:
        log("gave up for %s after 8 rounds; next daily run will retry" % target)
    return 0 if done else 1


if __name__ == "__main__":
    sys.exit(main())
