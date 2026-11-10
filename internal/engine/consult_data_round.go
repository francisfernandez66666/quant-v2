// consult_data_round.go — §CONSULT-DATARICH（2026-11-10）A 期：股票咨询「补数轮」。
//
// 形态：主数据块（B 期）拼好后，先用一次短调用问模型"回答这个问题还缺哪些字段"，
// Go 侧解析严格 JSON、只认白名单里的键，再用**既有取数腿**补取，追加进同一个数据块
// （因而自动落在 ⟦DATA⟧ 边界内、自动进反幻觉白名单，审计一行都不用改），最后正式作答。
//
// 四条硬边界（缺一条这条腿就会从"锦上添花"变成"主回答的故障源"）：
//  1. fail-open：超时/解析不出/取数失败/预算只够一次/开关关闭 ⇒ 一律返回空串＝今天的行为；
//     连 panic 都兜住（只读观测腿不许有停机权限，与 §OBS-GUARD 同一条纪律）。
//  2. 预算留位：A 轮与主回答各消耗一次咨询调用与一次总调用，剩余不足两次就整轮跳过——
//     否则 A 轮把额度吃掉、主回答撞 ErrBudgetExceeded 回 429，"只增不减"当场破产。
//  3. 白名单外一律丢弃：模型幻觉出的"龙虎榜/融资余额/北向资金"直接扔，**绝不**为 A 轮新接数据源。
//  4. 不造第二把尺子：股票名单走 consultCodesInMessage（与主块同一份解析），
//     资金流历史走 flowHistFetch（做 T 因子那条腿同一个闭包），均线走 consultMA。
//
// English: optional supplement round for stock consult. It asks the model which whitelisted
// fields are missing, fetches them through existing legs, and appends them inside the same
// ⟦DATA⟧ boundary so the anti-hallucination whitelist covers them with zero audit changes.
// Every failure path (timeout, bad JSON, fetch failure, insufficient budget, switch off, panic)
// returns an empty string, i.e. exactly today's behavior; the budget guard reserves room for the
// main answer so the extra round can never turn it into a 429.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

// consultDataRoundTimeout A 轮 LLM 调用的独立超时上限。
// 咨询入口 handleConsult 是**同步一次性**返回（无 SSE），所以这段时间是用户净多等的时长：
// 取 8s 而不是方案初稿的 15s——15s 叠加主回答本身的生成时间会把一次问答推到半分钟级。
// 到点即放弃补数、直接用 B 期数据作答（fail-open），不重试。
// English: independent cap for the supplement call; the consult endpoint is synchronous, so this
// is pure added user-visible latency — 8s, not 15s, and expiry simply skips the round.
const consultDataRoundTimeout = 8 * time.Second

// consultDataRoundMaxFields 单轮最多补取的字段数（模型多要也只给这么多，控外呼与 prompt 体积）。
const consultDataRoundMaxFields = 5

// consultSupplementFields A 轮字段白名单的**唯一真源**：提示词里的可选清单由它生成，
// 解析时的准入判定也由它生成——两处各写一份，就会出现"提示词允许要、Go 侧不认"的静默丢弃。
// 第一期只放两个既有取数腿能拿到的字段；industry_peers / north_flow 刻意不在册（无现成接口，
// 声明不可得比新接一个源便宜得多，也不会把咨询变成新的行情采集入口）。
// English: single source of truth for the supplement whitelist; both the prompt's menu and the
// parser's admission test are generated from it so they can never drift apart.
var consultSupplementFields = []struct {
	key  string
	desc string
}{
	{"moneyflow_5d", "近5个交易日逐日主力净流入（问「主力连续几天流入/流出」「资金趋势」时需要）"},
	{"kline_60d", "中期均线 MA20/MA60 与近60日最高/最低（问「中期趋势」「位置高低」时需要）"},
}

