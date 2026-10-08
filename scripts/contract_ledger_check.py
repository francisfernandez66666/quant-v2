#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""前后端契约台账核对器（§CONTRACT-LEDGER，2026-10-09 修复批 波 7 收尾）。

为什么要有这个文件（成因，别再改回去）：
  2026-10-05 全量评价里那句「35 条零调用 / 16 条无台账」是**代理读数**，10-06 逐条复核时抽查两例即偏
  （PUT /api/tenants/{id} 其实有前端调用，GET /api/tenant/usage 才是真零命中），于是两个数字被整体撤回
  「待重算」。重算不能靠人肉数——人肉数出来的数字下一批代码一挪就过期，而且「我数过了」这件事本身
  不可复核。所以这里把两件事变成机器判据：
    Q1 路由全集 R 从 internal/server/server.go 的 HandleFunc 注册串**派生**（不是抄注释里的条数）；
       台账 scripts/contract_ledger.tsv 必须与 R **逐条双向相等**，每条带分类标签。
    Q2 无标签条目 = 0；|R| < 下限即红（派生模式失效时会读出很小的数甚至 0，那比红更危险：
       它把「一行都没解析到」报成「全都没调用」）。
    Q3 幽灵调用 C − R = 0（前端拨了一个服务端没注册的地址＝运行期 404，比缺 UI 更严重）。
  另外两条不属于 Q1–Q3 但同样致命：
    · 标签为「前端调用」的行必须在实时派生的 C 里出现——前端把调用点删了而台账没改，就是台账在撒谎；
    · 非前端行的 evidence 必须能在点名的文件里找到那段文字——「运维 curl」不能只是形容词。

用法：
  python3 scripts/contract_ledger_check.py            # 核对，打印 KEY=VALUE 汇总，异常退出码 1
  python3 scripts/contract_ledger_check.py --list-uncalled   # 只列 R−C 逐条（重算时人工分类用）
  python3 scripts/contract_ledger_check.py --emit-web        # 打印「前端调用」行的台账行（维护用）

