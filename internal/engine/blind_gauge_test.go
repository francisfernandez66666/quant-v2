// blind_gauge_test.go — §INDEX-BLIND（2026-11-02）盲区告警三件套的对接用例。
//
// 钉的是"注册了规则却没人喂数"这一族（§DEADGAUGE 已经锤过三次：规则在、量规在、
// 两端键名不一致或压根没赋值点 ⇒ 规则永不触发，而现场看到的一切都是绿的）。
// 今天现网整天封顶半仓而告警零条，正是这条形状的实例——同期 broker_feed_unavailable
// 倒是响过，所以坏的不是通道，是"没人写 sizing_blind_state"。
package engine

import (
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
)

// TestBlindGaugeRuleKeyAligned 两条规则的 Metric 必须等于喂数端写的键名。
// 只断"规则在册"是不够的：键名打错时规则照样在册、照样一条不响。
func TestBlindGaugeRuleKeyAligned(t *testing.T) {
	want := map[string]string{"market_data_blind": "", "market_data_stale": ""}
	for _, r := range metrics.DefaultAlertRules() {
		if k, hit := want[r.Name]; hit {
			want[r.Name] = r.Metric
			_ = k
		}
	}
	for name, metric := range want {
		if metric != "sizing_blind_state" {
			t.Fatalf("规则 %s 读的键名是 %q（写端在 refreshStalenessGauges，两边必须同源）", name, metric)
		}
	}
	// 判据档位也必须成对钉住：blind 用 ge 3（真盲区才必推），stale 用 ge 2（兜底中进日汇总）。
	// 有人把 blind 改成 ge 2 的话，主链一退就天天弹窗——那会把真告警训练成噪音。
	for _, r := range metrics.DefaultAlertRules() {
		switch r.Name {
		case "market_data_blind":
			if r.Op != "ge" || r.Threshold != 3 {
				t.Fatalf("market_data_blind 判据被改成 %s %v（必须 ge 3）", r.Op, r.Threshold)
			}
		case "market_data_stale":
			if r.Op != "ge" || r.Threshold != 2 {
				t.Fatalf("market_data_stale 判据被改成 %s %v（必须 ge 2）", r.Op, r.Threshold)
			}
		}
	}
}

// TestRefreshStalenessFeedsBlindGauge 喂数腿：盘中写档位、非盘中一律写 0。
// 0 是"不适用"，不是"健康"——所以非盘中不许把上一轮的 2/3 留在量规上
// （收盘后残留会让次日开盘前的一条 For=300s 规则直接命中，那是假故障）。
func TestRefreshStalenessFeedsBlindGauge(t *testing.T) {
	f := newFundsFix(t)
	// 预置一份**真盲区**的定权缓存（10s 短窗内直接复用，不打上游）
	f.e.mu.Lock()
	if f.e.sizingCtxCache == nil {
		f.e.sizingCtxCache = map[string]sizingCtx{}
	}
	f.e.sizingCtxCache[f.uid] = sizingCtx{
		MS: 0.5, Missing: true, MissingDims: []string{"index_change"}, At: time.Now(),
	}
	f.e.mu.Unlock()

	f.e.refreshStalenessGauges()
	v, ok := metrics.GetGauge("sizing_blind_state")
	if !ok {
		t.Fatalf("量规 sizing_blind_state 从没被写过（规则永不触发）")
	}
	inSession := data.IsTradingDay(time.Now()) && data.IsContinuousTrade(time.Now())
	if inSession {
		if v != 3 {
			t.Fatalf("盘中真盲区应写 3，实得 %d", v)
		}
	} else if v != 0 {
		t.Fatalf("非盘中应写 0（不适用），实得 %d——残留会让次日开盘前假红", v)
	}

	// 后备腿接管（仍真实）⇒ 盘中应写 2，而不是 3
	f.e.mu.Lock()
	f.e.sizingCtxCache[f.uid] = sizingCtx{MS: 0.81, IndexSource: data.IndexSourceSina, At: time.Now()}
	f.e.mu.Unlock()
	f.e.refreshStalenessGauges()
	v2, _ := metrics.GetGauge("sizing_blind_state")
	if inSession && v2 != 2 {
		t.Fatalf("后备腿接管应写 2，实得 %d", v2)
	}
	if !inSession && v2 != 0 {
		t.Fatalf("非盘中应回到 0，实得 %d", v2)
	}
}

// TestBlindAlertDoesNotTouchDataPlane 负向行为锁：本批的告警腿只读判定，
// 不许动降级链/健康线/下单判定（owner 边界，§STARVE-STALE 的 J0 同规）。
// 形状：读 refreshStalenessGauges 函数体，出现 Degrade 赋值或对 /health 的写入即红。
func TestBlindAlertDoesNotTouchDataPlane(t *testing.T) {
	raw, err := os.ReadFile("scoring_loop.go")
	if err != nil {
		t.Fatalf("读 scoring_loop.go: %v", err)
	}
	src := string(raw)
	body := functionBody(src, "func (e *Engine) refreshStalenessGauges(")
	if body == "" {
		t.Fatalf("取不到 refreshStalenessGauges 函数体（锚漂了，这条锁是摆设）")
	}
	for _, banned := range []string{"st.Degrade =", "Degrade: \"J0\"", "healthOK", "/health"} {
		if strings.Contains(body, banned) {
			t.Fatalf("盲区告警腿里出现了 %q——观测面不许带动数据面/健康线", banned)
		}
	}
}
