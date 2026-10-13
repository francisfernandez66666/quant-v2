# -*- coding: utf-8 -*-
# dataload_keepalive.py §dataload 盘后保活（每日计划任务一锤子：拉齐即退出，不常驻）。
#
# 职责（计划任务每天 17:10 触发一次，交易时段绝不自跑）：
#   1. 检测 daily / index_daily / ths_limit_up_daily 是否已到最近交易日；
#      已齐 → 立即退出（幂等，当日重复触发零成本）；
#   2. 缺则补：baostock 日线增量 → 指数日线 → hithink 涨停池（当日）
#      → hithink daily-k-10d（ths_daily 主源增量）；
#   3. 幂等（全部 INSERT OR REPLACE / upsert），未拉齐则 10 分钟一轮至多 8 轮后退出，
#      次日计划任务兜底；§0927KA 加无进展早停——连续两轮三表读数零变化即收手
#      （09-26 实录：target 幻觉成休市日时 8 轮＝12h 空烧配额并锁死 dataload.exe）。
#
# §POOL-STAMP／§TARGET-NOT-CLOSED（2026-10-12）盘口链两条新增（都在 main 进场处）：
#   ④ 漏更日逐日回补：新鲜判定用的是 MAX(trade_date)>=target，中间漏掉的那一天既看不见
#      也不会再被请求。涨停池改为整日快照落库后，本函数每轮把 15 日窗内「日历开市但当日
#      0 行」的日子点名补一次（至多 5 天/轮），并把 found/fixed/left 打成一行读数。
#   ⑤ 收盘前归位：target 就是今天而此刻尚未过收盘缓冲（15:05）时，把目标改成上一开市日——
#      旧行为在收盘前手工拨一次必追不平，8 轮后 rc=1，把"早拨了一次"报成"补数失败"。
#   ⑥ §BOARD-COVER 按板块覆盖：全库 MAX(trade_date) 是 .SH/.SZ 的最新，整块板块停更
#      （`.BJ` 断更六周实录）在全库口径下永久隐形；每轮结束打一行三板块
#      （板块最新日＋全库最新日行数）读数，缺板当天可见（库读不到→unknown，绝不冒充 0）。
#      同形态第二犯（2026-10-12 当日实录）：one_round 的新鲜判定也用整库 MAX——.BJ 当天
#      补进的 311 行把全库 MAX 垫到 target，.SH/.SZ 还停在上一交易日也宣告「all fresh」。
#      判定腿同修：daily 新鲜改按三板块各自 MAX 全达标（latest_board/daily_boards_fresh），
#      判不成时把各板块最新日打成归因读数。
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
# §CAL-COVER（2026-10-11 现网实录）：日历自愈回写窗口向未来延伸的天数。
# 事故形态：0927KA 自愈的窗口 end=today，自愈次日 trade_cal 末行就落在昨天，
# 而开市日龄（CALENDAR_STALE_DAYS）要再等最多 12 天才会判过旧——中间每个真实
# 交易日都被 target=旧开市日 洗成「全部到齐」直接退出（09-28~30 与 10-08/09
# 两段漏更即此形态，引擎侧行情新鲜度自检共用同一张日历同样失明）。
# 治本＝回写时把未来开市日一并入库（baostock 日历含全年未来日期，已对现网
# sidecar 实测），配合 last_trade_date 的「末行不覆盖今日即过旧」检查，
# 日历从此永远覆盖今天，盲窗在结构上消失。
CALENDAR_LOOKAHEAD_DAYS = 45


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
        # §CAL-COVER 覆盖检查：除开市日龄外，还要求日历末行覆盖到今天。
        # 否则"日历行只写到过去某天"的库（自愈窗口 end=today 的历史形态）里，
        # 最近开市日永远"新鲜"，今天及之后的真实交易日被整段跳过。
        cov = c.execute("SELECT MAX(cal_date) FROM trade_cal").fetchone()[0]
        c.close()
    except Exception as e:
        log("trade_cal query err (fallback to pydata): %r" % e)
        return ""
    cal = str(r or "")
    if cal and cov and str(cov) < today.strftime("%Y%m%d"):
        log("trade_cal 不覆盖今日（末行=%s）：改用 pydata 交易日历" % cov)
        return ""
    return cal


