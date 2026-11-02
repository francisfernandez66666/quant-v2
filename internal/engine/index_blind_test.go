// index_blind_test.go — §INDEX-BLIND（2026-11-02）"缺的是哪一维"必须一路走到读数面。
//
// 这组用例钉的是同一条链的五个落点，全部来自 11-02 现网那笔账（全天封顶半仓、
// 而"掉的是大盘指数"这句话在任何一块面板上都写不出来）：
//  1. sizingContextFor 的**字面量必备键**——本仓被"手搓字面量漏一个字段"打死过整个进程
//     （§OBS-GUARD 的资金心跳），这里漏的不是 ctrl 而是维度与来源，症状是静默；
//  2. 资金帧三栏（missing_key_dims / index_source / index_stale）真的写进去；
//  3. 决策快照的「原因」点名维度（表结构不加列，走 reason 承载）；
//  4. 盲区档位判据（0/1/2/3）与"陈旧但真实 ≠ 盲区"的分形；
//  5. §DYN-BLIND 的节流是"只读判定 + 唯一写入点"成对形状（§FIX-20261028-F02 的纪律）。
//
// 另加一枚反向锁：短窗缓存命中时不许把陈旧读数洗成"新鲜"（旧实现一律报 false）。
package engine

import (
	"bytes"
	"log"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
)

// TestSizingCtxLiteralCarriesAllKeys 装配腿：sizingContextFor 里的字面量必须带上四个新键。
// 为什么读源码而不是只测行为：真实 T 轮的读数与心跳读的字段子集不同，
// 只测一条路径会放过"另一条路径漏填"（§163 那条锁最初就是被逐字相同的前缀骗过的）。
func TestSizingCtxLiteralCarriesAllKeys(t *testing.T) {
	raw, err := os.ReadFile("funds_today.go")
	if err != nil {
		t.Fatalf("读 funds_today.go: %v", err)
	}
	body := functionBody(string(raw), "func (e *Engine) sizingContextFor(")
	if body == "" {
		t.Fatalf("取不到 sizingContextFor 函数体（锚漂了，这条用例会变摆设）")
	}
	for _, key := range []string{"MS:", "Missing:", "MissingDims:", "IndexSource:", "IndexStale:", "IndexPct:"} {
		if !strings.Contains(body, key) {
			t.Fatalf("sizingCtx 字面量漏键 %s（漏了就是静默丢维度/丢来源）:\n%s", key, body)
		}
	}
}

// TestFundsFrameCarriesDimsAndSource 行为腿：盲区态发布出来的帧必须带 dims 与来源。
// 用**预置的定权缓存**（10s 短窗）而不是真打行情：本用例测的是"写没写到帧上"，
// 让上游可达性参与进来就变成"测网络"（夜跑假红的同族）。
func TestFundsFrameCarriesDimsAndSource(t *testing.T) {
	f := newFundsFix(t)
	f.e.mu.Lock()
	if f.e.sizingCtxCache == nil {
		f.e.sizingCtxCache = map[string]sizingCtx{}
	}
	f.e.sizingCtxCache[f.uid] = sizingCtx{
		MS: 0.50, Missing: true, MissingDims: []string{signalctl.MissingDimIndexChange},
		IndexSource: "", IndexStale: false, IndexPct: 0, At: time.Now(),
	}
	f.e.mu.Unlock()

	f.e.publishSignalFunds(f.uid, map[string]*data.StockInfo{})
	st := f.e.tLiveFor(f.uid)
	snap := st.fundsSnapshot()
	if snap == nil {
		t.Fatalf("资金帧没落出来（发布点早退）")
	}
	if !snap.MissingKey {
		t.Fatalf("missing_key_input 没写进帧")
	}
	if strings.Join(snap.MissingKeyDims, ",") != "index_change" {
		t.Fatalf("missing_key_dims 没写进帧：%v", snap.MissingKeyDims)
	}
	if snap.IndexSource != "" {
		t.Fatalf("没取到点位时不该编一个来源：%q", snap.IndexSource)
	}

	// 非盲区但靠后备腿：dims 必须清空、来源必须报出来（否则卡片分不出"齐"与"退"）。
	// 钟必须往前推过 10s 去重窗——帧本身是读改写，早退会让"清空"这件事根本没发生，
	// 用例就会因为发布点没跑而假绿（这条一开始就是这样红的，是真行为不是测试噪音）。
	f.e.SetClock(func() time.Time { return f.now.Add(15 * time.Second) })
	f.e.mu.Lock()
	f.e.sizingCtxCache[f.uid] = sizingCtx{
		MS: 0.81, Missing: false, IndexSource: data.IndexSourceSina, IndexStale: false, At: time.Now(),
	}
	f.e.mu.Unlock()
	f.e.publishSignalFunds(f.uid, map[string]*data.StockInfo{})
	snap2 := f.e.tLiveFor(f.uid).fundsSnapshot()
	if len(snap2.MissingKeyDims) != 0 {
		t.Fatalf("非盲区态还带着旧维度清单（陈旧账会一直挂着）：%v", snap2.MissingKeyDims)
	}
	if snap2.IndexSource != data.IndexSourceSina {
		t.Fatalf("后备腿来源没报出来：%q", snap2.IndexSource)
	}
}

