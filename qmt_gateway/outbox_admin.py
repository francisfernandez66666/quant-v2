#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""outbox_admin.py —— §0926E2E-W2D（2026-09-26 二波）持久化回报 outbox 的人工收敛 CLI。

存在理由：outbox 溢出语义已从"自动删最旧"改为"冻结+告警"（成交回报是资金事实，
静默销毁=决策端账本与柜台永久对不平且无人知晓）。冻结后必须有**人驱动的**收敛入口，
否则长期断链的网关磁盘无界增长。删除动作被刻意设计成最难被误触的样子：

    默认 dry-run：只报告"当前多少行、保留最新 N 行会删掉多少、最旧行时间戳"，不落一删；
    真删必须显式 --yes，且 --keep 必填（没有"清空全库"的顺手形态——全清请先 keep 0，
    把决定权逼到显式数字上）。

用法：
    python outbox_admin.py --store <gateway.db 路径> --keep 2000            # 预览
    python outbox_admin.py --store <gateway.db 路径> --keep 2000 --yes     # 执行
退出码：0 成功；2 参数错误（keep 缺失/为负）；3 库不可读。
"""
import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from store import Store  # noqa: E402


def main(argv=None):
    parser = argparse.ArgumentParser(description="durable outbox 人工收敛（§0926E2E-W2D）")
    parser.add_argument("--store", required=True, help="网关 SQLite 账本路径（store.db）")
    parser.add_argument("--keep", type=int, default=None,
                        help="保留最新 N 行（必填；删除动作没有默认量，防止顺手清库）")
    parser.add_argument("--yes", action="store_true",
                        help="确认执行删除；不带此flag 一律 dry-run")
    args = parser.parse_args(argv)

    if args.keep is None:
        print("[outbox-admin] 必须显式给出 --keep N（保留最新 N 行）——不给默认量是设计而非疏忽", file=sys.stderr)
        return 2
    if args.keep < 0:
        print("[outbox-admin] --keep 不能为负", file=sys.stderr)
        return 2
    if not os.path.exists(args.store):
        print("[outbox-admin] 账本不存在: %s" % args.store, file=sys.stderr)
        return 3

    st = Store(args.store)
    depth = st.outbox_count()
    print("[outbox-admin] 当前 outbox 深度=%d；口径=保留最新 %d 行" % (depth, args.keep))
    if depth <= args.keep:
        print("[outbox-admin] 无需收敛（深度未超保留量），零操作退出")
        return 0
    would_delete = depth - args.keep
    if not args.yes:
        print("[outbox-admin] DRY-RUN：将删除最旧 %d 行。**确认这 %d 条回报在决策端确已落账后，"
              "重跑并追加 --yes**" % (would_delete, would_delete))
        return 0
    deleted = st.outbox_trim(args.keep)
    print("[outbox-admin] 已删除最旧 %d 行（审计：本命令输出请粘贴进当日运维记录）；剩余 %d 行" % (deleted, st.outbox_count()))
    return 0


if __name__ == "__main__":
    sys.exit(main())
