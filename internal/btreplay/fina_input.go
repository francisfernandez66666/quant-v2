// 本文件实装 §B2（owner 裁决 2026-09-26「因子回放要不要喂财务数据：要」）：
// 因子规则回放此前只喂日K（ruleEvalAdapter.Trigger 构造的 StockMarketData 里 Fina 恒 nil），
// 而实盘同一套 Evaluate 吃的是 md.Fina——含财务成分的已启用规则，回放量出的胜率不是实盘机制的
// 读数（AUDIT_20260925EVE ⑦：注释宣称"同一套打分口径"只对价量成立）。
// 现在回放按**判定日**喂财务：可见性/新鲜度都用 strategy_engine 的共享裁决（与实盘同一份代码，
// 不是抄一份再各改各的），绝不引入实盘没有的未来函数——这正是当初"修法前置：先定 ann_date
// 口径（B7）"的原因，B7 已先行落地。
// English: §B2 — the factor-rule replay now feeds financials into the live Evaluate path, resolved
// per judgment day with the same point-in-time/freshness predicates the live scorer uses (shared
// functions, not a copied loop), so replay numbers answer "what the live mechanism would have done".
package btreplay

import (
	"fmt"
	"log"
	"time"

	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategy_engine"
)

// finaProvider 回放财务输入源：逐股惰性加载 fina_indicator 序列（一轮回放每票至多一次 SQL），
// 按判定日裁决可见期（§B7 同函数）与停用闸（§M-7 同谓词、asof=判定日收盘时刻）。
// 读数（判定票数/吃到可见票数/…）只随 §B2-FINA 一行出门禁日志，不参与判红——
// 与 §MINUTE-K 覆盖率读数同一姿势：前提与数字同屏，但降级是可见而非阻断。
// English: lazy per-stock financial series source for replays; resolves visibility/freshness per
// judgment day via the shared predicates, and keeps readout counters for the §B2 log line only.
type finaProvider struct {
	db     *store.DB
	rows   map[string][]store.FinaRow // ts_code → 财务序列（含空序列，避免重复查）
	failed map[string]bool            // 查库失败的票：一轮内不再砸库，按缺失计入
	// status 是"按票归集"的读数状态机：一只票在区间里既有可见期又有缺失期时，
	// "它到底吃到过财报没有"以吃到过为准（visible 粘滞）——读数回答的是"这一轮有多少票
	// 在裸奔（价量打分）"，不是逐日分布。缺这个粘滞规则的话，判定日恰好从披露日前开始的票
	// 会被永久记成"没吃到"，读数谎报裸奔规模（[[feedback-test-assertion-self-defeat]] 家族）。
	status    map[string]int  // ts_code → 当前归集档（st* 常量）
	futureHit map[string]bool // 该票本轮挡过"披露日在未来"的期（证明 PIT 闸真的在挡东西）
}

// 归集档常量（status 值域）。
const (
	stVisible = 1 // 至少一个判定日吃到可见财报（粘滞档）
	stNoFina  = 2 // 库里没有这只票的财报（真缺失）
	stStale   = 3 // 有财报但按判定日过旧停用（§M-7 在体）
	stMissing = 4 // 有财报但判定日尚无可见期（全部在未来）——后续吃到会被 stVisible 覆盖
)

// newFinaProvider 装配回放财务输入源（db 为 nil 时返回 nil：不喂、读数行如实报"未装配"）。
func newFinaProvider(db *store.DB) *finaProvider {
	if db == nil {
		return nil
	}
	return &finaProvider{db: db, rows: map[string][]store.FinaRow{}, failed: map[string]bool{},
		status: map[string]int{}, futureHit: map[string]bool{}}
}

// mark 按票归集读数：visible 粘滞（吃到过就是吃到过），其余档后到者覆盖。
func (p *finaProvider) mark(tsCode string, st int) {
	cur, ok := p.status[tsCode]
	if !ok {
		p.status[tsCode] = st
		return
	}
	if cur == stVisible && st != stVisible {
		return
	}
	p.status[tsCode] = st
}