// TestDynDecisionReasonNamesBlindDim 决策快照的「原因」要点名维度。
// 现网今天 84 行写着同一句"已达动态目标仓位"，看不出那批其实是"因为看不见大盘而被封顶"。
func TestDynDecisionReasonNamesBlindDim(t *testing.T) {
	db, sig, d := decisionFixture(t)
	d.Qty = 0 // 跳过态（被目标仓位拦住）
	d.CappedBy = "target"
	d.SkipReason = "已达动态目标仓位"
	d.MissingKeyInput = true
	d.MissingKeyDims = []string{signalctl.MissingDimIndexChange}

	e := &Engine{}
	e.realStore = db
	e.SetMembers([]string{"u_a"})
	e.persistDynBuyDecision(sig, d)

	rows, err := db.SignalDecisionsForDay("u_a", data.TradingDayDate(d.At))
	if err != nil || len(rows) != 1 {
		t.Fatalf("快照没落下来：%v err=%v", rows, err)
	}
	r := rows[0]
	if !r.MissingKeyInput {
		t.Fatalf("missing_key_input 位丢了")
	}
	if !strings.Contains(r.Reason, "关键读数缺失") || !strings.Contains(r.Reason, "index_change") {
		t.Fatalf("原因里点不出维度（值班只能猜）：%q", r.Reason)
	}
	if !strings.Contains(r.Reason, "已达动态目标仓位") {
		t.Fatalf("原有结论被覆盖了（应追加，不是替换）：%q", r.Reason)
	}
}

// TestBlindAlertCodeTiers 告警档位判据：0 不适用 / 1 齐 / 2 兜底中 / 3 真盲区。
// 分档的根据是处置方向：2 该查"谁在退"、3 才是"钱正被挡住"——折成一档就会天天响。
func TestBlindAlertCodeTiers(t *testing.T) {
	cases := []struct {
		name string
		ctx  sizingCtx
		want int64
	}{
		{"没取到过点位＝不适用", sizingCtx{}, 0},
		{"东财新鲜且齐", sizingCtx{IndexSource: data.IndexSourceEastMoney}, 1},
		{"后备腿接管（仍真实）", sizingCtx{IndexSource: data.IndexSourceSina}, 2},
		{"券商侧给点位（主源在位）", sizingCtx{IndexSource: data.IndexSourceBroker}, 1},
		{"东财回退陈旧值", sizingCtx{IndexSource: data.IndexSourceEastMoney, IndexStale: true}, 2},
		{"真盲区（缺大盘指数）", sizingCtx{Missing: true, MissingDims: []string{"index_change"}}, 3},
		{"盲区优先于兜底位", sizingCtx{Missing: true, IndexSource: data.IndexSourceSina}, 3},
	}
	e := &Engine{}
	for _, c := range cases {
		if got := e.blindAlertCode(c.ctx); got != c.want {
			t.Fatalf("%s：应 %d，实得 %d", c.name, c.want, got)
		}
	}
}

