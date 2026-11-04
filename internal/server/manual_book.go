// Package server — §2.12 手动维护账（2026-11-04，外脑化 B1' 批）。
// manual_book.go：POST /api/qmt/manual-positions 的处理与读数装配。
//
// 背景（一句话）：券商 11-05 起关闭 QMT 内置策略的本地文件读写与任何外部网络
// （AGENTS「通道前提变更」），桥断后**持仓/资金不再自动上报**。owner 改为在量仔里手工维护
// 持仓与可用资金，量仔据此继续出可执行的买卖信号卡（docs/PLAN_EXTERNAL_BRAIN_IMPL_20261103.md §2.12）。
// 本文件是这条链的输入端；输出端（三通道信号卡）在 web/src/utils/signalCard.js + SignalCardModal.jsx。
//
// 三条不许被"顺手优化"掉的边界：
//  1. **账号级 authMiddleware，刻意不落严格位**（§2.12 原文）。这是一张成员自己维护自己声称值的表，
//     挂 quant_trade/trading_admin 才能录，等于把"桥断了以后先把手上的票记进量仔"这条止血通道
//     一起锁进实盘权限面；但归属严格按 requestUserID，A 永远写不到 B 的账上
//     （与 §FIX-20261028-F01 读面纪律同源）。
//  2. **绝不进任何"必须新鲜"的硬闸**。手动账落在独立表 manual_positions / manual_account，
//     §M12-A 资金三态、risk.Gate 的 T+1 可卖量/集中度/买入纪律、VWAP 硬过滤 fail-close 的取数 SQL
//     一个字都没改、也读不到这张表 ⇒ "手敲一笔钱就解开自动买入的 fail-close"在结构上做不到。
//     为什么不给 real_positions/real_account 加 source 列：完整论证在 internal/store/manual_book.go 文件头，
//     一句话版本是"那两张表的 updated_at 会被硬闸读成刚刚回报过"。行为锁见 manual_book_test.go。
//  3. **请求体里的 user_id 一律不信**（§安全 T1 同口径）：归属只由登录态决定。
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// manualPositionsMaxRows 单次提交的最大持仓行数，超过即拒。
// 为什么要有这个上限：这是"一个人手敲的清单"，不是导入面。到了几百行就说明调用方坏了
// （脚本重放 / 前端把历史全量回灌），继续收下只会让下一次"全量"把真正在持的那几只冲掉。
const manualPositionsMaxRows = 300

// manualPositionItem 手动持仓的一行（body 形态，字段按 §2.12 与 H2：ts_code / qty / 成本可选）。
type manualPositionItem struct {
	TsCode    string  `json:"ts_code"`
	Name      string  `json:"name,omitempty"`
	Qty       *int    `json:"qty"`        // 指针：缺键与 0 不是一回事（缺键＝这行没填数量，拒收）
	CostPrice float64 `json:"cost_price"` // 可选：0＝未填，不参与任何判定
}

// manualBookReq POST /api/qmt/manual-positions 的请求体。
// positions / available_cash 用指针表达"这次没动"——H2 是**全量** upsert，
// 把"没填"读成"填了 0"会当场把 owner 的清单清空（那是本端点最坏的一次误写）。
type manualBookReq struct {
	Positions     *[]manualPositionItem `json:"positions"`      // nil＝本次不动持仓；[]＝清仓
	AvailableCash *float64              `json:"available_cash"` // nil＝本次不动资金
	TotalAsset    *float64              `json:"total_asset"`    // 可选（必须与 available_cash 同批）
	AsOf          string                `json:"asof"`           // 可选：缺省取服务端当前时刻（H2）
	// UserID 是**故意收下但永不采信**的字段：旧客户端/脚本可能带着它发。
	// 归属只认登录态（§安全 T1）；不报错也不采信，只在两者不一致时留一行日志——
	// 完全静默会让后来人以为"这个字段是有用的"，那才是下一次冒名的起点。
	UserID string `json:"user_id,omitempty"`
}

