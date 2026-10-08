#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway 单测（unittest，零第三方依赖）：§P2-G 文件桥命令下发顺序翻转。

缺陷原文（2026-10-05 全量评价 P2-G，2026-10-06 修复批 波 5 落码）：
`gateway._file_bridge_push_pending` 第一步调 `store.dispatch_pending(limit=50)`，
而这个函数**取单即把行翻成 inflight**（那是 HTTP /dispatch/pending 的正确语义——
响应体本身就把单交给了对方）。file 桥借用它之后，"写 bridge_cmd.json 失败"这一路
变成确定性的丢单：行已经是 inflight，桥永远看不到这张单，旧代码只留一行
`log.exception`，要等 §M16 超龄收割（默认 1800s）把行判成「已废」，而判废**不回 pending**
⇒ 这笔交易在系统里安静消失，前端与账本都看不出它曾经存在过。

修法：peek（只读）→ 写文件 → 写成功才 mark_inflight；写失败保持 pending 并计数进
/admin/status 的 file_bridge_push 观察位。

四条锁（与 docs/FIX_PLAN_20261006.md 第七节 P2-G 的 L1–L4 一一对应）：
  L1 写文件抛异常 ⇒ 派发行**仍为 pending** + 计数抬起来 + 命令文件没落盘；
  L2 正常路径     ⇒ 行翻成 inflight、命令文件在位且带该 seq、失败计数归零；
  L3 peek 无副作用 ⇒ 连 peek 两次都拿到同一批 pending（钉住旧 dispatch_pending 的副作用形态：
                    它第二次就取不到了——这正是 L1 那类丢单的成因）；
  L4 收割腿       ⇒ 写失败后把行龄推过 dispatch_inflight_reap_sec，收割**不得**把它判废
                    （对照腿：同期一条真 inflight 行必须被判废，证明收割线程真的跑到了）。

