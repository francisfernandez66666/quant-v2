#!/usr/bin/env python3
# -*- coding: utf-8 -*-
# 本地自动研究进度查看页面（不上云，纯本地）。
# 提供一个轻量 HTTP 服务（127.0.0.1:8099），页面实时展示：
#   1) 数据准备度：daily 覆盖 / 财务 / 行业 / 板块历史
#   2) 自动研究候选：因子组合 / 方向 / 权重 / IR·IC / 样本内外IR / 反推超额 / 护栏 / 状态
#   3) 脚本进度：财务装载→补行业→sector-rebuild→discover-factors→discover-patterns→汇总
#   4) 进程状态：load_finance / research / dataload 是否在运行
# 页面每 30s 轮询 /api/status 自动刷新。
#
# 仅用 Python 标准库（http.server + sqlite3 + subprocess + json），零第三方依赖。
# 启动：python3 scripts/progress_server.py   （浏览器打开 http://localhost:8099）
#
# English: local auto-research progress page (local-only, no cloud). Serves a lightweight HTTP
# service on 127.0.0.1:8099 showing data readiness, research candidates, script steps and process
# status, auto-refreshing every 30s via polling /api/status. Standard-library only.

import json
import os
import sqlite3
import subprocess
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

# ---------------- 可配置路径 ----------------

# 本地研究库（SQLite，只读访问）
DB_PATH = os.environ.get("QUANT_DB", "/Users/zhangzifei/.quant-trading-v2/trading.db")
# 自动研究主控脚本日志（run_auto_research_full.sh 输出）
LOG_PATH = os.environ.get("QUANT_LOG", "/tmp/auto_full.log")
# 端口
PORT = int(os.environ.get("QUANT_PROGRESS_PORT", "8099"))

# 需要检测的进程关键字（ps 匹配）
PROC_PATTERNS = [
    ("财务装载", "load_finance.py"),
    ("自动研究", "research-local"),
    ("历史装载", "dataload"),
]

