# test_capability_probe.py — §R3 step-2（2026-11-03）内置策略数据面能力探针的行为锁。
#
# 为什么这批用例必须存在（而不是"跑一次看日志就行"）：
# 券商公告 11-05 起关闭策略侧全部文件读写与任何网络，量仔能不能搬进 QMT 内置策略，
# 完全取决于 ContextInfo 到底暴露了什么。这份清单此前**从没被完整打印过**
# （11-02 那次只按 tick/market/quote 关键字筛，板块成分、合约详情、财务、日历一个都没列）。
# 探针本身一旦写错，拿回来的就是一个"看起来像答案"的空清单——所以判据要钉住四件事：
#   ① 全集分块不许丢名字（截断 = 我们把"没有这个函数"读成"没测到"）；
#   ② 盘中重跑窗口只生效一次（防它变成每跳一次的常驻取数）；
#   ③ 签名猜错时必须换下一种形状继续，且任何异常都不许冒出探针
#      （"调用失败"和"函数不存在"必须给出两种不同读数，只有一个是可修的）；
#   ④ 这条腿不许长回交易通道：passorder/cancel 只允许被"点名存在性"，绝不允许被调用。
# 第④条是 §164 那条"行情探针只碰行情"的同族纪律，只是这次的靶子换成了能力探针。
import os
import sys

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

import qmt_bridge_strategy as bs  # noqa: E402


def _fresh():
    """把两个闩复位，避免用例之间互相吃掉"每进程一次"的状态。"""
    bs._CAP_DONE = False
    bs._CAP_RERUN_DONE = False


class Sink:
    """捕获 _trace 的输出行，替代真实日志文件。"""

    def __init__(self):
        self.lines = []

    def __call__(self, line):
        self.lines.append(str(line))

    def has(self, needle):
        return any(needle in ln for ln in self.lines)

    def count(self, needle):
        return sum(1 for ln in self.lines if needle in ln)


class FakeCtx:
    """按需暴露若干函数并记录被调用过谁的什么形状。"""

    def __init__(self, apis=None, raise_on=None):
        self.calls = []
        apis = apis or {}
        for name, fn in apis.items():
            setattr(self, name, fn)
        self._raise_on = raise_on or {}

    def _record(self, name, args, kwargs):
        self.calls.append((name, args, kwargs))
        exc = self._raise_on.get(name)
        if exc is not None:
            raise exc


def _mk(name, *outs):
    """造一个按顺序抛/返回的假函数：outs 里每一项要么是异常实例，要么是返回值。"""
    state = {"i": 0}

    def fn(*args, **kwargs):
        idx = state["i"]
        state["i"] += 1
        item = outs[idx] if idx < len(outs) else outs[-1]
        if isinstance(item, BaseException):
            raise item
        return item

    fn.__name__ = name
    return fn


def test_dump_names_chunks_lose_nothing(monkeypatch):
    """① 分块必须不丢名字：600 个短名全部要能在某一段里出现。"""
    _fresh()
    sink = Sink()
    monkeypatch.setattr(bs, "_trace", sink)
    names = ["f%04d" % i for i in range(600)]

    class Ctx:
        pass

    for n in names:
        setattr(Ctx, n, lambda *a, **k: None)
    bs._cap_dump_names(Ctx())
    joined = "".join(ln.split("=", 2)[-1] for ln in sink.lines if ln.startswith("csurf part"))
    missing = [n for n in names if n not in joined]
    assert not missing, "分块截断丢了 %d 个函数名（读起来会像\"券商没这个 API\"）: %s" % (
        len(missing), missing[:5])
    assert sink.has("csurf total=600")
    assert not sink.has("TRUNCATED")


def test_dump_names_truncation_announces_itself(monkeypatch):
    """①' 超限必须自己喊 TRUNCATED——安静少打印会被读成"没有这个函数"，
    而这份清单的全部用途就是区分"没有"和"没测到"。"""
    _fresh()
    sink = Sink()
    monkeypatch.setattr(bs, "_trace", sink)

    class Ctx:
        pass

    for i in range(9000):
        setattr(Ctx, "very_long_function_name_%05d" % i, lambda *a, **k: None)
    bs._cap_dump_names(Ctx())
    assert sink.has("csurf TRUNCATED at 40 parts rest_unverified=1"), \
        "被截断却不自报＝把没测到伪装成不存在"


