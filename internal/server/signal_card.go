// Package server — §2.12 信号卡（2026-11-04，外脑化 B1' 批）。
// signal_card.go：把"一条交易信号 + 引擎当日的定权结果"装配成**给人照着下单的可执行卡片**。
//
// 为什么要这一步：桥 11-05 断后没有柜台链路，量仔的输出端只剩"给人看的卡"
// （docs/PLAN_EXTERNAL_BRAIN_IMPL_20261103.md §2.12）。一张卡必须凑齐
// {股票名, 代码, 方向, 股数, 参考价, 策略/理由, 置信/档位, 有效期}，
// **缺股数＝不可执行**；缺哪个字段就在卡上点名"数据缺失"，不许猜——
// 这张卡是要被人照着下单的，一个猜出来的数字比一条空缺贵得多
// （同族先例：§N-12d 不许把触发价塞进"现价"列，因为客户会拿它做交易判断）。
//
// 股数从哪里来（关键的一条，别在下一批被改成"从正文里正则抠"）：
//   - `data.MessageItem`（消息中心/SSE 的那条 item）**根本不带股数/置信度**；
//   - `fixSignal` 里那个 Price 是触发价或现价，也不是数量；
//   - 引擎每轮把逐信号的动态仓位快照写进 `signal_decisions`（§DECISION-TODAY：day-keyed、
//     含 qty/notional/capped_by/stage/reason），那才是"这一笔买多少"的唯一在册读数。
//   - 所以卡片按 (code, 战法键, 当日) 去 join 这本快照；join 不到就是**缺**。
//     绝不退回"从 Body 文本里抠一个数"：抠出来的很可能是现价被当成股数，凭空造出一笔敞口。
//
// 装配点选在**读面**（GET /api/signals 每条 item 上 additive 挂 `card` 键），不开新路由：
// §4 G11 只允许本批新增 POST /api/qmt/manual-positions 一条路由（契约台账 +1 行）；
// 卡片只在"有人看"时才需要，读面现算＝不存在第二本状态要去同步。
// 三通道去重与开关在设备侧（web/src/utils/signalCard.js），去重键就是这里的 signal_id。
package server

