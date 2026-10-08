#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""cmd/pydata/server.py 单测（unittest，零第三方依赖，缺 pandas/baostock 时整文件跳过）。

§P2-F（2026-10-06 修复批 波 5）降级行口径锁。

缺陷原文：新浪/东财兜底腿把"上游没有这一列"写成空串（pctChg/peTTM/isST…），
并把 tradestatus 硬编码成 "1"（=今天正常交易）。Go 侧 `TushareRow.F()` 把空串静默
折成 0，而 0 在行情语义里是一个真实读数（涨跌幅 0＝平盘、tradestatus 0＝停牌）
⇒ 降级链在库里把"断源日"写成"平盘日"，广度统计（上涨家数占比、板块平均涨跌幅）随之失真，
事后从数据本身完全看不出这一行是兜底来的。

本文件锁的是**线格式**（sidecar → Go 的那份 CSV），落库与统计侧的锁在 Go 测试里：
  K1w 降级腿每一行的长度 == 表头长度（列错位是这一族最容易复发且最静默的形态）；
  K1b 新浪行的 tradestatus/pctChg/估值/ST 全部是显式缺测标记 "NA"，且行尾 source=sina_degraded；
  K1c 东财行的 tradestatus 是 "NA"、pctChg 是真实读数、source=eastmoney_degraded；
  K1d 主链路（baostock）也带 source 列且值为 baostock——只有主链路也标，读侧才能用一句
      `source LIKE '%degraded%'` 精确圈出降级行；
  K1e 指数链路列集不变（多一列会被写入面校验判红）；
  K1f r_kline 降级响应的表头与数据行同为含 source 的同一列集；
  K4  降级腿**代码**内不得再出现裸字面量 "1"（旧硬编码 tradestatus 的形状，出现即红），
      并且行尾来源标记换算到值必须含 degraded（K4b：写成 _SRC_BAOSTOCK ＝冒充主链路）。
      判据只扫代码，docstring 与注释里的说明文字必须先剥掉——这两个函数的说明里正写着
      「旧实现硬编码成 "1"」，不剥就是恒红（本仓 09-28 那条静态负向锁误伤说明注释的同族）。

