// trade_card_20261105_test.go — §TRADE-CARD（2026-11-05）做 T 的投影腿与空仓路径的行为锁。
//
// 锁两件事：
//  1. tRoundAdvices 把日内回转账本投成 加/减/留 三种动词，并一律带 TradeKind=intraday_t；
//     已收口（done/unwound/converted）的行落 留 ⇒ **不在**交易级词表里，所以不会推；
//  2. pushTradeLevelWithoutPositions：空仓（建仓单被撤、持仓表还是空的）也要把交易级动作
//     推到消息中心——旧实现在 pushRealAdvice 的空仓分支直接 return，撤单一次都不响。
package engine

// §SESSION-WINDOW：本文件测试夹具统一注入固定时钟（见 testclock_test.go），钉到当日北京时 10:15；不固定会在 09:15–09:30 与 14:52 后假红。

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/trading"
)

// fixedDay 本组用例固定的交易日（2026-10-28 周三，交易日；与 trading 包 bjAt 的口径同日）。
const fixedDay = "20261028"

// tCardFix 造一个带 T 账本注册表的引擎（时刻钉死，避免用真实钟判"今天"）。
func tCardFix(t *testing.T) (*Engine, *tLiveRegistry) {
	t.Helper()
	rg := newTLiveRegistry(t.TempDir())
	at := time.Date(2026, 10, 28, 10, 0, 0, 0, cntime.Loc)
	rg.nowFn = func() time.Time { return at }
	rg.dayOfFn = func(time.Time) string { return fixedDay }
	e := &Engine{msgStore: data.NewMessageStore(""), tLiveReg: rg}
	return e, rg
}

// TestTRoundAdvicesVerbs 每一轮回转出一行，动词按首腿方向；已收口的落 留。
func TestTRoundAdvicesVerbs(t *testing.T) {
	e, rg := tCardFix(t)
	st := rg.get("u_1")
	if st == nil {
		t.Fatal("注册表应给出该账号账本")
	}
	rounds := []*TRoundLive{
		{ID: "t0:600001:buy_first:" + fixedDay + ":1", Day: fixedDay, Code: "600001", Name: "首开股份",
			Dir: tLDirBuy, Status: "open", Qty: 500, BuyFilled: 500, BuyPrice: 9.9, OpenedAt: "2026-10-28T10:00:00+08:00"},
		{ID: "t0:600002:sell_first:" + fixedDay + ":1", Day: fixedDay, Code: "600002", Name: "中石化",
			Dir: tLDirSell, Status: "open", Qty: 300, SellFilled: 300, SellPrice: 6.1, OpenedAt: "2026-10-28T10:05:00+08:00"},
		{ID: "t0:600003:buy_first:" + fixedDay + ":1", Day: fixedDay, Code: "600003", Name: "已配平",
			Dir: tLDirBuy, Status: "done", Qty: 200, BuyFilled: 200, BuyPrice: 5, SellFilled: 200, SellPrice: 5.2,
			QMatched: 200, TPnlNet: 33.5, OpenedAt: "2026-10-28T09:40:00+08:00", ClosedAt: "2026-10-28T10:20:00+08:00"},
		{ID: "t0:600004:buy_first:" + fixedDay + ":1", Day: fixedDay, Code: "600004", Name: "转持仓",
			Dir: tLDirBuy, Status: "converted", Qty: 400, BuyFilled: 400, BuyPrice: 7, UnwindReason: "no_signal",
			OpenedAt: "2026-10-28T09:50:00+08:00"},
	}
	for _, r := range rounds {
		if err := st.put(r); err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
	}
	rows := e.tRoundAdvices("u_1", time.Now())
	if len(rows) != 4 {
		t.Fatalf("四轮回转应四行, got %d", len(rows))
	}
	byCode := map[string]trading.PositionAdvice{}
	for _, r := range rows {
		if r.TradeKind != trading.AdviceTradeKindIntradayT {
			t.Fatalf("每一行都必须标做T: %+v", r)
		}
		if !strings.HasPrefix(r.Reason, "做T·") {
			t.Fatalf("理由必须以 做T· 开头: %+v", r.Reason)
		}
		byCode[r.Code] = r
	}
	if byCode["600001"].Action != "加仓" || byCode["600001"].Level != "高" {
		t.Fatalf("正 T 在途应是 加仓/高: %+v", byCode["600001"])
	}
	if byCode["600002"].Action != "减仓" {
		t.Fatalf("反 T 在途应是 减仓: %+v", byCode["600002"])
	}
	for _, c := range []string{"600003", "600004"} {
		if byCode[c].Action != "留" {
			t.Fatalf("已收口的轮次应落 留（不是交易级，故不推）: %+v", byCode[c])
		}
		if !strings.Contains(byCode[c].Reason, "已收口") {
			t.Fatalf("收口行要写明已收口: %s", byCode[c].Reason)
		}
	}
	if !strings.Contains(byCode["600003"].Reason, "已配平200股") || !strings.Contains(byCode["600003"].Reason, "净额+33.50元") {
		t.Fatalf("配平轮应带配对量与净额: %s", byCode["600003"].Reason)
	}
	if !strings.Contains(byCode["600004"].Reason, "原因=no_signal") {
		t.Fatalf("转持仓要带收摊原因: %s", byCode["600004"].Reason)
	}
	// OrderID 必须是回转幂等键（提醒按它去重，一天两次 T 就两条）。
	if byCode["600001"].OrderID != "t0:600001:buy_first:"+fixedDay+":1" {
		t.Fatalf("撤/T 行的定位键不能丢: %+v", byCode["600001"])
	}
}

