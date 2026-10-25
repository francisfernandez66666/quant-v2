// snapshot_test.go — §SIZING-EVO 影子落盘库单测（R1）。
// 钉死：往返保真、Settle 幂等/只结未结、Usable 门、Samples 汇总、Prune 保留、
// 畸形行容错、空 dir 禁用、原子重写不留 tmp 残留。
package sizing_evo

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/signalctl"
)

// evoSnap 一条测试快照（未结算）。
func evoSnap(day, code string, scale float64) SizingSnapshot {
	return SizingSnapshot{
		Day: day, At: time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC),
		Channel: "live", Account: "u1", Code: code,
		Sig:    SignalProfile{Confidence: 0.8, Price: 10, ATR: 0.2, QualityWeight: 1.05, FlowRatio5D: 1.5},
		Regime: signalctl.Regime{EmotionPhase: "发酵", MarketState: "bull", RiskTier: combat_agent.RiskTierNone},
		CfgVer: "u1#abc123", Scale: scale, Reasons: []string{"5日净流入×1.10"},
	}
}

// TestSnapshotAppendLoadRoundtrip 追加→读取往返保真（含 Regime 嵌套结构）。
func TestSnapshotAppendLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st := NewSnapshotStore(dir)
	snaps := []SizingSnapshot{evoSnap("20261025", "600001", 1.1), evoSnap("20261025", "600002", 0.9)}
	if err := st.Append("20261025", snaps...); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := st.Load("20261025")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 snapshots, got %d", len(got))
	}
	if got[0].Code != "600001" || got[0].Scale != 1.1 || got[0].Sig.FlowRatio5D != 1.5 {
		t.Fatalf("roundtrip mismatch: %+v", got[0])
	}
	if got[0].Regime.MarketState != "bull" || got[0].Regime.EmotionPhase != "发酵" {
		t.Fatalf("nested regime lost: %+v", got[0].Regime)
	}
	if got[1].Reasons[0] != "5日净流入×1.10" {
		t.Fatalf("reasons lost: %v", got[1].Reasons)
	}
	// 缺失日 = 空切片非错误。
	if s, err := st.Load("20261024"); err != nil || len(s) != 0 {
		t.Fatalf("missing day must be empty, got %v/%v", s, err)
	}
	// Days 升序。
	st.Append("20261026", evoSnap("20261026", "600003", 1.0))
	days, _ := st.Days()
	if len(days) != 2 || days[0] != "20261025" || days[1] != "20261026" {
		t.Fatalf("days = %v, want ascending two", days)
	}
}

// TestSnapshotSettleIdempotent Settle 只结未结条目；重复调用第二次返回 0 且不翻值。
func TestSnapshotSettleIdempotent(t *testing.T) {
	dir := t.TempDir()
	st := NewSnapshotStore(dir)
	st.Append("20261025", evoSnap("20261025", "600001", 1.1), evoSnap("20261025", "600001", 1.0), evoSnap("20261025", "600002", 0.9))
	n, err := st.Settle("20261025", "600001", 0.03, time.Now())
	if err != nil || n != 2 {
		t.Fatalf("settle 600001: n=%d err=%v, want 2/nil", n, err)
	}
	// 重复 settle 同码同值：无可结条目 → 0，且文件不被重写（tmp 不残留）。
	n2, err := st.Settle("20261025", "600001", 0.03, time.Now())
	if err != nil || n2 != 0 {
		t.Fatalf("re-settle must be 0, got %d/%v", n2, err)
	}
	got, _ := st.Load("20261025")
	for i, sn := range got {
		switch sn.Code {
		case "600001":
			if sn.FwdRet == nil || *sn.FwdRet != 0.03 || sn.SettledAt == nil {
				t.Fatalf("600001 must be settled: %+v", sn)
			}
		case "600002":
			if sn.FwdRet != nil {
				t.Fatalf("600002 must stay unsettled: %+v", sn)
			}
		}
		_ = i
	}
	// 异码 settle 不动已结条目。
	st.Settle("20261025", "600002", -0.01, time.Now())
	got2, _ := st.Load("20261025")
	for _, sn := range got2 {
		if sn.Code == "600001" && (sn.FwdRet == nil || *sn.FwdRet != 0.03) {
			t.Fatal("异码 settle 不得改动已结条目")
		}
	}
	// 原子重写不残留 tmp。
	if _, err := os.Stat(filepath.Join(dir, "20261025.jsonl.tmp")); !os.IsNotExist(err) {
		t.Fatal("atomic rewrite must not leave a tmp file")
	}
}

