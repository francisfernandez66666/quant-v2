#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§FRIEND-PROFILE 批 3 回归锁：远端执行腿的证据包与停腿动作（leg_ops）。

钉住四件事，全都是"证据类判据容易写歪且歪了不出声"的那一类：
  ① 行数**数不出时报 -1**，绝不折成 0——0 是"当天确实没交易"，-1 是"取证不可信"，
     这两种事实在复盘时差一个数量级（§CAL-COVER 的"数不出 ≠ 没落后"同口径）；
  ② 库坏（integrity 非 ok）⇒ **拒绝产出导出包**，不许生成一份"看起来有、实际撕了"的证据；
  ③ 导出走 SQLite 在线备份 API，取出来的副本必须自己过 `integrity_check`
     （`shutil.copy` 一个正在被网关写的库，副本可以是撕裂页——那就不叫凭据了）；
  ④ 停腿清空 `bridge_cmd.json` 是**原子写**且幂等（文件不存在也回 ok），
     并把账上还剩几行未交付如实报出来——只停服务不清这个文件，机器下次起来会把旧指令再执行一遍。
另外锁住所有锚点是纯 ASCII：中文经 PowerShell 管道会被码页换成 `?`，判读会恒假而流程照走。
"""
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest
import zipfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from leg_ops import EXIT_OK, EXIT_REFUSED, cmd_export, cmd_stop_cmd, cmd_verify  # noqa: E402
from store import Store  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CLI = os.path.join(HERE, "leg_ops.py")


def fresh_db(with_rows=True):
    """建一份带最小真实形状账本的库（orders/fills/real_positions/dispatch 各若干）。"""
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)  # Store 的建表/迁移路径要求"文件不存在"（空文件会撞上迁移，照 new_db_path 的姿势）
    st = Store(path)
    if with_rows:
        c = st._conn
        c.execute("INSERT INTO orders(order_id,signal_id,code,side,status,price,qty,created_at,user_id)"
                  " VALUES('1001','sig-1','600588.SH','买入','已成',8.93,600,'2026-11-02 10:00:00','u_friend')")
        c.execute("INSERT INTO orders(order_id,signal_id,code,side,status,price,qty,created_at,user_id)"
                  " VALUES('1002','sig-2','002415.SZ','买入','已报',12.5,500,'2026-11-02 14:20:00','u_friend')")
        c.execute("INSERT INTO fills(order_id,code,side,price,qty,amount,traded_at,signal_id,user_id,trade_id)"
                  " VALUES('1001','600588.SH','买入',8.93,600,5358,'2026-11-02 10:00:05','sig-1','u_friend','t-1')")
        c.execute("INSERT INTO real_positions(ts_code,name,qty,cost_price,amount,updated_at,user_id)"
                  " VALUES('600588.SH','XX',600,8.93,5358,'2026-11-02 15:00:00','u_friend')")
        c.execute("INSERT INTO dispatch(seq,signal_id,kind,code,side,price,qty,status,user_id,created_at)"
                  " VALUES('seq:1','sig-2','order','002415.SZ','买入',12.5,500,'pending','u_friend','2026-11-02 14:20:00')")
        c.commit()
    st.close()
    return path


class TestVerifyCounts(unittest.TestCase):
    def test_healthy_db_reports_real_counts(self):
        path = fresh_db()
        code = cmd_verify(path)
        self.assertEqual(code, EXIT_OK)

    def test_missing_db_is_not_zero(self):
        # 这是本文件最要紧的一条：missing 必须走 unreadable/missing，
        # 而不是"表数为 0 的空账本"——后者读起来像"这台机器今天什么都没做"
        code = cmd_verify(os.path.join(tempfile.gettempdir(), "legops_does_not_exist.db"))
        self.assertEqual(code, EXIT_REFUSED)

    def test_corrupt_db_is_refused(self):
        fd, path = tempfile.mkstemp(suffix=".db")
        os.write(fd, b"this is not a sqlite file at all" * 40)
        os.close(fd)
        self.assertEqual(cmd_verify(path), EXIT_REFUSED)


class TestExport(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp(prefix="legops_")
        self.db = fresh_db()
        self.zip = os.path.join(self.dir, "friend_ledger_u_friend_20261102.zip")

    def test_refuses_when_integrity_not_ok(self):
        import contextlib
        import io as _io
        bad = os.path.join(self.dir, "bad.db")
        with open(bad, "wb") as f:
            f.write(b"garbage" * 200)
        buf = _io.StringIO()
        with contextlib.redirect_stdout(buf):
            code = cmd_export(bad, self.dir, os.path.join(self.dir, "x.zip"), "20261102")
        self.assertEqual(code, EXIT_REFUSED)
        self.assertFalse(os.path.exists(os.path.join(self.dir, "x.zip")),
                         "库坏时仍产出了导出包（那就是假凭据）")
        # 必须断言"拒绝的理由是体检不过"：只判非 0 的话，摘掉体检也照样会被 SQLite 抛错兜住，
        # 这条用例就变成恒绿的摆设（反证 R3 实测出来正是这个形状）
        self.assertIn("reason=integrity_", buf.getvalue(),
                      "拒绝导出的原因不是体检（说明体检那道判断本身没人守）")

    def test_export_contains_backup_that_passes_integrity(self):
        # 桥回报文件也必须在包里（成交回车的原文，事后核对靠它）
        rep = os.path.join(self.dir, "bridge_report.jsonl")
        with open(rep, "w", encoding="utf-8") as f:
            f.write('{"type":"heartbeat"}\n')
        code = cmd_export(self.db, self.dir, self.zip, "20261102")
        self.assertEqual(code, EXIT_OK)
        self.assertTrue(os.path.exists(self.zip))
        with zipfile.ZipFile(self.zip) as z:
            names = set(z.namelist())
            self.assertIn("data.db", names)
            self.assertIn("manifest.json", names)
            self.assertIn("bridge_report.jsonl", names)
            out = os.path.join(self.dir, "extracted.db")
            with z.open("data.db") as src, open(out, "wb") as dst:
                dst.write(src.read())
        rows = sqlite3.connect(out).execute("PRAGMA integrity_check").fetchall()
        self.assertEqual(str(rows[0][0]).lower(), "ok", "导出的库过不了 integrity_check（备份方式不对）")

    def test_manifest_rows_match_ledger(self):
        cmd_export(self.db, self.dir, self.zip, "20261102")
        with zipfile.ZipFile(self.zip) as z:
            m = json.loads(z.read("manifest.json").decode("utf-8"))
        self.assertEqual(m["rows"]["orders"], 2)
        self.assertEqual(m["rows"]["fills"], 1)
        self.assertEqual(m["rows"]["positions"], 1)
        self.assertEqual(m["integrity"], "ok")
        self.assertEqual(m["day"], "20261102")

    def test_refuses_overwrite_without_force(self):
        cmd_export(self.db, self.dir, self.zip, "20261102")
        before = os.path.getsize(self.zip)
        self.assertEqual(cmd_export(self.db, self.dir, self.zip, "20261102"), EXIT_REFUSED)
        self.assertEqual(os.path.getsize(self.zip), before, "重名导出把旧取证覆盖了（证据不可再生）")
        self.assertEqual(cmd_export(self.db, self.dir, self.zip, "20261102", force=True), EXIT_OK)


class TestStopCmd(unittest.TestCase):
    def test_empties_existing_cmd_file_atomically(self):
        d = tempfile.mkdtemp(prefix="legstop_")
        cmd = os.path.join(d, "bridge_cmd.json")
        with open(cmd, "w", encoding="utf-8") as f:
            json.dump({"items": [{"seq": "seq:1", "kind": "order"}]}, f)
        db = fresh_db()
        self.assertEqual(cmd_stop_cmd(cmd, db), EXIT_OK)
        with open(cmd, encoding="utf-8") as f:
            self.assertEqual(json.load(f), {"items": []})
        self.assertFalse(os.path.exists(cmd + ".tmp"), "临时文件没清掉")

    def test_idempotent_when_file_absent(self):
        d = tempfile.mkdtemp(prefix="legstop_")
        cmd = os.path.join(d, "bridge_cmd.json")
        db = fresh_db()
        self.assertEqual(cmd_stop_cmd(cmd, db), EXIT_OK)
        self.assertFalse(os.path.exists(cmd), "文件不存在时不该凭空造一个")

    def test_reports_stranded_pending_rows(self):
        # 清的是"已写给桥还没执行"的清单；账上还剩几行未交付必须如实报（这里 db 有 1 行 pending）
        d = tempfile.mkdtemp(prefix="legstop_")
        cmd = os.path.join(d, "bridge_cmd.json")
        code = cmd_stop_cmd(cmd, fresh_db())
        self.assertEqual(code, EXIT_OK)


class TestCLIAnchorsAreAscii(unittest.TestCase):
    def _run(self, args):
        p = subprocess.run([sys.executable, CLI] + args, stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE, universal_newlines=True)
        return p.returncode, p.stdout

    def test_verify_anchor(self):
        rc, out = self._run(["verify", "--db", fresh_db()])
        line = out.strip().splitlines()[-1]
        self.assertEqual(rc, EXIT_OK)
        self.assertTrue(line.startswith("LEDGER_VERIFY integrity=ok"), line)
        self.assertTrue(all(ord(c) < 128 for c in line), "锚点含非 ASCII：PS 管道会换成 ?")

    def test_unknown_subcommand_exit_nonzero(self):
        rc, _out = self._run(["nope"])
        self.assertNotEqual(rc, EXIT_OK)


if __name__ == "__main__":
    unittest.main()
