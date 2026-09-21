// sell_anchor.go — §M10（2026-09-22 修复批）paper 账本移动止盈锚点的跨重启持久化。
//
// 背景：卖出裁决的移动止盈锚点（持仓期最高价）三本账来源不同——
//   - live：real_positions.highest_price（账本持久化，§M5 已有保护）；
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
// English: persists the paper-book trailing-stop high anchor across process restarts
// (the kernel's anchor was memory-only, so a restart reset it to entry price and the
// trailing take-profit could never fire again). Atomic JSON per account data dir;
// empty dataDir keeps the old memory-only semantics.
package engine

import (
	"encoding/json"
	"log"
	"os"

	"quant-trading-v2/internal/data"
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