English: wire-format locks for the §P2-F degraded-source marker and per-row source tag.
"""
import csv
import io
import os
import re
import sys
import unittest

# 依赖探测：本文件测的是 sidecar 的行构造，需要 pandas 造 DataFrame（baostock 只在
# import 期用到）。缺依赖时整文件 skip，绝不让门禁因为"这台机器没装 akshare 依赖"而判红。
try:
    import pandas as pd  # noqa: F401
    _PANDAS = True
except Exception:  # pragma: no cover - 取决于本机环境
    _PANDAS = False

# 路径按「本文件在 repo/cmd/pydata/tests/ 下」实算：_SIDECAR_DIR 就是被测 sidecar 所在目录，
# K4 读源码直接用它拼，不再从仓库根绕一层（多剥一层 dirname 会让 import 与读源码双双落空，
# 而 import 落空在这里的表现是整文件 skip —— 静默失效，比判红更糟）。
_SIDECAR_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_REPO = os.path.dirname(os.path.dirname(_SIDECAR_DIR))
sys.path.insert(0, _SIDECAR_DIR)

try:
    import server as srv  # noqa: E402
    _SERVER = True
except Exception as e:  # pragma: no cover - 本机没装 baostock 时走这里
    srv = None
    _SERVER = False
    _SERVER_ERR = e

# K4 的射程从源码派生，不写死函数名单：凡是形如 `_ak_xxx_daily` 的兜底日线腿都自动进锁。
_DEGRADED_DAILY_DEF = re.compile(r"^def (_ak_[A-Za-z0-9_]*_daily)\(")


class _FakeAk:
    """假 akshare 模块：只暴露两条降级腿用到的取数函数，返回夹具 DataFrame 并记录拨了谁。

    桩必须替换 `_try_ak` 的**入参形态**（真实现是 `fn(ak, *args)`），不能顺手改签名；
    记录 calls 是为了让断言能落在"这一腿真的拨了它该拨的接口"上，而不是只看到有返回值。
    """

    def __init__(self, df):
        self._df = df
        self.calls = []

    def stock_zh_a_daily(self, **kw):
        self.calls.append(("stock_zh_a_daily", kw))
        return self._df

    def stock_zh_a_hist(self, **kw):
        self.calls.append(("stock_zh_a_hist", kw))
        return self._df


def _stub_try_ak(srv_module, df):
    """把 `_try_ak` 换成"直接喂假 ak 模块"，返回 (桩函数, 假模块) 供断言拨号记录。"""
    fake = _FakeAk(df)

    def _stub(fn, *args):
        return fn(fake, *args)

    return _stub, fake


def _rows_from_csv(text):
    """把 sidecar 返回的 CSV 文本拆成 (表头, 数据行)，供逐格断言。"""
    rdr = csv.reader(io.StringIO(text))
    records = [r for r in rdr if r]
    return records[0], records[1:]


def _src_constant_values(src):
    """派生 `_SRC_*` 常量名 → 字面值（K4b 用它把「行尾用的标记名」换算成「它到底是不是降级名」。

    只看名字是判不出坏法的：把降级腿行尾写成 `_SRC_BAOSTOCK` 时，`_SRC_` 出现过、
    列数也对、Go 侧照样落库，唯一的痕迹是这一行的值是 baostock——于是读侧那句
    `source LIKE '%degraded%'` 恒不命中，降级数据被当主链路用（这正是本文件要防的那族缺陷
    的第三种形态）。所以这条锁必须**换算到值**，而不是数常量的名字在不在。
    """
    out = {}
    for m in re.finditer(r'^(_SRC_[A-Z0-9_]+)\s*=\s*"([^"]*)"', src, re.M):
        out[m.group(1)] = m.group(2)
    return out


def _degraded_daily_names(src):
    """从 server.py 源码里派生「兜底日线腿」函数名集合（K4 射程，禁写死清单）。"""
    names = []
    for line in src.splitlines():
        m = _DEGRADED_DAILY_DEF.match(line)
        if m:
            names.append(m.group(1))
    return names


def _function_bodies(src, names):
    """从源码里按函数名切出函数体（到下一个顶格 def/class/注释块为止的简化实现）。"""
    wanted = set(names)
    out = {}
    lines = src.splitlines()
    for i, line in enumerate(lines):
        stripped = line.strip()
        for name in wanted:
            if stripped.startswith("def %s(" % name):
                j = i + 1
                while j < len(lines) and (lines[j].startswith((" ", "\t")) or lines[j].strip() == ""):
                    j += 1
                out[name] = "\n".join(lines[i:j])
    return out


def _strip_inline_comment(line):
    """剥掉行尾注释（# 在引号内时不算注释起点）。不处理转义引号：现网这两个函数没有。"""
    quote = None
    for idx, ch in enumerate(line):
        if quote:
            if ch == quote:
                quote = None
        elif ch in ("'", '"'):
            quote = ch
        elif ch == "#":
            return line[:idx]
    return line


def _docstring_span(lines):
    """定位函数体开头的 docstring，返回 (起始行下标, 结束行下标·不含)；没有则 (None, None)。

    单实现两处消费（剥代码用、自证"确实有说明被剥掉了"用），两份实现迟早只修一份。
    """
    i = 1  # 0 是 def 行
    while i < len(lines) and (lines[i].strip() == "" or lines[i].lstrip().startswith("#")):
        i += 1
    if i >= len(lines):
        return None, None
    head = lines[i].strip()
    quote = None
    if head.startswith('"""'):
        quote = '"""'
    elif head.startswith("'''"):
        quote = "'''"
    if quote is None:
        return None, None
    start = i
    if quote in head[3:]:  # 单行式：同行闭合
        return start, i + 1
    i += 1
    while i < len(lines) and quote not in lines[i]:
        i += 1
    return start, i + 1


