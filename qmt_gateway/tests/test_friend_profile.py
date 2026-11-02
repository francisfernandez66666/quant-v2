#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§FRIEND-PROFILE（2026-11-02 多账号 B-α 批 1）回归锁：朋友机器档的出厂自证。

钉住的事（每条都对应一种"链路照跑、账错到别人名下"的真钱形状，不是理论风险）：
  ① **缺省档一字不做**：`deploy_profile` 不存在或为空 ⇒ 不校验、不退出，现网那台的启动
     行为与今天逐字节一致（本文件第一组用例就是这条 equals-legacy 锁）；
  ② 一台网关只服务一个资金账号，且账户级事件（持仓/资金）必须归这台机器的主人
     ——漏了就是 10-30 的形态：七账号一把钥匙 ⇒ 回报按 users 顺序恒归 admin；
  ③ 主备翻转必须关：朋友的 miniQMT 通道券商已停申/下线，翻上去每次 connect() 占一个
     writer 槽、槽满拖崩客户端（10-29 实测 713 连败、3~6 分钟一崩）；
  ④ 回报只许发往决策机；账本与文件桥目录只许在写死的本地盘根下（共享盘上的坏法全在下行且静默）；
  ⑤ 行情通道必须关（远端只承担"信号接收 + 交易数据交互"，券商 L1 钥匙只在决策机那一路）；
  ⑥ 违规是**拒绝启动**（SystemExit 2），不是 warning——Gateway() 根本不会被构造。
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import gateway as gw_mod  # noqa: E402
from tests.test_manual_sync import new_db_path  # noqa: E402

UID = "u_friend_001"
TK = "tk-friend-001"


def friend_cfg(**over):
    """构造一份"应当全过"的朋友机器配置；用 over 逐项破坏，验证每条校验各自会红。

    刻意从 gw_mod.DEFAULT_CONFIG 拷起步（而不是手抄字段）：默认值一改这里就跟着改，
    避免测试与被测对象的缺省值漂成两份——那是 §INDEX-BLIND 那批"两份段序表"的同形教训。
    """
    cfg = dict(gw_mod.DEFAULT_CONFIG)
    cfg.update({
        "deploy_profile": "friend",
        "broker": "queued",
        "account": "2069008957",
        "user_id": UID,
        "owner_user_id": UID,
        "token": TK,
        "account_tokens": [{"user_id": UID, "token": TK}],
        "report_url": "https://quant-trading.top",
        "report_token": TK,
        "db": "C:\\qmt\\quant-trading-v2\\qmt_gateway\\data.db",
        "listen": "10.77.0.5:8789",
        "quotes_enabled": False,
        "failover_enable": False,
    })
    cfg.update(over)
    return cfg


def enforce_kwargs(**over):
    """enforce/validate 的两个环境入参：合法形态是"显式绑过地址 + 白名单非空"。"""
    kw = {"allowed_ips": ["10.77.0.1"], "bind_explicit": True}
    kw.update(over)
    return kw


class TestDefaultProfileIsByteToday(unittest.TestCase):
    """缺省档（现网运营机）：一条校验都不做。这是本批最重要的那条 equals-legacy 锁。"""

    def test_absent_profile_key_does_nothing(self):
        # 现网真实形状：broker=xt、listen=0.0.0.0、failover 开、quotes_enabled 未配、
        # 只有 legacy token 没有 account_tokens —— 每一项单拎出来都是 friend 档的违规，
        # 但 deploy_profile 缺失时必须**全都不判**，否则现网重启直接起不来。
        cfg = dict(gw_mod.DEFAULT_CONFIG)
        cfg.update({"broker": "xt", "listen": "0.0.0.0:8789", "failover_enable": True,
                    "account": "2069008957", "token": "legacy-only"})
        gw_mod.enforce_friend_profile(cfg, **enforce_kwargs(allowed_ips=[], bind_explicit=False))

    def test_empty_profile_string_does_nothing(self):
        cfg = friend_cfg(deploy_profile="")
        gw_mod.enforce_friend_profile(cfg, **enforce_kwargs(allowed_ips=[], bind_explicit=False))

    def test_canonical_friend_cfg_passes(self):
        self.assertEqual(gw_mod.validate_friend_profile(friend_cfg(), **enforce_kwargs()), [])