# 因子 ID → (中文名, 一句说明)。与 internal/factor 库注册的因子一一对应，
# 供前端把候选里的因子 ID 渲染成"这条战法在做什么"的可读描述。
# English: factor ID → (Chinese name, one-line description), mirroring the factor registry so the
# frontend can render a candidate's factor IDs as a readable "what this strategy does" description.
FACTOR_META = {
    # 估值（Value）
    "EP_ttm": ("市盈率TTM倒数", "1/PE_ttm，越大越便宜"),
    "BP": ("市净率倒数", "1/PB，越大越便宜"),
    "SP_ttm": ("市销率TTM倒数", "1/PS_ttm，越大越便宜"),
    "CFP_ttm": ("市现率TTM倒数", "1/PCF_ttm，越大越便宜"),
    "DP": ("股息率TTM", "股息率（%），越高回报倾向越好"),
    # 成长（Growth）
    "YoyNetProfit": ("净利同比", "归母净利同比（%）"),
    "SUE": ("单季净利同比", "单季净利同比（%）"),
    # 质量（Quality）
    "ROE": ("净资产收益率", "ROE，越高越优质"),
    "GrossMargin": ("毛利率", "毛利率"),
    "NetMargin": ("净利率", "净利率"),
    "DebtToAssets": ("资产负债率", "负债/资产，越低越稳健"),
    # 规模（Size）
    "LnMktCap": ("对数市值", "ln(原始价×股本)"),
    # 波动率（Volatility）
    "Volatility20": ("20日收益波动率", "20日对数收益波动"),
    "Amplitude20": ("20日平均振幅", "20日(高−低)/收均值"),
    "RealizedVol5": ("5日已实现波动率", "5日对数收益波动"),
    "RealizedVol10": ("10日已实现波动率", "10日对数收益波动"),
    "AtrRatio14": ("ATR14/收盘", "单位价格真实波幅"),
    "HighLow20": ("20日区间比", "20日最高/最低价"),
    "VolRatio5": ("5日波动放大比", "|当日收益|/5日波动率"),
    "Drawdown20": ("20日回撤", "1−收盘/20日最高（回调深度）"),
    # 动量（Momentum）
    "Mom5": ("5日动量", "过去5日收益"),
    "Mom10": ("10日动量", "过去10日收益"),
    "Mom20": ("20日动量", "过去20日收益"),
    "Mom60": ("60日动量", "过去60日收益"),
    "RSI14": ("14日RSI", "Wilder 平滑 RSI14"),
    "BBI": ("多空指标", "(MA3+MA6+MA12+MA24)/4"),
    "EMA10_20": ("中期趋势斜率", "EMA10/EMA20−1"),
    "Alpha1": ("趋势强度", "WQ-Alpha1 复合动量"),
    "Alpha4": ("超跌反转", "WQ-Alpha4 超跌"),
    "Brk20": ("20日新高突破", "收盘创20日新高=1"),
    "Brk60": ("60日新高突破", "收盘创60日新高=1"),
    "BullAlign": ("均线多头排列", "MA5>MA10>MA20且收>MA5"),
    # 流动性（Liquidity）
    "STO20": ("20日平均换手率", "换手率20日均值"),
    "STOA": ("对数20日均成交额", "ln(20日均成交额)"),
    "Amihud20": ("20日Amihud非流动性", "越高越不流动"),
    "VMA5": ("5日量均比", "MA5量/MA20量"),
    "VMA10": ("10日量均比", "MA10量/MA20量"),
    "VSTD20": ("20日量变异系数", "20日量 std/均值"),
    "VMAX10": ("10日量峰比", "10日最大量/当日量"),
    "VMIN10": ("10日量地比", "当日量/10日最小量"),
    "TurnoverStd20": ("20日换手率波动", "20日换手率标准差"),
    "Alpha12": ("量价背离", "WQ-Alpha12 量价"),
    "Alpha101": ("区间位置×量", "WQ-Alpha101 变体"),
    # 形态算子（F1）
    "VolSurge5": ("放量倍数", "当日量/20日均量（放量突破）"),
    "VolShrink": ("缩量比", "5日均量/20日均量（回调缩量）"),
}


def factor_name(fid):
    """因子 ID → 中文名；未知 ID 回退为 ID 本身。"""
    return FACTOR_META.get(fid, (fid, ""))[0]


def factor_desc(fid):
    """因子 ID → 一句说明；未知 ID 返回空串。"""
    return FACTOR_META.get(fid, ("", ""))[1]


# ---------------- 数据聚合 ----------------

def db_connect():
    """以只读模式打开研究库（避免与写入进程的锁冲突）。"""
    return sqlite3.connect("file:%s?mode=ro" % DB_PATH, uri=True, timeout=5)