def _code_only(body):
    """函数体只留代码：去掉 docstring 整块、整行注释与行尾注释。

    负向锁必须扫这个返回值，而不是原始 body——否则说明文字里的反例串会让锁恒红。
    """
    lines = body.splitlines()
    out = []
    if lines and lines[0].lstrip().startswith("def "):
        out.append(lines[0])
    start, end = _docstring_span(lines)
    for idx, line in enumerate(lines):
        if idx == 0 or (start is not None and start <= idx < end):
            continue
        if line.strip() == "" or line.lstrip().startswith("#"):
            continue
        out.append(_strip_inline_comment(line))
    return "\n".join(out)


_SKIP_REASON = "本机缺 baostock/pandas 依赖，sidecar 线格式用例跳过"
if not _SERVER:
    # import 失败的原因必须打进 skip 文案：只写「缺依赖」会把"语法错/路径算错"也盖成环境问题
    _SKIP_REASON = "sidecar 无法 import（%r），线格式用例跳过" % (_SERVER_ERR,)


@unittest.skipUnless(_SERVER and _PANDAS, _SKIP_REASON)
class TestDegradedSourceWireFormat(unittest.TestCase):
    """降级腿与主链路的 CSV 列集/缺测标记/来源标记。"""

    def setUp(self):
        """备份被替换的模块级函数，用例结束原样恢复（不许污染同进程其它用例）。"""
        self._orig_try_ak = srv._try_ak
        self._orig_bs_query = srv._bs_query

    def tearDown(self):
        """恢复 monkeypatch。"""
        srv._try_ak = self._orig_try_ak
        srv._bs_query = self._orig_bs_query

    def _header_len(self):
        """主链路与降级链路共用的输出列数（_STOCK_FIELDS + source）。"""
        return len(srv._fields_with_source())

    def test_K1b_sina_leg_marks_every_unavailable_column(self):
        """K1b：新浪行——拿不出的列全是 "NA"，行尾带 sina_degraded。"""
        df = pd.DataFrame([
            {"date": "2026-10-06", "open": 10.0, "high": 11.0, "low": 9.5, "close": 10.5,
             "volume": 1000.0, "amount": 10500.0, "turnover": 0.0123},
            # 第二行用于验证 preclose 用前一日 close 填充的既有语义没被改动
            {"date": "2026-10-07", "open": 10.5, "high": 12.0, "low": 10.0, "close": 11.0,
             "volume": 2000.0, "amount": 22000.0, "turnover": 0.02},
        ])
        srv._try_ak, fake = _stub_try_ak(srv, df)

        rows = srv._ak_sina_daily("sh.600000", "20261006", "20261007")
        self.assertEqual(len(rows), 2)
        self.assertEqual([c[0] for c in fake.calls], ["stock_zh_a_daily"],
                         "新浪腿必须拨 stock_zh_a_daily（拨错接口＝夹具没在测这条腿）")
        self.assertEqual(fake.calls[0][1].get("adjust"), "", "新浪降级腿取未复权价（既有口径，改动需连 Go 侧复权链一起看）")
        width = self._header_len()
        for i, r in enumerate(rows):
            self.assertEqual(len(r), width,
                             "§P2-F 列错位：新浪第 %d 行有 %d 格，表头 %d 列" % (i, len(r), width))
        first = dict(zip(srv._fields_with_source(), rows[0]))
        # tradestatus：旧实现硬编码成 "1"（伪造"正常交易"）⇒ 必须是缺测标记
        self.assertEqual(first["tradestatus"], srv._NA,
                         "新浪拿不出停牌态，必须写 _NA 而不是伪造缺省值")
        self.assertEqual(first["pctChg"], srv._NA, "新浪无涨跌幅列 ⇒ 必须缺测标记（旧实现是空串→0＝平盘）")
        for col in ("peTTM", "pbMRQ", "psTTM", "pcfNcfTTM", "isST"):
            self.assertEqual(first[col], srv._NA, "%s 新浪拿不出，必须是 _NA" % col)
        self.assertEqual(first[srv._SOURCE_COL], srv._SRC_SINA)
        # 换手率仍是换算后的百分比口径（改动不得顺手改坏既有归一）
        self.assertAlmostEqual(first["turn"], 1.23, places=6)
        # 第二行 preclose = 前一日 close（旧语义保留，防止有人把缺测标记误扩散到 preclose）
        second = dict(zip(srv._fields_with_source(), rows[1]))
        self.assertAlmostEqual(second["preclose"], 10.5, places=6)

    def test_K1c_eastmoney_leg_keeps_real_pct_and_marks_status(self):
        """K1c：东财行——涨跌幅是真实读数，停牌态/估值/ST 是 _NA，行尾 eastmoney_degraded。"""
        df = pd.DataFrame([
            {"日期": "2026-10-06", "开盘": 10.0, "最高": 11.0, "最低": 9.5, "收盘": 10.5,
             "成交量": 10.0, "成交额": 10500.0, "换手率": 1.2, "涨跌幅": 5.0},
        ])
        srv._try_ak, fake = _stub_try_ak(srv, df)

        rows = srv._ak_em_daily("sh.600000", "2026-10-06", "2026-10-06")
        self.assertEqual(len(rows), 1)
        self.assertEqual([c[0] for c in fake.calls], ["stock_zh_a_hist"],
                         "东财腿必须拨 stock_zh_a_hist（拨错接口＝夹具没在测这条腿）")
        self.assertEqual(fake.calls[0][1].get("adjust"), "", "东财降级腿取未复权价（既有口径）")
        self.assertEqual(fake.calls[0][1].get("start_date"), "20261006", "东财入参日期口径（去横线）不得被改动")
        self.assertEqual(len(rows[0]), self._header_len(), "§P2-F 列错位：东财行列数与表头不符")
        got = dict(zip(srv._fields_with_source(), rows[0]))
        self.assertEqual(got["tradestatus"], srv._NA, "东财 hist 无停牌态列 ⇒ 不得伪造正常交易")
        self.assertAlmostEqual(got["pctChg"], 5.0, places=6, msg="涨跌幅是东财真实读数，不得一并抹成 NA")
        for col in ("peTTM", "pbMRQ", "psTTM", "pcfNcfTTM", "isST"):
            self.assertEqual(got[col], srv._NA, "%s 必须是 _NA（旧实现是空串→0）" % col)
        self.assertEqual(got[srv._SOURCE_COL], srv._SRC_EASTMONEY)
        # preclose 由 close/(1+pct/100) 反推：5% 涨幅、收 10.5 ⇒ 前收 10.0
        self.assertAlmostEqual(got["preclose"], 10.0, places=6)

    def test_K1d_primary_chain_also_tags_source(self):
        """K1d：主链路（baostock）行尾也带 source=baostock，两条链路同一把尺子。"""
        width = len(srv._STOCK_FIELDS.split(","))
        bs_rows = [["20261006", "sh.600000", "10", "11", "9.5", "10.5", "10", "1000", "10500",
                    "3", "1.2", "1", "5.0", "12.3", "1.1", "2.2", "3.3", "0"]]
        self.assertEqual(len(bs_rows[0]), width, "夹具必须与 baostock 请求列数等长（否则本用例失真）")
        srv._bs_query = lambda fn, *a, **k: (list(bs_rows), srv._STOCK_FIELDS)
        text = srv._kline_impl({"code": "sh.600000", "start": "2026-10-06", "end": "2026-10-06"})
        header, data = _rows_from_csv(text)
        self.assertEqual(header, srv._fields_with_source(), "主链路表头必须以 source 收尾")
        self.assertEqual(len(data), 1)
        self.assertEqual(len(data[0]), len(header), "§P2-F 列错位：主链路行数与表头不符")
        self.assertEqual(data[0][-1], srv._SRC_BAOSTOCK)

    def test_K1e_index_chain_untouched(self):
        """K1e：指数链路不参与降级链 ⇒ 列集必须保持原样（凭空多一列会被写入面校验判红）。"""
        srv._bs_query = lambda fn, *a, **k: ([["20261006", "sh.000300"] + ["1"] * 11], srv._INDEX_FIELDS)
        text = srv._kline_impl({"code": "sh.000300", "start": "2026-10-06", "end": "2026-10-06"}, index=True)
        header, data = _rows_from_csv(text)
        self.assertEqual(header, srv._INDEX_FIELDS.split(","), "指数表头不得被加上 source 列")
        self.assertEqual(len(data[0]), len(header))

    def test_K1f_fallback_response_header_matches_rows(self):
        """K1f：r_kline 走降级时，表头与行必须同为"含 source"的同一列集（Go 侧按表头建键，
        少一列就整行错位——这一条把 sidecar 与 Go 客户端之间唯一的静默通道钉住）。"""
        df = pd.DataFrame([
            {"date": "2026-10-06", "open": 10.0, "high": 11.0, "low": 9.5, "close": 10.5,
             "volume": 1000.0, "amount": 10500.0, "turnover": 0.01},
        ])
        srv._try_ak, fake = _stub_try_ak(srv, df)

        def boom(*a, **k):
            raise RuntimeError("sidecar 夹具：baostock 主链路不可用")

        srv._bs_query = boom
        text = srv.r_kline({"code": "sh.600000", "start": "2026-10-06", "end": "2026-10-06"})
        header, data = _rows_from_csv(text)
        self.assertEqual([c[0] for c in fake.calls], ["stock_zh_a_daily"],
                         "降级链必须先试新浪（新浪可用却拨了东财＝链序被改，来源标记也随之撒谎）")
        self.assertEqual(header, srv._fields_with_source())
        self.assertEqual(len(data), 1)
        self.assertEqual(len(data[0]), len(header), "降级响应必须整行含 source，不得截断")
        self.assertEqual(data[0][-1], srv._SRC_SINA)

    def test_K5_missing_marker_constants_are_shared(self):
        """K5：缺测标记与来源列名必须是单源常量（写死的字符串各腿各一份＝改一处漏一处）。"""
        self.assertEqual(srv._NA, "NA")
        self.assertEqual(srv._SOURCE_COL, "source")
        self.assertEqual(srv._fields_with_source(), (srv._STOCK_FIELDS + "," + srv._SOURCE_COL).split(","))
        # 三个来源值互不相同：读侧靠一句 LIKE '%degraded%' 圈降级行，主源值不许带 degraded
        tags = (srv._SRC_BAOSTOCK, srv._SRC_SINA, srv._SRC_EASTMONEY)
        self.assertEqual(len(set(tags)), 3, "来源标记重复")
        self.assertNotIn("degraded", srv._SRC_BAOSTOCK, "主源标记含 degraded 会让读侧判据把主链路一起排除")
        for tag in tags[1:]:
            self.assertIn("degraded", tag, "%s 必须带 degraded 后缀，否则 LIKE 判据漏网" % tag)


