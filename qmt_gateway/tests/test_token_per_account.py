#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§TOKEN-PERAACCT（2026-10-30）回归锁：实盘凭证从"一把共享钥匙"改为"一账号一把"。

钉住的事（全部来自当天现网盘查的因果链）：
  ① 入向鉴权认的是**钥匙集合**，每把钥匙属于一个账号；解不出账号的请求一律 401；
  ② 老配置（只写 token/user_id、没有 account_tokens）行为逐字节不变——绝不允许出现
     "配置改了但注册表是空的"那种七个账号一起 401 的半态；
  ③ 下单行/派发行落**真实下单账号**，事件里的自报 user_id 一律被入向钥匙覆盖（不采信客户端自报）；
  ④ 出向回报按事件归属账号选钥匙，取不到就回退默认钥匙并计数——**扣着不发是丢账**；
  ⑤ /orders 对账源按调用方收窄，无归属行（手动单/历史）对所有人仍可见。
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import gateway as gw_mod  # noqa: E402
from store import Store  # noqa: E402
from tests.test_manual_sync import new_db_path  # noqa: E402

OWNER = "u_owner"
MEMBER = "u_member"
TK_OWNER = "tk-owner"
TK_MEMBER = "tk-member"


def _cfg(**extra):
    cfg = {
        "listen": "127.0.0.1:0", "token": TK_OWNER, "broker": "mock", "account": "T0001",
        "db": new_db_path(), "report_url": "http://127.0.0.1:1", "report_token": TK_OWNER,
        "reconcile_sec": 0, "failover_enable": False, "user_id": OWNER,
    }
    cfg.update(extra)
    return cfg


class _FakeReq:
    """最小替身：_auth_caller 只读 headers 与 gateway 两个属性。"""

    def __init__(self, auth):
        self.headers = {"Authorization": auth}
        self.path = "/orders"


def _caller(gw, auth):
    fake = _FakeReq(auth)
    fake.gateway = gw
    return gw_mod._Handler._auth_caller(fake)


class TestRegistry(unittest.TestCase):
    def test_legacy_single_token_still_works(self):
        # ② 老配置：没有 account_tokens 时，那把 legacy token 映射到 owner——与今天完全同形
        gw = gw_mod.Gateway(_cfg())
        self.assertEqual(gw._auth_tokens, {TK_OWNER: OWNER})
        self.assertEqual(gw.owner_user_id, OWNER)
        self.assertEqual(_caller(gw, "Bearer " + TK_OWNER), (True, OWNER))
        self.assertEqual(_caller(gw, "Bearer nope"), (False, ""))
        self.assertEqual(_caller(gw, ""), (False, ""))

    def test_per_account_tokens(self):
        gw = gw_mod.Gateway(_cfg(account_tokens=[
            {"user_id": OWNER, "token": TK_OWNER},
            {"user_id": MEMBER, "token": TK_MEMBER},
        ]))
        self.assertEqual(_caller(gw, "Bearer " + TK_MEMBER), (True, MEMBER))
        self.assertEqual(_caller(gw, "Bearer " + TK_OWNER), (True, OWNER))
        self.assertEqual(gw._tokens_by_uid[MEMBER], TK_MEMBER)

    def test_one_token_cannot_belong_to_two_accounts(self):
        # ① 一把钥匙两个主人 = 归属不可判定：后到的那条丢弃，先登记的保留
        gw = gw_mod.Gateway(_cfg(account_tokens=[
            {"user_id": MEMBER, "token": TK_MEMBER},
            {"user_id": "u_third", "token": TK_MEMBER},
        ]))
        self.assertEqual(gw._auth_tokens[TK_MEMBER], MEMBER)

    def test_broken_entries_skipped(self):
        gw = gw_mod.Gateway(_cfg(account_tokens=[
            "not-a-dict", {"token": ""}, {"user_id": ""}, {"user_id": MEMBER, "token": TK_MEMBER},
        ]))
        self.assertEqual(gw._auth_tokens.get(TK_MEMBER), MEMBER)
        self.assertEqual(len(gw._auth_tokens), 2)  # + legacy owner 那把


