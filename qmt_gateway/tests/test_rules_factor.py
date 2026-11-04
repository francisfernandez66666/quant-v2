# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_rules_factor.py —— 因子战法 / 形态战法 Python 移植的行为锁。

为什么有这份文件（2026-11-05 券商新规之后）：
  券商关闭了 QMT 内置策略的本地文件读写与一切外部网络，自动化只剩"信号完全在 QMT
  内部产生"这一种合法形态。原先打分在 Go（internal/strategies/factor、
  internal/strategies/pattern），11-05 之后必须能在沙箱里的纯 Python 里跑。
  移植一旦走样，表现为"同一条战法在量和在 Python 判得不一样"——这种偏差不会报错、
  不会变红，只会静默换掉成交，所以每一条都要用行为用例钉住，而不是只钉语法。

这里锁的东西，逐条都是"改错了会亏钱而且没人报警"的形状：
  (1) 沙箱契约：整份 rules_factor.py 纯 ASCII（要进 GBK 终端）、除 math 外零依赖、
      全文不出现文件/网络/进程原语、规则层（标记之间）零数字字面量——阈值只能来自
      params，改旋钮必须重烘焙，不允许"代码里还留着旧数字"；
  (2) 打分口径（Go factor.go:253 scoreRule）：时序分位 × 权重、看空方向改写成
      w*(1-pct)、分母只累计"真的打上了分"的权重、复合分映射到 [floor, full] 域、
      阈值 <=0 回退默认值 70、factor_thresholds 覆盖优先；
  (3) 分位口径（Go factor.go:512 percentile）：涨到最高=1、横盘=0、跌=0、
      样本不足=弃权（返回 None，绝不当 0 分参与复合）；
  (4) 形态口径（Go pattern.go:198 evalRule）：条件区间 [min, max) 右开、
      必须全部命中才放行（AND），NaN 只计入分母不计入命中；
  (5) **缺读数一律弃权**：K 线不足、非交易日、卖出侧、以及所有"沙箱根本拿不到输入"
      的因子族（东财主力资金流、新闻/LLM、概念热度、情绪历史、涨停微结构、指数权重、
      财务 PIT、换手序列、股本/估值面板）——一律 return None，不伪造、不补 0；
  (6) 显式代理必须是"显式"的：只有操作员把 band 烘进 params 并且 ctx 真的带了值，
      代理路径才存在；band 在、值不在，仍然弃权。

