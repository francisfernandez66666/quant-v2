// flow_factor_api_test.go — §FLOW-FACTOR / §SIZING-EVO（2026-10-25）配置的 HTTP 往返锁。
//
// 设置页把净流入因子的全部档位搬上了表单，而档位阈值**天生带负号**（`mild_out_pct=-1` 表示
// "净流出 1% 以上"）。这条路径上有两个只会在真机上才暴露的坑，本文件各钉一条：
//
//	① 嵌套 map 的**键级稀疏 merge**：只发 `buy_5d_bands.mild_out_pct` 时，同段的
//	   `strong_out_pct` 与兄弟段 `buy_today_bands` 必须一个都不动。整段覆盖会让管理员
//	   改一个阈值、其余阈值悄悄回出厂值（页面显示的是新值，引擎吃的是默认值）。
//	② 负数的 JSON 往返：`-2.5` 必须原样读回。任何"缺失即回落默认/零值"的实现都会把
//	   负阈值吞成 -1 或 0，而 0 在分档里是**中性档边界**，静默改档比报错危险得多。
//
// 另钉两条状态语义：`flow_factor.enabled` 出厂 nil=**开**（缺键即生效），
// `evo.enabled` 出厂 nil=**关**（必须显式开才跑）——两个开关的默认方向相反，
// 写反了就是"一次保存启动常驻进化"或"页面显示关、引擎在跑"。
//
// English: HTTP round-trip locks for the net-inflow factor and evolution switches — nested
// per-key sparse merge (one threshold must never reset its siblings), signed band thresholds
// surviving JSON round-trips, and the two opposing nil-defaults (factor on, evolution off).
package server

import (
	"net/http"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
)

// reopenRegimeConfig 用同一份目录/文件重开一套 Manager（模拟进程重启）。
// 账号级 sizing 存在 auth 存储里，所以 auth manager 必须指向同一目录才能读到同一份数据。
// English: reopens a manager pair on the same directory/file, simulating a process restart.
func reopenRegimeConfig(t *testing.T, cfgPath string) *config.Manager {
	t.Helper()
	mgr := auth.NewManager(filepath.Dir(cfgPath))
	if err := mgr.Init(); err != nil {
		t.Fatalf("auth re-init: %v", err)
	}
	cm := config.NewManager(cfgPath)
	cm.SetStore(mgr)
	return cm
}

