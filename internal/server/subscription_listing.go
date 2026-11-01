// subscription_listing.go — §SUBSCRIPTION（2026-11-01）上市状态问询腿：给判据补上"这只票上没上市"这一半证据。
//
// 为什么需要它：深市新股申购直接用正股代码段（00/30），光看代码无法与真实委托区分，
// 必须有第二证据。而这份证据来自外部接口（IPO 日历 + 全市场可交易列表），
// 所以本文件的三条设计约束都是围绕"别把外源压力搬进热路径、别把取不到当成判定"：
//
//	① 只有段表命中才来查这里（server/qmt.go 的读面按 trading.ClassifySubscription 的顺序调用），
//	   一次全市场列表是 5500 只的 HTTP；
//	② 进程内 TTL 缓存（30 分钟），失败也缓存（5 分钟后才再试）——否则委托列表每翻一页都打外源；
//	③ **只用正面证据说"未上市"**：IPO 日历明示 ListStatus=U、或上市日期晚于今天，才回 known=true/listed=false；
//	   "可交易列表里没有这个码"绝不构成未上市证据（退市的票也不在列表里）。任何其它情形一律
//	   known=false ⇒ 判据落回"不判定、不标注、不跳过撤单"（owner 裁决 8 的底线方向）。
//
// English: listing-status probe. Only positive IPO evidence may mark a code as unlisted;
// absence from the tradable list is never proof. Cached with TTL so the order-list read path
// cannot hammer the external sources.
package server

import (
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/trading"
)

const (
	// subListingTTL 成功刷新后的缓存时长：申购判定一天内不会因分钟级抖动改变结论，30 分钟够用。
	subListingTTL = 30 * time.Minute
	// subListingFailTTL 失败后的重试冷却：拿不到就短时间不再拿，宁可按"不知道"处理（不标注）。
	subListingFailTTL = 5 * time.Minute
)

// subListingProbe 是 trading.ListingSource 的生产实现，数据源在调用时现取（s.dc 由
// SetCoordinator 在启动后期注入，所以这里持有 Server 反向引用而不是构造时的指针快照）。
type subListingProbe struct {
	s *Server

	mu           sync.Mutex
	tradable     map[string]bool          // 全市场可交易裸码集合；nil=本次没拿到
	ipo          map[string]data.IPOEvent // 裸码 → IPO 日历事件
	loadedAt     time.Time                // 上次成功刷新时刻
	failedAt     time.Time                // 上次失败时刻（用于冷却）
	refreshCount int                      // 观测用：整个进程生命周期查了几次外源
}

// IsListed 返回 (已上市?, 有证据?)。三态语义见文件头第 ③ 条，known=false 是本批的保守默认值。
func (p *subListingProbe) IsListed(code string) (bool, bool) {
	if p == nil || p.s == nil {
		return false, false // 装配缺失＝数据不足 ⇒ 不判定
	}
	bare := trading.BareCode(code)
	if bare == "" {
		return false, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureFreshLocked()

	// 证据一：出现在全市场可交易列表里 ⇒ 已上市（正面证据，可直接否决申购判定）
	if p.tradable != nil && p.tradable[bare] {
		return true, true
	}
	// 证据二：IPO 日历明示状态或上市日期
	if ev, ok := p.ipo[bare]; ok {
		status := strings.TrimSpace(strings.ToUpper(ev.ListStatus))
		if status == "L" {
			return true, true
		}
		if status == "U" {
			return false, true
		}
		// 状态串不认识时退回日期比较：listing_date 晚于今天 ⇒ 未上市（YYYYMMDD 字典序即时间序）
		if d := strings.TrimSpace(ev.ListingDate); len(d) == 8 {
			today := cntime.In(time.Now()).Format("20060102")
			if d > today {
				return false, true
			}
			if d <= today {
				return true, true
			}
		}
	}
	// 两条正面证据都没有 ⇒ 不知道。绝不因为"可交易列表里没有"就断未上市。
	return false, false
}

// ensureFreshLocked 按 TTL 决定是否重取两份数据源。调用方已持锁。
// 两个源各自独立：任一失败不污染另一份已拿到的数据（tradable 拿到、ipo 空，仍可据 tradable 判"已上市"）。
func (p *subListingProbe) ensureFreshLocked() {
	now := time.Now()
	if p.triedRecently(now) {
		return
	}
	ok := false
	if p.s.market != nil {
		if list, err := p.s.market.GetStockList(); err == nil && len(list) > 0 {
			set := make(map[string]bool, len(list)*2)
			for k := range list {
				// 键形态历史上是 symbol（sh600519）或 code（600519），这里两种都归一，
				// 免得源那边换个写法就让整个"已上市"证据失效（退化成"不知道"）。
				if bare := trading.BareCode(k); bare != "" {
					set[bare] = true
				}
			}
			p.tradable = set
			ok = true
		}
	}
	if p.s.dc != nil {
		if events := p.s.dc.GetAllIPOCalendar(); len(events) > 0 {
			m := make(map[string]data.IPOEvent, len(events))
			for _, e := range events {
				if bare := trading.BareCode(e.Code); bare != "" {
					m[bare] = e
				}
			}
			p.ipo = m
			ok = true
		}
	}
	p.refreshCount++
	if ok {
		p.loadedAt = now
		return
	}
	p.failedAt = now // 两个源都没拿到 ⇒ 进冷却，期间一律"不知道"
}

// triedRecently 判断是否仍在缓存/冷却窗口内（避免每次读委托列表都打外源）。
func (p *subListingProbe) triedRecently(now time.Time) bool {
	if !p.loadedAt.IsZero() && now.Sub(p.loadedAt) < subListingTTL {
		return true
	}
	return !p.failedAt.IsZero() && now.Sub(p.failedAt) < subListingFailTTL
}

// subscriptionSource 懒构造问询腿（进程内单例，委托列表与后续任何读面共用同一份缓存）。
func (s *Server) subscriptionSource() trading.ListingSource {
	s.subListingOnce.Do(func() { s.subListing = &subListingProbe{s: s} })
	return s.subListing
}
