# -*- coding: utf-8 -*-
"""broker_reads.py — §B1 柜台读数通道（11-05 之后柜台读数的**导出解析腿**，全程只读）。

为什么存在（IMPL_QMT_INTERNAL_LOOP_20261103 §4.1）：
  券商 2026-11-05 起关闭 QMT 内置策略的本地文件读写与一切网络连接，于是
  持仓/资金/委托的自动上行（`bridge_report.jsonl`）结构性断供。断供之后柜台读数只剩两个来源：
  ① QMT/掌证宝客户端**自己导出**的对账文件，② 人工回执（那是 Go 侧另一批的活，不在本文件）。
  本模块只负责 ①：发现文件 → 解码 → 归一化列名 → 给出可审计的读数，**不写库、不下单、不推账本**。

四条硬边界（§4.1 逐条，用例在 tests/test_broker_reads.py）：
  1. **只读**：解析器对导出目录零写/零删。导出是券商产物、是证据链（§F18 同取向），
     任何"顺手清个临时文件"的动作都会毁掉唯一的手工对账基线。
  2. **缺文件 ≠ 空仓**：`missing`（没取到）与 `empty`（取到了但零行）是两种读数，
     绝不折成一个布尔——这正是 §MANUAL-SYNC 三条安全边界里"空快照不做消失判定"的抄法
     （见 gateway.py `_apply_orders_snapshot` 的注释）。
  3. **半文件保护**：导出进行到一半被读走是常态。行数骤降或表头缺失 ⇒ 判 `parse_fail`、
     **保留上一份读数**、`parse_fail_total` 计数，绝不把半份账当成新事实提交。
  4. **编码三兜底**：utf-8-sig / utf-8 / gbk 依次尝试，姿势与桥侧 `_read_cfg` 完全一致
     （qmt_bridge_strategy.py:1221），不另发明一套——券商客户端与 Excel 混存，编码不固定。

格式边界（现实约束）：只认 csv / txt / tsv / **文本内容的** .xls。真正的二进制表格
（.xlsx、OLE2 复合文档 .xls）一律抛 `UnsupportedFormatError` 并标 `unsupported_binary_format`
——解析二进制要引第三方库（11-05 后连依赖都装不上），而且会把"读不出来"伪装成"读出来是空的"。

目录白名单（§173 ③ 的静态锁对象）：`discover_exports` 只认显式登记在白名单根目录之下的路径。
白名单之外的目录一律不发现、不解析——防止将来出现"读遍盘"式越权与信息泄漏。
路径判断用 realpath + 分隔符边界，`/x/exports` 与 `/x/exports_evil` 这种**同名前缀的兄弟目录**
不许被前缀匹配蒙过去。

English: read-only parser for the counter statements the QMT/掌证宝 client exports, which after
2026-11-05 is the only machine-readable source of positions/orders/fills/asset. Missing files and
empty files stay two distinct readings; half-written exports are rejected while the previous
trusted reading is kept; encodings fall back utf-8-sig → utf-8 → gbk exactly like the bridge's
`_read_cfg`. Discovery is confined to a whitelist of export roots.
"""
import csv
import io
import os
import re
import time

# ── 读数状态：四个词各自独立，谁也不许折成谁（§4.1 边界 2/3）────────────────
STATE_MISSING = "missing"          # 文件不存在/不可见：没取到，不是"柜台说没有"
STATE_EMPTY = "empty"              # 取到了但没有数据行
STATE_OK = "ok"                    # 取到了且有可信读数
STATE_PARSE_FAIL = "parse_fail"    # 半文件/表头缺失/编码全败：保留上一份读数并计数
STATE_UNSUPPORTED = "unsupported"  # 二进制格式：结构性读不了（不是今天的意外）
STATE_STALE = "stale"              # 上一次可信读数已过新鲜度窗口
STATE_NEVER = "never"              # 从未成功提交过任何读数