# §M6-② 第二数据源：pydata sidecar /trade_days（baostock 交易日历，CSV calendar_date,is_open；
# 日期可能带杠也可能不带，统一归一 YYYYMMDD；is_open 非 "1" 视为休市）。
# §0927KA（2026-09-27 部署实录加固）：本函数顺手把拉到的日历行 INSERT OR REPLACE 回写
# trade_cal（自愈）——现网实证该表停在 20260818 再无人刷新，第一数据源长期过旧全靠降级；
# 回写后 trade_cal 每晚随保活自动维持近 45 天窗，降级链退化为真兜底。回写失败只留痕，
# 绝不影响 target 推导本身（写库竞态/表锁死不得把保活打挂）。
# §CAL-COVER（2026-10-11）：窗口从 end=today 改为 end=today+CALENDAR_LOOKAHEAD_DAYS。
# 旧窗口的自愈写入永远"写到今天为止"，自愈次日日历就不再覆盖今天，而开市日龄判过旧
# 还要等最多 12 天 ⇒ 盲窗内每个交易日被 target=旧开市日 洗成"全部到齐"整段漏更
# （09-28~30、10-08/09 两段实录）。向前延伸后日历常备未来开市日，target 恒正确。
# （English: this fallback also self-heals trade_cal with the fetched window, so the
# first calendar source no longer rots; persistence errors never affect target resolution.
# Since §CAL-COVER the window extends CALENDAR_LOOKAHEAD_DAYS past today, so the stored
# calendar always covers today — the stale-but-blind window that silently skipped trading
# days is structurally gone.）
def _pydata_latest_open(today):
    start = (today - datetime.timedelta(days=45)).strftime("%Y-%m-%d")
    end = (today + datetime.timedelta(days=CALENDAR_LOOKAHEAD_DAYS)).strftime("%Y-%m-%d")
    best = ""
    today_str = today.strftime("%Y%m%d")  # §CAL-COVER：best 只许取 ≤今日 的开市日
    rows = []  # §0927KA 自愈素材：(归一日期, is_open int)
    try:
        url = "%s/trade_days?start=%s&end=%s" % (PYDATA, start, end)
        text = urllib.request.urlopen(url, timeout=10).read().decode("utf-8", "replace")
        if text.startswith("error:"):
            return ""
        for r in csv.DictReader(_io.StringIO(text)):
            d = str(r.get("calendar_date", "")).replace("-", "")
            if not _valid_yyyymmdd(d):
                continue
            op = str(r.get("is_open", "")).strip() in ("1", "1.0")
            rows.append((d, 1 if op else 0))
            # §CAL-COVER：窗口前移后未来开市日也进 rows（回写素材），
            # 但 target（best）必须夹在 ≤今日——否则 target 落在未来，
            # 三张判定表永远追不平，8 轮空烧后 rc=1（0927KA 事故同形）。
            if op and d <= today_str and d > best:
                best = d
    except Exception as e:
        log("pydata trade_days err (fallback to weekday approx): %r" % e)
        return ""
    # §0927KA 回写 trade_cal（骨架与 internal/store/store.go 建表语句逐字同形）。
    try:
        if rows:
            c = sqlite3.connect(DB, timeout=30)
            c.execute("CREATE TABLE IF NOT EXISTS trade_cal ("
                      "cal_date TEXT PRIMARY KEY, is_open INTEGER)")
            c.executemany("INSERT OR REPLACE INTO trade_cal (cal_date, is_open) VALUES (?,?)", rows)
            c.commit()
            c.close()
            log("§0927KA trade_cal self-heal: upserted %d calendar rows" % len(rows))
    except Exception as e:
        log("§0927KA trade_cal self-heal err (target derivation unaffected): %r" % e)
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


# ── §POOL-STAMP（2026-10-12）盘口三池漏更日逐日回补 + 收盘前"目标日未到"归位 ──

