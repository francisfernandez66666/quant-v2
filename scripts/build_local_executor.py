#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""scripts/build_local_executor.py -- bake the local executor artifact.

The broker rule effective 2026-11-05 splits the system in two: the QMT
built-in strategy (sandboxed, no file I/O, no network) and a LOCAL ordinary
Python process on the physical Windows box (venv, file I/O, network). The
local process is what actually computes the signals and places the orders.

That split creates a new failure mode this script exists to prevent: the
parameters live in one place and the code in another, and a knob can be
changed in the code without re-baking (or the other way round). The result
is a deployed artifact whose behaviour nobody can reconstruct from the
repository.

So: PARAMS is BAKED. This script reads the JSON parameter file, renders
the PARAMS constant table plus STRATEGY_VERSION into the executor source,
and then REFUSES to publish the artifact unless five static gates pass:

  G1  the artifact contains no non-ASCII byte (it runs on a GBK console);
  G2  the artifact calls nothing outside the documented allow-list (no
      requests, no socket, no urllib, no subprocess, no eval -- the rule
      artifact must not grow a side channel nobody reviews);
  G3  the artifact contains no f-string (the executor is printed and
      parsed by PowerShell pipes whose code page mangles braces);
  G4  STRATEGY_VERSION exists and is referenced by the remark builder
      (the version must actually reach the broker remark, not just sit in
      a constant);
  G5  no rule-layer function body contains a numeric literal (every
      threshold must come from PARAMS).

Each failing gate exits non-zero AND leaves no half-written artifact
behind: the artifact is assembled in memory, validated, and only then
written -- so a failed build never leaves a stale-looking file on disk.

The artifact lands OUTSIDE the repository working tree by default (the
repo has a worktree-drift gate: a build product inside the tree turns a
green gate red). Override with LOCAL_EXECUTOR_OUT_DIR.

Usage:
  scripts/build_local_executor.py                       # build from the default params
  scripts/build_local_executor.py --params <file.json>  # build from another params file
  scripts/build_local_executor.py --out-dir <dir>       # where to publish (outside the repo)
  scripts/build_local_executor.py --print-gates         # show the gate definitions and exit

Read-only anchors (pure ASCII, stable):
  BUILD_RESULT=OK version=... sha256=... artifact=...
  BUILD_RESULT=ABORT reason=<gate> detail=...
(English: bake the local executor artifact and gate it.)
"""

import argparse
import ast
import hashlib
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.dirname(HERE)
DEFAULT_PARAMS = os.path.join(REPO_ROOT, "qmt_gateway", "local_executor_params.json")
DEFAULT_SOURCE = os.path.join(REPO_ROOT, "qmt_gateway", "local_executor.py")

# Where the baked artifact is published. Must live outside the repository
# working tree: the repo gate treats a new file inside the tree as drift.
DEFAULT_OUT_DIR = os.path.join(
    os.environ.get("TMPDIR", "/tmp"), "qmt-local-executor-build")

# The artifact name embeds the version so a published file is traceable
# to the parameter set that produced it.
ARTIFACT_PREFIX = "local_executor_"
ARTIFACT_SUFFIX = ".py"

# ---------------------------------------------------------------------------
# G2 -- the call allow-list.
#
# The executor is a RULE module first and a program second. The rule
# layer must be pure logic, and the program part must stay inside the
# smallest surface that the job needs: read a parameter file, keep a
# local sqlite journal, write a log line, talk to the local QMT SDK.
#
# Two lists, because there are two questions:
#
#   ALLOWED_ROOTS  -- which names may be called at all. This is the
#                     "no over-privileged call" lock: a call rooted at a
#                     name that is not on this list (requests, socket,
#                     urllib, subprocess, eval, ...) fails the build.
#                     Builtins are listed by name, the local QMT SDK
#                     entry points by name, everything else must be a
#                     local variable of the artifact itself.
#
#   ALLOWED_CALLS  -- for the names that DO carry a capability (the
#                     standard-library modules), the exact call shape is
#                     pinned. "os.path.join" is fine, "os.system" is not.
#                     A ".*" suffix pins the whole module.
#
# The list is deliberately tiny. Anything not on it is a gate failure,
# not a warning -- a silent "just this once" exception is how a rule
# artifact grows a side channel that nobody reviews.
# ---------------------------------------------------------------------------

ALLOWED_ROOTS = {
    # builtins used by the executor
    "abs": "numeric helper",
    "all": "collection helper",
    "any": "collection helper",
    "bool": "type helper",
    "bytes": "type helper",
    "dict": "type helper",
    "enumerate": "collection helper",
    "filter": "collection helper",
    "float": "type helper",
    "format": "formatting helper",
    "frozenset": "type helper",
    "getattr": "attribute read",
    "hasattr": "attribute probe",
    "int": "type helper",
    "isinstance": "type probe",
    "iter": "iterator helper",
    "len": "collection helper",
    "list": "type helper",
    "map": "collection helper",
    "max": "numeric helper",
    "min": "numeric helper",
    "next": "iterator helper",
    "open": "read/write the local state file",
    "print": "write the readout line",
    "range": "numeric helper",
    "repr": "diagnostic helper",
    "reversed": "collection helper",
    "round": "numeric helper",
    "set": "type helper",
    "sorted": "collection helper",
    "str": "type helper",
    "sum": "numeric helper",
    "tuple": "type helper",
    "zip": "collection helper",
    # builtins raised or caught by the artifact
    "RuntimeError": "own error type",
    "ValueError": "own error type",
    "TypeError": "own error type",
    "KeyError": "own error type",
    "IndexError": "own error type",
    "OSError": "own error type",
    "IOError": "own error type",
    "StopIteration": "own error type",
    # the local QMT SDK objects
    "StockAccount": "local QMT SDK account handle",
    "XtQuantTrader": "local QMT SDK trading session",
    # exceptions raised by the artifact itself
    "BrokerUnavailable": "own error type",
    "DataUnavailable": "own error type",
}

# Roots that DO carry a capability: the exact call shape is pinned.
# The key is MODULE-QUALIFIED, so "os.path.join" is fine and "os.system"
# is not.
ALLOWED_CALLS = {
    # json.load: read the parameter/calendar JSON files (read-only).
    "json.load": "read a JSON document",
    # json.dumps: render the params digest for the self-test line.
    "json.dumps": "render a JSON document",
    # hashlib.sha256: fingerprint the artifact for the updater read-back.
    "hashlib.sha256": "fingerprint the artifact",
    # sqlite3.connect: the local "what did we already send" journal.
    "sqlite3.connect": "open the local state journal",
    # os.environ.get: read configuration (account, paths).
    "os.environ.get": "read configuration",
    # os.path.*: build and probe paths.
    "os.path.join": "build a path",
    "os.path.dirname": "build a path",
    "os.path.isdir": "check a path",
    # os.path.exists: a pure stat, no capability -- the shadow-journal probe must be
    # able to answer "there is no journal here" WITHOUT opening (and therefore
    # creating) the file, because a probe that creates its subject turns "missing"
    # into "empty" on the next read (missing != empty, the same reason the probe
    # does not use LocalState()).
    "os.path.exists": "check a path",
    "os.path.abspath": "build a path",
    # os.makedirs: create the state/log directory.
    "os.makedirs": "create the state directory",
    # logging: the executor writes a log file.
    "logging.getLogger": "write the executor log",
    "logging.basicConfig": "write the executor log",
    "logging.StreamHandler": "write the executor log",
    "logging.FileHandler": "write the executor log",
    # time.time: stamp journal rows. time.sleep: pace the polling loop.
    "time.time": "stamp journal rows",
    "time.sleep": "pace the polling loop",
    # argparse: the command line surface.
    "argparse.ArgumentParser": "parse the command line",
    # datetime: the Beijing wall clock (imported by name from datetime).
    "datetime.now": "read the Beijing wall clock",
    "timedelta": "read the Beijing wall clock",
    "timezone": "read the Beijing wall clock",
    # sys.exit: end the process with a status code.
    "sys.exit": "end the process",
}

# Modules the artifact may import at all.
ALLOWED_IMPORTS = {
    "argparse": "command line",
    # §SANDBOX-TARGET：QMT 把 passorder/cancel/get_trade_detail_data 注在 builtins
    # 或模块 globals 里（不同券商构建两边都可能），取用它们必须走 builtins 查找。
    # 这是"读券商给的能力"，不是"打开外部世界"——文件与网络仍由 g2/g6 逐条拦。
    "builtins": "look up the functions the terminal injects",
    # §STATUS-SINGLE-SOURCE（2026-11-04）：委托状态词表与终态集合**不在执行器里另立一份**。
    # 真源是 handler.XT_STATUS_CODES（柜台 m_nOrderStatus → 中文账本字面量，与桥同一代码空间）
    # 和 store.TERMINAL_STATUSES（终态集合，close_gate 也 import 它）。
    # 抄一份子集会静默失效（本仓「单点判据」教训：保守方向的错不会响）。
    "handler": "order-status vocabulary (single source)",
    "store": "terminal-status set (single source)",
    "datetime": "clock",
    "hashlib": "fingerprint",
    "json": "read the parameter file",
    "logging": "log file",
    "os": "paths and environment",
    "sqlite3": "local state journal",
    "sys": "process exit",
    "time": "clock and pacing",
    "math": "pure numeric helpers (percentile/clamp), no IO and no network",
    "xtquant": "the local QMT SDK",
}

# Names the artifact may pull out of the local QMT SDK.
ALLOWED_XT_NAMES = {
    "xtdata": "read local QMT market data",
    "xttrader": "talk to the local QMT trading session",
    "xtconstant": "read local QMT order constants",
    "xttype": "read local QMT SDK types",
    "XtQuantTrader": "local QMT SDK trading session",
    "StockAccount": "local QMT SDK account handle",
}

# The local QMT SDK entry points the artifact may call.
ALLOWED_XT_METHODS = {
    "get_full_tick": "read local QMT market data",
    "get_market_data_ex": "read local QMT market data",
    "order_stock": "place an order",
    "cancel_order_stock": "cancel an order",
    "query_stock_orders": "read today's orders",
    # §PARTIAL-FILL-1106（方案 §5.2）：部成数量必须来自柜台成交表，委托表的
    # traded_volume 只是柜台自己回填的副本。撤回一单的剩余量要按真实成交算，
    # 用副本会把「已成交的那部分」也算进可撤余量。
    "query_stock_trades": "read today's trades (partial-fill truth)",
    "query_stock_asset": "read the account asset",
    "query_stock_positions": "read the positions",
    "start": "open the trading session",
    "connect": "open the trading session",
    "stop": "close the trading session",
    "subscribe": "subscribe to quotes",
    "subscribe_quote": "subscribe to quotes",
}

# Known-pure standard-library methods the artifact may call on the objects
# it holds (containers, strings, files, cursors, loggers).
SAFE_METHODS = {
    # container / mapping methods
    "add": "container method", "append": "container method", "clear": "container method",
    "copy": "container method", "count": "container method", "discard": "container method",
    "extend": "container method", "get": "mapping method", "index": "container method",
    "items": "mapping method", "keys": "mapping method", "pop": "container method",
    "remove": "container method", "setdefault": "mapping method", "update": "mapping method",
    "values": "mapping method",
    # string methods
    "capitalize": "string method", "center": "string method", "encode": "string method",
    "endswith": "string method", "find": "string method", "format": "string method",
    "join": "string method", "lower": "string method", "lstrip": "string method",
    "partition": "string method", "replace": "string method", "rstrip": "string method",
    "split": "string method", "rsplit": "string method", "startswith": "string method",
    "strip": "string method", "lstrip": "string method", "rstrip": "string method",
    "index": "sequence position", "rsplit": "string method", "split": "string method",
    "callable": "is the terminal-injected name a function",
    "ascii": "ASCII-safe log rendering (a GBK console eats CJK)",
    "isnan": "NaN test on a bar value (Go's math.IsNaN equivalent)",
    "isfinite": "finite test before a money comparison",
    "floor": "lot rounding", "ceil": "rounding up", "sqrt": "volatility math",
    "log": "logarithm (factor library)", "log1p": "logarithm (factor library)",
    "exp": "exponential (factor library)", "pow": "power", "fsum": "stable sum",
    "copysign": "sign transfer (alpha formulas)", "isclose": "float comparison",
    "fabs": "absolute value of a float",
    "get": "dict read", "startswith": "prefix test", "title": "string method", "upper": "string method",
    "zfill": "string method",
    # file / stream / cursor methods
    "close": "handle method", "commit": "journal method", "execute": "journal method",
    "fetchall": "journal method", "fetchone": "journal method", "read": "file method",
    "write": "file method", "flush": "file method",
    # logging methods
    "critical": "log method", "debug": "log method", "error": "log method",
    "exception": "log method", "info": "log method", "warning": "log method",
    # argparse methods
    "add_argument": "command line", "parse_args": "command line",
    # clock reads (no IO, no process control)
    "time": "clock read", "sleep": "poll pacing",
    # datetime methods
    "date": "date method", "isoformat": "date method", "strftime": "date method",
    "timestamp": "date method", "total_seconds": "date method", "weekday": "date method",
    # hashlib methods
    "hexdigest": "fingerprint method", "update": "fingerprint method",
}

# Capability modules that must never appear: network egress, process
# spawning, dynamic code loading. The executor talks to the LOCAL QMT SDK
# over local IPC and needs none of these. They are listed separately from
# ALLOWED_IMPORTS so the failure message names the real reason.
FORBIDDEN_MODULES = {
    "requests": "network egress",
    "socket": "network egress",
    "urllib": "network egress",
    "http": "network egress",
    "ftplib": "file transfer",
    "smtplib": "mail egress",
    "telnetlib": "network egress",
    "xmlrpc": "network egress",
    "subprocess": "process spawning",
    "multiprocessing": "process spawning",
    "ctypes": "raw foreign calls",
    "importlib": "dynamic import",
    "pickle": "dynamic code loading",
    "webbrowser": "side effects",
    "asyncio": "unexpected concurrency",
}

# Builtins that defeat a static review of what the artifact can do.
FORBIDDEN_BUILTINS = {
    "eval": "dynamic code execution",
    "exec": "dynamic code execution",
    "compile": "dynamic code execution",
    "__import__": "dynamic import",
    "globals": "namespace introspection",
    "locals": "namespace introspection",
    "vars": "namespace introspection",
    "input": "console interaction",
    "breakpoint": "debugger hook",
}

# G3 -- f-strings are banned outright. The executor's output crosses a
# PowerShell pipe whose code page rewrites braces, and the repo's own
# discipline is "no f-string, use % formatting" for exactly this reason.
FSTRING_RE = re.compile(r"""\bf["']""")

