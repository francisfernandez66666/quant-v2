// 交易级提醒（§TRADE-CARD 2026-11-05）：卡上的撤/加/减/走四类进消息中心 + P1 强推，
// 持有/格局/观察预警卡一律不推；"系统做没做这一笔"读执行留痕，不读开关。
// 本文件同时锁住做 T 的特殊标注与三条去重粒度（撤单按委托号、做T 按回转幂等键、其余按码@动作@日）。
// English: trade-level reminders — cancel/add/trim/exit push, hold/watch never do; the
// "did the system act?" suffix comes from the executor's ledger, not the auto-sell switch.
package engine

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/trading"
)

// liveAdvice 构造一条实盘持仓处理建议（测试辅助）。
// English: builds one live-position advice (test helper).
func liveAdvice(code, name, action, reason string) trading.PositionAdvice {
	return trading.PositionAdvice{
		Code:        code,
		Name:        name,
		Action:      action,
		Level:       "高",
		RefPrice:    10.5,
		ProfitPct:   -6.8,
		DrawdownPct: -6.8,
		Reason:      reason,
		Source:      "discipline",
		Strategy:    "n_shape",
		GeneratedAt: time.Now(),
	}
}

// TestSyncTradeAlertsUnconfirmedStillPushes 止损级建议即使本进程没见证过下单动作，也必须照推
// （方向是"宁可多提醒一次"），但正文不许写成"已触发自动卖出"，也不许写成"没下单"——
// 只能写"未见证，请核对当日委托"。这两种误报分别对应"用户以为系统卖了"和"用户以为要自己卖"。
func TestSyncTradeAlertsUnconfirmedStillPushes(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore(""), notifier: notify.New()}
	ch := e.notifier.RegisterWS("live")
	defer e.notifier.UnregisterWS("live")

	adv := []trading.PositionAdvice{liveAdvice("600519", "贵州茅台", "止损", "止损窗结算无信号，止损离场")}
	e.syncTradeLevelAlerts("u123", adv)

	select {
	case m := <-ch:
		if m.Level != notify.LevelHigh {
			t.Errorf("止损强提醒应为 LevelHigh, got %+v", m)
		}
		if !strings.Contains(m.Title, "止损") {
			t.Errorf("强提醒标题应点名止损: %+v", m)
		}
		if strings.Contains(m.Content, "已触发自动卖出") || strings.Contains(m.Content, "本轮未自动下单") {
			t.Errorf("没有执行留痕时不许声称系统的动作，正文应是未见证: %s", m.Content)
		}
		if !strings.Contains(m.Content, "未见证") {
			t.Errorf("正文应写未见证并请核对当日委托: %s", m.Content)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("止损建议未触发强提醒")
	}

	list := e.msgStore.ListVisible("u123")
	if len(list) != 1 {
		t.Fatalf("应落库 1 条私有消息, got %d", len(list))
	}
	it := list[0]
	if it.Scope != "u123" || it.Level != "止损" || it.Action != "卖出" {
		t.Errorf("消息字段不符: %+v", it)
	}
	if !strings.Contains(it.Body, "止损") {
		t.Errorf("正文应带纪律原因: %s", it.Body)
	}

	// 同日重复：键已存在 → 不再重复推送（5s 循环不轰炸）。
	e.syncTradeLevelAlerts("u123", adv)
	select {
	case m := <-ch:
		t.Errorf("同日重复不应再推强提醒, got %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSyncTradeAlertsPlacedSuffixFromLedger 执行留痕说"已受理"时，正文才许写已自动卖出（带真实报出量）。
// 这条锁的是本轮改掉的那颗假读数：旧实现看开关（Enabled&&mode=auto&&AutoSell）就写"已触发"，
// 风控拒掉的单也一样被写。现在开关状态完全不参与文案。
func TestSyncTradeAlertsPlacedSuffixFromLedger(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore("")}
	e.recordTradeAction(tradeAction{
		Kind: tradeActionSell, Code: "000001.SH", Class: "止盈", Placed: true,
		Qty: 500, Price: 12.34, Reason: "卖单已受理（≠成交）", At: time.Now(),
	})
	e.syncTradeLevelAlerts("10086", []trading.PositionAdvice{
		liveAdvice("000001", "平安银行", "止盈", "止盈窗结算无信号，止盈离场"),
	})
	list := e.msgStore.ListVisible("10086")
	if len(list) != 1 {
		t.Fatalf("应落库 1 条, got %d", len(list))
	}
	if !strings.Contains(list[0].Body, "已自动卖出 500股@12.34") {
		t.Errorf("留痕为已受理时正文应报出真实量价, got: %s", list[0].Body)
	}
	if strings.Contains(list[0].Body, "未见证") {
		t.Errorf("有留痕却报未见证＝把已知说成未知: %s", list[0].Body)
	}
	// 私有作用域 ID 前缀（换名后仍是 u<uid>|trade@，跨账号不许串键）。
	if !strings.HasPrefix(list[0].ID, "u10086|trade@000001@止盈@") {
		t.Errorf("私有作用域 ID 应带 u<uid>|trade@ 前缀, got: %s", list[0].ID)
	}
}

// TestSyncTradeAlertsRejectedSuffixNamesGate 留痕为"一笔都没发出"时，正文必须带上是哪道闸/哪个拒因，
// 并给出"请手动处理"的指向（旧形态只有一句开关未开启，用户看不到真实原因）。
func TestSyncTradeAlertsRejectedSuffixNamesGate(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore("")}
	e.recordTradeAction(tradeAction{
		Kind: tradeActionSell, Code: "600519", Class: "止损", Placed: false,
		Reason: "跌停不可追单", At: time.Now(),
	})
	e.syncTradeLevelAlerts("u123", []trading.PositionAdvice{
		liveAdvice("600519", "贵州茅台", "止损", "破位止损"),
	})
	list := e.msgStore.ListVisible("u123")
	if len(list) != 1 {
		t.Fatalf("应落库 1 条, got %d", len(list))
	}
	body := list[0].Body
	if !strings.Contains(body, "本轮未自动下单：跌停不可追单") || !strings.Contains(body, "请手动处理") {
		t.Errorf("正文应写未发出并带拒因与手动处理指向: %s", body)
	}
}

// TestSyncTradeAlertsAddPushes 加仓现在也推（owner 裁「撤+加+减+走 四类全推」）：
// 自动买入是真下真单的动作，属于交易级；旧实现只推卖出三类，加仓一条都不响。
func TestSyncTradeAlertsAddPushes(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore(""), notifier: notify.New()}
	ch := e.notifier.RegisterWS("live")
	defer e.notifier.UnregisterWS("live")

	e.recordTradeAction(tradeAction{
		Kind: tradeActionBuy, Code: "600001", Placed: true, Qty: 300, Price: 9.9,
		Reason: "委托已受理（≠成交）", At: time.Now(),
	})
	add := liveAdvice("600001", "测试A", "加仓", "信号活跃且回撤可控")
	add.SuggestQty = 300
	add.SuggestAmount = 2970
	e.syncTradeLevelAlerts("u123", []trading.PositionAdvice{add})

	select {
	case m := <-ch:
		if m.Level != notify.LevelHigh {
			t.Errorf("加仓应为 LevelHigh, got %+v", m)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("加仓建议未触发强提醒（四类全推的裁决没落地）")
	}
	list := e.msgStore.ListVisible("u123")
	if len(list) != 1 || list[0].Action != "买入" {
		t.Fatalf("加仓应落库一条买入动作消息, got %+v", list)
	}
	if !strings.Contains(list[0].Body, "建议量:300股") {
		t.Errorf("正文应带上当日定权的量: %s", list[0].Body)
	}
	if !strings.Contains(list[0].Body, "已自动买入 300股@9.90") {
		t.Errorf("留痕为已受理时正文应报买入量价: %s", list[0].Body)
	}
}

// TestSyncTradeAlertsHoldNeverPushes 非交易级（持有/格局）不推——owner 令「不到交易级别不要提醒」。
// 减仓/止盈/止损照旧推（词表白名单，新增动作默认不推）。
func TestSyncTradeAlertsHoldNeverPushes(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore(""), notifier: notify.New()}
	ch := e.notifier.RegisterWS("live")
	defer e.notifier.UnregisterWS("live")

	e.syncTradeLevelAlerts("u123", []trading.PositionAdvice{
		liveAdvice("600002", "测试B", "格局", "盈利且趋势完好"),
		liveAdvice("600003", "测试C", "持有", "[卖出观察中] 观察窗内等待信号"),
		{Code: "600004", TsCode: "600004.SH", Name: "测试D", Action: "买入信号提示", Level: "高", Reason: "词表外的新动作"},
	})

	if n := len(e.msgStore.ListVisible("u123")); n != 0 {
		t.Errorf("非交易级不应落库, got %d", n)
	}
	select {
	case m := <-ch:
		t.Errorf("非交易级不应强提醒, got %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSyncTradeAlertsCancelPerOrderID 撤单提醒按**委托号**一条（owner 裁决）：
// 同日同一只票撤两笔必须出两条，折成"码@动作@日"一条就会把第二次撤单静默吞掉。
func TestSyncTradeAlertsCancelPerOrderID(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore("")}
	mk := func(oid, src, kind string) trading.PositionAdvice {
		return trading.PositionAdvice{
			Code: "600010", TsCode: "600010.SH", Name: "测试E", Action: "撤单", Level: "高",
			Reason: "信号消失即撤", Source: trading.AdviceSourceCancel, OrderID: oid,
			CancelKind: kind, CancelSource: src, GeneratedAt: time.Now(),
		}
	}
	e.syncTradeLevelAlerts("u123", []trading.PositionAdvice{
		mk("GW-1001", trading.SweepSourceSignalGone, trading.SweepActionRequested),
		mk("GW-1002", trading.SweepSourceSweep, trading.SweepActionWindow),
	})
	list := e.msgStore.ListVisible("u123")
	if len(list) != 2 {
		t.Fatalf("同码两笔撤单应各落一条, got %d", len(list))
	}
	for _, it := range list {
		if it.Action != "撤单" {
			t.Errorf("撤单消息的 Action 应是撤单: %+v", it)
		}
		if !strings.Contains(it.ID, "cancel@GW-10") {
			t.Errorf("撤单去重键应按委托号: %s", it.ID)
		}
	}
	joined := list[0].Body + "||" + list[1].Body
	if !strings.Contains(joined, "撤单请求已发出") {
		t.Errorf("已发出的撤单应写明等待交易所确认: %s", joined)
	}
	if !strings.Contains(joined, "禁撤窗口内") || !strings.Contains(joined, "还挂在柜台上") {
		t.Errorf("窗口内的撤单必须写清这单还挂着（没撤成）: %s", joined)
	}
}

// TestSyncTradeAlertsTRoundMarked 做 T 的行必须特殊标注，且与战略加减**分键**：
// 同一天同一只票先做 T 再收到战略止损，两条都要推；共键会让后一条永久静默。
func TestSyncTradeAlertsTRoundMarked(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore("")}
	tRow := trading.PositionAdvice{
		Code: "600011", TsCode: "600011.SH", Name: "测试F", Action: "加仓", Level: "高",
		Reason: "做T·正T（先买机动仓，计划配对卖） 状态=一腿在途 计划500股",
		Source: "t_round", OrderID: "t0:600011:buy_first:20261105:1",
		TradeKind: trading.AdviceTradeKindIntradayT, GeneratedAt: time.Now(),
	}
	strategic := liveAdvice("600011", "测试F", "止损", "破位止损")
	// 作用域用 "123"：消息 ID 前缀是引擎加的 "u"+uid，写成 "u123" 会得到 "uu123|" 这种
	// 看着像断言、其实是在断一个生产永远不会出现的键。
	e.syncTradeLevelAlerts("123", []trading.PositionAdvice{tRow, strategic})

	list := e.msgStore.ListVisible("123")
	if len(list) != 2 {
		t.Fatalf("做T 与战略止损应各一条, got %d", len(list))
	}
	var tBody, sBody string
	for _, it := range list {
		if strings.HasPrefix(it.ID, "u123|trade@t@") {
			tBody = it.Body
			if !strings.Contains(it.Title, "做T·") {
				t.Errorf("做T 的标题必须带标记: %+v", it)
			}
		} else {
			sBody = it.Body
		}
	}
	if !strings.HasPrefix(tBody, "【做T】") {
		t.Errorf("做T 正文应以前缀开始: %s", tBody)
	}
	if strings.Contains(sBody, "做T") {
		t.Errorf("战略止损正文不该带上做T 标记: %s", sBody)
	}
}

// TestSyncTradeAlertsTLegSizingUsesTKey 做 T 的买入留痕与战略买入分键：
// 只有 T 腿那一份被记 Placed，战略加仓行就**不能**引用它（否则卡上会说谎）。
func TestSyncTradeAlertsTLegSizingUsesTKey(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore("")}
	e.recordTradeAction(tradeAction{
		Kind: tradeActionBuy, Code: "600012", Placed: true, Qty: 400, Price: 8,
		Reason: "委托已受理（≠成交）", At: time.Now(), IntradayT: true,
	})
	tRow := trading.PositionAdvice{
		Code: "600012", TsCode: "600012.SH", Name: "测试G", Action: "加仓", Level: "高",
		Reason: "做T 买腿", OrderID: "t0:600012:buy_first:20261105:2",
		TradeKind: trading.AdviceTradeKindIntradayT, GeneratedAt: time.Now(),
	}
	strategic := liveAdvice("600012", "测试G", "加仓", "战略加仓")
	e.syncTradeLevelAlerts("u123", []trading.PositionAdvice{tRow, strategic})

	list := e.msgStore.ListVisible("u123")
	if len(list) != 2 {
		t.Fatalf("应两条, got %d", len(list))
	}
	for _, it := range list {
		isT := strings.Contains(it.Title, "做T")
		bodyHasPlaced := strings.Contains(it.Body, "已自动买入")
		if isT != bodyHasPlaced {
			t.Errorf("做T 标记与买入留痕必须同源（T 行读到 T 留痕、战略行读到未见证）: %+v", it)
		}
	}
}

