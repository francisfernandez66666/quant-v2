# test_carrier_flow_quadrant_20261120.py — §FLOW-QUADRANT / §FLOW-BLEND 载体侧行为锁（2026-11-20）。
#
# 背景（11-19/20 owner 裁决与物理机探针实测）：量比与净流入不是两套独立功能，而是同一条
# **后置金额乘子**（大盘/板块/量能三项 delta 均分、缺项 renormalize、合成后恒定封顶 ±20%）
# 加上**唯一一条硬闸**（禁抄底：熊市+放量+相对仓位高档）。取数面已被探针钉死：
#   * 量能：策略内 get_market_data_ex 的指数日 K（amount 元）可读 ⇒ 载体自算 r_yest/r_ma5；
#   * 净流入：QMT 八列全 NaN ⇒ 只能由云端每 30 分钟烘进 params.flow_snapshot；
#   * 外部 xtdata 在 XtItClient 上结构性不可用 ⇒ 任何"盒子上用 xtdata 取资金流"的写法都不成立。
#
# 本文件锁六件事（全部跑**构建出来的产物**，不读模板源码——模板对了产物可能还是错的）：
#   1. 量态三态双闸与"任一不可读即弃权"（NaN 不许冒充平量 0，那是把缺数算进合成）；
#   2. 快照新鲜度两把尺：跨日弃权 + 超 TTL 弃权（且负年龄只当钟差、不当过期）；
#   3. 四象限：只有"熊+放量+posRel 高档"落 block_buy，range/未知/低档一律 Neutral；
#   4. blend：缺项 renormalize、真零计入、全缺 1.0、上下界夹取；
#   5. sizing_cap 只在 per_trade 上乘 blend（等价 Go ComputeSizingWithBlend 的包装位置）；
#   6. 买入链硬闸只拦 new/add，T 腿豁免（range 态 T 必须照跑）。
#
# English: behaviour locks for the carrier-side flow quadrant and post-composition blend,
# exercised against the BUILT sandbox artifact (template-level grep cannot see the product).
import glob
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from datetime import datetime

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "qmt_gateway"))

BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")

# 盘中固定时刻（连续竞价中段）。用真钟会让"到 close_sweep_from 就撤在途单"那条收摊腿
# 在 14:52 之后插进来，断言随当日时刻漂移——11-05 那次假红就是这么来的。
_MID_SESSION = datetime(2026, 11, 20, 10, 30)


def _build(out_dir):
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox", "--out-dir", out_dir],
                          capture_output=True, text=True, cwd=REPO)
    path = ""
    for token in (proc.stdout + proc.stderr).replace("\n", " ").split():
        if token.startswith("artifact="):
            path = token.split("=", 1)[1]
    return proc.returncode, proc.stdout + proc.stderr, path


def _load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class _FakeEntry(object):
    """get_market_data_ex 的一条 DataFrame 替身：close/volume/amount + stime 时间轴。"""

    def __init__(self, amounts, stamps):
        self.close = list(amounts)
        self.volume = list(amounts)
        self.amount = list(amounts)
        self.stime = list(stamps)


class _FakeContext(object):
    """只实现载体真正会调的两个入口（get_market_data_ex / get_full_tick）。"""

    def __init__(self, payload=None, tick=None):
        self.payload = payload
        self.tick = tick or {}
        self.calls = []

    def get_market_data_ex(self, fields, codes, period="1d", count=0, *args, **kwargs):
        self.calls.append((tuple(fields), tuple(codes), period, count))
        return self.payload if self.payload is not None else {}

    def get_full_tick(self, codes):
        return self.tick


class CarrierFlowQuadrantTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = _build(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.mod = _load(path, "sandbox_flow_quadrant_under_test")
        # 产物里的 PARAMS 是烘进去的那份；逐条用例自己改，互不影响（setUp 重置）。
        cls.baked_params = dict(cls.mod.PARAMS)

    def setUp(self):
        self.mod.PARAMS.clear()
        self.mod.PARAMS.update(self.baked_params)
        self.mod._STATE.pop("round_flow", None)
        self.mod._STATE.pop("vol_ratio_state", None)
        self.mod._STATE.pop("flow_snapshot_state", None)

    # ---- ① 量态与弃权 -----------------------------------------------------
    def test_volume_state_three_states_and_abstain(self):
        """三态各自的判据 + 任一不可读必须**弃权**（None），不许落到平量 0。"""
        vol_state = self.mod._vol_state_of
        self.assertEqual(vol_state(1.3, 1.1), "surge")
        self.assertEqual(vol_state(0.9, 0.8), "shrink")
        self.assertEqual(vol_state(1.1, 0.95), "neutral")
        # 单边越线不算：放量要双闸同时成立（Go VolStateOf 同形）。
        self.assertEqual(vol_state(1.3, 0.95), "neutral")
        # None/0 是"读不到"，不是"平量"。
        self.assertEqual(vol_state(None, 1.1), "neutral")
        self.assertEqual(vol_state(0.0, 1.1), "neutral")

    def test_volume_ratio_today_from_last_bar_and_prorated(self):
        """末根日键=今日 ⇒ 当日额取末根；历史取 T-1..T-5，按 240/elapsed 折算。"""
        ctx = _FakeContext(payload={
            "000001.SH": _FakeEntry(
                [100.0, 100.0, 100.0, 100.0, 100.0, 100.0, 120.0],
                ["20261112", "20261113", "20261116", "20261117", "20261118",
                 "20261119", "20261120"]),
        })
        state = self.mod._vol_ratio_state(ctx, _MID_SESSION)
        self.assertEqual(state["reason"], "", state)
        # elapsed=60 ⇒ 折算 ×4：今日 120×4=480；T-1=100 ⇒ r_yest=4.8，五日均=100 ⇒ r_ma5=4.8
        self.assertAlmostEqual(state["r_yest"], 4.8, places=4)
        self.assertAlmostEqual(state["r_ma5"], 4.8, places=4)
        self.assertEqual(state["vol_state"], "surge")
        self.assertEqual(state["delta"], 1.0)
        self.assertTrue(state["source"].endswith(":bar"))

    def test_volume_ratio_falls_back_to_tick_when_last_bar_is_history(self):
        """末根仍是昨日 ⇒ 历史整段可用，当日累计改从指数 tick 的 amount 取。"""
        ctx = _FakeContext(
            payload={"000001.SH": _FakeEntry(
                [100.0] * 6,
                ["20261112", "20261113", "20261116", "20261117", "20261118", "20261119"])},
            tick={"000001.SH": {"amount": 30.0}})
        state = self.mod._vol_ratio_state(ctx, _MID_SESSION)
        self.assertEqual(state["reason"], "", state)
        self.assertTrue(state["source"].endswith(":tick"))
        self.assertAlmostEqual(state["r_yest"], 30.0 * 4.0 / 100.0, places=4)

    def test_volume_ratio_abstains_on_nan_amount(self):
        """NaN 必须弃权而不是变成 r=NaN→neutral→delta=0（那会把缺数算进合成）。"""
        ctx = _FakeContext(payload={
            "000001.SH": _FakeEntry(
                [100.0, 100.0, 100.0, 100.0, 100.0, float("nan"), 120.0],
                ["20261112", "20261113", "20261116", "20261117", "20261118",
                 "20261119", "20261120"]),
        })
        state = self.mod._vol_ratio_state(ctx, _MID_SESSION)
        self.assertIsNone(state["delta"], "NaN 历史额必须弃权，不能给出平量 0")
        self.assertIsNone(state["r_yest"])
        # 两个指数各试一次；reason 记的是最后一枚的失败原因，这里只要求它是个可读的弃权锚。
        self.assertTrue(state["reason"].startswith("vol_"), state)

    def test_volume_ratio_abstains_before_open(self):
        state = self.mod._vol_ratio_state(_FakeContext(), datetime(2026, 11, 20, 9, 20))
        self.assertEqual(state["reason"], "vol_premarket")
        self.assertIsNone(state["delta"])

    def test_volume_ratio_is_cached_per_minute(self):
        ctx = _FakeContext(payload={"000001.SH": _FakeEntry(
            [100.0] * 7,
            ["20261112", "20261113", "20261116", "20261117", "20261118", "20261119", "20261120"])})
        self.mod._vol_ratio_state(ctx, _MID_SESSION)
        first = len(ctx.calls)
        self.mod._vol_ratio_state(ctx, _MID_SESSION)
        self.assertEqual(len(ctx.calls), first, "同一分钟内重复读指数序列（缓存没生效）")

    # ---- ② 快照新鲜度 ----------------------------------------------------
    def _snapshot(self, **over):
        snap = {"day": "20261120", "baked_at": time.time(), "market_delta": -0.4,
                "sector_delta": {"银行": 0.5}, "market_state": "bear",
                "max_pos_pct": 0.35, "source": "cloud_flow_bake"}
        snap.update(over)
        return snap

    def test_snapshot_valid_same_day_and_direction(self):
        self.mod.PARAMS["flow_snapshot"] = self._snapshot()
        state = self.mod._flow_snapshot_state(_MID_SESSION)
        self.assertEqual(state["reason"], "")
        self.assertAlmostEqual(state["market_delta"], -0.4)
        self.assertAlmostEqual(state["sector_delta"]["银行"], 0.5)

    def test_snapshot_cross_day_abstains(self):
        self.mod.PARAMS["flow_snapshot"] = self._snapshot(day="20261119")
        state = self.mod._flow_snapshot_state(_MID_SESSION)
        self.assertEqual(state["reason"], "flow_snapshot_stale_day")
        self.assertIsNone(state["market_delta"])

    def test_snapshot_expired_abstains(self):
        self.mod.PARAMS["flow_snapshot"] = self._snapshot(baked_at=time.time() - 5 * 3600)
        state = self.mod._flow_snapshot_state(_MID_SESSION)
        self.assertEqual(state["reason"], "flow_snapshot_expired")
        self.assertIsNone(state["market_delta"])
        self.assertEqual(state["sector_delta"], {})

    def test_snapshot_negative_age_is_clock_skew_not_expiry(self):
        """盒子钟比云端慢几秒时年龄为负——那是钟差，不是"过期"（否则整天弃权）。"""
        self.mod.PARAMS["flow_snapshot"] = self._snapshot(baked_at=time.time() + 30.0)
        state = self.mod._flow_snapshot_state(_MID_SESSION)
        self.assertEqual(state["reason"], "", "负年龄被当成过期 ⇒ 时钟一偏就整天无资金流")

    def test_snapshot_missing_key_abstains(self):
        self.mod.PARAMS.pop("flow_snapshot", None)
        state = self.mod._flow_snapshot_state(_MID_SESSION)
        self.assertEqual(state["reason"], "flow_snapshot_missing")

    # ---- ③ 四象限 --------------------------------------------------------
    def _quad(self, vol_state, market_state, held, total, max_pos=0.35):
        return self.mod._quadrant_state(
            {"vol_state": vol_state, "delta": 0.0},
            {"market_state": market_state, "max_pos_pct": max_pos}, held, total)

    def test_only_bear_surge_high_blocks(self):
        q = self._quad("surge", "bear", 60.0, 100.0)     # posRel = 0.6/0.35 = 1.71
        self.assertTrue(q["block"])
        self.assertEqual(q["decision"], "block_buy")
        self.assertEqual(q["reason"], "bear_surge_high_nobottom")

    def test_bull_quadrants_do_not_block(self):
        for vol in ("surge", "shrink", "neutral"):
            q = self._quad(vol, "bull", 60.0, 100.0)
            self.assertFalse(q["block"], "牛市 %s 不许落硬闸（owner：除禁抄底外全走金额层）" % vol)
        self.assertEqual(self._quad("surge", "bull", 60.0, 100.0)["decision"], "reduce")
        self.assertEqual(self._quad("shrink", "bull", 60.0, 100.0)["decision"], "block_buy")

    def test_range_or_low_posrel_never_blocks(self):
        self.assertFalse(self._quad("surge", "range", 60.0, 100.0)["block"])
        self.assertFalse(self._quad("surge", "bear", 10.0, 100.0)["block"])   # posRel 0.29
        q = self._quad("surge", "range", 60.0, 100.0)
        self.assertEqual(q["reason"], "regime_not_quadrant")

    def test_unknown_target_abstains_the_quadrant(self):
        q = self._quad("surge", "bear", 60.0, 100.0, max_pos=None)
        self.assertFalse(q["block"])
        self.assertEqual(q["pos_rel"], 0.0)

    # ---- ④ blend ---------------------------------------------------------
    def _round_flow(self, market=None, sector=None, vol_delta=None):
        self.mod._STATE["round_flow"] = {
            "moment_key": "%s|%s" % (self.mod.day_key(_MID_SESSION),
                                     self.mod.hhmm_now(_MID_SESSION)),
            "vol": {"vol_state": "neutral", "delta": vol_delta},
            "snap": {"market_delta": market, "sector_delta": sector or {}},
            "quadrant": {"block": False},
        }

    def test_blend_renormalises_missing_items(self):
        self._round_flow(market=1.0, sector=None, vol_delta=None)
        self.assertAlmostEqual(self.mod._flow_blend_for("600519", _MID_SESSION), 1.2, places=4)

    def test_blend_keeps_a_real_zero(self):
        """0＝真平量，必须计入：三项均分 (1.0+0.0)/2=0.5 ⇒ 1.10，不是 1.20。"""
        self._round_flow(market=1.0, sector=None, vol_delta=0.0)
        self.assertAlmostEqual(self.mod._flow_blend_for("600519", _MID_SESSION), 1.1, places=4)

    def test_blend_all_missing_is_neutral(self):
        self._round_flow()
        self.assertEqual(self.mod._flow_blend_for("600519", _MID_SESSION), 1.0)

    def test_blend_clamps_to_bounds(self):
        self._round_flow(market=-1.0, sector=-1.0, vol_delta=-1.0)
        self.assertAlmostEqual(self.mod._flow_blend_for("600519", _MID_SESSION), 0.8, places=4)

    def test_blend_sector_resolution_uses_sector_map(self):
        """板块逐票腿走**专用** sector_map（东财板块名），不是 industry_map。"""
        self.mod.PARAMS["sector_map"] = {"600519": "酿酒行业"}
        self._round_flow(market=None, sector={"酿酒行业": 1.0}, vol_delta=None)
        self.assertAlmostEqual(self.mod._flow_blend_for("600519", _MID_SESSION), 1.2, places=4)
        # 没有该票映射 ⇒ 板块腿弃权（renormalize），不是 0。
        self.assertAlmostEqual(self.mod._flow_blend_for("000001", _MID_SESSION), 1.0, places=4)

    def test_industry_map_must_not_be_used_as_the_sector_join(self):
        """反证：industry_map 是证监会行业（C39…），与东财板块名交集为空。

        2026-11-20 现网实测：`industry_map` 79 个值全是 "C39计算机、通信和其他电子设备制造业"
        这种门类，而 `sector_delta` 的键是 "半导体/软件开发" 这类东财板块名 —— 用
        industry_map 当连接键会**看起来接上了、命中率恒 0**。这条锁保证没人再改回去。
        """
        self.mod.PARAMS.pop("sector_map", None)
        self.mod.PARAMS["industry_map"] = {"600519": "C15酒、饮料和精制茶制造业"}
        self._round_flow(market=None, sector={"C15酒、饮料和精制茶制造业": 1.0}, vol_delta=None)
        # 即使键名"恰好"写得一样也不许命中：连接键只能是 sector_map。
        self.assertAlmostEqual(self.mod._flow_blend_for("600519", _MID_SESSION), 1.0, places=4)

    def test_blend_pct_zero_is_the_off_switch(self):
        self.mod.PARAMS["flow_blend_pct"] = 0
        self._round_flow(market=1.0, sector=1.0, vol_delta=1.0)
        self.assertEqual(self.mod._flow_blend_for("600519", _MID_SESSION), 1.0)

    # ---- ⑤ sizing_cap 落点 -----------------------------------------------
    def test_sizing_cap_scales_only_per_trade(self):
        """blend 只乘 per_trade，且仍受 0.5×equity 上限（等价 Go PerTradePctOfBlend）。

        构造上故意把 order_amount_cap 抬到不绑定的位置：否则 min() 总是被别人拿走，
        "乘了 per_trade" 和 "根本没乘" 在读数上同形（这是恒绿用例的典型形状）。
        """
        self.mod.PARAMS.update({"sizing_enabled": True, "min_order_amount": 0.0,
                                "order_amount_cap": 1e9})
        self.mod._STATE["round_moment"] = _MID_SESSION
        ctx = {"symbol": "600519", "total_asset": 100000.0, "equity": 100000.0,
               "last_price": 10.0, "positions_live": [],
               "t_buy_leg_value": 0.0, "t_sell_leg_value": 0.0}
        # 无 blend 基线
        self.mod._STATE.pop("round_flow", None)
        self.mod._STATE.pop("flow_snapshot_state", None)
        plain, _why, plain_note = self.mod.sizing_cap(ctx, {}, "new", None)
        caps = self.mod.buy_size_caps(self.mod.PARAMS, self.mod._sizing_view(ctx, {}, None))
        plain_caps = min(v for v in caps.values() if v is not None)
        self.assertAlmostEqual(plain, plain_caps, places=2)
        # 三项全放量 ⇒ blend=1.20，仍然只作用在 per_trade 上
        self._round_flow(market=1.0, sector=1.0, vol_delta=1.0)
        amount, why, note = self.mod.sizing_cap(ctx, {}, "new", None)
        self.assertAlmostEqual(amount, min(plain_caps * 1.2, 50000.0), places=2,
                               msg="sizing_cap 没把 blend 乘在 per_trade 上")
        self.assertGreater(amount, plain, "blend=1.2 之后金额没变大 ⇒ 这把乘子没生效")
        self.assertEqual(why, "")
        self.assertIn("per_trade", note)

    def test_missing_knob_abstains_instead_of_using_a_fallback(self):
        """反证：载体里**不许**有阈值兜底常量（那就是第二本账）。

        三处缺键都必须落到"弃权"这一侧：量态→neutral、blend→1.0、象限→flow_knob_missing。
        任何一处回退到 1.2/1.3/0.20 这类字面量，改 Go 常量时载体就会继续按旧数交易。
        """
        self.mod.PARAMS.pop("flow_r_yest_surge")
        self.assertEqual(self.mod._vol_state_of(1.3, 1.1), "neutral")
        self.mod.PARAMS.pop("flow_blend_pct")
        self._round_flow(market=1.0, sector=1.0, vol_delta=1.0)
        self.assertEqual(self.mod._flow_blend_for("600519", _MID_SESSION), 1.0)
        self.mod.PARAMS.pop("flow_posrel_high")
        q = self._quad("surge", "bear", 60.0, 100.0)
        self.assertFalse(q["block"])
        self.assertEqual(q["reason"], "flow_knob_missing")

    # ---- ⑥ 买入链硬闸 ----------------------------------------------------
    def test_buy_chain_blocks_new_and_add_but_not_t_leg(self):
        src = open(os.path.join(REPO, "qmt_gateway", "qmt_internal_strategy.py"),
                   encoding="utf-8").read()
        self.assertIn('intent in ("new", "add") and _quadrant.get("block")', src,
                      "硬闸判据必须只拦 new/add（T 腿在 range 态要照跑）")
        self.assertIn('"flow_quadrant_block"', src)
        # 反证方向：闸门不许出现在 T 腿入口之前把整轮砍掉。
        self.assertNotIn('if _quadrant.get("block"):\n                summary["reason"]', src)


class HotReloadFlowSnapshotTest(unittest.TestCase):
    """烘参腿：只读库 → 快照 → 30 分钟节流 / 实质变化判定。"""

    @classmethod
    def setUpClass(cls):
        import importlib.util as iu
        path = os.path.join(REPO, "scripts", "carrier_hot_reload_gz.py")
        spec = iu.spec_from_file_location("hotreload_flow_under_test", path)
        cls.mod = iu.module_from_spec(spec)
        spec.loader.exec_module(cls.mod)

    def test_digest_ignores_baked_at_and_quantises(self):
        base = {"day": "20261120", "baked_at": 1.0, "market_delta": 0.4,
                "sector_delta": {"银行": 0.5}, "market_state": "bear", "max_pos_pct": 0.35}
        moved = dict(base, baked_at=999.0, market_delta=0.401)
        self.assertEqual(self.mod.flow_snapshot_digest(base),
                         self.mod.flow_snapshot_digest(moved),
                         "baked_at 或阈值内的抖动不该改变 digest（那会重启客户端）")
        real = dict(base, market_delta=0.9)
        self.assertNotEqual(self.mod.flow_snapshot_digest(base),
                            self.mod.flow_snapshot_digest(real))

    def test_digest_empty_for_missing_snapshot(self):
        self.assertEqual(self.mod.flow_snapshot_digest(None), "")
        self.assertEqual(self.mod.flow_snapshot_digest({}), "")

    def test_flow_snapshot_is_a_runtime_param(self):
        """快照必须登记成运行态键：否则静态基座会把它当"旧值"覆盖掉。"""
        self.assertIn("flow_snapshot", self.mod.RUNTIME_PARAM_KEYS)

    def test_read_flow_inputs_handles_both_date_spellings(self):
        """两张表的日期口径不同（flow 表带横线、risk 表不带），查错形状＝永远"今天没资金流"。"""
        import sqlite3
        d = tempfile.mkdtemp()
        db = os.path.join(d, "trading.db")
        now = datetime.now().astimezone()
        compact = now.strftime("%Y%m%d")
        dashed = now.strftime("%Y-%m-%d")
        conn = sqlite3.connect(db)
        conn.execute("CREATE TABLE flow_factor_daily (trade_date TEXT, market_flow_pct REAL,"
                     " flow_market_delta REAL)")
        conn.execute("CREATE TABLE sector_flow_daily (trade_date TEXT, sector TEXT, delta REAL)")
        conn.execute("CREATE TABLE market_risk_daily (trade_date TEXT, market_state TEXT,"
                     " max_pos_pct REAL)")
        conn.execute("INSERT INTO flow_factor_daily VALUES (?,?,?)", (dashed, -0.52, -0.17))
        conn.execute("INSERT INTO sector_flow_daily VALUES (?,?,?)", (dashed, "银行", 0.33))
        conn.execute("INSERT INTO market_risk_daily VALUES (?,?,?)", (compact, "bear", 0.35))
        conn.commit()
        conn.close()
        old = self.mod.HOTRELOAD_DB
        self.mod.HOTRELOAD_DB = db
        try:
            got = self.mod._read_flow_inputs(compact)
        finally:
            self.mod.HOTRELOAD_DB = old
        self.assertAlmostEqual(got["market_delta"], -0.17)
        self.assertAlmostEqual(got["sectors"]["银行"], 0.33)
        self.assertEqual(got["market_state"], "bear")
        self.assertAlmostEqual(got["max_pos_pct"], 0.35)

    def test_bake_is_throttled_and_only_reports_material_change(self):
        """30 分钟节流 + 只在实质变化时报 changed（changed 会重启盒子客户端）。"""
        import sqlite3
        d = tempfile.mkdtemp()
        os.makedirs(os.path.join(d, "qmt_gateway"))
        with open(os.path.join(d, "qmt_gateway", "local_executor_params.json"), "w",
                  encoding="utf-8") as fh:
            json.dump({"params": {}}, fh)
        db = os.path.join(d, "trading.db")
        now = datetime.now().astimezone()
        compact, dashed = now.strftime("%Y%m%d"), now.strftime("%Y-%m-%d")
        conn = sqlite3.connect(db)
        conn.execute("CREATE TABLE flow_factor_daily (trade_date TEXT, market_flow_pct REAL,"
                     " flow_market_delta REAL)")
        conn.execute("CREATE TABLE sector_flow_daily (trade_date TEXT, sector TEXT, delta REAL)")
        conn.execute("CREATE TABLE market_risk_daily (trade_date TEXT, market_state TEXT,"
                     " max_pos_pct REAL)")
        conn.execute("INSERT INTO flow_factor_daily VALUES (?,?,?)", (dashed, -0.5, -0.2))
        conn.execute("INSERT INTO market_risk_daily VALUES (?,?,?)", (compact, "bear", 0.35))
        conn.commit()
        conn.close()
        keep = (self.mod.BUILD_DIR, self.mod.HOTRELOAD_DB, self.mod.FLOW_SNAP_ATTEMPT)
        self.mod.BUILD_DIR = d
        self.mod.HOTRELOAD_DB = db
        self.mod.FLOW_SNAP_ATTEMPT = os.path.join(d, "attempt.json")
        try:
            params_path = os.path.join(d, "qmt_gateway", "local_executor_params.json")

            def _land(snapshot):
                """模拟主流程把快照写进参数文件（bake 只负责算与比，不负责落盘）。"""
                with open(params_path, "w", encoding="utf-8") as fh:
                    json.dump({"params": {"flow_snapshot": snapshot}}, fh)

            snap, changed = self.mod.bake_flow_snapshot()
            self.assertTrue(changed, "第一次烘入必须算变化（否则盒子永远拿不到快照）")
            self.assertEqual(snap["day"], compact)
            self.assertEqual(snap["market_state"], "bear")
            _land(snap)
            # 立刻再跑：节流命中，既不重算也不报变化
            again, moved = self.mod.bake_flow_snapshot()
            self.assertFalse(moved)
            self.assertEqual(again, snap)
            # 把节流时间拨回去：数据没动 ⇒ digest 相同 ⇒ changed=False（不重启客户端）
            import json as _json
            with open(self.mod.FLOW_SNAP_ATTEMPT, "w", encoding="utf-8") as fh:
                _json.dump({"at": 0, "day": compact}, fh)
            _same, moved = self.mod.bake_flow_snapshot()
            self.assertFalse(moved, "数据没变却报 changed ⇒ 每半小时重启一次客户端")
            # 真的动了 ⇒ changed=True
            conn = sqlite3.connect(db)
            conn.execute("UPDATE flow_factor_daily SET flow_market_delta=0.9")
            conn.commit()
            conn.close()
            with open(self.mod.FLOW_SNAP_ATTEMPT, "w", encoding="utf-8") as fh:
                _json.dump({"at": 0, "day": compact}, fh)
            moved_snap, moved = self.mod.bake_flow_snapshot()
            self.assertTrue(moved, "delta 实质变化没被识别 = 盒子永远停在旧读数")
            self.assertAlmostEqual(moved_snap["market_delta"], 0.9)
        finally:
            (self.mod.BUILD_DIR, self.mod.HOTRELOAD_DB, self.mod.FLOW_SNAP_ATTEMPT) = keep


class _CounterRow(object):
    """柜台表行替身：只给判据要读的字段，其余留空让载体自己认"读不到"。"""

    def __init__(self, **fields):
        for name, value in fields.items():
            setattr(self, name, value)


class _RoundContext(object):
    """run_round 用的最小 ContextInfo：tick + 指数日K，其余一律空。"""

    def __init__(self, ticks, index_amounts=None, stamps=None):
        self._ticks = ticks
        self._amounts = index_amounts or [100.0] * 7
        self._stamps = stamps or ["20261112", "20261113", "20261116", "20261117",
                                  "20261118", "20261119", "20261120"]

    def get_full_tick(self, symbols):
        return dict((s, self._ticks.get(s, {})) for s in symbols)

    def get_market_data_ex(self, fields, codes, period="1d", count=0, *args, **kwargs):
        out = {}
        for code in codes:
            out[code] = _FakeEntry(self._amounts, self._stamps)
        return out


class CarrierFlowRoundWiringTest(unittest.TestCase):
    """接线锁：run_round 里那条腿**真的被调用**（不是"函数在产物里"）。

    为什么必须跑真轮：本仓反复踩的形状是"模块在、没人问它"（移植规则、上行腿都栽过）。
    这里钉三件事：① 每轮真的装配了 round_flow；② 每轮真的打了小时读数；
    ③ 硬闸在 buy 分支里、且在 sizing 之前（放到 _place 之后就等于没有闸）。
    """

    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        rc, out, path = _build(cls.dir)
        if rc != 0:
            raise AssertionError("sandbox build failed: %s" % out[-800:])
        cls.artifact_path = path
        cls.mod = _load(path, "sandbox_flow_round_under_test")

    def setUp(self):
        # 每轮缓存按 day|HHMM 键存（同一分钟内复用），用例之间必须清掉，否则
        # 第二条用例会读到第一条留下的弃权态——"这条腿坏了"和"缓存没清"就分不清了。
        for key in ("round_flow", "vol_ratio_state", "flow_snapshot_state", "flow_tally"):
            self.mod._STATE.pop(key, None)

    def _run_round(self):
        # 一轮"最干净的假柜台"：有资金、无持仓、无委托 ⇒ 买入链走得到判据、
        # 卖出链无票可卖，于是这一轮只反映本批要测的那条腿。
        mod = self.mod
        symbol = (mod.PARAMS.get("symbols") or ["600000"])[0]
        mod._SANDBOX_FNS.update({
            "passorder": lambda *a: None,
            "cancel": lambda *a: None,
            "get_account": lambda *a: "8001",
            "get_trade_detail_data": lambda acct, kind, table: (
                [_CounterRow(m_dAvailable=500000.0, m_dBalance=500000.0,
                             total_asset=500000.0)] if table == "ACCOUNT" else []),
        })
        mod._STATE.update({"acct": "8001", "acct_type": "", "ops": [], "cancel_tried": [],
                           "warned": {}, "last_round": 0.0, "flow_log_key": ""})
        logs = []
        original = mod._log
        mod._log = lambda text: logs.append(text)
        try:
            summary = mod.run_round(_RoundContext(
                {symbol: {"last_price": 9.7, "prev_close": 10.0, "volume": 100000,
                          "ask1": 9.71, "bid1": 9.7}}), _MID_SESSION)
        finally:
            mod._log = original
        return summary, logs

    def test_round_prepares_the_flow_readings(self):
        self._run_round()
        round_flow = self.mod._STATE.get("round_flow") or {}
        self.assertTrue(round_flow, "run_round 没装配 round_flow ⇒ _flow_round_prepare 没被调用")
        self.assertEqual(round_flow.get("moment_key"),
                         "%s|%s" % (self.mod.day_key(_MID_SESSION),
                                    self.mod.hhmm_now(_MID_SESSION)),
                         "round_flow 的分钟键与本轮时刻不一致（传错 moment？）")
        # 指数日K 是假数据（7 根全 100、末根=今日）⇒ 量比腿必须真的算出数：
        # 10:30 elapsed=60 ⇒ 折算 ×4 ⇒ r_yest=r_ma5=4.0 ⇒ 放量。
        # 这条同时证明"取的是 bar 而不是 tick"（source 后缀）与"折算真的做了"（4.0 而非 1.0）。
        vol = round_flow.get("vol") or {}
        self.assertEqual(vol.get("reason"), "", vol)
        self.assertAlmostEqual(vol.get("r_yest"), 4.0, places=3)
        self.assertEqual(vol.get("vol_state"), "surge", vol)
        self.assertTrue(str(vol.get("source")).endswith(":bar"), vol)

    def test_round_prints_the_hourly_flow_line(self):
        _summary, logs = self._run_round()
        lines = [line for line in logs if line.startswith("flow vol_state=")]
        self.assertEqual(len(lines), 1, "小时读数没打（或打了两遍）：%s" % logs[:6])
        self.assertIn("sector_hit=", lines[0])

    def test_quadrant_gate_sits_inside_the_buy_branch_before_sizing(self):
        src = open(self.artifact_path, encoding="utf-8").read()
        body = src[src.index("def run_round("):src.index("def cancel_leg(")]
        i_intent = body.index("intent = _intent_of(")
        i_block = body.index('"flow_quadrant_block"')
        i_sizing = body.index("sizing_cap(ctx, positions, intent")
        self.assertLess(i_intent, i_block,
                        "四象限硬闸没有落在 intent 算出之后（判据拿不到 new/add）")
        self.assertLess(i_block, i_sizing,
                        "四象限硬闸落在 sizing 之后：等于先算完额度再拦，闸形同虚设")

    def test_a_raising_market_data_call_never_stops_the_round(self):
        """只读腿抛异常必须降级成弃权，绝不许把整轮带走（11-19 那次全天零委托的形状）。"""
        class _Boom(_RoundContext):
            """日K 全是历史（无今日那根），于是量比腿去读指数 tick —— 那里炸。"""

            def __init__(self, ticks):
                super(_Boom, self).__init__(
                    ticks, index_amounts=[100.0] * 6,
                    stamps=["20261112", "20261113", "20261116", "20261117",
                            "20261118", "20261119"])

            def get_full_tick(self, symbols):
                raise RuntimeError("client exploded")

        # 同上：假柜台 + 客户端在 tick 读上炸；判的是"整轮活下来且该腿弃权"。
        mod = self.mod
        symbol = (mod.PARAMS.get("symbols") or ["600000"])[0]
        mod._SANDBOX_FNS.update({
            "passorder": lambda *a: None, "cancel": lambda *a: None,
            "get_account": lambda *a: "8001",
            "get_trade_detail_data": lambda acct, kind, table: (
                [_CounterRow(m_dAvailable=500000.0, m_dBalance=500000.0,
                             total_asset=500000.0)] if table == "ACCOUNT" else []),
        })
        mod._STATE.update({"acct": "8001", "acct_type": "", "ops": [], "cancel_tried": [],
                           "warned": {}, "last_round": 0.0})
        summary = mod.run_round(_Boom({symbol: {"last_price": 9.7, "prev_close": 10.0,
                                                "volume": 100000, "ask1": 9.71,
                                                "bid1": 9.7}}), _MID_SESSION)
        self.assertIsInstance(summary, dict)
        vol = (mod._STATE.get("round_flow") or {}).get("vol") or {}
        # 读取链自己会把异常收成弃权（tick 读失败 → 无当日额），所以这里判的是
        # "整轮活下来 + 该腿弃权"，而不是某一条具体 reason。
        self.assertTrue(str(vol.get("reason")).startswith("vol_"), vol)
        self.assertIsNone(vol.get("delta"), "读失败却给出了 delta（会把缺数算进 blend）")

    def test_prepare_guard_turns_an_unexpected_exception_into_abstain(self):
        """兜底那一层单独咬：量比腿真抛出来时，round_flow 必须是一份弃权态而不是异常。

        上一条用例走的是"客户端异常被各层自己吞掉"的现实路径；这一条直接让
        _vol_ratio_state 抛，验的是 _flow_round_prepare 自己的 try/except 还在。
        """
        mod = self.mod
        saved = mod._vol_ratio_state
        mod._vol_ratio_state = lambda *a, **k: (_ for _ in ()).throw(RuntimeError("boom"))
        try:
            round_flow = mod._flow_round_prepare(object(), {}, 100000.0, _MID_SESSION)
        finally:
            mod._vol_ratio_state = saved
        vol = round_flow.get("vol") or {}
        self.assertEqual(vol.get("reason"), "vol_read_failed", vol)
        self.assertIsNone(vol.get("delta"))
        self.assertFalse((round_flow.get("quadrant") or {}).get("block"),
                         "读失败时不许落硬闸（弃权方向必须是放行，不是拦）")


if __name__ == "__main__":
    unittest.main()