# G4 -- the version constant and the remark builder that must carry it.
VERSION_RE = re.compile(r"^v\d{2}-\d{8}$")
VERSION_CONST = "STRATEGY_VERSION"
REMARK_BUILDER = "build_remark"

# G5 -- the rule layer region markers.
RULE_LAYER_BEGIN = "# ==== RULE LAYER BEGIN ===="
RULE_LAYER_END = "# ==== RULE LAYER END ===="

# The marker rows in the executor source that the builder replaces.
BAKED_BEGIN = "# ==== BAKED PARAMS BEGIN ===="
BAKED_END = "# ==== BAKED PARAMS END ===="


def fail(reason, detail=""):
    """Abort the build with a stable, machine-readable reason."""
    sys.stdout.write("BUILD_RESULT=ABORT reason=%s detail=%s\n" % (reason, detail))
    sys.stdout.flush()
    return 2


def sha256_text(text):
    """SHA256 of a text blob, hex digest."""
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def render_params(params):
    """Render the PARAMS constant table as Python source.

    Keys are sorted so the same parameter file always produces the same
    bytes: the artifact checksum is only meaningful if the rendering is
    deterministic.
    """
    lines = ["PARAMS = {"]
    for key in sorted(params):
        # ascii() not %r: the derived table carries Chinese phase keys (emotion_scales is keyed
        # by the Go side's phase names, and emotion_phase_labels maps them to their codes).
        # %r emits those bytes verbatim, the GBK console on the box then turns each one into "?"
        # and the artifact no longer parses as the file it claims to be. ascii() keeps the exact
        # same Python value while writing only \uXXXX escapes -- so g1_ascii can stay a hard gate.
        lines.append("    %s: %s," % (ascii(key), ascii(params[key])))
    lines.append("}")
    return "\n".join(lines)


def bake(source, params, version):
    """Replace the baked-params block and the version constant.

    The source file carries a marker block; the builder rewrites what is
    between the markers and leaves the rest of the file untouched. This
    keeps the executor source readable and reviewable in the repository
    while the published artifact carries the real values.
    """
    begin = source.find(BAKED_BEGIN)
    end = source.find(BAKED_END)
    if begin < 0 or end < 0:
        raise ValueError("baked params markers not found in source")
    if end < begin:
        raise ValueError("baked params markers are inverted")
    body = source[:begin + len(BAKED_BEGIN)]
    body += "\n"
    # Double quotes on purpose: the source file spells the version the
    # same way, and the updater's read-back regex accepts either -- but
    # one spelling keeps "grep the version" unambiguous.
    body += 'STRATEGY_VERSION = "%s"\n' % version
    body += render_params(params)
    body += "\n"
    body += source[end:]
    return body



# ---------------------------------------------------------------------------
# PORTED RULE MODULES (owner 2026-11-04: all live factor/pattern/T/sizing rules
# must end up inside the py carriers, not only in the Go engine).
#
# One source, two carriers: the rule bodies live in qmt_gateway/rules_*.py and
# are INLINED into the published artifact between the PORTED RULES markers.
# Neither carrier can keep a hand-copied second version:
#   * the QMT built-in strategy is a single file and cannot import anything;
#   * the local executor would otherwise drift the moment a threshold is edited.
# Each module is gated on its own before it is inlined (ASCII / privileged calls
# / bare numbers inside rule_* functions), and every kind it registers must own
# a short code in params.signal_codes -- without a code the counter-remark
# idempotency check cannot vouch for that rule, and two rules sharing one code
# would let the first one's orders silence the second forever.
# ---------------------------------------------------------------------------
PORTED_MODULE_ORDER = ("rules_form", "rules_ladder", "rules_factor", "rules_t", "rules_sizing")
PORTED_BEGIN = "# ==== PORTED RULES BEGIN ===="
PORTED_END = "# ==== PORTED RULES END ===="


