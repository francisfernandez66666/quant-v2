# ── qmt_gateway 测试会话前置 conftest.py（§BRIDGE-TRACE，2026-09-24）──
# 解决什么：任务 #43 登记的"仓库根反复长出怪文件名"——`C:\qmt\quant-trading-v2\qmt_gateway\bridge_boot.log`
# 字面带反斜杠的**单个文件**。根因不是权限也不是路径写错，而是桥策略模块把 Windows 目录常量
# 用字面反斜杠拼路径（qmt_bridge_strategy.py 的 BRIDGE_DIR/TRACE_PATH 等五个）：
# 在 macOS/Linux 上 `open("C:\\...\\bridge_boot.log", "ab")` **不会失败**，它就在当前工作目录
# 里创建一个名字里带反斜杠的文件。于是 `test_deal_direction`（未知方向组合要留痕一次）和
# `test_bridge_strategy_adapter`（_handle_cmd/_record_seen 走真 _trace）每跑一次就在仓库根
# 生成一个垃圾文件，同时把本该看到的 trace 丢在没人看的地方。09-23/09-24 各搬走过一次，又长回来。
#
# 口径：不改生产行为——广州那台机器上没有 QMT_BRIDGE_DIR，取值仍是原样的
# `C:\qmt\quant-trading-v2\qmt_gateway`；只在测试会话里把桥目录指到临时目录。
# 为什么写在 conftest：pytest 保证 conftest 先于任何 test 模块导入，而 `bs` 是在测试模块
# 顶层 import 时就读定这五个常量的——晚设 env 就来不及了。
# English: redirect the bridge's five file paths into a temp dir for the whole test session,
# so importing the GBK-sandbox strategy module can no longer create a backslash-named junk
# file in the repository root. Unset in production, where the Windows default still applies.
import os
import shutil
import tempfile

_TMP = tempfile.mkdtemp(prefix="qmt-bridge-test-")
os.environ["QMT_BRIDGE_DIR"] = _TMP


def pytest_unconfigure(config):
    """会话结束（含 Ctrl-C）后回收临时桥目录：测试产物一律不落仓库工作树。"""
    try:
        shutil.rmtree(_TMP, ignore_errors=True)
    except Exception:
        pass
