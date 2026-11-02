#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.leg_ops —— 远端执行腿的"账本可交、待发可清"三件套（§FRIEND-PROFILE 批 3）。

为什么又是 Python 而不是 PowerShell 直接写：
  这三件事的判据都碰**证据**，而证据类判据最容易在脚本里写歪且歪了不出声：
    ① 库坏没坏要 `PRAGMA integrity_check`，不是"文件存在就算能导出"；
    ② 行数"数不出"必须显式 `-1/unknown`，不许折成 0（§CAL-COVER 的"数不出 ≠ 没落后"同口径）；
    ③ 导出正在被网关写的库要用 SQLite 在线备份 API，直接拷文件出来的副本可以是撕裂页——
       拿它当"换机前的唯一凭据"就是假凭据。
  所以真源在这，ps1 只调 `verify|export|stop-cmd` 三个子命令并按退出码分流。

三条边界：
  B-N9  默认**不动**任何账本文件；`uninstall` 的删除面由调用方（ps1）决定，本模块只提供判定与只读计数。
  B-N11 换机/重装/卸载三类动作前必须先 `export` 成功——`export` 在 `verify` 不 ok 时**拒绝产出**，
        绝不生成一份"看起来有、实际撕了"的证据包。
  覆盖保护  导出件重名即拒（证据不可再生，静默覆盖等于销毁一次取证）。

