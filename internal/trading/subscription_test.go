// subscription_test.go — §SUBSCRIPTION（2026-11-01）判据单源的行为锁 + golden 双向等值锁。
//
// 覆盖四类（每条都对应 docs/IMPL_QMT_DATA_SUBSCRIPTION_20261101.md B1 的断言表）：
//  1. 判定顺序矩阵：仅申购段 / 与正股同段 / 已上市 / 未上市 / 数据不足 / 脏代码；
//  2. 三条裁决底线：0 值单独不构成判定、同段数据不足 ⇒ 不判定、判据绝不回答"要不要隐藏"；
//  3. 撤单护栏是白名单形状：只有「仅申购段 ∧ 0 值」可以不撤，脏字段的真实委托必须照撤；
//  4. 段表与 qmt_gateway/contract/subscription_codes.json 双向等值（两端漂移当场红）。
package trading

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// stubListing 可注入的上市状态桩：calls 计数用来证明"段未命中绝不查外源"（零网络调用断言）。
type stubListing struct {
	state map[string]string // 裸码 → listed / unlisted / unknown
	calls int
}

func (s *stubListing) IsListed(code string) (bool, bool) {
	s.calls++
	switch s.state[code] {
	case "listed":
		return true, true
	case "unlisted":
		return false, true
	default:
		return false, false // unknown＝我不知道，判据必须落回"不判定"
	}
}

// TestClassifyMatrix 判定矩阵：逐条点名期望，包含依据字符串（标注与留痕要展示它）。
func TestClassifyMatrix(t *testing.T) {
	sl := &stubListing{state: map[string]string{
		"002961": "listed",
		"301099": "unlisted",
		"001399": "unknown",
	}}
	cases := []struct {
		name       string
		code       string
		qty        int
		price      float64
		wantSub    bool
		wantReason string
	}{
		{"现网实录 370181 新债申购", "370181.SZ", 0, 0, true, SubscriptionReasonOnlySegment},
		{"沪主板申购段价量正常也判", "730899", 1000, 50, true, SubscriptionReasonOnlySegment},
		{"深市配号 08 段", "080123", 10, 100, true, SubscriptionReasonOnlySegment},
		{"同段已上市是真实委托", "002961", 500, 12.5, false, SubscriptionReasonNotSubscribe},
		{"同段证实未上市才判申购", "301099", 100, 8.8, true, SubscriptionReasonUnlisted},
		{"同段数据不足必须保留", "001399", 100, 8.8, false, SubscriptionReasonNotSubscribe},
		{"沪市正股 0 值也绝不判申购", "600000.SH", 0, 0, false, SubscriptionReasonNotSubscribe},
		{"北交所正股段校准前不判", "920001", 0, 0, false, SubscriptionReasonNotSubscribe},
		{"北交所 4 开头挂牌段不判", "430139", 0, 0, false, SubscriptionReasonNotSubscribe},
		{"脏代码不判定", "abc123", 0, 0, false, SubscriptionReasonNotSubscribe},
		{"短代码不判定", "12345", 0, 0, false, SubscriptionReasonNotSubscribe},
		{"空代码不判定", "", 0, 0, false, SubscriptionReasonNotSubscribe},
		{"sh 前缀写法同样识别", "sh730899", 100, 10, true, SubscriptionReasonOnlySegment},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotSub, gotReason := ClassifySubscription(c.code, c.qty, c.price, sl)
			if gotSub != c.wantSub {
				t.Fatalf("code=%s qty=%d price=%.2f ⇒ isSubscription=%v reason=%q，期望 %v/%q",
					c.code, c.qty, c.price, gotSub, gotReason, c.wantSub, c.wantReason)
			}
			if gotReason != c.wantReason {
				t.Fatalf("code=%s 依据串=%q，期望 %q（标注与留痕直接展示这个串，不许漂）", c.code, gotReason, c.wantReason)
			}
		})
	}
}

