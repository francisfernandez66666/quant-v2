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

# 段头：顶格 `echo "==> <编号> ...`（收尾的 `echo "==> 全部通过"` 没有编号，天然被排除）。
# §GATE-SELFACC（2026-10-15 批）：编号允许带小写字母后缀（`34b`／`34c` 这一形是本仓历史上
#   往老段后面追加新段时留下的），并且**整个编号串就是段的身份**。为什么不能只取数字：
#   `==> 34` / `==> 34b` / `==> 34c` 三行按旧口径解析出同一个 id 34，于是
#     ① `TOTAL_SECTIONS=len(ids)`=119 与四桶之和（按 id 去重只能到 117）天生对不上＝本批
#        PROGRESS 记的「TOTAL=116 vs 判数 114」那笔账；
#     ② 更要命的是 collect 的推进逻辑：红发生在第二个 34 时 classify 的 NEXT 仍是 34，
#        而 emit 的 `body_start` 按 `[s for s in sections if s[0]==start_id][0]` 取**第一处**，
#        下一遍从同一个红点重跑同样一段——直到 GATE_COLLECT_MAX_PASSES=60 用尽，
#        后面的段一条读数都没有，还烧满 60 遍尾体（2026-10-15 用 /tmp 最小夹具实测复现：
#        STARTED=1,2 / FAIL=2 / NEXT=2，emit 2 的尾体第一行落在 §甲）。
#   ⇒ 编号带后缀、身份唯一、重复即炸（见 parse() 的 uniqueness 守卫），三处坏法一起断根。
HEADER_RE = re.compile(r'^echo "==> (\d+[a-z]?)([^"]*)"')
DONE_TEXT = "==> 全部通过"
MARK_RE = re.compile(r'^GATE-SECTION-START ([0-9]+[a-z]?)$')
SKIP_RE = re.compile(r'^GATE-SKIP ([0-9]+[a-z]?) · (.*)$')
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


def heredoc_openers(lines, mask):
    """opener_of[i]：若第 i 行是 heredoc **正文**（即 mask[i] 为 True），记它所属 opener 的行号；否则 -1。

    与 heredoc_mask 同一次 O(n) 扫描顺手算出，供 _heredoc_complete 把「回退到 opener」从
    O(body_start) 降到 O(1)：每段的单元装配不再从文件头重扫到段头（173 段累计 O(n²) 正是
    selftest / parallel_units 单段装配卡死的元凶）。
    """
    opener_of = [-1] * len(lines)
    delim = None
    allow_tabs = False
    start = -1
    for i, line in enumerate(lines):
        if delim is not None:
            body = line.lstrip("\t") if allow_tabs else line
            if body == delim:
                delim = None
                start = -1
                continue
            if start != -1:
                opener_of[i] = start
            continue
        opener = heredoc_opener(line)
        if opener is not None:
            delim, allow_tabs = opener
            start = i
    return opener_of


def section_of(sections, idx):
    for sid, start, end, _title in sections:
        if start <= idx < end:
            return sid
    return None


def parse(lines):
    """返回 (sections, prologue_end, funcs)；索引 0-based，区间左闭右开。

    段的身份是**编号整串**（`"34"`、`"34b"`），不是数字：见 HEADER_RE 上面那段成因。
    编号重复＝三段共用一个身份，classify/emit/summary 会把"跑过三段"折成"跑过一段"，
    还会让 collect 在第二个同id 段上原地打转（NEXT 指回同一个起点、emit 永远取第一处）。
    与其让汇总安静地少一段，不如解析当场炸：谁在同一天撞两个编号，谁就得当场改名。
    """
    mask = heredoc_mask(lines)
    heads = []
    for i, line in enumerate(lines):
        if mask[i]:
            continue
        m = HEADER_RE.match(line)
        if m:
            heads.append((m.group(1), i, m.group(2).strip().rstrip(".").strip()))
    if not heads:
        raise SystemExit('gate-parse: 没解析出任何段头（^echo "==> <编号>），先确认门禁脚本格式没被改坏')
    seen = {}
    for sid, start, _title in heads:
        seen.setdefault(sid, []).append(start + 1)
    for sid, rows in sorted(seen.items()):
        if len(rows) > 1:
            raise SystemExit(
                "gate-parse: 段编号 %s 重复出现在第 %s 行——collect 的推进按编号定位，同编号的多段会被折成一段"
                "（汇总少一段），红在第二段时下一遍还会从第一段重跑（最多烧满 GATE_COLLECT_MAX_PASSES 遍）。"
                "追加段请写成 %sb／%sc 这样的带后缀唯一编号，或直接用下一个新号" % (sid, ",".join(str(r) for r in rows), sid, sid))
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


