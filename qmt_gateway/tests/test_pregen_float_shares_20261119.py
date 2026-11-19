# -*- coding: utf-8 -*-
"""§TURNOVER-WIRING 回归锁：pregen 的流通股本解析（2026-11-19 owner 要求补烘）。

为什么单独立文件：这段解析只在盘前跑一次（08:35），平时没人看；一旦字段位漂了，
表现是"T 的换手率闸又恒拒"，而不是任何显式报错 —— 用真实报文钉住字段位与两条兜底
（股本缺失用流通市值÷现价推导 / 推导与直读差 >10% 以推导为准 / 全缺就不烘）。
"""

import importlib.util
import os

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
SCRIPT = os.path.join(ROOT, "scripts", "pregen_carrier_factors_gz.py")

# 2026-11-19 现网实录（sz000034，截断到有用字段；顺序与腾讯一致）。
def _payload(price="21.12", circ_mv_yi="179.34", float_shares="849147841", total_shares="1016739663"):
    """按腾讯 88 字段的真实位置造一条报文（只填本解析读的几格，其余留空）。

    不用手打整条字符串：字段位是**位置语义**，手打长度一错，用例测的就是错位后的假数据
    ——2026-11-19 第一版就这么假红过一次。
    """
    fields = [""] * 88
    fields[0] = "51"
    fields[1] = "TEST"
    fields[2] = "000034"
    fields[3] = price
    fields[4] = "23.00"
    fields[5] = "22.80"
    fields[38] = "4.54"
    fields[44] = circ_mv_yi
    fields[45] = "214.74"
    fields[72] = float_shares
    fields[73] = total_shares
    fields[76] = float_shares
    return 'v_sz000034="' + "~".join(fields) + '";'


def load_module():
    spec = importlib.util.spec_from_file_location("pregen_carrier_factors_gz", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_parse_reads_the_float_share_field():
    mod = load_module()
    out = mod.parse_tencent_float_shares(_payload())
    assert out.get("000034.SZ") == 849147841.0


def test_parse_falls_back_to_circ_mv_over_price():
    """[72] 缺失 ⇒ 用 流通市值(亿) × 1e8 ÷ 现价 推导。"""
    mod = load_module()
    out = mod.parse_tencent_float_shares(_payload(float_shares=""))
    # 179.34e8 / 21.12 ≈ 8.49e8
    assert out["000034.SZ"] == 179.34 * 1e8 / 21.12


def test_parse_prefers_derived_value_when_the_field_disagrees():
    """[72] 与 流通市值÷现价 差 >10% ⇒ 认为 [72] 不是股本（板块/指数字段位漂），改用推导。"""
    mod = load_module()
    out = mod.parse_tencent_float_shares(_payload(float_shares="1234"))
    assert out["000034.SZ"] == 179.34 * 1e8 / 21.12


def test_parse_returns_nothing_when_both_legs_are_missing():
    """两条都缺 ⇒ 不烘（载体侧继续弃权；宁可 T 不开，也不拿猜的股本算换手）。"""
    mod = load_module()
    assert mod.parse_tencent_float_shares(_payload(float_shares="", circ_mv_yi="")) == {}


def test_parse_ignores_garbage_and_other_shapes():
    mod = load_module()
    assert mod.parse_tencent_float_shares("") == {}
    assert mod.parse_tencent_float_shares('v_sh600000="1~2~3"') == {}
    assert mod.parse_tencent_float_shares("not a quote") == {}

def test_tencent_code_adds_the_prefix_for_bare_codes():
    """现网踩过的坑：params.symbols 是裸 6 位码，不补前缀腾讯回 v_pv_none_match。

    首次真跑（2026-11-19 14:35）的读数就是 `with_float_shares 0 / 993`——
    请求发成 `q=000034,...`，解析自然为空。这条用例用"裸码 → 带前缀"逐档钉住。
    """
    mod = load_module()
    assert mod._tencent_code("000034") == "sz000034"
    assert mod._tencent_code("300750") == "sz300750"
    assert mod._tencent_code("600000") == "sh600000"
    assert mod._tencent_code("900901") == "sh900901"
    assert mod._tencent_code("430047") == "bj430047"
    assert mod._tencent_code("830799") == "bj830799"
    assert mod._tencent_code("000034.SZ") == "sz000034"
    assert mod._tencent_code("600000.SH") == "sh600000"
    assert mod._tencent_code("430047.BJ") == "bj430047"


def test_fetch_keys_are_bare_codes(monkeypatch):
    """fetch 的返回键必须是裸码：precalc 用裸码取值，带后缀等于整表查不到。"""
    mod = load_module()

    class _Resp(object):
        def __init__(self, payload):
            self._payload = payload

        def read(self):
            return self._payload

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    monkeypatch.setattr(mod.urllib.request, "urlopen",
                        lambda req, timeout=0: _Resp(_payload().encode("gbk")))
    out = mod.fetch_float_shares(["000034", "600000"])
    assert out.get("000034") == 849147841.0
    assert "000034.SZ" not in out