# ── 白名单：缺省为空＝什么都不许读；生产由调用方（网关装配处）显式登记导出根目录 ──
# 为什么缺省为空而不是内置 C:\ 路径：本机开发态/CI 没有券商客户端，内置真实路径会让
# "忘了配白名单"表现成"读到别人机器上的文件"，而空白的表现是发现零个文件——后者才是 fail-closed。
EXPORT_ROOT_WHITELIST = (
    # 现网实测后由运维登记，形如 r"C:\券商导出\持仓"。这里刻意不预填。
)

# 文本类扩展名：csv/txt/tsv 直接认；.xls 要过魔数嗅探（历史上有把 CSV 改名成 .xls 的导出）。
TEXT_EXTS = (".csv", ".txt", ".tsv", ".xls")
# 明确不支持的二进制格式（抛错而不是静默跳过——静默跳过会伪装成"今天没有导出"）。
BINARY_EXTS = (".xlsx", ".xlsm", ".et", ".wk1", ".qbw")
# Excel 锁文件与临时分片：导出目录里常见，永远不作为候选（它们天然是半文件）。
SKIP_NAME_PAT = re.compile(r"^~\$|\.tmp$|\.part$|^\.#", re.IGNORECASE)

# 每种读数的文件名形态（词干命中即归类；大小写不敏感）。
# 中英两套词干都要有：QMT 简体中文客户端出中文名，掌证宝/导出的英文模板出英文名。
KIND_STEMS = {
    "positions": ("持仓", "股份持仓", "positions", "stockposition", "position"),
    "orders": ("委托", "当日委托", "orders", "order", "entrust"),
    "fills": ("成交", "当日成交", "fills", "trade", "transaction", "deal"),
    "asset": ("资产", "资金", "账户资产", "asset", "account", "fund"),
}

# 列名归一化：券商导出的表头是中文的，且同一字段在不同客户端里叫法不同。
# 规范化 = 去空白（含全角空格）+ 转小写 + 去掉括号里的单位后缀。
HEADER_ALIASES = {
    "symbol": ("代码", "证券代码", "股票代码", "标的代码", "证券标识", "symbol", "code", "instrument"),
    "name": ("名称", "证券名称", "股票名称", "标的名称", "name"),
    # 2026-11-10: the QMT client's own PositionStatics export calls the held
    # qty "当前拥股" -- without this alias every positions file failed
    # header_missing:qty (PLAN_CARRIER_RISK_REBUILD 1.3 item 12).
    "qty": ("数量", "持仓数量", "股票数量", "当前拥股", "拥股数量", "拥股",
            "委托数量", "成交数量", "累计成交数量", "qty", "volume", "amount"),
    "usable_qty": ("可用数量", "可用余额", "可用拥股", "平仓数量", "usable_qty", "available"),
    "price": ("价格", "委托价格", "成交价", "成交均价", "最新价", "price"),
    # 2026-11-13: the QMT position export carries cost / period-high / first-buy
    # columns, but none of them were mapped here, so the parser dropped them and
    # the uplink could only ever send qty. The cloud then stored cost_price=0
    # and showed an empty first-buy anchor (owner report 11-13).
    # These are OPTIONAL columns: REQUIRED_COLUMNS stays (symbol, qty) so older
    # export files without them are still parsed instead of rejected.
    "cost_price": ("成本价", "持仓成本", "成本价格", "买入成本", "开仓价",
                   "cost_price", "cost", "open_price"),
    "highest_price": ("最高价", "期间最高价", "持仓最高价", "highest_price"),
    "anchor_price": ("首买价", "首买锚点", "锚点价", "anchor_price"),
    "order_id": ("委托号", "委托编号", "合同编号", "订单编号", "order_id", "orderid", "order"),
    "fill_id": ("成交编号", "成交序号", "fill_id", "trade_id", "deal_id"),
    # §TRADE-LEDGER（2026-11-13）：委托备注是载体成交的唯一战法归因源（<版本>-<信号短码>）。
    # 可选列：旧导出没有该列时照常解析（REQUIRED_COLUMNS 不变）。
    "remark": ("投资备注", "委托备注", "备注", "remark", "comment"),
    # 成交金额列（qty 的英文别名表里已占用 "amount"，这里只登记中文列名，
    # 避免英文导出 "amount" 表头在 qty/amount 两个键间产生二义）。
    "amount": ("成交金额", "发生金额", "成交额"),
    "status": ("委托状态", "状态", "订单状态", "status", "state"),
    "side": ("买卖标志", "操作", "业务名称", "交易方向", "委托方向", "买卖", "side", "direction"),
    "time": ("时间", "委托时间", "成交时间", "发生时间", "datetime", "time"),
    # 2026-11-10: the funding-account column is how the A-share main account is
    # told apart from the HGT/SGT (HK Connect) shell accounts that sit beside it
    # in D:\qmt and would otherwise win the mtime race with header-only rows.
    "fund_account": ("资金账号", "账号", "account_no", "fund_account"),
    "total_assets": ("总资产", "资产总值", "账户总资产", "总资产（人民币）", "净资产",
                     "total_assets", "totalasset"),
    "usable_cash": ("可用资金", "可用金额", "资金可用", "可取金额", "可用余额", "usable_cash", "available_cash"),
    # 2026-11-10 FIX: _norm_header used to strip the parenthetical currency, so
    # 总市值（港币） and 总市值（人民币） both collapsed to 总市值 and the
    # second-pass contains-match on 市值 grabbed the FIRST one -- the HKD column
    # (value 0) -- and frozen_cash = total − cash − mv booked the whole
    # portfolio as frozen. Currencies are no longer stripped as units; the CNY
    # spellings are listed here so the RMB column wins the exact-match pass and
    # the HKD column stays unmapped (its rows are dropped by the fund-account
    # filter anyway).
    "market_value": ("证券市值", "持仓市值", "市值", "总市值（人民币）", "股票总市值",
                     "market_value"),
}