// TestFlowFactorGetEchoesDefaults 出厂空配置下 GET 必须回显净流入因子的**出厂档位**。
// 反证靶心：若回显零值，设置页会显示"所有档位乘子=0"，管理员一保存就把因子写成全弃权。
// English: an untouched config must echo the factory bands, never zeros.
func TestFlowFactorGetEchoesDefaults(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/regime", ""))
	if rr.Code != 200 {
		t.Fatalf("GET /api/config/regime 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	_, sizing, _ := decodeRegime(t, rr.Body.String())
	ff, _ := sizing["flow_factor"].(map[string]any)
	if ff == nil {
		t.Fatalf("sizing.flow_factor 段缺失（前端表单拿不到档位）: %v", sizing)
	}
	b5, _ := ff["buy_5d_scales"].(map[string]any)
	if got, _ := b5["strong_in"].(float64); got != 1.10 {
		t.Fatalf("5日强流入档出厂应为 1.10, got %v", b5["strong_in"])
	}
	if got, _ := b5["strong_out"].(float64); got != 0.70 {
		t.Fatalf("5日强流出档出厂应为 0.70, got %v", b5["strong_out"])
	}
	bands, _ := ff["buy_5d_bands"].(map[string]any)
	if got, _ := bands["strong_out_pct"].(float64); got != -3 {
		t.Fatalf("5日强流出阈值出厂应为 -3, got %v（负数回显被吞＝档位静默改档）", bands["strong_out_pct"])
	}
	// 出厂态：因子开（nil=开），进化关（nil=关）。
	if on, ok := ff["enabled"]; ok && on == false {
		t.Fatal("flow_factor.enabled 出厂必须为开（nil=开），回显 false 会让管理员以为要手动打开")
	}
	evo, _ := sizing["evo"].(map[string]any)
	if on, ok := evo["enabled"].(bool); ok && on {
		t.Fatal("evo.enabled 出厂必须为关（nil=关），回显 true 等于默认启动常驻进化")
	}
}

// TestFlowFactorNestedSparseMerge 只发一个负阈值 ⇒ 兄弟键与兄弟段一律不动。
// English: posting a single signed threshold must leave every sibling key and section intact.
func TestFlowFactorNestedSparseMerge(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"sizing":{"flow_factor":{"buy_5d_bands":{"mild_out_pct":-2.5}}}}`))
	if rr.Code != 200 {
		t.Fatalf("写 flow_factor 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	_, sizing, _ := decodeRegime(t, rr.Body.String())
	ff, _ := sizing["flow_factor"].(map[string]any)
	b5, _ := ff["buy_5d_bands"].(map[string]any)
	if got, _ := b5["mild_out_pct"].(float64); got != -2.5 {
		t.Fatalf("负阈值 -2.5 未落盘, got %v", b5["mild_out_pct"])
	}
	// 同段兄弟键：不得被整段覆盖回默认以外的值（-3 就是默认值，故用"等于 -3"断言"没被动过"）。
	if got, _ := b5["strong_out_pct"].(float64); got != -3 {
		t.Fatalf("同段兄弟键 strong_out_pct 被覆盖, got %v", b5["strong_out_pct"])
	}
	// 兄弟段：5 日档位乘子一个都没提，必须原样保留出厂值。
	s5, _ := ff["buy_5d_scales"].(map[string]any)
	if got, _ := s5["strong_in"].(float64); got != 1.10 {
		t.Fatalf("兄弟段 buy_5d_scales 被抹平, got %v", s5["strong_in"])
	}
	// 另一侧（卖出加权）：同样不得被牵连。
	sell, _ := ff["sell_scales"].(map[string]any)
	if got, _ := sell["strong_out"].(float64); got != 0.85 {
		t.Fatalf("卖出加权段被牵连, got %v", sell["strong_out"])
	}
}

// TestFlowFactorAndEvoPersistAcrossReload 写盘后换一个 Manager 读同一份文件必须看到新值。
// 反证靶心：只在内存里生效的配置会在重启后静默回退，而页面仍显示"已保存"。
// English: written values must survive a manager reload — otherwise a restart silently reverts.
func TestFlowFactorAndEvoPersistAcrossReload(t *testing.T) {
	s, admin, cfgPath := newRegimeTestServer(t)
	rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/regime",
		`{"sizing":{"flow_factor":{"enabled":false,"sell_scales":{"today_mild_out":0.96}},`+
			`"evo":{"enabled":true,"auto_promote":true}}}`))
	if rr.Code != 200 {
		t.Fatalf("写 flow_factor+evo 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	// 换一套 Manager 从同一份文件重读（模拟进程重启）。
	reloaded := reopenRegimeConfig(t, cfgPath)
	got, _ := reloaded.GetSizingConfigFor(admin.ID)
	if got.FlowFactorOn() {
		t.Fatal("flow_factor.enabled=false 未落盘（重启后因子会静默重新打开）")
	}
	sell := got.FlowFactor.WithDefaults().SellScales
	if v, ok := sell["today_mild_out"]; !ok || v != 0.96 {
		t.Fatalf("sell_scales.today_mild_out 未落盘, got %v", sell)
	}
	if !got.EvoOn() || !got.EvoAutoPromoteOn() {
		t.Fatalf("evo 开关未落盘: on=%v promote=%v", got.EvoOn(), got.EvoAutoPromoteOn())
	}
}