# 逐日回补的扫描窗口与单轮上限。两个数都是**约束**而不是偏好：
#   LOOKBACK_DAYS=15 与 one_round 里日线补数窗口同尺（d0 = target-15d），两套"最近多少天"
#     用同一把尺，才不会出现日线补到 15 天、池子只补 5 天的口径分叉；
#   MAX_DAYS_PER_RUN=5 因为每个漏更日是一次独立的 hithink 上游调用（三池各一次），
#     一轮扫完整个窗口会顶到配额并把保活拖过部署停机窗口——剩下的次日继续，读数点名。
POOL_GAP_TABLE = "ths_limit_up_daily"
POOL_GAP_LOOKBACK_DAYS = 15
POOL_GAP_MAX_DAYS_PER_RUN = 5
# 收盘后当日行情可取的缓冲时刻（15:05）。早于此刻且 target 就是今天 ⇒ 那一天"还没收盘"，
# 不是"没备份"。旧形态在这一格直接 8 轮×10 分钟空烧后 rc=1，把"早拨了一次"报成补数失败。
POST_CLOSE_READY_MINUTES = 15 * 60 + 5


def row_count_on(table, date):
    """某表某交易日的行数；读不到（缺表/锁死/异常）返回 -1＝**无从判定**，绝不返回 0。
    0 与"读失败"在缺数判据里含义相反：前者是"确凿没数据、该补"，后者是"现在还不知道"。"""
    try:
        c = sqlite3.connect(DB, timeout=30)
        n = c.execute("SELECT COUNT(*) FROM %s WHERE trade_date=?" % table, (date,)).fetchone()[0]
        c.close()
        return int(n or 0)
    except Exception as e:
        log("row_count_on %s %s err: %r" % (table, date, e))
        return -1


def _calendar_open_days(start, end):
    """trade_cal 里 [start,end] 的开市日（升序 yyyyMMdd）。表缺失/异常 → []（缺数不猜、不工作日近似）。
    逐日回补只用这一条日历源：日历不可用时宁可不补，也不能把休市日当成漏更日去拨上游。"""
    try:
        c = sqlite3.connect(DB, timeout=30)
        rows = c.execute("SELECT cal_date FROM trade_cal WHERE is_open=1 AND cal_date>=? AND cal_date<=? "
                         "ORDER BY cal_date", (start, end)).fetchall()
        c.close()
    except Exception as e:
        log("trade_cal open days err: %r" % e)
        return []
    return [str(r[0]) for r in rows if _valid_yyyymmdd(str(r[0]))]


def pool_gap_days(target):
    """回看窗口内「日历开市、涨停池当日 0 行」的日子（升序，截到单轮上限，不含 target 本身）。

    为什么必须有这条腿：one_round 的新新鲜判定是 `latest(table) >= target`，即 MAX(trade_date) 对比。
    整日快照替换落地后，"最后一天到了"与"每一天都在"是两件事——中间漏掉的那天既不会被任何
    现有判据看见，也永远不会再被请求（MAX 已达标 ⇒ 池子一步都不拨）。这就是"漏更日永不自动回补"。
    """
    d = datetime.datetime.strptime(target, "%Y%m%d").date()
    start = (d - datetime.timedelta(days=POOL_GAP_LOOKBACK_DAYS)).strftime("%Y%m%d")
    out = []
    for day in _calendar_open_days(start, target):
        if day >= target:
            continue  # target 由 one_round 自己负责，两腿不并发拨同一天
        if row_count_on(POOL_GAP_TABLE, day) == 0:
            out.append(day)
    return out[:POOL_GAP_MAX_DAYS_PER_RUN]