# Units that DO get stripped by _norm_header (quantity/count units). Currency
# suffixes are deliberately NOT in this set: they distinguish columns that
# would otherwise share one canonical name (the HKD/CNY market-value bug).
HEADER_UNITS = ("股", "元", "份", "手", "张", "%", "笔")

# 每种读数**必须**齐备的规范列：缺任意一列即判表头缺失（半文件/不是这类文件的常见形态）。
REQUIRED_COLUMNS = {
    "positions": ("symbol", "qty"),
    "orders": ("symbol", "order_id", "status"),
    "fills": ("symbol", "price", "qty"),
    "asset": ("total_assets",),
}

# 半文件判定的两个数（§4.1 边界 3）：
#   上一份至少有 MIN_ROWS_FOR_COLLAPSE_CHECK 行才做骤降判定——1 行涨到 2 行不是骤降；
#   新读数不足上一份的 COLLAPSE_RATIO 即判解析失败。
COLLAPSE_RATIO = 0.5
MIN_ROWS_FOR_COLLAPSE_CHECK = 4

# 空值占位：券商导出里 "----" / "--" 表示"无"，按空串处理而不是当成数字 0。
PLACEHOLDER_VALUES = ("----", "---", "--", "-", "null", "none", "nan")

DEFAULT_STALE_SEC = 3600  # 与 §3.1 的 READ_STALE_SEC 建议值同源（外脑模式态 2 用的就是它）