_CSH_CACHE = {}


def cross_section_helpers(sections, funcs, lines, mask):
    """定义在某段、被更晚的段**调用**（注释与 heredoc 正文里的提及不算调用）的顶格函数。

    纯函数（只依赖传入的 lines/mask/sections/funcs），故按 id(lines) 记忆化：_assemble 在
    selftest / parallel_units 里对 173 段各调一次，若不缓存就是 173 × O(funcs×sections×lines)
    的 O(n³) 量级——实测单段装配卡死的真凶。缓存后整轮只算一次。
    """
    key = id(lines)
    if key in _CSH_CACHE:
        return _CSH_CACHE[key]
    first_by_name = {}
    for name, start, end, sid in funcs:
        if name not in first_by_name:
            first_by_name[name] = (start, end, sid)
    out = []
    for name, (pstart, _e, producer) in sorted(first_by_name.items()):
        if producer is None:
            continue  # 前言里的定义本来就会搬，不属于"跨段搬运"
        own_sections = {sid for _n, _a, _b, sid in funcs if _n == name}
        consumers = []
        pat = word_re(name)
        for cs, cstart, cend, _t in sections:
            # "被更晚的段调用"按**文件位置**判，不按编号比大小：编号自 §GATE-SELFACC 起是字符串
            #   （`34b` 与 `34` 比大小会当场 TypeError），而段的先后本来就以行序为准。
            if cstart <= pstart or cs in own_sections:
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
    _CSH_CACHE[key] = out
    return out


def cmd_sections(gate):
    lines = read_lines(gate)
    sections, prologue_end, _funcs = parse(lines)
    for sid, start, end, title in sections:
        print("%s\t%d\t%d\t%s" % (sid, start + 1, end + 1, title[:70]))
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


def build_inner(gate, start_id, end_id, root):
    """内层脚本文本：前言（set/cd + 顶格函数定义）+ 跨段 helper 搬运 + 带标记的尾体。

    `end_id=None` 表示跑到文件尾（旧 `emit` 尾体语义）；`end_id=<某段号>` 表示只跑到该段
    **之前**（即「单段单元」或任意区间）。并行收集模式用后者把每一段装配成自包含独立脚本，
    跨段变量/helper 依赖靠「把更早段定义的 helper 一并搬进前言」消除——消费段自带所需 helper，
    不需要运行时串行。
    """
    lines = read_lines(gate)
    mask = heredoc_mask(lines)
    sections, _pe, funcs = parse(lines)
    return _assemble(lines, mask, sections, funcs, gate, start_id, end_id, root)


def _heredoc_complete(lines, mask, opener_of, body_start, body_end):
    """把区间 [body_start, body_end) 调整到最近的 heredoc 边界，保证单元内 heredoc 全部闭合。

    为什么必须：emit-one 取 [某段头, 下一段头) 时，若该段尾有个 heredoc 开在下段才闭合（或上段
    开的 heredoc在本段头仍未闭），直接切会得到"未闭合 heredoc"——bash 解析卡 stdin 等终止符，
    bash -n 都挂。延伸到 opener / terminator 即可。延伸到 terminator 可能把下一段头也带进来
    （那一段头在 heredoc 体内＝数据、不会被执行，无害）；延伸到 opener 可能把上一段尾带进来
    （同理是 heredoc 数据）。两种情况都不影响被装配段的判据，只保证语法完整不自锁。

    opener_of[i] 是 heredoc_openers() 预计算的「逐行 opener 行号」（-1 表示不在 heredoc 体内），
    把原实现「从文件头重扫到 body_start」的 O(body_start)（173 段累计 O(n²)）降到 O(1)；
    向前延伸到闭合则直接读 mask（已编码整份文件的 heredoc 结构），O(段长)。
    """
    n = len(lines)
    # body_start 落在未闭合 heredoc 体内 ⇒ 回退到该 heredoc 的 opener（O(1)）。
    if body_start < n and opener_of[body_start] != -1:
        body_start = opener_of[body_start]
    # body_end 若落在 heredoc 体内 ⇒ 向前延伸到闭合行（mask 已含整份结构，O(段长)）。
    while body_end < n and mask[body_end]:
        body_end += 1
    return body_start, body_end


