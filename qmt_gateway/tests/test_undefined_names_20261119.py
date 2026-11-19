# -*- coding: utf-8 -*-
"""载体产物「未定义全局名」静态锁（2026-11-19 P0 事故回归）。

事故：07dad02 加的 CASH-PROBE 在 run_round 里写成
`_probe_cash_line(..., ctx, params)`——`params` 只是那个函数自己的形参名，模块
根本没有这个全局名。现象不是"探针不打印"，而是**整轮在第一笔买入候选处抛
NameError**，handlebar 的 `round error swallowed` 把它吞掉：
2026-11-19 09:31–09:38 共 73 次，当天零委托，卖出腿也被一起截断。

为什么 py_compile / 构建闸抓不到：语法完全合法，名字只在运行到那一条语句时才查。
所以这里的判据是 `symtable`：模块顶层的绑定集 + builtins + QMT 注入的四个沙箱函数
之外，任何 `is_global()` 且没人赋值的名字都是必然的 NameError。

为什么必须在**产物**上判而不是源文件：源文件里的 `rule_*` / `day_key` / `normalize_tick`
等名字由 scripts/build_local_executor.py 在烘焙时注入，扫源文件会得到 48 条假阳性。
产物才是真正被 QMT 装载的那份字节。
"""
import builtins
import os
import subprocess
import symtable
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GATEWAY = os.path.dirname(HERE)
REPO = os.path.dirname(GATEWAY)
BUILDER = os.path.join(REPO, "scripts", "build_local_executor.py")

# QMT 终端在策略进程里注入的四个交易函数（_resolve_sandbox_fns 用 try/except
# NameError 逐个探测）。它们没有模块级赋值是**设计如此**：拿不到就降级成 None。
# English: functions injected by the QMT terminal at runtime; their absence at module
# scope is by design (resolved through try/except NameError).
INJECTED_FNS = {"passorder", "cancel", "get_trade_detail_data", "get_account"}


def _undefined_globals(src, path="<artifact>"):
    """返回 [(函数名, 未定义全局名), ...]；空列表 = 这份源码不会 NameError。"""
    st = symtable.symtable(src, path, "exec")
    top = {s.get_name() for s in st.get_symbols()}
    known = top | set(dir(builtins)) | INJECTED_FNS
    found = set()

    def walk(table):
        for sym in table.get_symbols():
            if sym.is_global() and not sym.is_assigned() and sym.get_name() not in known:
                found.add((table.get_name(), sym.get_name()))
        for child in table.get_children():
            walk(child)

    walk(st)
    return sorted(found)


def _build_sandbox(out_dir):
    proc = subprocess.run([sys.executable, BUILDER, "--target", "sandbox",
                           "--out-dir", out_dir],
                          capture_output=True, text=True, cwd=REPO)
    if proc.returncode != 0:
        raise AssertionError("sandbox build failed: %s" % (proc.stdout + proc.stderr)[-800:])
    for token in proc.stdout.replace("\n", " ").split():
        if token.startswith("artifact="):
            return token.split("=", 1)[1]
    raise AssertionError("artifact= not reported: %s" % proc.stdout[-400:])


class UndefinedNamesTest(unittest.TestCase):
    def test_baked_artifact_has_no_undefined_global(self):
        """产物里不允许出现"用到但从未绑定"的全局名（事故形状：裸 `params`）。"""
        out = tempfile.mkdtemp()
        artifact = _build_sandbox(out)
        src = open(artifact, encoding="utf-8", errors="replace").read()
        bad = _undefined_globals(src, artifact)
        self.assertEqual([], bad, "产物存在未定义全局名（运行时必 NameError）: %r" % (bad,))

    def test_checker_catches_the_incident_shape(self):
        """反证：把事故那一行原样放进合成模块，判据必须报出来。

        恒绿用例是这类静态锁最常见的死法——判据自己坏了也会打印"通过"。
        English: mutation proof — the checker must flag the exact incident shape.
        """
        src = (
            "PARAMS = {'x': 1}\n"
            "\n"
            "def _probe(short, amount, ctx, params):\n"
            "    return params\n"
            "\n"
            "def run_round():\n"
            "    ctx = {}\n"
            "    _probe('x', 1, ctx, params)\n"
        )
        bad = _undefined_globals(src, "<mutation>")
        self.assertIn(("run_round", "params"), bad)


if __name__ == "__main__":
    unittest.main()
