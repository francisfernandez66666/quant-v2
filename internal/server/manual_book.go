// Package server — §2.12 手动维护账（2026-11-04 外脑化 B1' 批）+ §MANUAL-BOOK-UNIFY（2026-11-06 晚批）。
// manual_book.go：POST /api/qmt/manual-positions 的处理与读数装配。
//
// 背景（一句话）：券商 11-05 起关闭 QMT 内置策略的本地文件读写与任何外部网络
// （AGENTS「通道前提变更」），桥断后**持仓/资金不再自动上报**。owner 改为在量仔里手工维护
// 持仓与可用资金，量仔据此继续出可执行的买卖信号卡（docs/PLAN_EXTERNAL_BRAIN_IMPL_20261103.md §2.12）。
// 本文件是这条链的输入端；输出端（三通道信号卡）在 web/src/utils/signalCard.js + SignalCardModal.jsx。
//
// 11-06 晚批改了两件事（owner 裁决逐条对应）：
//   - 裁决 2/3/4：写面新增**增量改账**形态（`adjust`），四个动作（加仓/减仓/止盈/清仓）+ 改账新增 +
//     直接改资金都在这一条端点上落；与既有**全量清单**形态（`positions`）互斥，全量语义原样保留给脚本面。
//   - 裁决 1：手动读数**改为喂买卖硬闸**。旧边界"绝不进任何必须新鲜硬闸"已作废，换成两条可用性前置
//     （asof 属当前交易日 + 未被柜台接管覆盖），判据本体在 store.ManualBookViewFor / store.SelectBookSources。
//
// 三条不许被"顺手优化"掉的边界：
//  1. **账号级 authMiddleware，刻意不落严格位**（§2.12 原文）。这是一张成员自己维护自己声称值的表，
//     挂 quant_trade/trading_admin 才能录，等于把"桥断了以后先把手上的票记进量仔"这条止血通道
//     一起锁进实盘权限面；但归属严格按 requestUserID，A 永远写不到 B 的账上
//     （与 §FIX-20261028-F01 读面纪律同源）。
//  2. **绝不写进 real_positions / real_account**。改判后的说法是"参与判定靠读侧显式选源，不靠伪造柜台
//     时间戳"：柜台账只由回报与真实成交推进，是"冲突时以柜台为准"这条规则能被机器判出来的前提。
//     行为锁见 manual_book_test.go 与 store/manual_adjust_test.go:TestAdjustNeverWritesCounterTables。
//  3. **请求体里的 user_id 一律不信**（§安全 T1 同口径）：归属只由登录态决定。
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"quant-trading-v2/internal/cntime"
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

// manualAdjustItem 一次增量改账（§MANUAL-BOOK-UNIFY 2026-11-06，owner 裁决 2/3/4）。
// 形状与实仓 tab 那四个按钮一一对应：op ∈ new|add|reduce|tp|close|set_cash。
type manualAdjustItem struct {
	Op         string   `json:"op"`
	TsCode     string   `json:"ts_code"`
	Qty        *int     `json:"qty"`   // 指针：缺键与 0 不是一回事（持仓类动作缺数量＝拒，不许猜）
	Price      float64  `json:"price"` // 本笔成交价（持仓类动作必填）
	Cash       *float64 `json:"available_cash"`
	TotalAsset *float64 `json:"total_asset,omitempty"` // 2026-11-11：set_cash 可显式填总资产
	UserID     string   `json:"user_id,omitempty"`     // 收下但永不采信（§安全 T1）
}