English: §P2-G locks — the file bridge must write the command file before flipping dispatch
rows to inflight; a failed write keeps rows pending, is counted outwardly, and never gets reaped
as rejected.
"""
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from gateway import Gateway  # noqa: E402


def new_db_path():
    """创建临时 SQLite 文件并立即删除，得到“存在路径但空库”的存储路径（测试隔离用）。"""
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return path


def make_order_seq(gw, sid):
    """入队一笔待执行单并返回其 seq（只入队，不触碰任何取单语义）。"""
    return gw.store.dispatch_enqueue_order({
        "signal_id": sid, "code": "600519.SH", "side": "买入",
        "price_type": "limit", "price": 1500, "qty": 100,
    })


# 文件桥下发顺序单测：写盘失败不丢单、写成功才翻状态。
class TestFileBridgePushOrder(unittest.TestCase):
    def _new_gw(self, tmp_dir):
        """构造最小 Gateway（mock 通道 + 临时 bridge_report_dir，不起后台线程）。"""
        return Gateway({
            "listen": "127.0.0.1:0", "token": "tk", "broker": "mock", "account": "M",
            "db": new_db_path(), "report_url": "", "reconcile_sec": 0,
            "bridge_report_dir": tmp_dir,
            # 收割阈值保持默认 1800s：L4 靠"把行龄推到阈值之外"而不是改小阈值，
            # 以免把"阈值配置本身"和"收割会不会误伤 pending"两件事混成一条断言。
            "seed": [{"ts_code": "600519.SH", "name": "贵州茅台", "qty": 100,
                      "cost_price": 1500, "highest_price": 1500}],
        })

    def _block_cmd_write(self, gw):
        """把 cmd 文件的 .tmp 占成**目录**：open(tmp,"w") 必抛 IsADirectoryError。

        选这个桩而不是 monkeypatch builtins.open：它命中真实的失败形态（目标路径不可写），
        且不需要替换任何全局函数——patch 全局 open 会把测试进程里所有读写一起改变，
        一旦 fixture 自身也在写文件就会串台。
        """
        tmp_path = gw._file_bridge_cmd_path() + ".tmp"
        os.makedirs(tmp_path, exist_ok=True)
        return tmp_path

    def _unblock_cmd_write(self, tmp_path):
        """撤掉 .tmp 目录桩，让写文件恢复成功（同一路径成功/失败两支，只差这一个桩）。"""
        os.rmdir(tmp_path)

    def test_L1_write_failure_keeps_pending_and_counts(self):
        """L1：写文件失败 ⇒ 行仍 pending、观察位计数抬起、命令文件没落盘。"""
        gw = self._new_gw(tempfile.mkdtemp())
        try:
            seq = make_order_seq(gw, "P2G-A")
            blocked = self._block_cmd_write(gw)
            cmd_path = gw._file_bridge_cmd_path()
            before = (gw._file_bridge_push_fail_total, gw._file_bridge_push_fail_streak)
            gw._file_bridge_push_pending()
            row = self.store_get(gw, seq)
            self.assertEqual(row["status"], "pending",
                             "§P2-G 反例：写文件失败却已把派发行翻成 inflight（=丢单旧形态）")
            self.assertEqual(gw._file_bridge_push_fail_total, before[0] + 1,
                             "写失败必须累计 +1（不得只 log.exception）")
            self.assertEqual(gw._file_bridge_push_fail_streak, before[1] + 1,
                             "写失败必须连续计数 +1")
            self.assertEqual(gw._file_bridge_push_fail_held, 1,
                             "观察位要写清「这一轮被按住了几单」")
            self.assertFalse(os.path.exists(cmd_path), "写失败不该留下半截命令文件")
            # 下一轮（去掉桩）必须还能把这单推出去——pending 仍在队列里是这条修复的全部意义
            self._unblock_cmd_write(blocked)
            gw._file_bridge_push_pending()
            self.assertEqual(self.store_get(gw, seq)["status"], "inflight",
                             "写失败后保持 pending 的单必须在下一轮照常下发")
        finally:
            gw.stop()

    def test_L2_normal_path_marks_inflight_and_resets_streak(self):
        """L2：正常路径 ⇒ inflight + 命令文件在位带该 seq + 失败计数归零。"""
        gw = self._new_gw(tempfile.mkdtemp())
        try:
            # 先制造一次失败，让"归零"断言不是恒真（streak 起点非 0 才叫真的被清零）
            seq_bad = make_order_seq(gw, "P2G-B0")
            blocked = self._block_cmd_write(gw)
            gw._file_bridge_push_pending()
            self.assertEqual(gw._file_bridge_push_fail_streak, 1, "前置：应先有一次写失败")
            self._unblock_cmd_write(blocked)

            seq = make_order_seq(gw, "P2G-B")
            gw._file_bridge_push_pending()  # 这一批含 P2G-B0 与 P2G-B 两单
            cmd_path = gw._file_bridge_cmd_path()
            self.assertTrue(os.path.exists(cmd_path), "成功轮必须落命令文件")
            with open(cmd_path, "rb") as f:
                data = json.loads(f.read().decode("utf-8"))
            seqs = [str(c.get("seq", "")) for c in (data.get("cmds") or [])]
            self.assertIn(seq, seqs, "命令文件必须带上本次派发的 seq")
            self.assertEqual(sorted(seqs), sorted([seq_bad, seq]),
                             "命令文件内容应恰等于本轮 peek 到的两单（不多不少）")
            self.assertEqual(self.store_get(gw, seq)["status"], "inflight",
                             "文件写成功后才允许翻 inflight")
            self.assertEqual(self.store_get(gw, seq_bad)["status"], "inflight",
                             "上一轮被按住的单同样要翻上去（它已在本轮文件里）")
            self.assertEqual(gw._file_bridge_push_fail_streak, 0, "写成功必须把连续失败归零")
            self.assertEqual(gw._file_bridge_push_fail_held, 0, "按住单数同步归零")
        finally:
            gw.stop()

    def test_L3_peek_has_no_side_effect_and_mark_only_moves_pending(self):
        """L3：peek 纯读（连两次同结果）；mark 只翻仍为 pending 的行（不拖回别人的行）。

        对照旧形态：`dispatch_pending()` 第一次就把它取走的行翻成 inflight，
        第二次取不到 ⇒ 与本用例的第一条断言正好相反，这是 L1 丢单的成因证据。
        """
        gw = self._new_gw(tempfile.mkdtemp())
        try:
            seq = make_order_seq(gw, "P2G-C")
            first = gw.store.dispatch_pending_peek(limit=10)
            second = gw.store.dispatch_pending_peek(limit=10)
            self.assertEqual([r["seq"] for r in first], [seq], "peek 应取到刚入队的单")
            self.assertEqual([r["seq"] for r in second], [seq],
                             "§P2-G 反例：peek 有副作用（第二次取不到＝取单即翻状态的老形态）")
            self.assertEqual(gw.store.dispatch_stats().get("pending", 0), 1,
                             "两次 peek 都不该推进状态（队列里仍只有 1 条 pending）")
            # 旧 dispatch_pending 的副作用在此显式钉住（它翻状态；peek 不翻）：
            taken = gw.store.dispatch_pending(limit=1)
            self.assertEqual([r["seq"] for r in taken], [seq])
            self.assertEqual(self.store_get(gw, seq)["status"], "inflight",
                             "dispatch_pending 仍保持「取单即翻」的 HTTP 桥语义（本次没动它）")
            # mark 对已 inflight 的行必须 0 行命中：不把别人的在途行拖回 pending/inflight 时刻覆盖
            moved = gw.store.dispatch_mark_inflight([seq])
            self.assertEqual(moved, 0, "已 inflight 的行不得被 mark 二次翻动（会覆盖取单时刻）")
            self.assertEqual(gw.store.dispatch_mark_inflight([]), 0, "空 seqs 不得报错")
            self.assertEqual(gw.store.dispatch_mark_inflight(["seq:999999"]), 0,
                             "不存在的 seq 不得报错（返回 0 即未翻动）")
        finally:
            gw.stop()

    def test_L4_failed_write_rows_never_reaped(self):
        """L4：写失败后即使行龄超过收割阈值，收割也不得把它判废（对照真 inflight 行必须被判废）。

        排程按现网真实形态走，避免"手工造状态"把结论做没：
          ① D2 正常下发 → inflight（对照腿：桥取走后再没回报）；
          ② 桩住写盘后入队 D1 → peek 只拿到 D1、写失败 ⇒ D1 保持 pending（桥从未见过它）；
          ③ 把两行时间戳推到收割阈值之外跑 _reap_dispatch_inflight ⇒ D2 判废 done、D1 仍 pending；
          ④ 撤桩再推一轮 ⇒ D1 照常下发（既不判废也不烂在队列里，闭环成立）。
        """
        gw = self._new_gw(tempfile.mkdtemp())
        try:
            seq_inflight = make_order_seq(gw, "P2G-D2")
            gw._file_bridge_push_pending()  # 未桩：D2 正常走到 inflight
            self.assertEqual(self.store_get(gw, seq_inflight)["status"], "inflight",
                             "前置：对照腿必须真处于 inflight（否则第③步的「收割」零信息）")
            blocked = self._block_cmd_write(gw)
            seq_pending = make_order_seq(gw, "P2G-D1")
            gw._file_bridge_push_pending()  # 只 peek 到 D1，写失败 ⇒ D1 保持 pending
            self.assertEqual(self.store_get(gw, seq_pending)["status"], "pending",
                             "D1 必须仍 pending（写失败不丢单的形态）")
            self.assertEqual(self.store_get(gw, seq_inflight)["status"], "inflight",
                             "写失败轮不得动已在途的 D2")
            self._unblock_cmd_write(blocked)
            # 把两行的时间戳推到收割阈值之外（默认 1800s），模拟"卡了一小时"
            with gw.store._lock:
                gw.store._conn.execute(
                    "UPDATE dispatch SET created_at = '2020-01-01 09:00:00'")
                gw.store._conn.execute(
                    "UPDATE dispatch SET inflight_at = '2020-01-01 09:00:00' "
                    "WHERE seq = ?", (seq_inflight,))
                gw.store._conn.commit()
            reaped = gw._reap_dispatch_inflight("单测")
            self.assertGreaterEqual(reaped, 1, "对照腿（真 inflight）必须被收割，否则本用例零信息")
            self.assertEqual(self.store_get(gw, seq_inflight)["status"], "done",
                             "超龄 inflight 应被收割判废（§M16 语义不变）")
            self.assertEqual(self.store_get(gw, seq_pending)["status"], "pending",
                             "§P2-G 反例：写失败保持 pending 的单被收割成 done（=丢单）")
            # pending 行仍会被下一轮重新下发（丢单修复的闭环：既不被判废，也不会烂在队列里）
            gw._file_bridge_push_pending()
            self.assertEqual(self.store_get(gw, seq_pending)["status"], "inflight",
                             "收割后的下一轮必须仍能把 pending 单推给桥")
        finally:
            gw.stop()

    def test_L5_admin_status_exposes_push_counters(self):
        """L5（补位）：观察位真的把三个计数带出来——不接线的计数等于没告警（§DEADGAUGE 同族）。"""
        gw = self._new_gw(tempfile.mkdtemp())
        try:
            make_order_seq(gw, "P2G-E")
            blocked = self._block_cmd_write(gw)
            gw._file_bridge_push_pending()
            code, payload = gw._do_admin_status()
            self.assertEqual(code, 200)
            fb = payload.get("file_bridge_push") or {}
            self.assertEqual(fb.get("fail_total"), 1, "观察位应回显累计失败次数")
            self.assertEqual(fb.get("fail_streak"), 1, "观察位应回显连续失败次数")
            self.assertEqual(fb.get("fail_held"), 1, "观察位应回显被按住的单数")
            self._unblock_cmd_write(blocked)
            gw._file_bridge_push_pending()
            _, payload2 = gw._do_admin_status()
            fb2 = payload2.get("file_bridge_push") or {}
            self.assertEqual(fb2.get("fail_total"), 1, "成功轮不得把累计数抹掉（抖动 vs 系统性靠它区分）")
            self.assertEqual(fb2.get("fail_streak"), 0, "成功轮 streak 必须归 0")
            self.assertEqual(fb2.get("fail_held"), 0, "成功轮 held 必须归 0")
        finally:
            gw.stop()

    def store_get(self, gw, seq):
        """按 seq 取派发行（本文件唯一的读派发状态入口，断言全部走它以便逐字对齐）。"""
        row = gw.store.dispatch_get(seq)
        self.assertIsNotNone(row, "派发行应存在（seq=%s）" % seq)
        return row


if __name__ == "__main__":
    unittest.main()