def read_pported_module(name):
    """Read one ported module, or None when it is not on this box yet."""
    path = os.path.join(REPO_ROOT, "qmt_gateway", name + ".py")
    if not os.path.exists(path):
        return None
    with open(path, "r", encoding="utf-8") as handle:
        return handle.read()


def ported_rules_table(source, name):
    """Return {kind: function_name} a module registers, by AST (never by exec).

    The build host must not execute a trading module: it has no xtquant, and
    running rules that can place orders just to read a table is its own risk.
    """
    import ast
    tree = ast.parse(source)
    out = {}
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        targets = [t.id for t in node.targets if isinstance(t, ast.Name)]
        if "RULES" not in targets or not isinstance(node.value, ast.Dict):
            continue
        for key, value in zip(node.value.keys, node.value.values):
            if isinstance(key, ast.Constant) and isinstance(value, ast.Name):
                out[str(key.value)] = value.id
    if not out:
        # 显式的空 RULES 是合法的：T 腿与仓位腿移植的是**判定**（能不能开回合、帽子多大），
        # 不产出带短码的委托信号。允许它，但要求模块自己写明"我没有信号种类"——
        # 判据是文件里有没有 `RULES = {}` 这一行；没有就是漏注册，照旧拒建。
        if re.search(r"^RULES\s*=\s*\{\s*\}", source, re.M):
            return {}
        raise ValueError("ported_module_registers_nothing:%s" % name)
    return out


def ported_top_level_names(source):
    """Every top-level name a module defines (for the collision check)."""
    import ast
    tree = ast.parse(source)
    names = set()
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            names.add(node.name)
        elif isinstance(node, ast.Assign):
            for t in node.targets:
                if isinstance(t, ast.Name):
                    names.add(t.id)
    return names


def strip_pported_preamble(source):
    """Drop import lines (the carriers own the import surface) and keep bodies.

    An import of anything outside ALLOWED_IMPORTS is a hard failure: an inlined
    module must reach exactly the shared helpers, or the artifact would carry a
    dangling reference that only explodes on the first live tick.
    """
    import ast
    lines = source.split("\n")
    tree = ast.parse(source)
    drop = set()
    for node in tree.body:
        if isinstance(node, (ast.Import, ast.ImportFrom)):
            for alias in node.names:
                root = (getattr(node, "module", None) or alias.name).split(".")[0]
                if root not in ALLOWED_IMPORTS:
                    raise ValueError("ported_module_import_not_allowed:%s" % root)
            for ln in range(node.lineno, (node.end_lineno or node.lineno) + 1):
                drop.add(ln - 1)
    return "\n".join([l for i, l in enumerate(lines) if i not in drop])


def ported_top_level_texts(source):
    """Top-level name -> its exact source text (for the byte-identical dedup)."""
    import ast
    tree = ast.parse(source)
    out = {}
    for node in tree.body:
        seg = ast.get_source_segment(source, node)
        if not seg:
            continue
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            out[node.name] = seg
        elif isinstance(node, ast.Assign):
            for t in node.targets:
                if isinstance(t, ast.Name):
                    out[t.id] = seg
    return out


def drop_top_level_definition(source, name, definition):
    """Remove one exact top-level definition (the duplicate we chose not to carry)."""
    return source.replace(definition + "\n", "", 1)


def ported_judgement_legs(source, name):
    """The judgement legs one ported module DECLARES (its own JUDGEMENT_LEGS list).

    A judgement leg is a callable the carrier asks "how big / is this allowed at all";
    it never produces a signal kind, so it is not in the module's RULES table and the
    rule registry would never carry it. The 11-04 batch inlined rules_sizing and its
    code came along while nothing could reach the readout -- AGENTS still lists
    "the judgement legs have no call site in either carrier" as a NOT DONE item.

    Read from the module (never a list here) for the same reason as *_PARAM_KEYS:
    the module owns the contract, and a second copy in this script is two books.
    Declaring a leg that the module does not actually define is a hard failure:
    the carrier would look the name up forever and always get "not wired".
    """
    import ast
    tree = ast.parse(source)
    declared = []
    defined = set()
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            defined.add(node.name)
        elif isinstance(node, ast.Assign):
            names = [t.id for t in node.targets if isinstance(t, ast.Name)]
            if isinstance(node.value, ast.List):
                for target in names:
                    if target == "JUDGEMENT_LEGS":
                        for item in node.value.elts:
                            if not isinstance(item, ast.Constant) or not isinstance(item.value, str):
                                raise ValueError("ported_judgement_legs_not_a_string_list:%s" % name)
                            if not item.value.isascii():
                                raise ValueError("ported_judgement_legs_non_ascii:%s:%s"
                                               % (name, item.value))
                            declared.append(item.value)
    missing = [d for d in declared if d not in defined]
    if missing:
        raise ValueError("ported_judgement_leg_not_defined:%s:%s" % (name, ",".join(missing)))
    return sorted(set(declared))


def ported_param_keys(source, name):
    """Return the params keys one ported module DECLARES it reads, by AST.

    Why this is read from the module and not from a list in this script: the
    module owns the contract (its *_PARAM_KEYS list is what its rule bodies
    actually index), and a second copy here is exactly the "two books, one
    number" shape this repo keeps getting bitten by.

    Two declaration shapes are accepted, and anything else is a hard failure
    rather than a silent zero:
      * `X_PARAM_KEYS = ["a", "b", ...]`            (rules_form/ladder/factor/t)
      * `X_PARAM_KEYS = sorted(X_DEFAULTS)` where the defaults table is a dict
        literal at top level                        (rules_sizing)
    A non-ASCII key is refused here as well: it would be baked into a GBK
    artifact and silently turn into '?'.
    """
    import ast
    tree = ast.parse(source)
    dict_tables = {}
    for node in tree.body:
        if isinstance(node, ast.Assign) and isinstance(node.value, ast.Dict):
            for target in node.targets:
                if isinstance(target, ast.Name):
                    keys = [k.value for k in node.value.keys if isinstance(k, ast.Constant)]
                    dict_tables[target.id] = keys
    out = []
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        names = [t.id for t in node.targets if isinstance(t, ast.Name)]
        declared = [n for n in names if n.endswith("_PARAM_KEYS")]
        if not declared:
            continue
        value = node.value
        if isinstance(value, ast.List):
            items = value.elts
        elif (isinstance(value, ast.Call) and isinstance(value.func, ast.Name)
                and value.func.id == "sorted" and len(value.args) == 1
                and isinstance(value.args[0], ast.Name)
                and value.args[0].id in dict_tables):
            # 别名指向那张 dict 字面量表：键名就是声明，不算第二次定义。
            items = [ast.Constant(value=k) for k in dict_tables[value.args[0].id]]
        else:
            raise ValueError("ported_param_keys_unreadable:%s:%s" % (name, declared[0]))
        for item in items:
            if not isinstance(item, ast.Constant) or not isinstance(item.value, str):
                raise ValueError("ported_param_keys_not_a_string_list:%s" % name)
            if not item.value.isascii():
                raise ValueError("ported_param_keys_non_ascii:%s:%s" % (name, item.value))
            out.append(item.value)
    if not out:
        raise ValueError("ported_module_declares_no_param_keys:%s" % name)
    return sorted(set(out))