def _assemble(lines, mask, sections, funcs, gate, start_id, end_id, root, deps=None, opener_of=None,
              exports=None, producer_vars=None, work=None):
    """build_inner 的无重解析内核：接收已解析的 lines/mask/sections，产出内层脚本文本。

    emit-one / selftest / parallel_units 各自只解析一次门禁文件，再对 173 段反复调用本函数，
    避免每段重跑 heredoc_mask 的逐字符扫描（21k 行 × 173 段会拖垮自检与调度）。

    `deps` / `opener_of` 是预计算的跨段 helper 依赖与 heredoc opener 数组；173 段循环调用时由
    调用方算一次传入，避免每段重算（cross_section_helpers 是 O(funcs×sections×lines) 的纯函数，
    已按 id(lines) 记忆化兜底；opener_of 同理用 heredoc_openers 一次 O(n) 算出）。两个参数都缺省
    None ⇒ 函数内惰性补全，保持 build_inner / cmd_emit_one 旧调用形态不破。
    """
    ids = [s[0] for s in sections]
    if start_id not in ids:
        raise SystemExit("gate-emit: 段号 %s 不在派生段清单里（共 %d 段）" % (start_id, len(ids)))
    body_start = [s for s in sections if s[0] == start_id][0][1]
    # 段的"先后"一律按文件位置比，不按编号比大小：编号是字符串（`34b`），而且历史上确有
    #   带后缀的编号排在纯数字编号后面（§GATE-SELFACC）。order 就是派生段清单的下标。
    order = {sid: k for k, sid in enumerate(ids)}
    start_pos = order[start_id]
    if end_id is None:
        body_end = len(lines)
    else:
        if end_id not in order:
            raise SystemExit("gate-emit: 结束段号 %s 不在派生段清单里" % end_id)
        body_end = [s for s in sections if s[0] == end_id][0][1]
    # 不允许在 heredoc 体内截断：emit-one 的区间 [start, next) 可能把"开头在本段、结束在下段"的
    # heredoc 腰斩 ⇒ 单元里出现未闭合 heredoc ⇒ bash 解析时卡 stdin 等终止符（bash -n 也会挂）。
    # 把区间两端都延伸到最近的 heredoc 边界，保证单元里的 heredoc 全部完整闭合。
    if opener_of is None:
        opener_of = heredoc_openers(lines, mask)
    body_start, body_end = _heredoc_complete(lines, mask, opener_of, body_start, body_end)

    prelude = []
    prelude_names = []
    for name, s, e, sid in funcs:
        if sid is not None:
            continue
        prelude.append("\n".join(lines[s:e]))
        prelude_names.append(name)

    if deps is None:
        deps = cross_section_helpers(sections, funcs, lines, mask)
    for name, producer, consumers, _dup in deps:
        if not any(order[c] >= start_pos for c in consumers if c in order):
            continue
        if name in prelude_names:
            continue
        for n2, s2, e2, sid2 in funcs:
            if n2 == name and (sid2 is None or order.get(sid2, -1) < start_pos):
                prelude_names.append(name)
                prelude.append("# helper %s：原定义在第 %d 行（段 %s），collect 前置搬运" % (name, s2 + 1, sid2))
                prelude.append("\n".join(lines[s2:e2]))
                break

    tail = []
    for i in range(body_start, body_end):
        if not mask[i]:
            m = HEADER_RE.match(lines[i])
            if m:
                tail.append('echo "GATE-SECTION-START %s"' % m.group(1))
        tail.append(lines[i])

    # 并行跨进程变量桥接（gate_need 语义保真）：消费段在段体之前 source 各生产者段导出的环境变量文件。
    # 串行尾体模式下，生产者段与消费段跑在同一个 bash 作用域，IP_HITS 这类"前段派生的读数"天然可见；
    # 孤立单元里消费段的进程读不到，gate_need 会把消费段永远判成 SKIP（假信号）。用文件桥接复刻串行语义：
    # 生产者成功退出时把流动变量 declare -p 落盘，消费段启动即 source；生产者失败（文件不写）⇒ 消费段
    # 读不到 ⇒ 也 SKIP，与串行 collect「前段红⇒本遍尾体死在那段、下一遍从其后起重跑、消费段同样读不到 SKIP」同形。
    if exports and start_id in exports and work:
        for _p in exports[start_id]:
            tail.insert(0, 'source "%s/gate_exports_%s.sh" 2>/dev/null || true' % (work, _p))

    # 生产者侧：成功退出时把流动变量落盘（见上面 source 注释）。仅 exit 0 才写，失败不写。
    extra = []
    if producer_vars and start_id in producer_vars and work:
        evars = " ".join(sorted(set(producer_vars[start_id])))
        extra.append('GATE_EXPORTS_FILE="%s/gate_exports_%s.sh"' % (work, start_id))
        extra.append(
            "trap 'if [ \"$?\" = \"0\" ]; then declare -p %s > \"$GATE_EXPORTS_FILE\" 2>/dev/null; fi' EXIT" % evars)

    parts = [
        "#!/usr/bin/env bash",
        "# §P2-L collect 内层脚本：由 scripts/gate_sections.py 装配，只跑段 %s%s。" % (
            start_id, " 起到文件尾" if end_id is None else " 单段（含全部更早 helper）"),
        "# 仓库里的段体一字未改；变的只有 cd 目标（相对 $0 会指向 /tmp）与前置 helper 搬运。",
        "set -euo pipefail",
        'cd "%s"' % root,
        # §GZ-UBUNTU（11-07 批起）文件尾总闸被 emit 带进段尾（emit 本就「跑到文件尾」）；它的
        # 注释自述「段体被搬进临时脚本执行时也指得回仓内 scripts/」——所以这里必须把主脚本的
        # GATE_SELF_ABS 等值定义补进内层前言。11-07→11-10 三个批次里任何一段 single-emit 都以
        # 「GATE_SELF_ABS: unbound variable」收尾＝不是判据红、是装配器漏写定义（§173 的假红即此）。
        "GATE_SELF_ABS=\"%s\"" % os.path.abspath(gate),
        "\n".join(extra),
        "\n".join(prelude),
        "\n".join(tail).rstrip("\n"),
    ]
    text = "\n".join(p for p in parts if p != "") + "\n"
    return text, prelude_names


