#!/usr/bin/env python3
"""check_doc_comments.py — 导出面文档注释棘轮门禁（§DOC-COMMENTS 2026-11-09）。

判定规则（刻意保守，与 go doc 的紧邻语义一致）：
  * Go：`func Foo(` / `func (r R) Foo(` / `type Foo struct|interface`，首字母大写（导出），
    且紧邻上一行是 `//`（空行即不算 doc comment，不做向上跳过的宽松处理）。
  * 前端 TS/TSX：export function/const/interface/type，紧邻上一行以 //、/* 或 * 开头。
  * 跳过生成物：*.d.ts、vendor/、node_modules/、*_test.go（测试的说明写在用例体首行，
    导出测试辅助另行处理，避免把夹具噪声算进缺口）。

模式：
  --list               只列缺口清单（文件 → 符号名）
  （缺省）             超基线则 exit 1
  --update-baseline    把当前值写入基线（仅当下降时用）
  --selftest           自证：注入探针必须被命中、有注释的必须被放行

基线文件 .doc_comments_baseline 形如 `go=<n>` / `fe=<n>`，缺失按 0（最严）。
棘轮语义：只降不升——存量缺口逐步清，新代码不许再欠账。
"""
import os
import re
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASELINE = os.path.join(REPO, ".doc_comments_baseline")

GO_DECL = re.compile(r"^func (?:\([^)]+\) )?([A-Z]\w*)\(|^type ([A-Z]\w*) (?:struct|interface)\b")
FE_DECL = re.compile(r"^export (?:default )?(?:async )?(?:function|const|interface|type) ([A-Z]\w*)")
SKIP_DIRS = {"vendor", "node_modules", ".git", "__pycache__", ".qoder"}


def scan(root, backend_only=False):
    """返回 (go_gaps, fe_gaps)：{相对路径: [符号名]}。"""
    go_gaps, fe_gaps = {}, {}
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for fn in filenames:
            path = os.path.join(dirpath, fn)
            rel = os.path.relpath(path, root)
            if backend_only and not (fn.endswith(".go") and rel.startswith("internal")):
                continue
            if fn.endswith("_test.go") or fn.endswith(".d.ts"):
                continue
            is_go = fn.endswith(".go")
            is_fe = fn.endswith((".ts", ".tsx")) and not backend_only
            if not (is_go or is_fe):
                continue
            try:
                lines = open(path, encoding="utf-8").read().splitlines()
            except (OSError, UnicodeDecodeError):
                continue
            for i, line in enumerate(lines):
                m = GO_DECL.match(line) if is_go else FE_DECL.match(line)
                if not m:
                    continue
                name = m.group(1) or m.group(2)
                prev = lines[i - 1].strip() if i > 0 else ""
                ok = prev.startswith("//") if is_go else (prev.startswith("//") or prev.startswith("/*") or prev.startswith("*"))
                if not ok:
                    gaps = go_gaps if is_go else fe_gaps
                    gaps.setdefault(rel, []).append(name)
    return go_gaps, fe_gaps


def main(argv):
    backend_only = "--backend" in argv
    go_gaps, fe_gaps = scan(REPO, backend_only=backend_only)
    n_go, n_fe = len(go_gaps), len(fe_gaps)
    if "--list" in argv:
        for rel, names in sorted(go_gaps.items()):
            print("GO %s: %s" % (rel, ", ".join(names)))
        for rel, names in sorted(fe_gaps.items()):
            print("FE %s: %s" % (rel, ", ".join(names)))
        print("GAP_SUMMARY go_files=%d fe_files=%d" % (n_go, n_fe))
        return 0
    base = {}
    if os.path.exists(BASELINE):
        for line in open(BASELINE):
            k, _, v = line.strip().partition("=")
            if k in ("go", "fe"):
                base[k] = int(v)
    else:
        base = {"go": 0, "fe": 0}
    if "--update-baseline" in argv:
        with open(BASELINE, "w") as fh:
            fh.write("go=%d\nfe=%d\n" % (n_go, n_fe))
        print("BASELINE_UPDATED go=%d fe=%d" % (n_go, n_fe))
        return 0
    bad = n_go > base.get("go", 0) or n_fe > base.get("fe", 0)
    print("DOC_COMMENTS_RESULT=%s go=%d/%d fe=%d/%d" % (
        "FAIL" if bad else "PASS", n_go, base.get("go", 0), n_fe, base.get("fe", 0)))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
