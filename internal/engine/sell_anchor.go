// sell_anchor.go — 卖出裁决「移动止盈锚点（持仓期最高价）」的跨重启持久化。
// §M10（2026-09-22 修复批）覆盖 paper 账本（json 文件）；
// §N-7（2026-09-22 晚间批）补上 live 账本（回写 real_positions.highest_price）。
//
// 背景：卖出裁决的移动止盈锚点（持仓期最高价）三本账来源不同——
//   - live：real_positions.highest_price（账本持久化，§M5 已有只增保护）；
//     ⚠ 该列历史上只有「建仓价/成交价」两个来源（对账快照 open_price、成交回报
//     成交价），期间最高价从未写回，live 的移动止盈锚点重启后必然退回建仓价——
//     §N-7（本批）起由 persistLiveSellAnchors 把内核自抬出的锚点回写账本，
//     本文件的 json 持久化只覆盖 paper 账本，别把两者混为一谈。
//   - report：ExecLog.HighestPrice（账本持久化）；
//   - paper：账本无该字段，裁决内核靠状态机从 EntryPrice「每轮自抬」
//     （signalctl.sellState.HighPrice 纯内存）→ 进程重启锚点清零，高点回落成
//     成本价，"先涨够 tp 再回撤 pb"的移动止盈条件永不满足——重启前涨过 15%
//     的持仓，重启后移动止盈永不触发（docs/AUDIT_FULL_UAT_20260921C.md M10）。
//
// 修复：引擎侧按账号（per-acctDir 装配天然隔离）维护 账号→代码→锚点 嵌套表，
// 原子落盘 <acctDir>/paper_sell_anchors.json（data.AtomicWrite，唯一临时名+fsync）。
// runPaperUnifiedJudge 裁决前取锚点注入探针（SellInput.HighPrice，内核以此播种），
// 轮末按本轮结果同步回表并落盘：
//   - 锚点 = max(旧锚点, 本轮有效现价)，与内核自抬口径一致；
//   - 平仓（不在本轮探针持仓集）即删——重新入场从零起算，与 PruneSellStates 同生命周期；
//   - dataDir 为空（纯内存测试路径）自动退回旧的纯内存语义，行为零变化。
//
// 范围说明：signalctl sellState 内的观察窗/结算栅格进度仍是纯内存（重启丢窗口）。
// 该残留为已接受口径——窗口只影响裁定节奏、不产生误平仓，锚点恢复后移动线在重启后
// 首个有效价轮即重新锁线；窗口状态账本化牵动 signalctl 内核，留待 F11 拆分批次。
//
// English: persists the trailing-stop high anchor across process restarts (the kernel's
// anchor was memory-only, so a restart reset it to entry price and the trailing take-profit
// could never fire again). Paper: atomic JSON per account data dir, empty dataDir keeps the
// old memory-only semantics. Live (§N-7): the raised anchor is written back to the ledger
// column real_positions.highest_price, which previously only ever carried entry/fill prices.
package engine

import (
	"encoding/json"
	"log"
	"os"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

// paperSellAnchor 取指定账号/代码的 paper 移动止盈锚点（§M10）。
// 返回 0=无锚点（新入场或纯内存模式），裁决内核回退用成本价播种（原语义）。
// English: returns the persisted trailing-stop anchor for account/code (0 = none → kernel seeds from entry).
func (e *Engine) paperSellAnchor(account, code string) float64 {
	e.paperAnchorMu.Lock()
	defer e.paperAnchorMu.Unlock()
	e.loadPaperAnchorsLocked()
	return e.paperAnchors[account][code]
}

// syncPaperSellAnchors 一轮纸面裁决结束后同步锚点表并原子落盘（§M10）：
// held=本轮探针持仓代码集（平仓即删），verdicts=本轮有效裁决（有效现价抬高锚点）。
// 表确有变更才写盘——5s 轮 + 主循环双轮高频调用下避免无谓 IO。
// English: syncs the anchor table after a paper judge round (prune exited codes, raise with
// valid round prices) and writes the JSON file atomically only when something changed.
func (e *Engine) syncPaperSellAnchors(account string, held map[string]bool, verdicts []sellRoundVerdict) {
	e.paperAnchorMu.Lock()
	defer e.paperAnchorMu.Unlock()
	e.loadPaperAnchorsLocked()
	cur := e.paperAnchors[account]
	if cur == nil {
		if len(held) == 0 {
			return // 该账号无持仓也无历史锚点：无事可做
		}
		cur = make(map[string]float64, len(held))
		e.paperAnchors[account] = cur
	}
	changed := false
	for code := range cur {
		if !held[code] {
			delete(cur, code) // 平仓即删：重新入场从零起算（与裁决状态同生命周期）
			changed = true
		}
	}
	for _, v := range verdicts {
		if v.Price <= 0 || v.Price <= cur[v.TsCode] {
			continue
		}
		cur[v.TsCode] = v.Price // 本轮有效现价抬高锚点（与内核 HighPrice 自抬同口径）
		changed = true
	}
	if len(cur) == 0 {
		delete(e.paperAnchors, account)
	}
	if !changed {
		return
	}
	e.savePaperAnchorsLocked()
}

// loadPaperAnchorsLocked 懒加载锚点文件（首次访问读一次；调用方持 paperAnchorMu）。
// 文件缺失/损坏不致命：读失败按空表起步并记日志，绝不清空内存表。
func (e *Engine) loadPaperAnchorsLocked() {
	if e.paperAnchors == nil {
		e.paperAnchors = make(map[string]map[string]float64)
	}
	if e.paperAnchorLoaded {
		return
	}
	e.paperAnchorLoaded = true
	if e.paperAnchorPath == "" {
		return // 纯内存模式（测试/未配 dataDir）：锚点退回旧的每轮自抬语义
	}
	raw, err := os.ReadFile(e.paperAnchorPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[sell-anchor] §M10 锚点文件读取失败（本轮按无锚起步）: %v", err)
		}
		return
	}
	var persisted map[string]map[string]float64
	if err := json.Unmarshal(raw, &persisted); err != nil {
		log.Printf("[sell-anchor] §M10 锚点文件解析失败（忽略文件内容）: %v", err)
		return
	}
	// 合并持久化锚点进内存表：同键双向取较高者——锚点单调不降，与内核「每轮自抬」口径
	// 一致；即便盘上是一份滞后旧拷贝，也只会被现值覆盖抬高、绝不把高点拉回（防重启倒退）。
	for acct, m := range persisted {
		dst := e.paperAnchors[acct]
		if dst == nil {
			dst = make(map[string]float64, len(m))
			e.paperAnchors[acct] = dst
		}
		for code, high := range m {
			if high > dst[code] {
				dst[code] = high
			}
		}
	}
}

