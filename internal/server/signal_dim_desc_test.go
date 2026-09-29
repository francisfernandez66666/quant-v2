// §0929DIM（09-29 全量审计批 ⑥）四维理由文本过界回归：
//
// 缺陷的因果链（读码锤实，非文档推断）：
//  1. 战法在评分阶段**就算好了**每个维度的中文依据——N形 scorer 直接产出
//     D2Desc/D3Desc/D4Desc（竞价强弱、回调形态、承接力度），龙头/双响炮/龙回头也各自算过
//     涨幅贴板、板块最强涨幅、缩量比这些原始量；
//  2. 这些文本进了 strategy.Evaluation.Reasons；
//  3. 但 GenerateSignal 只把 eval.Details 拷进 Signal.Meta，**Reasons 整张表被丢弃**；
//  4. combat_agent.Signal 干脆没有承载它的字段，转换处自然也没得拷；
//  5. 服务端 toFixSignals 于是只能 D1Desc=信号 Reason、D2Desc=板块名，
//     d3_desc/d4_desc **在所有战法下恒为空串**；
//  6. 前端 Signals.jsx 的 D1~D4 单元格里 `${row.d3_desc && <em>…</em>}` 这段渲染分支因此**从来没走到过**
//     ——列上只剩一个分数，用户看不出这一维是被什么条件抬过门槛的，复盘也复原不了判定现场。
//
// 修法按 §0929DIM 裁决走"扩展信号结构体"而不是"把文本塞进 Reason 串"：
//
//	Reasons 是**带维键的结构化表**，塞进自由文本会让前端只能靠子串猜维（§P4 缺陷3 已经教训过
//	"从 Reason 子串猜级别"），也让四维错配无从校验。
//
// 本文件的锁分工：
//
//	T1~T4 四条战法分支：四维分数与四维说明**同一格同一次**取到（N形 d1..d4 / 龙头 f1_seal.. /
//	   双响炮 vol_score.. / 龙回头 dragon_score..），任一分支键名写错即红；
//	T5 兜底分支：未接入理由通道的战法不得被这次改动**降级**（D1 仍回 Reason、D2 仍回板块）；
//	T6 静态锁（go/ast）：dimDescs 与 dimScores 的分支与键序列必须逐条相等——
//	   这是"分数说 A 维、话术说 B 维"这类跨维错配的唯一机器防线；
//	T7 契约锁：fixSignal 序列化后的 JSON 键名仍是 d1_desc~d4_desc（前端按这套键读）。
//
// English: the four dimension descriptions were computed at scoring time and dropped at the
// strategy→signal boundary, so the frontend's d3_desc/d4_desc never rendered. These locks cover each
// per-strategy key branch, the backward-compatible fallback, an AST lock that dimDescs mirrors
// dimScores branch-for-branch, and the JSON contract key names.
package server

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"quant-trading-v2/internal/combat_agent"
)