def fill_pool_gaps(target):
    """逐日回补漏更日，返回 (发现数, 补成数, 仍缺数)。

    仍缺数只可能来自两种成因：上游确实给不出那一天（含同步链的幻影日拦停），或调用失败。
    两种都不许用"补上了别的地方的数据"来消掉——宁可留一个点名到日的缺数读数。
    """
    gaps = pool_gap_days(target)
    if not gaps:
        return 0, 0, 0
    fixed = 0
    for day in gaps:
        rc, msg = run([DATALOAD, "-db", DB, "hithink-sync", "--kind", "pools", "--date", day])
        after = row_count_on(POOL_GAP_TABLE, day)
        log("pools_gap day=%s rc=%d rows_after=%s msg=%s" % (day, rc, after, msg))
        if after > 0:
            fixed += 1
    left = len(gaps) - fixed
    log("pools_gap scanned=%d refilled=%d still_missing=%d days=%s" % (len(gaps), fixed, left, ",".join(gaps)))
    return len(gaps), fixed, left


def target_day_reachable(target, now=None):
    """target 那天现在拿得到数据吗：早于今日一律 True；就是今天则须已过收盘缓冲时刻。
    返回 (可取的 target, 是否发生归位, 归位说明)。"""
    now = now or datetime.datetime.now()
    if not isinstance(now, datetime.datetime):
        # 只给日期＝无法判"是否已过收盘时刻"，按收盘后处理（归位这条腿宁可不触发，
        # 也不要把一个没钟点的输入猜成"还没收盘"从而改走上一交易日）。
        now = datetime.datetime.combine(now, datetime.time(POST_CLOSE_READY_MINUTES // 60,
                                                           POST_CLOSE_READY_MINUTES % 60))
    today_obj = now.date() if hasattr(now, "date") else now  # 允许传 date（测试与人工拨测同形）
    today = today_obj.strftime("%Y%m%d")
    if target != today:
        return target, False, ""
    if now.hour * 60 + now.minute >= POST_CLOSE_READY_MINUTES:
        return target, False, ""
    # 归位到"上一个开市日"：收盘前的当日数据本来就不存在，把今天当目标是恒追不平的；
    # 但今天之前那一天可能真有缺（例如昨夜补数没跑完），所以是换目标继续补，而不是直接退出。
    prev = last_trade_date(today_obj - datetime.timedelta(days=1))
    if not _valid_yyyymmdd(prev) or prev >= target:
        # 拿不到上一开市日（日历两源皆不可用）时**不改 target**，退回旧行为并留痕——
        # 把 target 猜成某天，比追不平退出更糟。
        log("target-not-closed day=%s now=%s retreat=none（上一开市日不可得，保持原 target）" % (target, now.strftime("%H:%M")))
        return target, False, ""
    log("target-not-closed day=%s now=%s retreat=%s（收盘前当日数据未到，改按上一开市日补）"
        % (target, now.strftime("%H:%M"), prev))
    return prev, True, prev


# ── §BOARD-COVER（2026-10-12 波 6）按板块覆盖读数 ──
# 背景：`.BJ` 整板日线断更六周而全库口径看不见（RUNBOOK §2.1 ③）——所有"最新"判据读的
# 都是 MAX(trade_date)，那是 .SH/.SZ 的最新；任何一整块板块停下都只是在总数里静默隐形，
# 每票三次重试还把整窗耗时往上抬。这条读数把每个板块的（板块最新交易日、全库最新日
# 当日行数）逐个点名，与全库同日基线直接对照：缺板当天可见。
# 语义：库读不到 → unknown 态；daily 全表无行 → empty 态（未启动采集的合法态）；
# 单板块查询失败 → 该板块记 -1。"无从判定"绝不冒充 0 行——0 是"确凿没数据、该补"，
# -1 是"现在还不知道"，两者在缺数判据里含义相反。
BOARD_SUFFIXES = (".SH", ".SZ", ".BJ")