全部用例不需要 QMT、不需要 xtquant、不需要网络、不读盘：ctx 里的日K是内存构造的
字典列表，params 是 PARAMS_TEMPLATE 的副本加覆盖项。
（English: behaviour locks for the ported factor/pattern rule library — the sandbox
contract, the Go scoring/thresholding/normalisation semantics, the half-open pattern
bands, and the decline-when-the-readout-is-missing rule for every input the QMT
sandbox can no longer obtain.）
"""

import ast
import math
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
sys.path.insert(0, GW_DIR)

import rules_factor as rf  # noqa: E402

RULE_BEGIN = "# ==== RULE LAYER BEGIN ===="
RULE_END = "# ==== RULE LAYER END ===="
MODULE_PATH = os.path.join(GW_DIR, "rules_factor.py")

# 会被"沙箱里没有这味输入"打立的因子（含因子库外、只由 ctx 供数的资金流/新闻族）。
SANDBOX_ONLY_INPUTS = [
    "FlowToday", "Flow5d",          # 东财主力净流入
    "NewsEvent", "ConceptHeat", "IsLeader", "IndexChange",  # 新闻/LLM/概念/指数
    "emo_limit_up", "emo_max_board", "emo_blast_rate", "emo_blast_cnt", "emo_chg5",
    "lu_board", "lu_board3", "lu_seal_strength", "lu_early_seal", "lu_reblast_low",
    "ROE", "GrossMargin", "NetMargin", "DebtToAssets", "YoyNetProfit", "SUE",
    "EP_ttm", "BP", "SP_ttm", "CFP_ttm", "DP",
    "STO20", "TurnoverStd20", "LnMktCap",
]


def params(**overrides):
    """PARAMS_TEMPLATE 的副本 + 覆盖项（用例只改自己要改的那一枚）。"""
    table = dict(rf.PARAMS_TEMPLATE)
    table.update(overrides)
    return table


def bars(count=60, price_fn=None, volume=1000.0):
    """内存日K：默认横盘，字段名与沙箱 ctx 的 bars 形状一致。"""
    price_fn = price_fn or (lambda index: 100.0)
    out = []
    for index in range(count):
        close = float(price_fn(index))
        out.append({
            "time": 20260101 + index,
            "open": close,
            "high": close + 1.0,
            "low": close - 1.0,
            "close": close,
            "volume": float(volume),
            "amount": float(volume) * close,
        })
    return out


def ramp_bars(count=60, flat_until=45, daily_gain=0.02):
    """横盘后末端加速上攻：Mom20 的时序分位=1（Go 侧同款形状）。"""
    def price(index):
        if index < flat_until:
            return 100.0
        return 100.0 * (1.0 + daily_gain * (index - flat_until + 1))
    return bars(count=count, price_fn=price)


def shrinking_bars(count=60, tail=5, tail_volume=200.0):
    """价格横盘 + 末端缩量：VolShrink < 1、Drawdown20 = 0。"""
    def price(_index):
        return 100.0
    series = bars(count=count, price_fn=price)
    for index in range(count - tail, count):
        series[index]["volume"] = float(tail_volume)
        series[index]["amount"] = float(tail_volume) * 100.0
    return series


def ctx(**overrides):
    """一份"什么都齐"的沙箱上下文；缺什么由用例自己摘。"""
    base = {
        "symbol": "600000.SH",
        "day": "20261106",
        "hhmm": "10:15",
        "trading_day": True,
        "last_price": 110.0,
        "prev_close": 100.0,
        "available_cash": 100000.0,
        "daily_buy_total": 0.0,
        "position_qty": 0.0,
        "bars": ramp_bars(),
    }
    base.update(overrides)
    return base


def mom_rule(threshold=70.0, direction=1, weight=1.0, rule_id="fac_mom"):
    """单因子（Mom20）规则表项，形状同 Go factor.ActiveRule。"""
    return {
        "id": rule_id,
        "name": "factor_composite",
        "factors": ["Mom20"],
        "weights": {"Mom20": weight},
        "directions": {"Mom20": direction},
        "buy_threshold": threshold,
    }


# ---------------------------------------------------------------------------
# (1) 沙箱契约：ASCII / 依赖 / 规则层零字面量 / 参数清单不漂
# ---------------------------------------------------------------------------

class TestSandboxContract(unittest.TestCase):
    """新规把"策略只能待在沙箱里"变成硬约束，这几条就是那约束的可执行版本。"""

    def source(self):
        with open(MODULE_PATH, "r", encoding="utf-8") as handle:
            return handle.read()

    def test_module_is_pure_ascii(self):
        """GBK 终端里一个非 ASCII 字节就是整份文件读不进来。"""
        with open(MODULE_PATH, "rb") as handle:
            raw = handle.read()
        offenders = [index for index, item in enumerate(raw) if item > 127]
        self.assertEqual(offenders, [], "non-ascii bytes at %s" % offenders[:5])

    def test_module_has_no_side_channel(self):
        """文件/网络/进程原语一次都不许出现（连注释里都不写，免得被当成待接的口子）。"""
        text = self.source()
        for token in ("open(", "socket", "requests", "urllib", "sqlite3",
                      "subprocess", "os.", "sys."):
            self.assertNotIn(token, text, "forbidden token in module: %s" % token)

    def test_only_stdlib_math_is_imported(self):
        tree = ast.parse(self.source())
        imported = []
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                imported.extend(alias.name for alias in node.names)
            elif isinstance(node, ast.ImportFrom):
                imported.append(str(node.module))
        self.assertEqual(imported, ["math"])

    def test_rule_layer_contains_no_numeric_literal(self):
        """标记之间是判钱的地方：任何裸数字都可能是"改了旋钮没重烘焙"的阈值。"""
        text = self.source()
        lines = text.splitlines()
        begin = next(index for index, line in enumerate(lines, 1)
                     if line.strip() == RULE_BEGIN)
        end = next(index for index, line in enumerate(lines, 1)
                   if line.strip() == RULE_END)
        self.assertLess(begin, end, "rule layer markers inverted")
        tree = ast.parse(text)
        hits = []
        for node in ast.walk(tree):
            if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                continue
            if not begin <= node.lineno <= end:
                continue
            for inner in ast.walk(node):
                if isinstance(inner, ast.Constant) and not isinstance(inner.value, bool) \
                        and isinstance(inner.value, (int, float)):
                    hits.append("%s:%r" % (node.name, inner.value))
        self.assertEqual(hits, [], "bare numbers in rule layer: %s" % hits)

    def test_param_key_inventory_matches_the_code(self):
        """FACTOR_PARAM_KEYS 必须与代码真实读取的键一一对应（漏一枚=烘焙时漏一项）。"""
        tree = ast.parse(self.source())
        read = set()
        for node in ast.walk(tree):
            if isinstance(node, ast.Subscript) and isinstance(node.value, ast.Name) \
                    and node.value.id == "params" and isinstance(node.slice, ast.Constant):
                read.add(node.slice.value)
            elif isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) \
                    and node.func.attr == "get" and isinstance(node.func.value, ast.Name) \
                    and node.func.value.id == "params" and node.args \
                    and isinstance(node.args[0], ast.Constant):
                read.add(node.args[0].value)
        listed = set(rf.FACTOR_PARAM_KEYS)
        self.assertEqual(read - listed, set(), "params key read but not listed")
        self.assertEqual(listed - read, set(), "listed but never read")

    def test_every_param_key_the_template_claims_is_used(self):
        """模板里躺着一枚没人读的键，等于给操作员一个假旋钮。"""
        for key in rf.FACTOR_PARAM_KEYS:
            self.assertIn(key, rf.PARAMS_TEMPLATE, "template lacks param key %s" % key)

    def test_rule_entry_points_share_the_contract_signature(self):
        """签名必须是 (params, ctx, side_hint="")，构建器按这个名字与顺序调。"""
        for name in ("rule_factor_composite", "rule_pattern_template"):
            entry = getattr(rf, name)
            names = list(entry.__code__.co_varnames[:entry.__code__.co_argcount])
            self.assertEqual(names, ["params", "ctx", "side_hint"])
            self.assertEqual(entry.__defaults__, ("",))

    def test_rules_table_is_ast_readable_and_fully_coded(self):
        """构建器用 AST 读 RULES（不 exec 交易模块）：值必须是函数名本身。

        每条 kind 还要在 signal_codes 里有短码——没有短码就没有幂等，
        这条规则宁可构建失败也不许发布。
        """
        import ast
        tree = ast.parse(self.source())
        table = None
        for node in tree.body:
            if isinstance(node, ast.Assign) and "RULES" in [
                    t.id for t in node.targets if isinstance(t, ast.Name)]:
                table = node
        self.assertIsNotNone(table, "RULES table missing (builder reads it by AST)")
        kinds = {}
        for key, value in zip(table.value.keys, table.value.values):
            self.assertIsInstance(value, ast.Name, "kind %r is not a bare function name" % key.value)
            kinds[str(key.value)] = value.id
        self.assertEqual(sorted(kinds), ["factor_composite", "pattern_template"])
        for kind, fn in kinds.items():
            self.assertTrue(callable(getattr(rf, fn, None)), "%s is not callable" % fn)
            self.assertEqual(getattr(rf, fn).__name__, fn)
            self.assertIn(kind, rf.PARAMS_TEMPLATE["signal_codes"],
                          "kind %s has no short code" % kind)
        self.assertEqual(sorted(rf.PARAMS_TEMPLATE["buy_signals"]), sorted(kinds))

    def test_decline_by_design_table_covers_every_offsandbox_input(self):
        """因子库里凡是 input != bars 的，都必须在这张"会弃权"的表里点名。"""
        for fid, spec in rf.FACTOR_LIBRARY.items():
            if spec.get("input") == rf.BARS_INPUT:
                self.assertNotIn(fid, rf.DECLINE_BY_DESIGN)
                continue
            self.assertIn(fid, rf.DECLINE_BY_DESIGN)
            self.assertTrue(rf.DECLINE_BY_DESIGN[fid], "missing input not named: %s" % fid)


# ---------------------------------------------------------------------------
# (2) 因子战法：命中 / 不命中 / 改 params 改变判定
# ---------------------------------------------------------------------------

class TestFactorComposite(unittest.TestCase):
    """Go factor.go:253 scoreRule + factor.go:210 Evaluate 的移植。"""

    def test_fires_on_high_percentile_momentum(self):
        signal = rf.rule_factor_composite(params(factor_rules=[mom_rule()]), ctx())
        self.assertIsNotNone(signal)
        self.assertEqual(signal["kind"], "factor_composite")
        self.assertEqual(signal["side"], "buy")
        self.assertEqual(signal["short_code"], "FC600000")
        self.assertEqual(signal["rule_id"], "fac_mom")
        self.assertGreaterEqual(signal["qty"], 1)

    def test_price_falls_back_to_the_newest_bar(self):
        """现价为空时用最新收盘，不自己造一个价（qty 由 amount/price 反推）。"""
        signal = rf.rule_factor_composite(params(factor_rules=[mom_rule()]),
                                          ctx(last_price=None))
        self.assertIsNotNone(signal)
        self.assertAlmostEqual(signal["price"], 130.0)   # 末端那根的收盘

    def test_refuses_when_threshold_not_reached(self):
        """复合分 100 是时序分位的上限：把阈值抬到上限之上必须不放开。"""
        full = rf.score_factor_rule(params(factor_rules=[mom_rule()]), mom_rule(), ctx())
        self.assertAlmostEqual(full["score"], 100.0)
        strict = params(factor_rules=[mom_rule(threshold=101.0)])
        self.assertIsNone(rf.rule_factor_composite(strict, ctx()))

    def test_flat_series_sits_at_the_bottom_of_the_domain(self):
        """横盘 → 分位 0 → (0+1)/2*100 = 50（Go 的域下沿），阈值 70 必不命中。"""
        scored = rf.score_factor_rule(params(factor_rules=[mom_rule()]), mom_rule(),
                                      ctx(bars=bars()))
        self.assertAlmostEqual(scored["score"], 50.0)
        self.assertFalse(scored["pass"])
        self.assertIsNone(rf.rule_factor_composite(params(factor_rules=[mom_rule()]),
                                                   ctx(bars=bars())))

    def test_threshold_change_flips_the_decision(self):
        """同一份行情只动阈值就翻转判定：阈值真的来自 params，不是写死的。"""
        scene = ctx(bars=bars())            # 横盘：复合分 50
        self.assertIsNone(rf.rule_factor_composite(params(factor_rules=[mom_rule(70.0)]),
                                                   scene))
        self.assertIsNotNone(rf.rule_factor_composite(params(factor_rules=[mom_rule(40.0)]),
                                                      scene))

    def test_direction_short_rewrites_the_contribution(self):
        """看空方向贡献 w*(1-pct)（factor.go:291）：下跌序列因此拿高分。"""
        falling = ctx(bars=bars(count=60, price_fn=lambda index: 300.0 - 0.1 * index))
        long_rule = mom_rule(direction=1)
        short_rule = mom_rule(direction=-1)
        table = params(factor_rules=[long_rule, short_rule])
        long_scored = rf.score_factor_rule(table, long_rule, falling)
        short_scored = rf.score_factor_rule(table, short_rule, falling)
        self.assertAlmostEqual(long_scored["details"]["Mom20"], 0.0)
        # 同一份下跌序列：看空改写 w*(1-pct) 把复合分从域下沿抬到域上沿
        self.assertAlmostEqual(long_scored["score"], 50.0)
        self.assertAlmostEqual(short_scored["score"], 100.0)
        self.assertFalse(long_scored["pass"])
        self.assertTrue(short_scored["pass"])

    def test_weight_normalisation_divides_by_used_weights_only(self):
        """total/used 只除以"真打上了分"的权重：改权重配比必须改变复合分。"""
        rule = {"id": "fac_w", "name": "factor_composite",
                "factors": ["Mom20", "VolSurge5"],
                "weights": {"Mom20": 3.0, "VolSurge5": 1.0},
                "directions": {"Mom20": 1, "VolSurge5": 1},
                "buy_threshold": 70.0}
        table = params(factor_rules=[rule])
        scene = ctx()
        scored = rf.score_factor_rule(table, rule, scene)
        self.assertAlmostEqual(scored["details"]["Mom20"], 1.0)
        self.assertAlmostEqual(scored["details"]["VolSurge5"], 0.0)
        # (3*1 + 1*0)/4 = 0.75 -> (0.75 + 1)/2 * 100 = 87.5
        self.assertAlmostEqual(scored["score"], 87.5)
        flipped = dict(rule)
        flipped["weights"] = {"Mom20": 1.0, "VolSurge5": 3.0}
        self.assertAlmostEqual(rf.score_factor_rule(table, flipped, scene)["score"], 62.5)

    def test_missing_weight_and_direction_use_the_baked_defaults(self):
        """Go factor.go:281/286：表和方向表里缺项按 1.0 计，而不是丢因子。"""
        rule = {"id": "fac_d", "name": "factor_composite", "factors": ["Mom20"],
                "weights": {}, "directions": {}, "buy_threshold": 70.0}
        scored = rf.score_factor_rule(params(factor_rules=[rule]), rule, ctx())
        self.assertAlmostEqual(scored["score"], 100.0)

    def test_threshold_override_table_beats_the_rule_row(self):
        """factor_thresholds 是操作员逐条调阈值的那一枚，优先级高于规则自带值。"""
        rule = mom_rule(threshold=99.0)
        table = params(factor_rules=[rule], factor_thresholds={"fac_mom": 55.0})
        scored = rf.score_factor_rule(table, rule, ctx())
        self.assertAlmostEqual(scored["threshold"], 55.0)
        self.assertTrue(scored["pass"])
        self.assertIsNotNone(rf.rule_factor_composite(table, ctx()))

    def test_non_positive_threshold_falls_back_to_the_default(self):
        """factor.go:314：阈值 <=0 不是"永不通过"，而是回退默认 70。"""
        rule = mom_rule(threshold=0.0)
        scored = rf.score_factor_rule(params(factor_rules=[rule]), rule, ctx())
        self.assertAlmostEqual(scored["threshold"], 70.0)

    def test_highest_raw_score_is_the_one_asked_to_pass(self):
        """Go factor.go:228-236 + 337：只取原始分最高的那条规则去过阈值。

        这条口径很容易被"改好"成"任一过阈值的规则都放行"，后果是同一次扫描
        放出一条本来分更低、研究侧证据更弱的规则——所以两个方向都要钉住。
        """
        strong = mom_rule(threshold=40.0, rule_id="fac_strong")        # 复合分 100
        weak = {"id": "fac_weak", "name": "factor_composite", "factors": ["VolSurge5"],
                "weights": {"VolSurge5": 1.0}, "directions": {"VolSurge5": 1},
                "buy_threshold": 40.0}                                 # 复合分 50
        scene = ctx()
        signal = rf.rule_factor_composite(params(factor_rules=[weak, strong]), scene)
        self.assertEqual(signal["rule_id"], "fac_strong")
        # 最高分那条没过阈值时，低分的合格规则照样不出手（Go 就是这个形状）
        strict = mom_rule(threshold=101.0, rule_id="fac_strong")
        self.assertIsNone(rf.rule_factor_composite(params(factor_rules=[weak, strict]),
                                                   scene))

    def test_empty_library_is_a_disabled_strategy(self):
        """Go factor.go:159：没有规则就是关闭，不许"默认放行"。"""
        self.assertIsNone(rf.rule_factor_composite(params(factor_rules=[]), ctx()))
        self.assertIsNone(rf.rule_pattern_template(params(pattern_rules=[]), ctx()))

    def test_money_caps_are_anded(self):
        """现金不够 / 日额度用完：判定成立也不出单（cap 与打分是两道独立的闸）。"""
        table = params(factor_rules=[mom_rule()])
        self.assertIsNone(rf.rule_factor_composite(table, ctx(available_cash=500.0)))
        spent = float(params()["daily_buy_cap"])
        self.assertIsNone(rf.rule_factor_composite(table, ctx(daily_buy_total=spent)))

    def test_signal_without_a_baked_code_declines(self):
        """没登记短码 = 这台机器没法幂等，宁可不发（短码是去重的唯一凭据）。"""
        table = params(factor_rules=[mom_rule()],
                       signal_codes={"factor_composite": None})
        self.assertIsNone(rf.rule_factor_composite(table, ctx()))

    def test_make_signal_rounds_down_to_whole_lots(self):
        signal = rf.make_signal(params(), ctx(), "factor_composite", "buy", 20000.0, 30.0)
        self.assertEqual(signal["qty"] % params()["lot_size"], 0)
        self.assertLessEqual(signal["qty"] * 30.0, 20000.0)

    def test_make_signal_refuses_an_amount_below_one_lot(self):
        self.assertIsNone(rf.make_signal(params(), ctx(), "factor_composite", "buy",
                                         100.0, 300.0))


# ---------------------------------------------------------------------------
# (3) 时序分位口径（Go factor.go:512）
# ---------------------------------------------------------------------------

class TestPercentileSemantics(unittest.TestCase):
    """分位是这套战法唯一的"相对强弱"来源，口径错一档整条链都错。"""

    def test_newest_high_scores_one(self):
        self.assertAlmostEqual(rf.time_series_percentile([1, 2, 3, 4, 5], 5), 1.0)

    def test_flat_series_scores_zero_not_half(self):
        """相等不算"低于"：横盘序列末值分位 0（与 Go 逐字一致）。"""
        self.assertAlmostEqual(rf.time_series_percentile([7, 7, 7, 7, 7], 5), 0.0)

    def test_newest_low_scores_zero(self):
        self.assertAlmostEqual(rf.time_series_percentile([5, 4, 3, 2, 1], 5), 0.0)

    def test_too_short_history_is_a_walk_away(self):
        self.assertIsNone(rf.time_series_percentile([1, 2, 3], 5))

    def test_nan_newest_value_is_a_walk_away(self):
        self.assertIsNone(rf.time_series_percentile([1, 2, 3, 4, float("nan")], 5))

    def test_warm_up_nan_values_do_not_enter_the_denominator(self):
        """预热期的 NaN 不进分母，否则分位会被"没数据的日子"稀释。"""
        values = [float("nan"), float("nan"), 1.0, 2.0, 3.0, 4.0]
        self.assertAlmostEqual(rf.time_series_percentile(values, 5), 1.0)

    def test_min_samples_is_a_param_not_a_literal(self):
        """样本下限来自 params：抬到 10 之后同一序列就该弃权。"""
        values = [1, 2, 3, 4, 5, 6, 7, 8]
        self.assertIsNotNone(rf.time_series_percentile(values, 5))
        self.assertIsNone(rf.time_series_percentile(values, len(values) + 1))


# ---------------------------------------------------------------------------
# (4) 形态战法：AND + 右开区间（Go pattern.go:198）
# ---------------------------------------------------------------------------

class TestPatternTemplate(unittest.TestCase):
    """形态模板的每个细节都会决定放不放行，尤其那个"右开"。"""

    @staticmethod
    def shrink_pattern():
        return {"id": "pat_pull", "name": "pattern_template",
                "conds": [{"factor": "Drawdown20", "min": 0.0, "max": 0.3},
                          {"factor": "VolShrink", "min": 0.0, "max": 0.9}]}

    def test_full_hit_fires(self):
        scene = ctx(bars=shrinking_bars(), last_price=100.0)
        result = rf.eval_pattern_rule(params(), self.shrink_pattern(), scene)
        self.assertTrue(result["pass"])
        signal = rf.rule_pattern_template(params(pattern_rules=[self.shrink_pattern()]),
                                          scene)
        self.assertIsNotNone(signal)
        self.assertEqual(signal["short_code"], "PT600000")
        self.assertEqual(signal["rule_id"], "pat_pull")

    def test_partial_hit_does_not_fire(self):
        """AND 语义：缺一枚条件就是不打扰，不是"按命中比例放行"。"""
        pattern = self.shrink_pattern()
        pattern["conds"].append({"factor": "BullAlign", "min": 0.5, "max": 2.0})
        scene = ctx(bars=shrinking_bars(), last_price=100.0)
        result = rf.eval_pattern_rule(params(), pattern, scene)
        self.assertEqual(result["met"], result["total"] - 1)
        self.assertFalse(result["pass"])
        self.assertIsNone(rf.rule_pattern_template(params(pattern_rules=[pattern]),
                                                   scene))

    def test_band_upper_bound_is_exclusive(self):
        """[min, max) 右开：横盘缩量比正好等于 1 时不得算命中（pattern_test.go 同判）。"""
        pattern = {"id": "pat_edge", "name": "pattern_template",
                   "conds": [{"factor": "VolShrink", "min": 0.0, "max": 1.0}]}
        scene = ctx(bars=bars(), last_price=100.0)
        result = rf.eval_pattern_rule(params(), pattern, scene)
        self.assertAlmostEqual(rf.factor_input(params(), scene, "VolShrink")["last"], 1.0)
        self.assertFalse(result["pass"])

    def test_widening_the_band_flips_the_decision(self):
        """只改 params 里的 band 就翻转判定——band 真的没写死在代码里。"""
        narrow = {"id": "pat_edge", "name": "pattern_template",
                  "conds": [{"factor": "VolShrink", "min": 0.0, "max": 1.0}]}
        wide = dict(narrow)
        wide["conds"] = [{"factor": "VolShrink", "min": 0.0, "max": 1.5}]
        scene = ctx(bars=bars(), last_price=100.0)
        self.assertIsNone(rf.rule_pattern_template(params(pattern_rules=[narrow]), scene))
        self.assertIsNotNone(rf.rule_pattern_template(params(pattern_rules=[wide]), scene))

    def test_first_fully_matched_pattern_wins(self):
        """Go pattern.go:181：第一条全命中的模板直接采用（归属跟着它走）。"""
        miss = {"id": "pat_miss", "name": "pattern_template",
                "conds": [{"factor": "BullAlign", "min": 0.5, "max": 2.0}]}
        hit = self.shrink_pattern()
        scene = ctx(bars=shrinking_bars(), last_price=100.0)
        signal = rf.rule_pattern_template(params(pattern_rules=[miss, hit]), scene)
        self.assertEqual(signal["rule_id"], "pat_pull")

    def test_unknown_factor_can_never_complete_a_template(self):
        """库里没有的算子：Go 计入分母却不计入命中，这里直接判该模板不可评。"""
        pattern = {"id": "pat_ghost", "name": "pattern_template",
                   "conds": [{"factor": "NoSuchFactor", "min": 0.0, "max": 9.0},
                             {"factor": "Drawdown20", "min": 0.0, "max": 0.3}]}
        scene = ctx(bars=shrinking_bars(), last_price=100.0)
        result = rf.eval_pattern_rule(params(), pattern, scene)
        self.assertTrue(result["declined"])
        self.assertIsNone(rf.rule_pattern_template(params(pattern_rules=[pattern]), scene))

    def test_empty_conds_is_a_disabled_template(self):
        """Go pattern.go:77：空条件集就是关掉这条形态。"""
        self.assertTrue(rf.eval_pattern_rule(params(), {"id": "x", "conds": []},
                                             ctx())["declined"])


# ---------------------------------------------------------------------------
# (5) 缺读数一律弃权：沙箱拿不到的输入不许伪造
# ---------------------------------------------------------------------------

class TestDeclineWhenInputMissing(unittest.TestCase):
    """这一组是本任务最要紧的锁：拿不到的读数只能沉默，不能补一个数。"""

    def test_eastmoney_flow_factor_does_not_fire_without_the_flow_readout(self):
        """东财主力净流入在 11-05 之后取不到：flow_today_pct 为空必须不出信号。"""
        rule = {"id": "fac_flow", "name": "factor_composite", "factors": ["FlowToday"],
                "weights": {"FlowToday": 1.0}, "directions": {"FlowToday": 1},
                "buy_threshold": 60.0}
        table = params(factor_rules=[rule],
                       scalar_bands={"FlowToday": {"min": 0.0, "max": 100.0}})
        scene = ctx(flow_today_pct=None)
        self.assertIsNone(rf.rule_factor_composite(table, scene))
        scored = rf.score_factor_rule(table, rule, scene)
        self.assertTrue(scored["declined"])
        self.assertIn("input_absent:FlowToday", scored["reason"])

    def test_flow_band_plus_value_is_an_explicit_proxy_not_a_default(self):
        """代理路径只在操作员烘了 band 且 ctx 真带值时才存在，二者缺一仍弃权。"""
        rule = {"id": "fac_flow", "name": "factor_composite", "factors": ["FlowToday"],
                "weights": {"FlowToday": 1.0}, "directions": {"FlowToday": 1},
                "buy_threshold": 60.0}
        with_band = params(factor_rules=[rule],
                           scalar_bands={"FlowToday": {"min": 0.0, "max": 100.0}})
        self.assertIsNotNone(rf.rule_factor_composite(
            with_band, ctx(flow_today_pct=3.5, flow_5d_pct=2.0)))
        without_band = params(factor_rules=[rule])
        self.assertIsNone(rf.rule_factor_composite(
            without_band, ctx(flow_today_pct=3.5)))

    def test_flow_outside_the_baked_band_does_not_fire(self):
        """band 是绝对区间：值在区间外按 0 分计入复合，而不是"有值就放行"。"""
        rule = {"id": "fac_flow", "name": "factor_composite", "factors": ["FlowToday"],
                "weights": {"FlowToday": 1.0}, "directions": {"FlowToday": 1},
                "buy_threshold": 70.0}
        table = params(factor_rules=[rule],
                       scalar_bands={"FlowToday": {"min": 5.0, "max": 100.0}})
        scored = rf.score_factor_rule(table, rule, ctx(flow_today_pct=1.0))
        self.assertAlmostEqual(scored["details"]["FlowToday"], 0.0)
        self.assertFalse(scored["pass"])

    def test_every_off_sandbox_factor_declines_alone(self):
        """逐个因子单挂：ctx 没这味输入时，含它的规则一条都不许放。"""
        for fid in SANDBOX_ONLY_INPUTS:
            with self.subTest(factor=fid):
                rule = {"id": "fac_%s" % fid, "name": "factor_composite",
                        "factors": [fid], "weights": {fid: 1.0},
                        "directions": {fid: 1}, "buy_threshold": 50.0}
                table = params(factor_rules=[rule])
                scene = ctx(bars=bars())   # 横盘：价量因子也给 0 分
                self.assertIsNone(rf.rule_factor_composite(table, scene),
                                  "factor %s fired without its readout" % fid)

    def test_decline_table_names_the_missing_readout(self):
        """每条弃权都要说清缺的是哪味输入，否则排障只能靠猜。"""
        for fid in SANDBOX_ONLY_INPUTS:
            self.assertIn(fid, rf.DECLINE_BY_DESIGN)
            self.assertTrue(rf.DECLINE_BY_DESIGN[fid].strip())

    def test_financial_pit_scores_on_the_go_absolute_ranges_when_supplied(self):
        """Go finaScore（factor.go:561）的区间照搬：ROE 20 封顶、负债率反向。"""
        rule = {"id": "fac_roe", "name": "factor_composite", "factors": ["ROE"],
                "weights": {"ROE": 1.0}, "directions": {"ROE": 1}, "buy_threshold": 70.0}
        table = params(factor_rules=[rule])
        scored = rf.score_factor_rule(table, rule, ctx(fin_pit={"roe": 15.0}))
        self.assertAlmostEqual(scored["details"]["ROE"], 0.75)   # 15/20
        self.assertTrue(scored["pass"])
        self.assertIsNone(rf.rule_factor_composite(table, ctx(fin_pit=None)))

    def test_debt_ratio_is_inverted(self):
        rule = {"id": "fac_debt", "name": "factor_composite", "factors": ["DebtToAssets"],
                "weights": {"DebtToAssets": 1.0}, "directions": {"DebtToAssets": 1},
                "buy_threshold": 70.0}
        table = params(factor_rules=[rule])
        scored = rf.score_factor_rule(table, rule, ctx(fin_pit={"debt_to_assets": 30.0}))
        self.assertAlmostEqual(scored["details"]["DebtToAssets"], 0.7)  # (100-30)/100

    def test_news_event_has_no_proxy_until_a_band_is_baked(self):
        """新闻/LLM 事件：沙箱里没有这条链，就算 ctx 塞了布尔也不许凭空打分。"""
        rule = {"id": "fac_news", "name": "factor_composite", "factors": ["NewsEvent"],
                "weights": {"NewsEvent": 1.0}, "directions": {"NewsEvent": 1},
                "buy_threshold": 60.0}
        self.assertIsNone(rf.rule_factor_composite(params(factor_rules=[rule]),
                                                   ctx(news_event=True)))
        opted_in = params(factor_rules=[rule], scalar_bands={"NewsEvent": {"min": 0.0,
                                                                           "max": 1.0}})
        self.assertIsNotNone(rf.rule_factor_composite(opted_in, ctx(news_event=True)))

    def test_no_bars_means_no_opinion(self):
        """Go factor.go:219：K 线不足 30 根判 nodata，这里同样只能弃权。"""
        table = params(factor_rules=[mom_rule()])
        self.assertIsNone(rf.rule_factor_composite(table, ctx(bars=[])))
        self.assertIsNone(rf.rule_factor_composite(table, ctx(bars=bars(count=29))))
        # 刚好到 min_bars 就不再是"nodata"（横盘复合分 50，故阈值压到 50 以下）
        fire = params(factor_rules=[mom_rule(40.0)])
        self.assertIsNone(rf.rule_factor_composite(fire, ctx(bars=bars(count=29))))
        self.assertIsNotNone(rf.rule_factor_composite(
            fire, ctx(bars=bars(count=int(params()["min_bars"])))))

    def test_pattern_leg_also_declines_on_a_short_series(self):
        pattern = TestPatternTemplate.shrink_pattern()
        table = params(pattern_rules=[pattern])
        self.assertIsNone(rf.rule_pattern_template(table, ctx(bars=bars(count=10))))

    def test_closed_day_and_sell_side_both_decline(self):
        table = params(factor_rules=[mom_rule()])
        self.assertIsNone(rf.rule_factor_composite(table, ctx(trading_day=False)))
        self.assertIsNone(rf.rule_factor_composite(table, ctx(), "sell"))
        self.assertIsNotNone(rf.rule_factor_composite(table, ctx(), "buy"))

    def test_require_trading_day_can_be_switched_off(self):
        """交易日前置检查是 params 开关（与本地执行器规则层同一口径），不是硬编码。"""
        table = params(factor_rules=[mom_rule()], require_trading_day=False)
        self.assertIsNotNone(rf.rule_factor_composite(table, ctx(trading_day=False)))

    def test_unusable_history_leaves_the_rule_without_a_denominator(self):
        """所有成分都算不出来（used<=0）时：Go 判"无可用因子"，这里不放行。"""
        # Brk60 在 40 根序列上整列预热期 NaN：成分全部打不上分 -> Go 判"无可用因子"
        rule = {"id": "fac_brk", "name": "factor_composite", "factors": ["Brk60"],
                "weights": {"Brk60": 1.0}, "directions": {"Brk60": 1},
                "buy_threshold": 10.0}
        table = params(factor_rules=[rule])
        scored = rf.score_factor_rule(table, rule, ctx(bars=bars(count=40)))
        self.assertFalse(scored["declined"])
        self.assertEqual(scored["reason"], "no_usable_factor")
        self.assertFalse(scored["pass"])
        self.assertIsNone(rf.rule_factor_composite(table, ctx(bars=bars(count=40))))

    def test_missing_params_key_raises_instead_of_defaulting(self):
        """阈值键丢了要炸，不能悄悄用旧数字（"改了旋钮没重烘焙"的成因形状）。"""
        bare = dict(mom_rule())
        bare["buy_threshold"] = None
        broken = params(factor_rules=[bare])
        broken.pop("default_buy_threshold")
        with self.assertRaises(KeyError):
            rf.rule_factor_composite(broken, ctx())


# ---------------------------------------------------------------------------
# (6) 因子库本身：每个价量因子都得真算出来
# ---------------------------------------------------------------------------

class TestFactorLibrary(unittest.TestCase):
    """处理器接错/窗口写错在这组用例里会当场现形，而不是变成一条静默的 0 分。"""

    def scene(self):
        """140 根上下波动 + 量能轮动的序列：所有价量因子的窗口都预热完了。"""
        series = bars(count=140, price_fn=lambda index: 100.0 + 0.05 * index
                      + (0.8 if index % 3 == 0 else -0.6))
        for index, bar in enumerate(series):
            bar["volume"] = 1200.0 + 30.0 * (index % 5)
            bar["amount"] = bar["volume"] * bar["close"]
        return ctx(bars=series)

    def test_every_bar_factor_returns_an_aligned_series(self):
        scene = self.scene()
        table = params()
        size = len(scene["bars"])
        for fid, spec in rf.FACTOR_LIBRARY.items():
            if spec.get("input") != rf.BARS_INPUT:
                continue
            with self.subTest(factor=fid):
                entry = rf.factor_input(table, scene, fid)
                self.assertIsNotNone(entry, "%s reported absent input" % fid)
                self.assertEqual(len(entry["values"]), size)
                self.assertFalse(math.isnan(entry["last"]), "%s tail is NaN" % fid)

    def test_indicators_match_the_go_warm_up_rule(self):
        """SMA/EMA/RollingStd 预热期为 NaN（Go 库的口径），不做前向填充。"""
        values = [1.0, 2.0, 3.0, 4.0]
        self.assertTrue(all(math.isnan(item) for item in rf.sma(values, 3)[:2]))
        self.assertAlmostEqual(rf.sma(values, 3)[2], 2.0)
        self.assertTrue(math.isnan(rf.rolling_std(values, 3)[1]))
        self.assertTrue(math.isnan(rf.ema(values, 3)[1]))
        self.assertAlmostEqual(rf.ema(values, 3)[2], 2.0)

    def test_percentile_style_helpers(self):
        # 窗口 [1,2,3] 里当前值 3：两枚严格更小、一枚相等 -> (2 + 0.5)/3
        self.assertAlmostEqual(rf.ts_rank([1, 2, 3, 4], 3)[2], 2.5 / 3.0)
        self.assertAlmostEqual(rf.ts_argmax_offset([1, 5, 3, 4], 3)[2], 0.5)
        self.assertAlmostEqual(rf.delta_series([1, 3, 6], 2)[2], 5.0)
        self.assertEqual(rf.sign_of(-2.0), -1.0)

    def test_clamp01_is_the_go_domain(self):
        self.assertEqual(rf.clamp01(-1.0), 0.0)
        self.assertEqual(rf.clamp01(2.0), 1.0)
        self.assertAlmostEqual(rf.clamp01(0.5), 0.5)
        self.assertTrue(math.isnan(rf.clamp01(float("nan"))))

    def test_score_domain_comes_from_params(self):
        """域是烘进来的：把 full 抬到 200，同一份分位的复合分跟着翻倍。"""
        wide = params(score_domain={"floor": 0.0, "shift": 1.0, "divisor": 2.0,
                                    "full": 200.0}, factor_rules=[mom_rule(150.0)])
        scored = rf.score_factor_rule(wide, mom_rule(150.0), ctx())
        self.assertAlmostEqual(scored["score"], 200.0)
        self.assertTrue(scored["pass"])
        narrow = params(score_domain={"floor": 0.0, "shift": 1.0, "divisor": 2.0,
                                      "full": 100.0}, factor_rules=[mom_rule(150.0)])
        self.assertFalse(rf.score_factor_rule(narrow, mom_rule(150.0), ctx())["pass"])

    def test_template_tables_are_all_scoreable_and_never_raise(self):
        """出厂模板（fac_1 / pat_1）在两份行情上各跑一遍：只允许出信号或 None。"""
        for scene in (self.scene(), ctx(bars=bars())):
            for signal in (rf.rule_factor_composite(params(), scene),
                           rf.rule_pattern_template(params(), scene)):
                self.assertTrue(signal is None or isinstance(signal, dict))

    def test_signal_carries_score_and_rule_attribution(self):
        """归属与分数随信号走：效果监测（Go SignalCount/Win/Loss）靠这两个字段。"""
        table = params(factor_rules=[mom_rule(40.0)])
        signal = rf.rule_factor_composite(table, ctx(bars=bars()))
        self.assertEqual(signal["rule_id"], "fac_mom")
        self.assertAlmostEqual(signal["score"], 50.0)
        self.assertIn("factor_composite", signal["reason"])


if __name__ == "__main__":
    unittest.main()