// dimCaseKeys 用 go/ast 从指定函数里抽出「switch 分支 → 该分支按序读到的 map 键名」映射。
// 只认 `x := <ident>["<key>"]` 这一种形态（dimScores/dimDescs 就写这一种），
// 其它写法一概不计——这样有人把映射改成循环或查表时本锁会因"分支数对不上"直接判红，
// 而不是静默通过。返回的 map 含合成键 "default" 代表默认分支。
// （dimCaseKeys extracts, per switch case, the ordered list of map keys the function reads; anything
// other than the literal `ident["key"]` form is not counted, so restructure trips the lock instead of
// silently passing.）
func dimCaseKeys(t *testing.T, funcName string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "handlers_fix.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 handlers_fix.go 失败: %v", err)
	}
	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == funcName {
			fn = d
		}
		return true
	})
	if fn == nil {
		t.Fatalf("未找到 %s 函数（改名/搬走要同时更新本锁）", funcName)
	}
	var sw *ast.SwitchStmt
	ast.Inspect(fn, func(n ast.Node) bool {
		if s, ok := n.(*ast.SwitchStmt); ok && sw == nil {
			sw = s
		}
		return true
	})
	if sw == nil {
		t.Fatalf("%s 内没有 switch 分支（战法维度键映射必须按 StrategyType 分支）", funcName)
	}
	out := map[string][]string{}
	for _, cc := range sw.Body.List {
		clause, ok := cc.(*ast.CaseClause)
		if !ok {
			continue
		}
		label := "default"
		if len(clause.List) > 0 {
			// 分支标签只可能是字符串字面量（"dragon" 等）；直接取原文去引号
			if lit, ok := clause.List[0].(*ast.BasicLit); ok {
				label = lit.Value
			}
		}
		var keys []string
		for _, stmt := range clause.Body {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok {
				continue
			}
			for _, r := range ret.Results {
				idx, ok := r.(*ast.IndexExpr)
				if !ok {
					continue
				}
				if _, ok := idx.X.(*ast.Ident); !ok {
					continue
				}
				if lit, ok := idx.Index.(*ast.BasicLit); ok {
					keys = append(keys, lit.Value)
				}
			}
		}
		out[label] = keys
	}
	return out
}

// TestDimDescMirrorsDimScores T6 静态锁：说明映射与分数映射**逐分支同键同序**。
func TestDimDescMirrorsDimScores(t *testing.T) {
	scoreKeys := dimCaseKeys(t, "dimScores")
	descKeys := dimCaseKeys(t, "dimDescs")
	if len(scoreKeys) != len(descKeys) {
		t.Fatalf("§0929DIM：dimDescs 分支数 %d ≠ dimScores 分支数 %d（新增战法只补一侧＝分数与话术跨维错配）",
			len(descKeys), len(scoreKeys))
	}
	for label, sk := range scoreKeys {
		dk, ok := descKeys[label]
		if !ok {
			t.Fatalf("§0929DIM：dimDescs 缺分支 %s", label)
		}
		if len(dk) != len(sk) {
			t.Fatalf("§0929DIM：分支 %s 键数不同（分数 %v / 说明 %v）", label, sk, dk)
		}
		for i := range sk {
			if sk[i] != dk[i] {
				t.Fatalf("§0929DIM：分支 %s 第 %d 维键不一致（分数读 %s，说明读 %s）", label, i+1, sk[i], dk[i])
			}
		}
	}
	// 四条分支都得在（防止某个分支整体蒸发后上面循环仍然"逐条相等"）
	for _, label := range []string{`"dragon"`, `"double_bump"`, `"dragon_return"`, "default"} {
		if _, ok := descKeys[label]; !ok {
			t.Fatalf("§0929DIM：维度键映射缺分支 %s", label)
		}
		if len(descKeys[label]) != 4 {
			t.Fatalf("§0929DIM：分支 %s 必须映射四维，got %v", label, descKeys[label])
		}
	}
}

// TestDimDescNShape T1：N形四维说明从 Reasons 的 d1~d4 取，且 D1 优先用维度理由而不是信号 Reason。
func TestDimDescNShape(t *testing.T) {
	got := toFixSignals([]combat_agent.Signal{{
		Code: "600000", Name: "N形票", StrategyType: "n_shape", Action: "buy", Confidence: 0.9,
		Reason: "full_chain", Sector: "电力",
		Meta:    map[string]float64{"d1": 30, "d2": 20, "d3": 15, "d4": 10},
		Reasons: map[string]string{"d1": "事件:重组", "d2": "竞价强/放量", "d3": "缩量回调6日", "d4": "承接放大"},
	}})
	if len(got) != 1 {
		t.Fatalf("应转出 1 条信号, got %d", len(got))
	}
	s := got[0]
	if s.D1Desc != "事件:重组" || s.D2Desc != "竞价强/放量" || s.D3Desc != "缩量回调6日" || s.D4Desc != "承接放大" {
		t.Fatalf("§0929DIM：N形四维说明未落到 d1_desc~d4_desc，got %q/%q/%q/%q", s.D1Desc, s.D2Desc, s.D3Desc, s.D4Desc)
	}
	// 分数侧必须同时保持原口径（本批只加文本，不许顺手改分值映射）
	if s.D1 != 30 || s.D2 != 20 || s.D3 != 15 || s.D4 != 10 {
		t.Fatalf("§0929DIM：四维分值被改动了（%v/%v/%v/%v）", s.D1, s.D2, s.D3, s.D4)
	}
}