def parse_candidates(rows):
    """把 research_candidates 行解析为页面可展示的字典列表。"""
    out = []
    for r in rows:
        cid, created, kind, status, factors_raw, weights_raw, metric, ic, ir, excess, horizon, reason = r
        try:
            factors = json.loads(factors_raw) if factors_raw else []
        except Exception:
            factors = []
        weights = {}
        directions = {}
        buy_threshold = 70
        if weights_raw:
            try:
                wobj = json.loads(weights_raw)
                weights = wobj.get("weights", {})
                directions = wobj.get("directions", {})
                buy_threshold = wobj.get("buy_threshold", 70)
            except Exception:
                pass
        # 从 reason 提取样本内外 IR、反推超额与反推 t（discover-factors 写入的格式）
        in_ir = out_ir = gen_excess = gen_t = None
        try:
            # 形如：... | 样本内IR=0.914 样本外IR=0.939 反推超额=-0.0102 反推t=-11.35
            import re
            m = re.search(r"样本内IR=([-\d.]+)", reason or "")
            if m:
                in_ir = float(m.group(1))
            m = re.search(r"样本外IR=([-\d.]+)", reason or "")
            if m:
                out_ir = float(m.group(1))
            m = re.search(r"反推超额=([-\d.]+)", reason or "")
            if m:
                gen_excess = float(m.group(1))
            m = re.search(r"反推t=([-\d.]+)", reason or "")
            if m:
                gen_t = float(m.group(1))
        except Exception:
            pass
        # 护栏判定：reason 含"通过护栏"字样；非 factor/pattern 一律视为权重类
        guard_pass = "通过护栏" in (reason or "") or "护栏=true" in (reason or "")
        guard_reason = reason or ""
        if guard_pass and not guard_reason:
            guard_reason = "通过护栏"
        # 因子可读列表 + 玩法说明（前端"这条战法在做什么"）
        factor_lines = []
        for f in factors:
            w = weights.get(f, 0.0) if isinstance(weights, dict) else 0.0
            d = directions.get(f, 1) if isinstance(directions, dict) else 1
            factor_lines.append({
                "id": f,
                "name": factor_name(f),
                "desc": factor_desc(f),
                "dir": d,
                "dir_text": "看多" if d >= 0 else "看空",
                "weight": w,
            })
        n = len(factor_lines)
        short_n = sum(1 for x in factor_lines if x["dir"] >= 0)
        short_d = n - short_n
        play = (
            "每天给所有股票按上面 {0} 个指标打分，分数最高的前一批会被标记为「值得买」，"
            "赌它们接下来 {1} 个交易日能涨。".format(n, horizon if horizon else 5)
        )
        if short_d > 0:
            play += " 注意：带「看空」的 {0} 个指标是反着用的——这项数值越高，反而越说明不该买。".format(short_d)
        reliable = {
            "ok": guard_pass,
            "verdict": "可以试试" if guard_pass else "不建议",
            "samples": [{"label": "前半段历史回放（样本内）", "key": "稳定度", "value": in_ir,
                         "desc": "先拿前半段历史行情回放：这套打分的选股效果，稳定度越高越靠谱"},
                        {"label": "另一段没用过的历史回放（样本外）", "key": "稳定度", "value": out_ir,
                         "desc": "再拿一段完全没参与挑规律的行情回放：防止规律只对老数据灵、换市场就失灵"},
                        {"label": "反推超额（高分股 vs 随便买）", "key": "超额", "value": gen_excess,
                         "desc": "对比「按这套规律选出的股票」和「随便买」：选的比平均多赚多少"}],
            "details": {
                "gen_t": gen_t,
                "gen_excess": gen_excess,
                "ir": ir,
                "ic": ic,
                "reason": guard_reason,
            },
        }
        out.append({
            "id": cid,
            "created": created,
            "kind": kind,
            "status": status,
            "factors": factors,
            "weights": weights,
            "directions": directions,
            "buy_threshold": buy_threshold,
            "metric": metric,
            "ic": ic,
            "ir": ir,
            "excess": excess,
            "horizon": horizon,
            "in_ir": in_ir,
            "out_ir": out_ir,
            "gen_excess": gen_excess,
            "gen_t": gen_t,
            "guard_pass": guard_pass,
            "reason": guard_reason,
            "factor_lines": factor_lines,
            "play": play,
            "reliable": reliable,
        })
    return out


def collect_data_ready(c):
    """收集数据准备度统计。"""
    def one(sql):
        try:
            return c.execute(sql).fetchone()[0]
        except Exception:
            return None
    stocks = one("SELECT COUNT(*) FROM stocks") or 0
    industry = one("SELECT COUNT(*) FROM stocks WHERE industry IS NOT NULL AND industry!=''") or 0
    daily_rows = one("SELECT COUNT(*) FROM daily") or 0
    ready = one("SELECT COUNT(DISTINCT ts_code) FROM daily") or 0
    dmin = one("SELECT MIN(trade_date) FROM daily")
    dmax = one("SELECT MAX(trade_date) FROM daily")
    fina = one("SELECT COUNT(*) FROM fina_indicator") or 0
    income = one("SELECT COUNT(*) FROM income") or 0
    sector_rows = one("SELECT COUNT(*) FROM sector_history") or 0
    sector_ind = one("SELECT COUNT(DISTINCT industry) FROM sector_history") or 0
    cand_total = one("SELECT COUNT(*) FROM research_candidates") or 0
    cand_applied = one("SELECT COUNT(*) FROM research_candidates WHERE status='applied'") or 0
    cand_proposed = one("SELECT COUNT(*) FROM research_candidates WHERE status='proposed'") or 0
    return {
        "stocks": stocks,
        "industry": industry,
        "daily_rows": daily_rows,
        "ready_stocks": ready,
        "date_min": dmin,
        "date_max": dmax,
        "fina": fina,
        "income": income,
        "sector_rows": sector_rows,
        "sector_ind": sector_ind,
        "cand_total": cand_total,
        "cand_applied": cand_applied,
        "cand_proposed": cand_proposed,
    }