def assemble_pported(params):
    """Inline every ported module params asks for, and merge their RULES tables.

    Two failure modes are refused rather than tolerated:
      * a named module that is not on disk (the box would run a smaller rule set
        than the one that was tested -- invisible from the outside);
      * a registered kind with no short code (the counter remark is the ONLY
        idempotency key, so such a rule could double-send), and two modules
        defining the same top-level name (the later definition would silently
        erase the earlier one).
    """
    wanted = list(params.get("ported_modules") or [])
    codes = params.get("signal_codes") or {}
    missing, missing_codes, collisions = [], [], []
    seen_names = {}
    deduped = []
    dropped = {}
    blocks = []
    knobs = {}
    legs = {}
    for name in wanted:
        source = read_pported_module(name)
        if source is None:
            missing.append(name)
            continue
        table = ported_rules_table(source, name)
        # 这一腿以前没有：构建器只验"模块到没到"，从不验"模块声明要的旋钮在不在 PARAMS"。
        # 11-04 的实际形态就是 rules_form 的代码进来了、它的 102 枚阈值一枚都没进来，
        # 于是产物里每条移植规则第一次被调用就 raise KeyError——装载自证（g7）看到的是
        # "模块注册成功"，不是"阈值读得到"，两者差整整一轮盘中。
        knobs[name] = ported_param_keys(source, name)
        # 判断腿台账（§JUDGEMENT-LEG）：模块自己声明它暴露哪些"该不该买/买多少"的读数函数。
        # 内联只把代码搬进来不等于把接线做好——11-04 那批 rules_sizing 进来了、
        # 产物里没有任何地方能调到 sizing_readout，于是"动态仓位已移植"这句话
        # 在屏幕上是真的、在钱上是空的。这里把声明烘成显式绑定，g7e 再逐条验它可调。
        legs[name] = ported_judgement_legs(source, name)
        for kind in sorted(table):
            if not codes.get(kind):
                missing_codes.append("%s.%s" % (name, kind))
        for nm, definition in sorted(ported_top_level_texts(source).items()):
            if nm in ("RULES", "JUDGEMENT_LEGS"):
                # 这两张表都是**每个模块自己的声明**（RULES=信号种类，JUDGEMENT_LEGS=判断腿名字），
                # 不是可复用的定义：两个模块各写一份是设计，不是重名覆盖。
                # 少这一条，第二条腿一进册就撞 ported_name_collision，等于"只准有一条判断腿"。
                continue  # re-snapshotted / re-declared per module below
            if nm in seen_names:
                if seen_names[nm][1] == definition:
                    # 逐字相同的定义（例如共享的 make_signal）只带一份进产物：
                    # 去重而不是报错，是为了让"模块单独可 import 自测"与"内联后不互相覆盖"
                    # 同时成立。文本一旦漂了就不相同 ⇒ 照样报 collision。
                    deduped.append("%s:%s" % (nm, name))
                    dropped.setdefault(name, set()).add(nm)
                    continue
                collisions.append("%s:%s/%s" % (nm, seen_names[nm][0], name))
            seen_names[nm] = (name, definition)
        snapshot = "_RULES_SNAPSHOT_%s = dict(RULES)" % name.upper()
        body = strip_pported_preamble(source)
        for nm, definition in sorted(ported_top_level_texts(source).items()):
            if nm in dropped.get(name, ()):
                body = drop_top_level_definition(body, nm, definition)
        blocks.append("# ---- inlined from qmt_gateway/%s.py ----\n%s\n%s"
                      % (name, body, snapshot))
    if missing:
        raise ValueError("ported_modules_missing:%s" % ",".join(missing))
    if missing_codes:
        raise ValueError("ported_kinds_without_short_code:%s" % ",".join(missing_codes))
    if collisions:
        raise ValueError("ported_name_collision:%s" % ",".join(collisions[:6]))
    if deduped:
        sys.stdout.write("PORTED_DEDUP=%s\n" % ",".join(deduped))
    if not blocks:
        # 没有模块内联进来时也必须把两处注册表建出来：载体代码里
        # _PORTED_LEGS.get(...) 与 _PORTED_KNOBS 是无条件读的，缺名字就是 NameError，
        # 而那会把"这台机器一个移植模块都没带"这种可读的坏消息变成一条崩栈。
        return ("\n_PORTED_RULES = {}\n_PORTED_INLINE = {}\n_PORTED_KNOBS = {}\n"
                "_PORTED_LEGS = {}\n_PORTED_LEGS_DECLARED = {}\n")
    merged = "\n".join(
        ["    %r: _RULES_SNAPSHOT_%s," % (n, n.upper()) for n in wanted])
    # 旋钮台账：每个内联进来的模块自己声明它读哪些 params，构建期一次性烘成数据。
    # 为什么烘成表而不是让产物自己去解析源码：装载后的载体里没有"模块"这个概念了，
    # 而运维在机器上必须能一眼看到"这条规则要的阈值到底缺哪几枚"（--selftest 那一行）。
    # json.dumps 在这里就是 Python 字面量，且 ensure_ascii 保证 GBK 终端不会被中文字节咬。
    knob_table = json.dumps({n: knobs[n] for n in sorted(knobs)},
                            ensure_ascii=True, sort_keys=True)
    # 判断腿绑定：模块声明的每条读数函数都在产物里**显式绑一次名**。
    # 为什么显式赋值而不是让载体自己翻命名空间（globals()/vars()）：闸 g2 禁止载体做
    # 命名空间自省——一个能查自己环境的载体就能长出没人审过的侧信道；而 import 在单文件
    # 载体里取决于文件落在哪，这跟"内联而不是运行时 import"是同一条理由。
    leg_table = json.dumps({n: legs[n] for n in sorted(legs) if legs[n]},
                           ensure_ascii=True, sort_keys=True)
    leg_lines = "".join('_PORTED_LEGS[%r] = %s\n' % (leg, leg)
                        for n in sorted(legs) for leg in legs[n])
    # 注入是**整段替换**标记区，所以注册表本体必须写在注入内容里：
    # 少这一行，产物里就是 `register_ported_rule` 往不存在的 _PORTED_RULES 里写
    # （g7 装载自证今天就是抓的这个）。
    return ("\n_PORTED_RULES = {}\n_PORTED_KNOBS = " + knob_table +
            "\n_PORTED_LEGS = {}\n_PORTED_LEGS_DECLARED = " + leg_table + "\n"
            + "\n\n".join(blocks) +
            "\n\n_PORTED_INLINE = {\n" + merged + "\n}\n" +
            "\nfor _name, _table in _PORTED_INLINE.items():\n"
            "    for _kind, _fn in (_table or {}).items():\n"
            "        register_ported_rule(_kind, _fn, _name)\n"
            "del _name, _kind, _fn\n"
            + leg_lines +
            "\n_PORTED_RULES = _PORTED_RULES\n")


def inject_pported(artifact, params):
    """Replace the PORTED RULES marker block in the artifact."""
    begin = artifact.index(PORTED_BEGIN) + len(PORTED_BEGIN)
    end = artifact.index(PORTED_END, begin)
    return artifact[:begin] + assemble_pported(params) + "    " + artifact[end:]


def gate_pported_short_codes_unique(artifact):
    """Two registered kinds must not share one short code.

    The remark is the ONLY idempotency key inside the sandbox, so a collision
    means one rule's orders permanently suppress another's -- and it would look
    like a quiet strategy that simply never fires.
    """
    import ast
    tree = ast.parse(artifact)
    seen = {}
    for node in ast.walk(tree):
        if not isinstance(node, ast.Assign):
            continue
        names = [t.id for t in node.targets if isinstance(t, ast.Name)]
        if "PARAMS" not in names or not isinstance(node.value, ast.Dict):
            continue
        for key, value in zip(node.value.keys, node.value.values):
            if isinstance(key, ast.Constant) and key.value == "signal_codes" \
                    and isinstance(value, ast.Dict):
                for k, v in zip(value.keys, value.values):
                    code = getattr(v, "value", None)
                    kind = getattr(k, "value", None)
                    if not isinstance(code, str):
                        continue
                    if code in seen and seen[code] != kind:
                        return False, "short_code_collision:%s shared by %s/%s" % (
                            code, seen[code], kind)
                    seen[code] = kind
    if not seen:
        return False, "signal_codes_unreadable"
    return True, "short_codes_unique n=%d" % len(seen)

def gate_ported_rule_knobs(artifact):
    """g9: every knob an inlined ported module declares must be in the baked PARAMS.

    This gate exists because the eight gates before it can all be green while the
    artifact cannot compute a single signal. The 11-04 batch is the proof: the
    bear-tactic rules were ported into rules_form.py, the module was inlined, the
    registry loaded, g7's smoke calls passed -- and the first handlebar raised
    KeyError('bd_min_bars') for every one of them, because the VALUES those bodies
    read had never been brought over (102 of the 105 declared keys existed nowhere).

    A rule that cannot read its threshold must not be publishable. The module
    declares the list itself (FORM_PARAM_KEYS etc.), so this stays true without
    anyone maintaining a second inventory here.
    """
    import ast
    tree = ast.parse(artifact)
    baked = set()
    knobs = None
    wanted_modules = set()
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        names = [t.id for t in node.targets if isinstance(t, ast.Name)]
        if "PARAMS" in names and isinstance(node.value, ast.Dict):
            for key, value in zip(node.value.keys, node.value.values):
                if isinstance(key, ast.Constant) and isinstance(key.value, str):
                    baked.add(key.value)
                # What params says must be inlined: the ledger has to cover THESE,
                # not just whatever rows happen to have been baked. Without this,
                # an artifact whose knob table is an empty dict passes with
                # "declared=0 modules=0" -- the gate would grade "never checked"
                # as "checked and clean", which is the exact shape g9 was born to kill.
                if key.value == "ported_modules" and isinstance(value, ast.List):
                    wanted_modules = set(getattr(e, "value", None) for e in value.elts)
                    wanted_modules.discard(None)
        if "_PORTED_KNOBS" in names and isinstance(node.value, ast.Dict):
            knobs = {}
            for key, value in zip(node.value.keys, node.value.values):
                module = getattr(key, "value", None)
                if not isinstance(module, str) or not isinstance(value, ast.List):
                    continue
                knobs[module] = [getattr(e, "value", None) for e in value.elts]
    if knobs is None:
        return False, "knob_table_absent"
    if not baked:
        return False, "baked_params_unreadable"
    missing = []
    declared = 0
    for module in sorted(knobs):
        for key in knobs[module]:
            declared += 1
            if key not in baked:
                missing.append("%s:%s" % (module, key))
    if missing:
        return False, "rule_knobs_missing n=%d keys=%s" % (len(missing), ",".join(missing))
    # 缺件检查放在"哪一枚阈值没烘上"之后：两条都成立时，点名具体阈值的那句才是运维要看的第一句。
    if wanted_modules and not knobs:
        return False, "knob_table_absent:modules=%s" % ",".join(sorted(wanted_modules))
    unledgered = sorted(m for m in wanted_modules if m not in knobs)
    if unledgered:
        return False, "knob_ledger_missing_module=%s" % ",".join(unledgered)
    return True, "rule_knobs_baked declared=%d modules=%d" % (declared, len(knobs))


