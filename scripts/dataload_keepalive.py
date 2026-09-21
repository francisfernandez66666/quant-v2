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
# §M6（2026-09-22 修复批）两处根治：
#   ① 指数日线表头键名：pydata sidecar 的 CSV 表头契约（cmd/pydata/server.py _INDEX_FIELDS）
#      为 "pctChg"（驼峰），旧实现读 r.get("pctchg") —— csv.DictReader 大小写敏感，恒取不到
#      → index_daily.change/pct_chg 静默写 0 且日志报成功。现改正键名，并在写 0 之前校验
#      源字段确实存在且可解析，否则显式弃权（整批不落库、计 rejected）→ 本轮判不成 → rc=1，
#      绝不静默写 0 报成功。表头字段集以 pytest 契约锁对齐 server.py 同源定义。
#      （English: the DictReader key was case-wrong ("pctchg" vs contract "pctChg"), so
#      change/pct_chg silently stored 0 while the log claimed success. Fixed the key and
#      added strict header/parse validation: any violation abstains the batch and fails rc.）
#   ② 最近交易日推导：旧实现按"最近工作日"近似，逢法定节假日必 rc=1。现优先查本库
#      trade_cal 表（is_open=1 的 ≤今日 最大开市日，Go dataload 装载同源），过期（>12 天
#      没有开市日，日历自身太旧不可信）或不可用时降级 pydata sidecar /trade_days 接口；
#      两者皆不可用才退回工作日近似并在日志留痕（回退路径显式标注，便于取证）。
#      （English: resolve the target trading day from the trade_cal table (fallback: the
#      pydata /trade_days sidecar), only approximating by weekday when no calendar is
#      available at all — holidays no longer force rc=1.）
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

# §M6 指数日线 CSV 表头契约（与 cmd/pydata/server.py _INDEX_FIELDS 同源，pytest 契约锁比对）：
# 大小写敏感——DictReader 按 server 下发的原始表头建键，"pctChg" 写成 "pctchg" 即恒取空。
INDEX_DATE_COL = "date"
INDEX_PCT_COL = "pctChg"  # §M6 修正：旧代码读 "pctchg"（不存在）→ change/pct_chg 恒 0
INDEX_REQUIRED_FIELDS = ("date", "open", "high", "low", "close", "preclose",
                         "pctChg", "volume", "amount")
# 交易日历距今日超过该天数仍无开市日 → 判定日历表本身过旧，不可再作 target 依据。
CALENDAR_STALE_DAYS = 12


def log(msg):
    # 2026-09-16：任务动作行 cmd /c python … >> 同名log 时，cmd 的独占追加句柄会让
    # open(LOG,"a") 报 Errno 13 把进程崩在第一行日志上（断供 5 日才排查到）。
    # 写日志失败不致命：stdout 兜底（重定向下经 cmd 继承句柄照样入档）。
    line = "%s %s" % (datetime.datetime.now().strftime("%Y-%m-%d %H:%M:%S"), msg)
    try:
        with io_open(LOG, "a") as f:
            f.write(line + "\n")
    except Exception:
        pass
    try:
        sys.stdout.write(line + "\n"); sys.stdout.flush()
    except Exception:
        pass


# 统一文本打开方式：UTF-8 + 错误替换，避免不同 locale 下解码崩溃。
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


# 查询指定表最新交易日（默认 trade_date 列），失败返回 (-1, 错误摘录)。
def latest(table, col="trade_date"):
    try:
        c = sqlite3.connect(DB, timeout=30)
        r = c.execute("SELECT MAX(%s) FROM %s" % (col, table)).fetchone()[0]
        c.close()
        return str(r or "")
    except Exception as e:
        log("query %s err: %r" % (table, e))
        return ""


def _valid_yyyymmdd(s):
    # 8 位数字且可解析为真实日期 → True（日历来源的数据必须过这一关才敢当 target）。
    s = str(s or "")
    if len(s) != 8 or not s.isdigit():
        return False
    try:
        datetime.datetime.strptime(s, "%Y%m%d")
        return True
    except ValueError:
        return False


# §M6-② target 交易日推导第一数据源：本库 trade_cal 表（Go dataload 装载，
# cal_date=YYYYMMDD、is_open=1 为交易日；internal/store data_freshness 同口径）。
def _calendar_latest_open(today):
    try:
        c = sqlite3.connect(DB, timeout=30)
        r = c.execute("SELECT MAX(cal_date) FROM trade_cal WHERE is_open=1 AND cal_date<=?",
                      (today.strftime("%Y%m%d"),)).fetchone()[0]
        c.close()
    except Exception as e:
        log("trade_cal query err (fallback to pydata): %r" % e)
        return ""
    return str(r or "")


# §M6-② 第二数据源：pydata sidecar /trade_days（baostock 交易日历，CSV calendar_date,is_open；
# 日期可能带杠也可能不带，统一归一 YYYYMMDD；is_open 非 "1" 视为休市）。
def _pydata_latest_open(today):
    start = (today - datetime.timedelta(days=45)).strftime("%Y-%m-%d")
    end = today.strftime("%Y-%m-%d")
    best = ""
    try:
        url = "%s/trade_days?start=%s&end=%s" % (PYDATA, start, end)
        text = urllib.request.urlopen(url, timeout=10).read().decode("utf-8", "replace")
        if text.startswith("error:"):
            return ""
        for r in csv.DictReader(_io.StringIO(text)):
            d = str(r.get("calendar_date", "")).replace("-", "")
            if _valid_yyyymmdd(d) and str(r.get("is_open", "")).strip() in ("1", "1.0") and d > best:
                best = d
    except Exception as e:
        log("pydata trade_days err (fallback to weekday approx): %r" % e)
        return ""
    return best


