// strategy_name_admit_test.go — §ADVICE-TACTICS（2026-11-05）：战法成员判定「只有一个实现」的行为锁。
//
// 背景：持仓建议层手里只有一个战法字符串（没有 Signal 结构体），它此前既不看开关、
// 也归一不了"龙头 / dragon"这一对写法。本文件锁两件事：
//
//	① AdmitStrategyName（串入口）与 AdmitStrategy（Signal 入口）对同一输入必须同判——
//	   两条入口一旦各写一份规则，表现就是"屏上建议说可以买、下单闸把它拦了"；
//	② StrategyKeyByDisplay 与 StrategyKeyOf 的显示名腿逐字一致（归一表只有一张）。
package signalctl

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
)

// tacticPairs 显示名 ↔ 规范键的在册对照（与 StrategyKeyByDisplay 的 switch 同源）。
// 这里刻意只放"归一表里真有其行"的九对：无法映射的名字按 passthrough 原样返回，
// 单独在下面的用例里锁，不在对照表里给旧名字背书。
var tacticPairs = []struct{ display, key string }{
	{"龙头", "dragon"},
	{"双响炮", "double_bump"},
	{"N形", "n_shape"},
	{"龙回头", "dragon_return"},
	{"动量", "momentum"},
	{"高位滞涨", "high_churn"},
	{"放量破位", "break_down"},
	{"龙头断板", "leader_decay"},
	{"利好兑现砸盘", "good_news_fade"},
}

// libraryKeys 库规则/聚合池的规范键：它们没有"显示名↔键"的归一表腿（显示名是战法库里的自由文本），
// 但默认全集按前缀放行——单独一组用例锁这一点，别混进上面的归一对里假装它们能反查。
var libraryKeys = []string{"factor", "pattern", "fac_1", "pat_2"}

// TestStrategyKeyByDisplayMatchesStrategyKeyOf 归一表只有一张：
// 同一只战法，无论调用方拿的是显示名还是规范键，串入口归出的键必须与 Signal 入口一致。
func TestStrategyKeyByDisplayMatchesStrategyKeyOf(t *testing.T) {
	for _, p := range tacticPairs {
		// Signal 走 StrategyType（规范 ID）那条腿得到的键
		if got := StrategyKeyOf(combat_agent.Signal{StrategyType: p.key, Strategy: p.display}); got != p.key {
			t.Fatalf("规范 ID 腿应得 %q，实得 %q", p.key, got)
		}
		// 串入口：显示名与规范键两种写法都必须归到同一个键
		if got := StrategyKeyByDisplay(p.display); got != p.key {
			t.Fatalf("显示名 %q 应归一到 %q，实得 %q", p.display, p.key, got)
		}
		if got := StrategyKeyByDisplay(p.key); got != p.key {
			t.Fatalf("规范键 %q 归一后必须仍是自己，实得 %q", p.key, got)
		}
	}
	// 无法映射的名字原样返回（旧 StrategyKeyOf 的 passthrough 语义不许变）
	for _, s := range []string{"我的自定义战法", ""} {
		if got := StrategyKeyByDisplay(s); got != s {
			t.Fatalf("无法映射的串必须原样返回，%q 实得 %q", s, got)
		}
	}
}

// TestAdmitStrategyNameAgreesWithAdmitStrategy 两条入口、一条规则：
// 对白名单的四种存量形状（空白名单 / 存规范键 / 存显示名 / 存别的战法），
// 串入口（分别按显示名与规范键问）与 Signal 入口必须给出同一个答案。
func TestAdmitStrategyNameAgreesWithAdmitStrategy(t *testing.T) {
	lists := [][]string{
		nil,          // 空白名单＝存量"全部开启"语义
		{"momentum"}, // 只列动量（严格 opt-in 的那一支）
		{"dragon"},   // 开关存规范键（量化页保存的形状）
		{"龙头"},       // 开关存显示名（旧保存形状）
		{"dragon", "double_bump"},
		{"fac_1"},
		{"unknown_tactic"},
	}
	for _, list := range lists {
		pol := Policy{Strategies: list}
		for _, p := range tacticPairs {
			sig := combat_agent.Signal{Code: "600000", Action: "buy", StrategyType: p.key, Strategy: p.display}
			wantOK, wantWhy := AdmitStrategy(pol, sig)
			for _, ask := range []string{p.display, p.key} {
				gotOK, gotWhy := AdmitStrategyName(pol, ask)
				if gotOK != wantOK {
					t.Fatalf("列表=%v 问=%q：AdmitStrategy=%v(%s) 而 AdmitStrategyName=%v —— 两条入口漂了",
						list, ask, wantOK, wantWhy, gotOK)
				}
				if gotOK != wantOK || gotWhy != wantWhy {
					t.Fatalf("列表=%v 问=%q：留痕文本也不同（%q vs %q）", list, ask, wantWhy, gotWhy)
				}
			}
		}
		// 库规则/聚合池那一腿（前缀放行）也按同一把尺子量一遍：
		// 调用方手里只有规范 ID 时，串入口必须与 Signal 入口同判。
		for _, k := range libraryKeys {
			sig := combat_agent.Signal{Code: "600000", Action: "buy", StrategyType: k, Strategy: "某战法库自由文本"}
			wantOK, _ := AdmitStrategy(pol, sig)
			if gotOK, _ := AdmitStrategyName(pol, k); gotOK != wantOK {
				t.Fatalf("库规则 %q 在列表=%v 下两把入口判得不一样：Signal=%v 串=%v", k, list, wantOK, gotOK)
			}
		}
	}
}

// TestAdmitStrategyNameLegacyAllOnSemantics 空白名单的存量语义在串入口同样成立：
// 内置四形态 + 库规则（fac_/pat_ 前缀与聚合池）放行，动量与未知战法必须显式列名才放行
// ——与 TestAdmitStrategyDefaultSet 同一条 09-17 根修语义，不许两把入口各判一套。
func TestAdmitStrategyNameLegacyAllOnSemantics(t *testing.T) {
	pol := Policy{}
	for _, k := range []string{"dragon", "double_bump", "n_shape", "dragon_return", "fac_1", "pat_2", "factor", "pattern"} {
		if ok, why := AdmitStrategyName(pol, k); !ok {
			t.Fatalf("默认全集应放行 %q，实得 %s", k, why)
		}
	}
	for _, k := range []string{"momentum", "unknown_tactic", "我的自定义战法", ""} {
		if ok, _ := AdmitStrategyName(pol, k); ok {
			t.Fatalf("默认全集不得放行 %q（动量与未知来源严格 opt-in）", k)
		}
	}
	if ok, _ := AdmitStrategyName(pol, "动量"); ok {
		t.Fatal("显示名写法也不许绕过动量的 opt-in")
	}
	// 显式列名后放行（两种写法都认）
	pol2 := Policy{Strategies: []string{"momentum"}}
	if ok, _ := AdmitStrategyName(pol2, "动量"); !ok {
		t.Fatal("开关存规范键、持仓存显示名时必须命中（这就是 11-05 缺陷的另一半）")
	}
	pol3 := Policy{Strategies: []string{"动量"}}
	if ok, _ := AdmitStrategyName(pol3, "momentum"); !ok {
		t.Fatal("开关存显示名、持仓存规范键时必须命中（反方向同一件事）")
	}
}