def cmd_emit(gate, start_id, out_path, root):
    # start_id 原样传（字符串编号，可能是 `34b`）；旧口径的 int() 会把带后缀的编号当场炸掉。
    # end_id=None ⇒ 跑到文件尾（旧尾体语义，串行 -collect 仍用这条）。
    text, prelude_names = build_inner(gate, start_id, None, root)
    with open(out_path, "w", encoding="utf-8") as fh:
        fh.write(text)
    os.chmod(out_path, 0o755)
    sys.stderr.write("gate-emit: %s start=§%s lines=%d prelude=%s\n" % (
        out_path, start_id, text.count("\n"), ",".join(prelude_names) or "-"))


def next_section_id(sections, sid):
    """返回 sid 在派生段清单里的下一编号；sid 是最后一段时返回 None。"""
    ids = [s[0] for s in sections]
    k = ids.index(sid)
    return ids[k + 1] if k + 1 < len(ids) else None


def cmd_emit_one(gate, sid, out_path, root):
    """把**单段**装配成自包含独立脚本（带 GATE-SECTION-START 标记，含全部更早 helper）。

    区间取 [sid, 下一编号)：最后一段则 [sid, 文件尾)（含 §GZ-UBUNTU 终端总闸，与串行尾体一致）。
    """
    lines = read_lines(gate)
    mask = heredoc_mask(lines)
    sections, _pe, funcs = parse(lines)
    ids = [s[0] for s in sections]
    if sid not in ids:
        raise SystemExit("gate-emit-one: 段号 %s 不在派生段清单里（共 %d 段）" % (sid, len(ids)))
    nxt = next_section_id(sections, sid)
    text, prelude_names = _assemble(lines, mask, sections, funcs, gate, sid, nxt, root)
    with open(out_path, "w", encoding="utf-8") as fh:
        fh.write(text)
    os.chmod(out_path, 0o755)
    sys.stderr.write("gate-emit-one: %s end=%s out=%s prelude=%s\n" % (
        sid, nxt or "EOF", out_path, ",".join(prelude_names) or "-"))