def last_trade_date(today=None):
    # §M6-② 最近交易日：交易日历（库内 trade_cal → sidecar /trade_days）优先，
    # 逢节假日 target 正确回退到节前交易日，不再"工作日近似必踩节假日"。
    # 两源皆不可用（冷启动库无 trade_cal 且 sidecar 不在线）才退回工作日近似，
    # 并在日志显式留痕——该回退逢节假日仍会误判，属最后防线的已知残余（报告说明）。
    d = today or datetime.date.today()
    cal = _calendar_latest_open(d)
    if _valid_yyyymmdd(cal):
        floor = (d - datetime.timedelta(days=CALENDAR_STALE_DAYS)).strftime("%Y%m%d")
        if cal >= floor:
            return cal
        log("trade_cal 过旧（max=%s < %s）：改用 pydata 交易日历" % (cal, floor))
    py = _pydata_latest_open(d)
    if _valid_yyyymmdd(py):
        return py
    log("交易日历两源均不可用，target 回退工作日近似（节假日可能误判，§M6 已知残余）")
    while d.weekday() >= 5:
        d -= datetime.timedelta(days=1)
    return d.strftime("%Y%m%d")


def fetch_index(start_iso, end_iso):
    # 指数日线直补（pydata sidecar，CSV 协议与 Go BaostockClient 对齐）。
    # §M6-① 返回 (written, rejected)：rejected>0 表示该批源字段缺失/不可解析，
    # 已显式弃权（一行都不写，绝不静默写 0）；由 one_round 判成本轮未完成 → 最终 rc=1。
    import urllib.error
    total = 0
    rejected = 0
    for code, name in IDX:
        url = "%s/index_kline?code=%s&start=%s&end=%s&adjust=3" % (PYDATA, code, start_iso, end_iso)
        try:
            text = urllib.request.urlopen(url, timeout=60).read().decode("utf-8", "replace")
        except Exception as e:
            log("index %s fetch err %r" % (name, e)); rejected += 1; continue
        if text.startswith("error:"):
            log("index %s srv err %s" % (name, text[:80])); rejected += 1; continue
        rdr = csv.DictReader(_io.StringIO(text))
        header = rdr.fieldnames or []
        missing = [f for f in INDEX_REQUIRED_FIELDS if f not in header]
        if missing:
            # 表头契约破坏（如大小写漂移）：整源弃权，不落任何行。
            log("index %s ABSTAINED: header missing %s (contract %s)"
                % (name, ",".join(missing), ",".join(INDEX_REQUIRED_FIELDS)))
            rejected += 1
            continue
        rows = []
        bad = ""
        for r in rdr:
            td = str(r.get(INDEX_DATE_COL, "")).replace("-", "")
            if not _valid_yyyymmdd(td):
                bad = "bad date %r" % td
                break
            def fp(key):
                # §M6 写 0 前置校验：源字段必须存在且可解析（空串/None/非数一律拒绝）。
                v = r.get(key)
                if v is None or str(v).strip() == "":
                    raise ValueError("empty %s" % key)
                return float(str(v).strip())
            try:
                vals = {k: fp(k) for k in INDEX_REQUIRED_FIELDS if k != INDEX_DATE_COL}
            except (TypeError, ValueError) as e:
                bad = "unparsable field at %s: %r" % (td, e)
                break
            rows.append((name, td, vals))
        if bad:
            log("index %s ABSTAINED: %s" % (name, bad))
            rejected += 1
            continue
        c = sqlite3.connect(DB, timeout=60)
        for name_, td, v in rows:
            # §M6 口径与 Go bsLoadIndex 对齐：change/pct_chg 都取 pctChg（Go 侧
            # internal/data/baostock 表头小写映射后同为该值），vol=股/100（手）。
            c.execute("INSERT OR REPLACE INTO index_daily (ts_code,trade_date,open,high,low,close,pre_close,change,pct_chg,vol,amount) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                      (name_, td, v["open"], v["high"], v["low"], v["close"], v["preclose"],
                       v[INDEX_PCT_COL], v[INDEX_PCT_COL], v["volume"] / 100.0, v["amount"]))
            total += 1
        c.commit(); c.close()
    return total, rejected


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
        n, bad = fetch_index(start_iso, end_iso)
        # §M6-① rejected>0 = 源字段缺失/不可解析已显式弃权：即使表里"看起来够新"也判未完成，
        # 本轮回不上就 8 轮耗尽 → rc=1（绝不静默写 0 报成功）。
        log("index_daily wrote %d rows (abstained=%d) (max=%s)" % (n, bad, latest("index_daily")))
        if bad:
            ok = False
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


# 保活主流程：取最新交易日并对数据加载链路做一轮心跳检查/补数。
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