// TestSyncTradeAlertsNoStorePerRound nil 消息存储/空建议 静默跳过，不 panic。
// English: nil message store or empty advices are silent no-ops without panicking.
func TestSyncTradeAlertsNoStorePerRound(t *testing.T) {
	e := &Engine{}
	e.syncTradeLevelAlerts("u123", nil)
	e.syncTradeLevelAlerts("u123", []trading.PositionAdvice{liveAdvice("600013", "测试H", "止损", "破位")})
}

// TestSyncTradeAlertsNoPositiveDrawdown §P4 缺陷4 锁：现价新高（DrawdownPct≥0）时
// 正文不得渲染「回撤:+0.64%」这种正值语义噪声；仅真实回撤（<0）才渲染。
// English: P4 defect-4 lock — a new stage high (drawdown ≥ 0) must not render the positive
// "回撤:+x%" noise; only a real pullback (<0) is rendered.
func TestSyncTradeAlertsNoPositiveDrawdown(t *testing.T) {
	e := &Engine{msgStore: data.NewMessageStore("")}
	high := liveAdvice("600004", "新高股", "止盈", "移动止盈触线")
	high.DrawdownPct = 0.64 // 旧实现会渲染成「回撤:+0.64%」
	down := liveAdvice("600005", "回撤股", "止盈", "移动止盈触线")
	down.DrawdownPct = -3.2
	e.syncTradeLevelAlerts("u123", []trading.PositionAdvice{high, down})

	list := e.msgStore.ListVisible("u123")
	if len(list) != 2 {
		t.Fatalf("应落库 2 条, got %d", len(list))
	}
	for _, it := range list {
		switch {
		case strings.Contains(it.ID, "600004"):
			if strings.Contains(it.Body, "回撤:") {
				t.Errorf("新高股正文不应渲染正值回撤: %s", it.Body)
			}
		case strings.Contains(it.ID, "600005"):
			if !strings.Contains(it.Body, "回撤:-3.20%") {
				t.Errorf("真实回撤应照常渲染: %s", it.Body)
			}
		}
	}
}
