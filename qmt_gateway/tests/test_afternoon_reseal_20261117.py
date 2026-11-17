# test_afternoon_reseal_20261117.py — 机内自算"午后回封"保护腿的行为锁（2026-11-17）。
#
# leader_decay / leader_decay_v2 是 live 卖出战法，但 ctx["afternoon_reseal"]
# 长期只从烘参读、恒 None ⇒ 整条战法结构性死亡（None=弃权，从不卖）。
# 17:46 真机只读探针证明这台 build 能取当日分钟K(get_market_data_ex 位置姿势)
# 与精确涨停价(get_instrument_detail 的 UpStopPrice)，于是改为机内自算。
#
# 这是**保护腿**：True/None 都不卖，只有明确 False 才允许按其它分数卖。
# owner 口径（2026-11-17）：任何读数缺失都必须保持 None（fail-closed，
# 绝不从"没有分钟数据"推断"没有回封"）。本文件逐条钉死这个口径。
#
# English: behaviour locks for the in-client afternoon-reseal protective gate:
# a missing reading must stay None so the sell is skipped, never inferred False.
import importlib.util
import os
import sys
import unittest

GW_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, GW_DIR)

_SPEC = importlib.util.spec_from_file_location(
    "qmt_strategy_afternoon_reseal", os.path.join(GW_DIR, "qmt_internal_strategy.py"))
mod = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(mod)


class _Entry(object):
    """Minimal stand-in for the QMT market-data DataFrame entry."""

    def __init__(self, closes, times=None):
        self.close = closes
        self._times = times

    @property
    def index(self):
        return self._times


class _FakeContext(object):
    """Only the two read APIs the adapter touches."""

    def __init__(self, up_stop=13.53, bars=None):
        self._up_stop = up_stop
        self._bars = bars or {}

    def get_instrument_detail(self, symbol):
        return {"UpStopPrice": self._up_stop}

    def get_market_data_ex(self, *args, **kwargs):
        return self._bars


class TestBarTimeThreshold(unittest.TestCase):
    """14:30 边界：字符串时间解析不依赖日期库、不依赖真实钟。"""

    def test_after_1430_shapes(self):
        for stamp in ("202611171430", "20261117143500", "2026-11-17 14:31:00",
                      "14:30", "143000"):
            self.assertTrue(mod._is_after_1430(stamp), stamp)

    def test_before_1430(self):
        for stamp in ("202611171429", "20261117100000", "09:31", "20261117135959"):
            self.assertFalse(mod._is_after_1430(stamp), stamp)

    def test_unparseable_is_false(self):
        for stamp in ("", "abc", "hello14:30world"):
            self.assertFalse(mod._is_after_1430(stamp), stamp)


class TestAfternoonResealPure(unittest.TestCase):
    """纯函数判据：秒板保护 True / 未回封 False / 缺读数 None。"""

    SYM = "000001.SZ"
    UP = 13.53
    TOL = 0.005            # = params ld_seal_tolerance 0.995 的容差

    def test_reseal_after_1430_is_true(self):
        payload = {self.SYM: _Entry([12.0, 13.53, 13.53],
                                    ["202611170930", "202611171430", "202611171435"])}
        self.assertIs(mod._afternoon_reseal_from(payload, self.SYM, self.UP, self.TOL), True)

    def test_no_bar_after_1430_is_false(self):
        payload = {self.SYM: _Entry([12.0, 12.5, 12.9],
                                    ["202611170930", "202611171000", "202611171429"])}
        self.assertIs(mod._afternoon_reseal_from(payload, self.SYM, self.UP, self.TOL), False)

    def test_missing_time_axis_is_none(self):
        # 有价格、没时间 ⇒ 无法证明 14:30 之后 ⇒ None（保护腿不卖）
        payload = {self.SYM: _Entry([13.53, 13.53], None)}
        self.assertIsNone(mod._afternoon_reseal_from(payload, self.SYM, self.UP, self.TOL))

    def test_missing_up_stop_is_none(self):
        payload = {self.SYM: _Entry([13.53], ["202611171430"])}
        self.assertIsNone(mod._afternoon_reseal_from(payload, self.SYM, None, self.TOL))

    def test_empty_payload_is_none(self):
        self.assertIsNone(mod._afternoon_reseal_from({}, self.SYM, self.UP, self.TOL))

    def test_tolerance_boundary(self):
        # 收盘刚好达 up*(1-tol) 算回封；差一分则不算。
        edge = self.UP * (1.0 - self.TOL)
        hit = {self.SYM: _Entry([edge], ["202611171430"])}
        miss = {self.SYM: _Entry([edge - 0.01], ["202611171430"])}
        self.assertIs(mod._afternoon_reseal_from(hit, self.SYM, self.UP, self.TOL), True)
        self.assertIs(mod._afternoon_reseal_from(miss, self.SYM, self.UP, self.TOL), False)

    def test_stime_column_read_without_index(self):
        class ColumnOnly(object):
            def __init__(self):
                self.close = [13.53, 13.53]
                self.stime = ["202611170930", "202611171435"]
        self.assertIs(mod._afternoon_reseal_from(
            {self.SYM: ColumnOnly()}, self.SYM, self.UP, self.TOL), True)


class TestAfternoonResealAdapter(unittest.TestCase):
    """适配器：假 ContextInfo 上两个只读 API，缺任一也返回 None。"""

    SYM = "000001.SZ"
    TOL = 0.005

    def test_adapter_happy_path(self):
        bars = {self.SYM: _Entry([12.0, 13.53], ["202611170930", "202611171430"])}
        self.assertIs(mod._afternoon_reseal(_FakeContext(13.53, bars), self.SYM, self.TOL, 80), True)

    def test_adapter_no_instrument_detail_is_none(self):
        class NoDetail(_FakeContext):
            def get_instrument_detail(self, symbol):
                return None
        bars = {self.SYM: _Entry([13.53], ["202611171430"])}
        self.assertIsNone(mod._afternoon_reseal(NoDetail(13.53, bars), self.SYM, self.TOL, 80))

    def test_adapter_no_bars_is_none(self):
        self.assertIsNone(mod._afternoon_reseal(_FakeContext(13.53, {}), self.SYM, self.TOL, 80))


if __name__ == "__main__":
    unittest.main()
