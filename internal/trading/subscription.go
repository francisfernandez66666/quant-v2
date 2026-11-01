// subscription.go — §SUBSCRIPTION（2026-11-01 裁决 6/7/8/9）：新股/新债「申购类委托行」的识别判据。
//
// 背景（纯函数、无 IO，与 session_guard.go 同取向）：券商把未发行的新股/新债申购也列进当日
// 委托表，这类行的形态是「申购代码段 + qty=0 + price=0」，状态码 86 在桥侧读不懂
// （`qmt_bridge_strategy.py:_ORD_STATUS` 只覆盖 48–57，owner 裁定**不给 86 造中文语义**）。
// 旧形状是"状态读不懂 ⇒ 整行吞掉并计入 unknown_status_skipped"，两个害处：
// ① unknown 告警位被每个申购日的常规噪声永久污染，真出现新码时看不出来；
// ② 排除的锚挂在"读不懂"而不是"这是申购"上 ⇒ 哪天有人补了映射，同一批行就会落进委托列表、
//
//	并被超时撤单/收盘清场当在途单发撤单（柜台必拒）。
//
// 本批按 owner 裁决把锚换正：**识别靠代码段与上市状态，与状态码能否读懂无关**。
//
// 三条不可让的口径（裁决 7/8 原话，改这段前先读 docs/IMPL_QMT_DATA_SUBSCRIPTION_20261101.md §0）：
//
//	① 申购行**要能看见**：判据只回答"这是不是申购类"，绝不回答"要不要隐藏"。隐藏形状一律不许复活。
//	② **数据不足 ⇒ 不判定**：ListingSource 返回 known=false（IPO 日历/全市场列表取不到）时一律
//	   按"不是申购"处理——宁可委托列表多一行噪声，绝不把真实委托标成申购或跳过撤单。
//	③ 价量 0 **单独不构成判定**：`qty<=0 || price<=0` 只在"仅申购段"已经命中时作辅助佐证，
//	   否则一只落库缺字段的真实委托会被误标，那是把"消噪声"换成"藏委托"。
//
// 段表的来源与边界（`qmt_gateway/contract/subscription_codes.json` 是唯一镜像，双向等值由
// subscription_codes_test.go 锁）：
//   - 「仅申购段」＝该代码段永不作为可交易正股出现（730/732/787 沪新股、754/733/783 沪新债、
//     370/380 深新债、07/08 深申购与配号），命中即可判定，**不需要上市状态背书**；
//   - 「同段」＝深市新股申购与正股同段（00/30），光看代码无法区分，必须问上市状态；
//   - 北交所段（889/810/920）**刻意不进仅申购段表**：北交所挂牌公司代码本身就 4/8 开头、
//     920 段是正股，把这三段当"永不交易"会有把真持仓票误标成申购的实际风险。它们连同原始表里
//     其它没把握的段一起记在 golden 的 `unverified_segments` 里，等真实委托表校准后再定，
//     校准完成前 golden 标 `calibration=pending`（裁决 9：先校准再固化）。
//
// English: pure predicates identifying IPO/bond subscription registration rows (segment table +
// listing state). Judgement never depends on whether status code 86 is readable. Missing listing
// data must NOT be treated as "unlisted" (fail-safe = leave the row as a normal order).
package trading

import "strings"

// subscriptionOnlyPrefixes 「仅申购段」：命中即可判为申购类，不查上市状态。
// 与 qmt_gateway/gateway.py 的 SUBSCRIPTION_ONLY_PREFIXES 必须逐项一致（golden 双向锁）。
var subscriptionOnlyPrefixes = []string{
	"730", "732", "787", // 沪市新股申购（主板 730/732、科创板 787）
	"754", "733", "783", // 沪市新债（可转债）申购与配售
	"370", "380", // 深市新债申购/配售
	"07", "08", // 深市申购与配号（六位码 07xxxx/08xxxx 永不作为可交易正股代码出现）
}

// subscriptionSharedPrefixes 「与正股同段」：深市新股申购直接用正股代码段，光看代码分不出来，
// 必须由 ListingSource 证实"未上市"才判定。
var subscriptionSharedPrefixes = []string{
	"00", "30", // 深市主板 00xxxx / 创业板 30xxxx
}

// 判定依据字符串：进标注、进日志、进 opslog 留痕，让"为什么这行被标成申购"当场可答。
const (
	SubscriptionReasonOnlySegment  = "only-segment" // 命中仅申购段（无需上市状态）
	SubscriptionReasonUnlisted     = "unlisted"     // 同段 + 已证实未上市
	SubscriptionReasonNotSubscribe = ""             // 不是申购类（含"数据不足"这一形）
)

// ListingSource 上市状态问询口。三态必须靠 known 显式表达：
// listed=true 已上市；listed=false 且 known=true 已证实未上市；known=false＝我不知道（⇒ 不判定）。
// 生产实现由 server 侧用 DataCoordinator 的 IPO 日历 + MarketAPI 的全市场列表装配
// （见 internal/server/subscription_listing.go），测试可注入桩。
// English: three-state listing probe; known=false means "no evidence" and must never be folded
// into "unlisted".
type ListingSource interface {
	IsListed(code string) (listed bool, known bool)
}

