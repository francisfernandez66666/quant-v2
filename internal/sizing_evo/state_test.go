// state_test.go — §SIZING-EVO 生命周期状态库单测。
// 钉死：空目录/缺文件/畸形文件都读成空状态（不 panic 不丢库）、灰度增改删幂等、
// 驳回去重、生效写入/回默认、降级记录上限。
package sizing_evo

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCfgVerOf 配置戳格式：账号 + "#" + 候选 ID（空候选→base，空账号→global）。
func TestCfgVerOf(t *testing.T) {
	if got := CfgVerOf("u1", "evo1234"); got != "u1#evo1234" {
		t.Fatalf("CfgVerOf = %q", got)
	}
	if got := CfgVerOf("u1", ""); got != "u1#base" {
		t.Fatalf("空候选应 base，得 %q", got)
	}
	if got := CfgVerOf("", "evo1234"); got != "global#evo1234" {
		t.Fatalf("空账号应 global，得 %q", got)
	}
}

// TestStateGrayscaleLifecycle 灰度条目增/改/删与组合指纹列举。
func TestStateGrayscaleLifecycle(t *testing.T) {
	st := NewEvoStateStore(t.TempDir())
	if err := st.AddGrayscale(GrayscaleEntry{CandID: "evo1", Params: map[string]float64{"kelly_lambda": 0.45}, EnteredAt: "20261025"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddGrayscale(GrayscaleEntry{CandID: "evo2", Params: map[string]float64{"cap": 1.2}, EnteredAt: "20261025"}); err != nil {
		t.Fatal(err)
	}
	// 同 ID 更新：参数/备注覆盖，EnteredAt 保留（观察期不因更新重置）。
	if err := st.AddGrayscale(GrayscaleEntry{CandID: "evo1", Params: map[string]float64{"kelly_lambda": 0.5}, EnteredAt: "20261101", Note: "改"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Grayscale) != 2 {
		t.Fatalf("灰度条目 = %d, want 2", len(got.Grayscale))
	}
	if got.Grayscale[0].EnteredAt != "20261025" || got.Grayscale[0].Params["kelly_lambda"] != 0.5 || got.Grayscale[0].Note != "改" {
		t.Fatalf("同 ID 更新错：%+v", got.Grayscale[0])
	}
	combos := st.GrayscaleCombos()
	if len(combos) != 2 {
		t.Fatalf("组合指纹 = %d, want 2", len(combos))
	}
	if err := st.RemoveGrayscale("evo1"); err != nil {
		t.Fatal(err)
	}
	if combos := st.GrayscaleCombos(); len(combos) != 1 {
		t.Fatalf("移除后 = %d, want 1", len(combos))
	}
	// 移除不存在 → 幂等成功。
	if err := st.RemoveGrayscale("nope"); err != nil {
		t.Fatal(err)
	}
}

// TestStateRejectDedup 同指纹重复驳回只留一条。
func TestStateRejectDedup(t *testing.T) {
	st := NewEvoStateStore(t.TempDir())
	fp := ComboKey(map[string]float64{"kelly_lambda": 0.45})
	if err := st.Reject(fp); err != nil {
		t.Fatal(err)
	}
	if err := st.Reject(fp); err != nil {
		t.Fatal(err)
	}
	if got := st.RejectedCombos(); len(got) != 1 {
		t.Fatalf("去重后 = %d, want 1", len(got))
	}
}

// TestStateAppliedSetClear 生效写入 → 读取；写 nil → 回默认。
func TestStateAppliedSetClear(t *testing.T) {
	st := NewEvoStateStore(t.TempDir())
	e := &AppliedEntry{CandID: "evo1", Params: map[string]float64{"cap": 1.2}, CfgVer: "global#evo1", Auto: true}
	if err := st.SetApplied(e); err != nil {
		t.Fatal(err)
	}
	if got := st.AppliedOf(); got == nil || got.CandID != "evo1" || got.CfgVer != "global#evo1" || !got.Auto {
		t.Fatalf("生效条目错：%+v", got)
	}
	if err := st.SetApplied(nil); err != nil {
		t.Fatal(err)
	}
	if got := st.AppliedOf(); got != nil {
		t.Fatalf("回默认后应为 nil，得 %+v", got)
	}
}

// TestStateDemoteCap 降级记录保留最近 50 条。
func TestStateDemoteCap(t *testing.T) {
	st := NewEvoStateStore(t.TempDir())
	for i := 0; i < 60; i++ {
		if err := st.AddDemote(DemoteRecord{CandID: "evo1", At: "20261025", Reason: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.Load()
	if len(got.Demoted) != 50 {
		t.Fatalf("降级记录 = %d, want 50", len(got.Demoted))
	}
}

// TestStateEmptyDirNoop 空目录=纯内存禁用：读写全静默，读为空状态。
func TestStateEmptyDirNoop(t *testing.T) {
	st := NewEvoStateStore("")
	if err := st.AddGrayscale(GrayscaleEntry{CandID: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := st.GrayscaleCombos(); len(got) != 0 {
		t.Fatalf("空目录不应有灰度条目，得 %d", len(got))
	}
	if err := st.SetApplied(&AppliedEntry{CandID: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := st.AppliedOf(); got != nil {
		t.Fatalf("空目录不应有生效条目，得 %+v", got)
	}
}

// TestStateCorruptFileReadsEmpty 畸形状态文件读成空状态（不 panic、不清库）。
func TestStateCorruptFileReadsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, evoStateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewEvoStateStore(dir)
	got, err := st.Load()
	if err != nil {
		t.Fatalf("畸形文件不应报错，得 %v", err)
	}
	if len(got.Grayscale) != 0 || got.Applied != nil {
		t.Fatalf("畸形文件应读成空状态，得 %+v", got)
	}
	// 写入后能正常落库（畸形内容被覆盖，库不卡死）。
	if err := st.AddGrayscale(GrayscaleEntry{CandID: "evo9", EnteredAt: "20261025"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Load(); len(got.Grayscale) != 1 {
		t.Fatalf("覆盖后应能读回 1 条，得 %d", len(got.Grayscale))
	}
}

// TestCandidateStableID 稳定 ID：同组合同 ID，不同组合不同 ID。
func TestCandidateStableID(t *testing.T) {
	a := Candidate{Params: map[string]float64{"kelly_lambda": 0.45, "cap": 1.2}}
	b := Candidate{Params: map[string]float64{"cap": 1.2, "kelly_lambda": 0.45}} // 顺序不同
	c := Candidate{Params: map[string]float64{"kelly_lambda": 0.5}}
	if a.StableID() != b.StableID() {
		t.Fatalf("同组合应同 ID：%s vs %s", a.StableID(), b.StableID())
	}
	if a.StableID() == c.StableID() {
		t.Fatalf("不同组合不应同 ID：%s", a.StableID())
	}
}