def parse_script_steps():
    """解析 /tmp/auto_full.log，提取各步骤状态与最近日志行。"""
    steps = [
        ("财务装载", "财务装载进程已结束", "财务装载完成"),
        ("补行业", "当前有行业 stocks", "行业检查"),
        ("sector-rebuild", "板块历史重建完成", "板块历史重建"),
        ("discover-factors", "因子候选", "因子战法发现"),
        ("discover-patterns", "无形态通过护栏", "形态战法发现"),
        ("候选汇总", None, "候选汇总"),
    ]
    try:
        with open(LOG_PATH, "r", errors="replace") as f:
            content = f.read()
    except FileNotFoundError:
        return {"running": False, "steps": [], "last_lines": [], "started": False}

    lines = content.strip().splitlines()
    # 是否有"结束"标记
    finished = "自动研究 结束" in content
    started = "自动研究 开始" in content

    step_states = []
    for name, done_marker, label in steps:
        # 脚本已整体结束 → 全部步骤视为已完成
        if finished:
            state = "done"
        elif done_marker and done_marker in content:
            state = "done"
        elif name == "财务装载" and "财务装载进程已结束" in content:
            state = "done"
        elif name == "补行业" and "当前有行业 stocks" in content:
            state = "done"
        else:
            # 检查该步骤关键字是否已出现（进行中）
            state = "running" if any(k in content for k in step_keywords(name)) else "pending"
        step_states.append({"name": label, "state": state})

    last_lines = lines[-20:] if lines else []
    return {
        "running": started and not finished,
        "finished": finished,
        "started": started,
        "steps": step_states,
        "last_lines": last_lines,
    }


def step_keywords(name):
    """返回某步骤可能出现在日志中的关键字，用于判断是否已开始。"""
    return {
        "财务装载": ["load_finance", "财务装载", "装载 2079 只"],
        "补行业": ["行业分类", "有行业 stocks"],
        "sector-rebuild": ["sector-rebuild", "重建板块历史"],
        "discover-factors": ["discover-factors", "因子发现"],
        "discover-patterns": ["discover-patterns", "形态搜索"],
        "候选汇总": ["候选汇总", "list"],
    }.get(name, [])


def check_processes(data_ready):
    """用 ps 检测关键进程是否在运行，并结合数据状态给出语义提示。
    状态：running 运行中 / done 已完成 / partial 可补齐 / idle 未运行。
    """
    procs = []
    running = {}
    try:
        r = subprocess.run(["ps", "aux"], capture_output=True, text=True, timeout=5)
        stdout = r.stdout
    except Exception:
        stdout = ""
    for label, pat in PROC_PATTERNS:
        running[label] = pat in stdout

    # 财务装载：fina/income 有行数 → 已完成；>0 但不足 → 部分
    fina = (data_ready or {}).get("fina") or 0
    procs.append(_proc("财务装载", running.get("财务装载"),
                       done_cond=fina > 0,
                       done_tip="已装载财务指标 %d 行" % fina,
                       partial_cond=0 < fina < 5000,
                       partial_tip="财务数据不完整（%d 行），可重跑 load_finance 补齐"))

    # 自动研究：有候选 → 已完成
    cands = (data_ready or {}).get("cand_total") or 0
    procs.append(_proc("自动研究", running.get("自动研究"),
                       done_cond=cands > 0,
                       done_tip="已产出 %d 条候选（见下方）" % cands,
                       partial_cond=False,
                       partial_tip=""))

    # 历史装载：有行情股票 < 全市场 → 可补齐
    ready = (data_ready or {}).get("ready_stocks") or 0
    stocks = (data_ready or {}).get("stocks") or 0
    partial = 0 < ready < stocks
    procs.append(_proc("历史装载", running.get("历史装载"),
                       done_cond=ready >= stocks,
                       done_tip="全市场 %d 只已装载行情" % ready,
                       partial_cond=partial,
                       partial_tip="已有 %d/%d 只有行情，可重跑 dataload 补齐" % (ready, stocks)))

    return procs