def _norm_header(cell):
    """表头单元格规范化：去空白（含全角）＋转小写＋剥掉**数量单位**后缀。

    2026-11-10 FIX（PLAN_CARRIER_RISK_REBUILD 1.3 第 12 项）：这里曾经无差别剥掉
    任何括号后缀，`总市值（港币）` 与 `总市值（人民币）` 归一化后都变成 `总市值`，
    第二趟包含匹配让港币列（值 0）抢走 market_value，下游
    frozen_cash = 总资产 − 可用 − 市值 把全部市值算成冻结资金。货币后缀是
    **列名的一部分**，不在 HEADER_UNITS 里，不再剥掉。
    """
    s = str(cell or "").strip().replace("\u3000", "").replace(" ", "")
    s = s.lower()
    # 「成交数量(股)」「持仓数量（手）」这类数量单位对本模块无意义 ⇒ 剥掉；
    # 货币后缀（人民币/港币/美元）保留——它们区分的是不同的列。
    s = re.sub(r"[（(](%s)?[)）]$" % "|".join(HEADER_UNITS), "", s)
    return s


def _canon_columns(header):
    """表头 → {列下标: 规范名}。两趟：先精确相等，再宽松包含，且一列只认一个规范名。

    为什么两趟：`委托数量` 与 `数量` 是包含关系，单趟包含匹配会让先遍历到的短别名抢走列，
    于是同一份导出在不同列序下归一化成不同结果——那是最坏的一种"看起来能解析"。
    """
    mapping = {}
    used = set()
    norm = [_norm_header(h) for h in header]
    # 第一趟：精确命中（绝大多数规范表头在这一趟就完成）
    for canonical, aliases in HEADER_ALIASES.items():
        for idx, cell in enumerate(norm):
            if idx in mapping:
                continue
            if cell and cell in {_norm_header(a) for a in aliases}:
                mapping[idx] = canonical
                used.add(canonical)
                break
    # 第二趟：只对仍未命中的规范名做包含匹配（长别名优先，减少抢占）
    for canonical, aliases in HEADER_ALIASES.items():
        if canonical in used:
            continue
        for alias in sorted(aliases, key=len, reverse=True):
            na = _norm_header(alias)
            if not na:
                continue
            hit = next((i for i, c in enumerate(norm) if i not in mapping and na in c), None)
            if hit is not None:
                mapping[hit] = canonical
                used.add(canonical)
                break
    return mapping


def _coerce(value):
    """单元格取值：去掉千分位与百分号；纯数字转 int/float，占位符转空串，其余原样保留。"""
    s = str(value if value is not None else "").strip()
    if s.lower() in PLACEHOLDER_VALUES:
        return ""
    s2 = s.replace(",", "").replace("%", "")
    if re.fullmatch(r"-?\d+", s2):
        return int(s2)
    if re.fullmatch(r"-?\d+\.\d+", s2):
        return float(s2)
    return s


def _decode_bytes(raw):
    """编码三兜底：utf-8-sig → utf-8 → gbk（同 `_read_cfg` 姿势，逐个试、失败就换下一个）。"""
    for enc in ("utf-8-sig", "utf-8", "gbk"):
        try:
            return raw.decode(enc), enc
        except Exception:  # noqa: BLE001 — 解码失败是兜底链的正常一环，不是异常路径
            continue
    return None, ""


def _sniff_delimiter(sample):
    """分隔符嗅探：逗号 / 制表 / 分号，按首行出现次数最多的那个（导出模板不统一）。"""
    first = sample.split("\n", 1)[0]
    counts = {d: first.count(d) for d in (",", "\t", ";")}
    best = max(counts, key=lambda k: counts[k])
    return best if counts[best] > 0 else ","


class UnsupportedFormatError(ValueError):
    """二进制/未知格式：明确标注"读不了"，绝不折成"读出来是空的"（§4.1 边界 2 的另一半）。"""


class ExportFile:
    """一个通过白名单与文件名形态校验的候选导出文件。"""

    def __init__(self, path, kind, size=0, mtime=0.0):
        self.path = path
        self.kind = kind
        self.file_name = os.path.basename(path)
        self.size = size
        self.mtime = mtime

    def __repr__(self):
        return "ExportFile(kind=%s, file_name=%s)" % (self.kind, self.file_name)


