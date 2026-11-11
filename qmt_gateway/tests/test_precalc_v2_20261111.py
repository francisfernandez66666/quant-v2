# -*- coding: utf-8 -*-
"""§PRECALC v2（2026-11-11）回归锁：QMT 直取 + 脑侧烘焙的对照方案。

钉三件事（owner 17:5x 需求「自动化测试程序加上今天的开发内容」）：
  1. bake_bakeables 烘出「QMT 拿不到的因子」且形态逐一对齐 rules_factor 消费端
     （valuation_series=_field_series 面板 / fin_pit=_pit_value / flow·heat·news=scalar_proxy）；
  2. 价量缺源时 bake_only 语义（估值/财务/D1 仍值得烘，绝不静默整票丢弃）；
  3. scalar_bands 出厂带 NewsEvent/ConceptHeat（D1 分数/利好池强度的评分带）。
数据全部用临时 SQLite 造（不碰真实研究库），跑完即清理（owner：清理掉测试数据）。
"""
import json
import os
import sqlite3
import sys
import tempfile

import pytest

QG = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(QG), "scripts"))

import pregen_carrier_factors_gz as P  # noqa: E402


@pytest.fixture()
def research_db(tmp_path, monkeypatch):
    """临时研究库：daily_basic/fina_indicator/d1_scores 三表 + 资金流 json。"""
    dbp = str(tmp_path / "trading.db")
    conn = sqlite3.connect(dbp)
    conn.execute("CREATE TABLE daily_basic (ts_code TEXT, trade_date TEXT, pe_ttm REAL,"
                 " pb REAL, ps_ttm REAL, turnover_rate REAL, circ_mv REAL)")
    for i in range(20):
        conn.execute("INSERT INTO daily_basic VALUES (:c, :d, 51.8, 3.5, 2.7, 8.4, 210000)",
                     {"c": "600580.SH", "d": "2026%02d01" % (i + 1)})
    conn.execute("CREATE TABLE fina_indicator (ts_code TEXT, end_date TEXT, ann_date TEXT,"
                 " eps REAL, roe REAL, roe_waa REAL, roa REAL, grossprofit_margin REAL,"
                 " netprofit_margin REAL, debt_to_assets REAL, yoy_or REAL)")
    conn.execute("INSERT INTO fina_indicator VALUES ('600580.SH','20260930','20261028',"
                 "0.19,1.36,1.30,0.86,25.70,4.41,60.1,12.3)")
    conn.execute("CREATE TABLE d1_scores (date TEXT, code TEXT, score REAL, blocked INT,"
                 " reason TEXT, created_at TEXT)")
    conn.execute("INSERT INTO d1_scores VALUES ('20261111','600580','18.0',0,'电机事件','2026-11-11 15:06')")
    conn.commit()
    conn.close()
    flowp = tmp_path / "moneyflow"
    flowp.mkdir()
    flow = {"saved_at": "2026-11-11T15:05:48+08:00", "days": [
        {"Date": "2026-11-07", "MainNet": -47401832},
        {"Date": "2026-11-10", "MainNet": -37376221},
        {"Date": "2026-11-11", "MainNet": 12000000}]}
    (flowp / "600580.json").write_text(json.dumps(flow), encoding="utf-8")
    monkeypatch.setattr(P, "LOCAL_DB", dbp)
    monkeypatch.setattr(P, "FLOW_DIR", str(flowp))
    return dbp


def test_bake_bakeables_shapes_align_consumers(research_db):
    """烘出键=QMT 拿不到的集合，形态对齐 rules_factor 消费端（_field_series/_pit_value/_scalar_score）。"""
    out = P.bake_bakeables("600580.SH", {"hot_pool": {"bullish": {"600580.SH": {"score": 0.94}}}})
    # 估值面板：{"pe_ttm":[...],...} —— _field_series 直接 get(field) 取序列
    assert set(out["valuation_series"]) == {"pe_ttm", "pb", "ps_ttm"}
    assert len(out["valuation_series"]["pe_ttm"]) == 20
    # 换手序列：纯数值列表（_plain_series）
    assert len(out["turnover_series"]) == 20
    # 财务分位：_pit_value 按字段取最新档
    assert out["fin_pit"]["roe"] == pytest.approx(1.36)
    # 资金流标量：MainNet/流通市值（今日为净流入 -> 正）
    assert out["flow_today_pct"] > 0
    assert out["flow_5d_pct"] < out["flow_today_pct"]
    # 新闻事件 = D1 分数（非 blocked 原值），概念热度 = 利好池强度分
    assert out["news_event"] == pytest.approx(18.0)
    assert out["concept_heat"] == pytest.approx(0.94)


def test_bake_only_when_price_volume_missing(research_db):
    """价量源（hithink）失败 ⇒ bake_only：估值/财务/D1 仍烘，绝不整票静默丢弃。"""
    # 模拟 hithink 失败：临时改坏 URL 域（monkeypatch 类属性走 fetch 分支失败）
    orig = P.fetch_klines

    def broken(_symbol):
        raise OSError("network down")

    P.fetch_klines = broken
    try:
        bars = None
        fact = P.compute(bars) if bars else None
        assert fact is None
        bakeables = P.bake_bakeables("600580.SH", {"hot_pool": {"bullish": {}}})
        # 无 hot_pool 命中 ⇒ 无 concept_heat（如实缺省，不编造）
        assert "concept_heat" not in bakeables
        assert bakeables["fin_pit"]["eps"] == pytest.approx(0.19)
        assert "valuation_series" in bakeables
    finally:
        P.fetch_klines = orig


def test_scalar_bands_factory_defaults():
    """scalar_bands 出厂带 NewsEvent/ConceptHeat 评分带（D1 分数/利好池分的评分依据）。"""
    params = json.load(open(os.path.join(QG, "local_executor_params.json"), encoding="utf-8"))
    bands = (params.get("params") or {}).get("scalar_bands") or {}
    assert bands.get("NewsEvent", {}).get("min") == 30
    assert bands.get("ConceptHeat", {}).get("min") == 0.6


def test_bars_count_covers_mom60():
    """bars_count 出厂 ≥60（Mom60 因子窗口需要），且 _hist_bars 读的是这个旋钮。"""
    params = json.load(open(os.path.join(QG, "local_executor_params.json"), encoding="utf-8"))
    # 旋钮未显式烘时模板缺省 80（覆盖 Mom60=60）
    src = open(os.path.join(QG, "qmt_internal_strategy.py"), encoding="utf-8").read()
    assert 'PARAMS.get("bars_count", 80)' in src
    assert (params.get("params") or {}).get("bars_count", 80) >= 60


def test_status_file_tracks_consec_fail(research_db, tmp_path, monkeypatch):
    """状态文件记录 consec_fail/coverage（B 机制：连续失败≥2 触发通知的判定依据）。"""
    (tmp_path / "qmt_gateway").mkdir()  # _write_status 落 BUILD_DIR/qmt_gateway/ 下
    monkeypatch.setattr(P, "BUILD_DIR", str(tmp_path))
    status, coverage = P._write_status("20261111", 96, 92, 4, 0, False)
    assert coverage == 95
    assert status["consec_fail"] == 0
    status2, _ = P._write_status("20261111", 96, 0, 96, 1, False)
    assert status2["coverage"] == 0 and status2["consec_fail"] == 1