def derive_edges(gate):
    """派生跨段产物依赖边（producer → consumer），供并行调度保序。来源：

    ① `gate_need <段号> "$VAR"` 声明里被校验的 `$VAR` 在更早单一段赋值 ⇒ 边。这是门禁作者声明
       跨段依赖的正式通道；未来新增的「直接变量」依赖会自动被抓到。
    ② KNOWN_SEMANTIC_EDGES：gate_need 用**本地代理变量**间接表达、自动检测看不见的依赖。
       目前只有一条：§107 → §110——§110 读 §107 派生扫描的 IP_HITS / IP_SCAN_N 读数
       （注释明写「§110 不再自己写第二遍扫描」），但 gate_need 校验的是段内据 IP_HITS 算出的
       本地 IP110_HAS，自动检测只能看到 IP110_HAS 在 §110 自己段里赋值，看不到真正的跨段链路
       ——故显式点名。

    为什么不扫「跨段变量读」做通用推断：串行尾体模式下所有段跑在同一个 bash 作用域，单字母/短变量
    （$g/$b/$_/$V/$bad/$fn…）会"顺带"从更早段泄漏到更晚段，但并不是真依赖；并行模式下每段是独立
    进程，这些泄漏本就不存在。把这种泄漏误判成依赖会过度串行化，且无助于正确性。真·跨段依赖请走
    gate_need（或直接加进 KNOWN_SEMANTIC_EDGES）。
    """
    lines = read_lines(gate)
    mask = heredoc_mask(lines)
    sections, _pe, funcs = parse(lines)
    ids = [s[0] for s in sections]
    order = {sid: k for k, sid in enumerate(ids)}
    sec_of_line = {}
    for sid, start, end, _t in sections:
        for i in range(start, end):
            sec_of_line[i] = sid
    # 不只认行首赋值：`then VAR=`、`; VAR=`、缩进后的 `VAR=$( )` 都要抓到。左侧锚「非单词字符
    # 且非 =」、右侧排除 `==`，避免误吞比较表达式。
    assign_re = re.compile(r'(?<![\w=])([A-Za-z_][A-Za-z0-9_]*)=(?!=)')
    var_assigners = {}      # var -> 赋值它的段集合
    for i, line in enumerate(lines):
        if mask[i] or i not in sec_of_line:
            continue
        for m in assign_re.finditer(line):
            var_assigners.setdefault(m.group(1), set()).add(sec_of_line[i])
    edges = []
    seen = set()

    def add_edge(p, c):
        if p not in order or c not in order or order[p] >= order[c]:
            return
        e = (p, c)
        if e not in seen:
            seen.add(e)
            edges.append(e)

    # ① gate_need 声明边
    need_re = re.compile(r'^\s*gate_need\s+(\d+[a-z]?)\s+"?\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?')
    for i, line in enumerate(lines):
        if mask[i] or i not in sec_of_line:
            continue
        m = need_re.match(line)
        if not m:
            continue
        consumer = sec_of_line[i]
        producers = [a for a in var_assigners.get(m.group(2), set()) if order[a] < order[consumer]]
        if len(producers) == 1:
            add_edge(producers[0], consumer)
    # ② 显式已知语义边（gate_need 用代理变量表达、自动检测看不见的跨段链路）
    KNOWN_SEMANTIC_EDGES = [("107", "110")]
    for p, c in KNOWN_SEMANTIC_EDGES:
        add_edge(p, c)
    return edges, ids, order


def cmd_deps(gate):
    edges, ids, _order = derive_edges(gate)
    for p, c in edges:
        print("%s>%s" % (p, c))
    print("DEPS_EDGES\t%d" % len(edges))


