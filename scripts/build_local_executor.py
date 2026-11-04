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
        lines.append("    %r: %r," % (key, params[key]))
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
]

SANDBOX_ALLOWED_IMPORTS = {"builtins", "datetime", "hashlib", "time"}

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

    ok, results = run_gates(artifact, gates)
    if not ok:
        # No artifact is written: a half-gated file on disk is worse than
        # no file, because the next deploy would happily pick it up.
        for name, gate_ok, detail in results:
            if not gate_ok:
                sys.stdout.write("GATE_FAIL gate=%s detail=%s\n" % (name, detail))
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
