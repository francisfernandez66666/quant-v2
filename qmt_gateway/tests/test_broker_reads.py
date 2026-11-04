#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§B1 柜台读数通道（导出解析腿）单测：真写夹具、真解析、无 mock 复刻。

覆盖 IMPL_QMT_INTERNAL_LOOP_20261103 §4.1 的四条硬边界与 §4.3 的四枚用名：
  ① 白名单之外的目录一律不发现（防"读遍盘"式越权）；
  ② missing ≠ empty（把"没取到"当"没有"是本仓最贵的错误，§MANUAL-SYNC 同取向）；
  ③ 半文件（表头缺失/行数骤降）判解析失败并**保留上一份读数** + 计数；
  ④ utf-8-sig / utf-8 / gbk 三种导出编码都解析得动（券商客户端与 Excel 混存的现实）。
另加两枚本模块特有的：只读（对导出目录零写零删，行为 + 静态双锁）、二进制格式明确抛错不静默。
全部离线：只在本用例的 tempfile 临时目录里读写，不碰网络、不碰仓库工作树。
"""
import hashlib
import os
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import broker_reads as br  # noqa: E402

POS_HEADER = "证券代码,证券名称,持仓数量,可用数量"
POS_ROWS = [("600588", "用友网络", "600", "600"),
            ("001258", "立新能源", "700", "700"),
            ("002415", "海康威视", "500", "500")]
ORD_HEADER = "证券代码,合同编号,委托状态,委托数量,价格"
FILL_HEADER = "证券代码,成交编号,成交价,成交数量"
ASSET_HEADER = "总资产,可用资金,证券市值"


def write_bytes(path, data):
    with open(path, "wb") as fh:
        fh.write(data)


def positions_text(rows=None):
    lines = [POS_HEADER]
    for r in (POS_ROWS if rows is None else rows):
        lines.append(",".join(r))
    return "\n".join(lines) + "\n"


class TmpCase(unittest.TestCase):
    """每个用例一套独立临时目录：用例之间不留任何共享状态。"""

    def setUp(self):
        self.root = tempfile.mkdtemp(prefix="broker-exports-")
        self.export_dir = os.path.join(self.root, "exports")
        os.makedirs(self.export_dir)
        # 同名前缀的兄弟目录：白名单判定必须是"带分隔符边界的前缀"，否则它会混进来
        self.sibling_dir = os.path.join(self.root, "exports_evil")
        os.makedirs(self.sibling_dir)
        self.addCleanup(shutil.rmtree, self.root, True)
        self.whitelist = [self.export_dir]

    def put(self, name, text, encoding="utf-8"):
        path = os.path.join(self.export_dir, name)
        write_bytes(path, text.encode(encoding))
        return path


class DiscoveryTest(TmpCase):
    """边界之外的文件不该被知道存在（§4.3 test_export_discovery_whitelist_only）。"""

    def test_export_discovery_whitelist_only(self):
        good = self.put("持仓明细.csv", positions_text())
        self.put("新建文本文档.txt", positions_text())          # 形态不匹配
        self.put("~$持仓明细.csv", positions_text())            # Excel 锁文件
        self.put("持仓明细.csv.tmp", positions_text())          # 半份分片
        os.makedirs(os.path.join(self.export_dir, "持仓表.txt"))  # 目录冒充文件
        # 兄弟目录里放一份完全合法的文件名——它唯一的错是"不在白名单里"
        write_bytes(os.path.join(self.sibling_dir, "当日委托.csv"),
                    positions_text().encode("utf-8"))
        # 白名单外另建一个目录，同样不许被发现
        outside = os.path.join(self.root, "someone_elsa")
        os.makedirs(outside)
        write_bytes(os.path.join(outside, "持仓.csv"), positions_text().encode("utf-8"))

        hits = br.discover_exports(os.path.join(self.export_dir, "*.csv"),
                                   allowed_roots=self.whitelist)
        self.assertEqual([os.path.realpath(h.path) for h in hits], [os.path.realpath(good)])
        self.assertEqual(hits[0].kind, "positions")

        # 一条通配把所有目录都罩住时，白名单才是唯一的闸
        wide = br.discover_exports(os.path.join(self.root, "*", "*.csv"),
                                   allowed_roots=self.whitelist)
        self.assertEqual([os.path.realpath(h.path) for h in wide], [os.path.realpath(good)])
        # 只把兄弟目录登记进白名单，就不该看见白名单那份（正反两向都验）
        only_evil = br.discover_exports(os.path.join(self.root, "*", "*.csv"),
                                        allowed_roots=[self.sibling_dir])
        self.assertEqual({os.path.realpath(os.path.dirname(h.path)) for h in only_evil},
                         {os.path.realpath(self.sibling_dir)})
        # 空白名单＝什么都不许读（fail-closed，缺省必须是这个形态）
        self.assertEqual(br.discover_exports(os.path.join(self.export_dir, "*")), [])
        self.assertFalse(br.in_whitelist(good, allowed_roots=()))

    def test_export_kind_classification(self):
        for name, kind in (("当日委托.csv", "orders"), ("成交记录.txt", "fills"),
                           ("账户资产.csv", "asset"), ("positions_20261105.csv", "positions"),
                           ("随便什么.csv", "")):
            self.assertEqual(br.classify_name(name), kind, name)


class MissingNotEmptyTest(TmpCase):
    """§4.3 test_export_missing_not_treated_as_empty：两种读数各自独立，都不推进账本。"""

    def test_export_missing_not_treated_as_empty(self):
        missing = os.path.join(self.export_dir, "持仓.csv")   # 从不创建
        empty = self.put("持仓空.csv", "")                     # 存在但零字节

        r_missing = br.parse_positions(missing)
        r_empty = br.parse_positions(empty)

        self.assertEqual(r_missing.state, br.STATE_MISSING)
        self.assertEqual(r_empty.state, br.STATE_EMPTY)
        self.assertNotEqual(r_missing.state, r_empty.state)
        self.assertEqual(r_missing.reason, "file_not_found")
        self.assertEqual(r_empty.reason, "blank_file")
        # 两者都不许被读成"上一次可信读数作废"
        ledger = br.ReadLedger("positions")
        ledger.refresh(self.put("持仓明细.csv", positions_text()))
        before_rows = ledger.current_rows()
        before_ok = ledger.last_ok_at
        ledger.refresh(missing)
        ledger.refresh(empty)
        self.assertEqual(ledger.current_rows(), before_rows)
        self.assertEqual(ledger.last_ok_at, before_ok)
        self.assertEqual(ledger.missing_total, 1)
        self.assertEqual(ledger.empty_total, 1)
        self.assertEqual(ledger.parse_fail_total, 0)  # 空文件不是解析失败

    def test_header_present_but_zero_data_rows_is_empty_not_missing(self):
        # 表头在、零行＝柜台说"今天没有"，仍是 empty 态，但原因要与空文件区分开
        path = self.put("持仓.csv", POS_HEADER + "\n")
        res = br.parse_positions(path)
        self.assertEqual(res.state, br.STATE_EMPTY)
        self.assertEqual(res.reason, "no_data_rows")


class HalfWrittenTest(TmpCase):
    """§4.3 test_export_half_written_keeps_previous：失败保旧 + 计数。"""

    def test_export_half_written_keeps_previous(self):
        full = POS_ROWS + [("300502", "新易盛", "100", "100"), ("000683", "远兴能源", "200", "200")]
        path = self.put("持仓明细.csv", positions_text(full))
        ledger = br.ReadLedger("positions")
        first = ledger.refresh(path)
        self.assertEqual(first.state, br.STATE_OK)
        self.assertEqual(ledger.row_count, 5)
        ok_at = ledger.last_ok_at
        kept = ledger.current_rows()

        # 形态 A：写到一半，表头被截掉一列 → header_missing
        write_bytes(path, "证券代码,证券名称\n600588,用友网络\n".encode("utf-8"))
        res = ledger.refresh(path)
        self.assertEqual(res.state, br.STATE_PARSE_FAIL)
        self.assertIn("header_missing", res.reason)
        self.assertEqual(ledger.current_rows(), kept, "半文件不许推翻上一份可信读数")
        self.assertEqual(ledger.row_count, 5)
        self.assertEqual(ledger.parse_fail_total, 1)
        self.assertEqual(ledger.last_ok_at, ok_at, "失败不推进 last_ok_at")

        # 形态 B：表头完好、行数骤降（5→2，低于参照的 0.5 倍）→ row_collapse，同样保留旧值
        write_bytes(path, positions_text(POS_ROWS[:2]).encode("utf-8"))
        res = ledger.refresh(path)
        self.assertEqual(res.state, br.STATE_PARSE_FAIL)
        self.assertIn("row_collapse:5->2", res.reason)
        self.assertEqual(ledger.current_rows(), kept)
        self.assertEqual(ledger.parse_fail_total, 2)

        # 5→3（正好在闸值之上）＝真减仓，照旧提交、旧值让位、last_ok_at 推进
        write_bytes(path, positions_text(POS_ROWS[:3]).encode("utf-8"))
        res = ledger.refresh(path)
        self.assertEqual(res.state, br.STATE_OK)
        self.assertEqual(ledger.row_count, 3)
        self.assertGreaterEqual(ledger.last_ok_at, ok_at)
        self.assertEqual(ledger.parse_fail_total, 2, "成功提交不清零失败计数（它是留痕）")

    def test_small_previous_reading_does_not_trigger_collapse(self):
        # 上一份只有 1 行时，今天 0 个持仓行不该被"骤降"误判成解析失败（参照物不足即不判）
        ledger = br.ReadLedger("positions")
        ledger.refresh(self.put("持仓明细.csv", positions_text(POS_ROWS[:1])))
        self.assertEqual(ledger.row_count, 1)
        res = ledger.refresh(self.put("持仓明细.csv", positions_text(POS_ROWS[:1])))
        self.assertEqual(res.state, br.STATE_OK)

    def test_ledger_state_three_terms_are_distinct(self):
        # never / stale / ok 三态不许互相冒充（§173 ③ 的"三态不混"）
        clock = {"t": 1000.0}
        ledger = br.ReadLedger("positions", now_fn=lambda: clock["t"], stale_after_sec=3600)
        self.assertEqual(ledger.state(), br.STATE_NEVER)
        ledger.refresh(self.put("持仓明细.csv", positions_text()))
        self.assertEqual(ledger.state(), br.STATE_OK)
        clock["t"] += 3601
        self.assertEqual(ledger.state(), br.STATE_STALE)
        self.assertGreater(ledger.age_sec(), 3600)


class EncodingTest(TmpCase):
    """§4.3 test_export_encoding_three_fallbacks：三种编码都真解出中文列名与中文名称。"""

    def test_export_encoding_three_fallbacks(self):
        for enc in ("utf-8-sig", "utf-8", "gbk"):
            path = self.put("持仓明细.csv", positions_text(), encoding=enc)
            res = br.parse_positions(path)
            self.assertEqual(res.state, br.STATE_OK, enc)
            # utf-8-sig 兜底也能吃无 BOM 的 utf-8，所以只要求落在三条兜底之内；
            # gbk 必须被认成 gbk——它是最后一条兜底，前两条解码不了它（这正是中文列值完整的证明）
            self.assertIn(res.encoding, ("utf-8-sig", "utf-8", "gbk"), enc)
            if enc == "gbk":
                self.assertEqual(res.encoding, "gbk")
            self.assertEqual(res.rows[0]["symbol"], 600588, enc)     # 数字去掉前导零即整型
            self.assertEqual(res.rows[0]["name"], "用友网络", enc)     # 中文列值完整
            self.assertEqual(res.rows[0]["qty"], 600, enc)

    def test_gbk_asset_and_orders(self):
        # 券商导出的金额是带千分位的**带引号**字段；不带引号时逗号就是列分隔符（现实歧义，不猜）
        quoted = self.put("账户资产.csv", '总资产,可用资金,证券市值\n"69,551.32","12,300.00",0\n',
                          encoding="gbk")
        res = br.parse_asset(quoted)
        self.assertEqual(res.state, br.STATE_OK)
        self.assertEqual(res.rows[0]["total_assets"], 69551.32)
        self.assertEqual(res.rows[0]["usable_cash"], 12300.0)
        self.assertEqual(res.rows[0]["market_value"], 0)
        orders = self.put("当日委托.csv", ORD_HEADER + "\n002415,88001,已报,500,33.20\n",
                          encoding="gbk")
        res3 = br.parse_orders(orders)
        self.assertEqual(res3.state, br.STATE_OK)
        self.assertEqual(res3.rows[0]["order_id"], 88001)
        self.assertEqual(res3.rows[0]["status"], "已报")
        self.assertEqual(res3.rows[0]["qty"], 500)

    def test_undecodable_bytes_is_parse_fail_not_empty(self):
        # 三种兜底全失败＝解析失败（保留旧读数），绝不能读成"空仓"
        path = os.path.join(self.export_dir, "持仓明细.csv")
        write_bytes(path, bytes([0xFF, 0xFE, 0x00, 0x01, 0x02, 0x03, 0xFF, 0x81, 0x40]))
        res = br.parse_positions(path)
        self.assertEqual(res.state, br.STATE_PARSE_FAIL)
        self.assertEqual(res.reason, "encoding_undecodable")


class FormatGateTest(TmpCase):
    """二进制格式明确抛错：读不了 ≠ 读出来是空的（§4.1 边界 2 的另一半）。"""

    def test_binary_formats_raise_labelled_unsupported(self):
        xlsx = os.path.join(self.export_dir, "持仓明细.xlsx")
        write_bytes(xlsx, b"PK\x03\x04" + b"\x00" * 64)
        ole = os.path.join(self.export_dir, "当日委托.xls")
        write_bytes(ole, b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1" + b"\x00" * 64)
        # 显式解析＝抛错（"读不了"是一条明确的错误，不是空结果）
        with self.assertRaises(br.UnsupportedFormatError) as ctx:
            br.parse_positions(xlsx)
        self.assertIn("unsupported_binary_format", str(ctx.exception))
        with self.assertRaises(br.UnsupportedFormatError) as ctx2:
            br.parse_orders(ole)
        self.assertIn("unsupported_binary_magic", str(ctx2.exception))
        # 走账本这条腿＝收成独立的 unsupported 读数，既不算 ok 也不算 empty
        ledger = br.ReadLedger("orders")
        res = ledger.refresh(ole)
        self.assertEqual(res.state, br.STATE_UNSUPPORTED)
        self.assertEqual(ledger.unsupported_total, 1)
        self.assertEqual(ledger.current_rows(), [])
        # 发现侧：.xlsx 连列都不列（它不是"今天的导出"）；.xls 列出来再由解析侧嗅探定生死
        self.assertEqual([h.file_name for h in
                          br.discover_exports(os.path.join(self.export_dir, "*.xlsx"),
                                              allowed_roots=[self.export_dir])], [])
        listed = br.discover_exports(os.path.join(self.export_dir, "*.xls"),
                                     allowed_roots=[self.export_dir])
        self.assertEqual([h.file_name for h in listed], ["当日委托.xls"])
        self.assertEqual(ledger.refresh(listed[0].path).state, br.STATE_UNSUPPORTED)

    def test_xls_extension_with_text_body_still_parses(self):
        # 历史上确有把 CSV 改名成 .xls 的导出：嗅探通过就照文本解析
        path = self.put("持仓明细.xls", positions_text(), encoding="gbk")
        res = br.parse_positions(path)
        self.assertEqual(res.state, br.STATE_OK)
        self.assertEqual(res.row_count, 3)


class ReadOnlyTest(TmpCase):
    """边界 1：解析器对导出目录零写零删——行为锁 + 静态锁各一枚。"""

    def _dir_fingerprint(self):
        entries = {}
        for dirpath, dirnames, filenames in os.walk(self.root):
            entries[dirpath] = ("dirs:" + ",".join(sorted(dirnames)),
                                "files:" + ",".join(sorted(filenames)))
            for name in filenames:
                p = os.path.join(dirpath, name)
                st = os.stat(p)
                with open(p, "rb") as fh:
                    digest = hashlib.sha256(fh.read()).hexdigest()
                entries[p] = (digest, st.st_size, int(st.st_mtime))
        return entries

    def test_parse_touches_nothing_in_export_dir(self):
        good_paths = [
            self.put("持仓明细.csv", positions_text()),
            self.put("当日委托.csv", ORD_HEADER + "\n002415,88001,已报,500,33.20\n", encoding="gbk"),
            self.put("成交记录.txt", FILL_HEADER + "\n002415,7001,33.21,500\n", encoding="utf-8-sig"),
            self.put("账户资产.csv", ASSET_HEADER + '\n"69,551.32",0,12300\n'),
        ]
        self.put("坏文件.csv", "不是表头\n1,2,3\n")           # 必判解析失败
        write_bytes(os.path.join(self.export_dir, "二进制.xls"),
                    b"\xd0\xcf\x11\xe0" + b"\x00" * 40)       # 必判 unsupported
        missing = os.path.join(self.export_dir, "今天没导出.csv")
        before = self._dir_fingerprint()

        ledgers = {k: br.ReadLedger(k) for k in ("positions", "orders", "fills", "asset")}
        for p in good_paths:
            kind = br.classify_name(os.path.basename(p))
            res = ledgers[kind].refresh(p)
            self.assertEqual(res.state, br.STATE_OK, p)
        # 三条失败分支同样走一遍：它们都只读，不许留下任何痕迹
        self.assertEqual(br.parse_positions(missing).state, br.STATE_MISSING)
        self.assertEqual(br.parse_positions(os.path.join(self.export_dir, "坏文件.csv")).state,
                         br.STATE_PARSE_FAIL)
        with self.assertRaises(br.UnsupportedFormatError):
            br.parse_positions(os.path.join(self.export_dir, "二进制.xls"))
        for exp in br.discover_exports(os.path.join(self.export_dir, "*"),
                                       allowed_roots=self.whitelist):
            ledgers[exp.kind].refresh(exp.path)

        self.assertEqual(self._dir_fingerprint(), before, "导出目录必须逐字节原样（证据链）")
        # 失败分支也没把旧读数刷掉
        self.assertEqual(ledgers["positions"].row_count, 3)

    def test_module_source_has_no_write_or_delete_primitive(self):
        # 静态负锁：本模块内出现的每个 open() 只能是二进制只读；写/删原语一律不许存在
        src = os.path.abspath(br.__file__)
        code_lines = []
        for line in open(src, "r", encoding="utf-8").read().split("\n"):
            stripped = line.strip()
            if stripped.startswith("#") or stripped.startswith('"""'):
                continue
            code_lines.append(line)
        joined = "\n".join(code_lines)
        for forbidden in ("os.remove", "os.unlink", "os.rename", "os.replace", "os.makedirs",
                          "os.mkdir", "shutil", "write_text", "write_bytes", "truncate("):
            self.assertNotIn(forbidden, joined, forbidden)
        for line in code_lines:
            if "open(" in line:
                self.assertIn('"rb"', line, line)
        # 白名单准入函数只能有一处真源，别处再写一份就会漂
        self.assertEqual(joined.count("def is_under("), 1)


class LedgerSnapshotTest(TmpCase):
    """账本快照字段形状（Go 侧只透传不重算，字段名即契约）。"""

    def test_snapshot_field_names(self):
        self.put("持仓明细.csv", positions_text())
        ledger = br.ReadLedger("positions", source="export")
        ledger.refresh(os.path.join(self.export_dir, "持仓明细.csv"))
        snap = ledger.snapshot()
        for key in ("last_ok_at", "source", "file_name", "row_count", "parse_fail_total"):
            self.assertIn(key, snap)
        self.assertEqual(snap["source"], "export")
        self.assertEqual(snap["row_count"], 3)
        self.assertEqual(snap["parse_fail_total"], 0)
        # current_rows 返回拷贝：调用方改它不许污染账本
        rows = ledger.current_rows()
        rows[0]["qty"] = -1
        self.assertEqual(ledger.current_rows()[0]["qty"], 600)

    def test_refresh_rejects_unknown_kind(self):
        with self.assertRaises(ValueError):
            br.ReadLedger("mystery").refresh(os.path.join(self.export_dir, "nope.csv"))


if __name__ == "__main__":
    unittest.main()