// TestSnapshotUsableAndSamples Usable 门（已结算+Scale>0）与 Samples 汇总。
func TestSnapshotUsableAndSamples(t *testing.T) {
	dir := t.TempDir()
	st := NewSnapshotStore(dir)
	st.Append("20261025",
		evoSnap("20261025", "600001", 1.1), // 可结算后可用
		evoSnap("20261025", "600002", 0),   // Scale=0（未定权/硬拦）→ 永不入样本
		evoSnap("20261025", "600003", 1.0),
	)
	st.Settle("20261025", "600001", 0.02, time.Now())
	st.Settle("20261025", "600003", -0.01, time.Now())
	st.Settle("20261025", "600002", 0.05, time.Now()) // 结了也不入（Scale=0）
	samples, err := st.Samples()
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("want 2 usable samples, got %d", len(samples))
	}
	// ICSample 投影：Ret 取自 FwdRet，State 取自 Regime.MarketState。
	ics := samples[0].ICSample()
	if ics.Ret != 0.02 || ics.State != "bull" || ics.Scale != 1.1 || ics.Date != "20261025" {
		t.Fatalf("ICSample projection mismatch: %+v", ics)
	}
	// 未结算快照的 ICSample：Ret=NaN（弃权，不进截面）。
	unsettled := evoSnap("20261026", "600004", 1.0)
	if got := unsettled.ICSample(); !math.IsNaN(got.Ret) {
		t.Fatalf("unsettled projection must be NaN ret, got %v", got.Ret)
	}
	// Counts：总 3 / 已结 3 / 1 天。
	c, err := st.Counts()
	if err != nil || c.Total != 3 || c.Settled != 3 || c.Days != 1 {
		t.Fatalf("counts = %+v err=%v, want total=3 settled=3 days=1", c, err)
	}
}

// TestSnapshotToSignal 画像→Signal 字段逐一对应（反事实重算的输入面）。
func TestSnapshotToSignal(t *testing.T) {
	p := SignalProfile{Confidence: 0.9, Price: 33.3, ATR: 0.7, QualityWeight: 1.1, D1Blocked: true, FlowRatio5D: 2.5, FlowRatioToday: -1.5}
	sig := p.ToSignal("600519")
	if sig.Code != "600519" || sig.Confidence != 0.9 || sig.Price != 33.3 || sig.ATR != 0.7 ||
		sig.QualityWeight != 1.1 || !sig.D1Blocked || sig.FlowRatio5D != 2.5 || sig.FlowRatioToday != -1.5 {
		t.Fatalf("ToSignal mismatch: %+v", sig)
	}
}

// TestSnapshotPrune 只保留最近 keepDays 个日文件。
func TestSnapshotPrune(t *testing.T) {
	dir := t.TempDir()
	st := NewSnapshotStore(dir)
	for _, d := range []string{"20261020", "20261021", "20261022", "20261023", "20261024"} {
		if err := st.Append(d, evoSnap(d, "600001", 1.0)); err != nil {
			t.Fatalf("append %s: %v", d, err)
		}
	}
	n, err := st.Prune(2)
	if err != nil || n != 3 {
		t.Fatalf("prune: n=%d err=%v, want 3/nil", n, err)
	}
	days, _ := st.Days()
	if len(days) != 2 || days[0] != "20261023" || days[1] != "20261024" {
		t.Fatalf("days after prune = %v, want the newest two", days)
	}
	// 无需删时 0。
	if n, _ := st.Prune(10); n != 0 {
		t.Fatalf("no-op prune must be 0, got %d", n)
	}
}

// TestSnapshotMalformedLineTolerant 单行畸形不报废整日（本地库容错读取）。
func TestSnapshotMalformedLineTolerant(t *testing.T) {
	dir := t.TempDir()
	st := NewSnapshotStore(dir)
	st.Append("20261025", evoSnap("20261025", "600001", 1.1))
	// 手工追加一行垃圾 + 一行空行。
	f, _ := os.OpenFile(filepath.Join(dir, "20261025.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("{not json\n\n")
	f.Close()
	got, err := st.Load("20261025")
	if err != nil {
		t.Fatalf("load with malformed line: %v", err)
	}
	if len(got) != 1 || got[0].Code != "600001" {
		t.Fatalf("malformed line must be skipped, got %+v", got)
	}
}

// TestSnapshotEmptyDirDisabled 空 dir = 禁用落盘（各路径静默通过，不 panic）。
func TestSnapshotEmptyDirDisabled(t *testing.T) {
	st := NewSnapshotStore("")
	if err := st.Append("20261025", evoSnap("20261025", "600001", 1.1)); err != nil {
		t.Fatalf("append on disabled store: %v", err)
	}
	if n, err := st.Settle("20261025", "600001", 0.01, time.Now()); n != 0 || err != nil {
		t.Fatalf("settle on disabled store: %d/%v", n, err)
	}
	if days, err := st.Days(); len(days) != 0 || err != nil {
		t.Fatalf("days on disabled store: %v/%v", days, err)
	}
	if s, err := st.Samples(); len(s) != 0 || err != nil {
		t.Fatalf("samples on disabled store: %v/%v", s, err)
	}
	if n, err := st.Prune(1); n != 0 || err != nil {
		t.Fatalf("prune on disabled store: %d/%v", n, err)
	}
	if st.Dir() != "" {
		t.Fatal("disabled store dir must be empty")
	}
}

// TestSnapshotNilStoreSafe nil 库全路径安全（防御性：宿主忘注入时不 panic）。
func TestSnapshotNilStoreSafe(t *testing.T) {
	var st *SnapshotStore
	if err := st.Append("20261025", evoSnap("20261025", "600001", 1.1)); err != nil {
		t.Fatalf("nil append: %v", err)
	}
	if n, err := st.Settle("20261025", "600001", 0.01, time.Now()); n != 0 || err != nil {
		t.Fatalf("nil settle: %d/%v", n, err)
	}
}
