// macro_calibrate_test.go — §MARKET_RISK_GATE P6 日历校准：合并覆盖 / 外部事件转换 / 降级链 / 版本失效。
package data

import (
	"testing"
	"time"
)

func TestMergeCalibratedOverride(t *testing.T) {
	formula := []MacroEvent{
		{Date: time.Date(2026, 9, 13, 0, 0, 0, 0, time.Local), Title: "8月CPI(估)", Level: "cpi", Impact: "high", Duration: 3, Source: "formula"},
		{Date: time.Date(2026, 9, 18, 0, 0, 0, 0, time.Local), Title: "交割日", Level: "contract", Impact: "high", Duration: 2, Source: "formula"},
	}
	calibrated := []MacroEvent{
		{Date: time.Date(2026, 9, 10, 0, 0, 0, 0, time.Local), Title: "美国8月CPI", Level: "cpi", Impact: "high", Duration: 3, Source: "llm"}, // 同类型±5天→覆盖
		{Date: time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), Title: "全新事件", Level: "pce", Impact: "medium", Duration: 2, Source: "llm"},  // 新事件→并入
	}
	merged := MergeCalibrated(formula, calibrated)
	if len(merged) != 3 {
		t.Fatalf("应 2 公式 + 1 全新 = 3, got %d (%+v)", len(merged), merged)
	}
	// cpi 被校准日期覆盖（9/13→9/10），Source 转 llm
	for _, m := range merged {
		if m.Level == "cpi" {
			if m.Date.Day() != 10 || m.Source != "llm" {
				t.Errorf("cpi 应被覆盖为 9/10 且 Source=llm, got day=%d src=%s", m.Date.Day(), m.Source)
			}
		}
		if m.Level == "contract" && m.Source != "formula" {
			t.Errorf("未被覆盖的 contract 应保留 formula, got src=%s", m.Source)
		}
	}
	// 空校准 → 原样返回公式
	if got := MergeCalibrated(formula, nil); len(got) != 2 {
		t.Errorf("空校准不应改变列表, got %d", len(got))
	}
}

func TestExternalToMacro(t *testing.T) {
	ext := []ExternalEvent{
		{Date: "2026-11-05", Title: "FOMC", Impact: "high", Level: "fomc"},
		{Date: "not-a-date", Title: "坏日期", Impact: "high", Level: "cpi"}, // 跳过
		{Date: "2026-12-01", Title: "无类型", Impact: "weird", Level: ""},    // level→other, impact→medium
	}
	got := externalToMacro(ext, "external")
	if len(got) != 2 {
		t.Fatalf("应过滤非法日期, got %d", len(got))
	}
	if got[0].Source != "external" || got[0].Duration != 3 { // high→dur 3
		t.Errorf("外部 high 事件映射异常: %+v", got[0])
	}
	if got[1].Level != "other" || got[1].Impact != "medium" {
		t.Errorf("缺省归一异常: %+v", got[1])
	}
}

func TestCalibratedStoreVersion(t *testing.T) {
	v0 := CalibratedVersion()
	SetCalibratedEvents([]MacroEvent{{Level: "cpi", Date: time.Now()}})
	if CalibratedVersion() != v0+1 {
		t.Fatalf("SetCalibratedEvents 应递增版本, %d→%d", v0, CalibratedVersion())
	}
	if len(CalibratedEvents()) != 1 {
		t.Fatal("应读到 1 条校准事件")
	}
	SetCalibratedEvents(nil) // 清空回退公式
	if len(CalibratedEvents()) != 0 {
		t.Fatal("清空后应无校准事件")
	}
	if CalibratedVersion() != v0+2 {
		t.Fatal("清空也应递增版本（触发消费方重建）")
	}
}

// TestCalibrateFallbackNoChatNoAPI 无 LLM 无 API 无缓存 → 回退 formula（清空覆盖、条数 0），不 panic。
func TestCalibrateFallbackNoChatNoAPI(t *testing.T) {
	dir := t.TempDir()
	cacheFile := dir + "/none.json" // 不存在
	src, n := CalibrateMacroCalendar(nil, "", cacheFile, time.Now().Year(), 3)
	if src != "formula" || n != 0 {
		t.Fatalf("无源应回退 formula, got src=%s n=%d", src, n)
	}
	if len(CalibratedEvents()) != 0 {
		t.Fatal("formula 回退应清空覆盖")
	}
}