class TestInboundAttribution(unittest.TestCase):
    def setUp(self):
        self.gw = gw_mod.Gateway(_cfg(account_tokens=[
            {"user_id": OWNER, "token": TK_OWNER},
            {"user_id": MEMBER, "token": TK_MEMBER},
        ]))

    def _place(self, uid, sid, created="2026-10-30 09:30:00"):
        return self.gw._do_order({"code": "600000.SH", "side": "买入", "qty": 100,
                                  "price": 10.0, "signal_id": sid,
                                  "created_at": created}, caller_uid=uid)

    def test_order_row_belongs_to_placing_account(self):
        # ③ 谁下的单落谁名下（此前所有人都落网关配置里那一个 user_id）
        self._place(MEMBER, "buy:600000:fac_1:20261030")
        row = self.gw.store.order_by_signal("buy:600000:fac_1:20261030")
        self.assertEqual(row["user_id"], MEMBER)

    def test_body_self_declared_uid_is_overwritten(self):
        # ③ 客户端自报归属不可信：入向钥匙才是权威
        self.gw._do_order({"code": "600000.SH", "side": "买入", "qty": 100, "price": 10.0,
                           "signal_id": "buy:600001:fac_1:20261030", "user_id": OWNER},
                          caller_uid=MEMBER)
        row = self.gw.store.order_by_signal("buy:600001:fac_1:20261030")
        self.assertEqual(row["user_id"], MEMBER)

    def test_orders_endpoint_scoped_by_caller(self):
        # ⑤ 对账源按调用方收窄，但无归属行不许凭空消失
        # 日期一律取"今天"再拼 created_at：跨零点跑测试时，写死 2026-10-30 的行会被 day 过滤掉，
        # 断言就成了假失败（2026-10-31 00:0x 实测就是这样）。
        day = gw_mod._now_cn()[:10]
        sid_mine = "buy:600002:fac_1:" + day.replace("-", "")
        self._place(MEMBER, sid_mine, created=day + " 09:30:00")
        self.gw.store.upsert_order({"order_id": "x1", "signal_id": "ext:x1", "code": "600003.SH",
                                    "side": "买入", "status": "已报", "price": 1, "qty": 100,
                                    "created_at": day + " 09:31:00", "user_id": ""})

        class _R:
            def __init__(self, uid):
                self.path = "/orders?day=" + day
                self._caller_uid = uid

        mine = self.gw._do_orders(_R(MEMBER))[1]["orders"]
        self.assertEqual({o["signal_id"] for o in mine}, {sid_mine, "ext:x1"})
        theirs = self.gw._do_orders(_R(OWNER))[1]["orders"]
        self.assertEqual({o["signal_id"] for o in theirs}, {"ext:x1"})
        # owner 显式全量视角
        class _RA:
            path = "/orders?day=" + day + "&scope=all"
            _caller_uid = OWNER

        self.assertIn(sid_mine,
                      [o["signal_id"] for o in self.gw._do_orders(_RA())[1]["orders"]])
        # 非 owner 带 scope=all 不生效：它看到的仍只是自己那批 + 无归属行
        class _RM:
            path = "/orders?day=" + day + "&scope=all"
            _caller_uid = MEMBER

        scoped = self.gw._do_orders(_RM())[1]["orders"]
        self.assertEqual({o["signal_id"] for o in scoped}, {sid_mine, "ext:x1"})