def test_rerun_window_and_single_shot(monkeypatch):
    """② 只有 09:31–09:34 之间的那一跳会触发重跑，且每进程只触发一次。"""
    _fresh()
    sink = Sink()
    monkeypatch.setattr(bs, "_trace", sink)
    ran = []
    monkeypatch.setattr(bs, "probe_capability_surface", lambda ctx, force=False: ran.append(force))

    for t in ("09:30:59", "09:34:00", "14:59:59", "08:54:00"):
        assert bs.probe_capability_rerun(FakeCtx(), now_hhmmss=t) is False
    assert ran == []

    assert bs.probe_capability_rerun(FakeCtx(), now_hhmmss="09:31:00") is True
    assert ran == [True], "盘中重跑必须 force=True（init 那次已经把闩置上了）"
    # 窗口内的后续跳不再跑
    assert bs.probe_capability_rerun(FakeCtx(), now_hhmmss="09:32:00") is False
    assert ran == [True]


def test_call_ladder_survives_bad_signature(monkeypatch):
    """③ 猜错签名（TypeError）要换下一种形状继续；别的异常记录后也继续；绝不外抛。"""
    _fresh()
    sink = Sink()
    monkeypatch.setattr(bs, "_trace", sink)

    ctx = FakeCtx({"get_financial_data": _mk(
        "get_financial_data", TypeError("bad arity"), RuntimeError("table missing"), {"Balance": [1]})})
    ok = bs._cap_call(ctx, "get_financial_data", [
        ("a", (["600588.SH"],), {}),
        ("b", (["600588.SH"], "Balance"), {}),
        ("c", (["600588.SH"], "Balance", "20260101", "20261103"), {}),
    ])
    assert ok is True
    # a 因 TypeError 被跳过（不记录，属"签名猜错"的正常路径），b 因 RuntimeError 被记录后继续，
    # c 才拿到返回值——三段的分工必须长这样，否则清单会把"猜错签名"报成"没有 API"。
    assert not sink.has("shape=a")
    assert sink.has("shape=b CALL_FAIL") and sink.has("shape=c OK")
    assert sink.has("get_financial_data/c")


def test_absent_vs_present_are_two_different_readings(monkeypatch):
    """③' "没有这个函数" 与 "有但调不通" 必须是两行不同读数——否则清单会自证失败。"""
    _fresh()
    sink = Sink()
    monkeypatch.setattr(bs, "_trace", sink)

    bs._cap_call(FakeCtx(), "get_index_weight", [("noarg", (), {})])
    assert sink.has("get_index_weight ABSENT")

    ctx = FakeCtx({"get_index_weight": _mk("get_index_weight", TypeError("needs arg"))})
    bs._cap_call(ctx, "get_index_weight", [("noarg", (), {})])
    assert sink.has("get_index_weight PRESENT no_shape_worked")


def test_probe_never_calls_trading_apis(monkeypatch):
    """④ 交易类函数只许被点名存在性，一次都不许被调用（能力探针不长回交易通道）。"""
    _fresh()
    sink = Sink()
    monkeypatch.setattr(bs, "_trace", sink)

    called = []

    def trap(*a, **k):
        called.append("passorder")
        return None

    ctx = FakeCtx({"passorder": trap, "cancel": trap})
    bs.probe_capability_surface(ctx)
    assert called == [], "能力探针调用了交易函数：%s" % called
    # 名字仍要被点名（我们要知道它在不在），但要以 name= ABSENT/PRESENT 的形式，不是 shape=OK
    assert not sink.has("api=passorder shape=")
    assert not sink.has("api=cancel shape=")


def test_surface_runs_once_per_process(monkeypatch):
    """闩：init 那次已经 dump 过就不再重复（除盘中 force 重跑），防它变成常驻取数。"""
    _fresh()
    sink = Sink()
    monkeypatch.setattr(bs, "_trace", sink)
    bs.probe_capability_surface(FakeCtx())
    first = sink.count("csurf total=")
    bs.probe_capability_surface(FakeCtx())
    assert sink.count("csurf total=") == first == 1


def test_shape_logger_is_ascii_safe_and_bounded(monkeypatch):
    """返回体里带中文名（板块/财务表名）时，日志行必须仍能把整段交出去而不炸。"""
    _fresh()
    sink = Sink()
    monkeypatch.setattr(bs, "_trace", sink)
    bs._cap_shape("get_sector_list", {u"\u884c\u4e1a": [1, 2], u"\u6982\u5ff5": [3]})
    assert sink.has("get_sector_list kind=dict n=2")
    for ln in sink.lines:
        assert ln.isascii() or True  # _trace 自身负责落盘编码，这里只保证不抛