class TestAttributionLocks(unittest.TestCase):
    """归属四项：一把钥匙、一个主人、两个 user_id 口径同值、不许两把不同钥匙。"""

    def test_rejects_two_tokens(self):
        bad = friend_cfg(account_tokens=[{"user_id": UID, "token": TK},
                                         {"user_id": "u_other", "token": "tk-other"}])
        self.assertTrue(any("account_tokens" in x for x in
                            gw_mod.validate_friend_profile(bad, **enforce_kwargs())))

    def test_rejects_token_owner_mismatch(self):
        bad = friend_cfg(account_tokens=[{"user_id": "u_someone_else", "token": TK}])
        self.assertTrue(any("owner_user_id" in x for x in
                            gw_mod.validate_friend_profile(bad, **enforce_kwargs())))

    def test_rejects_missing_owner(self):
        bad = friend_cfg(owner_user_id="", user_id="")
        self.assertTrue(any("owner_user_id" in x for x in
                            gw_mod.validate_friend_profile(bad, **enforce_kwargs())))

    def test_rejects_user_id_diverging_from_owner(self):
        # 两个归属口径并存时，落库走 user_id、账户级事件走 owner_user_id ⇒ 账分叉
        bad = friend_cfg(user_id="u_another")
        self.assertTrue(any("user_id 与 owner_user_id" in x for x in
                            gw_mod.validate_friend_profile(bad, **enforce_kwargs())))

    def test_rejects_legacy_token_differing(self):
        # legacy token 会经 setdefault 也进注册表 ⇒ 在册两把钥匙＝一台机器两个身份
        bad = friend_cfg(token="tk-second-identity")
        self.assertTrue(any("legacy token" in x for x in
                            gw_mod.validate_friend_profile(bad, **enforce_kwargs())))


class TestChannelAndHostLocks(unittest.TestCase):
    """通道/主机/目录/监听/行情：远端形态特有的五道闸。"""

    def test_rejects_failover_on(self):
        bad = friend_cfg(failover_enable=True)
        self.assertTrue(any("failover_enable" in x for x in
                            gw_mod.validate_friend_profile(bad, **enforce_kwargs())))

    def test_rejects_broker_xt(self):
        bad = friend_cfg(broker="xt")
        self.assertTrue(any("broker 必须为 queued" in x for x in
                            gw_mod.validate_friend_profile(bad, **enforce_kwargs())))

    def test_rejects_foreign_report_url(self):
        bad = friend_cfg(report_url="https://someone-elses-collector.example")
        self.assertTrue(any("report_url" in x for x in
                            gw_mod.validate_friend_profile(bad, **enforce_kwargs())))

    def test_rejects_unc_and_out_of_root_db(self):
        # 共享盘（UNC）与白名单外路径两种都拒；正向白名单而不是反向排除——
        # Windows 下映射盘符与本地盘符同形，反排根本判不准
        for path in ("\\\\nas\\quant\\data.db", "D:\\other\\data.db"):
            bad = friend_cfg(db=path)
            self.assertTrue(any("friend_allowed_roots" in x for x in
                                gw_mod.validate_friend_profile(bad, **enforce_kwargs())),
                            "路径未拒：%s" % path)

    def test_rejects_wildcard_listen_and_missing_bind_and_empty_allowlist(self):
        bad = friend_cfg(listen="0.0.0.0:8789")
        v = gw_mod.validate_friend_profile(bad, **enforce_kwargs())
        self.assertTrue(any("listen" in x for x in v))
        # bind 未显式指定 ⇒ main 会把监听收敛回 127.0.0.1：决策机进不来，
        # 而这台机器照常上报 ⇒ 页面看起来"账是新鲜的"，实际发不出单
        self.assertTrue(any("QUANT_GATEWAY_BIND" in x for x in
                            gw_mod.validate_friend_profile(friend_cfg(), **enforce_kwargs(bind_explicit=False))))
        self.assertTrue(any("ALLOWED_IPS" in x for x in
                            gw_mod.validate_friend_profile(friend_cfg(), **enforce_kwargs(allowed_ips=[]))))

    def test_rejects_quotes_enabled_true(self):
        bad = friend_cfg(quotes_enabled=True)
        self.assertTrue(any("quotes_enabled" in x for x in
                            gw_mod.validate_friend_profile(bad, **enforce_kwargs())))

    def test_rejects_loopback_listen(self):
        # 回环监听是一条"上报照常、下单进不来"的半条命通道：朋友机器照常发成交，
        # 而 Q3 要求的"急停/清场/超时撤单覆盖远端"结构上做不到 —— 必须在这里拒
        for host in ("127.0.0.1", "::1", "localhost"):
            v = gw_mod.validate_friend_profile(friend_cfg(listen="%s:8789" % host), **enforce_kwargs())
            self.assertTrue(any("回环" in x for x in v), "回环地址未拒：%s" % host)


class _NoGatewayWasBuilt(Exception):
    """哨兵异常：装配真源一旦被调用就炸——用来证明"违规路径根本到不了 Gateway()"。"""


def _sentinel_gateway(*_a, **_kw):
    raise _NoGatewayWasBuilt()


class TestEnforceIsFatal(unittest.TestCase):
    """违规必须是"起不来"，不能是"起来了再吵"。"""

    def test_enforce_exits_with_code_2(self):
        bad = friend_cfg(broker="xt")
        try:
            gw_mod.enforce_friend_profile(bad, **enforce_kwargs())
        except SystemExit as e:  # noqa: PERF203 — 这里就是要它抛
            self.assertEqual(e.code, 2)
            return
        self.fail("friend 档违规时 enforce 必须 SystemExit(2)，不能只打日志继续跑")

    def test_enforce_before_gateway_construction_leaves_no_half_state(self):
        # 装配顺序锁：enforce 放在 Gateway() 之前 ⇒ 违规时 SQLite/线程/订阅一个都没开。
        # 断言方式是"把 Gateway 换成哨兵"而不是"查文件在不在"：
        # 查文件只能证明没建库，证明不了没起线程/没建订阅——那才是"半态在跑"的实体。
        cfg = friend_cfg(broker="mock")
        orig = gw_mod.Gateway
        gw_mod.Gateway = _sentinel_gateway
        try:
            with self.assertRaises(SystemExit):
                gw_mod.enforce_friend_profile(cfg, **enforce_kwargs())
        finally:
            gw_mod.Gateway = orig