def gate_ascii(artifact):
    """G1: the artifact must contain no non-ASCII byte."""
    bad = [i for i, byte in enumerate(artifact.encode("utf-8")) if byte > 127]
    if bad:
        return False, "non_ascii_bytes=%d first_offset=%d" % (len(bad), bad[0])
    return True, ""


def dotted_name(node):
    """Render an ast call target as a dotted name ('json.load', 'open').

    Returns None when the target is not a plain name chain (a subscript
    or a lambda): those are handled by the caller as unknown roots.
    """
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute):
        base = dotted_name(node.value)
        if base is None:
            return None
        return "%s.%s" % (base, node.attr)
    return None


def call_allowed(name):
    """Is this exact call shape on the allow-list?"""
    if name in ALLOWED_CALLS:
        return True
    for allowed in ALLOWED_CALLS:
        if allowed.endswith(".*") and name.startswith(allowed[:-1]):
            return True
    return False


def bound_names(tree):
    """Every name the artifact binds itself (params, locals, defs)."""
    names = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and isinstance(node.ctx, ast.Store):
            names.add(node.id)
        elif isinstance(node, ast.arg):
            names.add(node.arg)
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            names.add(node.name)
        elif isinstance(node, ast.ExceptHandler) and node.name:
            names.add(node.name)
        elif isinstance(node, ast.Global) or isinstance(node, ast.Nonlocal):
            names.update(node.names)
    return names


def artifact_methods(tree):
    """Every function/method/attribute/class name the artifact defines.

    Includes the attributes the artifact assigns on its own objects
    (self.clock, self.broker, ...) and the classes it defines: a call on
    one of those is a call on the artifact's own code, so the name is
    reviewable here.
    """
    names = set()
    for node in ast.walk(tree):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            names.add(node.name)
        elif isinstance(node, ast.Attribute) and isinstance(node.ctx, ast.Store):
            names.add(node.attr)
        elif isinstance(node, ast.ClassDef):
            names.add(node.name)
    return names


def getattr_is_literal(node):
    """Is getattr/hasattr called with a literal attribute name?

    getattr(obj, "system") is a reviewable read; getattr(obj, name) with
    a computed name is a way to reach anything the object happens to
    carry. The literal form is allowed, the computed form is not.
    """
    if len(node.args) < 2:
        return False
    second = node.args[1]
    return isinstance(second, ast.Constant) and isinstance(second.value, str)


def _inner_call_target(func):
    """For "f(x).m(...)", return the attribute node f(x).m; else None."""
    if not isinstance(func, ast.Attribute):
        return None
    if not isinstance(func.value, ast.Call):
        return None
    return func


def _computed_receiver_method(func):
    """Method name of a call on a computed receiver, or None.

    A computed receiver is a subscript ("params['x']"), a boolean
    expression ("a or b"), a literal, or a comprehension. The method must
    be a known-pure one, so the expression can only read or build data --
    it cannot reach a capability that the surrounding code did not already
    have.
    """
    if not isinstance(func, ast.Attribute):
        return None
    receiver = func.value
    if isinstance(receiver, (ast.Subscript, ast.BoolOp, ast.Constant,
                             ast.ListComp, ast.GeneratorExp,
                             ast.DictComp, ast.SetComp, ast.IfExp)):
        return func.attr
    return None


def gate_no_privileged_calls(artifact):
    """G2: no call outside the documented allow-list.

    The scan is an AST walk on purpose. A text scan trips over prose: the
    executor's own docstring mentions the forbidden names, and a substring
    match would fail the build on a comment. The AST sees real code only.

    Checks, in order:
      1. imports: only the allow-listed modules may be imported at all,
         and the capability modules (network, process, dynamic code) are
         named explicitly so the failure message says why;
      2. module-rooted calls: a call whose root is an imported module must
         match the pinned call shape ("os.path.join" is fine, "os.system"
         is not);
      3. builtin-rooted calls: a call whose root is a builtin must be on
         the builtin allow-list, and the dynamic-execution builtins are
         refused outright;
      4. instance-rooted calls: a call on an object the artifact holds is
         allowed only when the method name is a known-pure standard
         library method, a pinned local QMT SDK entry point, or something
         the artifact itself defines;
      5. reflection: getattr/hasattr must name their attribute literally.
    """
    try:
        tree = ast.parse(artifact)
    except SyntaxError as exc:
        return False, "artifact_not_parsable:%s" % (exc,)
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            roots = [alias.name.split(".")[0] for alias in node.names]
        elif isinstance(node, ast.ImportFrom):
            roots = [(node.module or "").split(".")[0]]
        else:
            continue
        for root in roots:
            if not root:
                continue
            if root in FORBIDDEN_MODULES:
                return False, "line=%d import=%s reason=%s" % (
                    node.lineno, root, FORBIDDEN_MODULES[root])
            if root not in ALLOWED_IMPORTS:
                return False, "line=%d import=%s not_allowed" % (node.lineno, root)
            if root == "xtquant":
                for alias in getattr(node, "names", []):
                    name = alias.asname or alias.name
                    if name not in ALLOWED_XT_NAMES:
                        return False, "line=%d xtquant_name=%s not_allowed" % (
                            node.lineno, name)
    bound = bound_names(tree)
    own = artifact_methods(tree)
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        func = node.func
        name = dotted_name(func)
        if name is None:
            # The receiver is a computed expression (a call on a call
            # result, a subscript, a boolean expression, ...). That shape
            # cannot be reviewed statically, so it is refused -- EXCEPT
            # for the two forms the executor actually uses:
            #   * a pure method call on the result of an already-allowed
            #     call ("datetime.now(x).replace(...)"); the inner call is
            #     visited on its own by this same walk, so it is checked;
            #   * a known-pure method call on a subscript or a boolean
            #     expression ("params[...] or {}).get(kind)").
            inner = _inner_call_target(func)
            if inner is not None:
                method = getattr(inner, "attr", "")
                if method not in SAFE_METHODS:
                    return False, "line=%d method=%s on_computed_receiver" % (
                        node.lineno, method)
                continue
            method = _computed_receiver_method(func)
            if method is None:
                return False, "line=%d call_target_not_a_name" % node.lineno
            if method not in SAFE_METHODS:
                return False, "line=%d method=%s on_computed_receiver" % (
                    node.lineno, method)
            continue
        root = name.split(".")[0]
        method = name.split(".")[-1]
        if root in ("getattr", "hasattr") and not getattr_is_literal(node):
            return False, "line=%d reflection=%s needs_literal_attribute" % (
                node.lineno, root)
        if root in ("json", "hashlib", "sqlite3", "os", "logging", "time",
                    "argparse", "sys", "datetime", "timedelta", "timezone"):
            # A call into an imported module: the shape is pinned.
            if not call_allowed(name):
                return False, "line=%d call=%s shape_not_allowed" % (node.lineno, name)
            continue
        if root in FORBIDDEN_BUILTINS:
            return False, "line=%d builtin=%s reason=%s" % (
                node.lineno, root, FORBIDDEN_BUILTINS[root])
        if root in ALLOWED_ROOTS:
            continue
        if root in ALLOWED_XT_NAMES:
            # A call on an SDK object: only the pinned entry points.
            if method not in ALLOWED_XT_METHODS:
                return False, "line=%d sdk_call=%s not_allowed" % (node.lineno, method)
            continue
        if root in own:
            # A call on a class or object the artifact itself defines.
            continue
        if root in bound:
            # A call on a LOCAL variable the artifact itself bound. What
            # that variable holds was decided by an already-checked
            # statement (an allowed call, an import), so the call cannot
            # reach a capability the artifact did not already have.
            continue
        # A call on an object the artifact holds: only known-pure methods
        # and the artifact's own attributes.
        if method not in SAFE_METHODS and method not in ALLOWED_XT_METHODS \
                and method not in own:
            return False, "line=%d method=%s not_allowed" % (node.lineno, method)
        continue
    return True, ""


def gate_no_fstring(artifact):
    """G3: no f-string anywhere in the artifact."""
    match = FSTRING_RE.search(artifact)
    if match:
        line = artifact[:match.start()].count("\n") + 1
        return False, "fstring_at_line=%d" % line
    return True, ""


def gate_version_in_remark(artifact):
    """G4: STRATEGY_VERSION must exist and reach the remark builder.

    A version constant that no order remark carries is decoration: the
    broker table could not be rebuilt from the remarks, and the idempotency
    reconstruction would have nothing to key on.
    """
    match = re.search(r"^%s\s*=\s*[\"']([^\"']+)[\"']" % VERSION_CONST,
                      artifact, re.M)
    if not match:
        return False, "%s_constant_missing" % VERSION_CONST
    version = match.group(1)
    if not VERSION_RE.match(version):
        return False, "version_format_bad=%s" % version
    # The remark builder must reference the constant.
    builder = re.search(r"^def %s\(.*?(?=^\S|\Z)" % REMARK_BUILDER,
                        artifact, re.M | re.S)
    if not builder:
        return False, "%s_missing" % REMARK_BUILDER
    if VERSION_CONST not in builder.group(0):
        return False, "%s_does_not_reference_%s" % (REMARK_BUILDER, VERSION_CONST)
    return True, ""


