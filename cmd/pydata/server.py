#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""baostock 主数据源 + akshare 降级 的本地 HTTP sidecar（B0 研究链路数据服务）。

用途：给 Go 侧 cmd/dataload 提供 A 股历史数据（日线/复权因子/估值/ST/财务季频/交易日历/指数）。
baostock 免费免注册，日线一条查询即含 open/high/low/close/volume/amount/turn(换手)/
tradestatus(停牌)/peTTM/pbMRQ/psTTM/pcfNcfTTM/isST；前后复权由 adjust_factor 接口给出。
baostock 单账号同一时间只能开一个连接且不支持多线程 → 全部请求用 _bs_lock 串行化。
akshare 作降级：仅覆盖 交易日历/股票列表/日线(新浪源, 避开东财)；财务降级暂不做（注明限制）。

运行：python3 cmd/pydata/server.py [--host 127.0.0.1] [--port 8787]
依赖：pip install baostock akshare pandas   （见 cmd/pydata/requirements.txt）
"""
import argparse
import concurrent.futures
import csv
import datetime
import io
import json
import logging
import os
import socket
import sys
import threading
import time
import traceback

# baostock 单查询可能永久挂起（个别代码无响应，且是长连接，socket.setdefaulttimeout 只对
# 新建 socket 生效，拦不住已建立连接上的阻塞读）。查询外层再做线程级超时：
# 超时即视为查询失败并 relogin 踢掉卡死连接，保证 sidecar 永不被单个股票冻结。
# （English: a single baostock query may hang forever on a long-lived connection; the default
# socket timeout only affects new sockets. A per-query thread timeout plus relogin on expiry
# keeps the sidecar from ever being frozen by one stock.）
_QUERY_TIMEOUT = 30
socket.setdefaulttimeout(_QUERY_TIMEOUT)
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

import baostock as bs

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("pydata")

# baostock 单连接限制 → 全局串行锁（English: baostock allows one connection per account,
# so all requests are serialized behind a single lock.)
_bs_lock = threading.Lock()


def _bs_call(fn, *args, **kwargs):
    """在独立守护线程里执行一次 baostock 调用，返回 future。

    每次查询新建线程（不复用），超时后可丢弃而不阻塞后续查询；线程为 daemon，
    卡死的线程随进程退出，不影响 sidecar 继续服务。
    """
    fut = concurrent.futures.Future()

    def _run():
        try:
            fut.set_result(fn(*args, **kwargs))
        except BaseException as e:  # noqa: BLE001
            fut.set_exception(e)

    threading.Thread(target=_run, daemon=True).start()
    return fut

# 股票日线查询字段（含估值/换手/停牌/ST），adjustflag=3 不复权时可用。
# 注意：baostock 无 change 字段，涨跌幅由 Go 侧用 close-preclose 计算。
_STOCK_FIELDS = ("date,code,open,high,low,close,preclose,volume,amount,adjustflag,"
                 "turn,tradestatus,pctChg,peTTM,pbMRQ,psTTM,pcfNcfTTM,isST")
# 指数日线查询字段（无估值/ST）
_INDEX_FIELDS = ("date,code,open,high,low,close,preclose,volume,amount,adjustflag,turn,tradestatus,pctChg")


def _bs_query(fn, *args, **kwargs):
    """在串行锁内执行一次 baostock 查询，返回 (rows, fields)。

    健壮性：baostock 免费账号单连接长时间高频请求后，连接会失效（错误码 10002007
    网络接收错误 / 10002009 等）。遇到此类网络错误时自动重新 login 并重试一次，
    使 sidecar 在高频装载（dataload）期间能自愈，避免整批中断。
    另：每次查询以 _QUERY_TIMEOUT 为上限，超时抛错（上层重试/跳过），不冻结 sidecar。
    """
    with _bs_lock:
        try:
            rs = _bs_call(fn, *args, **kwargs).result(timeout=_QUERY_TIMEOUT)
        except concurrent.futures.TimeoutError:
            _bs_relogin()  # 踢掉卡死连接，僵尸线程随连接关闭退出
            raise RuntimeError("baostock query timeout (%ds)" % _QUERY_TIMEOUT)
        if rs.error_code != "0":
            # 网络/连接类错误：重新 login 后重试一次
            if rs.error_code in ("10002007", "10002009", "10001001", "10002013"):
                _bs_relogin()
                try:
                    rs = _bs_call(fn, *args, **kwargs).result(timeout=_QUERY_TIMEOUT)
                except concurrent.futures.TimeoutError:
                    _bs_relogin()
                    raise RuntimeError("baostock query timeout (%ds)" % _QUERY_TIMEOUT)
            if rs.error_code != "0":
                raise RuntimeError("baostock error %s: %s" % (rs.error_code, rs.error_msg))
        rows = []
        while rs.error_code == "0" and rs.next():
            rows.append(rs.get_row_data())
        return rows, list(rs.fields)


# 网络错误后自动重连的连续重连计数（防止连接彻底坏死时无限重试刷屏）
_bs_relogin_count = 0
_bs_relogin_lock = threading.Lock()


def _bs_login():
    """登录 baostock：优先用环境变量 BAOSTOCK_USER/BAOSTOCK_PASS 指定账号密码，
    未配置时保持匿名登录（兼容原匿名使用方式）。换账号/被限流时通过注入新凭据即换身份。
    （English: log in to baostock — use BAOSTOCK_USER/BAOSTOCK_PASS env credentials when set,
    otherwise fall back to anonymous login. Rotating the account is a matter of updating the env.）"""
    user = os.environ.get("BAOSTOCK_USER", "")
    pwd = os.environ.get("BAOSTOCK_PASS", "")
    if user and pwd:
        return bs.login(user_id=user, password=pwd)
    return bs.login()


def _bs_relogin():
    """重新登录 baostock（带节流：两次重连之间至少间隔 1s）。"""
    global _bs_relogin_count
    with _bs_relogin_lock:
        try:
            bs.logout()
        except Exception:
            pass
        time.sleep(1)
        lg = _bs_login()
        _bs_relogin_count += 1
        if lg.error_code == "0":
            log.warning("baostock 连接失效，已自动重连（第 %d 次）", _bs_relogin_count)
        else:
            log.warning("baostock 自动重连失败: %s %s", lg.error_code, lg.error_msg)


def _to_csv(cols, rows):
    """rows(list[list]) 按 cols 序列化为 CSV 文本。"""
    buf = io.StringIO()
    w = csv.writer(buf)
    w.writerow(cols)
    for r in rows:
        w.writerow(r)
    return buf.getvalue()


def _try_ak(fn, *args):
    """akshare 降级封装：失败抛异常由上层统一返回 error: 前缀。"""
    try:
        import akshare as ak
        return fn(ak, *args)
    except Exception as e:
        raise RuntimeError("akshare fallback failed: %s" % e)


# ---------------- 路由实现 ----------------

def r_health(params):
    """健康检查：返回固定 "ok"，供 Go 侧探测 sidecar 是否存活。"""
    return "ok"


def r_trade_days(params):
    start, end = params.get("start", "2020-01-01"), params.get("end", "2026-12-31")
    try:
        rows, fields = _bs_query(bs.query_trade_dates, start_date=start, end_date=end)
        return _to_csv(["calendar_date", "is_open"], rows)
    except Exception:
        # 降级：新浪交易日历（只含交易日，is_open 恒 1）
        df = _try_ak(lambda ak: ak.tool_trade_date_hist_sina())
        rows = [[str(r).replace("-", ""), "1"] for r in df["trade_date"].tolist()]
        return _to_csv(["calendar_date", "is_open"], rows)


def r_all_stock(params):
    try:
        day = params.get("day") or _recent_weekday()
        rows, fields = _bs_query(bs.query_all_stock, day=day)
        if not rows:
            raise RuntimeError("query_all_stock empty on %s" % day)
        return _to_csv(fields, rows)
    except Exception:
        df = _try_ak(lambda ak: ak.stock_info_a_code_name())
        rows = [[str(r["code"]), str(r["name"]), "1"] for _, r in df.iterrows()]
        return _to_csv(["code", "code_name", "tradeStatus"], rows)


def _recent_weekday():
    """返回最近的工作日（baostock 按交易日返回股票列表，周末/节假日为空）。"""
    d = datetime.date.today()
    for _ in range(15):
        if d.weekday() < 5:
            return d.strftime("%Y-%m-%d")
        d -= datetime.timedelta(days=1)
    return d.strftime("%Y-%m-%d")


def r_stock_basic(params):
    """查询个股基础信息（代码/名称/上市状态等），与 Tushare stock_basic 对齐。"""
    code = params.get("code", "")
    rows, fields = _bs_query(bs.query_stock_basic, code=code, code_name="")
    return _to_csv(fields, rows)


def _kline_impl(params, index=False):
    code = params.get("code", "")
    start, end = params.get("start", ""), params.get("end", "")
    adj = params.get("adjust", "3")  # 1后复权 2前复权 3不复权
    fields = _INDEX_FIELDS if index else _STOCK_FIELDS
    rows, _ = _bs_query(bs.query_history_k_data_plus, code, fields,
                        start_date=start, end_date=end, frequency="d", adjustflag=adj)
    return _to_csv(fields.split(","), rows)


def r_kline(params):
    try:
        return _kline_impl(params, index=False)
    except Exception:
        # 降级：新浪日线（无估值/换手/ST，空列补齐），返回同样的列结构
        code, start, end = params.get("code", ""), params.get("start", ""), params.get("end", "")
        df = _try_ak(lambda ak: ak.stock_zh_a_daily(symbol=code.replace(".", ""), start_date=start, end_date=end, adjust=""))
        df = df.sort_values("date")
        cols = _STOCK_FIELDS.split(",")
        rows = []
        for _, r in df.iterrows():
            rows.append([
                str(r["date"]).replace("-", ""), code,
                r.get("open", ""), r.get("high", ""), r.get("low", ""), r.get("close", ""),
                "", r.get("volume", ""), r.get("amount", ""), "3", "", "1", "", "", "", "", "", "",
            ])
        return _to_csv(cols, rows)


def r_index_kline(params):
    """指数日线（无估值/ST 字段），由 _kline_impl 以指数模式执行。"""
    return _kline_impl(params, index=True)


def r_adjust_factor(params):
    """复权因子（前后复权系数），Go 侧用它计算前/后复权价格。"""
    code, start, end = params.get("code", ""), params.get("start", ""), params.get("end", "")
    rows, fields = _bs_query(bs.query_adjust_factor, code=code, start_date=start, end_date=end)
    return _to_csv(fields, rows)


def _fina(params, fn, name):
    code, year, quarter = params.get("code", ""), params.get("year", ""), params.get("quarter", "")
    if not (code and year and quarter):
        raise ValueError("finance 接口需要 code/year/quarter")
    try:
        rows, fields = _bs_query(fn, code=code, year=int(year), quarter=int(quarter))
        return _to_csv(fields, rows)
    except Exception as e:
        # 财务暂不做 akshare 降级（akshare 财务接口为爬虫、易变），直接抛错
        raise RuntimeError("%s 无降级: %s" % (name, e))


def r_profit(params):
    return _fina(params, bs.query_profit_data, "profit")


def r_growth(params):
    return _fina(params, bs.query_growth_data, "growth")


def r_balance(params):
    return _fina(params, bs.query_balance_data, "balance")


def r_cashflow(params):
    return _fina(params, bs.query_cash_flow_data, "cashflow")


def r_dividend(params):
    """分红送配数据：year 必填，year_type 默认按报告期（report）查询。"""
    code, year = params.get("code", ""), params.get("year", "")
    year_type = params.get("year_type", "report")  # baostock 参数名为 yearType
    rows, fields = _bs_query(bs.query_dividend_data, code=code, year=year, yearType=year_type)
    return _to_csv(fields, rows)


_ROUTES = {
    "health": r_health,
    "trade_days": r_trade_days,
    "all_stock": r_all_stock,
    "stock_basic": r_stock_basic,
    "kline": r_kline,
    "index_kline": r_index_kline,
    "adjust_factor": r_adjust_factor,
    "profit": r_profit,
    "growth": r_growth,
    "balance": r_balance,
    "cashflow": r_cashflow,
    "dividend": r_dividend,
}


class _Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):  # 精简 access log
        log.info("req " + fmt % args)

    def do_GET(self):
        """处理 GET 请求：按路径路由到对应业务函数，业务错误以 error: 前缀返回 200。
        未识别的路由返回 404。"""
        u = urlparse(self.path)
        name = u.path.strip("/") or "health"
        params = {k: v[0] for k, v in parse_qs(u.query).items()}
        try:
            fn = _ROUTES.get(name)
            if fn is None:
                self._send(404, "error: unknown method %s" % name)
                return
            body = fn(params)
            self._send(200, body, "text/csv; charset=utf-8")
        except Exception as e:
            log.warning("method=%s error: %s", name, e)
            self._send(200, "error: %s" % e)  # 与 Tushare 风格一致：业务错误以 error: 前缀返回

    def _send(self, code, body, ctype="text/plain; charset=utf-8"):
        """写入 HTTP 响应：设置状态码/Content-Type/Content-Length 后输出 body。"""
        data = body.encode("utf-8") if isinstance(body, str) else body
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=8787)
    args = ap.parse_args()

    # 启动即登录（可用 BAOSTOCK_USER/BAOSTOCK_PASS 换账号），失败仅告警：请求时仍会再次报错
    # （English: log in at startup — credentials via env, else anonymous; failure only warns,
    # requests will surface errors again.）
    lg = _bs_login()
    if lg.error_code == "0":
        log.info("baostock login ok")
    else:
        log.warning("baostock login failed: %s %s", lg.error_code, lg.error_msg)

    srv = ThreadingHTTPServer((args.host, args.port), _Handler)
    log.info("pydata sidecar listening on %s:%d", args.host, args.port)
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        bs.logout()
        log.info("pydata sidecar exited")


if __name__ == "__main__":
    main()