// manualBookReq POST /api/qmt/manual-positions 的请求体。
// positions / available_cash 用指针表达"这次没动"——H2 是**全量** upsert，
// 把"没填"读成"填了 0"会当场把 owner 的清单清空（那是本端点最坏的一次误写）。
//
// adjust 与 positions **互斥**（§MANUAL-BOOK-UNIFY）：两种语义同时提交，必然有一个被吞，
// 而这里被吞掉的那一个可能是"我刚改的那笔减仓"。宁可 400，不许猜哪个优先。
type manualBookReq struct {
	Positions     *[]manualPositionItem `json:"positions"`      // nil＝本次不动持仓；[]＝清仓
	Adjust        []manualAdjustItem    `json:"adjust"`         // 非空＝增量改账（一次一条）
	AvailableCash *float64              `json:"available_cash"` // nil＝本次不动资金（全量形态）
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
	if req.Positions == nil && req.AvailableCash == nil && len(req.Adjust) == 0 {
		// 什么都没带的提交一律拒。这条端点是"全量"语义，一次空提交回 200 会让调用方以为
		// "提交成功了"，而页面下一眼看到的仍是旧数——两端都以为对方生效，是假账的诞生方式。
		writeError(w, http.StatusBadRequest, "positions / available_cash / adjust 至少带一项（全量 upsert 不接受空提交）")
		return
	}
	if len(req.Adjust) > 0 && (req.Positions != nil || req.AvailableCash != nil || req.TotalAsset != nil) {
		// 互斥判据：全量清单与增量改账同时提交，被吞的那一个恰好是"我刚点的那笔减仓"。
		writeError(w, http.StatusBadRequest, "adjust（增量改账）与 positions/available_cash（全量清单）互斥：一次只提交一种语义")
		return
	}
	if len(req.Adjust) > 1 {
		// 刻意只接一条。多条要在一个事务里跑完才有"批次"意义，而 AdjustManualBook 自己管事务；
		// 把"部分成功"留在钱路径上，比少一个批量入口坏得多。前端四个按钮本来就是一次一条。
		writeError(w, http.StatusBadRequest, fmt.Sprintf("adjust 一次只允许一条（收到 %d 条）：批量改账请逐笔提交，每笔都要能单独看见结果", len(req.Adjust)))
		return
	}
	asof, err := store.NormalizeManualAsOf(req.AsOf)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// ── 增量改账（§MANUAL-BOOK-UNIFY 裁决 2/3/4）：与全量形态互斥，走同一条端点 ──
	if len(req.Adjust) == 1 {
		item := req.Adjust[0]
		if item.UserID != "" && item.UserID != uid {
			// 改账同样只认登录态：请求体自报身份不报错也不采信，只留一行日志点名
			// 「有人试着不是自己」（§安全 T1）。完全静默会让后来人以为这个字段有用。
			// English: body-declared user_id is never trusted; log it, keep the session identity.
			log.Printf("[manual-book] adjust 请求体 user_id=%s 与登录账号 %s 不一致，按登录账号归属（§安全 T1）", item.UserID, uid)
		}
		// 把 HTTP 形状翻成 store 请求：qty 是指针，缺键必须保持「没填」而不是折成 0；
		// asof 已由 NormalizeManualAsOf 判过，User 只取登录态（请求体自报的 UserID 上面已丢弃）。
		// English: translate the HTTP shape without collapsing "missing" into zero.
		adj := store.ManualAdjustReq{Op: strings.TrimSpace(item.Op), TsCode: item.TsCode, Price: item.Price, AsOf: asof, User: uid}
		if item.Qty != nil {
			adj.Qty = *item.Qty
		}
		adj.Cash = item.Cash
		adj.TotalAsset = item.TotalAsset
		res, err := db.AdjustManualBook(uid, adj, cntime.DayOf(cntime.Now()))
		if err != nil {
			if errors.Is(err, store.ErrInvalidManualAdjust) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, 500, "adjust manual book failed: "+err.Error())
			return
		}
		opslog.Audit("manual_book_adjust", uid, uid,
			fmt.Sprintf("op=%s code=%s 本笔=%d@%.2f 改后持仓=%d 资金=%.0f→%.0f seeded_from=%s/%s asof=%s",
				res.Op, res.TsCode, res.DealQty, res.Price, res.Qty, res.Cash, res.CashAfter,
				res.SeededFrom, res.CashSeeded, asof))
		out, err := s.manualBookReadout(uid)
		if err != nil {
			writeError(w, 500, "read back manual book failed: "+err.Error())
			return
		}
		out["status"] = "ok"
		out["adjust"] = res
		writeJSON(w, 200, out)
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
	view, viewErr := db.ManualBookViewFor(uid, cntime.DayOf(cntime.Now()))
	if viewErr != nil {
		// 可用性读数读不到 ⇒ 两维都按"不可用"报（前端因此不会把手动账画成当前来源），但绝不留痕为零。
		log.Printf("[server] §MANUAL-BOOK 手动账可用性读取失败（读数面按不可用处理）user=%s: %v", uid, viewErr)
		view = store.ManualBookView{Day: cntime.DayOf(cntime.Now())}
	}
	flows, flowErr := db.ManualFlowsForUser(uid, cntime.DayOf(cntime.Now()))
	if flowErr != nil {
		log.Printf("[server] §MANUAL-BOOK 改账流水读取失败（flows 键按缺失处理，不冒充当天没改过）user=%s: %v", uid, flowErr)
	}
	return map[string]interface{}{
		"present":     len(positions) > 0 || acc.Present,
		"source":      store.ManualSourceLabel,
		"positions":   positions,
		"account":     acc,
		"asof":        asof,
		"positions_n": len(positions),
		"cash_state":  manualCashState(acc),
		// §MANUAL-BOOK-UNIFY（2026-11-06 裁决 1/5）可用性三态上屏：
		// usable_today＝asof 属当前交易日；taken_over_at＝最近一次被柜台接管；
		// cash_usable / positions_usable 是**两维独立**的最终结论（读侧真正会不会用它）。
		"usable_today":     view.CashUsable || view.PositionsUsable,
		"cash_usable":      view.CashUsable,
		"positions_usable": view.PositionsUsable,
		"taken_over_at":    view.TakenOverAt,
		// note 是**给人看的那句**：读数面一律标"手动维护"，绝不让它长得像柜台回报
		// （手动数据不是柜台证据，哪怕它现在会参与判定）。
		"note": manualNote(asof),
		// used_by / unavailable_when 明写这条读数**参与**哪些判据、在什么情况下**不参与**。
		// 放在响应里而不是只放在文档里，是因为前端/脚本会照响应写显示逻辑；
		// 一个不知道边界的调用方才会把"手动资金"当"柜台余额"，也才会把"今天没用它"读成"这本账是空的"。
		"used_by": []string{
			"§M12-A 资金三态（柜台不可信时回落手动可用资金）",
			"risk.Gate T+1 可卖量持仓基线与当日改账买入扣减",
			"risk.Gate 集中度/板块/概念/持仓数分母与持仓行",
			"动态仓位与买卖推送（engine.accountBookFor）",
			"持仓页实仓表与可用资金读数",
		},
		"unavailable_when": []string{
			"柜台该维仍在报数（资金 30 分钟窗内 / 持仓快照属当前交易日）——以柜台为准",
			"asof 不属于当前交易日",
			"已被柜台接管且 asof 不晚于接管时刻",
		},
		// not_used_by 只留**真正不读手动账**的那一条：VWAP 硬过滤吃的是行情成交额/量，与账本无关。
		// 保留这个键是为了旧调用方形状不炸（有测试按它存在性判），但内容必须诚实。
		"not_used_by": []string{"VWAP 硬过滤（取行情成交额/量，不取账本资金）"},
		"flows":       flows,
	}, nil
}