class TestDispatchAttribution(unittest.TestCase):
    """派发行落**下单账号**（§TOKEN-PERAACCT）：桥回报回来时只能靠这一列归因。

    为什么单独测 queued 通道：`/order` 的 orders 行归属已经在
    TestInboundAttribution 里钉住，但真正交付给桥的是 **dispatch 行**——
    `store.dispatch_enqueue_order(req, user_id=...)` 此前恒写通道级配置账号，
    于是"谁下的单"在派发这一层是丢掉的（10-30 的错归属就是从这里开始的）。
    """

    def setUp(self):
        from broker import QueuedBroker  # 与 gateway 同一 store 语义，避免起整条网关
        self.store = Store(new_db_path())
        self.broker = QueuedBroker(self.store, account="2069008957", user_id=OWNER)

    def _place(self, req):
        ok, ref, err = self.broker.place_order(req)
        self.assertTrue(ok, err)
        return ref

    def test_dispatch_row_uses_caller_uid(self):
        self._place({"code": "600000.SH", "side": "买入", "qty": 100, "price": 10.0,
                     "signal_id": "buy:600000:fac_9:20261031", "user_id": MEMBER})
        row = self.store.dispatch_by_signal_id("buy:600000:fac_9:20261031")
        self.assertEqual(row["user_id"], MEMBER, "派发行必须写着入向钥匙解出的下单账号")

    def test_dispatch_row_falls_back_to_channel_uid(self):
        # 没带 caller（旧调用形态/别的客户端）时回退通道级账号——行为与今天一致，不许变空
        self._place({"code": "600001.SH", "side": "买入", "qty": 100, "price": 10.0,
                     "signal_id": "buy:600001:fac_9:20261031"})
        row = self.store.dispatch_by_signal_id("buy:600001:fac_9:20261031")
        self.assertEqual(row["user_id"], OWNER)

    def test_self_declared_uid_in_request_is_overridden_upstream(self):
        # 网关层覆盖自报（_do_order 里 req["user_id"]=place_uid），到 broker 时已是钥匙解出的值；
        # 这里锁住 broker 的取值优先级：req 里有值就用 req 的（网关已保证那是入向账号），否则用通道配置。
        ref = self._place({"code": "600002.SH", "side": "卖出", "qty": 100, "price": 10.0,
                           "signal_id": "sell:600002:fac_9:20261031", "user_id": "u_other"})
        row = self.store.dispatch_by_signal_id("sell:600002:fac_9:20261031")
        self.assertEqual(row["user_id"], "u_other")
        self.assertTrue(str(ref).startswith("seq:"))


class TestOutboundToken(unittest.TestCase):
    def test_uid_picks_its_own_key(self):
        gw = gw_mod.Gateway(_cfg(account_tokens=[
            {"user_id": OWNER, "token": TK_OWNER},
            {"user_id": MEMBER, "token": TK_MEMBER},
        ]))
        # ④ 归属是谁就用谁的钥匙上报；首尔侧"钥匙→账号"的既有逻辑天然分开，
        #    回报体里的 user_id 继续一律不信
        self.assertEqual(gw.handler._token_for(MEMBER), TK_MEMBER)
        self.assertEqual(gw.handler._token_for("u-不存在"), "")

    def test_event_uid_resolved_from_ledger(self):
        gw = gw_mod.Gateway(_cfg(account_tokens=[
            {"user_id": OWNER, "token": TK_OWNER},
            {"user_id": MEMBER, "token": TK_MEMBER},
        ]))
        gw.store.upsert_order({"order_id": "seq:7", "signal_id": "buy:600004:fac_1:20261030",
                               "code": "600004.SH", "side": "买入", "status": "已报",
                               "price": 10.0, "qty": 100, "created_at": "2026-10-30 09:40:00",
                               "user_id": MEMBER})
        self.assertEqual(gw.handler._resolve_uid("buy:600004:fac_1:20261030"), MEMBER)
        # 查不到归属（手动单/账户级事实）⇒ 落回 owner，绝不留空
        self.assertEqual(gw.handler._resolve_uid("buy:999999:fac_1:20261030"), OWNER)


if __name__ == "__main__":
    unittest.main()
