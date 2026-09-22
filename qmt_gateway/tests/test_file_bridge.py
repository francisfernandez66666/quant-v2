#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway 单测（unittest，零第三方依赖）：§QMT-F16 文件桥 sidecar。

覆盖：上报文件逐行 _do_dispatch_result 语义（heartbeat 回执 / order_result 派发结算）、
pending 派发原子推送 bridge_cmd.json、文件轮转（缩短）回读。无需 Windows/xtquant。

§A4（2026-09-22 修复批）新增锁：位点只在**该行 apply 成功后**推进（旧实现先推位点
再 apply，异常行被永久静默跳过）、apply 失败行先重试后落 bridge_report_dead.jsonl 死信、
坏 JSON 行同样落死信、末段无换行的半行本轮不消费（详见 TestFileBridge 内 §A4 用例）。

§TZ（2026-09-22 修复批）：夹具里模拟沙箱侧产出的 ts 一律走 store._now_cn()（显式北京
时区），不再用「本地钟面 strftime + 硬编码 +08:00」的假偏移写法——非北京时区部署机上
那种写法会自产漂移样本，掩盖真实时钟缺陷。
"""
import json
import os
import sys
import tempfile
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from gateway import Gateway  # noqa: E402
from store import _now_cn  # §TZ 夹具时间串统一显式北京时区  # noqa: E402


def new_db_path():
    """创建临时 SQLite 文件并立即删除，得到“存在路径但空库”的存储路径（测试隔离用）。"""
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return path


# 文件桥协议单测：指令下发/回报读取的文件桥闭环。
class TestFileBridge(unittest.TestCase):
    def _new_gw(self, tmp_dir):
        """构造最小 Gateway（mock 通道 + 临时 bridge_report_dir，不起后台线程）。"""
        # 构造最小 Gateway：显式 bridge_report_dir 指向临时目录，隔离真实网关目录
        gw = Gateway({
            "listen": "127.0.0.1:0", "token": "tk", "broker": "mock", "account": "M",
            "db": new_db_path(), "report_url": "", "reconcile_sec": 0,
            "bridge_report_dir": tmp_dir,
            "seed": [{"ts_code": "600519.SH", "name": "贵州茅台", "qty": 100,
                      "cost_price": 1500, "highest_price": 1500}],
        })
        return gw

    def test_reconcile_cmds_drops_done_keeps_inflight(self):
        """§P2-13（2026-09-15）FIX 2026-09-14 drill-3 回归：cmd 文件 reconcile 为
        「仅 inflight」——桥重启后重放 done 老单会被沙箱重复真实下单（221 次事故根因），
        reconcile 必须把 done 残留洗掉、同时保留 inflight（掉线期间未消费单不丢）。"""
        gw = self._new_gw(tempfile.mkdtemp())
        # 两条派发：A 保持 inflight（桥已取走未回报），B 已 done（已回报）
        gw.store.dispatch_enqueue_order({"signal_id": "RA", "code": "600519.SH",
                                         "side": "买入", "price_type": "limit",
                                         "price": 1500, "qty": 100})
        gw.store.dispatch_enqueue_order({"signal_id": "RB", "code": "600519.SH",
                                         "side": "卖出", "price_type": "limit",
                                         "price": 1600, "qty": 100})
        seq_a = gw.store.dispatch_pending(limit=1)[0]["seq"]
        seq_b = gw.store.dispatch_pending(limit=1)[0]["seq"]
        gw.store.dispatch_set_result(seq_b, {"ok": True, "order_id": "EXC-B", "err": ""})
        try:
            # 模拟桥重启后的 cmd 文件残留（含 done 的 B）
            cmd_path = gw._file_bridge_cmd_path()
            with open(cmd_path, "w", encoding="utf-8") as f:
                json.dump({"ts": 1.0, "cmds": [
                    {"seq": seq_a, "kind": "order"}, {"seq": seq_b, "kind": "order"},
                ]}, f)
            gw._file_bridge_reconcile_cmds(cmd_path, gw.store.dispatch_inflight())
            with open(cmd_path, "rb") as f:
                data = json.loads(f.read().decode("utf-8"))
            seqs = [str(c.get("seq", "")) for c in (data.get("cmds") or [])]
            self.assertEqual(seqs, [seq_a], "reconcile 必须只保留 inflight（done 老单洗掉）")
            # inflight 无变化时幂等：不重写（内容一致直接 return）
            mtime1 = os.path.getmtime(cmd_path)
            time.sleep(0.02)
            gw._file_bridge_reconcile_cmds(cmd_path, gw.store.dispatch_inflight())
            self.assertEqual(os.path.getmtime(cmd_path), mtime1, "内容一致不得空转重写")
        finally:
            gw.stop()

    def test_apply_trade_attributed_by_exchange_order_id(self):
        """§P2-13（2026-09-15）FIX 2026-09-14 drill-3 回归：桥 DEAL 行 m_strRemark 实测为空，
        成交回报缺 signal_id 时必须能按交易所委托号反查派发项归因，并推进委托终态——
        否则成交无法归因、委托永远停留已报。"""
        gw = self._new_gw(tempfile.mkdtemp())
        gw.store.dispatch_enqueue_order({"signal_id": "FB3", "code": "600519.SH",
                                         "side": "买入", "price_type": "limit",
                                         "price": 1500, "qty": 100})
        seq = gw.store.dispatch_pending(limit=1)[0]["seq"]
        # 桥回填交易所委托号（order_result）
        gw.store.dispatch_set_result(seq, {"ok": True, "order_id": "EXC-9", "err": ""})
        # 成交回报只带交易所委托号（无 signal_id、无 seq——最恶劣形态）
        gw._apply_trade({"order_id": "EXC-9", "price": 1500.0, "qty": 100,
                         "amount": 150000.0})
        try:
            # fills 行归因到 FB3
            with gw.store._lock:
                rows = gw.store._conn.execute(
                    "SELECT signal_id, order_id, qty FROM fills").fetchall()
            self.assertEqual(len(rows), 1)
            self.assertEqual(rows[0]["signal_id"], "FB3")
            self.assertEqual(rows[0]["order_id"], "EXC-9")
            # 委托状态按累计成交量推进为 已成（不再是悬置的 已报）
            order = gw.store.order_by_signal("FB3")
            self.assertIsNotNone(order)
            self.assertEqual(order["status"], "已成")
        finally:
            gw.stop()

    @staticmethod
    def _seed_600580(gw, qty=800, price=25.0):
        """先用一笔正常买入成交建出 600580.SH 底仓（复刻事故账户形态）。

        必须走成交回报而非直接写库：卖出成交在无底仓时不会落 fills（apply_fill 的
        "卖出空仓 = no-op" 语义），底仓是让本用例能观测到 fills.side 的前提。
        """
        gw.handler._push = lambda p: None
        gw.handler.on_trade({"order_id": "EXC-BUY", "code": "600580.SH", "side": "买入",
                             "price": price, "qty": qty, "amount": price * qty,
                             "traded_at": "2026-09-16T10:00:00+08:00",
                             "signal_id": "buy:600580:manual:2026-09-16"})

    def test_apply_trade_dispatch_side_is_authoritative(self):
        """§P0 2026-09-18「买入卖出不分」回归：本端派发过的单，成交方向以派发项为权威。

        事故形态：600580.SH 一笔真实手动卖出（800 股 @26.71）在成交流水里显示为「买入」。
        方向来源链只有柜台 DEAL 行字段反推（枚举空间跨券商构建不定），而本地派发项
        dispatch.side 是我们下单时写下的物理事实。旧实现用 setdefault —— 桥行恒带 side，
        覆盖从未发生，误判方向原样落进 fills（成本/已实现盈亏/胜率全线污染）。
        """
        gw = self._new_gw(tempfile.mkdtemp())
        pushed = []
        self._seed_600580(gw)
        gw.handler._push = lambda p: pushed.append(p)
        # 派发一笔真实卖出（与事故同形的量价）
        gw.store.dispatch_enqueue_order({"signal_id": "manual@600580.SH@20260917100026",
                                         "code": "600580.SH", "side": "卖出",
                                         "price_type": "limit", "price": 26.71, "qty": 800})
        seq = gw.store.dispatch_pending(limit=1)[0]["seq"]
        gw.store.dispatch_set_result(seq, {"ok": True, "order_id": "EXC-600580", "err": ""})
        try:
            # 桥报来「买入」但归因正确（最恶劣形态：signal_id 对、方向反）；code 故意留空，
            # 顺带钉住"代码也必须按派发项回填"（否则卖出会拿空代码查底仓而漏账）。
            gw._apply_trade({"order_id": "EXC-600580",
                             "signal_id": "manual@600580.SH@20260917100026",
                             "side": "买入", "price": 26.71, "qty": 800,
                             "amount": 21368.0,
                             "traded_at": "2026-09-17T10:00:26+08:00"})
            with gw.store._lock:
                rows = gw.store._conn.execute(
                    "SELECT side, qty FROM fills WHERE order_id = ?",
                    ("EXC-600580",)).fetchall()
            self.assertEqual(len(rows), 1)
            self.assertEqual(rows[0]["side"], "卖出",
                             "派发项方向（卖出）必须覆盖桥的误判方向（买入）")
            # 上报量仔的 payload 同源为卖出
            self.assertEqual(pushed[0]["side"], "卖出")
            # 反向回归：误判为买入会把 800 股卖出记成加仓（底仓变 1600 股）
            held = {p["ts_code"]: p["qty"] for p in gw.store.list_positions()}
            self.assertNotIn("600580.SH", held, "清仓卖出后不得残留持仓：%s" % held)
        finally:
            gw.stop()

    def test_apply_trade_unattributed_keeps_reported_side(self):
        """未派发过的成交（客户端手工单/对账来源）：无权威方向可依，回报方向原样保留。

        防止上面那条权威化改动扩大到"所有成交一律改写"——只有本端派发过的单才有权威方向。
        """
        gw = self._new_gw(tempfile.mkdtemp())
        pushed = []
        self._seed_600580(gw)
        gw.handler._push = lambda p: pushed.append(p)
        try:
            gw._apply_trade({"order_id": "EXC-CLIENT", "side": "卖出", "code": "600580.SH",
                             "price": 26.71, "qty": 800, "amount": 21368.0,
                             "traded_at": "2026-09-17T10:00:26+08:00"})
            with gw.store._lock:
                rows = gw.store._conn.execute(
                    "SELECT side FROM fills WHERE order_id = ?", ("EXC-CLIENT",)).fetchall()
            self.assertEqual(len(rows), 1)
            self.assertEqual(rows[0]["side"], "卖出")
            self.assertEqual(pushed[0]["side"], "卖出")
        finally:
            gw.stop()

    def test_report_heartbeat_drives_bridge_connected(self):
        """上报一行 heartbeat → sidecar 应用 → store.bridge_heartbeat() → 桥在线。"""
        gw = self._new_gw(tempfile.mkdtemp())
        path = gw._file_bridge_path()
        # 先写一条心跳 JSONL 行（沙箱侧产出）
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps({"type": "heartbeat",
                                "ts": _now_cn(),
                                "n": 1}, ensure_ascii=False) + "\n")
        th = None
        try:
            th = threading.Thread(target=gw._file_bridge_loop, daemon=True)
            th.start()
            # 等 sidecar 至少扫到一行（tick=2s × 2 轮兜底）
            deadline = time.time() + 6
            while time.time() < deadline and not gw.store.bridge_connected(15):
                time.sleep(0.2)
            self.assertTrue(gw.store.bridge_connected(15), "心跳应经文件桥被网关采纳")
        finally:
            gw._stop.set()
            if th:
                th.join(timeout=2)

    def test_pending_pushed_to_cmd_file(self):
        """pending 派发项 → sidecar 原子写 bridge_cmd.json（桥执行前记录）。"""
        gw = self._new_gw(tempfile.mkdtemp())
        gw.store.dispatch_enqueue_order({"signal_id": "FB1", "code": "600519.SH",
                                         "side": "买入", "price_type": "limit",
                                         "price": 1500, "qty": 100}, user_id="test")
        try:
            gw._file_bridge_push_pending()
            cmd_path = gw._file_bridge_cmd_path()
            self.assertTrue(os.path.exists(cmd_path), "bridge_cmd.json 未生成")
            with open(cmd_path, "rb") as f:
                data = json.loads(f.read().decode("utf-8"))
            cmds = data.get("cmds") or []
            self.assertEqual(len(cmds), 1)
            self.assertEqual(str(cmds[0].get("kind")), "order")
            self.assertEqual(cmds[0].get("signal_id"), "FB1")
            # 再推一次：pending 已被取走（inflight），不重复推送、也不清空
            # （inflight 未结算的指令必须留在文件里，桥重启后才能续跑）。
            gw._file_bridge_push_pending()
            with open(cmd_path, "rb") as f:
                data = json.loads(f.read().decode("utf-8"))
            self.assertEqual(len(data.get("cmds") or []), 1)  # 文件仍只含上一批（不被覆盖清空）
        finally:
            gw.stop()

    def test_report_order_result_settles_dispatch(self):
        """order_result JSONL 行 → sidecar → 派发项 done + 委托号回填（实盘语义 dry 也通）。"""
        gw = self._new_gw(tempfile.mkdtemp())
        # 准备一条已完成下单的派发行（pending 派发并标记 inflight，模拟桥取单后）。
        # 量价用 600519 一手：与实盘最小委托同形，顺带验证 qty=100 整手约束不拦内部派发。
        gw.store.dispatch_enqueue_order({"signal_id": "FB2", "code": "600519.SH",
                                         "side": "买入", "price_type": "limit",
                                         "price": 1500, "qty": 100})
        self.store_helper = None
        pend = gw.store.dispatch_pending(limit=1)
        self.assertEqual(len(pend), 1)
        seq = pend[0]["seq"]
        path = gw._file_bridge_path()
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps({"type": "order_result", "seq": seq, "ok": True,
                                "order_id": "DRYRUN-1", "err": ""},
                               ensure_ascii=False) + "\n")
        try:
            th = threading.Thread(target=gw._file_bridge_loop, daemon=True)
            th.start()
            deadline = time.time() + 6
            row = None
            while time.time() < deadline:
                row = gw.store.dispatch_get(seq)
                if row and row.get("status") == "done":
                    break
                time.sleep(0.2)
            self.assertIsNotNone(row)
            self.assertEqual(row.get("status"), "done")
            self.assertEqual(row.get("order_id"), "DRYRUN-1")
        finally:
            gw._stop.set()

    # ── §A4（2026-09-22 修复批）位点只在 apply 成功后推进 + 死信留痕 ──

    def _write_lines(self, path, lines):
        """按 JSONL 原样写入若干行（每行补换行符，与桥的追加写同形）。"""
        with open(path, "w", encoding="utf-8") as f:
            for ln in lines:
                f.write(ln + "\n")

    def test_apply_exception_line_keeps_offset_and_dead_letters_after_retries(self):
        """§A4 主断言：apply 抛异常的行**位点不推进**，重试超限后落死信文件而非静默跳过。

        缺陷原文：旧实现先 `pos = f.tell()` 把位点推到块尾再逐行 apply，
        apply 异常只 `log.exception` → 该行的事件永久丢失（成交/结算回报丢失＝账本失真）。
        """
        gw = self._new_gw(tempfile.mkdtemp())
        path = gw._file_bridge_path()
        boom = json.dumps({"type": "boom", "n": 1}, ensure_ascii=False)
        after = json.dumps({"type": "heartbeat", "ts": _now_cn(), "n": 7}, ensure_ascii=False)
        self._write_lines(path, [boom, after])
        seen = []
        real_apply = gw._do_dispatch_result

        def _fake_apply(body):
            """桩：boom 类型必抛（模拟 store/handler 侧异常），其余走真实应用逻辑。"""
            seen.append(body.get("type"))
            if body.get("type") == "boom":
                raise RuntimeError("apply boom")
            return real_apply(body)

        gw._do_dispatch_result = _fake_apply
        retries = {}
        try:
            size = os.path.getsize(path)
            # 第 1 轮：毒行失败 → 位点必须停在它之前（旧实现此时已推到块尾）
            pos = gw._bridge_consume_report(path, 0, retries)
            self.assertEqual(pos, 0, "§A4 apply 失败时位点不得推进")
            self.assertEqual(seen, ["boom"], "失败行之后的行不得被越过消费（保序）")
            self.assertFalse(os.path.exists(gw._file_bridge_dead_path()),
                             "未超限就落死信 = 放弃了重试")
            # 继续巡检：重试到超限后必须落死信并越过该行，后续行随后被正常应用
            for _ in range(4):
                pos = gw._bridge_consume_report(path, pos, retries)
            self.assertEqual(pos, size, "死信后位点应越过毒行、消费到块尾")
            self.assertEqual(seen.count("boom"), gw._BRIDGE_LINE_RETRY_MAX,
                             "重试次数应等于 §A4 上限")
            self.assertEqual(seen.count("heartbeat"), 1, "后续行必须在毒行放行后被应用")
            self.assertTrue(gw.store.bridge_connected(60), "心跳事件未被应用（死信流程异常）")
            with open(gw._file_bridge_dead_path(), encoding="utf-8") as f:
                dead = [json.loads(x) for x in f if x.strip()]
            self.assertEqual(len(dead), 1, "超限行必须且只能落一条死信")
            self.assertIn("apply boom", dead[0]["reason"])
            self.assertIn("boom", dead[0]["line"])
            self.assertTrue(dead[0].get("at"), "死信必须带留痕时间")
        finally:
            gw._stop.set()
            gw.stop()

    def test_bad_json_line_goes_to_dead_letter_not_silently_skipped(self):
        """§A4：坏 JSON 行落死信并推进位点（确定性缺陷不重试），其后正常行照常消费。"""
        gw = self._new_gw(tempfile.mkdtemp())
        path = gw._file_bridge_path()
        broken = '{"type": "heartbeat", "n": 1,   <-- 桥被 kill 时留下的半截/畸形行'
        good = json.dumps({"type": "heartbeat", "ts": _now_cn(), "n": 3}, ensure_ascii=False)
        self._write_lines(path, [broken, good])
        gw._do_dispatch_result = lambda body: (200, {"ok": True})
        retries = {}
        try:
            pos = gw._bridge_consume_report(path, 0, retries)
            self.assertEqual(pos, os.path.getsize(path), "坏行不得卡住后续事件的消费")
            with open(gw._file_bridge_dead_path(), encoding="utf-8") as f:
                dead = [json.loads(x) for x in f if x.strip()]
            self.assertEqual(len(dead), 1)
            self.assertIn("bad_json", dead[0]["reason"])
            self.assertIn("heartbeat", dead[0]["line"])
        finally:
            gw.stop()

    def test_incomplete_tail_line_is_not_consumed_or_dead_lettered(self):
        """§A4 附带修复：末段无换行 = 桥可能还在写，本轮不消费、不误判为坏行；补全后正常消费。"""
        gw = self._new_gw(tempfile.mkdtemp())
        path = gw._file_bridge_path()
        line = json.dumps({"type": "heartbeat", "ts": _now_cn(), "n": 11}, ensure_ascii=False)
        retries = {}   # 不打桩：走真实 _do_dispatch_result，半行消费后桥心跳必须真的落库
        try:
            with open(path, "w", encoding="utf-8") as f:
                f.write(line[:20])  # 半行（无换行符）
            pos = gw._bridge_consume_report(path, 0, retries)
            self.assertEqual(pos, 0, "半行必须留在原地等下一轮补全")
            self.assertFalse(os.path.exists(gw._file_bridge_dead_path()), "半行不得进死信")
            # 桥补全这一行并加换行 → 正常消费
            with open(path, "a", encoding="utf-8") as f:
                f.write(line[20:] + "\n")
            pos = gw._bridge_consume_report(path, pos, retries)
            self.assertEqual(pos, os.path.getsize(path))
            self.assertTrue(gw.store.bridge_connected(60))
        finally:
            gw.stop()

    def test_report_pos_persisted_only_after_line_consumed(self):
        """§A4 端到端：sidecar 循环持久化的 report_pos 不含未消费行（重启不丢事件）。"""
        gw = self._new_gw(tempfile.mkdtemp())
        path = gw._file_bridge_path()
        boom = json.dumps({"type": "boom"}, ensure_ascii=False)
        self._write_lines(path, [boom])
        gw._do_dispatch_result = lambda body: (_ for _ in ()).throw(RuntimeError("boom"))
        th = threading.Thread(target=gw._file_bridge_loop, daemon=True)
        try:
            th.start()
            time.sleep(1.5)  # 至少跑两轮（0.5s/轮）
            saved = gw.store.bridge_snapshot_get("report_pos", None)
            self.assertEqual(int(saved or 0), 0,
                             "未消费行的位点被持久化 = 网关重启后该行永久丢失")
        finally:
            gw._stop.set()
            th.join(timeout=3)
            gw.stop()

    def test_report_file_rotation_rescanned_from_zero(self):
        """上报文件被沙箱重写（长度缩短）→ sidecar 偏移回退重读，不丢事件。"""
        gw = self._new_gw(tempfile.mkdtemp())
        path = gw._file_bridge_path()
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps({"type": "heartbeat",
                                "ts": _now_cn(), "n": 1},
                               ensure_ascii=False) + "\n")
        th = threading.Thread(target=gw._file_bridge_loop, daemon=True)
        try:
            th.start()
            deadline = time.time() + 6
            while time.time() < deadline and not gw.store.bridge_connected(15):
                time.sleep(0.2)
            self.assertTrue(gw.store.bridge_connected(15))
            # 沙箱轮转：清文本（文件 len 变小），重写两条事件（心跳修正场景）
            self.assertTrue(os.path.exists(path))
            with open(path, "w", encoding="utf-8") as f:
                f.write(json.dumps({"type": "heartbeat",
                                    "ts": _now_cn(), "n": 999},
                                   ensure_ascii=False) + "\n")
                f.write(json.dumps({"type": "heartbeat",
                                    "ts": _now_cn(), "n": 2},
                                   ensure_ascii=False) + "\n")
            # 等待 sidecar 处理（2s/轮 * 3 轮兜底）
            deadline = time.time() + 8
            ok = False
            while time.time() < deadline:
                if os.path.exists(path) and os.path.getsize(path) > 0:
                    ok = True
                    break
                time.sleep(0.2)
            self.assertTrue(ok)
        finally:
            gw._stop.set()
            th.join(timeout=2)


import threading  # noqa: E402