// handleManualPositionsPost POST /api/qmt/manual-positions（账号级 authMiddleware，不落严格位）。
// 语义：账号级**全量** upsert（按 ts_code 幂等）；同一份清单提交两次，读数逐字节不变。
func (s *Server) handleManualPositionsPost(w http.ResponseWriter, r *http.Request) {
	// 归属只由登录态决定：空身份直接拒（不是"落到运营账号"——那会变成跨账号写）。
	uid := requestUserID(r)
	if uid == "" {
		writeError(w, http.StatusForbidden, "无法确定账号")
		return
	}
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	var req manualBookReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	// 拼错字段名（postions / availableCash）必须当场失败：静默丢一行＝账错了但没人知道。
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.UserID != "" && req.UserID != uid {
		log.Printf("[manual-book] 请求体 user_id=%s 与登录账号 %s 不一致，按登录账号归属（§安全 T1：请求体自报身份一律不信）", req.UserID, uid)
	}
	if req.Positions == nil && req.AvailableCash == nil {
		// 什么都没带的提交一律拒。这条端点是"全量"语义，一次空提交回 200 会让调用方以为
		// "提交成功了"，而页面下一眼看到的仍是旧数——两端都以为对方生效，是假账的诞生方式。
		writeError(w, http.StatusBadRequest, "positions 与 available_cash 至少带一项（全量 upsert 不接受空提交）")
		return
	}
	asof, err := store.NormalizeManualAsOf(req.AsOf)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// ── 持仓：nil＝不动，非 nil＝全量替换（按 ts_code 幂等 upsert） ──
	written, deleted := 0, 0
	if req.Positions != nil {
		items := *req.Positions
		if len(items) > manualPositionsMaxRows {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("positions 行数 %d 超过上限 %d（这是手工清单，不是导入面）", len(items), manualPositionsMaxRows))
			return
		}
		rows := make([]store.ManualPosition, 0, len(items))
		for i, it := range items {
			if it.Qty == nil {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("第 %d 行 %s 缺 qty：手动持仓必须显式给股数（没有数量的行既算不出市值也出不了可执行信号）", i, it.TsCode))
				return
			}
			rows = append(rows, store.ManualPosition{
				TsCode:    strings.TrimSpace(it.TsCode),
				Name:      strings.TrimSpace(it.Name),
				Qty:       *it.Qty,
				CostPrice: it.CostPrice,
			})
		}
		n, del, err := db.PutManualPositions(uid, rows, asof)
		if err != nil {
			// 校验类失败（ErrInvalidManualPosition）映射 400，其余 500：
			// 400 让调用方知道"是你这份清单有问题，一条都没写进去"，而不是"服务坏了但也许写了一半"。
			if errors.Is(err, store.ErrInvalidManualPosition) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, 500, "save manual positions failed: "+err.Error())
			return
		}
		written, deleted = n, del
	}

	// ── 资金：available_cash 缺省＝不动；填了就必须 ≥0（含真 0，三态见 ManualAccountState） ──
	if req.AvailableCash != nil {
		total := 0.0
		if req.TotalAsset != nil {
			total = *req.TotalAsset
		}
		if err := db.PutManualAccount(uid, *req.AvailableCash, true, total, asof); err != nil {
			if errors.Is(err, store.ErrInvalidManualPosition) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, 500, "save manual cash failed: "+err.Error())
			return
		}
	} else if req.TotalAsset != nil {
		// 只带总资产、不带可用资金：拒。
		// total_asset 是"现金 + 市值"的合计，单独改它而不动 available_cash 会造出一个自相矛盾的行
		// （两个数怎么相加都对不上），而下游展示只能挑一个信——宁可拒，不许猜。
		writeError(w, http.StatusBadRequest, "total_asset 必须与 available_cash 一起提交（只改合计会留下对不上的资金行）")
		return
	}

	opslog.Audit("manual_book_set", uid, uid,
		fmt.Sprintf("asof=%s written=%d deleted=%d cash_set=%v", asof, written, deleted, req.AvailableCash != nil))

	// 回显刚写进去的那本账（而不是让前端再读一次）：录入人必须立刻看见"服务端认下来的这份账
	// 长什么样"，否则手敲错位数的第一现场就丢了。
	out, err := s.manualBookReadout(uid)
	if err != nil {
		writeError(w, 500, "read back manual book failed: "+err.Error())
		return
	}
	out["status"] = "ok"
	out["written"] = written
	out["deleted"] = deleted
	writeJSON(w, 200, out)
}

// manualBookReadout 装配该账号的手动账读数（**additive**：只新增键，不改任何既有字段的语义）。
//
// 为什么单独抽一个函数：这句话（「手动维护，更新于 …」）会出现在两个地方（保存后的回显、
// 持仓页的读面）。一处拼字符串、一处拼另一处，迟早漂成"页面说更新于 09:30、库里最新行是 10:00"。
func (s *Server) manualBookReadout(uid string) (map[string]interface{}, error) {
	db := s.realDB()
	if db == nil {
		// 没接账本库＝"这份账读不到"，刻意不回 present=true 的空壳：
		// 空壳会让前端把"读不到"渲染成"手动账是空的"，两者对操作人的含义完全不同。
		return map[string]interface{}{"present": false, "unavailable": true, "note": ""}, nil
	}
	positions, err := db.ManualPositionsForUser(uid)
	if err != nil {
		return nil, err
	}
	acc, err := db.ManualAccountForUser(uid)
	if err != nil {
		return nil, err
	}
	asof, err := db.ManualAsOf(uid)
	if err != nil {
		return nil, err
	}
	if positions == nil {
		positions = []store.ManualPosition{} // 空表显式回 []，不回 null：前端要能区分"没有"与"没读到"
	}
	return map[string]interface{}{
		"present":     len(positions) > 0 || acc.Present,
		"source":      store.ManualSourceLabel,
		"positions":   positions,
		"account":     acc,
		"asof":        asof,
		"positions_n": len(positions),
		"cash_state":  manualCashState(acc),
		// note 是**给人看的那句**：读数面一律标"手动维护"，绝不让它长得像柜台回报
		// （§2.12：手动数据不得冒充柜台真值）。
		"note": manualNote(asof),
		// not_used_by 明写这条读数**不参与**哪些判据。放在响应里而不是只放在文档里，
		// 是因为前端/脚本会照响应写显示逻辑；一个不知道边界的调用方才会把"手动资金"当"可用资金"。
		"not_used_by": []string{"§M12-A 资金三态 fail-close", "risk.Gate T+1 可卖量/集中度/买入纪律", "VWAP 硬过滤 fail-close"},
	}, nil
}

// manualNote 手动账的统一话术；asof 为空＝从没录过，这句话就不该出现在页面上。
func manualNote(asof string) string {
	if asof == "" {
		return ""
	}
	return "手动维护，更新于 " + asof
}

// manualCashState 资金三态的字符串形态（none / unfilled / zero / positive）。
// 为什么要有这个键：Present/Filled 两个布尔很容易被下游折错（"Filled=false 就当 0 显示"
// 是 H-4 那族事故的原形），这里把三种态一次性分成三个字符串，前端只需要判一个值。
func manualCashState(acc store.ManualAccountState) string {
	if !acc.Present {
		return "none" // 从没录过手动资金
	}
	if !acc.Filled {
		return "unfilled" // 有行，但这份清单没填可用资金
	}
	if acc.Account.AvailableCash <= 0 {
		return "zero" // owner 明确说"可用资金就是 0"（真 0，不是未知）
	}
	return "positive"
}