class TestDegradedSourceStaticLocks(unittest.TestCase):
    """K4：源码级负锁——不依赖 pandas/baostock，任何环境都必须跑（放进上面的 skip 类里＝恒绿假锁）。"""

    def test_K4_no_hardcoded_tradestatus_literal_in_degraded_legs(self):
        """K4（源码级负锁）：兜底日线腿的**代码**里不得再出现裸字面量 "1"。

        三件事分别锁住：
          ① 射程派生：函数名从源码正则派生（新增第三条腿自动进锁），并钉「派生结果 <2 即红」
             的正锁——写死清单会让下一个新增的兜底腿天生落在锁外（§BOM-REPO-DERIVE 同族教训）；
          ② 只扫代码：docstring/注释必须先剥掉，并且把"剥离腿在承重"写成三条各自可红的断言——
             定位得到 docstring（说明被删＝作用对象没了）、剥完不含三引号（剥没剥干净）、
             docstring 里确实有反例串（反例串没了说明这条负锁已空转，要显式复核而不是留着装绿）。
             不剥就是恒红，而恒红的锁等于没有锁（本仓 09-28 静态负向锁误伤说明注释的同族）。
          ③ 正向形态：每腿代码里必须同时出现 _NA（缺测标记）与 _SRC_（来源标记），并且
             每个用到的 _SRC_* 常量换算到值必须含 degraded（K4b，否则「降级腿写主链路标记」
             这种冒充形态在线格式层是隐身的）。
        """
        src = open(os.path.join(_SIDECAR_DIR, "server.py"), encoding="utf-8").read()
        names = _degraded_daily_names(src)
        self.assertGreaterEqual(len(names), 2,
                                "派生到的兜底日线腿少于 2 个（改名/正则失效 ⇒ 本锁退化成空循环，必须红）：%s" % (names,))
        consts = _src_constant_values(src)
        self.assertGreaterEqual(len(consts), 3,
                                "派生到的 _SRC_* 来源常量少于 3 个（主链路 + 至少两条降级腿；读不到＝定义写法变了，"
                                "下面那条「标记值必须是降级名」会跟着空转）：%s" % (sorted(consts),))
        bodies = _function_bodies(src, names)
        self.assertEqual(sorted(bodies), sorted(set(names)), "有派生到的函数没切出函数体")
        for name in sorted(bodies):
            raw = bodies[name]
            code = _code_only(raw)
            # ② 自证剥离腿真在承重（三条各自可红，不用"看起来绿就是剥干净了"）：
            lines = raw.splitlines()
            dstart, dend = _docstring_span(lines)
            self.assertIsNotNone(dstart,
                                 "%s：函数体开头没定位到 docstring ⇒ 说明文字进射程（本锁会恒红）。"
                                 "说明被删/格式变了都要显式复核，别静默留着这枚锁" % name)
            self.assertNotIn('"""', code,
                             "%s：剥完还留着三引号 ⇒ docstring 没剥掉，负锁误伤自己的说明文字" % name)
            doc_text = "\n".join(lines[dstart:dend])
            self.assertIn('"1"', doc_text,
                          "%s：说明里不再出现反例串（旧硬编码写法），剥离腿不再承重 ⇒ "
                          "请复核本锁是否仍需扫代码/docstring 两式，不要把它降级成恒绿的空判据" % name)
            self.assertNotIn('"1"', code,
                             "§P2-F K4a 反例：%s 的代码里又出现裸字面量 tradestatus 伪造形态" % name)
            self.assertIn("_NA", code, "%s 必须用 _NA 标记缺测列" % name)
            self.assertIn("_SRC_", code, "%s 行尾必须带来源标记（_SRC_SINA/_SRC_EASTMONEY）" % name)
            # K4b：来源标记**换算到值**必须是降级名。只数 `_SRC_` 出现过＝假锁：把降级腿行尾
            # 改成 _SRC_BAOSTOCK 时线格式照旧、Go 照落库，而读侧那句 LIKE 恒不命中——
            # 降级数据冒充主链路，正是本文件开头那条缺陷的第三种形态。
            for mk in sorted(set(re.findall(r"\b_SRC_[A-Za-z0-9_]+\b", code))):
                val = consts.get(mk)
                self.assertIsNotNone(
                    val, "§P2-F K4b：%s 用了没有定义的来源常量 %s（定义与使用分家＝判据恒假）" % (name, mk))
                self.assertIn("degraded", (val or "").lower(),
                              "§P2-F K4b 反例：%s 行尾的来源标记 %s=%r 不是降级名 ⇒ sidecar 这条兜底腿"
                              "会被读侧那句 LIKE 当主链路统计" % (name, mk, val))


if __name__ == "__main__":
    unittest.main()