class ReadResult:
    """一次解析的完整读数：状态 + 行 + 留痕字段（调用方永远先看 state，不看布尔）。"""

    def __init__(self, kind, state, rows=None, file_name="", reason="",
                 encoding="", asof=0.0):
        self.kind = kind
        self.state = state
        self.rows = list(rows or [])
        self.file_name = file_name
        self.reason = reason
        self.encoding = encoding
        self.asof = asof

    @property
    def row_count(self):
        # 行数只描述"这次解析拿到多少行"，不承载可信性——可信性在 state 里。
        return len(self.rows)

    def __repr__(self):
        return "ReadResult(kind=%s, state=%s, rows=%d, reason=%s)" % (
            self.kind, self.state, self.row_count, self.reason)


def is_under(path, root):
    """realpath 后的目录归属判断：必须是同一路径或**带分隔符边界**的前缀。

    负例（现网真实形状）：root=`.../exports` 时 `.../exports_evil` 不许算在内——
    纯字符串 startswith 会把它放进白名单，那是越权读别人目录的入口。
    """
    try:
        p = os.path.realpath(path)
        r = os.path.realpath(root)
    except Exception:  # noqa: BLE001 — 路径解析不了就当不在白名单内（fail-closed）
        return False
    if p == r:
        return True
    # 前缀必须带分隔符边界：`exports` 不许把 `exports_evil` 算进白名单
    return p.startswith(r.rstrip(os.sep) + os.sep)


def in_whitelist(path, allowed_roots=None):
    """白名单判定（发现侧唯一的准入函数，静态锁 §173 ③ 盯的就是它）。"""
    roots = tuple(allowed_roots if allowed_roots is not None else EXPORT_ROOT_WHITELIST)
    if not roots:
        return False
    return any(is_under(path, root) for root in roots)


def classify_name(file_name):
    """文件名形态 → 读数类别；不匹配任何形态则返回空串（不猜、不兜底）。"""
    base = os.path.basename(str(file_name or ""))
    stem = os.path.splitext(base)[0].lower()
    for kind, stems in KIND_STEMS.items():
        for token in stems:
            if token.lower() in stem:
                return kind
    return ""


def _iter_globbed(dir_glob):
    """把 dir_glob（字符串或字符串序列）展开成候选绝对路径。"""
    import glob as _glob
    patterns = [dir_glob] if isinstance(dir_glob, (str, os.PathLike)) else list(dir_glob or [])
    for pat in patterns:
        for hit in _glob.glob(str(pat)):
            yield hit


def discover_exports(dir_glob, allowed_roots=None):
    """发现导出文件：白名单目录 ∩ 文件名形态 ∩ 文本扩展名 ∩ 普通文件。

    四类一律不返回：白名单之外的路径、形态不匹配的文件名（例如 `新建文本文档.txt`）、
    Excel 锁文件/`.tmp` 分片（它们必然半份），以及目录本身。
    返回按 mtime 倒序（最新导出在前），调用方据此挑"今天那一份"。
    """
    found = []
    for hit in _iter_globbed(dir_glob):
        try:
            full = os.path.realpath(hit)
        except Exception:  # noqa: BLE001
            continue
        if not in_whitelist(full, allowed_roots):
            continue  # 白名单外的目录连"叫什么名字"都不许被本模块知道
        base = os.path.basename(full)
        if SKIP_NAME_PAT.search(base):
            continue
        if not os.path.isfile(full):
            continue
        ext = os.path.splitext(base)[1].lower()
        if ext in BINARY_EXTS:
            continue  # 二进制不在发现阶段报错：它只是"不是可读导出"，抛错留给显式解析
        if ext not in TEXT_EXTS:
            continue
        kind = classify_name(base)
        if not kind:
            continue
        try:
            st = os.stat(full)
        except OSError:  # noqa: BLE001 — 刚消失的临时文件：跳过，不编造读数
            continue
        found.append(ExportFile(full, kind, size=st.st_size, mtime=st.st_mtime))
    found.sort(key=lambda e: e.mtime, reverse=True)
    return found


