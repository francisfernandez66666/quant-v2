# -*- coding: utf-8 -*-
"""qmt_gateway 时间格式契约单测（§TZ 假 +08:00 根治 + §3.1-5 golden）：

覆盖三层锁：
  1. 产出锁——网关/桥全部落库/上报时间串（store._now_cn、handler on_stock_* 回报、
     gateway /dispatch/result 缺省 traded_at、桥 _now_cn_str）必须
     `%Y-%m-%dT%H:%M:%S%z` 可解析且偏移恒 +0800（假偏移直接红）。
  2. golden 锁——qmt_gateway/contract/time_formats.json 登记的 ISO 字段格式与实际
     产出一致；GET /settlement 的 date 入参只认 YYYY-MM-DD（§H1 事故锚）。
  3. 静态负向锁——qmt_gateway 生产 .py 与 tests/ 夹具不得再出现 `time.strftime(` 展开
     本地钟面贴 "+08:00" 的裸调用（收敛到显式北京时区函数）。
关键反例：把本机 TZ 伪装成 Asia/Seoul（首尔部署前必修项），落库串钟面必须仍为北京。
"""
import datetime as dt
import json
import os
import re
import sys
import tempfile
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from store import Store, _now_cn, CN_TZ  # noqa: E402
from handler import ReportHandler  # noqa: E402
from gateway import Gateway  # noqa: E402
import qmt_bridge  # noqa: E402
import qmt_bridge_strategy as qbs  # noqa: E402

_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
GOLDEN_PATH = os.path.join(_ROOT, "qmt_gateway", "contract", "time_formats.json")
ISO_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\+08:00$")


def _new_store():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return Store(path)


def _assert_beijing(ts, ctx=""):
    """任何落库/上报时间串：必须 %z 可解析、偏移恒 +0800、钟面与 UTC+8 现值同步。"""
    parsed = dt.datetime.strptime(ts, "%Y-%m-%dT%H:%M:%S%z")
    assert parsed.utcoffset() == dt.timedelta(hours=8), "假偏移: %s %s" % (ts, ctx)
    now_bj = dt.datetime.now(CN_TZ)
    drift = abs((now_bj - parsed).total_seconds())
    assert drift < 120, "钟面漂移 %ss（偏移标签造假?）: %s %s" % (drift, ts, ctx)
    assert ISO_RE.match(ts), "格式不合 golden ISO 契约: %s %s" % (ts, ctx)


class TestGoldenFile(unittest.TestCase):
    """§3.1-5 golden 本体：文件可解析、ISO 族格式登记自洽。"""

    def setUp(self):
        with open(GOLDEN_PATH, encoding="utf-8") as f:
            self.golden = json.load(f)

    def test_formats_registered_and_self_consistent(self):
        fmts = self.golden["formats"]
        iso = fmts["iso_beijing"]
        self.assertEqual(iso["format"], "YYYY-MM-DDTHH:MM:SS+08:00")
        self.assertEqual(iso["strptime"], "%Y-%m-%dT%H:%M:%S%z")
        self.assertEqual(iso["regex"], ISO_RE.pattern)
        self.assertIn(fmts["date_dash"]["format"], ("YYYY-MM-DD",))
        self.assertIn(fmts["date_compact"]["format"], ("YYYYMMDD",))
        # 每个登记字段都必须挂到已定义的三种格式族之一（ISO 族一律含 +08:00 显式偏移）
        families = set(fmts.keys())
        for ent in self.golden["fields"]:
            fam = [x for x in families if fmts[x]["format"] in ent["format"]]
            self.assertTrue(fam, "golden 字段 %s 格式未登记族: %s" % (ent["id"], ent["format"]))
            if "T" in ent["format"]:
                self.assertIn("+08:00", ent["format"],
                              "带时刻字段必须显式偏移（§TZ）: %s" % ent["id"])

    def test_settlement_and_report_fields_present(self):
        ids = {e["id"] for e in self.golden["fields"]}
        for must in ("settlement.query.date", "report.trade.traded_at", "report.order.at",
                     "report.positions.updated_at", "gateway.store.created_at",
                     "db.trade_date", "pydata.csv.date"):
            self.assertIn(must, ids)