// savePaperAnchorsLocked 全表原子落盘（调用方持 paperAnchorMu；路径空=纯内存直接跳过）。
func (e *Engine) savePaperAnchorsLocked() {
	if e.paperAnchorPath == "" {
		return
	}
	raw, err := json.Marshal(e.paperAnchors)
	if err != nil {
		log.Printf("[sell-anchor] §M10 锚点表序列化失败: %v", err)
		return
	}
	ensureParentDir(e.paperAnchorPath)
	if err := data.AtomicWrite(e.paperAnchorPath, raw, 0644); err != nil {
		log.Printf("[sell-anchor] §M10 锚点文件原子写入失败: %v", err)
	}
}

// persistLiveSellAnchors §N-7（2026-09-22 傍晚批复验）live 账本移动止盈锚点回写：
// 把裁决内核本轮自抬出来的持仓期最高价写进 real_positions.highest_price。
//
// 为什么必须做（owner 裁决 12=回写账本）：live 侧探针的播种值就是该列
// （sell_shadow.go 的 sellProbeRow.HighPrice = p.HighestPrice），而该列此前只有三个写入源
// ——券商对账快照的 open_price、成交回报成交价、以及 §M5 的只增 CASE，**期间最高价从来没有
// 任何写入路径**。内核的 HighPrice 是纯内存「播种 + 每轮自抬」，进程一重启锚点就退回建仓价：
// 涨过 tp（默认 15%）再回撤的仓位，移动止盈线永远锁不上，本批主题「静默失效」的典型样本。
//
// 为什么不开 json：paper 用 <acctDir>/paper_sell_anchors.json 是因为纸面账本根本没有可承载
// 锚点的列；live 的账本就是 sqlite，再落一份文件等于给同一条规则两份真相（重启后谁覆盖谁、
// 手工清账时谁失效都说不清）。落库后 §M5/N-6 的只增 CASE 天然就是它的合并规则。
//
// 与 §N-6 在同一条 upsert 上的协调（这就是两条并给一个人的原因）：本函数走**独立的单调
// UPDATE**（RaiseRealPositionHigh，WHERE highest_price < ?），不与对账 upsert 争同一写路径；
// 而对账 upsert 里 highest_price 的 CASE 是 max(excluded, 现值)，券商快照的 open_price 只可能
// 抬高、不可能把已回写的锚点拉回。两条路径都单调不降 ⇒ 谁先谁后都不丢高点。
//
// English: §N-7 — writes the kernel's in-process trailing-stop anchor back to
// real_positions.highest_price (live's ledger is the source of truth for the seed, and that
// column previously only ever carried entry/fill prices, so a restart reset the anchor).
// Uses a separate monotonic UPDATE, coordinated with §N-6 by both sides being only-up.
func (e *Engine) persistLiveSellAnchors(account string, positions []store.RealPosition) {
	e.mu.RLock()
	realDB := e.realStore
	e.mu.RUnlock()
	if realDB == nil || len(positions) == 0 {
		return // 未接实盘账本（纯内存/测试路径）或无持仓：无事可做
	}
	ctl := e.SignalCtl()
	for i := range positions {
		p := positions[i]
		// 内核锚点：本轮无有效价的持仓状态仍在（sellState 只在跃迁时更新），取到的是历史高点。
		anchor := ctl.SellHighAnchor(signalctl.ChannelLive, account, p.TsCode)
		if anchor <= p.HighestPrice {
			continue // 账本已不低于内核锚点：无变更不写库（5s 轮高频，避免无谓 IO 与 updated_at 抖动）
		}
		raised, err := realDB.RaiseRealPositionHigh(account, p.TsCode, anchor)
		if err != nil {
			// 写失败只影响「下次重启后的播种值」，不影响本轮裁决与执行：降级为日志、绝不打断卖出主链。
			log.Printf("[sell-anchor] §N-7 live 锚点回写失败（不影响本轮裁决） %s 锚点=%.4f: %v", p.TsCode, anchor, err)
			continue
		}
		if !raised {
			continue // 并发轮已抢先抬高（WHERE 只增守卫拦下）：正常竞态，不记日志
		}
		// 留痕限流：单边上涨行情下每轮都可能创新高，只按码节流输出，写入本身不限流。
		code := p.TsCode
		before := p.HighestPrice
		opslog.OncePer("live-anchor-raise:"+account+"|"+code, time.Minute, func() {
			log.Printf("[sell-anchor] §N-7 live 锚点已回写账本 %s 账本最高 %.4f → %.4f（重启后移动止盈线仍在此锚上锁线）",
				code, before, anchor)
		})
	}
}