// TestDimDescDragonBranch T2：龙头分支按 f1_seal/f2_resonance/f3_premium/f4_rs 取说明。
func TestDimDescDragonBranch(t *testing.T) {
	got := toFixSignals([]combat_agent.Signal{{
		Code: "000001", Name: "龙头票", StrategyType: "dragon", Action: "buy", Confidence: 0.85,
		Reason: "full_chain", Sector: "半导体",
		Meta:    map[string]float64{"f1_seal": 27, "f2_resonance": 25, "f3_premium": 20, "f4_rs": 15},
		Reasons: map[string]string{"f1_seal": "涨幅9.9%贴板(阈9.9%)", "f2_resonance": "板块共振4.2%", "f3_premium": "超板块3.1pp辨识度高", "f4_rs": "5日趋势+12.0%"},
	}})
	s := got[0]
	if s.D1Desc != "涨幅9.9%贴板(阈9.9%)" || s.D2Desc != "板块共振4.2%" || s.D3Desc != "超板块3.1pp辨识度高" || s.D4Desc != "5日趋势+12.0%" {
		t.Fatalf("§0929DIM：龙头分支未取到四维说明，got %q/%q/%q/%q", s.D1Desc, s.D2Desc, s.D3Desc, s.D4Desc)
	}
	// D2 有理由时不再退回板块名（板块名是旧兜底，不是 D2 的判据）
	if s.D2Desc == "半导体" {
		t.Fatalf("§0929DIM：龙头 D2 说明仍是板块名（分支没走到）")
	}
}

// TestDimDescDoubleBumpBranch T3：双响炮分支按 vol_score/adjust_score/ma_score/adjust_depth 取说明。
func TestDimDescDoubleBumpBranch(t *testing.T) {
	got := toFixSignals([]combat_agent.Signal{{
		Code: "000002", Name: "双凸票", StrategyType: "double_bump", Action: "buy", Confidence: 0.7,
		Reason: "brief", Sector: "地产",
		Meta:    map[string]float64{"vol_score": 30, "adjust_score": 25, "ma_score": 20, "adjust_depth": 3},
		Reasons: map[string]string{"vol_score": "今量2.3倍均量(需≥1.5)", "adjust_score": "振幅4.1%(<6.0%算窄幅)", "ma_score": "多头排列且站稳MA5(5.10/4.90)", "adjust_depth": "当日振幅4.1%"},
	}})
	s := got[0]
	if s.D1Desc != "今量2.3倍均量(需≥1.5)" || s.D2Desc != "振幅4.1%(<6.0%算窄幅)" || s.D3Desc != "多头排列且站稳MA5(5.10/4.90)" || s.D4Desc != "当日振幅4.1%" {
		t.Fatalf("§0929DIM：双响炮分支未取到四维说明，got %q/%q/%q/%q", s.D1Desc, s.D2Desc, s.D3Desc, s.D4Desc)
	}
}