class TestProducersEmitBeijingISO(unittest.TestCase):
    """产出锁：各生产者实际产出的时间串逐条 %z 解析 + 偏移 +0800。"""

    def test_store_now_cn(self):
        _assert_beijing(_now_cn(), "store._now_cn")

    def test_handler_stock_callbacks(self):
        class FakeTrade:
            order_id = "OID-1"
            trade_id = "TID-1"
            stock_code = "600519.SH"
            stock_name = "贵州茅台"
            traded_price = 10.0
            traded_volume = 100
            traded_amount = 1000.0
            order_type = 23
            strategy_name = "S-TZ"

        class FakeOrder(FakeTrade):
            order_status = 50
            order_volume = 100
            price = 10.0

        s = _new_store()
        h = ReportHandler(s, "", "")
        pushed = []
        h._push = pushed.append
        h.on_stock_order(FakeOrder())
        h.on_stock_trade(FakeTrade())
        _assert_beijing(pushed[0]["at"], "order.at")
        _assert_beijing(pushed[0]["created_at"], "order.created_at")
        _assert_beijing(pushed[1]["traded_at"], "trade.traded_at")
        # 落库侧同锁：fills.traded_at 入库串依旧显式 +0800
        row = s._conn.execute("SELECT traded_at FROM fills ORDER BY rowid DESC LIMIT 1").fetchone()
        _assert_beijing(str(row["traded_at"]), "fills.traded_at 落库值")

    def test_gateway_dispatch_trade_default_traded_at(self):
        """桥回报缺 traded_at → 网关补 _now_cn()：补出的值必须显式 +0800。"""
        cfg = {"db": None}
        fd, path = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        os.unlink(path)
        cfg["listen"] = "127.0.0.1:0"
        cfg["token"] = "t"
        cfg["broker"] = "mock"
        cfg["account"] = "MOCK"
        cfg["db"] = path
        cfg["report_url"] = ""
        cfg["report_token"] = ""
        cfg["user_id"] = ""
        gw = Gateway(cfg)
        code, resp = gw._do_dispatch_result({
            "type": "trade", "order_id": "BRIDGE-1", "code": "600519.SH",
            "side": "买入", "price": 10.0, "qty": 100, "amount": 1000.0,
            "signal_id": "S-BRIDGE",  # 不带 traded_at：走网关补时分支
        })
        self.assertEqual(code, 200, resp)
        row = gw.store._conn.execute(
            "SELECT traded_at FROM fills ORDER BY rowid DESC LIMIT 1").fetchone()
        _assert_beijing(str(row["traded_at"]), "/dispatch/result 缺省 traded_at 补值")
        # /settlement 装配日键：traded_at 前 10 位即 YYYY-MM-DD（golden date_dash）
        day = str(row["traded_at"])[:10]
        self.assertRegex(day, r"^\d{4}-\d{2}-\d{2}$")
        self.assertEqual(len(gw.store.settlement_trades(day)), 1)

    def test_bridge_helper_functions(self):
        _assert_beijing(qmt_bridge._now_cn_str(), "qmt_bridge._now_cn_str")
        _assert_beijing(qbs._now_cn_str(), "qmt_bridge_strategy._now_cn_str")
        # 短 fmt 形态（strategy 缺日期兜底分支用）：产出前缀必须是北京日期钟面
        got = qbs._now_cn_str("%Y-%m-%dT")
        self.assertRegex(got, r"^\d{4}-\d{2}-\d{2}T$")
        self.assertEqual(got, dt.datetime.now(CN_TZ).strftime("%Y-%m-%dT"))