def rule_layer_line_range(artifact):
    """Line range (1-based, inclusive) of the rule layer region."""
    lines = artifact.splitlines()
    begin = end = None
    for index, line in enumerate(lines, 1):
        if line.strip() == RULE_LAYER_BEGIN:
            begin = index
        elif line.strip() == RULE_LAYER_END:
            end = index
    if begin is None or end is None:
        return None
    if end < begin:
        return None
    return (begin, end)


def gate_no_bare_numbers(artifact):
    """G5: no rule-layer function body may contain a numeric literal.

    This is the gate that makes "baked parameters" real. A threshold
    written straight into a rule function is a threshold that no longer
    comes from the parameter file: change the knob, and the code keeps
    the old number. The scan is an AST walk, so a number inside a
    docstring or a comment is prose and does not count -- only a literal
    in executable code inside the rule layer fails the build.
    """
    span = rule_layer_line_range(artifact)
    if span is None:
        return False, "rule_layer_markers_missing"
    begin, end = span
    try:
        tree = ast.parse(artifact)
    except SyntaxError as exc:
        return False, "artifact_not_parsable:%s" % (exc,)
    hits = []
    for node in ast.walk(tree):
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            continue
        if not (begin <= node.lineno <= end):
            continue
        for inner in ast.walk(node):
            if not isinstance(inner, ast.Constant):
                continue
            value = inner.value
            # bool is a subclass of int in Python; True/False are logic,
            # not thresholds, so they do not count as numeric literals.
            if isinstance(value, bool):
                continue
            if isinstance(value, (int, float)):
                hits.append("%s:%r" % (node.name, value))
    if hits:
        return False, "bare_numbers=%s" % ",".join(hits[:8])
    return True, ""


GATES = [
    ("g1_ascii", gate_ascii, "artifact contains a non-ASCII byte"),
    ("g2_privileged_calls", gate_no_privileged_calls,
     "artifact calls something outside the documented allow-list"),
    ("g3_no_fstring", gate_no_fstring, "artifact contains an f-string"),
    ("g4_version_in_remark", gate_version_in_remark,
     "version constant missing or not wired into the remark builder"),
    ("g5_no_bare_numbers", gate_no_bare_numbers,
     "a rule-layer function body contains a numeric literal"),
]


def run_gates(artifact, gates=None):
    """Run every gate. Returns (ok, [(gate, ok, detail), ...])."""
    results = []
    for name, func, _why in (gates or GATES):
        ok, detail = func(artifact)
        results.append((name, ok, detail))
    return all(ok for _n, ok, _d in results), results


def print_gates():
    """Show the gate definitions (used by the docs and by the tests)."""
    for name, _func, why in GATES:
        sys.stdout.write("GATE %s :: %s\n" % (name, why))
    for name in sorted(ALLOWED_CALLS):
        sys.stdout.write("ALLOWED_CALL %s :: %s\n" % (name, ALLOWED_CALLS[name]))
    for name in sorted(ALLOWED_ROOTS):
        sys.stdout.write("ALLOWED_ROOT %s :: %s\n" % (name, ALLOWED_ROOTS[name]))
    for name in sorted(ALLOWED_IMPORTS):
        sys.stdout.write("ALLOWED_IMPORT %s :: %s\n" % (name, ALLOWED_IMPORTS[name]))
    sys.stdout.write("BUILD_RESULT=OK gates=%d\n" % len(GATES))
    return 0


def load_params(path):
    """Read the parameter file. Returns (params, version, error)."""
    try:
        with open(path, "r", encoding="utf-8") as handle:
            doc = json.load(handle)
    except (IOError, OSError) as exc:
        return None, None, "params_unreadable:%s" % (exc,)
    except ValueError as exc:
        return None, None, "params_not_json:%s" % (exc,)
    if not isinstance(doc, dict):
        return None, None, "params_not_an_object"
    params = doc.get("params")
    if not isinstance(params, dict) or not params:
        return None, None, "params_missing_or_empty"
    version = doc.get("strategy_version")
    if not isinstance(version, str) or not VERSION_RE.match(version):
        return None, None, "strategy_version_missing_or_bad"
    return params, version, ""


def out_dir_for(explicit):
    """Resolve the output directory, refusing to publish inside the repo.

    The repository has a worktree-drift gate. Writing the artifact into
    the tree turns that gate red for a reason that has nothing to do with
    the code under review, so the builder refuses instead.
    """
    target = explicit or os.environ.get("LOCAL_EXECUTOR_OUT_DIR") or DEFAULT_OUT_DIR
    target = os.path.abspath(target)
    repo = os.path.abspath(REPO_ROOT)
    if target == repo or target.startswith(repo + os.sep):
        raise ValueError("out_dir_inside_repository:%s" % target)
    return target




# ---------------------------------------------------------------------------
# SANDBOX TARGET (owner decision 2026-11-04): the built-in QMT strategy.
#
# The carrier question was settled as: execution and signal maths live in the
# QMT BUILT-IN strategy (the only automated path still inside the broker's
# exemption), while the ordinary local process does delivery, read-back and
# reporting. That split forces three things the executor target does not need:
#   * the strategy cannot import the gateway modules, so the status vocabulary
#     is GENERATED into the artifact from the single sources (handler.XT_STATUS_CODES
#     / store.TERMINAL_STATUSES) -- a hand-copied subset would silently mis-classify
#     a live order as settled, or re-cancel a dead one forever;
#   * the rule layer must not exist twice, so the util/rule functions are
#     extracted from qmt_gateway/local_executor.py by AST and injected;
#   * the artifact may not touch files or the network at all, so its import set
#     is narrower than the executor's and is gated separately (g6).
# ---------------------------------------------------------------------------

SANDBOX_TEMPLATE = os.path.join(REPO_ROOT, "qmt_gateway", "qmt_internal_strategy.py")
SANDBOX_PREFIX = "qmt_internal_strategy_"
SHARED_BEGIN = "# ==== SHARED CODE BEGIN ===="
SHARED_END = "# ==== SHARED CODE END ===="
VOCAB_BEGIN = "# ==== STATUS VOCAB BEGIN ===="
VOCAB_END = "# ==== STATUS VOCAB END ===="

# The functions the strategy reuses verbatim from the executor. Anything that
# touches files or the environment (file_sha256, load_closed_days) is excluded
# on purpose -- the sandbox cannot call them even if they were present.
SHARED_FUNCTIONS = [
    "now_cn", "day_key", "hhmm_now", "symbol_head", "text_sha256",
    "build_remark", "short_code_from_remark", "normalize_tick",
    "normalize_order_status",
    # §PORTED-RULES：注册表本身也共用一份。沙箱不能 import，所以"怎么登记一条规则、
    # 怎么取它的武装态、缺模块怎么报"这三件事必须在两个载体里逐字相同——
    # 各写一份就会漂成"执行器里 shadow、沙箱里 live"那种最贵的不对称。
    "register_ported_rule", "rule_arm_mode", "_ported_static_table",
    "load_ported_modules", "ported_load_status",
]

# math is pure arithmetic -- the built-in strategy may use it, it still reaches
# neither a file nor a socket (that is what g6's forbidden-token pass enforces).
SANDBOX_ALLOWED_IMPORTS = {"builtins", "datetime", "hashlib", "math", "time"}

def gate_sandbox_loads(artifact):
    """g7: the published artifact must LOAD, not merely parse.

    Why this gate exists: the sandbox strategy passed five gates while the shared
    code it injected called datetime/timezone/hashlib that the file never
    imported. ast.parse() cannot see that -- only executing the module body can,
    and the failure would have surfaced as a NameError on the first handlebar,
    i.e. "the strategy is deployed and reports nothing" (the shape this repo
    calls 声称已做). Loading the module body is safe here precisely because the
    other gates already proved it opens no file and opens no socket.
    """
    import ast
    try:
        tree = ast.parse(artifact)
        namespace = {"__name__": "qmt_internal_strategy_gate"}
        exec(compile(tree, "<sandbox-artifact>", "exec"), namespace)
    except SyntaxError as exc:
        return False, "sandbox_syntax_error:%s" % exc
    except Exception as exc:  # noqa: BLE001
        return False, "sandbox_load_failed:%s" % repr(exc)[:160]
    for needed in ("STRATEGY_VERSION", "PARAMS", "XT_STATUS_CODES", "TERMINAL_STATUSES",
                   "run_round", "handlebar", "init", "rule_pullback_buy", "build_remark"):
        if needed not in namespace:
            return False, "sandbox_missing_symbol:%s" % needed
    if not namespace.get("XT_STATUS_CODES"):
        return False, "sandbox_status_vocab_empty"
    if not namespace.get("TERMINAL_STATUSES"):
        return False, "sandbox_terminal_set_empty"
    # Loading is not enough: an unused import disappears at load time and only
    # raises when the clock or a rule is first touched -- which in this carrier
    # means "the first handlebar of the trading day". So call the three pure
    # helpers that every round depends on. They take no external input, and g6
    # has already proven the artifact reaches neither files nor sockets, so this
    # cannot place an order; it can only prove the module is callable.
    try:
        moment = namespace["now_cn"]()
        namespace["day_key"](moment)
        namespace["hhmm_now"](moment)
        remark = namespace["build_remark"]("PBTEST")
        if namespace["short_code_from_remark"](remark) != "PBTEST":
            return False, "sandbox_remark_roundtrip_broken"
        namespace["normalize_order_status"](50, namespace["XT_STATUS_CODES"],
                                            namespace["TERMINAL_STATUSES"])
        namespace["rule_pullback_buy"](namespace["PARAMS"], {})
        namespace["rule_no_cancel_window"](namespace["PARAMS"],
                                           {"trading_day": False, "hhmm": "10:00"})
    except Exception as exc:  # noqa: BLE001
        return False, "sandbox_smoke_call_failed:%s" % repr(exc)[:160]
    return True, "sandbox_loads_clean"