// TestDimDescDragonReturnBranch T4：龙回头分支按 dragon_score/pullback_score/duck_score/confirm_score 取说明。
func TestDimDescDragonReturnBranch(t *testing.T) {
	got := toFixSignals([]combat_agent.Signal{{
		Code: "000003", Name: "龙回头票", StrategyType: "dragon_return", Action: "buy", Confidence: 0.8,
		Reason: "main", Sector: "通信",
		Meta:    map[string]float64{"dragon_score": 25, "pullback_score": 21, "duck_score": 20, "confirm_score": 14},
		Reasons: map[string]string{"dragon_score": "板块Top2=true 首涨42% RPS20=88", "pullback_score": "回调18%/6日/缩量25%", "duck_score": "鸭头(MA5 10.20/MA10 9.80/MA20 9.50)", "confirm_score": "确认(现价10.50/MA5 10.20/量比1.30)"},
	}})
	s := got[0]
	if s.D3Desc != "鸭头(MA5 10.20/MA10 9.80/MA20 9.50)" || s.D4Desc != "确认(现价10.50/MA5 10.20/量比1.30)" {
		t.Fatalf("§0929DIM：龙回头 D3/D4 未落到说明列，got %q/%q", s.D3Desc, s.D4Desc)
	}
	if s.D1Desc != "板块Top2=true 首涨42% RPS20=88" || s.D2Desc != "回调18%/6日/缩量25%" {
		t.Fatalf("§0929DIM：龙回头 D1/D2 未落到说明列，got %q/%q", s.D1Desc, s.D2Desc)
	}
}

// TestDimDescFallbackKeepsOldReading T5 兜底锁：没有 Reasons 的信号（做空/因子/形态等单键战法）
// 必须**保持改动前的显示**——D1=信号 Reason、D2=板块名，D3/D4 空串。
// 这条防的是"把兜底删了来凑等值锁"：那会让未接入理由通道的战法整列空白，属于降级。
// （T5 guards the backward-compatible fallback so strategies without the reason channel keep showing
// Reason/Sector instead of an empty column.）
func TestDimDescFallbackKeepsOldReading(t *testing.T) {
	got := toFixSignals([]combat_agent.Signal{{
		Code: "600004", Name: "无理由票", StrategyType: "factor", Action: "buy", Confidence: 0.6,
		Reason: "因子复合分82.00", Sector: "化工",
		Meta: map[string]float64{"d1": 40, "d2": 30, "d3": 20, "d4": 10},
	}})
	s := got[0]
	if s.D1Desc != "因子复合分82.00" {
		t.Fatalf("§0929DIM：无理由时 D1 必须退回信号 Reason，got %q", s.D1Desc)
	}
	if s.D2Desc != "化工" {
		t.Fatalf("§0929DIM：无理由时 D2 必须退回板块名，got %q", s.D2Desc)
	}
	if s.D3Desc != "" || s.D4Desc != "" {
		t.Fatalf("§0929DIM：未接入理由的维度应留空串而不是编造文案，got %q/%q", s.D3Desc, s.D4Desc)
	}
	// 分值映射不受影响（factor 走 default 分支读 d1..d4）
	if s.D1 != 40 || s.D4 != 10 {
		t.Fatalf("§0929DIM：兜底分支把分值映射改动了（%v/%v）", s.D1, s.D4)
	}
}

// TestDimDescJSONContractKeys T7 契约锁：序列化后的键名必须是 d1_desc~d4_desc（前端按此读）。
// 键名一旦改动，Signals 页的四维单元格会整列失去文本而**接口仍然 200**——正是本批在消灭的
// 「静默降级」形态，所以在后端就把键名钉住。
// （T7 pins the wire key names; a rename would silently empty the frontend column while the endpoint
// still returns 200.）
func TestDimDescJSONContractKeys(t *testing.T) {
	got := toFixSignals([]combat_agent.Signal{{
		Code: "600005", Name: "契约票", StrategyType: "n_shape", Action: "buy", Confidence: 0.9,
		Reasons: map[string]string{"d1": "一", "d2": "二", "d3": "三", "d4": "四"},
	}})
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("序列化 fixSignal 失败: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	for k, want := range map[string]string{"d1_desc": "一", "d2_desc": "二", "d3_desc": "三", "d4_desc": "四"} {
		if v, ok := m[k]; !ok {
			t.Fatalf("§0929DIM：响应缺键 %s（前端 %s 列会整列空白）body=%s", k, k, raw)
		} else if v != want {
			t.Fatalf("§0929DIM：键 %s 值应为 %q，got %v", k, want, v)
		}
	}
}
