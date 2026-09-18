// 本文件：§FIX-2 + §FIX-5（20260919 批三）回归。
// FIX-2：反编造白名单边界化——trusted 只采 ⟦DATA⟧ 数据块/用户消息/历史，提示词模板
// （含"错误示范"假数字 2383万/1.2亿）彻底出局；旧实现连 system 一起抽，模型原样复现
// 提示词假数字即被放行，本文件的 TestAuditNumbersIgnoresPromptDecoys 是该缺陷的哨兵
// （在旧代码上必红）。
// FIX-5：审计单位/符号容差——万元↔亿元换算归一、反向措辞锚（数据-22200万 ↔ 回复
// "净流出2.22亿元"）、现价/昨收推算涨跌幅 ±0.5pp 放行，同时真编造数字仍必须被拦。
// English: batch-3 regressions — prompt decoys can no longer whiteway numbers, and
// unit-converted / sign-anchored / derived paraphrases of real data survive the audit
// while genuinely fabricated numbers are still replaced.
package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/llm"
)

// TestAuditNumbersIgnoresPromptDecoys FIX-2 哨兵：ConsultLLM 全链路——mock LLM 复述
// 提示词"错误示范"里的假数字（2383万/1.2亿），必须被替换为 [数据缺失]；数据块里的真数字保留。
// 旧代码把 system 传进 collectTrustedNumbers，这两个假数字会进白名单原样放行 → 本用例必红。
func TestAuditNumbersIgnoresPromptDecoys(t *testing.T) {
	var gotSystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) > 0 {
			gotSystem = req.Messages[0].Content
		}
		w.Header().Set("Content-Type", "application/json")
		// 模型回复：复述提示词假数字 + 引用数据块真数字
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": "集合竞价撤单达2383万，主力净流出1.2亿，现价 36.10元 尚可。"},
			}},
		})
	}))
	defer srv.Close()

	e := &Engine{}
	// §FIX-10(20260919 批四)：咨询改为"账号隔离存储不可用即拒绝"，测试须注入 accountsRoot。
	e.SetAccountsRoot(t.TempDir())
	// 预置单股数据块缓存：绕开行情源，注入含真实数字的数据块（buildStockBlock 60s 内命中）。
	e.consultBlockCache = map[string]consultBlockEntry{
		"600580": {text: "\n—— 股票 600580 卧龙电驱 ——\n现价 36.10元 涨跌幅-5.67%（即下跌5.67%） 今开36.50元 最高36.80元 最低35.90元 昨收 38.27元\n", at: time.Now()},
	}
	e.llmClient = llm.New(llm.Config{APIKey: "k", APIURL: srv.URL, Model: "m", Streaming: false})

	reply, err := e.ConsultLLM(context.Background(), "u_fix2", "帮我分析 600580", false)
	if err != nil {
		t.Fatalf("ConsultLLM: %v", err)
	}
	if strings.Contains(reply, "2383万") || strings.Contains(reply, "1.2亿") {
		t.Fatalf("提示词反面教材假数字必须被替换，不得因模板入白名单而放行: %s", reply)
	}
	if !strings.Contains(reply, "36.10元") {
		t.Fatalf("数据块内的真数字应保留: %s", reply)
	}
	// 边界标记接线：数据块必须包在 ⟦DATA⟧/⟦/DATA 内注入 system。
	if !strings.Contains(gotSystem, "⟦DATA⟧") || !strings.Contains(gotSystem, "⟦/DATA") {
		t.Fatalf("system 内数据块必须带 ⟦DATA⟧ 边界: %s", gotSystem)
	}
}

// TestAuditNumbersUnitsAndSigns FIX-5 三类：①单位换算放行（-22200万元 ↔ 2.22亿元 净流出）；
// ②带单位注入字段被规范复述（今开36.50元）与反向措辞（跌幅5.67%）放行；
// ③推算涨跌幅容差（5.7% ≈ 现价/昨收推算 -5.6702%）放行；真编造第三值仍被拦。
func TestAuditNumbersUnitsAndSigns(t *testing.T) {
	data := "主力净流入 -22200.00万元 现价 36.10元 涨跌幅-5.67%（即下跌5.67%） 今开36.50元 昨收 38.27元"
	trusted := collectTrustedNumbers(data)
	trusted.addDerivedPct(data)
	reply := "主力净流出2.22亿元，跌幅5.67%，今开36.50元，跌了5.7%，但换手率8.88%、成交额61.9亿元无从谈起。"
	got := auditNumbers(reply, trusted)
	for _, want := range []string{"2.22亿元", "跌幅5.67%", "今开36.50元", "跌了5.7%"} {
		if !strings.Contains(got, want) {
			t.Errorf("换算/反向措辞/推算容差应放行 %q, got: %s", want, got)
		}
	}
	for _, banned := range []string{"8.88%", "61.9亿元"} {
		if strings.Contains(got, banned) {
			t.Errorf("编造数字必须仍被拦 %q, got: %s", banned, got)
		}
	}
}

// TestAuditNoTrustedStaysNoop 无数据块且用户消息不含数字 → trusted 为空 → 审计整体跳过
// （维持历史语义：无锚点时宁可放行不误伤）。
func TestAuditNoTrustedStaysNoop(t *testing.T) {
	trusted := collectTrustedNumbers("今天大盘怎么走？", "")
	if !trusted.isEmpty() {
		t.Fatalf("纯文本消息不应产生可信数字: %+v", trusted.vals)
	}
	reply := "主力净流入5亿元"
	if got := auditNumbers(reply, trusted); got != reply {
		t.Fatalf("无白名单时审计应整体跳过, got: %s", got)
	}
}

// TestParseAuditedNumberTokenBases 单位归一表逐条锁死：量纲类别 + 基础量。
func TestParseAuditedNumberTokenBases(t *testing.T) {
	cases := []struct {
		tok string
		cat numCat
		val float64
	}{
		{"-22200.00万元", catMoney, -2.22e8},
		{"2.22亿元", catMoney, 2.22e8},
		{"2.22 亿元", catMoney, 2.22e8}, // 数字与单位间空白（正则 \s* 形态）
		{"36.86元", catMoney, 36.86},
		{"1.2亿股", catShares, 1.2e8},
		{"2.3万", catAmbiguous, 2.3e4},
		{"12%", catPct, 12},
		{"12％", catPct, 12},
		{"3倍", catMult, 3},
	}
	for _, c := range cases {
		cat, val, ok := parseAuditedNumberToken(c.tok)
		if !ok || cat != c.cat || !approxEqual(val, c.val) {
			t.Errorf("parseAuditedNumberToken(%q) = (%v,%v,%v), 期望 (%v,%v)", c.tok, cat, val, ok, c.cat, c.val)
		}
	}
}

// TestAuditNumbersNoCrossCategoryLeak 量纲隔离：金额白名单不得替百分比编造背书。
func TestAuditNumbersNoCrossCategoryLeak(t *testing.T) {
	trusted := collectTrustedNumbers("现价 12.50元")
	reply := "振幅12.5%，主力净流入12.5亿元，现价 12.50元属实。"
	got := auditNumbers(reply, trusted)
	if strings.Contains(got, "12.5%") {
		t.Errorf("百分比编造不应被金额白名单放行: %s", got)
	}
	if strings.Contains(got, "12.5亿元") {
		t.Errorf("12.5亿元(1.25e9)≠12.50元，应被拦: %s", got)
	}
	if !strings.Contains(got, "12.50元") {
		t.Errorf("同量纲真数字应保留: %s", got)
	}
}