def gate_executor_loads(artifact):
    """g7e: the published *executor* artifact must LOAD too, not merely parse.

    This gate exists because of a failure the other seven could not see. The
    inlined ported-rule block ends with a module-level registration loop that
    calls register_ported_rule(); the marker block sat *above* that function's
    definition, so the repository copy parsed fine, passed the ASCII / call /
    f-string / bare-number gates, and the published artifact raised
    NameError on import. A carrier that cannot be imported does not "trade
    wrong" -- it says nothing at all, which is how 11-03's
    "deployed and reporting nothing" shape returns.

    ast.parse() cannot see a missing name; only executing the module body can.
    Executing it is safe because the artifact's own gates already bound what it
    may call, and nothing here reaches the network: this is import plus pure
    helper calls, with no broker, no state file and no order path.
    """
    import ast
    import sys as _sys
    # counter_status_vocab() imports the gateway ledger modules at call time (that
    # import *is* the single source -- copying the status table here is forbidden),
    # so the gate has to give the artifact the same sys.path the delivered package
    # has on the box. Without it the helper returns the "unknown" pair and the
    # gate would report an empty vocabulary as a defect of the artifact.
    _gateway_dir = os.path.join(REPO_ROOT, "qmt_gateway")
    if _gateway_dir not in _sys.path:
        _sys.path.insert(0, _gateway_dir)
    try:
        tree = ast.parse(artifact)
        namespace = {"__name__": "local_executor_gate"}
        exec(compile(tree, "<executor-artifact>", "exec"), namespace)
    except SyntaxError as exc:
        return False, "executor_syntax_error:%s" % exc
    except Exception as exc:  # noqa: BLE001
        return False, "executor_load_failed:%s" % repr(exc)[:200]
    # The path stays inserted on purpose: the smoke calls below import the same
    # ledger modules through counter_status_vocab(), and render_status_vocab()
    # has always left this entry in place (one build process, one direction).

    for needed in ("STRATEGY_VERSION", "PARAMS", "ported_load_status", "rule_arm_mode",
                   "_ported_static_table", "normalize_order_status", "Executor",
                   "counter_status_vocab", "rule_window_hit"):
        if needed not in namespace:
            return False, "executor_missing_symbol:%s" % needed

    params = namespace["PARAMS"]
    status = namespace["ported_load_status"](params)
    # A module named in params but not carried in the artifact is a shortage, and
    # a build must not publish a smaller rule set than the one that was tested.
    if status["missing"]:
        return False, "executor_ported_module_missing:%s" % ",".join(status["missing"])
    if params.get("ported_modules") and not status["kinds"]:
        return False, "executor_ported_registry_empty"
    # Judgement legs (rules_t / rules_sizing) legitimately carry zero kinds.
    # They must still be *present*, or "by design" and "file gone" read the same.
    for name in (params.get("ported_modules") or []):
        if name not in status["loaded"]:
            return False, "executor_ported_module_absent:%s" % name

    # Import is not enough -- a name used only inside one branch blows up on the
    # first call that reaches it. So exercise the pure helpers every round depends on.
    # §JUDGEMENT-LEG: 模块自己声明的判断腿必须真的**可调**，不是"文件在内联区里出现过"。
    # 11-04 的形态就是 rules_sizing 的代码进来了、没有任何名字能调到它，于是"动态仓位已移植"
    # 这句话在装载自证里是绿的，在盘上是空的。声明表与绑定表两处都必须对得上，缺一即红。
    declared_legs = namespace.get("_PORTED_LEGS_DECLARED") or {}
    bound_legs = namespace.get("_PORTED_LEGS") or {}
    for module_name, leg_names in sorted(declared_legs.items()):
        for leg in leg_names:
            if not callable(bound_legs.get(leg)):
                return False, "executor_judgement_leg_unbound:%s.%s" % (module_name, leg)
    try:
        vocab, terminals = namespace["counter_status_vocab"]()
        if not vocab or not terminals:
            return False, "executor_status_vocab_empty"
        namespace["normalize_order_status"](50, vocab, terminals)
        for kind in status["kinds"][:1]:
            namespace["rule_arm_mode"](params, kind)
        if not namespace["rule_window_hit"](params, [["09:30", "11:30"]], "10:00"):
            return False, "executor_window_judgement_broken"
        if namespace["rule_window_hit"](params, [["09:30", "11:30"]], "11:30"):
            return False, "executor_window_end_is_closed_not_half_open"
        table = namespace["_ported_static_table"]()
        if len(table) != len(status["kinds"]):
            return False, "executor_registry_table_mismatch:%d_vs_%d" % (len(table),
                                                                        len(status["kinds"]))
    except Exception as exc:  # noqa: BLE001
        return False, "executor_smoke_call_failed:%s" % repr(exc)[:200]
    return True, "executor_loads_clean kinds=%d judgement=%s" % (
        len(status["kinds"]), ",".join(status.get("judgement") or []) or "none")


def extract_shared(source_text):
    """Pull the named functions out of the executor source, verbatim.

    AST-based, so the injected bytes are the same bytes the executor runs: one
    threshold, one implementation, two carriers. A missing name is a build
    failure, never a silent skip -- a rule that quietly stopped being injected
    would leave the strategy trading without it.
    """
    import ast
    tree = ast.parse(source_text)
    wanted = list(SHARED_FUNCTIONS)
    segments = []
    for node in tree.body:
        if isinstance(node, ast.FunctionDef) and node.name in wanted:
            segment = ast.get_source_segment(source_text, node)
            if not segment:
                raise ValueError("shared_source_unreadable:%s" % node.name)
            segments.append((node.name, segment))
    found = set(name for name, _ in segments)
    missing = [name for name in wanted if name not in found]
    if missing:
        raise ValueError("shared_functions_missing:%s" % ",".join(missing))
    # Rule layer: every function between the RULE LAYER markers, in source order.
    begin = source_text.index("# ==== RULE LAYER BEGIN ====")
    end = source_text.index("# ==== RULE LAYER END ====")
    block = source_text[begin:source_text.index("\n", source_text.index("# ==== RULE LAYER BEGIN ====")) + 1]
    block += source_text[source_text.index("# ==== RULE LAYER BEGIN ====") + len("# ==== RULE LAYER BEGIN ===="):end]
    block = block.strip() + "\n# ==== RULE LAYER END ====\n"
    rules = []
    for node in ast.parse(block).body:
        if isinstance(node, ast.FunctionDef):
            rules.append(node.name)
    if not rules:
        raise ValueError("rule_layer_empty")
    ordered = [seg for _name, seg in sorted(segments, key=lambda pair: pair[0])]
    return "\n\n".join(ordered) + "\n\n" + block.strip() + "\n"


def render_status_vocab():
    """Generate the status tables from their single sources, ASCII-safe.

    json.dumps(ensure_ascii=True) is what keeps a Chinese ledger literal from
    putting non-ASCII bytes into a file the GBK terminal has to read -- the
    ASCII gate (g1) would reject it, and worse, a torn source would take the
    whole strategy down at load time.
    """
    sys.path.insert(0, os.path.join(REPO_ROOT, "qmt_gateway"))
    import json as _json
    try:
        from handler import XT_STATUS_CODES
        from store import TERMINAL_STATUSES
    except Exception as exc:  # noqa: BLE001
        raise ValueError("status_vocab_source_unavailable:%s" % exc)
    lines = ["XT_STATUS_CODES = {"]
    for code in sorted(XT_STATUS_CODES):
        lines.append("    %d: %s," % (int(code), _json.dumps(XT_STATUS_CODES[code])))
    lines.append("}")
    lines.append("TERMINAL_STATUSES = (")
    for text in TERMINAL_STATUSES:
        lines.append("    %s," % _json.dumps(text))
    lines.append(")")
    return "\n".join(lines) + "\n"


