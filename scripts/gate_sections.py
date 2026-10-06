#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§P2-L（2026-10-07 修复批 波 4）门禁收集模式的段解析器。

为什么需要它（缺陷本体）：`scripts/verify_changes.sh` 在 `set -euo pipefail` 下顺序跑 108 段，
**第一条红就把整轮带走**，后面的段一行不跑。10-06 实录：§104 gofmt 判红 ⇒ §105–107 当轮零读数，
"改了 Go 文件但专项锁从没验过"这件事正是被这个机制埋掉的。`-collect` 要"跑完全部段、收集所有 FAIL、
末尾统一报数"，但把 108 段包成 108 个函数会动到每一段的退出契约（段内到处是 `exit 1`，
函数化后 `if ( set -e; sec ); then` 会把 errexit 关掉、`$1` 语义变掉、跨段变量交接断链），
风险远大于收益——所以这里只做**文本级装配**：把「前言 + 需要的 helper 定义 + 从某段起的尾体」
拼成一个临时脚本再执行，仓库里的门禁脚本一个字不改。

六个子命令（除 `emit` 写指定输出文件外全部只读）：

  sections <gate>                       段清单 + `TOTAL`（派生段数，任何等值锁的分母）
  helpers  <gate>                       跨段 helper 依赖（定义在前段、调用在后段的顶格函数）
  emit     <gate> <start> <out> <root>  生成"从段 start 跑到文件尾"的内层脚本
  classify <gate> <log> <rc>            按日志标记与退出码给本遍分类，输出机器可读键值
  summary  <gate> <work_dir>            汇总各遍 cls_*.txt，跑「四桶之和 == 派生总段数」
  check    <gate>                       前言副作用自检（emit 的正确性前提，见函数 docstring）

设计取舍（写下来免得下次被"顺手简化"掉）：
  * **heredoc 感知且引号感知**：门禁里有一行 `sed -n '/cat <<PSEOF/,/^PSEOF$/p'`，
    一把正则会把它当成真 heredoc 起点，于是从该行起全文被判成正文——段数只解析出 92、
    helper 依赖解析出 0，collect 会把"根本没跑到"读成"跑过了、全绿"。所以 `heredoc_opener()`
    逐字符跟踪引号，只认引号外的 `<<`。
  * **段边界靠注入标记，不靠 parse 日志里的 `==> N`**：段体内也会打印别的段号（镜像反证脚本、
    嵌套 fixture），按文本认必被污染。`emit` 在生成的尾体里每个段头前插一行
    `echo "GATE-SECTION-START <id>"`（只改生成物，不改仓库文件），classify 只认这个标记。
  * **尾体而不是单段**：一遍跑 start..EOF。全绿时 collect 与默认模式同成本（只跑一遍）；
    有 N 条红时才跑 N+1 遍。
  * **静默退出不算 PASS**：一段既不红、后面又没有别的段开始、日志里也没收尾标记 ⇒ SILENT（判红）。
    把「没验过」折算成「验过了」，正是本次改造要消灭的形态。
  * **读日志容错、读源码严格**（§P2-M，2026-10-07 整轮 -collect 第一轮实跑逼出来的）：
    段日志是别人写的字节（go test、pytest、ssh 回来的 PowerShell 读数、以及被 LC_CTYPE=C
    按字节截断的 echo），一个非法字节就把整遍分类打挂，汇总会读成「109 段全 MISSING」——
    那是把「全都验过」误报成「谁都没验」，与本枚改造的目标同形反了个方向。
    所以只有 `cmd_classify` 的日志入口用 errors=replace，源码入口保持严格（源文件坏了＝
    解析器读的不是人们以为在看的那份脚本，任何派生读数都不作数）。细节见 read_lines docstring。