class TestSeoulClockTamper(unittest.TestCase):
    """§TZ 反例锁（首尔部署前必修）：把本机 TZ 伪装为 Asia/Seoul（UTC+9），
    收敛后的时间函数必须仍产出北京钟面+偏移——旧裸 strftime 会产出
    「首尔钟面贴 +08:00」的造假串。"""

    def setUp(self):
        # 注意：os.tzset 在 Python 3.13 已移除，存活的是 time.tzset（POSIX 语义一致）
        if not hasattr(time, "tzset"):
            self.skipTest("time.tzset 不可用（Windows CI 跳过，macOS/Linux 必跑）")
        self._orig = os.environ.get("TZ")
        os.environ["TZ"] = "Asia/Seoul"
        time_vendor = sys.modules["time"]
        time_vendor.tzset()

    def tearDown(self):
        if self._orig is None:
            os.environ.pop("TZ", None)
        else:
            os.environ["TZ"] = self._orig
        sys.modules["time"].tzset()

    def test_now_cn_independent_of_local_tz(self):
        before_utc = dt.datetime.now(dt.timezone.utc)
        got = _now_cn()
        parsed = dt.datetime.strptime(got, "%Y-%m-%dT%H:%M:%S%z")
        # 同一瞬间：parsed（+08:00 显式偏移）与 UTC 现值之差应≈0。
        # 若实现回落到本地钟面贴 +08:00（首尔=UTC+9），字符串瞬间会凭空提前 1h → 差 3600s 红。
        self.assertLess(abs((parsed - before_utc).total_seconds()), 120, got)
        # 反证：本锁只在「本地钟面确实 ≠ 北京钟面」时才有意义（tzdata 缺失环境跳过）
        local_now = dt.datetime.now(dt.timezone.utc).astimezone()
        if local_now.utcoffset() == dt.timedelta(hours=8):
            self.skipTest("TZ 伪装未生效（缺 tzdata？）：本地钟面仍为 +08:00，本锁无法反证")
        self.assertNotEqual((parsed.hour, parsed.minute), (local_now.hour, local_now.minute),
                            "落库串钟面==本地钟面 → 假 +08:00 复发")
        # 同锁面覆盖桥端产出（沙箱进程独立，但口径必须同样是北京钟面）
        _assert_beijing(qmt_bridge._now_cn_str(), "TZ 伪装下 qmt_bridge._now_cn_str")
        _assert_beijing(qbs._now_cn_str(), "TZ 伪装下 qmt_bridge_strategy._now_cn_str")
        _assert_beijing(_now_cn(), "TZ 伪装下 store._now_cn（落库口径）")


class TestNoBareStrftimeStaticLock(unittest.TestCase):
    """静态负向锁：qmt_gateway 生产 .py 与 tests/ 夹具内禁止再出现把 "+08:00" 字面量
    喂给 time.strftime 的裸调用（该形态 = 本地钟面贴假偏移，§TZ 根治对象）。
    测试夹具同样受锁——假偏移夹具会在非北京时区机器自产漂移样本，掩盖真实缺陷。"""

    PATTERN = re.compile(r'time\.strftime\([^)\n]*\+08:00')

    def test_production_files_clean(self):
        gdir = os.path.join(_ROOT, "qmt_gateway")
        targets = [(name, os.path.join(gdir, name))
                   for name in sorted(os.listdir(gdir)) if name.endswith(".py")]
        tdir = os.path.join(gdir, "tests")
        targets += [(os.path.join("tests", name), os.path.join(tdir, name))
                    for name in sorted(os.listdir(tdir)) if name.endswith(".py")]
        offenders = []
        for name, path in targets:
            with open(path, encoding="utf-8") as f:
                for i, line in enumerate(f, 1):
                    if self.PATTERN.search(line):
                        offenders.append("%s:%d %s" % (name, i, line.strip()))
        self.assertEqual(offenders, [], "裸 strftime 假 +08:00 复发（应走 _now_cn/_now_cn_str）:\n"
                         + "\n".join(offenders))


class TestSettlementDateFormatContract(unittest.TestCase):
    """/settlement date 入参格式锁（golden settlement.query.date=YYYY-MM-DD，§H1）。"""

    def _gw(self):
        fd, path = tempfile.mkstemp(suffix=".db")
        os.close(fd)
        os.unlink(path)
        cfg = {"listen": "127.0.0.1:0", "token": "t", "broker": "mock", "account": "M",
               "db": path, "report_url": "", "report_token": "", "user_id": ""}
        return Gateway(cfg)

    def test_dash_accepted_compact_rejected(self):
        gw = self._gw()
        for day, want in (("2026-09-22", 200), ("20260922", 400), ("", 400), ("2026-9-2", 400)):
            code, resp = gw._do_settlement(type("R", (), {"path": "/settlement?date=" + day})())
            self.assertEqual(code, want, (day, resp))


if __name__ == "__main__":
    unittest.main()