def discover_exports_recursive(roots, allowed_roots=None):
    """白名单根目录下的**递归**发现（2026-11-10，PLAN 1.3 第 12 项）。

    11-10 实锤：QMT 客户端把导出落在 `D:\\qmt` 的**子目录**里（HGT / SGT /
    Stock，其中 Stock 才是 A 股主账户），而旧的发现姿势只扫 `root/*` 一层，
    于是所有类别都 `file_not_found`、脑侧全天 0 事件。本函数 os.walk 每个根，
    把**文件**路径直接喂给 discover_exports（白名单/形态/扩展名/锁文件四道闸
    全部复用，不另起第二套准入），结果同样按 mtime 倒序。
    同名前缀在多个子目录并存（HGT/SGT/Stock 都有 Account-*.csv）时**不做**
    目录取舍——那由调用方按「资金账号」列过滤（report_uplink 侧），发现层
    只负责"把真实存在的文件都交出来"。
    """
    paths = []
    for root in tuple(roots or []):
        root = str(root)
        if not os.path.isdir(root):
            continue
        for dirpath, dirnames, filenames in os.walk(root):
            dirnames[:] = [d for d in dirnames if not d.startswith(".")]
            for name in filenames:
                paths.append(os.path.join(dirpath, name))
    return discover_exports(paths, allowed_roots)


def _require_text_format(path):
    """扩展名 + 魔数两级格式闸：只有确认是文本才继续读，否则抛 UnsupportedFormatError。"""
    ext = os.path.splitext(str(path))[1].lower()
    if ext in BINARY_EXTS:
        raise UnsupportedFormatError("unsupported_binary_format:%s" % ext)
    if ext not in TEXT_EXTS:
        raise UnsupportedFormatError("unsupported_format:%s" % (ext or "<none>"))
    with open(path, "rb") as fh:
        head = fh.read(8)
    # OLE2（真 .xls）与 zip（.xlsx 改名）都是二进制复合文档，文本嗅探救不回来
    if head.startswith(b"\xd0\xcf\x11\xe0") or head.startswith(b"PK\x03\x04"):
        raise UnsupportedFormatError("unsupported_binary_magic:%s" % ext)
    return head