"""

import glob
import os
import re
import sys

# 段头：顶格 `echo "==> <数字> ...`（收尾的 `echo "==> 全部通过"` 没有数字，天然被排除）。
HEADER_RE = re.compile(r'^echo "==> (\d+)([^"]*)"')
DONE_TEXT = "==> 全部通过"
MARK_RE = re.compile(r'^GATE-SECTION-START (\d+)$')
SKIP_RE = re.compile(r'^GATE-SKIP (\d+) · (.*)$')
FUNC_RE = re.compile(r'^([A-Za-z_][A-Za-z0-9_]*)\s*\(\)\s*\{')
CD_RE = re.compile(r'^cd "\$\(dirname "\$0"\)/\.\."$')
DRIVER_BEGIN = "# >>> GATE-COLLECT-DRIVER"
DRIVER_END = "# <<< GATE-COLLECT-DRIVER"


def read_lines(path, errors=None):
    """按行读文本。

    **默认严格（errors=None＝utf-8 严格解码），只有读段日志时调用点显式给 errors="replace"。**
    两套口径不是洁癖，是 §P2-M（2026-10-07）那轮真实事故逼出来的：

      * 门禁**源文件**必须严格。源文件里出现非法 UTF-8 字节＝这个解析器读的根本不是人们
        以为在看的那份脚本，此时任何"派生段数 / helper 依赖"都不作数，宁可在解码处炸掉。
      * **段日志**必须容错。日志是别人（go test、pytest、ssh 回来的 PS 侧读数、被 LC_CTYPE=C
        按字节截断的 echo）写的字节，门禁不拥有它的编码质量。04:39 那轮整轮 -collect 的真实
        形态是：109 段全绿跑完、日志里只有一个被截成半截的中文标点（position 162180 一个
        0xe6），严格解码当场抛 UnicodeDecodeError ⇒ 分类器没有输出 ⇒ 汇总读成
        `BALANCE=MISSING:1,2,…,111`，把"一个字节不可读"放大成"整轮没有结论"。
        这恰好是 P2-L 要消灭的形状（"没验过"穿着"验过了"的衣服出场）的反面同族——
        "验过了"被读成"谁都没验"。所以日志侧按 replace 解码：坏字节换成 U+FFFD，
        段标记、GATE-SKIP 标记、收尾标记这些**判据依赖的 ASCII 结构**一个都不会因此改变。
    """
    with open(path, encoding="utf-8", errors=errors) as fh:
        return fh.read().split("\n")


def heredoc_opener(line):
    """找出本行**引号之外**的 heredoc 重定向，返回 (delimiter, 是否 <<-) 或 None。

    逐字符而不是正则：见模块 docstring 第一条取舍。行内注释之后的内容一律不看。
    """
    in_s = in_d = False
    i = 0
    found = None
    n = len(line)
    apos = chr(39)
    quot = chr(34)
    while i < n:
        c = line[i]
        if in_s:
            if c == apos:
                in_s = False
            i += 1
            continue
        if in_d:
            if c == "\\":
                i += 2
                continue
            if c == quot:
                in_d = False
            i += 1
            continue
        if c == apos:
            in_s = True
            i += 1
            continue
        if c == quot:
            in_d = True
            i += 1
            continue
        if c == "#":
            return found
        if c == "\\":
            i += 2
            continue
        if line.startswith("<<", i):
            if line.startswith("<<<", i):
                i += 3
                continue
            j = i + 2
            dash = False
            if j < n and line[j] == "-":
                dash = True
                j += 1
            while j < n and line[j] in " \t":
                j += 1
            quote = ""
            if j < n and line[j] in (apos, quot):
                quote = line[j]
                j += 1
            mm = re.match(r"[A-Za-z_][A-Za-z0-9_]*", line[j:])
            if mm:
                found = (mm.group(0), dash)
                i = j + len(mm.group(0))
                if quote and i < n and line[i] == quote:
                    i += 1
                continue
        i += 1
    return found


def heredoc_mask(lines):
    """in_heredoc[i]：第 i 行是否是 heredoc **正文**（起始行与结束行算 False）。

    结束定界符按 bash 语义只认顶格（`<<-` 允许制表缩进后顶格）。误判方向是安全的：
    少屏蔽一行 ⇒ 抽到的是普通文本，`bash -n` 立刻炸；多屏蔽一段 ⇒ 段数变少，
    由 `check` 的"派生段数过短即红"正锁接住。
    """
    mask = [False] * len(lines)
    delim = None
    allow_tabs = False
    for i, line in enumerate(lines):
        if delim is not None:
            body = line.lstrip("\t") if allow_tabs else line
            if body == delim:
                delim = None
                continue
            mask[i] = True
            continue
        opener = heredoc_opener(line)
        if opener is not None:
            delim, allow_tabs = opener
    return mask


def section_of(sections, idx):
    for sid, start, end, _title in sections:
        if start <= idx < end:
            return sid
    return None


def parse(lines):
    """返回 (sections, prologue_end, funcs)；索引 0-based，区间左闭右开。"""
    mask = heredoc_mask(lines)
    heads = []
    for i, line in enumerate(lines):
        if mask[i]:
            continue
        m = HEADER_RE.match(line)
        if m:
            heads.append((int(m.group(1)), i, m.group(2).strip().rstrip(".").strip()))
    if not heads:
        raise SystemExit('gate-parse: 没解析出任何段头（^echo "==> <数字>），先确认门禁脚本格式没被改坏')
    prologue_end = heads[0][1]
    sections = []
    for k, (sid, start, title) in enumerate(heads):
        end = heads[k + 1][1] if k + 1 < len(heads) else len(lines)
        sections.append((sid, start, end, title))
    funcs = []
    for i, line in enumerate(lines):
        # §P2-L 反证锤出来的一条：这里**不能**跳过前言（i < prologue_end）。
        # 前言里的 `py_tests()` 是十余段 Python 用例的唯一入口，把它当"非函数副作用语句"排除掉，
        # 后果不是报错而是**内层脚本安静地少搬一个函数**——collect 跑到调用它的那段时报
        # `py_tests: command not found`，读数是"段判红"而不是"装配坏了"，正是本枚改造要防的形态。
        # 正确姿势是全部收集，靠 `section_of()` 返回 None 来区分"前言定义"与"段内定义"。
        if mask[i]:
            continue
        fm = FUNC_RE.match(line)
        if not fm:
            continue
        name = fm.group(1)
        closer = None
        j = i + 1
        while j < len(lines):
            if not mask[j] and lines[j].rstrip() == "}":
                closer = j
                break
            j += 1
        if closer is None:
            raise SystemExit("gate-parse: 函数 %s（第 %d 行）找不到顶格 `}`，helper 抽取无法继续" % (name, i + 1))
        if line.count("{") > 1 and closer == i + 1:
            # 定义行内还有别的 `{` 却次行就顶格 `}` ⇒ 多半是被截断的假结尾（宁可炸也不静默少搬）。
            raise SystemExit("gate-parse: 函数 %s（第 %d 行）的结尾可疑（定义行内含多个 `{`）" % (name, i + 1))
        funcs.append((name, i, closer + 1, section_of(sections, i)))
    return sections, prologue_end, funcs


def word_re(name):
    return re.compile(r'(?<![\w])' + re.escape(name) + r'(?![\w])')


def cross_section_helpers(sections, funcs, lines, mask):
    """定义在某段、被更晚的段**调用**（注释与 heredoc 正文里的提及不算调用）的顶格函数。"""
    first_by_name = {}
    for name, start, end, sid in funcs:
        if name not in first_by_name:
            first_by_name[name] = (start, end, sid)
    out = []
    for name, (_s, _e, producer) in sorted(first_by_name.items()):
        if producer is None:
            continue  # 前言里的定义本来就会搬，不属于"跨段搬运"
        own_sections = {sid for _n, _a, _b, sid in funcs if _n == name}
        consumers = []
        pat = word_re(name)
        for cs, cstart, cend, _t in sections:
            if cs <= producer or cs in own_sections:
                continue
            hit = False
            for i in range(cstart, cend):
                if mask[i] or lines[i].lstrip().startswith("#"):
                    continue
                if FUNC_RE.match(lines[i]) and lines[i].startswith(name + "()"):
                    continue  # 重定义行本身不算调用
                if pat.search(lines[i]):
                    hit = True
                    break
            if hit:
                consumers.append(cs)
        if consumers:
            out.append((name, producer, consumers, len(own_sections) > 1))
    return out


def cmd_sections(gate):
    lines = read_lines(gate)
    sections, prologue_end, _funcs = parse(lines)
    for sid, start, end, title in sections:
        print("%d\t%d\t%d\t%s" % (sid, start + 1, end + 1, title[:70]))
    print("TOTAL\t%d\tPROLOGUE\t%d" % (len(sections), prologue_end))


def cmd_helpers(gate):
    lines = read_lines(gate)
    mask = heredoc_mask(lines)
    sections, _pe, funcs = parse(lines)
    deps = cross_section_helpers(sections, funcs, lines, mask)
    for name, producer, consumers, dup in deps:
        print("%s\tdef@%s\tuse@%s%s" % (name, producer, ",".join(str(c) for c in consumers),
                                        "\t(同名重复定义)" if dup else ""))
    print("HELPDERIVED\t%d" % len(deps))


def build_inner(gate, start_id, root):
    """内层脚本文本：前言（set/cd + 顶格函数定义）+ 跨段 helper 搬运 + 带标记的尾体。"""
    lines = read_lines(gate)
    mask = heredoc_mask(lines)
    sections, _pe, funcs = parse(lines)
    ids = [s[0] for s in sections]
    if start_id not in ids:
        raise SystemExit("gate-emit: 段号 %s 不在派生段清单里（共 %d 段）" % (start_id, len(ids)))
    body_start = [s for s in sections if s[0] == start_id][0][1]

    prelude = []
    prelude_names = []
    for name, s, e, sid in funcs:
        if sid is not None:
            continue
        prelude.append("\n".join(lines[s:e]))
        prelude_names.append(name)

    deps = cross_section_helpers(sections, funcs, lines, mask)
    for name, producer, consumers, _dup in deps:
        if not any(c >= start_id for c in consumers):
            continue
        if name in prelude_names:
            continue
        for n2, s2, e2, sid2 in funcs:
            if n2 == name and (sid2 is None or sid2 < start_id):
                prelude_names.append(name)
                prelude.append("# helper %s：原定义在第 %d 行（段 %s），collect 前置搬运" % (name, s2 + 1, sid2))
                prelude.append("\n".join(lines[s2:e2]))
                break

    tail = []
    for i in range(body_start, len(lines)):
        if not mask[i]:
            m = HEADER_RE.match(lines[i])
            if m:
                tail.append('echo "GATE-SECTION-START %s"' % m.group(1))
        tail.append(lines[i])

    parts = [
        "#!/usr/bin/env bash",
        "# §P2-L collect 内层脚本：由 scripts/gate_sections.py 从 %s 装配，只跑段 %s 起到文件尾。" % (gate, start_id),
        "# 仓库里的段体一字未改；变的只有 cd 目标（相对 $0 会指向 /tmp）与前置 helper 搬运。",
        "set -euo pipefail",
        'cd "%s"' % root,
        "\n".join(prelude),
        "\n".join(tail).rstrip("\n"),
    ]
    text = "\n".join(p for p in parts if p != "") + "\n"
    return text, prelude_names


def cmd_emit(gate, start_id, out_path, root):
    text, prelude_names = build_inner(gate, int(start_id), root)
    with open(out_path, "w", encoding="utf-8") as fh:
        fh.write(text)
    os.chmod(out_path, 0o755)
    sys.stderr.write("gate-emit: %s start=§%s lines=%d prelude=%s\n" % (
        out_path, start_id, text.count("\n"), ",".join(prelude_names) or "-"))


def cmd_classify(gate, log_path, rc):
    lines = read_lines(gate)
    sections, _pe, _funcs = parse(lines)
    ids = [s[0] for s in sections]
    # 唯一的"读别人写的字节"的地方 ⇒ 容错解码（§P2-M，理由见 read_lines docstring）。
    log = read_lines(log_path, errors="replace")
    started = []
    skipped = {}
    for line in log:
        m = MARK_RE.match(line)
        if m:
            sid = int(m.group(1))
            if sid in ids and sid not in started:
                started.append(sid)
            continue
        sm = SKIP_RE.match(line)
        if sm and int(sm.group(1)) in ids:
            skipped[int(sm.group(1))] = sm.group(2)
    done = any(line.startswith(DONE_TEXT) for line in log)
    rc = int(rc)
    fail_id = ""
    silent_id = ""
    pass_ids = [x for x in started if x not in skipped]
    if started:
        if rc != 0:
            fail_id = started[-1]
        elif started[-1] in skipped:
            # 本遍是 gate_need 主动止步（打的是 GATE-SKIP 标记 + exit 0），不是"跑完没结论"：
            # 这段既不能算 PASS（它自己的判据一条没验），也不能算 SILENT（SILENT 的含义是
            # "脚本没报错却什么结论都没有"，那是装配/段体坏了，成因完全不同、修法也不同）。
            pass
        elif not done:
            silent_id = started[-1]
        pass_ids = [x for x in pass_ids if x not in (fail_id, silent_id)]
    nxt = ""
    need_tail = bool(fail_id or silent_id) or (bool(started) and not done)
    if need_tail and started:
        pos = ids.index(started[-1])
        if pos + 1 < len(ids):
            nxt = ids[pos + 1]
    out = {
        "STARTED": ",".join(str(x) for x in started),
        "PASS": ",".join(str(x) for x in pass_ids),
        "FAIL": str(fail_id),
        "SKIP": ";".join("%d·%s" % (k, v) for k, v in sorted(skipped.items())),
        "SILENT": str(silent_id),
        "DONE": "1" if done else "0",
        "NEXT": str(nxt),
        # 一遍连一个段标记都没有＝内层脚本前言/装配坏了。这种"零段绿"必须单独可辨，
        # 否则 collect 最坏的失效形态（什么都没跑却报全绿）就又回来了。
        "EMPTY": "1" if not started else "0",
    }
    for k in ("STARTED", "PASS", "FAIL", "SKIP", "SILENT", "DONE", "NEXT", "EMPTY"):
        print("%s\t%s" % (k, out[k]))


def cmd_summary(gate, work_dir):
    """汇总各遍 cls_*.txt：桶间互斥 + 四桶之和 == 派生总段数，缺一段/重一段都判红。"""
    lines = read_lines(gate)
    sections, _pe, _funcs = parse(lines)
    ids = [s[0] for s in sections]
    buckets = {"PASS": [], "FAIL": [], "SKIP": [], "SILENT": []}
    skip_reason = {}
    empties = []
    for path in sorted(glob.glob(os.path.join(work_dir, "cls_*.txt"))):
        kv = {}
        with open(path, encoding="utf-8") as fh:
            for raw in fh.read().split("\n"):
                if "\t" not in raw:
                    continue
                k, v = raw.split("\t", 1)
                kv[k] = v
        for key in ("PASS", "FAIL", "SILENT"):
            # 这三桶是裸段号，逗号分隔。
            for tok in (kv.get(key, "") or "").split(","):
                if tok.strip().isdigit():
                    buckets[key].append(int(tok))
        # SKIP 桶单独解：classify 输出的是 `id·原因` 用分号串起来的形态，
        # 拿逗号去切它会得到 "4·fixture：…" 这种非数字串，`isdigit()` 一律为假 ⇒
        # 四桶之和凭空少一段，等值锁以 MISSING 判红（2026-10-07 夹具第一轮实跑就是这个形状）。
        for chunk in (kv.get("SKIP", "") or "").split(";"):
            if "·" in chunk:
                sid, reason = chunk.split("·", 1)
                if sid.strip().isdigit():
                    buckets["SKIP"].append(int(sid))
                    skip_reason[int(sid)] = reason
            elif chunk.strip().isdigit():
                buckets["SKIP"].append(int(chunk))
        if kv.get("EMPTY") == "1":
            empties.append(os.path.basename(path))
    allids = buckets["PASS"] + buckets["FAIL"] + buckets["SKIP"] + buckets["SILENT"]
    dup = sorted({x for x in allids if allids.count(x) > 1})
    missing = sorted(set(ids) - set(allids))
    balance = "OK"
    if dup:
        balance = "DUPLICATE:" + ",".join(str(x) for x in dup)
    elif missing:
        balance = "MISSING:" + ",".join(str(x) for x in missing)
    print("PASS_SECTIONS\t%d" % len(buckets["PASS"]))
    print("FAIL_SECTIONS\t%d" % len(buckets["FAIL"]))
    print("SKIP_SECTIONS\t%d" % len(buckets["SKIP"]))
    print("SILENT_SECTIONS\t%d" % len(buckets["SILENT"]))
    print("TOTAL_SECTIONS\t%d" % len(ids))
    print("BALANCE\t%s" % balance)
    # PASS_IDS 也点名（只有四个计数不够用：H3 要断"消费段记在 SKIP、没记在 PASS"，
    # 光看 PASS_SECTIONS 的数字看不出一个段是不是两边都挂着——DUPLICATE 只抓同桶外的重复）。
    print("PASS_IDS\t%s" % ",".join(str(x) for x in sorted(set(buckets["PASS"]))))
    print("FAIL_IDS\t%s" % ",".join(str(x) for x in sorted(set(buckets["FAIL"]))))
    print("SKIP_IDS\t%s" % ",".join(str(x) for x in sorted(set(buckets["SKIP"]))))
    print("SILENT_IDS\t%s" % ",".join(str(x) for x in sorted(set(buckets["SILENT"]))))
    print("EMPTY_PASSES\t%s" % ",".join(empties))
    for sid in sorted(skip_reason):
        print("SKIP_REASON\t%d·%s" % (sid, skip_reason[sid]))


def cmd_check(gate):
    """emit 的正确性前提：前言（第一段之前）除 `set`/`cd`/驱动标记区/顶格函数定义外不许有副作用。

    为什么必须钉住：内层脚本只搬「前言里的函数定义」，不搬前言里的普通语句。今天前言恰好只有
    set/cd/py_tests，所以搬运等价；哪天有人往前言加一句 `FOO=$(...)` 或 `mkdir`，
    collect 的内层脚本会**安静地少做那件事**，读数就不属于那段了——"少搬一步"没人会看见。
    """
    lines = read_lines(gate)
    mask = heredoc_mask(lines)
    sections, prologue_end, funcs = parse(lines)
    func_lines = set()
    for _n, s, e, _sid in funcs:
        for i in range(s, e):
            func_lines.add(i)
    driver_zone = [False] * len(lines)
    inside = False
    for i, line in enumerate(lines):
        if line.startswith(DRIVER_BEGIN):
            inside = True
        if i < prologue_end:
            driver_zone[i] = inside
        if line.startswith(DRIVER_END):
            inside = False
    bad = []
    for i in range(prologue_end):
        if mask[i] or i in func_lines or driver_zone[i]:
            continue
        raw = lines[i]
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if raw == "set -euo pipefail" or CD_RE.match(raw):
            continue
        bad.append((i + 1, raw[:70]))
    print("CHECK_SECTIONS\t%d" % len(sections))
    print("CHECK_PROLOGUE_SIDE_EFFECTS\t%d" % len(bad))
    for ln, raw in bad:
        print("OFFENDER\t%d\t%s" % (ln, raw))
    if len(sections) < 50:
        print("CHECK\tFAIL 派生段数 %d 过短（本仓门禁应为 100+ 段）＝解析与脚本格式脱节，collect 读数无意义" % len(sections))
        return 1
    if bad:
        print("CHECK\tFAIL 前言里有 %d 处非函数副作用语句，collect 装配不会搬运它们（见 OFFENDER 行）" % len(bad))
        return 1
    print("CHECK\tOK")
    return 0


def main(argv):
    if len(argv) < 3:
        raise SystemExit(__doc__)
    cmd = argv[1]
    if cmd == "sections":
        cmd_sections(argv[2])
    elif cmd == "helpers":
        cmd_helpers(argv[2])
    elif cmd == "emit":
        cmd_emit(argv[2], argv[3], argv[4], argv[5])
    elif cmd == "classify":
        cmd_classify(argv[2], argv[3], argv[4])
    elif cmd == "summary":
        cmd_summary(argv[2], argv[3])
    elif cmd == "check":
        return cmd_check(argv[2])
    else:
        raise SystemExit("gate_sections: 未知子命令 %r" % cmd)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