退出码：0=全部闭合；1=有任一判据不成立（打印 --- FAIL 行）；2=文件读不到/格式脱节（比 1 更根本的坏）。
"""

import re
import sys
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
SERVER_GO = ROOT / "internal/server/server.go"
API_JS = ROOT / "web/src/api/index.js"
LEDGER = ROOT / "scripts/contract_ledger.tsv"

# ROUTE_FLOOR：|R| 的下限。现值 186（2026-10-09 派生实数），下限留 180 是因为「删几条路由」是合法演进，
# 但「解析器与 Go 侧注册格式脱节」会一下掉到 0——两者之间隔着这个下限。
ROUTE_FLOOR = 180

# 允许的标签集合。「无依据」是**故意留着**的诚实档位：一条路由既没有调用者又说不清为什么存在，
# 就必须写在这里而不是被归类成某个好听的桶；它必须等于 0（等值锁），一旦 >0 就是待决策项。
CLASSES = {
    "前端调用",              # web/src/api/index.js 的 request() 调用点
    "前端直连",              # 不走 request() 封装：fetch()/EventSource 直连（登录、登出、SSE）
    "运维 curl",             # scripts/ / deploy/ 的脚本拨测或人工 curl
    "网关回调",              # qmt_gateway（Python 网关）回调 Go 服务端
    "原生壳",                # Android APK 内嵌壳在登录前就要拨的通道
    "兼容端点",              # fix/别名兼容，保留尺寸、无现役调用者
    "无 UI 待补",            # 后端能力已在，前端还没有入口（必须写清为什么不删）
    "无依据",                # 说不清为什么存在 ⇒ 待决策（台账里必须为 0 条）
}
# 必须有 evidence 落点可核的标签（前端调用/前端直连另有实时派生校验，见 check_ledger）。
EVIDENCE_REQUIRED = CLASSES


def read(path):
    """读文件，读不到直接退出码 2（这不是「判据不成立」，是「判据没跑起来」）。"""
    try:
        return path.read_text(encoding="utf-8")
    except OSError as e:  # pragma: no cover - 路径问题属于环境故障
        print("--- FAIL: 契约台账核对器读不到 %s（%s）" % (path, e))
        sys.exit(2)


# ── Q1 前半：服务端路由全集 R ─────────────────────────────────────────
def derive_routes():
    """从 HandleFunc 注册串派生 (方法, 注册路径原文) 全集，保留 {id} 原样以便与台账逐字相等。"""
    src = read(SERVER_GO)
    src = strip_js_or_go_comments_keep_lines(src)
    found = re.findall(r'HandleFunc\("([A-Z]+) ([^"]+)"', src)
    seen, out = set(), []
    for method, path in found:
        if (method, path) in seen:
            continue          # 同一条注册被重复写两次也只在 R 里算一次，重复由路由自身的一致性管
        seen.add((method, path))
        out.append((method, path))
    return out


# ── Q3 后半：前端调用全集 C ──────────────────────────────────────────
def strip_js_or_go_comments_keep_lines(text):
    """剥 // 行注释与 /* */ 块注释，但**保留行数**（行号要能回指真实文件）。

    为什么要剥：web/src/api/index.js 头部有一整张「端点函数清单」注释表，逐行列着几十个路径。
    不剥的话这张表会被当成调用点，C−R=0 那枚锁当场失去意义（注释里的路径不会在运行期拨出去）。
    字符串里的 //（例如 'https://'）不算注释起点，这里用「前一个字符不是冒号/斜杠」粗判即可，
    因为台账只关心 request('...') 的**路径字面量**，判错的代价是少剥一行注释、不是多算一个调用点。
    """
    text = re.sub(r"/\*.*?\*/", lambda m: "\n" * m.group(0).count("\n"), text, flags=re.S)
    out = []
    for line in text.split("\n"):
        idx = -1
        i = 0
        while i < len(line):
            ch = line[i]
            if ch in "'\"`":                      # 跳过字符串字面量内部
                close = ch
                i += 1
                while i < len(line) and line[i] != close:
                    i += 2 if line[i] == "\\" else 1
                i += 1
                continue
            if ch == "/" and i + 1 < len(line) and line[i + 1] == "/":
                idx = i
                break
            i += 1
        out.append(line if idx < 0 else line[:idx])
    return "\n".join(out)


LITQ = re.compile(r"^'([^']*)'$|^\"([^\"]*)\"$|^`([^`]*)`$")


def lit_of(token):
    """整段是字符串字面量时返回其内容，否则 None。"""
    m = LITQ.match(token)
    if not m:
        return None
    return next(g for g in m.groups() if g is not None)


def split_top(expr):
    """按顶层 + 拆表达式：括号内的 + 不拆（'?x=' + y 属于括号里的三元分支）。"""
    parts, depth, cur = [], 0, ""
    for ch in expr:
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            depth -= 1
        if ch == "+" and depth == 0:
            parts.append(cur)
            cur = ""
        else:
            cur += ch
    parts.append(cur)
    return [x.strip() for x in parts if x.strip()]


def norm(path):
    """把两侧写法归一到同一把尺子：变量段统一成 *、查询串切掉、斜杠折叠、去尾斜杠。"""
    path = path.split("?")[0]
    path = re.sub(r"\$\{[^}]*\}", "*", path)
    path = re.sub(r"\{[^}]*\}", "*", path)
    path = re.sub(r"/+", "/", path)
    return path.rstrip("/") or "/"


def head_of(arg):
    """取调用第一个实参（到顶层逗号为止），opts 里的 data/timeout 不参与路径判定。"""
    depth, i = 0, 0
    while i < len(arg):
        ch = arg[i]
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            depth -= 1
        elif ch == "," and depth == 0:
            return arg[:i]
        i += 1
    return arg


def paths_of(head):
    """一条 request() 调用可能拨到的规范化路径集合。

    形态覆盖（2026-10-09 实跑抽出来的五种，都是本仓真实写法，不是假设）：
      request('/api/x')                                  → /api/x
      request('/api/minute?code=' + ...)                 → /api/minute（? 之前才是路径）
      request('/api/x/' + encodeURIComponent(id) + '/y')  → /api/x/*/y
      request('/api/research/task/' + id + '/log')        → 同上（裸标识符也是路径段）
      request('/api/library/' + id + (on ? '/enable' : '/disable'))
                                                        → 展开成两条（三元拼段）
      request('/api/opslog' + (q.length ? '?' + q.join('&') : ''))
                                                        → /api/opslog（候选串不是 / 开头＝查询串）
    判不准的一律**截断**而不是猜：截断会留下 C−R 或 R−C 的可见缺口，猜会造出台账上的假勾。
    """
    head = re.sub(r"encodeURIComponent\([^()]*\)", "*", head)
    toks = split_top(head)
    res, buf, stopped = [], [], False
    for i, tok in enumerate(toks):
        lit = lit_of(tok)
        if lit is not None:
            if "?" in lit:
                buf.append(lit.split("?")[0])
                break
            buf.append(lit)
            continue
        if tok == "*":
            buf.append("*")
            continue
        if "(" in tok or "?" in tok:                 # 三元/分组：里面的 '/' 开头字面量是候选尾段
            cands = [norm(t) for t in re.findall(r"'([^']*)'", tok) if t and t.startswith("/")]
            if not cands:                            # 顶层直接就是 `cond ? '/a' : '/b'`（无引号前缀）
                cands = [norm(t) for t in re.findall(r"^[\s\w.?!$]*\? '([^']*)' :", tok) if t]
            for c in cands:
                r = norm("".join(buf + [c]))
                if r and r not in res:
                    res.append(r)
            if cands:
                stopped = True
            break
        if re.fullmatch(r"[A-Za-z_$][\w.$]*", tok):
            if i == len(toks) - 1:
                break                                # 尾裸变量（'…' + q / + suffix）当查询串拼接
            buf.append("*")
            continue
        break
    if buf and not stopped:
        r = norm("".join(buf))
        if r and r not in res:
            res.append(r)
    return res


def enclosing_fn(lines, idx):
    """向上找最近的 function 声明名，作为台账 evidence 的可读落点。"""
    for j in range(idx, -1, -1):
        m = re.search(r"function ([A-Za-z0-9_]+)\s*\(", lines[j])
        if m:
            return m.group(1)
    return "?"


def derive_calls():
    """派生前端调用全集 C：返回 (rows, unresolved)。

    rows = [(方法, 规范化路径, 文件行号, 函数名)]；
    unresolved = [(行号, 函数名, 首参片段)]——首参展开后**一个路径字面量都读不出来**的调用
    （典型形态是 `request(url)` 这种路径整段来自变量的写法）。这类不在射程，但必须现形：
    静默跳过＝「前端拨了而台账没数」，正是本核对器要消灭的形态（判据 Q3 会因此漏计幽灵调用）。
    三元形态 `request(cond ? '/api/x?a=1' : '/api/x')` 是**可读的**：paths_of 会展开成候选集，
    不当作 unresolved（10-09 首版在这里用了「首参必须引号开头」的粗判，把 fetchNews 的
    GET /api/news 整条读丢，台账当场撒了一次谎——所以射程判据是「能不能展开出字面路径」，
    不是「长得不长得像字面量」）。
    """
    src = strip_js_or_go_comments_keep_lines(read(API_JS))
    lines = src.split("\n")
    rows, unresolved = [], []
    for m in re.finditer(r"(?<!function )\brequest\(", src):
        start = m.end()
        depth, j = 1, start
        while j < len(src) and depth:
            if src[j] == "(":
                depth += 1
            elif src[j] == ")":
                depth -= 1
            j += 1
        arg = src[start:j - 1]
        mm = re.search(r"method:\s*['\"]([A-Z]+)['\"]", arg)
        method = mm.group(1) if mm else "GET"
        ln = src[:m.start()].count("\n")
        head = head_of(arg)
        paths = paths_of(head)
        if not paths:
            unresolved.append((ln + 1, enclosing_fn(lines, ln), re.sub(r"\s+", " ", head)[:70]))
            continue
        for path in paths:
            rows.append((method, path, ln + 1, enclosing_fn(lines, ln)))
    return rows, unresolved


# ── 台账读取 ─────────────────────────────────────────────────────────
def read_ledger():
    """台账行：method<TAB>path<TAB>class<TAB>evidence<TAB>note（# 开头是说明，不参与判据）。"""
    rows = []
    for raw in read(LEDGER).split("\n"):
        if not raw.strip() or raw.startswith("#"):
            continue
        cols = raw.split("\t")
        if len(cols) < 5:
            print("--- FAIL: 契约台账第 %d 行只有 %d 列（应 5 列：method/path/class/evidence/note）：%s"
                  % (len(rows) + 1, len(cols), raw[:120]))
            sys.exit(2)
        rows.append(tuple(cols[:5]))
    return rows


def evidence_ok(evidence):
    """evidence 形如 相对路径:行号:锚串（或 相对路径:锚串）。给了行号就必须**在那一行附近**命中锚串。

    只验「文件存在」不够——「运维 curl」写个不存在的行号也算过，那和没写一样；
    只验「锚串在全文件某处」也不够——调用点被删掉后注释里往往还留着同样的路径，台账会假勾。
    行号容差 ±2 行：给编辑留出手抖的空间，超出就说明点名的那一腿已经不是这件事了。
    """
    parts = evidence.split(":", 2)
    if len(parts) < 2:
        return False, "evidence 不是 file:… 形态"
    rel = parts[0]
    needle = parts[2] if len(parts) == 3 else ""
    lineno = parts[1] if len(parts) == 3 and parts[1].isdigit() else ""
    path = ROOT / rel
    if not path.exists():
        return False, "文件不存在"
    text = path.read_text(encoding="utf-8", errors="replace")
    if lineno:
        lines = text.split("\n")
        lo = max(0, int(lineno) - 3)
        hi = min(len(lines), int(lineno) + 2)
        window = "\n".join(lines[lo:hi])
        if needle and needle not in window:
            return False, "锚串不在点名的第 %s 行附近（±2 行窗口内找不到）" % lineno
        if not needle:
            return False, "给了行号却没给锚串（行号会随文件漂移，锚串才是判据）"
        return True, ""
    if not needle:
        return False, "evidence 既没有行号也没有锚串"
    if needle not in text:
        return False, "锚串在该文件里找不到"
    return True, ""


def check():
    routes = derive_routes()
    calls, unresolved = derive_calls()
    ledger = read_ledger()
    rset = {(m, norm(p)) for m, p in routes}
    rraw = set(routes)
    cset = {(m, p) for m, p, _, _ in calls}
    fails = []

    def lock(cond, msg):
        if not cond:
            fails.append(msg)

    # Q2 前半：派生本身可信吗
    lock(len(routes) >= ROUTE_FLOOR,
         "派生路由数 %d < 下限 %d（HandleFunc 注册格式与解析模式脱节时最典型的表现是读出很小的数，"
         "那比红更危险：它把「一行都没解析到」报成「全都没调用」）" % (len(routes), ROUTE_FLOOR))
    lock(len(calls) >= 100, "派生前端调用点 %d < 100（api/index.js 的 request() 形态变了就会静默读空）" % len(calls))
    # 射程账：路径展开不出来的 request() 调用。不锁死的话，Q1/Q3 两根判据对它一律失明——
    # 「台账没这条」和「前端根本没拨」在这里读起来一模一样，而只有后者可以归类。
    lock(not unresolved,
         "有 %d 条 request() 调用展开不出路径（整段来自变量），派生射程看不见它，Q1/Q3 都判不到："
         "%s ⇒ 改写成字面量/三元字面量，或按「前端直连」在台账点名并给真实 fetch 锚串"
         % (len(unresolved), "; ".join("%s:%d %s()" % (API_JS.name, ln, fn) for ln, fn, _ in unresolved[:6])))

    # Q1：台账与 R 逐条双向相等
    lset = {(m, p) for m, p, _, _, _ in ledger}
    missing = sorted(rraw - lset)
    extra = sorted(lset - rraw)
    lock(not missing, "台账漏了 %d 条路由（新增/改名的注册没有分类账）：%s"
         % (len(missing), "; ".join("%s %s" % x for x in missing[:8])))
    lock(not extra, "台账里有 %d 行对不上现网注册（路由已删/改名而台账没跟着改）：%s"
         % (len(extra), "; ".join("%s %s" % x for x in extra[:8])))
    dup = len(ledger) - len(lset)
    lock(dup == 0, "台账有 %d 行重复（同一 method+path 出现多次＝分类会被后写的覆盖）" % dup)

    # Q2 后半：标签齐全 + 无依据清零
    bad_class = sorted({c for _, _, c, _, _ in ledger if c not in CLASSES})
    lock(not bad_class, "台账出现未登记的分类标签：%s" % ", ".join(bad_class))
    untagged = sum(1 for _, _, c, e, n in ledger if not c or not e or not n)
    lock(untagged == 0, "有 %d 行分类/依据/说明三者缺一（空标签＝「没看过」被算成「看过了」）" % untagged)
    no_basis = sum(1 for _, _, c, _, _ in ledger if c == "无依据")
    lock(no_basis == 0, "有 %d 条路由填了「无依据」＝说不清为什么还留着，必须逐条决策后才能入台账" % no_basis)

    # 「无 UI 待补 / 兼容端点」这两档是给"保留尺寸"用的，必须点名为什么不删
    thin = sorted("%s %s" % (m, p) for m, p, c, _, n in ledger if c in ("无 UI 待补", "兼容端点") and len(n) < 12)
    lock(not thin, "有 %d 行保留类标签的说明太短（少于 12 字＝等于没写理由）：%s" % (len(thin), "; ".join(thin[:5])))

    # evidence 可核
    broken = []
    for m, p, c, e, _ in ledger:
        if c in EVIDENCE_REQUIRED:
            ok, why = evidence_ok(e)
            if not ok:
                broken.append("%s %s（%s）" % (m, p, why))
    lock(not broken, "有 %d 行的 evidence 落点核不过：%s" % (len(broken), "; ".join(broken[:5])))

    # 前端调用行必须被实时派生证实（台账撒谎检测）
    web_rows = {(m, norm(p)) for m, p, c, _, _ in ledger if c in ("前端调用", "前端直连")}
    direct_ok = set()
    for m, p, c, e, _ in ledger:
        if c == "前端直连":
            # 直连腿（fetch/EventSource）不在 request() 派生里，靠 evidence 锚串在 web/src 真实出现来证
            parts = e.split(":", 2)
            rel = parts[0]
            needle = parts[2] if len(parts) == 3 else ""
            fp = ROOT / rel
            if fp.exists() and needle and needle in fp.read_text(encoding="utf-8", errors="replace"):
                direct_ok.add((m, norm(p)))
    lied = sorted(web_rows - cset - direct_ok)
    lock(not lied, "台账把这 %d 条记成「前端调用/前端直连」，但实时派生里找不到调用点（前端已删而台账没改＝假勾）：%s"
         % (len(lied), "; ".join("%s %s" % x for x in lied[:8])))
    ghostweb = sorted(cset - web_rows)
    lock(not ghostweb, "实时派生里有 %d 条前端调用在台账上没标成前端（台账与代码分家）：%s"
         % (len(ghostweb), "; ".join("%s %s" % x for x in ghostweb[:8])))

    # Q3：幽灵调用 C − R = 0
    ghost = sorted(cset - rset)
    lock(not ghost, "有 %d 条前端调用拨了服务端没注册的地址（运行期 404）：%s"
         % (len(ghost), "; ".join("%s %s" % x for x in ghost[:8])))

    # 汇总读数（门禁按这些键做等值锁，禁止自己再数一遍）
    counts = {}
    for _, _, c, _, _ in ledger:
        counts[c] = counts.get(c, 0) + 1
    print("ROUTES_DERIVED=%d" % len(routes))
    print("CALLSITES_DERIVED=%d" % len(calls))
    print("CALLPATH_DERIVED=%d" % len(cset))
    print("OUT_OF_RANGE=%d" % len(unresolved))
    print("LEDGER_ROWS=%d" % len(ledger))
    print("COVERED_BY_WEB=%d" % len(cset & web_rows))
    print("UNCALLED=%d" % len(rset - cset))
    print("GHOST_CALLS=%d" % len(ghost))
    print("UNTAGGED=%d" % untagged)
    print("NO_BASIS=%d" % no_basis)
    print("BROKEN_EVIDENCE=%d" % len(broken))
    print("LEDGER_LIES=%d" % len(lied))
    for c in sorted(counts):
        print("CLASS_%s=%d" % (c, counts[c]))
    if fails:
        for f in fails:
            print("--- FAIL: 契约台账 " + f)
        print("LEDGER_CHECK=RED")
        return 1
    print("LEDGER_CHECK=GREEN")
    return 0


def main():
    if "--list-uncalled" in sys.argv:
        cset = {(m, p) for m, p, _, _ in derive_calls()[0]}
        for method, path in sorted(derive_routes()):
            if (method, norm(path)) not in cset:
                print("%s\t%s" % (method, path))
        return 0
    if "--emit-web" in sys.argv:
        routes = derive_routes()
        raw_by_key = {}
        for method, path in routes:
            raw_by_key.setdefault((method, norm(path)), path)
        best = {}
        for method, path, ln, fn in derive_calls()[0]:
            key = (method, path)
            if key not in best or ln < best[key][0]:
                best[key] = (ln, fn)
        # 台账里写的必须是**注册原文**（带 {id}），否则 Q1 的双向相等对不上；锚串取路径里第一个
        # 变量段之前的字面量：它一定真实出现在调用行上（'/api/x/' 或整串）。
        for (method, path), (ln, fn) in sorted(best.items(), key=lambda x: (x[0][1], x[0][0])):
            raw = raw_by_key.get((method, path))
            if raw is None:
                print("# GHOST %s %s（服务端无此注册，不进台账）" % (method, path), file=sys.stderr)
                continue
            anchor = path.split("*")[0] or path
            print("%s\t%s\t前端调用\tweb/src/api/index.js:%d:%s\t调用点 %s()（本行由核对器派生，勿手改）"
                  % (method, raw, ln, anchor, fn))
        return 0
    return check()


if __name__ == "__main__":
    sys.exit(main())