// TestBlindLogThrottleIsOneWritePoint §DYN-BLIND 的节流必须是"只读判定 + 唯一写入点"。
// 判据里顺手推基线＝第二次判定永远为假（§FIX-20261028-F02 就是因为基线无条件前进，
// 资金帧在 5s 节拍下几乎不再发）。
func TestBlindLogThrottleIsOneWritePoint(t *testing.T) {
	e := &Engine{}
	now := time.Now()
	dims := []string{"index_change"}

	if !e.blindLogDue(dims, now) {
		t.Fatalf("首次盲区应当出声")
	}
	// 只调判定、不调写入：下一次判定必须**仍然**为真（基线没被推走）
	if !e.blindLogDue(dims, now.Add(time.Second)) {
		t.Fatalf("blindLogDue 是只读的——它不许推进基线")
	}
	e.markBlindLogged(dims, now)
	if e.blindLogDue(dims, now.Add(10*time.Second)) {
		t.Fatalf("60s 内重复出声（打分 5s + 心跳 10s + T 参数各调一次，会刷屏）")
	}
	// 维度组合变了必须立刻出声：从"缺大盘指数"变成"缺市场状态"是另一件事
	if !e.blindLogDue([]string{"market_state"}, now.Add(20*time.Second)) {
		t.Fatalf("维度变化没被识别成新事件")
	}
	e.markBlindLogged([]string{"market_state"}, now.Add(20*time.Second))
	if e.blindLogDue([]string{"market_state"}, now.Add(50*time.Second)) {
		t.Fatalf("同一组维度在节流窗内不该再响")
	}
	// 超窗后同一组维度要能再次出声（盲区持续 3 小时与持续 30 秒必须看得出来）
	if !e.blindLogDue([]string{"market_state"}, now.Add(90*time.Second)) {
		t.Fatalf("过窗后没恢复出声能力")
	}
}

// TestBlindLineNamesDimension §DYN-BLIND 正文必须带维度与来源（不是光秃"缺失"）。
// 这条走真实函数（捕获日志），因为"打了但打的是废话"只有看正文才测得出来。
func TestBlindLineNamesDimension(t *testing.T) {
	f := newFundsFix(t)
	f.e.mu.Lock()
	if f.e.sizingCtxCache == nil {
		f.e.sizingCtxCache = map[string]sizingCtx{}
	}
	// 缓存里放一份**非盲区**的旧帧，强制 sizingContextFor 重算（TTL 过期）
	f.e.sizingCtxCache[f.uid] = sizingCtx{MS: 0.8, At: time.Now().Add(-time.Hour)}
	f.e.idxQuoteSrc = ""
	f.e.idxQuotePct = math.NaN()
	f.e.mu.Unlock()

	var buf bytes.Buffer
	origOut, origFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	f.e.sizingContextFor(f.uid)
	log.SetOutput(origOut)
	log.SetFlags(origFlags)

	out := buf.String()
	if !strings.Contains(out, "§DYN-BLIND") {
		t.Fatalf("盲区没出声（今天这条路径一行日志都不打）：\n%s", out)
	}
	if !strings.Contains(out, "dims=") {
		t.Fatalf("§DYN-BLIND 正文缺 dims（等于白打）：\n%s", out)
	}
	if !strings.Contains(out, "index_source=") {
		t.Fatalf("§DYN-BLIND 正文缺来源：\n%s", out)
	}
}

// TestIndexCacheHitKeepsStaleFlag 短窗缓存命中时不许把陈旧读成新鲜。
// 旧实现固定报 false ⇒ "用 10 分钟前的点位"这件事在 30s 窗内被洗白。
func TestIndexCacheHitKeepsStaleFlag(t *testing.T) {
	e := &Engine{}
	now := time.Now()
	today := data.TradingDayDate(now)
	e.mu.Lock()
	e.idxQuoteDay, e.idxQuotePct, e.idxQuoteAt = today, 1.23, now.Add(-5*time.Second)
	e.idxQuoteStale, e.idxQuoteSrc = true, data.IndexSourceEastMoney
	e.mu.Unlock()
	pct, day, stale := e.indexChangePctNow(now)
	if pct != 1.23 || day != today {
		t.Fatalf("命中缓存应直接回缓存值：%.2f %s", pct, day)
	}
	if !stale {
		t.Fatalf("缓存里那份是回退值，命中时必须照实报 stale")
	}
}

// functionBody 从源码里按"函数起始锚"取出**括号配平**的函数体。
// 与 §163 的 awk 区间同理：按整文件 grep 判键会被另一处合法写法放过（真实 T 轮的前缀逐字相同）。
func functionBody(src, anchor string) string {
	i := strings.Index(src, anchor)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	open := strings.Index(rest, "{")
	if open < 0 {
		return ""
	}
	depth := 0
	for j := open; j < len(rest); j++ {
		switch rest[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[:j+1]
			}
		}
	}
	return rest
}