def _parse(path, kind, prev_row_count=None):
    """唯一的解析实现：状态机在文档字符串里，四个分支互不折叠。"""
    file_name = os.path.basename(str(path))
    if not os.path.exists(path):
        # 边界 2 的第一半：文件不存在 ⇒ missing（"没取到"），绝不能表现成 0 行/空仓
        return ReadResult(kind, STATE_MISSING, file_name=file_name, reason="file_not_found")
    if not os.path.isfile(path):
        return ReadResult(kind, STATE_PARSE_FAIL, file_name=file_name, reason="not_a_regular_file")
    # 二进制格式在这里向上抛 UnsupportedFormatError（调用方显式处理；ledger 侧收成 unsupported 态）：
    # "读不了"必须是一条独立读数，不许伪装成"读出来是空的"（边界 2 的另一半）。
    _require_text_format(path)
    try:
        with open(path, "rb") as fh:  # 只读打开（边界 1：全模块没有第二个 open 模式）
            raw = fh.read()
    except OSError as exc:
        return ReadResult(kind, STATE_PARSE_FAIL, file_name=file_name, reason="read_error:%s" % exc)

    asof = os.path.getmtime(path)
    text, encoding = _decode_bytes(raw)
    if text is None:
        return ReadResult(kind, STATE_PARSE_FAIL, file_name=file_name, reason="encoding_undecodable")
    if not text.strip():
        # 边界 2 的第二半：文件在、字节为空 ⇒ empty（取到了，但柜台那边零行），与 missing 不同态
        return ReadResult(kind, STATE_EMPTY, file_name=file_name,
                          reason="blank_file", encoding=encoding, asof=asof)

    rows_raw = [r for r in csv.reader(io.StringIO(text),
                                      delimiter=_sniff_delimiter(text)) if any(
        str(c).strip() for c in r)]
    if not rows_raw:
        return ReadResult(kind, STATE_EMPTY, file_name=file_name,
                          reason="no_rows", encoding=encoding, asof=asof)
    header, data_rows = rows_raw[0], rows_raw[1:]
    mapping = _canon_columns(header)
    missing_cols = [c for c in REQUIRED_COLUMNS.get(kind, ())
                    if c not in set(mapping.values())]
    if missing_cols:
        # 边界 3：表头缺必需列＝读到的不是这份账/被截断，判失败并保留上一份
        return ReadResult(kind, STATE_PARSE_FAIL, file_name=file_name,
                          reason="header_missing:%s" % ",".join(missing_cols),
                          encoding=encoding, asof=asof)

    rows = []
    for raw_row in data_rows:
        rec = {}
        for idx, cell in enumerate(raw_row):
            key = mapping.get(idx)
            if key is None:
                continue  # 未识别的列原样丢弃：读数只承诺规范字段，不承诺全字段透传
            rec[key] = _coerce(cell)
        if not any(str(v).strip() for v in rec.values()):
            continue  # 全空行（导出常见的分隔行/收尾空行）
        rows.append(rec)

    if not rows:
        return ReadResult(kind, STATE_EMPTY, file_name=file_name,
                          reason="no_data_rows", encoding=encoding, asof=asof)

    if prev_row_count is not None and prev_row_count >= MIN_ROWS_FOR_COLLAPSE_CHECK:
        if len(rows) < prev_row_count * COLLAPSE_RATIO:
            # 边界 3：行数骤降＝大概率读到半文件；失败、保留上一份、由 ledger 计数
            return ReadResult(kind, STATE_PARSE_FAIL, file_name=file_name,
                              reason="row_collapse:%d->%d" % (prev_row_count, len(rows)),
                              encoding=encoding, asof=asof)

    return ReadResult(kind, STATE_OK, rows=rows, file_name=file_name,
                      reason="", encoding=encoding, asof=asof)


def parse_positions(path, prev_row_count=None):
    """持仓导出 → ReadResult（state 必判，别把 rows 空当成"没仓位"）。"""
    return _parse(path, "positions", prev_row_count)


def parse_orders(path, prev_row_count=None):
    """当日委托导出 → ReadResult。"""
    return _parse(path, "orders", prev_row_count)


def parse_fills(path, prev_row_count=None):
    """当日成交导出 → ReadResult。"""
    return _parse(path, "fills", prev_row_count)


def parse_asset(path, prev_row_count=None):
    """账户资产导出 → ReadResult（总资产红线在这里：只报读数，不改资金）。"""
    return _parse(path, "asset", prev_row_count)


PARSERS = {
    "positions": parse_positions,
    "orders": parse_orders,
    "fills": parse_fills,
    "asset": parse_asset,
}