import (
	"fmt"
	"log"
	"strings"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// 卡片八字段的固定名。前端 web/src/utils/signalCard.js 的 CARD_FIELDS 与本清单**逐字对齐**
// （由 web/src/__tests__/signal_card_20261104.test.jsx 锁住）。
// 为什么要钉死：字段名漂一个字，"缺什么"就会显示成"什么都不缺"——那是最危险的方向。
const (
	CardFieldStockName = "股票名"
	CardFieldCode      = "代码"
	CardFieldSide      = "方向"
	CardFieldQty       = "股数"
	CardFieldPrice     = "参考价"
	CardFieldReason    = "策略/理由"
	CardFieldConf      = "置信/档位"
	CardFieldExpiry    = "有效期"
)

// CardMissingLabel 缺字段的统一说法：只写「字段名 + 数据缺失」，绝不写成 0 或"—"。
// 为什么必须带上字段名：一条光秃秃的"数据缺失"无法让人知道缺的是股数还是价格，
// 而这两个的可执行含义完全不同（缺价还能按市价执行，缺股数就根本不知道该买多少）。
const CardMissingLabel = "数据缺失"

// cardExpiryPolicyNote 有效期的口径说明（**政策**，不是读数）：交易信号当日有效、收盘作废。
// 为什么把这句话随卡片下发：有效期是八字段里唯一"人定的"那个，
// 卡上只印一个时刻而不印口径，读的人会把"收盘作废"误当成"这条信号在收盘后被撤销"。
const cardExpiryPolicyNote = "本交易日收盘（15:00）作废"

// SignalCard 一张可执行信号卡（八字段 + 缺字段点名 + 可执行判定）。
type SignalCard struct {
	StockName   string   `json:"stock_name"`     // 股票名（空串＝没有，见 missing_fields）
	Code        string   `json:"code"`           // 代码
	Side        string   `json:"side"`           // 方向：买入/卖出（其它值判缺，不猜多空）
	Qty         int      `json:"qty"`            // 股数；0＝**没有这个读数**（不等于"0 股"）
	Price       float64  `json:"price"`          // 参考价
	PriceKind   string   `json:"price_kind"`     // live/trigger/none：价格的语义必须显式（§N-12d）
	Reason      string   `json:"reason"`         // 策略/理由
	Confidence  string   `json:"confidence"`     // 置信/档位（总分 + 提醒级别，不做二次折算）
	ExpiresAt   string   `json:"expires_at"`     // 有效期时刻（口径见 expiry_note）
	ExpiryNote  string   `json:"expiry_note"`    // 有效期的**口径**那句话
	SignalID    string   `json:"signal_id"`      // 三通道共用的去重键
	Missing     []string `json:"missing_fields"` // 缺哪些字段（点名的清单，前端直接渲染）
	Executable  bool     `json:"executable"`     // 八字段齐 ⇒ 可执行；缺任一 ⇒ false
	SourceLabel string   `json:"source_label"`   // 固定"量仔建议"：卡上说明这不是柜台指令
}

// cardQty 从当日定权快照取这一笔的股数，返回 (股数, 是否有读数)。
//   - 查不到行 / qty<=0 ⇒ 没有读数；
//   - stage 不是 evaluated/placed ⇒ **没有读数**：rejected/skipped 的 qty 是"本来想给但没给"，
//     把它印成可执行卡的数量，等于让人去买一笔引擎明确拒绝过的仓位。
func cardQty(dec *store.SignalDecision) (int, bool) {
	if dec == nil || dec.Qty <= 0 {
		return 0, false
	}
	switch strings.TrimSpace(dec.Stage) {
	case "evaluated", "placed":
		return dec.Qty, true
	default:
		return 0, false
	}
}

// signalIDForCard 卡片的去重键：优先引擎的 signal_id（与 signal_decisions/signal_records 同源），
// 缺失时退回 `code|战法|生成时刻`。
// 为什么必须有兜底键而不是留空：留空的卡片在设备侧无法去重，同一条信号会在
// 页面弹两次、手机弹两次——去重键缺一个的代价是"告警刷屏"，而那正是把真告警训练成噪音的成因。
func signalIDForCard(signalID, code, strategy, generatedAt string) string {
	if id := strings.TrimSpace(signalID); id != "" {
		return id
	}
	return strings.TrimSpace(code) + "|" + strings.TrimSpace(strategy) + "|" + strings.TrimSpace(generatedAt)
}

// BuildSignalCard 装配一张卡。qtyKnown=false 表示这笔没有在册的数量读数（见 cardQty）；
// expiry 由调用方按交易日给出（收盘后传空串＝无法给出有效期，如实进缺失清单）。
//
// 入参刻意是"已经取到的读数"而不是整个 fixSignal：卡片只该在**有数**的地方画格子。
// 直接把结构体序列化出去，会让"空串"和"没有这个读数"在 JSON 里长成同一个样子（H-4 同族缺陷的形状）。
func BuildSignalCard(signalID, code, name, side string, price float64, priceKind, reason, confidence,
	expiry string, qty int, qtyKnown bool) SignalCard {
	card := SignalCard{
		StockName:   strings.TrimSpace(name),
		Code:        strings.TrimSpace(code),
		Side:        normalizeCardSide(side),
		Price:       price,
		PriceKind:   strings.TrimSpace(priceKind),
		Reason:      strings.TrimSpace(reason),
		Confidence:  strings.TrimSpace(confidence),
		ExpiresAt:   strings.TrimSpace(expiry),
		ExpiryNote:  cardExpiryPolicyNote,
		SignalID:    signalID,
		SourceLabel: "量仔建议",
	}
	// 数量：只在调用方确实给了数量（qtyKnown）且为正时才印——
	// "没给"和"给 0"都必须落到下面的缺失清单，不许印成 0 股的建议。
	// English: only a known positive quantity is printed; absent/0 goes to the missing list.
	if qtyKnown && qty > 0 {
		card.Qty = qty
	}
	// 缺失清单按字段逐个收集（顺序即前端补填顺序），空字段不许被折成"看起来完整"的卡片。
	// English: missing fields are collected one by one so an incomplete card never renders as complete.
	var missing []string
	add := func(label string) { missing = append(missing, label) }
	if card.StockName == "" {
		add(CardFieldStockName)
	}
	if card.Code == "" {
		add(CardFieldCode)
	}
	if card.Side == "" {
		add(CardFieldSide)
	}
	if card.Qty <= 0 {
		add(CardFieldQty)
	}
	// 参考价：只有 >0 才算有读数。price_kind=none（取不到价）时保留为缺失——
	// 不许把 0 印成"参考价 ¥0.00"，那是"这支票免费"的意思。
	if card.Price <= 0 {
		add(CardFieldPrice)
	}
	if card.Reason == "" {
		add(CardFieldReason)
	}
	if card.Confidence == "" {
		add(CardFieldConf)
	}
	if card.ExpiresAt == "" {
		add(CardFieldExpiry)
	}
	card.Missing = missing
	// 可执行判定：缺**任一**字段即不可执行（§2.12「字段缺一不可」，缺股数只是最致命的那个）。
	// 刻意不只判股数：只判股数的话，缺方向/缺代码的卡也会被标成"可执行"，
	// 而那种卡在页面上与真卡长得一模一样。
	card.Executable = len(missing) == 0
	return card
}

// normalizeCardSide 方向词表：只认两组**已知枚举**的字面值
// （柜台/消息中心用 买入/卖出，引擎内部 Signal.Action 用 buy/sell），其它一律判缺。
// 为什么不做模糊归一（从"减仓/清仓/做多"里猜方向）：每加一条猜测就多一处"猜错方向"的机会，
// 而方向是花真钱的字段——与 §SIDE-AUTH-2（下单方向必填、只接受 买入/卖出）同一条纪律。
func normalizeCardSide(side string) string {
	switch strings.TrimSpace(side) {
	case "买入", "buy":
		return "买入"
	case "卖出", "sell":
		return "卖出"
	default:
		return ""
	}
}

// cardExpiryAt 有效期时刻：今日（北京时间）15:00:00。
// 收盘之后返回**空串**（→ 卡面显示「有效期 数据缺失」），不返回"明天 15:00"：
// 收盘后这条信号属于哪个交易日已经不确定（盘后回放/次日集合竞价都会改变结论），
// 把它说成"明天还有效"就是伪造有效期。
func cardExpiryAt() string {
	now := cntime.Now()
	if now.Format("15:04:05") >= "15:00:00" {
		return ""
	}
	return now.Format("2006-01-02") + " 15:00:00"
}

// attachSignalCards 给 /api/signals 的每条信号挂上可执行卡片（原地填充）。
//
// 数量取数：当日（北京时间）`signal_decisions` 里同 (代码, 战法键) 的最新一条 evaluated/placed 行。
// 一次读整本当日快照再在内存里配对，**不是**每条信号查一次库：现网实录单轮 271 条信号，
// 逐条查库会把这个读面打成 N+1（§SIGBATCH 同族教训）。
// 读不到库/读失败 ⇒ 一律按"没有数量读数"出卡（卡面标数据缺失），不静默跳过整张卡：
// 卡片是这条链的输出端，它坏了必须看得见，不能表现为"信号页没有卡"。
func (s *Server) attachSignalCards(uid string, items []fixSignal) {
	if len(items) == 0 {
		return
	}
	expiry := cardExpiryAt()
	qtyByKey, qtyOK := s.cardQtyIndex(uid)
	if !qtyOK {
		log.Printf("[server] §2.12 当日定权快照不可得：本轮信号卡一律标「股数 数据缺失」（不猜数量）用户=%s", uid)
	}
	for i := range items {
		qty, has := 0, false
		if qtyOK {
			// 键同时容战法名与战法池类型：signal_decisions.strategy_key 落的是战法键（fac_1 等），
			// 而 fixSignal.Strategy 是战法**名**——两处不同源，先按 StrategyType/StrategyID 试，
			// 再退回 Strategy 名；三把都对不上就是没有（不猜）。
			for _, k := range cardQtyKeys(items[i]) {
				if v, ok := qtyByKey[k]; ok {
					qty, has = v, true
					break
				}
			}
		}
		card := BuildSignalCard(
			signalIDForCard(items[i].SignalID, items[i].Code, items[i].Strategy, items[i].GeneratedAt),
			items[i].Code, items[i].Name, items[i].Action,
			items[i].Price, items[i].PriceKind,
			cardReason(items[i]), cardConfidence(items[i]),
			expiry, qty, has,
		)
		c := card
		items[i].Card = &c
	}
}

// cardQtyIndex 读当日定权快照并归并成 `纯代码|战法键 → 股数`。返回 (索引, 是否可用)；
// 不可用（没接库/查询失败）时调用方必须把数量判成缺失（绝不当成"数量为 0"）。
func (s *Server) cardQtyIndex(uid string) (map[string]int, bool) {
	db := s.realDB()
	if db == nil {
		return nil, false
	}
	day := cntime.DayCompactOf(cntime.Now())
	items, err := db.SignalDecisionsForDay(uid, day)
	if err != nil {
		log.Printf("[server] §2.12 当日定权快照读取失败（信号卡按缺数量出）day=%s 用户=%s: %v", day, uid, err)
		return nil, false
	}
	out := make(map[string]int, len(items))
	// SignalDecisionsForDay 已按 at 倒序 ⇒ 第一次写进键的就是**最新**一条，后来的只补空键不回改。
	for i := range items {
		d := items[i]
		qty, ok := cardQty(&d)
		if !ok {
			continue // rejected/skipped/零数量：不占键位，让同日更新的行来填
		}
		k := cardQtyKey(d.Code, d.Strategy)
		if _, exists := out[k]; !exists {
			out[k] = qty
		}
	}
	return out, true
}

// cardQtyKeys 一条信号在定权索引里的候选键，顺序与**写入侧** signalctl.StrategyKeyOf 一致
// （StrategyType → StrategyID → 战法名）。
// 为什么要按同一顺序试而不是只试一把：定权快照里落的是"规范战法键"（dragon / fac_1 / …），
// 而展示侧同时带着战法**名**；只按名字找，凡是 StrategyType 非空的战法都对不上号，
// 表现是"信号一直在、卡上永远缺股数"——那看起来像功能没做，其实是键空间不同源。
func cardQtyKeys(f fixSignal) []string {
	code := pureCardCode(f.Code)
	keys := make([]string, 0, 3)
	for _, k := range []string{f.StrategyType, f.StrategyID, f.Strategy} {
		if strings.TrimSpace(k) == "" {
			continue
		}
		keys = append(keys, cardQtyKey(code, k))
	}
	return keys
}

// cardQtyKey 索引键的**唯一**拼法（读写两侧都经这里，防止一边带后缀一边不带）。
func cardQtyKey(code, strategy string) string {
	return pureCardCode(code) + "|" + strings.TrimSpace(strategy)
}

// pureCardCode 去掉 .SH/.SZ/.BJ 后缀：`signal_decisions.code` 落的是纯 6 位码
// （engine.pureTsCode 写入），而展示侧的 Code 带后缀。
// 为什么不在库里补后缀统一：那张表已经是逐信号快照的在册真源且被决策页在用，
// 为一张卡片改它的键空间，代价是历史快照全对不上。
func pureCardCode(tsCode string) string {
	c := strings.TrimSpace(tsCode)
	for _, suf := range []string{".SH", ".SZ", ".BJ"} {
		if i := strings.LastIndex(c, suf); i > 0 {
			return c[:i]
		}
	}
	return c
}

// cardReason 卡面"策略/理由"：战法名 + 四维里唯一带理由文本的那一维（§0929DIM 的 D1Desc）。
// 为什么只取 D1Desc：这一列本身就是"战法算出的维度理由，无则退回信号 Reason"，
// 是全列表里唯一保证非空且语义为"为什么"的文本；自己再拼一遍只会拼出两个来源不同的东西。
func cardReason(f fixSignal) string {
	if strings.TrimSpace(f.Strategy) == "" {
		return strings.TrimSpace(f.D1Desc)
	}
	if strings.TrimSpace(f.D1Desc) == "" {
		return f.Strategy
	}
	return f.Strategy + "｜" + f.D1Desc
}

// cardConfidence 卡面"置信/档位"：总分 + 提醒级别原样拼（strong/observe/mute 是既有档位词，
// 本函数不重新定档、不改阈值——判档只在 scoreToRemindLevel 那一处）。
// 分数与档位**两个都要**：只有分数没有档位，读的人得自己回忆阈值；只有档位没有分数，
// 两条 observe 的信号在他眼里就成了一模一样的东西。
func cardConfidence(f fixSignal) string {
	if f.TotalScore <= 0 || f.RemindLevel == "" {
		return ""
	}
	return fmt.Sprintf("%.1f 分 · %s", f.TotalScore, f.RemindLevel)
}
