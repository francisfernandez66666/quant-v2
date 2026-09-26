#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§0926E2E-W2D（2026-09-26 二波）outbox 溢出「冻结不删」行为用例。

旧语义（缺陷 8）：落库行数超 max_outbox 时 `_push` 自动删最旧——断线期间的**早期成交
回报**被静默销毁，决策端账本与柜台从此对不平，且除了运行日志里一行 error 外无任何留痕。
新语义：溢出只告警冻结（跨水位即时一条 + 每加深 50 行追一条），深度经 /health
outbox_depth 暴露，删除动作只存在于人工 CLI（outbox_admin.py，--keep 必填 + --yes 才删）。

四件事逐条钉：
  ① 超限入队 N 条 → 行数仍是 N（零删除，资金事实不可再生）；
  ② 告警节拍：跨水位那条必吵、水位下不得出现"冻结"告警（反证闸不误伤）；
  ③ CLI dry-run 零删除 / --yes 按保留量删最旧 / --keep 缺失拒执行；
  ④ store.outbox_trim 本体语义不变（保留最新 cap 行）——CLI 复用它，行为回归在此兜底。
"""
import logging
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from handler import ReportHandler  # noqa: E402
from store import Store  # noqa: E402
import outbox_admin  # noqa: E402


def _fresh_store():
    fd, path = tempfile.mkstemp(suffix=".db")
    os.close(fd)
    os.unlink(path)
    return Store(path), path


def _push_events(h, n):
    for i in range(n):
        h._push({"type": "tick", "n": i})


class _Cap(logging.Handler):
    def __init__(self):
        super().__init__(level=logging.WARNING)
        self.msgs = []

    def emit(self, record):
        self.msgs.append(record.getMessage())


def _capture(cap_name="qmt_gateway.handler"):
    logger = logging.getLogger(cap_name)
    h = _Cap()
    return logger, h


def test_overflow_keeps_everything_and_warns_once():
    st, _ = _fresh_store()
    h = ReportHandler(st, "http://sink.invalid/api/qmt/report", "tok", max_outbox=3)
    logger, cap = _capture()
    old = logger.level
    # §全局静音对策（先例 test_claim_release）：部分存量测试模块 import 期 logging.disable(CRITICAL)，
    # 同进程跑到这里会吞掉 WARNING → "冻结"告警抓空＝假红；用例期间解除、结束还原原值。
    prev_disable = logging.Logger.manager.disable
    logging.disable(logging.NOTSET)
    logger.addHandler(cap)
    logger.setLevel(logging.WARNING)
    try:
        _push_events(h, 5)
    finally:
        logger.removeHandler(cap)
        logger.setLevel(old)
        logging.disable(prev_disable)
    assert st.outbox_count() == 5, "溢出必须冻结零删除（旧语义删最旧=销毁成交回报）"
    frozen = [m for m in cap.msgs if "冻结" in m]
    assert len(frozen) == 1, "跨水位即时告警恰好一条（节拍：后续每加深 50 行才追）；实得 %s" % frozen
    assert "outbox_admin" in frozen[0], "告警必须指向人工收敛入口，否则运维只能盲 chmod 磁盘"


def test_below_watermark_stays_silent():
    st, _ = _fresh_store()
    h = ReportHandler(st, "http://sink.invalid/api/qmt/report", "tok", max_outbox=10)
    logger, cap = _capture()
    # 负向用例同样要解除全局静音：否则"抓不到"是静音所致而非代码不告警，静过＝假绿。
    prev_disable = logging.Logger.manager.disable
    logging.disable(logging.NOTSET)
    logger.addHandler(cap)
    logger.setLevel(logging.WARNING)
    try:
        _push_events(h, 5)
    finally:
        logger.removeHandler(cap)
        logging.disable(prev_disable)
    assert [m for m in cap.msgs if "冻结" in m] == [], "水位下不得有冻结告警（反证：闸不误伤常态）"
    assert st.outbox_count() == 5


def test_cli_dry_run_deletes_nothing_and_requires_keep():
    st, path = _fresh_store()
    _push_events(ReportHandler(st, "http://sink.invalid", "tok"), 5)
    # 缺 --keep：拒执行（删除没有默认量是设计）
    rc = outbox_admin.main(["--store", path, "--yes"])
    assert rc == 2 and st.outbox_count() == 5, "--keep 缺失必须拒执行且零删除"
    # 带 --keep 无 --yes：dry-run 零删除
    rc = outbox_admin.main(["--store", path, "--keep", "2"])
    assert rc == 0 and st.outbox_count() == 5, "无 --yes 一律 dry-run"


def test_cli_yes_keeps_newest():
    st, path = _fresh_store()
    _push_events(ReportHandler(st, "http://sink.invalid", "tok"), 5)
    rc = outbox_admin.main(["--store", path, "--keep", "2", "--yes"])
    assert rc == 0
    assert st.outbox_count() == 2, "--yes 后按保留量收敛"
    rows = [st.outbox_oldest()]
    # 删的是最旧：留下最新两行 ⇒ 队首 payload 应为 n=3（n=0..4 中的后两条）。
    # 注意 outbox_oldest 返回的是**解析后的 dict**（§探针取值链纪律：按真实返回形态断，不猜文本）。
    assert rows[0][1]["n"] == 3, "必须删最旧保最新，实得 %s" % rows


def test_trim_semantics_unchanged():
    st, _ = _fresh_store()
    _push_events(ReportHandler(st, "http://sink.invalid", "tok"), 5)
    assert st.outbox_trim(10) == 0, "未超保留量零删除"
    assert st.outbox_trim(2) == 3
    assert st.outbox_count() == 2