def cmd_parallel_units(gate, root, jobs, work, inner_args):
    """预发射全部单段单元 + 按依赖边有界并发执行 + 逐段 classify，产出 cls_*.txt 供汇总。

    * 每段一个独立 bash 进程（只读仓库树，各写各自 log_/cls_ 文件，互不踩）。
    * 依赖边（§107→§110）用有界 DAG 调度：消费者段等生产者段完成后才起。
    * 调度器返回 0 只表示"全部单元已执行并分类"，段自身的 PASS/FAIL/SKIP/SILENT 由 cls 汇总判定；
      发射失败的段不写 cls ⇒ 汇总记 MISSING（不当绿）。
    """
    import concurrent.futures as _cf
    import subprocess as _sp
    lines = read_lines(gate)
    mask = heredoc_mask(lines)
    sections, _pe, funcs = parse(lines)
    ids = [s[0] for s in sections]
    edges, _ids, _order = derive_edges(gate)
    deps = {sid: [] for sid in ids}
    for p, c in edges:
        if c in deps:
            deps[c].append(p)
    py = sys.executable
    os.makedirs(work, exist_ok=True)
    # 跨进程变量桥接：每条 gate_need 边（producer→consumer）需要把生产者"流动变量"喂给消费段。
    # 流动变量 = 生产者段顶层赋值 ∩ 消费段顶层读取（且消费段自己不赋值、不 for-绑定）。单字符／下划线
    # 前缀／rc（$? 约定）一律排除——它们是循环或瞬时量，跨进程传递只会制造噪声，真实跨段读数（如
    # IP_HITS／IP_SCAN_N）都是多字符语义变量，照常导出。
    assign_re = re.compile(r'(?<![\w=])([A-Za-z_][A-Za-z0-9_]*)=(?!=)')
    read_re = re.compile(r'\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)\b')
    for_re = re.compile(r'\bfor\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\b')
    func_lines = set()
    for _n, s, e, _sid in funcs:
        for i in range(s, e):
            func_lines.add(i)
    sec_of_line = {}
    for sid, start, end, _t in sections:
        for i in range(start, end):
            sec_of_line[i] = sid
    assigned_top = {}
    loopbind = {}
    for i, line in enumerate(lines):
        if mask[i] or i in func_lines or i not in sec_of_line or i < _pe:
            continue
        for m in assign_re.finditer(line):
            assigned_top.setdefault(sec_of_line[i], set()).add(m.group(1))
        for m in for_re.finditer(line):
            loopbind.setdefault(sec_of_line[i], set()).add(m.group(1))
    read_top = {}
    for i, line in enumerate(lines):
        if mask[i] or i in func_lines or i not in sec_of_line or i < _pe:
            continue
        for m in read_re.finditer(line):
            v = m.group(1) or m.group(2)
            read_top.setdefault(sec_of_line[i], set()).add(v)
    exports = {}        # consumer -> [producer]
    producer_vars = {}  # producer -> [流动变量]
    for (p, c) in edges:
        exports.setdefault(c, []).append(p)
        flow = (assigned_top.get(p, set()) & read_top.get(c, set())) \
            - assigned_top.get(c, set()) - loopbind.get(c, set())
        for v in flow:
            if len(v) <= 1 or v.startswith("_") or v == "rc":
                continue
            producer_vars.setdefault(p, []).append(v)
    # 173 段装配共享一份跨段 helper 依赖与 heredoc opener 数组，避免每段重算（见 _assemble docstring）。
    csh = cross_section_helpers(sections, funcs, lines, mask)
    opener_of = heredoc_openers(lines, mask)
    unit_paths = {}
    emit_fail = []
    for sid in ids:
        nxt = next_section_id(sections, sid)
        out = os.path.join(work, "unit_%s.sh" % sid)
        try:
            text, _ = _assemble(lines, mask, sections, funcs, gate, sid, nxt, root,
                               deps=csh, opener_of=opener_of,
                               exports=exports, producer_vars=producer_vars, work=work)
            with open(out, "w", encoding="utf-8") as fh:
                fh.write(text)
            os.chmod(out, 0o755)
            unit_paths[sid] = out
        except SystemExit as e:
            emit_fail.append((sid, str(e)))
            sys.stderr.write("gate-parallel: 发射 §%s 失败：%s\n" % (sid, e))
    if emit_fail:
        print("PARALLEL_EMIT_FAIL\t%d" % len(emit_fail))

    def run_unit(sid):
        out = unit_paths.get(sid)
        if out is None:
            return (sid, 127, "emit-failed")
        log = os.path.join(work, "log_%s.txt" % sid)
        cls = os.path.join(work, "cls_%s.txt" % sid)
        with open(log, "w", encoding="utf-8") as lf:
            rc = _sp.run(["bash", out] + list(inner_args),
                         stdout=lf, stderr=_sp.STDOUT).returncode
        with open(cls, "w", encoding="utf-8") as cf2:
            _sp.run([py, gate, "classify", gate, log, str(rc)], stdout=cf2)
        return (sid, rc, "ok")

    jobs = max(1, int(jobs))
    done = set()
    running = {}
    pending = list(ids)
    with _cf.ThreadPoolExecutor(max_workers=jobs) as ex:
        while pending or running:
            ready = [s for s in pending if all(d in done for d in deps[s])]
            for s in ready:
                if len(running) >= jobs:
                    break
                pending.remove(s)
                running[ex.submit(run_unit, s)] = s
            if not running:
                break
            done_futs, _ = _cf.wait(list(running.keys()), return_when=_cf.FIRST_COMPLETED)
            for f in done_futs:
                s = running.pop(f)
                try:
                    f.result()
                except Exception as e:  # noqa: BLE001 - 调度异常必须现形，不能静默少一段
                    sys.stderr.write("gate-parallel: §%s 调度异常：%s\n" % (s, e))
                done.add(s)
    print("PARALLEL_DONE\t%d\tEMIT_FAIL\t%d" % (len(done), len(emit_fail)))
    return 0


