// §0929DIM 组合层搬运锁：CombatAgent 把战法信号转成自身信号时，
// **凡是从 strategy.Signal 拷了 Meta（四维分数）的地方，必须同时拷 Reasons（四维理由）**。
//
// 缺陷的因果链（完整版见 internal/server/signal_dim_desc_test.go 头注）：
// 战法侧 GenerateSignal 只搬分值、组合层无处可搬（combat_agent.Signal 当年根本没有这个字段）、
// 服务端只能拿 Reason/板块名顶位 —— 三段串起来让前端 d3_desc/d4_desc 恒空。
// 前两段各有产地侧与消费侧的行为锁，本文件补上最容易复发的一段：
// **做多腿与做空腿是两处独立的 Signal 字面量**，只补一侧就会出现
// "做多信号有话可看、做空信号整列空白"这种**不对称的假象**（看起来像做空战法本身没维度），
// 而这种不对称跑测扫不到——只有把两处字面量放在一起比才看得见。
//
//	T1 结构锁：每个设置了 Meta 的 Signal 字面量必须同时设置 Reasons；
//	T2 计数锁：agent.go 里"同时带 Meta 与 Reasons"的转换点 ≥ 2（做多/做空两条腿都在）。
//
// English: the two signal-conversion literals (long leg and short leg) are independent; copying Meta
// without Reasons on one of them produces the asymmetric "bear signals have no dimension text" illusion
// that no runtime test would catch. T1 requires every Signal literal that sets Meta to also set
// Reasons; T2 requires at least two such paired literals to exist.
package combat_agent

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// signalLiteralsWithMeta 解析 agent.go，返回所有"设置了 Meta 键"的 Signal 字面量是否同时设了 Reasons。
// 返回切片的每个元素是一条字面量的审计结果（行号 + 有无 Reasons），便于失败信息直接指出漏拷的位置。
// （signalLiteralsWithMeta parses agent.go and reports, for every Signal literal that sets Meta,
// whether it also sets Reasons, carrying the line number so a failure points at the exact leg.）
func signalLiteralsWithMeta(t *testing.T) []struct {
	line      int
	hasReason bool
} {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "agent.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 agent.go 失败: %v", err)
	}
	var out []struct {
		line      int
		hasReason bool
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		// 只认本包的 Signal 字面量（ combat_agent.Signal 在同包内写作 Signal{} / &Signal{} ）
		name := ""
		switch id := lit.Type.(type) {
		case *ast.Ident:
			name = id.Name
		case *ast.SelectorExpr:
			name = id.Sel.Name
		}
		if name != "Signal" {
			return true
		}
		hasMeta, hasReason := false, false
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "Meta":
				hasMeta = true
			case "Reasons":
				hasReason = true
			}
		}
		if hasMeta {
			out = append(out, struct {
				line      int
				hasReason bool
			}{line: fset.Position(lit.Pos()).Line, hasReason: hasReason})
		}
		return true
	})
	return out
}

// TestAgentMetaLegsAlsoCarryReasons T1+T2：带 Meta 的转换点必须成对带 Reasons，且两条腿都在。
func TestAgentMetaLegsAlsoCarryReasons(t *testing.T) {
	legs := signalLiteralsWithMeta(t)
	if len(legs) < 2 {
		t.Fatalf("§0929DIM：agent.go 里带 Meta 的 Signal 转换点少于 2 处（got %d）——做多/做空两条腿被改名或合并要先更新本锁", len(legs))
	}
	paired := 0
	for _, l := range legs {
		if !l.hasReason {
			t.Fatalf("§0929DIM：agent.go:%d 的 Signal 转换拷了 Meta 却没拷 Reasons（该腿的信号四维说明整列空白）", l.line)
		}
		paired++
	}
	if paired < 2 {
		t.Fatalf("§0929DIM：Meta+Reasons 成对的转换点应≥2，got %d", paired)
	}
}

// TestCombatSignalReasonsJSONKey 契约锁：combat_agent.Signal.Reasons 序列化键必须是 reasons，
// 且零值时该键不出现（omitempty）。
// 这个字段随信号进信号日志与固化信号的 JSON 落库，键名一旦改动，历史读数与新读数会在
// 同一张表里出现两种拼法，复盘按 reasons 取数会静默取到空；而 omitempty 一旦被摘掉，
// "空表"与"字段从未存在"两套读法就开始分叉（§FIX 系列反复踩过的形态）。
// （The field is persisted inside signal-log/pinned-signal JSON; a key rename would silently split the
// table between two spellings, and dropping omitempty would make "empty map" and "absent field"
// diverge.）
func TestCombatSignalReasonsJSONKey(t *testing.T) {
	// 只塞 Reasons、其余字段留零值：断言"键出现且值正确"，不穷举整份 JSON
	// （穷举会把本批其它字段变更误判红，也不属于这条锁的职责）。
	raw, err := json.Marshal(Signal{Reasons: map[string]string{"d1": "事件:重组"}})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	reasons, ok := m["reasons"].(map[string]interface{})
	if !ok || reasons["d1"] != "事件:重组" {
		t.Fatalf("§0929DIM：Reasons 未以 reasons 键序列化，got %s", raw)
	}

	empty, err := json.Marshal(Signal{})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var em map[string]interface{}
	if err := json.Unmarshal(empty, &em); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if _, present := em["reasons"]; present {
		t.Fatalf("§0929DIM：无理由时不应落 reasons 键（omitempty 被摘会让空表与缺失分叉），got %s", empty)
	}
}