def assemble_sandbox(template, shared, vocab, params, version):
    """Inject params + shared code + vocabulary into the template."""
    out = bake(template, params, version)
    # 注入 = **替换标记之间的整段**，不是在标记后面追加。
    # 追加那种写法今天实测踩过：模板里 XT_STATUS_CODES = {} 的占位行留在注入块后面，
    # 后一句把前一句覆盖掉 ⇒ 产物里的词表运行时是空的，撤单腿把所有状态都读成"未知"，
    # 于是一单不撤——fail-closed 方向没错，但整条腿静默失效，正是本仓最贵的"以为在跑"。
    for begin_marker, end_marker, payload in ((VOCAB_BEGIN, VOCAB_END, vocab),
                                              (SHARED_BEGIN, SHARED_END, shared)):
        begin = out.index(begin_marker) + len(begin_marker)
        finish = out.index(end_marker, begin)
        out = out[:begin] + "\n" + payload + out[finish:]
    return out


def gate_sandbox_imports(artifact):
    """g6: the built-in strategy imports nothing that can reach a file or a socket."""
    import ast
    tree = ast.parse(artifact)
    bad = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                root = alias.name.split(".")[0]
                if root not in SANDBOX_ALLOWED_IMPORTS:
                    bad.append("%s@%d" % (root, node.lineno))
        elif isinstance(node, ast.ImportFrom):
            root = (node.module or "").split(".")[0]
            if root and root not in SANDBOX_ALLOWED_IMPORTS:
                bad.append("%s@%d" % (root, node.lineno))
    if bad:
        return False, "sandbox_imports_not_allowed:%s" % ",".join(sorted(set(bad)))
    # 只判**可执行形态**（AST），不判文本：注释里写 "no socket" 是文档，
    # 按子串拦就是把锁指着自己的说明文字（本仓 §164 的"负锁命中自己说明"同形教训）。
    forbidden_calls = {"open": "file open", "exec": "dynamic exec", "eval": "dynamic eval",
                       "__import__": "dynamic import", "input": "console input"}
    for node in ast.walk(tree):
        if isinstance(node, ast.Call):
            func = node.func
            name = getattr(func, "id", None) or getattr(func, "attr", None) or ""
            if name in forbidden_calls:
                return False, "sandbox_forbidden_call:%s@%d" % (name, node.lineno)
        if isinstance(node, ast.Attribute):
            base = getattr(node.value, "id", "") or ""
            if base in ("socket", "requests", "urllib", "http", "os", "sys", "shutil",
                        "subprocess", "sqlite3"):
                return False, "sandbox_forbidden_attribute:%s@%d" % (base, node.lineno)
    return True, "sandbox_imports_clean"


def main(argv=None):
    """Command line entry point of the builder."""
    parser = argparse.ArgumentParser(
        prog="build_local_executor",
        description="Bake the local executor artifact and gate it.")
    parser.add_argument("--params", default=DEFAULT_PARAMS,
                        help="parameter JSON file (default: the repo copy)")
    parser.add_argument("--source", default=DEFAULT_SOURCE,
                        help="executor source to bake (default: the repo copy)")
    parser.add_argument("--out-dir", default=None,
                        help="publish directory (default: outside the repo)")
    parser.add_argument("--print-gates", action="store_true",
                        help="print the gate definitions and exit")
    # §DRIFT-GATE（2026-11-04 补）：仓内那份"源文件"里带着一个已烘的 PARAMS 块，
    # 而它必须与参数 JSON 逐字节一致——否则仓里显示的就是**过时阈值**，读代码的人
    # 会按过时的数判策略（本文件头与 params JSON 的注释一直这么声称，但今天之前
    # **并没有**这样一道闸：bake() 无条件覆写，仓内副本停在 v00 占位也构建绿）。
    # 缺省行为＝漂移即拒建；--refresh-source 才把 JSON 回写进仓内副本。
    # English: refuse to build while the in-repo baked copy drifts from the JSON;
    # --refresh-source writes the JSON back into the repo copy.
    parser.add_argument("--refresh-source", action="store_true",
                        help="write the baked params back into the repo source copy")
    parser.add_argument("--target", choices=("executor", "sandbox"), default="executor",
                        help="executor = the local delivery/reporting process; "
                             "sandbox = the QMT built-in strategy (no file, no network)")
    args = parser.parse_args(argv)

    if args.print_gates:
        return print_gates()

    params, version, error = load_params(args.params)
    if error:
        return fail("params_invalid", error)

    try:
        with open(args.source, "r", encoding="utf-8") as handle:
            source = handle.read()
    except (IOError, OSError) as exc:
        return fail("source_unreadable", str(exc))

    gates = list(GATES)
    prefix = ARTIFACT_PREFIX
    if args.target == "sandbox":
        # §SANDBOX-TARGET（owner 2026-11-04 定载体）：QMT 内置策略是 11-05 之后
        # 唯一仍在券商豁免内的自动下单通道；规则层从 local_executor.py 按 AST 抽出注入，
        # 状态词表由构建期从 handler/store 生成——**两处都不许手抄第二份**。
        try:
            with open(SANDBOX_TEMPLATE, "r", encoding="utf-8") as handle:
                template = handle.read()
        except (IOError, OSError) as exc:
            return fail("template_unreadable", str(exc))
        try:
            shared = extract_shared(source)
            vocab = render_status_vocab()
            artifact = assemble_sandbox(template, shared, vocab, params, version)
        except ValueError as exc:
            return fail("sandbox_assemble_failed", str(exc))
        gates = gates + [
            ("g6_sandbox_imports", gate_sandbox_imports,
             "built-in strategy must reach neither files nor network"),
            ("g7_sandbox_loads", gate_sandbox_loads,
             "the published artifact must load, not merely parse"),
        ]
        prefix = SANDBOX_PREFIX
    else:
        try:
            artifact = bake(source, params, version)
        except ValueError as exc:
            return fail("bake_failed", str(exc))
        gates = gates + [
            ("g7e_executor_loads", gate_executor_loads,
             "the published executor artifact must import and answer a call"),
        ]

    # §DRIFT-GATE：仓内副本与"应当如此"的字节不一致 ⇒ 要么拒建，要么显式回写。
    # 判据用 bake 本身（幂等：已正确烘过的文件再过一次字节不变），
    # 这样"阈值漂移"与"版本没跟着参数走"两种形态被同一把尺子抓到，不写第二套。
    if args.target == "executor" and source != artifact:
        if args.refresh_source:
            try:
                with open(args.source, "w", encoding="utf-8", newline="\n") as handle:
                    handle.write(artifact)
            except (IOError, OSError) as exc:
                return fail("source_unwritable", str(exc))
            sys.stdout.write("DRIFT_REFRESHED=1 path=%s version=%s\n" % (args.source, version))
            # 回写之后继续正常构建（产物与仓内副本此刻同源）。
            source = artifact
        else:
            return fail("baked_copy_drifted",
                        "repo copy %s differs from params JSON (version=%s); "
                        "re-run with --refresh-source" % (args.source, version))

    # §PORTED-RULES：两个载体都要在发布前把移植进来的规则内联进去（缺模块/缺短码
    # 一律拒建，而不是带着半套规则上线）。
    try:
        artifact = inject_pported(artifact, params)
    except ValueError as exc:
        return fail("ported_rules_inject_failed", str(exc))
    gates = gates + [("g8_short_codes_unique", gate_pported_short_codes_unique,
                     "two rules must not share one counter remark code"),
                    ("g9_rule_knobs_baked", gate_ported_rule_knobs,
                     "every knob a ported module declares must be present in the baked PARAMS")]

    ok, results = run_gates(artifact, gates)
    if not ok:
        # No artifact is written: a half-gated file on disk is worse than
        # no file, because the next deploy would happily pick it up.
        for name, gate_ok, detail in results:
            if not gate_ok:
                sys.stdout.write("GATE_FAIL gate=%s detail=%s\n" % (name, detail))
        # 缺旋钮给**自己的**中止原因：这一个问题运维只关心"哪几枚阈值没有主人"，
        # 把它混进 gate_failed 的闸名清单里，就得再去翻 detail 才知道是钱的事。
        knob_detail = [d for n, o, d in results if not o and n == "g9_rule_knobs_baked"]
        if knob_detail:
            return fail("rule_knobs_missing", knob_detail[0])
        failed = [name for name, gate_ok, _d in results if not gate_ok]
        return fail("gate_failed", ",".join(failed))

    try:
        out_dir = out_dir_for(args.out_dir)
    except ValueError as exc:
        return fail("out_dir_invalid", str(exc))

    if not os.path.isdir(out_dir):
        try:
            os.makedirs(out_dir)
        except OSError as exc:
            return fail("out_dir_unwritable", str(exc))

    name = "%s%s%s" % (prefix, version, ARTIFACT_SUFFIX)
    artifact_path = os.path.join(out_dir, name)
    digest = sha256_text(artifact)
    try:
        with open(artifact_path, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(artifact)
    except (IOError, OSError) as exc:
        return fail("artifact_unwritable", str(exc))

    sys.stdout.write("BUILD_RESULT=OK target=%s version=%s sha256=%s artifact=%s\n"
                     % (args.target, version, digest, artifact_path))
    sys.stdout.write("BUILD_GATES gates=%d all_passed=1\n" % len(gates))
    sys.stdout.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