def board_coverage_line():
    """逐板块覆盖读数一行文本（进保活日志，夜间验收可 grep）。"""
    try:
        c = sqlite3.connect(DB, timeout=30)
        overall = str(c.execute("SELECT COALESCE(MAX(trade_date),'') FROM daily").fetchone()[0] or "")
        c.close()
    except Exception as e:
        log("board_coverage daily err: %r" % (e,))
        return "board_coverage state=unknown（daily 读不到，无从判定）"
    if not overall:
        return "board_coverage state=empty（daily 全表无行，未启动采集）"
    parts = ["board_coverage latest=%s" % overall]
    for suf in BOARD_SUFFIXES:
        try:
            c = sqlite3.connect(DB, timeout=30)
            mx = str(c.execute("SELECT COALESCE(MAX(trade_date),'') FROM daily WHERE ts_code LIKE ?",
                               ("%" + suf,)).fetchone()[0] or "")
            n = int(c.execute("SELECT COUNT(*) FROM daily WHERE trade_date=? AND ts_code LIKE ?",
                              (overall, "%" + suf)).fetchone()[0])
            c.close()
            parts.append("%s[max=%s rows=%d]" % (suf.strip("."), mx or "none", n))
        except Exception as e:
            log("board_coverage %s rows=-1 err: %r" % (suf, e))
            parts.append("%s[max=unknown rows=-1]" % suf.strip("."))
    return " ".join(parts)


# §BOARD-COVER 判定腿（2026-10-12 当日实录）：one_round 的新鲜判定原本也读整库 MAX——
# .BJ 当天补进的 311 行把全库 MAX 垫到 target，.SH/.SZ 还停在上一交易日也宣告
# 「all fresh」零调用退出。daily 的新鲜改按三板块各自 MAX 全部 >= target 才算齐；
# 读不到/空板块一律算不新鲜（unknown 不冒充达标——宁可多拨一轮，不可谎报齐了）。
def latest_board(suffix, col="trade_date"):
    """单板块 daily 最新交易日（整库 latest 的板块级版本）；查询失败返回空串。"""
    try:
        c = sqlite3.connect(DB, timeout=30)
        r = c.execute("SELECT COALESCE(MAX(%s),'') FROM daily WHERE ts_code LIKE ?" % col,
                      ("%" + suffix,)).fetchone()[0]
        c.close()
        return str(r or "")
    except Exception as e:
        log("query daily[%s] err: %r" % (suffix, e))
        return ""


def daily_boards_fresh(target):
    """daily 全板块新鲜判定：三板块各自 MAX 全部 >= target 才算齐（整库 MAX 会
    被任何一块垫高——§BOARD-COVER 判定腿修的就是这个谎报形态）。"""
    for suf in BOARD_SUFFIXES:
        mx = latest_board(suf)  # 空串（读不到/空板块）一律不新鲜：unknown 不冒充达标
        if not mx or mx < target:
            return False
    return True


def daily_boards_line(target):
    """判不成时的板块级归因读数（哪个板块停在哪个交易日，一行点名）。"""
    return " ".join("%s[max=%s]" % (suf.strip("."), latest_board(suf) or "unknown")
                    for suf in BOARD_SUFFIXES)


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
    # daily 判据走板块级（§BOARD-COVER 判定腿）：整库 MAX 会被任何一块板块垫高
    # （2026-10-12 实录：.BJ 补到 target、.SH/.SZ 停在上一交易日也宣告全新鲜）。
    # 其余两表全新鲜时零外部调用直接返回（不触发任何拉取）。
    if daily_boards_fresh(target) and all(latest(t) >= target for t in ("index_daily", "ths_limit_up_daily")):
        return True
    ok = True
    d0 = datetime.datetime.strptime(target, "%Y%m%d").date() - datetime.timedelta(days=15)
    if not daily_boards_fresh(target):
        rc, msg = run([DATALOAD, "-db", DB, "-start", d0.strftime("%Y%m%d"),
                       "-end", time.strftime("%Y%m%d"), "daily"])
        log("daily rc=%d %s" % (rc, msg))
        if not daily_boards_fresh(target):
            # 板块级落后不冒充完成：点名哪个板块停在哪个交易日（归因读数进保活日志）。
            log("daily 板块覆盖仍缺 %s（target=%s）" % (daily_boards_line(target), target))
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


