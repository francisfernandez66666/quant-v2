# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_report_contract.py — §M4（2026-09-22 PM 批）回报契约双向锁（网关侧）。

背景：report_fields.json 此前只有 Go 一侧的反射面（qmtReportEvent 的 JSON tag），
于是"网关发了、Go 没接"这类**丢腿**在两侧测试里都是绿的——实录：
trade 回报常年携带 `trade_id`（券商唯一成交编号）与 `name`（证券名称），
Go 信封没有这两个 tag → encoding/json 静默丢弃，成交判重只剩
(order_id,traded_at,price,qty) 复合键（同委托同秒同价同量的第二笔真实部成会被误判为重放丢单），
持仓名称建仓回填恒为空；order 回报的 `created_at` 同样被丢，委托行 created_at 记成了回报时刻。

本文件把 golden 的 `gateway_emitted_fields` 钉成**发出侧**的 golden：
  ① 每条回报实际入 outbox 的载荷键集 == golden 声明（网关加字段不登记 golden 即红）；
  ② 发出键集必须全部落在 golden 的 `report_event_fields`（Go 信封反射集）内
     ——Go 不接的字段禁止存在（要么接、要么显式删掉发送），杜绝再次出现静默丢腿。
English: emit-side golden — every key the gateway puts on the wire must be declared in the
Go envelope's reflected field set, so an untagged (silently dropped) leg can never come back.
"""
import json
import os
import sys
import tempfile
import types
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from store import Store  # noqa: E402
import handler as handler_mod  # noqa: E402
from handler import ReportHandler  # noqa: E402
from gateway import Gateway  # noqa: E402
from broker import XtBroker  # noqa: E402

_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
GOLDEN_PATH = os.path.join(_ROOT, "qmt_gateway", "contract", "report_fields.json")
# §0925EVE-W2-A1（2026-09-26 批）持仓对账「真行采样」文件——本契约锁的输入源不再是手喂合成行。
SAMPLE_PATH = os.path.join(_ROOT, "qmt_gateway", "contract", "positions_sample.json")


def _load_positions_sample():
    """读取真行采样（rows 至少一行）。样例形态=broker.py 真实 emit 键集，见文件 _meta 诚实口径。"""
    with open(SAMPLE_PATH, encoding="utf-8") as f:
        doc = json.load(f)
    rows = doc.get("rows") or []
    assert rows, "positions_sample.json 无样例行——契约真行采样锁禁止空输入"
    return rows


def _xt_broker_emit_keys():
    """跑 XtBroker.query_positions 真实映射代码路径（假 trader 注入假持仓对象），
    返回一行的真实 emit 键集。用于钉『样例键集 == 网关真实产出键集』——
    旧契约测试的失守形态正是合成行与 emit 路径无关，加腿（can_use_qty）两侧都是绿。
    """
    b = XtBroker.__new__(XtBroker)  # 绕开真实 connect，仅走映射段（同 test_channel_position_fields 先例）
    b._connected = True
    b._acc = "A1"
    pos = types.SimpleNamespace(stock_code="600000.SH", stock_name="浦发银行", volume=500,
                                can_use_volume=300, open_price=10.6, market_value=5300.0)
    b._trader = types.SimpleNamespace(query_stock_positions=lambda acc: [pos])
    return set(b.query_positions()[0].keys())


def _new_store():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return Store(path)


def _capturing_handler(user_id="u1"):
    """回报处理器 + 载荷收集器：_push 走真实代码路径（含 user_id 注入），
    只把 outbox 落库换成内存收集，避免依赖发送线程。"""
    s = _new_store()
    h = ReportHandler(s, "http://seoul.invalid/api/qmt/report", "tok", user_id=user_id)
    pushed = []
    s.outbox_enqueue = lambda payload: (pushed.append(payload), 1)[1]
    s.outbox_trim = lambda n: 0
    s.outbox_count = lambda: 0
    return h, pushed


class _FakeTrade:
    order_id = "GW-1"
    trade_id = "T-9001"
    stock_code = "600519.SH"
    stock_name = "贵州茅台"
    traded_price = 10.0
    traded_volume = 100
    traded_amount = 1000.0
    order_type = 23
    strategy_name = "S-M4"


class _FakeOrder(_FakeTrade):
    order_status = 50
    order_volume = 100
    price = 10.0


def _bridge_trade_payload():
    """queued 通道（文件/HTTP 桥回报）经 gateway._do_dispatch_result 转发到上报队列的载荷。

    xt 直连通道的 trade 回报不带 stamp_tax（xtquant 成交对象无该字段可取），
    桥的 DEAL 行尽力带——所以 golden 的 trade 发出集是**两条通道的并集**。
    English: the golden trade set is the union of both channels; the xt path carries no
    stamp_tax while the bridge DEAL row does.
    """
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    cfg = {"listen": "127.0.0.1:0", "token": "t", "broker": "mock", "account": "M",
           "db": path, "report_url": "", "report_token": "", "user_id": "u1"}
    gw = Gateway(cfg)
    gw.handler.report_url = "http://seoul.invalid/api/qmt/report"
    pushed = []
    gw.handler.store.outbox_enqueue = lambda p: (pushed.append(p), 1)[1]
    gw.handler.store.outbox_trim = lambda n: 0
    gw.handler.store.outbox_count = lambda: 0
    code, _resp = gw._do_dispatch_result({
        "type": "trade", "seq": "S-M4", "order_id": "GW-M4", "trade_id": "T-M4",
        "name": "贵州茅台", "code": "600519.SH", "side": "买入",
        "price": 10.0, "qty": 100, "amount": 1000.0,
        "traded_at": "2026-09-22T09:35:00+08:00", "signal_id": "S-M4",
    })
    assert code == 200, "桥成交回报转发失败: %s" % code
    assert pushed, "桥成交回报未进入上报队列"
    return pushed[-1]


class TestGatewayEmitsMatchGolden(unittest.TestCase):
    """① 实际发出键集 == golden.gateway_emitted_fields（逐事件类型）。"""

    @classmethod
    def setUpClass(cls):
        with open(GOLDEN_PATH, encoding="utf-8") as f:
            cls.golden = json.load(f)
        # 断线回报只在连续竞价时段上报（非交易时段静默是 §MIGRATION 设计），
        # 本契约锁关心的是"发出时带了哪些字段"，故把时段判定钉成 True 去掉时钟依赖。
        cls._real_session = handler_mod.is_active_trading_session
        handler_mod.is_active_trading_session = lambda *a, **k: True

    @classmethod
    def tearDownClass(cls):
        handler_mod.is_active_trading_session = cls._real_session

    def _emit_and_capture(self, h, pushed, fn, *args):
        del pushed[:]
        fn(*args)
        self.assertTrue(pushed, "未捕获到任何上报载荷：%s" % fn)
        return pushed[-1]

    def test_trade_legs(self):
        h, pushed = _capturing_handler()
        xt = self._emit_and_capture(h, pushed, h.on_stock_trade, _FakeTrade())
        # 丢腿本体：这三条必须在载荷里（Go 侧同名 tag 由 ② 与 Go 测试共同保证）
        self.assertEqual(xt["trade_id"], "T-9001")
        self.assertEqual(xt["name"], "贵州茅台")
        # §SIDE-AUTH-2：桥路径未命中派发行时带 side_unverified=true（并集来源）；
        # 命中派发行的回报与旧契约一样**不带该键**。
        bridge = _bridge_trade_payload()
        self.assertIs(bridge.get("side_unverified"), True,
                      "§SIDE-AUTH-2 未命中派发行的桥成交必须带 side_unverified")
        union = set(xt.keys()) | set(bridge.keys())
        self.assertEqual(sorted(union), sorted(self.golden["gateway_emitted_fields"]["trade"]),
                         "trade 两通道并集与 golden 不一致")

    def test_order_legs(self):
        h, pushed = _capturing_handler()
        payload = self._emit_and_capture(h, pushed, h.on_stock_order, _FakeOrder())
        self.assertEqual(sorted(payload.keys()), sorted(self.golden["gateway_emitted_fields"]["order"]))
        self.assertIn("created_at", payload)  # §M4：Go 旧信封缺这条，委托创建时间被记成回报时刻

    def test_positions_account_disconnect_heartbeat_broker(self):
        h, pushed = _capturing_handler()
        cases = [
            ("positions", h.on_positions, ([{"ts_code": "600519.SH", "name": "贵州茅台", "qty": 100,
                                             "cost_price": 10.0, "amount": 1000.0,
                                             "highest_price": 10.0, "updated_at": "2026-09-22T09:30:00+08:00"}],)),
            ("account", h.on_account, ({"cash": 1000.0, "frozen_cash": 0.0,
                                        "total_asset": 2000.0, "market_value": 1000.0},)),
            ("disconnect", h.on_disconnected, ()),
        ]
        for name, fn, args in cases:
            payload = self._emit_and_capture(h, pushed, fn, *args)
            self.assertEqual(sorted(payload.keys()), sorted(self.golden["gateway_emitted_fields"][name]),
                             "事件 %s 发出键集与 golden 不一致" % name)
        hb = {"type": "heartbeat", "user_id": h.user_id}
        self.assertEqual(sorted(hb.keys()), sorted(self.golden["gateway_emitted_fields"]["heartbeat"]))
        br = {"type": "broker", "broker": "queued", "from": "xt", "at": "2026-09-22T09:30:00+08:00",
              "user_id": h.user_id}
        self.assertEqual(sorted(br.keys()), sorted(self.golden["gateway_emitted_fields"]["broker"]))

    def test_positions_row_legs(self):
        """§0925EVE-W2-A1 真行采样锁（改前形态：本测试手喂 7 键合成行——测的是契约文件
        自己，网关每行实发的 can_use_qty/open_price 从未进入断言，断腿两侧全绿）。
        改后：消费 contract/positions_sample.json 的采样真行（形态=broker.py 真实产出键集），
        经 handler.on_positions **真实上报路径**入 outbox（该路径会为每行追加 user_id），
        断言上线行键集合 == golden.positions_row_emitted_fields **双向等值**
        （发出而未登记 → 红；登记而未发出 → 也红），并 ⊆ Go RealPosition 反射集。
        English: §A1 — the positions-row contract leg now consumes the sampled real broker row
        through the real on_positions path and asserts emitted keys == golden both ways.
        """
        h, pushed = _capturing_handler()
        rows = [dict(r) for r in _load_positions_sample()]
        payload = self._emit_and_capture(h, pushed, h.on_positions, rows)
        for i, row in enumerate(payload["positions"]):
            emitted = set(row.keys())
            self.assertEqual(
                emitted, set(self.golden["positions_row_emitted_fields"]),
                "第 %d 行采样上线键集与 golden 双向不等（多=%s 少=%s）——新键必须先进 golden，"
                "登记键必须真实发出（登记但停发=假腿，同样要修）" % (
                    i,
                    sorted(emitted - set(self.golden["positions_row_emitted_fields"])),
                    sorted(set(self.golden["positions_row_emitted_fields"]) - emitted)))
        self.assertTrue(emitted <= set(self.golden["positions_row_fields"]),
                        "对账行发出了 Go 不认的字段：%s" % (emitted - set(self.golden["positions_row_fields"])))

    def test_positions_sample_is_broker_real_emit(self):
        """§A1 样例合法性锁：采样文件的行键集 == XtBroker.query_positions 真实映射产出的键集
        （行为面，非 AST 文本）。emit 路径加/删键而不同步样例 → 红；样例凭空虚增键 → 也红。
        这条锁的存在理由：样例本身若与真实产出脱钩，采样锁会退化成又一份手喂合成行。
        """
        real = _xt_broker_emit_keys()
        for i, row in enumerate(_load_positions_sample()):
            self.assertEqual(set(row.keys()), real,
                             "第 %d 行样例键集与 broker.py 真实 emit 键集漂移（多=%s 少=%s）" % (
                                 i,
                                 sorted(set(row.keys()) - real),
                                 sorted(real - set(row.keys()))))

    def test_positions_sample_golden_consistency(self):
        """§A1 静态面：golden 登记集 == 样例键集 ∪ {user_id}（on_positions 追加腿）。
        与动态 test_positions_row_legs 互为印证：不跑 handler 也能在纯文件面钉住漂移。
        """
        with open(GOLDEN_PATH, encoding="utf-8") as f:
            golden = json.load(f)
        for i, row in enumerate(_load_positions_sample()):
            self.assertEqual(set(row.keys()) | {"user_id"},
                             set(golden["positions_row_emitted_fields"]),
                             "第 %d 行样例键集 ∪ {user_id} 与 golden 登记集不等" % i)


class TestEveryEmittedLegIsReceived(unittest.TestCase):
    """② golden 声明的每条发出字段都必须存在于 Go 信封反射集（丢腿负向锁）。"""

    def setUp(self):
        with open(GOLDEN_PATH, encoding="utf-8") as f:
            self.golden = json.load(f)

    def test_gateway_emitted_subset_of_envelope(self):
        envelope = set(self.golden["report_event_fields"])
        for ev_type, keys in self.golden["gateway_emitted_fields"].items():
            orphan = set(keys) - envelope
            self.assertFalse(orphan, "事件 %s 发出了 Go 信封没有的字段 %s（会被 encoding/json 静默丢弃）"
                             % (ev_type, sorted(orphan)))

    def test_consumed_subset_of_emitted(self):
        """Go 声称消费的字段，网关必须真的发（否则消费的是恒零值假腿）。"""
        for ev_type, keys in self.golden["consumed_by_event"].items():
            emitted = set(self.golden["gateway_emitted_fields"].get(ev_type, []))
            ghost = set(keys) - emitted
            self.assertFalse(ghost, "事件 %s 消费了网关未发出的字段 %s" % (ev_type, sorted(ghost)))


class TestBridgeTradePathKeepsLegs(unittest.TestCase):
    """queued 通道（文件/HTTP 桥回报）同锁：桥成交经 gateway._do_dispatch_result 转发时
    trade_id/name/stamp_tax 三条腿不得在网关中转时被吃掉。"""

    def test_dispatch_trade_forwards_legs(self):
        payload = _bridge_trade_payload()
        self.assertEqual(payload["trade_id"], "T-M4")
        self.assertEqual(payload["name"], "贵州茅台")
        self.assertIn("stamp_tax", payload)  # 桥 DEAL 行独有腿
        with open(GOLDEN_PATH, encoding="utf-8") as f:
            golden = json.load(f)
        self.assertTrue(set(payload.keys()) <= set(golden["gateway_emitted_fields"]["trade"]),
                        "桥转发发出了 golden 未登记的 trade 字段")


if __name__ == "__main__":
    unittest.main()