def cmd_selftest(gate):
    """装配器自检：emit-one 产出的全部单段单元必须 bash -n 通过，且跨段 helper 正确捆绑。

    这是 `-collect-parallel` 的可回归前提：一旦 build_inner 把某段装配成语法坏脚本或漏搬 helper，
    读数归属就不可信。bash -n 覆盖「语法坏」；helper 捆绑抽查覆盖「漏搬跨段定义」（§108 消费
    §100 的 gw_code_hits、§111 消费 §110 的 legnote110）。两者都不过＝装配器坏，整轮并行直接判红。
    """
    import subprocess as _sp
    import tempfile as _tf
    lines = read_lines(gate)
    mask = heredoc_mask(lines)
    sections, _pe, funcs = parse(lines)
    ids = [s[0] for s in sections]
    # 173 段装配共享一份跨段 helper 依赖与 heredoc opener 数组，避免每段重算（见 _assemble docstring）。
    csh = cross_section_helpers(sections, funcs, lines, mask)
    opener_of = heredoc_openers(lines, mask)
    root = os.path.dirname(os.path.abspath(gate)) + "/.."
    bad = []          # bash -n 不过（含未闭合 heredoc）的单元
    nomark = []       # 单元里找不到自己段号的 GATE-SECTION-START 标记（heredoc 跨越段头致标记被吞）
    for sid in ids:
        nxt = next_section_id(sections, sid)
        text, _ = _assemble(lines, mask, sections, funcs, gate, sid, nxt, root,
                           deps=csh, opener_of=opener_of)
        fd, path = _tf.mkstemp(suffix=".sh")
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as fh:
                fh.write(text)
            # stdin 重定向 /dev/null：万一仍有未闭合 heredoc，bash -n 会立刻遇 EOF 报错而非卡 stdin 挂死。
            if _sp.run(["bash", "-n", path], stdin=_sp.DEVNULL).returncode != 0:
                bad.append(sid)
        finally:
            os.unlink(path)
        # 标记完整性：单元里必须出现本段自己的 GATE-SECTION-START 标记（说明段头没被 heredoc 当成数据吞掉）。
        if ('GATE-SECTION-START %s' % sid) not in text:
            nomark.append(sid)
    if bad:
        print("SELFTEST\tFAIL bash -n 不过的单元：%s" % ",".join(bad))
        return 1
    if nomark:
        print("SELFTEST\tFAIL 缺本段标记的单元（heredoc 跨越段头致段头被当数据）：%s" % ",".join(nomark))
        return 1
    expect = {
        "108": "gw_code_hits",   # §100 定义、§108 消费
        "111": "legnote110",     # §110 定义、§111 消费
    }
    miss = []
    for sid, helper in expect.items():
        if sid not in ids:
            continue
        nxt = next_section_id(sections, sid)
        text, _ = _assemble(lines, mask, sections, funcs, gate, sid, nxt, root,
                           deps=csh, opener_of=opener_of)
        if ("%s()" % helper) not in text:
            miss.append("%s:%s" % (sid, helper))
    if miss:
        print("SELFTEST\tFAIL 跨段 helper 未捆绑：%s" % ",".join(miss))
        return 1
    print("SELFTEST\tOK sections=%d bash_n_ok=1 helper_bundle_ok=1" % len(ids))
    return 0


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
            sid = m.group(1)
            if sid in ids and sid not in started:
                started.append(sid)
            continue
        sm = SKIP_RE.match(line)
        if sm and sm.group(1) in ids:
            skipped[sm.group(1)] = sm.group(2)
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
        "SKIP": ";".join("%s·%s" % (k, v) for k, v in sorted(skipped.items())),
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
    id_set = set(ids)
    order = {sid: k for k, sid in enumerate(ids)}
    buckets = {"PASS": [], "FAIL": [], "SKIP": [], "SILENT": []}
    skip_reason = {}
    bogus = []
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
            # 这三桶是裸段编号，逗号分隔。
            #   §GATE-SELFACC：认段的尺子从 `isdigit()` 换成「在不在派生段清单里」。按数字认，
            #   `34b` 这种带后缀的编号会被**安静丢掉**——四桶之和凭空少一段，而少了的那段
            #   正是"没人验过"的那段；把「没验过」折算成「验过了」是本枚改造要消灭的形状，
            #   反过来把「验过了」折算成「没验过」同样是假结论（会逼人去改无关的段）。
            #   清单是 parse() 派生的唯一真相，落在清单外的一律进 bogus 点名，不折进任何桶。
            for tok in (kv.get(key, "") or "").split(","):
                t = tok.strip()
                if not t:
                    continue
                if t in id_set:
                    buckets[key].append(t)
                else:
                    bogus.append("%s:%s" % (os.path.basename(path), t))
        # SKIP 桶单独解：classify 输出的是 `编号·原因` 用分号串起来的形态，
        # 拿逗号去切它会得到 "4·fixture：…" 这种非数字串，`isdigit()` 一律为假 ⇒
        # 四桶之和凭空少一段，等值锁以 MISSING 判红（2026-10-07 夹具第一轮实跑就是这个形状）。
        for chunk in (kv.get("SKIP", "") or "").split(";"):
            if "·" in chunk:
                sid, reason = chunk.split("·", 1)
                if sid.strip() in id_set:
                    buckets["SKIP"].append(sid.strip())
                    skip_reason[sid.strip()] = reason
                elif sid.strip():
                    bogus.append("%s:%s" % (os.path.basename(path), sid.strip()))
            elif chunk.strip() in id_set:
                buckets["SKIP"].append(chunk.strip())
            elif chunk.strip():
                bogus.append("%s:%s" % (os.path.basename(path), chunk.strip()))
        if kv.get("EMPTY") == "1":
            empties.append(os.path.basename(path))
    allids = buckets["PASS"] + buckets["FAIL"] + buckets["SKIP"] + buckets["SILENT"]
    dup = sorted({x for x in allids if allids.count(x) > 1}, key=lambda x: order[x])
    missing = sorted(set(ids) - set(allids), key=lambda x: order[x])
    balance = "OK"
    if dup:
        balance = "DUPLICATE:" + ",".join(str(x) for x in dup)
    elif missing:
        balance = "MISSING:" + ",".join(str(x) for x in missing)
    elif bogus:
        # 桶里出现清单外的编号＝分类器与解析器对"什么是一段"的看法分叉了，读数不可归属。
        balance = "BOGUS_ID:" + ",".join(bogus)
    print("PASS_SECTIONS\t%d" % len(buckets["PASS"]))
    print("FAIL_SECTIONS\t%d" % len(buckets["FAIL"]))
    print("SKIP_SECTIONS\t%d" % len(buckets["SKIP"]))
    print("SILENT_SECTIONS\t%d" % len(buckets["SILENT"]))
    print("TOTAL_SECTIONS\t%d" % len(ids))
    print("BALANCE\t%s" % balance)
    # PASS_IDS 也点名（只有四个计数不够用：H3 要断"消费段记在 SKIP、没记在 PASS"，
    # 光看 PASS_SECTIONS 的数字看不出一个段是不是两边都挂着——DUPLICATE 只抓同桶外的重复）。
    print("PASS_IDS\t%s" % ",".join(sorted(set(buckets["PASS"]), key=lambda x: order[x])))
    print("FAIL_IDS\t%s" % ",".join(sorted(set(buckets["FAIL"]), key=lambda x: order[x])))
    print("SKIP_IDS\t%s" % ",".join(sorted(set(buckets["SKIP"]), key=lambda x: order[x])))
    print("SILENT_IDS\t%s" % ",".join(sorted(set(buckets["SILENT"]), key=lambda x: order[x])))
    print("EMPTY_PASSES\t%s" % ",".join(empties))
    for sid in sorted(skip_reason, key=lambda x: order[x]):
        print("SKIP_REASON\t%s·%s" % (sid, skip_reason[sid]))


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
    elif cmd == "emit-one":
        cmd_emit_one(argv[2], argv[3], argv[4], argv[5])
    elif cmd == "deps":
        cmd_deps(argv[2])
    elif cmd == "parallel_units":
        cmd_parallel_units(argv[2], argv[3], argv[4], argv[5], argv[6:])
    elif cmd == "selftest":
        return cmd_selftest(argv[2])
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