// TestNilSourceNeverJudgesSharedSegment 装配缺失（src==nil）必须等价"数据不足 ⇒ 不判定"，
// 而不是"当作未上市"。这是裁决 8 的另一条形：判据不许把 nil 折成 false。
func TestNilSourceNeverJudgesSharedSegment(t *testing.T) {
	if sub, reason := ClassifySubscription("301099", 100, 8.8, nil); sub {
		t.Fatalf("src==nil 时同段被判成申购（reason=%q）：数据不足必须保留真实委托", reason)
	}
	if sub, _ := ClassifySubscription("730899", 100, 8.8, nil); !sub {
		t.Fatal("仅申购段必须不依赖上市状态：src==nil 也要判申购（否则网关侧无 IPO 日历时整体失明）")
	}
}

// TestZeroValueAloneNeverClassifies 单独钉裁决 ③：任何非申购段代码，无论 qty/price 多脏都不判定。
// 摘掉这条会把"消噪声"换成"藏真实委托"——落库缺字段的真单会被标成申购类。
func TestZeroValueAloneNeverClassifies(t *testing.T) {
	realCodes := []string{"600519", "002961", "301099", "920001", "430139"}
	for _, code := range realCodes {
		if sub, reason := ClassifySubscription(code, 0, 0, &stubListing{state: map[string]string{}}); sub {
			t.Fatalf("真实代码 %s 在 qty=0/price=0 下被标成申购（reason=%q）：0 值单独绝不构成判定", code, reason)
		}
	}
}

// TestZeroNetworkCallsWhenSegmentMiss 段表未命中绝不查上市状态：撤单/读面都在热路径上，
// 一次全市场列表是 5500 只的 HTTP。这条把"判定顺序按成本排"从口头变成断言。
func TestZeroNetworkCallsWhenSegmentMiss(t *testing.T) {
	sl := &stubListing{state: map[string]string{}}
	for i := 0; i < 100; i++ {
		ClassifySubscription("600519", 100, 10, sl) // 沪市正股：两段都不命中 ⇒ 零查询
		ClassifySubscription("002961", 100, 10, sl) // 同段：必须查一次（已上市 ⇒ 不判定）
	}
	if sl.calls != 100 {
		t.Fatalf("查外源次数=%d，期望 100：只有「同段」那 100 次该查，段未命中的 600519 一次都不许查", sl.calls)
	}
	sl2 := &stubListing{state: map[string]string{}}
	ClassifySubscription("370181.SZ", 0, 0, sl2)
	ClassifySubscription("730899", 1000, 50, sl2)
	if sl2.calls != 0 {
		t.Fatalf("仅申购段走了查外源那一路（calls=%d）：第一段命中即判，不查上市状态", sl2.calls)
	}
}

// TestCancelGuardWhitelistShape 撤单护栏是白名单：只有「仅申购段 ∧ 0 值」可以不撤。
// 三条形必须各自挡住：①真实委托即便字段脏也必须照撤；②仅申购段但价量齐全也不撒手
// （那种形态更可能是柜台登记异常，撤失败进明细比静默跳过可查）；③非申购段的 0 值必须照撤。
func TestCancelGuardWhitelistShape(t *testing.T) {
	if !CancelSkipsSubscriptionRow("370181.SZ", 0, 0) {
		t.Fatal("仅申购段 + 0 值应当被护栏跳过（这就是裁决 6 要的效果）")
	}
	if CancelSkipsSubscriptionRow("600000", 0, 0) {
		t.Fatal("真实代码 + 0 值被跳过：紧急停止会静默漏撤一笔真单，形状错")
	}
	if CancelSkipsSubscriptionRow("730899", 1000, 50) {
		t.Fatal("价量齐全的仅申购段被跳过：护栏必须要求 0 值登记形态同时成立")
	}
	if CancelSkipsSubscriptionRow("920001", 0, 0) {
		t.Fatal("校准前的北交所段被跳过：该段未进仅申购段表，不得参与撤单判定")
	}
	// 脏字段 + 仅申购段：0 值成立 ⇒ 跳过（这条与 ① 的差别只在代码段，正是白名单的边界）
	if !CancelSkipsSubscriptionRow("080123", 0, 100) {
		t.Fatal("0 量也算登记形态（qty<=0 ∨ price<=0），应当跳过")
	}
}