// manualNote 手动账的统一话术；asof 为空＝从没录过，这句话就不该出现在页面上。
func manualNote(asof string) string {
	if asof == "" {
		return ""
	}
	return "手动维护，更新于 " + asof
}

// markManualTakenOver 柜台回报**成功落库之后**给该账号的手动账打接管指针（owner 2026-11-06 裁决 5）。
//
// 三条边界，逐条都有测试盯着：
//  1. 只在落库成功的分支调用。回报失败也打标记＝把"没回来的账"说成"回来了"，
//     后果是手动账当场变成读不到而没有任何报警（读侧判据是 asof 晚于接管时刻）；
//  2. 不删行、不改 asof——接管指针只回答"这本账有没有被柜台证明过一次"，账本身是证据；
//  3. 打标失败只留痕不阻断回报链：回报是主链，接管标记是旁路；
//     把旁路故障升级成主链 500 会让网关无限重推一份已经落库的好数据。
func markManualTakenOver(db *store.DB, uid, source string) {
	if db == nil || uid == "" {
		return
	}
	at := store.ManualNow()
	n, err := db.MarkManualTakenOver(uid, at)
	if err != nil {
		log.Printf("[manual-book] 接管标记写入失败（不阻断回报链，下次回报会再打一次）user=%s source=%s: %v", uid, source, err)
		return
	}
	if n > 0 {
		log.Printf("[manual-book] 柜台回报恢复，手动账已标接管：user=%s source=%s 打标=%d 行 at=%s（行与流水一律保留）", uid, source, n, at)
	}
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