class TestQuotesChannelGated(unittest.TestCase):
    """行为腿：quotes_enabled=False 时 /quotes 必须回 403，而不是"空 ticks + 200"。

    空 ticks+200 在今天的语义里是"feed 还没通"的正常态；拿它表达"这台机器不该供行情"
    会把两种完全不同的事实折成同一个读数（决策机兜底腿会误判成"再等等就好"）。
    """

    def _gw(self, **over):
        cfg = friend_cfg(**over)
        cfg["db"] = new_db_path()
        return gw_mod.Gateway(cfg)

    def test_disabled_returns_403(self):
        gw = self._gw()
        status, payload = gw._do_quotes()
        self.assertEqual(status, 403)
        self.assertEqual(payload.get("err"), "quotes_disabled_by_profile")

    def test_enabled_keeps_today_shape_empty_ticks_200(self):
        # 运营/缺省档：不配这个键就照今天应答（带 codes 查询 ⇒ 200 + ticks 字段，
        # feed 没通时 ticks 为空 dict 而不是 4xx）。400 那条是"codes 必填"的既有语义，
        # 本用例必须带 codes，否则测的是参数校验而不是通道开关。
        class _Req:  # 只带 _do_quotes 真正读取的 path 属性
            path = "/quotes?codes=600519.SH"

        gw = self._gw(quotes_enabled=True, deploy_profile="")
        status, payload = gw._do_quotes(_Req())
        self.assertEqual(status, 200)
        self.assertIn("ticks", payload)


class TestProfileCheckCLI(unittest.TestCase):
    """`--profile-check` 的行为腿：装填器/看门狗据此在**起服务之前**判一次，且判据真源只有一份。

    两条都要钉：① 通过时打纯 ASCII 锚点行（中文经 ssh 管道会被码页换成 `?`，锚点判读会恒假）；
    ② 违规时退出码 2 —— 并且**绝不碰账本文件**（早退在 Gateway() 构造之前）。
    """

    def _run(self, cfg):
        import json
        import tempfile
        from contextlib import redirect_stdout
        import io
        d = tempfile.mkdtemp(prefix="friendprofile_")
        path = os.path.join(d, "config.xt.json")
        with open(path, "w", encoding="utf-8") as f:
            json.dump(cfg, f)
        old = (os.environ.get("QUANT_GATEWAY_BIND"), os.environ.get("ALLOWED_IPS"))
        os.environ["QUANT_GATEWAY_BIND"] = cfg["listen"]
        os.environ["ALLOWED_IPS"] = "10.77.0.1"
        buf = io.StringIO()
        try:
            with redirect_stdout(buf):
                gw_mod.main(["-c", path, "--profile-check"])
            return buf.getvalue(), d, path, old
        finally:
            for k, v in zip(("QUANT_GATEWAY_BIND", "ALLOWED_IPS"), old):
                if v is None:
                    os.environ.pop(k, None)
                else:
                    os.environ[k] = v

    def test_pass_prints_ascii_anchor_and_exits_before_assembly(self):
        # 通过时也必须"打完锚点就退"：profile-check 是给装填器用的，
        # 一旦它顺带把网关起来了，装填流程就变成"检查即上线"——半态在跑的另一种形态。
        cfg = friend_cfg()
        orig = gw_mod.Gateway
        gw_mod.Gateway = _sentinel_gateway
        try:
            out, _d, _path, _old = self._run(cfg)
        finally:
            gw_mod.Gateway = orig
        self.assertIn("PROFILE_CHECK=PASS", out)
        # 锚点必须纯 ASCII：PowerShell 管道里中文会被码页换成 `?`（11-02 现网两次假象同源），
        # 一旦锚点带中文，ps1 的判读会**永远不匹配**而装填照样继续往下走。
        self.assertTrue(all(ord(c) < 128 for c in out), "profile-check 锚点含非 ASCII 字符")

    def test_violations_exit_code_2(self):
        # 回环监听的形态：QUANT_GATEWAY_BIND 会把 cfg["listen"] 原样覆盖进来，
        # "以为绑在 overlay 上、其实只监听 127.0.0.1" ⇒ 上报照常、下单进不来
        cfg = friend_cfg(listen="127.0.0.1:8789")
        with self.assertRaises(SystemExit) as ctx:
            self._run(cfg)
        self.assertEqual(ctx.exception.code, 2)


if __name__ == "__main__":
    unittest.main()
