# -*- coding: utf-8 -*-
"""2026-11-16 持仓成本口径回归（owner 现场：9.52 元的票显示成本 9527 元）。

QMT 的 PositionStatics 导出同时给出两列：
  * ``持仓成本`` = 该票的持仓成本**合计（元）**；
  * ``成本价``   = **每股**成本。
旧别名表把 ``持仓成本`` 也登记成 cost_price，而 ``_canon_columns`` 的精确趟按**列序**
命中，``持仓成本`` 排在 ``成本价`` 前面 ⇒ cost_price 恒等于整票合计，report_uplink 再按
qty×cost 算 amount，云端账面成本直接错到千元/股量级，并把动态目标仓位长期顶在
"已达目标仓位"（全天零买入）。

本文件钉三件事：
  1. 两列都在时，cost_price 取 ``成本价``、cost_amount 取 ``持仓成本``，绝不互相顶替；
  2. 只有合计列时按 qty 反推每股成本（老导出版本仍可用）；
  3. qty<=0 或合计<=0 时**不造数**（清仓残行不该长出成本）。
"""
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import broker_reads  # noqa: E402

# 现网 PositionStatics 的真实表头（2026-11-16 从盒子导出取，29 列）。
POSITIONSTATICS_HEADER = (
    "证券公司,资金账号,账号名称,账号类别,账号备注,市场代码,市场名称,证券代码,证券名称,"
    "当前拥股,持仓成本,成本价,盈亏,市值,股东账号,可用股份,在途股份,盈亏比例,最新价,"
    "昨收价,etf净值,当日盈亏,资产占比,市值占比,状态,冻结是否,隔夜拥股,账号key,证券key"
)

FULL_ROW = (
    "东莞证券uf20量化智能股票实盘,2069008957,张三,普通,,SZ,深交所,002115,三维通信,"
    "2000,19045.20,9.522600,-1059.20,0.00,0940353701,2000,0,-100.00%,9.01,,0,-620.00,"
    "0.0000%,0.0000%,,-3.22%,2000,acctkey,stockkey"
)


class CostColumnSplit(unittest.TestCase):
    def test_per_share_wins_over_total(self):
        """两列都在：cost_price=每股，cost_amount=合计（旧实现取反了）。"""
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "PositionStatics-20261116_141329.csv")
            with open(path, "w", encoding="gbk") as fh:
                fh.write(POSITIONSTATICS_HEADER + "\n" + FULL_ROW + "\n")
            res = broker_reads.parse_positions(path)
            self.assertEqual(res.state, broker_reads.STATE_OK, res.reason)
            rec = res.rows[0]
            self.assertEqual(rec.get("qty"), 2000)
            self.assertAlmostEqual(rec.get("cost_price"), 9.5226, places=6)
            self.assertAlmostEqual(rec.get("cost_amount"), 19045.20, places=2)

    def test_total_never_overrides_present_per_share(self):
        """合计列离谱时也不能把每股列顶掉（不做平均、不做覆盖）。"""
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "PositionStatics-20261116_141329.csv")
            header = "证券代码,证券名称,当前拥股,持仓成本,成本价"
            row = "002385,大北农,6700,99999999,2.950800"
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(header + "\n" + row + "\n")
            rec = broker_reads.parse_positions(path).rows[0]
            self.assertAlmostEqual(rec.get("cost_price"), 2.9508, places=6)


class TotalOnlyDerivation(unittest.TestCase):
    def test_derive_per_share_from_total(self):
        """只有合计列（老导出版）：按 qty 反推每股成本。"""
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "PositionStatics-20261116_141329.csv")
            header = "证券代码,证券名称,当前拥股,持仓成本"
            row = "002548,金新农,5700,19841.13"
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(header + "\n" + row + "\n")
            rec = broker_reads.parse_positions(path).rows[0]
            self.assertAlmostEqual(rec.get("cost_price"), 19841.13 / 5700, places=6)
            # 反推回去必须还原合计（qty×cost≈持仓成本），这是"量纲对齐"的自证。
            self.assertAlmostEqual(rec.get("cost_price") * rec.get("qty"), 19841.13, places=2)

    def test_zero_qty_row_gets_no_cost(self):
        """清仓残行（qty=0，导出仍带 持仓成本 残值）：不造成本价。"""
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "PositionStatics-20261116_141329.csv")
            header = "证券代码,证券名称,当前拥股,持仓成本"
            row = "002115,三维通信,0,1059.20"
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(header + "\n" + row + "\n")
            rec = broker_reads.parse_positions(path).rows[0]
            self.assertNotIn("cost_price", rec)

    def test_zero_total_gets_no_cost(self):
        """合计为 0：同样不造数（0 会被下游当成"未知"再去兜底，平地长出 0 成本）。"""
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "PositionStatics-20261116_141329.csv")
            header = "证券代码,证券名称,当前拥股,持仓成本"
            row = "002115,三维通信,2000,0.00"
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(header + "\n" + row + "\n")
            rec = broker_reads.parse_positions(path).rows[0]
            self.assertNotIn("cost_price", rec)


if __name__ == "__main__":
    unittest.main()