// visibleFina 返回该股在判定日 day **应喂给实盘打分口径**的财务数据；nil=那天按缺失计入。
// 三段裁决全部走共享函数：查库失败留痕（§N-5）、ann_date>判定日回退上一期（§B7）、
// 过旧停用（§M-7）。
// English: the financial row the live scoring path would have used on judgment day, or nil when that
// day treats it as missing; every decision goes through the shared strategy_engine predicates.
func (p *finaProvider) visibleFina(tsCode string, day time.Time) *strategy_engine.FinancialData {
	asof := day.Format("20060102")
	rows, ok := p.rows[tsCode]
	if !ok {
		if p.failed[tsCode] {
			return nil
		}
		r, err := p.db.FinaHistory(tsCode)
		if err != nil {
			// §N-5 同款姿势：查库失败≠真没有，单票 log 一次留痕，本轮回放按缺失计入。
			p.failed[tsCode] = true
			log.Printf("§B2-FINA %s 财务指标查库失败（本轮回放该票按缺失计入，不代表真的没有财报）: %v", tsCode, err)
			return nil
		}
		p.rows[tsCode] = r
		rows = r
	}
	// 该股在 fina_indicator 里一条记录都没有：按缺失计入，状态计数供 §B2-FINA 收尾读数点名。
	if len(rows) == 0 {
		p.mark(tsCode, stNoFina)
		return nil
	}
	// 取「判定日已可见」的最新一期（§B7 同一谓词）；skipped>0 = 有公告日晚于判定日的
	// 报告期被跳过（未来函数防护腿），整票打 futureHit 标记供读数区分"真没有"与"还没披露"。
	fina, skipped := strategy_engine.LatestVisibleFina(rows, asof)
	if skipped > 0 {
		p.futureHit[tsCode] = true
	}
	// 全部报告期都晚于判定日（理论上少见，配合上面 skipped 计数暴露口径异常）→ 按缺失计入。
	if fina == nil {
		p.mark(tsCode, stMissing)
		return nil
	}
	// §M-7 停用闸在回放侧同样生效，但 asof 是判定日而非今天：
	// 闸的语义是"那天还能不能看到够新的财报"，与实盘同一谓词（strategy_engine.ReportStale）。
	if stale, _ := strategy_engine.ReportStale(fina, day); stale {
		p.mark(tsCode, stStale)
		return nil
	}
	p.mark(tsCode, stVisible)
	return fina
}

// String §B2-FINA 单行读数（缺 provider 时也要能出门，"未装配"本身就是信息）。
// 查库失败的票不在 status 值域里（visibleFina 提前 return），按 failed 集合单列。
func (p *finaProvider) String() string {
	if p == nil {
		return "§B2-FINA 回放财务输入：未装配（本轮因子规则没有财务腿可吃）"
	}
	var visible, noFina, stale, missing int
	for _, st := range p.status {
		switch st {
		case stVisible:
			visible++
		case stNoFina:
			noFina++
		case stStale:
			stale++
		case stMissing:
			missing++
		}
	}
	return fmt.Sprintf("§B2-FINA 回放财务输入：因子判定 %d 只（吃到可见 %d、真缺失 %d、尚无可见期 %d、过旧停用 %d、查库失败 %d；PIT 挡过未来期的票 %d）",
		len(p.status)+len(p.failed), visible, noFina, missing, stale, len(p.failed), len(p.futureHit))
}

// finaScoped §B2 财务输入的逐股装配点（与 minuteMACDScoped 同一手法：能接的接、不能接的跳过）。
// 形态规则不实现它——pattern.seriesFromKLines 本就只有价量字段，形态成分不消费 Fina（喂了也没人读）。
type finaScoped interface {
	setFinaScope(p *finaProvider, tsCode string)
}

// applyFinaScope 把财务输入源与当前票的 ts_code 注入适配器：
// 主适配器之外，兜底档战法的**兄弟清单**也一并注入——兜底互斥回查走的是兄弟的裸 Trigger，
// 兄弟若停留在上一只票的注入态，就是 §0925EVE-W3-J(B6) 锤过的跨股串台在财务腿上的复发。
// tsCode 传空＝本轮没有来源（provider 为 nil 或调用方拿不到 ts_code），适配器按"不喂"处理。
// English: injects the financial source + current ts_code into the adapter and, because the live
// fallback cross-check probes siblings' raw triggers, into the fallback peers as well (the B6
// cross-stock contamination lesson applied to the financial leg).
func (o *Options) applyFinaScope(ad adapter, tsCode string) {
	inject := func(a adapter) {
		if fs, ok := a.(finaScoped); ok {
			fs.setFinaScope(o.finaSrc, tsCode)
		}
	}
	inject(ad)
	if isFallbackTier(ad) {
		for _, p := range o.fallbackPeers {
			inject(p)
		}
	}
}