// consultDataRoundSystemPrompt 由白名单生成 A 轮的短系统提示词（只要 JSON，不要正文）。
func consultDataRoundSystemPrompt() string {
	var sb strings.Builder
	sb.WriteString("你是股票咨询的数据补齐助手。唯一任务：判断回答用户问题是否需要下列补充字段，只输出一个严格 JSON 数组。\n可选字段（只能从这里选，最多 ")
	sb.WriteString(fmt.Sprintf("%d", consultDataRoundMaxFields))
	sb.WriteString(" 项；不需要就输出 []）：\n")
	for _, f := range consultSupplementFields {
		sb.WriteString(fmt.Sprintf("- %q: %s\n", f.key, f.desc))
	}
	sb.WriteString("规则：只输出 JSON 数组本身，不要解释、不要 markdown 代码块；白名单外的字段（龙虎榜、融资余额、北向资金、盘口五档、同板块个股等）一律取不到，写了也会被丢弃，不要写。")
	return sb.String()
}

// consultDataRound 跑一次补数轮，返回要追加进数据块的补充段（空串＝本轮什么都没补，主流程照旧）。
// 调用方把它直接拼在 dataCtx 尾部，因此补充段与主块同在 ⟦DATA⟧ 边界内、同受白名单覆盖。
// English: runs the supplement round and returns text to append inside the data block; an empty
// string means nothing was supplemented and the main flow is byte-identical to today's.
func (e *Engine) consultDataRound(ctx context.Context, client *llm.Client, userMsg string) (supp string) {
	// 边界1：只读腿不许有停机权限——A 轮里任何一处 nil 装配/解析 panic 都不能带走主回答。
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[consult] §CONSULT-DATARICH 补数轮异常已跳过（主回答不受影响）: %v", r)
			supp = ""
		}
	}()
	if client == nil {
		return ""
	}
	codes, _, _ := e.consultCodesInMessage(userMsg)
	if len(codes) == 0 {
		return "" // 没识别到个股＝没有可补的对象（主流程此时走 noStock 提示词）
	}
	// 开关：配置管理器未注入时不跑（生产恒有；测试/最小装配下保持今天的字节行为）。
	if e.cfgMgr == nil || !e.cfgMgr.Get().Runtime.ConsultDataRoundOn() {
		return ""
	}
	// 边界2：预算只够一次时整轮跳过，把额度留给主回答。
	if !consultRoundBudgetRoom(client) {
		return ""
	}

	rctx, cancel := context.WithTimeout(ctx, consultDataRoundTimeout)
	defer cancel()
	reply, err := client.ChatMessagesCtx(rctx, []llm.Message{
		{Role: "system", Content: consultDataRoundSystemPrompt()},
		{Role: "user", Content: userMsg},
	})
	if err != nil {
		// 用户已断开（ctx 取消）属正常，不刷日志；其余（超时/上游故障）留一行可 grep 的证据。
		if ctx.Err() == nil {
			log.Printf("[consult] §CONSULT-DATARICH 补数轮调用失败已跳过: %v", err)
		}
		return ""
	}
	keys := parseConsultDataRoundKeys(reply)
	if len(keys) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, code := range codes {
		for _, key := range keys {
			var line string
			switch key {
			case "moneyflow_5d":
				line = e.consultSupplementFlow(code)
			case "kline_60d":
				line = e.consultSupplementKLine(code)
			}
			if line != "" {
				sb.WriteString(line)
			}
		}
	}
	if sb.Len() == 0 {
		return "" // 要了但一条都没取到：不写空段（空段会让模型以为"补过了但还是没数"）
	}
	log.Printf("[consult] §CONSULT-DATARICH 补数轮命中 fields=%v codes=%d", keys, len(codes))
	return "\n—— 补充数据（按需补取；资金流为各日当日累计口径，均线/高低为日K口径，均非盘中即时增量）——\n" + sb.String()
}

// consultRoundBudgetRoom 判断"再跑两轮（A 轮 + 主回答）额度够不够"。
// 咨询预算与总调用预算都要留 ≥2 次；token 预算已耗尽同样跳过。
// 读数走 client.UsageStats()（唯一的当日口径出口，含跨日归零），不在引擎侧另记一本账。
// English: requires room for two more calls in both the consult and total call budgets, reading the
// client's own usage snapshot rather than keeping a second ledger in the engine.
func consultRoundBudgetRoom(client *llm.Client) bool {
	st := client.UsageStats()
	for _, pair := range [][2]string{{"consult_calls", "consult_budget"}, {"calls", "call_budget"}} {
		used, budget := st[pair[0]], st[pair[1]]
		if budget > 0 && budget-used < 2 {
			return false
		}
	}
	if b := st["token_budget"]; b > 0 && st["tokens"] >= b {
		return false
	}
	return true
}