// TestTRoundAdvicesUnknownStatusIgnored 认不出状态的轮次不出行：
// 给一个"留"或"加仓"都是猜——卡上不许出现猜出来的动作。
func TestTRoundAdvicesUnknownStatusIgnored(t *testing.T) {
	e, rg := tCardFix(t)
	st := rg.get("u_2")
	if err := st.put(&TRoundLive{ID: "t0:600005:buy_first:" + fixedDay + ":1", Day: fixedDay,
		Code: "600005", Dir: tLDirBuy, Status: "weird"}); err != nil {
		t.Fatal(err)
	}
	if rows := e.tRoundAdvices("u_2", time.Now()); len(rows) != 0 {
		t.Fatalf("未知状态不该出行, got %+v", rows)
	}
}

// TestPushTradeLevelWithoutPositions 空仓路径：一腿在途的 T 行仍要进消息中心（这是
// pushRealAdvice 空仓分支里唯一新增的那条腿）。没持仓、没行情、sse 为 nil 都不许把它挡掉。
func TestPushTradeLevelWithoutPositions(t *testing.T) {
	e, rg := tCardFix(t)
	if err := rg.get("u_3").put(&TRoundLive{
		ID: "t0:600006:buy_first:" + fixedDay + ":1", Day: fixedDay, Code: "600006", Name: "空仓做T",
		Dir: tLDirBuy, Status: "open", Qty: 500, BuyFilled: 500, BuyPrice: 8.8,
		OpenedAt: "2026-10-28T10:00:00+08:00",
	}); err != nil {
		t.Fatal(err)
	}
	e.tradeActDay = fixedDay
	e.tradeActions = map[string]tradeAction{
		"buy|600006||t": {Kind: tradeActionBuy, Code: "600006", Placed: true, Qty: 500, Price: 8.8,
			Reason: "委托已受理（≠成交）", At: time.Now(), Day: fixedDay, IntradayT: true},
	}
	// ctrl 为非 nil 的空控制器（无持仓、无挂单）；sse 传 nil 只验消息中心那半条链。
	ctrl := newPinnedController(nil, nil, "u_3", tradingEmptyCfg(), nil)
	e.pushTradeLevelWithoutPositions("u_3", ctrl, nil)

	list := e.msgStore.ListVisible("u_3")
	if len(list) != 1 {
		t.Fatalf("空仓时做T 的一腿仍应落 1 条交易级消息, got %d: %+v", len(list), list)
	}
	if !strings.Contains(list[0].Title, "做T·加仓") {
		t.Errorf("标题必须带做T 标注: %+v", list[0])
	}
	if !strings.Contains(list[0].Body, "已自动买入 500股@8.80") {
		t.Errorf("正文应引用 T 腿自己的执行留痕: %s", list[0].Body)
	}
}

// TestPushTradeLevelWithoutPositionsSilentWhenNothing 空仓且没有任何交易级动作 ⇒ 一条都不落
// （撤单快照为空 + 没有回转轮次时不许凭空造消息）。
func TestPushTradeLevelWithoutPositionsSilentWhenNothing(t *testing.T) {
	e, _ := tCardFix(t)
	ctrl := newPinnedController(nil, nil, "u_4", tradingEmptyCfg(), nil)
	e.pushTradeLevelWithoutPositions("u_4", ctrl, nil)
	if n := len(e.msgStore.ListVisible("u_4")); n != 0 {
		t.Fatalf("没发生过任何交易级动作就不该落库, got %d", n)
	}
}

// TestTradeActionSuffixUnknownNeverClaimsAbsence 三态里最容易撒谎的一态：
// 查无留痕（多半是重启前做的）不许写成"本轮未下单"。方向搞反就是让用户去手动补一笔
// 已经存在的委托。
func TestTradeActionSuffixUnknownNeverClaimsAbsence(t *testing.T) {
	e, _ := tCardFix(t)
	row := trading.PositionAdvice{Code: "600007", Action: "止损", Level: "高", Reason: "破位", GeneratedAt: time.Now()}
	s := e.tradeActionSuffix(row)
	if !strings.Contains(s, "未见证") || strings.Contains(s, "未自动下单") {
		t.Fatalf("未见证态的措辞只能是未见证: %s", s)
	}
}

// TestTradeLevelVocabularyIsWhitelist 词表是白名单：撤单/加仓/减仓/止盈/止损在表内，
// 其余（持有/格局/清仓之外的新词）默认不推——新增动作的人没想过"会不会轰炸用户"时，
// 默认结果必须是安静。
func TestTradeLevelVocabularyIsWhitelist(t *testing.T) {
	for _, a := range []string{"撤单", "加仓", "减仓", "止盈", "止损"} {
		if !tradeLevelAlertActions[a] {
			t.Errorf("%s 必须在交易级词表里", a)
		}
	}
	for _, a := range []string{"持有", "格局", "观察", "买入信号提示"} {
		if tradeLevelAlertActions[a] {
			t.Errorf("%s 不该在词表里（不到交易级别）", a)
		}
	}
}

// tradingEmptyCfg 一个只 Enabled 的 QMT 配置（本组不测下单闸，控制器只当动作快照的宿主）。
func tradingEmptyCfg() config.QMTConfig {
	return config.QMTConfig{Enabled: true}
}