输出锚点一律**纯 ASCII**：中文经 PowerShell 管道会被码页换成 `?`，
带中文的锚点会让上层判读**永远不匹配**而流程照样往下走（2026-11-02 现网两次假象同源）。
"""
from __future__ import annotations

import argparse
import json
import os
import sqlite3
import sys
import tempfile
import time
import zipfile

EXIT_OK = 0
EXIT_REFUSED = 30          # 判据不过 / 拒绝覆盖 / 库不可读
TABLES = ("orders", "fills", "real_positions")


def _row_counts(db_path):
    """逐表数行数；任何一层读不到都记 -1（unknown），绝不冒充 0。

    为什么较真这个 -1：owner 的 Q9 把证据真源放到朋友机器上，广州不留副本；
    导出包里的行数就是"这台机器替谁守过账"的唯一说明。0 和"数不出"在复盘时是两件事——
    前者是"当天确实没交易"，后者是"我们的取证不可信"，折成一个数就会把后者洗掉。
    """
    out = {t: -1 for t in TABLES}
    if not db_path or not os.path.exists(db_path):
        return out, "missing"
    try:
        conn = sqlite3.connect("file:%s?mode=ro" % db_path, uri=True)
    except sqlite3.Error:
        return out, "unreadable"
    try:
        try:
            have = {str(r[0]) for r in conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table'")}
        except sqlite3.Error:
            # "file is not a database" 走这里：整套保持 -1（数不出），并把原因如实带出去
            return out, "unreadable"
        for t in TABLES:
            if t not in have:
                continue  # 表不存在 ⇒ 保持 -1（数不出），不是 0
            try:
                out[t] = int(conn.execute("SELECT COUNT(*) FROM %s" % t).fetchone()[0])
            except sqlite3.Error:
                out[t] = -1
        return out, "ok"
    finally:
        conn.close()


def _integrity(db_path):
    """`PRAGMA integrity_check` 的三态：ok / corrupt / unreadable（missing 单独归 unreadable）。"""
    if not db_path or not os.path.exists(db_path):
        return "missing"
    try:
        conn = sqlite3.connect("file:%s?mode=ro" % db_path, uri=True)
    except sqlite3.Error:
        return "unreadable"
    try:
        rows = conn.execute("PRAGMA integrity_check").fetchall()
    except sqlite3.Error:
        conn.close()
        return "unreadable"
    conn.close()
    if len(rows) == 1 and str(rows[0][0]).lower() == "ok":
        return "ok"
    return "corrupt"


def _dispatch_counts(db_path):
    """dispatch 表按状态计数（只读）。停腿时要让操作人看到"还剩几行没交付"。"""
    res = {"pending": -1, "inflight": -1}
    if not db_path or not os.path.exists(db_path):
        return res
    try:
        conn = sqlite3.connect("file:%s?mode=ro" % db_path, uri=True)
    except sqlite3.Error:
        return res
    try:
        for status, n in conn.execute("SELECT status, COUNT(*) FROM dispatch GROUP BY status"):
            if status in res:
                res[str(status)] = int(n)
        # 表里没有这两种状态＝真的没有待发（与"数不出"分家：查询成功才是 0）
        for k in res:
            if res[k] == -1:
                res[k] = 0
    except sqlite3.Error:
        pass
    finally:
        conn.close()
    return res


def cmd_verify(db_path):
    """只读体检：库坏没坏 + 三表行数。打印锚点，返回退出码。"""
    integ = _integrity(db_path)
    counts, _why = _row_counts(db_path)
    print("LEDGER_VERIFY integrity=%s orders=%d fills=%d positions=%d db_tail=%s" % (
        integ, counts["orders"], counts["fills"], counts["real_positions"],
        os.path.basename(str(db_path))[-16:]))
    return EXIT_OK if integ == "ok" else EXIT_REFUSED


def cmd_export(db_path, report_dir, out_zip, day, force=False):
    """出一份**可信**的证据包：在线备份库 + 桥回报文件 + 清单。verify 不过就拒绝产出。"""
    integ = _integrity(db_path)
    counts, why = _row_counts(db_path)
    if integ != "ok":
        print("LEDGER_EXPORT ok=0 reason=integrity_%s counts_db=%s" % (integ, why))
        return EXIT_REFUSED
    if os.path.exists(out_zip) and not force:
        # 覆盖一份旧取证 = 销毁它。要重出必须显式 force（人工决定），不给"顺手再来一次"留路。
        print("LEDGER_EXPORT ok=0 reason=refuse_overwrite out=%s" % os.path.basename(out_zip))
        return EXIT_REFUSED

    backup_fd, backup_path = tempfile.mkstemp(prefix="legdb_", suffix=".db")
    os.close(backup_fd)
    os.unlink(backup_path)  # 备份 API 要求目标不存在
    entries, bytes_total = [], 0
    try:
        src = sqlite3.connect(db_path)
        dst = sqlite3.connect(backup_path)
        with dst:
            src.backup(dst)  # 在线备份：撕裂页不会进包（copy 文件会）
        src.close()
        dst.close()
        os.makedirs(os.path.dirname(os.path.abspath(out_zip)) or ".", exist_ok=True)
        manifest = {
            "kind": "qmt_friend_ledger",
            "schema": 1,
            "day": str(day or ""),
            "made_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "integrity": integ,
            # 键名与 CLI 锚点行同源（orders/fills/positions）；库里那张表叫 real_positions，
            # 但取证包给人读，两处两套名字早晚有人换算错
            "rows": {"orders": counts["orders"], "fills": counts["fills"],
                     "positions": counts["real_positions"]},
            "db_bytes": int(os.path.getsize(backup_path)),
        }
        with zipfile.ZipFile(out_zip, "w", zipfile.ZIP_DEFLATED) as z:
            z.write(backup_path, "data.db")
            entries.append("data.db")
            # 回报原文一起进包：事后核对成交要的是"当时收到的那一行"，不是重算出来的表
            for name in ("bridge_report.jsonl", "bridge_report_dead.jsonl", "bridge_seen.jsonl"):
                p = os.path.join(report_dir or "", name)
                if report_dir and os.path.exists(p):
                    z.write(p, name)
                    entries.append(name)
            z.writestr("manifest.json", json.dumps(manifest, ensure_ascii=False, sort_keys=True))
            entries.append("manifest.json")
        bytes_total = int(os.path.getsize(out_zip))
    except (sqlite3.Error, OSError, zipfile.BadZipFile) as e:
        print("LEDGER_EXPORT ok=0 reason=%s detail=%s" % ("write_failed", type(e).__name__))
        return EXIT_REFUSED
    finally:
        if os.path.exists(backup_path):
            try:
                os.remove(backup_path)
            except OSError:
                pass

    print("LEDGER_EXPORT ok=1 entries=%d bytes=%d integrity=ok orders=%d fills=%d positions=%d" % (
        len(entries), bytes_total, counts["orders"], counts["fills"], counts["real_positions"]))
    return EXIT_OK


def cmd_stop_cmd(cmd_path, db_path):
    """把待发命令文件**原子写空**，并报告账上还剩几行未交付。

    为什么这一步属于撤钥匙的顺序里：`bridge_cmd.json` 是"已经写给桥、桥还没执行"的清单，
    只停服务不清这个文件，机器下次起来仍会把旧指令执行一遍——钥匙都撤了单照下，
    那是 B-N4 的反面。写空复用网关自己的 tmp + os.replace 形态（半截文件会让桥读成异常）。
    """
    emptied = 0
    existed = 1 if os.path.exists(cmd_path) else 0
    if existed:
        tmp = cmd_path + ".tmp"
        try:
            with open(tmp, "w", encoding="utf-8") as f:
                f.write(json.dumps({"items": []}))
                f.flush()
                os.fsync(f.fileno())
            os.replace(tmp, cmd_path)
            emptied = 1
        except OSError as e:
            print("LEG_STOP ok=0 reason=write_failed detail=%s" % type(e).__name__)
            try:
                if os.path.exists(tmp):
                    os.remove(tmp)
            except OSError:
                pass
            return EXIT_REFUSED
    d = _dispatch_counts(db_path)
    print("LEG_STOP ok=1 cmd_emptied=%d cmd_existed=%d pending_rows=%d inflight_rows=%d" % (
        emptied, existed, d["pending"], d["inflight"]))
    return EXIT_OK


def main(argv=None):
    # 三个子命令都只有"读 + 至多写导出包/命令文件"这一种副作用；没有 del/drop/truncate 类操作。
    # 退出码语义固定（0 可交、30 拒绝），调用方（ps1）只按码分流、不再自己解释文本——
    # 文本给人看，码给机器判，两者混用的那次一定会变成"人改了文案，判读静默失效"。
    ap = argparse.ArgumentParser(description="friend-leg ledger & pending ops (read-mostly)")
    sub = ap.add_subparsers(dest="op", required=True)
    v = sub.add_parser("verify")
    v.add_argument("--db", required=True)
    e = sub.add_parser("export")
    e.add_argument("--db", required=True)
    e.add_argument("--report-dir", default="")
    e.add_argument("--out", required=True)
    e.add_argument("--day", default="")
    e.add_argument("--force", action="store_true")
    s = sub.add_parser("stop-cmd")
    s.add_argument("--cmd", required=True)
    s.add_argument("--db", default="")
    a = ap.parse_args(argv)
    if a.op == "verify":
        return cmd_verify(a.db)
    if a.op == "export":
        return cmd_export(a.db, a.report_dir, a.out, a.day, force=a.force)
    return cmd_stop_cmd(a.cmd, a.db)


if __name__ == "__main__":
    sys.exit(main())