// TestBareCodeShapes 裸码归一的形态覆盖：后缀、sh/sz/bj 前缀、大小写、空格、脏值。
func TestBareCodeShapes(t *testing.T) {
	cases := map[string]string{
		"370181.SZ": "370181",
		"600519.SH": "600519",
		"sh730899":  "730899",
		"SZ002961":  "002961",
		"920001.BJ": "920001",
		"  600000 ": "600000",
		"60000":     "",
		"abc123":    "",
		"":          "",
	}
	for in, want := range cases {
		if got := BareCode(in); got != want {
			t.Fatalf("BareCode(%q)=%q，期望 %q", in, got, want)
		}
	}
}

// subscriptionGolden 镜像 contract/subscription_codes.json 的结构。
type subscriptionGolden struct {
	Calibration        string   `json:"calibration"`
	OnlySegments       []string `json:"only_segments"`
	SharedSegments     []string `json:"shared_segments"`
	UnverifiedSegments []string `json:"unverified_segments"`
	Samples            []struct {
		Code         string  `json:"code"`
		Qty          int     `json:"qty"`
		Price        float64 `json:"price"`
		Listed       string  `json:"listed"`
		ExpectSub    bool    `json:"expect_subscription"`
		ExpectReason string  `json:"expect_reason"`
	} `json:"samples"`
}

// TestSubscriptionGoldenMatchesConsts golden 双向等值锁：
// Go 常量 ↔ JSON 逐项一致（多一项少一项都红），且 samples 的期望与实际分类逐条相符。
// 这条锁存在的理由：网关与 Go 两端各自内联一份表，漂移的表现是"网关跳了、Go 落库了"，
// 比没做还糟（委托列表半标半不标）。运行时不读文件（部署目录不同源），所以靠锁而不是靠加载。
func TestSubscriptionGoldenMatchesConsts(t *testing.T) {
	raw, err := os.ReadFile("../../qmt_gateway/contract/subscription_codes.json")
	if err != nil {
		t.Fatalf("读 golden 失败（新文件必须进部署清单与仓库，见 AGENTS 发版纪律）: %v", err)
	}
	var g subscriptionGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("golden 解析失败: %v", err)
	}
	assertSameSet(t, "only_segments", g.OnlySegments, subscriptionOnlyPrefixes)
	assertSameSet(t, "shared_segments", g.SharedSegments, subscriptionSharedPrefixes)

	if g.Calibration != "pending" && g.Calibration != "fixed" {
		t.Fatalf("calibration=%q，只许 pending（校准前）或 fixed（校准后固化）", g.Calibration)
	}
	// 校准前的段绝不能出现在判据里（防止有人边校准边把没把握的段塞进主表）
	for _, seg := range g.UnverifiedSegments {
		for _, p := range subscriptionOnlyPrefixes {
			if strings.HasPrefix(seg, p) {
				t.Fatalf("未校准段 %s 与判据段 %s 重叠：裁决 9 要求先校准再固化", seg, p)
			}
		}
	}

	for _, s := range g.Samples {
		sl := &stubListing{state: map[string]string{BareCode(s.Code): s.Listed}}
		sub, reason := ClassifySubscription(s.Code, s.Qty, s.Price, sl)
		if sub != s.ExpectSub {
			t.Fatalf("golden 样本 %s（listed=%s qty=%d price=%.2f）实判=%v，期望=%v", s.Code, s.Listed, s.Qty, s.Price, sub, s.ExpectSub)
		}
		if sub && reason != s.ExpectReason {
			t.Fatalf("golden 样本 %s 依据串=%q，期望=%q", s.Code, reason, s.ExpectReason)
		}
	}
}

// assertSameSet 等值且等长比较（顺序不敏感，但缺失/多余都判红）。
func assertSameSet(t *testing.T, field string, golden, code []string) {
	t.Helper()
	if len(golden) != len(code) {
		t.Fatalf("%s 数量不一致：golden=%d 代码=%d（golden=%v code=%v）", field, len(golden), len(code), golden, code)
	}
	inCode := make(map[string]bool, len(code))
	for _, v := range code {
		inCode[v] = true
	}
	for _, v := range golden {
		if !inCode[v] {
			t.Fatalf("%s：golden 有 %q 而代码常量没有（两端漂移＝网关跳了、Go 落库了）", field, v)
		}
	}
}