// BareCode 把 "370181.SZ" / "sh600519" / "600519" 统一成裸六位码 "600519"。
// 非六位纯数字返回空串——调用方据此直接判"不是申购"（fail-safe：脏代码不许被分类掉）。
// English: normalize to a bare 6-digit code; anything else yields "" and is never a subscription.
func BareCode(code string) string {
	s := strings.TrimSpace(code)
	if s == "" {
		return ""
	}
	// 带交易所后缀（.SH/.SZ/.BJ）先剪掉
	if i := strings.IndexByte(s, '.'); i > 0 {
		s = s[:i]
	}
	// 桥/网关历史上也送 sh600519 / sz000001 这种前缀写法
	if len(s) == 8 {
		lower := strings.ToLower(s[:2])
		if lower == "sh" || lower == "sz" || lower == "bj" {
			s = s[2:]
		}
	}
	if len(s) != 6 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

// SubscriptionOnlySegment 判断是否命中「仅申购段」。纯前缀比较，零 IO，
// 撤单护栏（controller.go 的 HaltAll/SweepOrders）与读面标注（server/qmt.go）都引这一个函数，
// 避免两处各写一份表而漂移。
// English: pure prefix test against the never-tradable segments; single source for all call sites.
func SubscriptionOnlySegment(code string) bool {
	bare := BareCode(code)
	if bare == "" {
		return false
	}
	for _, p := range subscriptionOnlyPrefixes {
		if strings.HasPrefix(bare, p) {
			return true
		}
	}
	return false
}

// SubscriptionSharedSegment 判断是否命中「与正股同段」（深市 00/30）。命中后仍需上市状态证据。
func SubscriptionSharedSegment(code string) bool {
	bare := BareCode(code)
	if bare == "" {
		return false
	}
	for _, p := range subscriptionSharedPrefixes {
		if strings.HasPrefix(bare, p) {
			return true
		}
	}
	return false
}

// ClassifySubscription 是本文件唯一的判定入口：只回答"是不是申购类"＋依据字符串。
//
// 判定顺序按成本排（顺序本身是断言对象，见 subscription_test.go）：
//  1. 裸码归一失败（脏代码）⇒ 不是申购；
//  2. 仅申购段命中 ⇒ 申购（`only-segment`），此路**不看** qty/price、**不查**上市状态；
//  3. 同段命中 ⇒ 问 ListingSource；只有 known=true 且 listed=false 才判申购（`unlisted`）；
//  4. 其余 ⇒ 不是申购。
//
// 明确不做的事：qty/price 的 0 值单独绝不构成判定（裁决 ③）；src==nil 时同段一律不判定
// （装配缺失＝数据不足，落回"保留"）。
//
// English: the single classification entry point. Order is cost-ordered and locked by tests.
// Zero qty/price alone never classifies; a nil source means "no evidence" and yields false.
func ClassifySubscription(code string, qty int, price float64, src ListingSource) (bool, string) {
	if BareCode(code) == "" {
		return false, SubscriptionReasonNotSubscribe
	}
	if SubscriptionOnlySegment(code) {
		return true, SubscriptionReasonOnlySegment
	}
	if SubscriptionSharedSegment(code) {
		if src == nil {
			return false, SubscriptionReasonNotSubscribe
		}
		listed, known := src.IsListed(BareCode(code))
		if known && !listed {
			return true, SubscriptionReasonUnlisted
		}
	}
	return false, SubscriptionReasonNotSubscribe
}

// ZeroValueRegistration 判断是否是"登记行形态"（0 量或 0 价）。
// **只允许作为辅助佐证使用**——单独拿它当判据是本文件禁止的形状。
func ZeroValueRegistration(qty int, price float64) bool {
	return qty <= 0 || price <= 0
}

// CancelSkipsSubscriptionRow 撤单护栏：紧急停止与例行超时撤单/收盘清场**共用**这一条判据。
//
// 形状是白名单跳过（不是黑名单穷举派发前缀）：只有「仅申购段 ∧ 0 值登记形态」才允许不撤。
// 为什么必须这么写：真单万一因为回报缺字段而落成 0 值，也绝不能被静默跳过——那是把
// "跳过申购"换成"紧急停止漏撤一笔真单"，方向完全错。所以这里刻意**不加** signal_id 前缀判据，
// 前缀清单（buy:/sell:/t0:/ext:/pend:/seq:）会随批次变，不配当判据。
// 命中"同段 + 未上市"那类行不在此跳过：它需要 IO（上市状态），而撤单循环每 30s 跑一次，
// 在热路径里查外源不划算；况且这类行本身 qty/price 为正时就是可撤的真实委托。
//
// English: cancel-path guard — whitelist shape. Only a never-tradable segment combined with the
// zero-value registration form may skip the cancel; dirty/zero-valued real orders are always
// cancelled (never silently dropped from the kill-switch path).
func CancelSkipsSubscriptionRow(code string, qty int, price float64) bool {
	return SubscriptionOnlySegment(code) && ZeroValueRegistration(qty, price)
}