def _proc(name, is_running, done_cond, done_tip, partial_cond, partial_tip):
    """构造单个进程的展示结构。"""
    if is_running:
        state, tip, css = "running", "运行中", "running"
    elif done_cond:
        state, tip, css = "done", done_tip, "done"
    elif partial_cond:
        state, tip, css = "partial", partial_tip, "partial"
    else:
        state, tip, css = "idle", "未运行", "idle"
    return {"name": name, "running": is_running, "state": state, "tip": tip, "css": css}


def build_status():
    """聚合全部状态为 JSON 字典。"""
    try:
        conn = db_connect()
        c = conn.cursor()
        try:
            rows = c.execute(
                "SELECT id,created_at,kind,status,factors,weights,metric,ic_mean,ir,avg_excess,horizon,reason "
                "FROM research_candidates ORDER BY id DESC").fetchall()
        except Exception:
            rows = []
        data_ready = collect_data_ready(c)
        conn.close()
    except Exception as e:
        data_ready = {"error": str(e)}
        rows = []
    return {
        "data_ready": data_ready,
        "candidates": parse_candidates(rows),
        "script": parse_script_steps(),
        "processes": check_processes(data_ready),
    }


# ---------------- HTML 页面 ----------------

def render_html():
    """生成进度页面 HTML（中文，内嵌 CSS/JS，30s 轮询）。"""
    return """<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>本地自动研究进度</title>
<style>
  body{font-family:system-ui,"PingFang SC","Microsoft YaHei",sans-serif;margin:0;background:#0f1220;color:#e6e8ef;}
  .wrap{max-width:1080px;margin:0 auto;padding:20px;}
  h1{font-size:20px;margin:0 0 4px;}
  .sub{color:#8a8fa3;font-size:13px;margin-bottom:16px;}
  .card{background:#1a1e31;border:1px solid #2a2f47;border-radius:10px;padding:16px;margin-bottom:16px;}
  .card h2{font-size:15px;margin:0 0 12px;color:#cdd3e6;}
  .grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:10px;}
  .stat{background:#222740;border-radius:8px;padding:10px;}
  .stat .k{font-size:12px;color:#8a8fa3;}
  .stat .v{font-size:18px;font-weight:600;margin-top:2px;}
  .cand{background:#222740;border:1px solid #2e3550;border-radius:8px;padding:12px;margin-bottom:10px;}
  .cand .head{display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px;}
  .cand .factors{font-weight:600;font-size:14px;}
  .badge{display:inline-block;padding:2px 8px;border-radius:10px;font-size:12px;font-weight:600;}
  .badge.proposed{background:#3a2f6b;color:#b9a8ff;}
  .badge.applied{background:#1f4d33;color:#5ee89a;}
  .badge.rejected{background:#4d2a2a;color:#ff9d9d;}
  .badge.guard-ok{background:#1f4d33;color:#5ee89a;}
  .badge.guard-fail{background:#4d2a2a;color:#ff9d9d;}
  .meta{font-size:12px;color:#8a8fa3;margin-top:6px;line-height:1.6;}
  .reason{margin-top:8px;font-size:12px;color:#c9c2ff;background:#1a1e31;border-radius:6px;padding:8px;}
  /* 战法卡片：因子行 / 玩法 / 靠谱度 */
  .flist{margin-top:10px;display:flex;flex-direction:column;gap:4px;}
  .frow{display:flex;align-items:center;gap:8px;font-size:13px;background:#1c2136;border-radius:6px;padding:6px 10px;}
  .dir-long{color:#5ee89a;font-weight:600;min-width:32px;}
  .dir-short{color:#ff9d9d;font-weight:600;min-width:32px;}
  .fname{font-weight:600;color:#e6e8ef;}
  .fid{color:#6a7190;font-size:11px;}
  .fdesc{color:#8a8fa3;font-size:12px;flex:1;}
  .fw{color:#c9c2ff;font-size:12px;white-space:nowrap;}
  .play{margin-top:10px;font-size:13px;color:#cdd3e6;background:#222740;border-left:3px solid #64b5f6;border-radius:4px;padding:8px 10px;line-height:1.6;}
  .reli{margin-top:10px;background:#222740;border-radius:8px;padding:10px;}
  .reliq{font-size:13px;font-weight:600;color:#cdd3e6;}
  .verdict{margin:6px 0;}
  .evlist{display:flex;flex-direction:column;gap:6px;margin-top:6px;}
  .evrow{display:flex;align-items:center;gap:10px;flex-wrap:wrap;font-size:13px;}
  .evlabel{color:#8a8fa3;font-size:12px;flex-basis:230px;}
  .evval{color:#e6e8ef;font-weight:600;}
  .evdesc{color:#6a7190;font-size:11px;width:100%;}
  .exp{margin-top:8px;}
  .exp summary{cursor:pointer;font-size:12px;color:#64b5f6;}
  .detbox{margin-top:8px;background:#1a1e31;border-radius:6px;padding:8px;}
  .detbox table{width:100%;font-size:12px;}
  .warn{color:#ffc75f;}
  .proc{display:inline-flex;align-items:center;gap:6px;background:#222740;border-radius:6px;padding:4px 10px;margin:0 8px 8px 0;font-size:13px;}
  .dot{width:8px;height:8px;border-radius:50%;display:inline-block;}
  .dot.running{background:#5ee89a;box-shadow:0 0 6px #5ee89a;}
  .dot.done{background:#64b5f6;}
  .dot.partial{background:#ffc75f;}
  .dot.idle{background:#5a5f73;}
  .proc .tiptext{color:#8a8fa3;font-size:12px;}
  table{width:100%;border-collapse:collapse;font-size:13px;}
  th,td{text-align:left;padding:6px 8px;border-bottom:1px solid #2a2f47;}
  th{color:#8a8fa3;font-weight:500;}
  .logbox{background:#0d0f1a;border-radius:8px;padding:10px;font-family:ui-monospace,Menlo,monospace;font-size:11px;color:#9fa6c0;max-height:200px;overflow:auto;}
  .logbox div{white-space:pre-wrap;}
  .foot{color:#5a5f73;font-size:12px;text-align:center;margin:20px 0;}
  @media(max-width:600px){.grid{grid-template-columns:repeat(auto-fill,minmax(120px,1fr));}}
</style>
</head>
<body>
<div class="wrap">
  <h1>本地自动研究进度</h1>
  <div class="sub" id="refreshHint">加载中…</div>

  <!-- 数据准备度 -->
  <div class="card">
    <h2>数据准备度</h2>
    <div class="grid" id="dataGrid"></div>
  </div>

  <!-- 进程状态 -->
  <div class="card">
    <h2>进程状态</h2>
    <div id="procBox"></div>
  </div>

  <!-- 自动研究候选 -->
  <div class="card">
    <h2>自动研究候选</h2>
    <div id="candBox"></div>
  </div>

  <!-- 脚本进度 -->
  <div class="card">
    <h2>脚本进度</h2>
    <div id="stepBox"></div>
    <div class="logbox" id="logBox"></div>
  </div>

  <div class="foot">本地进度页 · 30s 自动刷新 · <span id="time"></span></div>
</div>

<script>
async function load() {
  try {
    const r = await fetch('/api/status');
    const s = await r.json();
    render(s);
    document.getElementById('time').textContent = new Date().toLocaleTimeString('zh-CN');
  } catch (e) {
    document.getElementById('refreshHint').textContent = '状态加载失败: ' + e.message;
  }
}

function fmt(v, d) { return (v === null || v === undefined || isNaN(v)) ? (d !== undefined ? d : '-') : v; }

function render(s) {
  const d = s.data_ready || {};
  document.getElementById('refreshHint').textContent =
    '研究库 ' + fmt(d.stocks, '-') + ' 只股票 · 近' + (d.ready_stocks||0) + ' 只有行情' +
    ' · ' + (d.date_min||'-') + ' ~ ' + (d.date_max||'-');

  // 数据准备度
  const dg = document.getElementById('dataGrid');
  const readyPct = d.stocks ? Math.round(d.ready_stocks / d.stocks * 100) : 0;
  const indPct = d.stocks ? Math.round(d.industry / d.stocks * 100) : 0;
  dg.innerHTML = [
    stat('全市场股票', fmt(d.stocks, 0)),
    stat('有行情', fmt(d.ready_stocks, 0) + ' (' + readyPct + '%)'),
    stat('日线行数', numfmt(d.daily_rows)),
    stat('数据区间', (d.date_min||'-') + '~' + (d.date_max||'-')),
    stat('行业覆盖', fmt(d.industry, 0) + ' (' + indPct + '%)'),
    stat('财务指标', numfmt(d.fina)),
    stat('利润表', numfmt(d.income)),
    stat('板块历史', numfmt(d.sector_rows) + '行/' + fmt(d.sector_ind, 0) + '业'),
    stat('候选总数', fmt(d.cand_total, 0)),
    stat('已应用', fmt(d.cand_applied, 0)),
  ].join('');

  // 进程状态
  const pb = document.getElementById('procBox');
  pb.innerHTML = (s.processes||[]).map(p =>
    '<span class="proc"><span class="dot ' + (p.css||'idle') + '"></span>' + p.name +
    '<span class="tiptext">' + esc(p.tip||'') + '</span></span>'
  ).join('') || '<span style="color:#8a8fa3">无进程信息</span>';

  // 候选
  const cb = document.getElementById('candBox');
  const cands = s.candidates || [];
  if (cands.length === 0) {
    cb.innerHTML = '<div style="color:#8a8fa3">暂无候选（尚未运行自动研究，或护栏未通过）</div>';
  } else {
    cb.innerHTML = cands.map(c => renderCand(c)).join('');
  }

  // 脚本步骤
  const sb = document.getElementById('stepBox');
  const sc = s.script || {};
  if (sc.steps && sc.steps.length) {
    sb.innerHTML = '<table><tr><th>步骤</th><th>状态</th></tr>' +
      sc.steps.map(st =>
        '<tr><td>' + st.name + '</td><td>' + stepBadge(st.state) + '</td></tr>'
      ).join('') + '</table>';
  } else {
    sb.innerHTML = '<div style="color:#8a8fa3">未找到脚本日志（' + 'auto_full.log' + '）</div>';
  }
  // 日志尾部
  const lb = document.getElementById('logBox');
  lb.innerHTML = (sc.last_lines||[]).map(l => '<div>' + esc(l) + '</div>').join('');
}

function renderCand(c) {
  const statusBadge = '<span class="badge ' + c.status + '">' + c.status + '</span>';
  const reliable = c.reliable || {};
  const guardBadge = reliable.ok
    ? '<span class="badge guard-ok">✅ 可以试试</span>'
    : '<span class="badge guard-fail">⚠️ 不建议</span>';

  // 因子列表：方向 + 中文名 + (ID) + 权重
  const lines = (c.factor_lines || []).map(f => {
    const dirCls = f.dir < 0 ? 'dir-short' : 'dir-long';
    return '<div class="frow"><span class="' + dirCls + '">' + esc(f.dir_text) + '</span>' +
      '<span class="fname">' + esc(f.name) + '</span>' +
      '<span class="fid">(' + esc(f.id) + ')</span>' +
      '<span class="fdesc">' + esc(f.desc || '') + '</span>' +
      '<span class="fw">' + (f.weight * 100).toFixed(0) + '%权重</span></div>';
  }).join('');

  // 靠谱度证据列表
  const samples = (reliable.samples || []).map(s =>
    '<div class="evrow"><span class="evlabel">' + esc(s.label) + '</span>' +
    '<span class="evval">' + fmt(s.value, '-') + '</span>' +
    '<div class="evdesc">' + esc(s.desc || '') + '</div></div>'
  ).join('');

  // 展开详细（全样本 IR/IC、反推 t、阈值）
  const det = reliable.details || {};
  const detailHtml =
    '<div class="detbox"><table><tr><th>全样本IR</th><th>全样本IC</th><th>反推超额</th><th>反推t</th><th>前瞻</th><th>阈值</th></tr>' +
    '<tr><td>' + fmt(c.ir && c.ir.toFixed(4), '-') + '</td>' +
    '<td>' + fmt(c.ic && c.ic.toFixed(4), '-') + '</td>' +
    '<td>' + fmt(c.gen_excess !== null ? (c.gen_excess * 100).toFixed(1) + '%' : '-', '-') + '</td>' +
    '<td>' + fmt(det.gen_t && det.gen_t.toFixed(2), '-') + '</td>' +
    '<td>' + fmt(c.horizon, '-') + '日</td>' +
    '<td>' + fmt(c.buy_threshold, 70) + '</td></tr></table>' +
    '<div class="reason">' + esc(c.reason || '') + '</div></div>';

  return '<div class="cand">' +
    '<div class="head"><span class="factors">#' + c.id + ' 战法 · ' + c.kind + '</span>' +
      '<span>' + statusBadge + ' ' + guardBadge + '</span></div>' +
    '<div class="flist">' + lines + '</div>' +
    '<div class="play">玩法：' + esc(c.play || '') + '</div>' +
    '<div class="reli"><span class="reliq">这条规律靠谱吗？（电脑验证过）</span>' +
      '<div class="verdict">' + guardBadge + '</div>' +
      '<div class="evlist">' + samples + '</div>' +
      '<details class="exp"><summary>想看具体数字？展开</summary>' + detailHtml + '</details>' +
    '</div>' +
    '</div>';
}

function stepBadge(st) {
  if (st === 'done') return '<span class="badge applied">已完成</span>';
  if (st === 'running') return '<span class="badge proposed">进行中</span>';
  return '<span class="badge rejected">未开始</span>';
}

function stat(k, v) { return '<div class="stat"><div class="k">' + k + '</div><div class="v">' + v + '</div></div>'; }
function numfmt(n) { return (n===null||n===undefined) ? '-' : Number(n).toLocaleString('zh-CN'); }
function esc(s) { return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }

// 首次加载 + 30s 轮询
load();
setInterval(load, 30000);
</script>
</body>
</html>
"""


# ---------------- HTTP 服务 ----------------

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        # 精简访问日志，避免刷屏
        pass

    def _send(self, code, body, ctype):
        data = body.encode("utf-8") if isinstance(body, str) else body
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path
        if path == "/" or path == "/index.html":
            self._send(200, render_html(), "text/html; charset=utf-8")
        elif path == "/api/status":
            status = build_status()
            self._send(200, json.dumps(status, ensure_ascii=False), "application/json; charset=utf-8")
        elif path == "/health":
            self._send(200, "ok", "text/plain")
        else:
            self._send(404, "not found", "text/plain")


def main():
    srv = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print("本地自动研究进度页: http://localhost:%d" % PORT)
    print("研究库: %s" % DB_PATH)
    print("脚本日志: %s" % LOG_PATH)
    print("Ctrl+C 停止")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    srv.server_close()


if __name__ == "__main__":
    main()