// parseConsultDataRoundKeys 解析 A 轮回复：严格 JSON 字符串数组、白名单外丢弃、去重、最多 5 项。
// 模型常把 JSON 包进 markdown 围栏或前后带解释，故先取第一个 '[' 到最后一个 ']' 再解；
// 解不出（含数组里混了对象/数字）一律返回 nil ＝ 本轮不补，绝不"尽力猜"模型想要什么。
// English: strict JSON string array, whitelist-filtered, deduped and capped; anything unparseable
// yields nil (skip the round) rather than guessing the model's intent.
func parseConsultDataRoundKeys(reply string) []string {
	s := strings.TrimSpace(reply)
	i := strings.Index(s, "[")
	j := strings.LastIndex(s, "]")
	if i < 0 || j <= i {
		return nil
	}
	var raw []string
	if err := json.Unmarshal([]byte(s[i:j+1]), &raw); err != nil {
		return nil
	}
	allowed := make(map[string]bool, len(consultSupplementFields))
	for _, f := range consultSupplementFields {
		allowed[f.key] = true
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, k := range raw {
		k = strings.TrimSpace(k)
		if !allowed[k] || seen[k] {
			continue // 边界3：白名单外一律丢弃，不为它新接任何数据源
		}
		seen[k] = true
		out = append(out, k)
		if len(out) >= consultDataRoundMaxFields {
			break
		}
	}
	return out
}

// consultSupplementFlow 近5个交易日逐日主力净流入（走做 T 因子那条同一个取数闭包）。
// 协调器未注入/取数失败/零行 ⇒ 返回空串弃权，不另找源、不输出 0 冒充实测。
// 逐日带日期输出：模型据此才能说"连续几天流出"，只给一个合计值是答不了那个问题的。
// English: per-day main-force net inflow via the same closure the T factor leg uses; abstains
// (empty string) when unavailable, and prints dates so "N consecutive days" is answerable.
func (e *Engine) consultSupplementFlow(code string) string {
	fetch := e.flowHistFetch(code)
	if fetch == nil {
		return ""
	}
	days, err := fetch()
	if err != nil || len(days) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("股票 %s 近%d个交易日主力净流入（东财，各日为当日累计口径）:", code, len(days)))
	for _, d := range days {
		sb.WriteString(fmt.Sprintf(" %s %.2f万元", d.Date, d.MainNet/1e4))
	}
	sb.WriteString(fmt.Sprintf("；合计 %.2f万元\n", data.SumMainNet(days)/1e4))
	return sb.String()
}

// consultSupplementKLine 中期均线与近60日高低（日K口径，只注入摘要不注入原始K线，控 prompt 体积）。
// 不足 20 根不报 MA20、不足 60 根不报 MA60——新股/停牌复牌不拿短窗冒充长窗。
// English: MA20/MA60 plus the window high/low as a summary (never raw bars); a short history
// abstains instead of passing a 20-day mean off as a 60-day one.
func (e *Engine) consultSupplementKLine(code string) string {
	if e.marketAPI == nil {
		return ""
	}
	kl, err := e.marketAPI.GetSinaKLine(code, 60)
	if err != nil || len(kl) < 20 {
		return ""
	}
	hi, lo := 0.0, 0.0
	for _, k := range kl {
		if k.High > hi {
			hi = k.High
		}
		if k.Low > 0 && (lo == 0 || k.Low < lo) {
			lo = k.Low
		}
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("股票 %s 中期均线: MA20=%.2f元", code, consultMA(kl[len(kl)-20:])))
	if len(kl) >= 60 {
		sb.WriteString(fmt.Sprintf(" MA60=%.2f元", consultMA(kl[len(kl)-60:])))
	}
	if hi > 0 && lo > 0 {
		sb.WriteString(fmt.Sprintf("；近%d日最高%.2f元 最低%.2f元", len(kl), hi, lo))
	}
	sb.WriteString("（日K口径，含最新一根）\n")
	return sb.String()
}