class ReadLedger:
    """一类读数的最近可信账本：只记"最后一次可信读数"，失败一律保留旧值（边界 3）。

    字段形状按 §4.1：`last_ok_at` / `source` / `file_name` / `row_count` / `parse_fail_total`，
    另带三个只为把"空"与"缺"区分清楚而存在的观察位（`empty_total`/`last_empty_at`/`missing_total`）：
    它们只进读数、不推动任何资金/持仓状态——和 §MANUAL-SYNC 的三条安全边界同取向。
    """

    def __init__(self, kind, source="export", now_fn=time.time,
                 stale_after_sec=DEFAULT_STALE_SEC):
        self.kind = kind
        self.source = source
        self._now_fn = now_fn
        self.stale_after_sec = float(stale_after_sec or DEFAULT_STALE_SEC)
        self.last_ok_at = 0.0
        self.file_name = ""
        self.row_count = 0
        self.parse_fail_total = 0
        self.empty_total = 0
        self.missing_total = 0
        self.unsupported_total = 0
        self.last_reason = ""
        self.last_asof = 0.0
        self._rows = []  # 上一份可信读数：只有 ok 才替换它

    # ---------- 只读观察 ----------
    def current_rows(self):
        """返回上一份**可信**读数（拷贝）；解析失败期间它一直是旧值，这是设计而不是 bug。"""
        return [dict(r) for r in self._rows]

    def state(self, now=None):
        """账本自身的三态：never / stale / ok —— 与单次解析的 missing/empty 是两个维度。"""
        now = self._now_fn() if now is None else now
        if self.last_ok_at <= 0:
            return STATE_NEVER
        if now - self.last_ok_at > self.stale_after_sec:
            return STATE_STALE
        return STATE_OK

    def age_sec(self, now=None):
        now = self._now_fn() if now is None else now
        if self.last_ok_at <= 0:
            return -1.0
        return max(0.0, now - self.last_ok_at)

    # ---------- 唯一提交点 ----------
    def apply(self, result):
        """把一次解析结果并进账本。**只有 ok 会推进 last_ok_at/rows**（边界 3）。

        为什么不写进布尔：missing/empty/parse_fail/unsupported 四条分支各自的处置方向不同
        （补导出 / 查柜台会话 / 查半文件 / 换格式），折成一个"有没有数据"就变成盲操作。
        """
        self.last_reason = result.reason
        state = result.state
        if state == STATE_OK:
            self._rows = [dict(r) for r in result.rows]
            self.row_count = len(self._rows)
            self.file_name = result.file_name or self.file_name
            self.last_ok_at = self._now_fn()
            self.last_asof = result.asof
        elif state == STATE_EMPTY:
            # 空文件不进 parse_fail_total（它不是解析失败），但也不推翻上一份可信读数
            self.empty_total += 1
            self.file_name = result.file_name or self.file_name
        elif state == STATE_MISSING:
            self.missing_total += 1
        elif state == STATE_UNSUPPORTED:
            self.unsupported_total += 1
            self.parse_fail_total += 1
        elif state == STATE_PARSE_FAIL:
            self.parse_fail_total += 1
        return self

    def refresh(self, path, parser_by_kind=None):
        """发现侧的便捷入口：按本账本的类别解析指定路径并并进账本（prev 行数从账本取）。

        UnsupportedFormatError 在这条腿上收成 `unsupported` 读数：账本要的是"今天为什么没读数"，
        不是让一次格式意外把整轮读数刷成异常（但绝不收成 ok/empty——那是边界 2 禁止的折叠）。
        """
        parsers = parser_by_kind or PARSERS
        fn = parsers.get(self.kind)
        if fn is None:
            raise ValueError("unknown_read_kind:%s" % self.kind)
        # 关键：把"上一份可信行数"喂给解析器，骤降判定才有参照物（边界 3 的另一半）
        prev = self.row_count if self.last_ok_at > 0 else None
        try:
            result = fn(path, prev_row_count=prev)
        except UnsupportedFormatError as exc:
            result = ReadResult(self.kind, STATE_UNSUPPORTED,
                                file_name=os.path.basename(str(path)), reason=str(exc))
        self.apply(result)
        return result

    def snapshot(self):
        """观测面用的纯读数（字段名与 §4.1 一致，Go 侧只透传不重算）。"""
        return {
            "kind": self.kind,
            "source": self.source,
            "file_name": self.file_name,
            "state": self.state(),
            "last_ok_at": self.last_ok_at,
            "row_count": self.row_count,
            "parse_fail_total": self.parse_fail_total,
            "empty_total": self.empty_total,
            "missing_total": self.missing_total,
            "unsupported_total": self.unsupported_total,
            "reason": self.last_reason,
        }