# §0927KA 无进展早停快照：三张核心表的最新交易日元组（与 one_round 判定表同集合）。
# 09-26 实录：target 被误推导成休市日（幻觉目标）时，每轮 1.5h 的日线补数永远追不平，
# 8 轮＝约 12h 空烧 baostock 配额并锁死 dataload.exe（挡掉一次生产部署）。幻觉来源修不尽，
# 用「连续两轮三表读数零变化」这一与目标无关的信号兜住失控放大面：真缺数据必然伴随读数
# 变化或新行写入，零进展连踩两轮即收手，留 rc=1 与日志明细供次日人工/自动复核。
def _core_snap():
    return tuple(latest(t) for t in ("daily", "index_daily", "ths_limit_up_daily"))


# 保活主流程：取最新交易日并对数据加载链路做一轮心跳检查/补数。
def main(now=None):
    # §TARGET-NOT-CLOSED（2026-10-12）：now 可注入（测试与人工收盘前拨测同形），缺省=当下。
    log("keepalive run start pid=%d" % os.getpid())
    # §TARGET-NOT-CLOSED 的注入时钟必须喂满两处：target 的推导（last_trade_date）和
    # 归位判定（target_day_reachable）。只喂归位那一处时，"今天"仍按本机墙上钟算——
    # 2026-10-13 00:10 复跑本批用例时，注入的是 10-12 10:44（收盘前、该归位到 20261009），
    # 而 last_trade_date() 按墙上钟取出 §CAL-COVER 向未来延伸后的日历开市日 20261013，
    # 于是 target(20261013) != today(20261012) 让归位腿直接判定"不是今天、无需归位"，
    # 归位永远不触发、追不平 rc=1（夜里跨过零点才现形＝白天同一条用例是绿的）。
    today_injected = None
    if now is not None:
        today_injected = now.date() if isinstance(now, datetime.datetime) else now
    target = last_trade_date(today_injected)
    # 收盘前手工拨：当日数据本来就没出，旧形态把"今天"当目标 ⇒ 8 轮追不上 ⇒ rc=1 报"补数失败"，
    # 现按上一开市日归位（那一天若真缺照样补），归位过程本身留下读数。
    target, retreated, retreat_note = target_day_reachable(target, now)
    done = False
    # §POOL-GAP：MAX(trade_date) 达标≠每一天都在——把 15 日窗里的漏更日逐日点名回补，
    # 一轮至多 5 天（每池每日一拨，扫完整窗会顶配额并拖过停机窗口）。整日替换落库后
    # 这是唯一会主动补漏更日的通道，也是唯一把漏更日打成读数的地方。
    pool_gap_total, pool_gap_fixed, pool_gap_days_left = fill_pool_gaps(target)
    last_snap = _core_snap()   # §0927KA 首轮进场前的基线快照
    no_progress = 0             # §0927KA 连续零进展轮数（见 _core_snap 注释）
    for rnd in range(8):
        if one_round(target):
            log("all fresh up to %s (round %d) — exit%s" % (target, rnd + 1,
                (" [retreated=%s]" % retreat_note) if retreated else ""))
            done = True
            break
        snap = _core_snap()
        if snap == last_snap:
            no_progress += 1
            log("no progress (snap unchanged %s) round %d" % ("/".join(snap or ()), rnd + 1))
            if no_progress >= 2:
                # §0927KA 早停：target 很可能是幻觉（休市日误判/日历源同时失真），
                # 继续跑只会空烧配额并锁 exe——明日的保活轮次与人工处置是正解。
                log("no progress 2 rounds — abort early (target %s suspect, calendar sources stale?)" % target)
                break
        else:
            no_progress = 0
        last_snap = snap
        time.sleep(600)
    if not done:
        log("gave up for %s after %d rounds; next daily run will retry" % (target, rnd + 1))
    if pool_gap_total:
        log("pool gap: found=%d fixed=%d left=%d" % (pool_gap_total, pool_gap_fixed, pool_gap_days_left))
    # §BOARD-COVER：收尾逐板块点名一次（断板在全库 MAX 口径下永久隐形——.BJ 六周断更的教训；
    # 放在补数轮之后＝这行读数就是当天补完后的真实覆盖面）。
    log(board_coverage_line())
    return 0 if done else 1


if __name__ == "__main__":
    sys.exit(main())
