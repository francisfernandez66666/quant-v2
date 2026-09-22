# backup_snap.py — 广州机 SQLite 一致性快照 + 完整性校验（HARDENING 件1，拉取模式的服务端半边）
# 由 backup_snapshot.ps1 调用，每个库调一次。
#
# 用法（§P0-B 2026-09-23 起参数化，此前 SRC/DST 硬编码只支持 trading.db 一个库）：
#   python backup_snap.py <src_db> <dst_db>
#   python backup_snap.py C:\var\lib\quant-trading-v2\trading.db C:\var\lib\quant-snapshot\trading.db
#   python backup_snap.py C:\var\lib\quant-trading-v2\live.db    C:\var\lib\quant-snapshot\live.db
#
# 为什么必须走 sqlite3 backup API、禁止裸 cp / shutil.copy（本文件存在的首要理由）：
#   两个库都是 WAL 模式，且引擎 7×24 在写（live.db=实盘持仓/委托/成交/资产，
#   cmd/quant/main.go:266-278 打开；trading.db=夜间研究库）。WAL 下主库文件在 checkpoint
#   之前**不包含** -wal 里的最新页：裸拷贝会在"写主库页 + 写 -wal"之间取到一个撕裂状态，
#   产物大小看着正常、能打开，但要么 PRAGMA integrity_check 直接报错（好的一面），
#   要么就是"打开得起来、账本记录却错位/半笔"（坏的一面——恢复时才发现，且已无第二次备份）。
#   backup API 走只读事务 + 逐页复制，产出**事务一致**的 standalone 单文件，既不需要停引擎，
#   也不需要 wal_checkpoint 独占，快照本身也不再依赖 -wal/-shm 文件。
#
# 退出码（调用方按非零即失败处理，不得降级报成功）：
#   0 成功；2 参数用法错/源库不存在；3 integrity_check 未过（产物已删除，不留下一个坏快照）
#
# 编码约束：注释可用中文（CPython3 源文件默认按 UTF-8 读），但**打印必须保持 ASCII**——
#   本脚本在 Windows 计划任务（SYSTEM，无控制台）下跑，stdout 编码按系统 ANSI 代码页（cp936）
#   走，中文输出有 UnicodeEncodeError 风险，且 PS 侧会把这行写进 _snapshot.log。
import sqlite3
import sys
from pathlib import Path

USAGE = "usage: python backup_snap.py <src_db> <dst_db>"


def main(argv) -> int:
    if len(argv) != 3:
        print(USAGE, file=sys.stderr)
        return 2
    src_path = Path(argv[1])
    dst_path = Path(argv[2])
    if not src_path.exists():
        # 源库不存在不是"跳过"：备份对象集合必须由调用方（backup_snapshot.ps1）保证完整，
        # 静默跳过会让"广州侧少了 live.db"这类状态重新变成不可见（§P0-B 定性即此）。
        print(f"missing source db: {src_path}", file=sys.stderr)
        return 2
    dst_path.parent.mkdir(parents=True, exist_ok=True)
    if dst_path.exists():
        dst_path.unlink()
    src = sqlite3.connect(str(src_path))
    dst = sqlite3.connect(str(dst_path))
    try:
        with dst:
            src.backup(dst)
        ok = dst.execute("PRAGMA integrity_check;").fetchone()[0]
        if ok != "ok":
            # 校验不过就地删除：宁可今晚没有快照（Mac 拉取器会因 SNAPSHOT_OK 非 ok 告警），
            # 也不能把一个坏快照留在中转链路上被当成"有备份"。
            print(f"integrity_check failed: {ok}", file=sys.stderr)
            dst.close()
            dst = None
            src.close()
            src = None
            try:
                dst_path.unlink()
            except OSError:
                pass
            return 3
        print(f"snapshot ok src={src_path.name} size={dst_path.stat().st_size}")
        return 0
    finally:
        if dst is not None:
            dst.close()
        if src is not None:
            src.close()


if __name__ == "__main__":
    sys.exit(main(sys.argv))
