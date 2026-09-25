// 本文件锁 §LIB-GATE：战法库"零条启用规则"必须判红，且零条的**成因**必须随读数出门。
// 起因（2026-09-25 全量重算）：本机研究目录没有线上战法库副本时，`-strategy all` 会照常跑完一整轮，
// 出门的动量数字（144,420 触发）其实是"线上没有任何战法时动量能拿多少单"，与带上 3 条库规则的真值
// （67,925）差 2.1×——因为动量是兜底档，兄弟战法当日出过信号它就不入场，兄弟集不同数字就不可比。
// 旧代码里唯一一处"库为空"的判据（all 分支 append 完五个内置战法之后才判 len(ads)==0）永远走不到，
// 所以这轮既不会报错也不会在报告里留下前提。
//
// 本文件的用例分三类，缺一类就是漏锁：
//  1. 判红面：五种"空"各自的成因读数（file_missing / file_blank / no_entries / all_disabled /
//     no_usable_rule）与门状态 enforced，逐个**等值**断言（只断"有错误"的话，任何成因都能糊过去）；
//  2. 放行面：AllowEmptyLibrary / 单内置战法 / 候选直读三种"本来就不该受库门约束"的路径必须为绿，
//     且读数照样出门（放行≠静默）；这是**反证组**——没有它们，"处处判红"的写法会把不该红的也判红；
//  3. 出门面：读数进 String()/approxNote 的文本锚点，防止"内部算对了但没人看得见"。
//
// English: locks the §LIB-GATE behaviour — a zero-rule strategy library must fail the replay by
// default, each of the five empty causes must be reported distinctly, and the three legitimately
// library-free paths (explicit waiver, single built-in strategy, candidate-direct replay) must stay
// green while still shipping their reading.
package btreplay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// writeLibraryFile 在给定目录里写一个战法库文件（applied_factors.json / applied_patterns.json）。
// 内容原样落盘：这些用例要测的就是"真库文件的形状"读进门后的反应，故不经过 Go 结构体序列化。
func writeLibraryFile(t *testing.T, dir, fileName, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建库目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0o644); err != nil {
		t.Fatalf("写战法库文件失败: %v", err)
	}
}

// entryBody 把 factorEntryJSON/patternEntryJSON 产出的单元素数组剥掉外层方括号，
// 便于在同一个文件里拼多条条目（拼装只在这里做，用例里不再各自 TrimPrefix/TrimSuffix）。
func entryBody(t *testing.T, entriesJSON string) string {
	t.Helper()
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(entriesJSON), "["), "]")
}

// factorEntryJSON 造一条因子库条目（字段与 research.AppliedFactorEntry 的 json tag 同源，
// 刻意走"真库文件的形状"而不是 Go 结构体，免得测的是自己造的读法）。
func factorEntryJSON(t *testing.T, id string, enabled bool, factors ...string) string {
	t.Helper()
	if factors == nil {
		factors = []string{}
	}
	b, err := json.Marshal([]map[string]any{{
		"id": id, "name": "因子战法-" + id, "enabled": enabled,
		"factors": factors, "weights": map[string]float64{}, "directions": map[string]int{},
		"buy_threshold": 70, "horizon": 5, "adj_basis": research.AdjBaselineVersion,
	}})
	if err != nil {
		t.Fatalf("构造库条目失败: %v", err)
	}
	return string(b)
}

// patternEntryJSON 造一条形态库条目（conds 为空即"启用但建不出适配器"那一档）。
func patternEntryJSON(t *testing.T, id string, enabled bool, conds ...map[string]any) string {
	t.Helper()
	if conds == nil {
		conds = []map[string]any{}
	}
	b, err := json.Marshal([]map[string]any{{
		"id": id, "name": "形态战法-" + id, "enabled": enabled, "conds": conds,
		"adj_basis": research.AdjBaselineVersion,
	}})
	if err != nil {
		t.Fatalf("构造形态库条目失败: %v", err)
	}
	return string(b)
}

// runBuild 跑一次 buildAdapters（库路径不碰研究库，故 db 传 nil；传非 nil 就是用例写错了）。
func runBuild(t *testing.T, o *Options) error {
	t.Helper()
	if o.Start == "" {
		o.Start = "20230801"
		o.End = "20230810"
	}
	_, _, err := o.buildAdapters(nil)
	return err
}

// TestLibraryGateZeroRuleCauses 五种"空"各判各的成因：等值断言 ZeroReason + Gate，
// 并断言错误文本里点名了正规出口（读者拿到红字就知道下一步跑什么命令）。
// 每例自己写清"跑哪一侧、目录里放什么"，不在循环体里做二次修补——上一版靠"先写占位文件再删掉"
// 拼出 file_blank，读起来像在测 setup 而不是在测门。
func TestLibraryGateZeroRuleCauses(t *testing.T) {
	cases := []struct {
		name     string
		strategy string                         // 声明跑哪一侧库规则
		setup    func(t *testing.T, dir string) // 往目录里放什么
		reason   string                         // 期望点名的成因
		entries  int                            // 期望的过滤前条目数（0=不校验具体侧）
		enabled  int                            // 期望的启用条目数
	}{
		{
			name:     "file_missing：目录存在但没有库文件（＝这台机器根本没带线上库副本）",
			strategy: "factor",
			setup:    func(t *testing.T, dir string) {}, // 什么都不放
			reason:   "factor:file_missing",
		},
		{
			name:     "file_blank：文件存在但零字节（写入被截断／只建了空壳）",
			strategy: "factor",
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), nil, 0o644); err != nil {
					t.Fatalf("写零字节库文件失败: %v", err)
				}
			},
			reason: "factor:file_blank",
		},
		{
			name:     "no_entries：合法 JSON 数组但零条目",
			strategy: "factor",
			setup:    func(t *testing.T, dir string) { writeLibraryFile(t, dir, "applied_factors.json", "[]") },
			reason:   "factor:no_entries",
		},
		{
			name:     "all_disabled：有条目但全部停用（业务事实，不是故障）",
			strategy: "factor",
			setup: func(t *testing.T, dir string) {
				writeLibraryFile(t, dir, "applied_factors.json", factorEntryJSON(t, "fac_9", false, "Mom20"))
			},
			reason:  "factor:all_disabled",
			entries: 1,
		},
		{
			name:     "no_usable_rule：条目启用但自身没带因子集（历史脏数据）",
			strategy: "pattern",
			setup: func(t *testing.T, dir string) {
				writeLibraryFile(t, dir, "applied_patterns.json", patternEntryJSON(t, "pat_8", true))
			},
			reason:  "pattern:no_usable_rule",
			entries: 1,
			enabled: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			o := &Options{Strategy: tc.strategy, DataDir: dir}
			err := runBuild(t, o)
			if err == nil {
				t.Fatalf("零条库规则必须判红（%s），实得 nil + 读数 %s", tc.reason, o.Library.String())
			}
			if o.Library.Gate != "enforced" {
				t.Fatalf("门状态应为 enforced，实得 %q（读数：%s）", o.Library.Gate, o.Library.String())
			}
			if o.Library.ZeroReason != tc.reason {
				t.Fatalf("成因应为 %s，实得 %q（把五种空压成一个词＝没报）", tc.reason, o.Library.ZeroReason)
			}
			// 三档读数按声明的侧校验（另一侧不许被顺手装配，也不许被伪造成因）。
			f, e, en := o.Library.RulesF, o.Library.EntriesF, o.Library.EnabledF
			if tc.strategy == "pattern" {
				f, e, en = o.Library.RulesP, o.Library.EntriesP, o.Library.EnabledP
			}
			if f != 0 {
				t.Fatalf("判红时该侧规则数必须为 0，实得 %d", f)
			}
			if tc.entries != 0 && e != tc.entries {
				t.Fatalf("过滤前条目数应为 %d，实得 %d（读数没带出真实条数，成因就不可核）", tc.entries, e)
			}
			if tc.enabled != 0 && en != tc.enabled {
				t.Fatalf("启用条目数应为 %d，实得 %d", tc.enabled, en)
			}
			// 红字必须自带修法：这两串缺一串，报错就只是"又红了"。
			for _, must := range []string{"--datadir", "--allow-empty-library"} {
				if !strings.Contains(err.Error(), must) {
					t.Fatalf("错误文本必须点名正规出口 %s，实得：%v", must, err)
				}
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("错误文本必须带成因 %s，实得：%v", tc.reason, err)
			}
		})
	}
	// 反证（等值锁的另一半）：同一份"有启用条目"的库跑同一条路径必须为绿——
	// 否则上面五例可以是"无论输入都判红"的假锁。
	ok := t.TempDir()
	writeLibraryFile(t, ok, "applied_factors.json", factorEntryJSON(t, "fac_ok", true, "Mom20"))
	o := &Options{Strategy: "factor", DataDir: ok}
	if err := runBuild(t, o); err != nil {
		t.Fatalf("有启用条目时必须放行，实得 %v", err)
	}
}

// TestLibraryGateWaiveStillShipsReading 放行面：显式 AllowEmptyLibrary 时不判红，
// 但**成因与零读数照样出门**——放行是"知情选择"，不是"把话咽回去"。
func TestLibraryGateWaiveStillShipsReading(t *testing.T) {
	dir := t.TempDir()
	writeLibraryFile(t, dir, "applied_factors.json", "[]")
	o := &Options{Strategy: "factor", DataDir: dir, AllowEmptyLibrary: true}
	if err := runBuild(t, o); err != nil {
		t.Fatalf("放行后不应报错: %v", err)
	}
	if o.Library.Gate != "waived" {
		t.Fatalf("门状态应为 waived，实得 %q", o.Library.Gate)
	}
	if o.Library.RulesF != 0 || o.Library.ZeroReason == "" {
		t.Fatalf("放行时零读数与成因必须还在，实得 %+v", o.Library)
	}
	// 等值锁的反证：把放行去掉，同一份库必须立刻判红（否则 waived 是恒绿的空锁）。
	o2 := &Options{Strategy: "factor", DataDir: dir}
	if err := runBuild(t, o2); err == nil {
		t.Fatalf("摘掉 AllowEmptyLibrary 后必须判红——放行开关是唯一出口，缺省必须为红")
	}
}

// TestLibraryGateOkReadings 有规则时门放行且三档读数齐（条目/启用/建成），并确认适配器数一致。
func TestLibraryGateOkReadings(t *testing.T) {
	dir := t.TempDir()
	// 两条条目：一条启用、一条停用 —— Entries=2、Enabled=1、Rules=1，三档必须各不相同才说明读数没糊。
	writeLibraryFile(t, dir, "applied_factors.json", "["+
		entryBody(t, factorEntryJSON(t, "fac_7", true, "Mom20"))+","+
		entryBody(t, factorEntryJSON(t, "fac_6", false, "Mom20"))+"]")
	o := &Options{Strategy: "factor", DataDir: dir}
	if err := runBuild(t, o); err != nil {
		t.Fatalf("有启用规则时不应判红: %v", err)
	}
	if o.Library.Gate != "ok" {
		t.Fatalf("门状态应为 ok，实得 %q", o.Library.Gate)
	}
	if o.Library.EntriesF != 2 || o.Library.EnabledF != 1 || o.Library.RulesF != 1 {
		t.Fatalf("三档读数应为 entries=2 enabled=1 rules=1，实得 %+v", o.Library)
	}
	if o.Library.ZeroReason != "" {
		t.Fatalf("非零条时不得伪造成因，实得 %q", o.Library.ZeroReason)
	}
}

// TestLibraryGateAllModeIgnoresBuiltinCount §LIB-GATE 的结构根因反证：
// all 模式的判据必须是**库侧条数**，不是"内置战法也算进去"后的适配器总数——
// 内置五形态恒在，用总数判就永远非零，这正是旧代码那个分支走不到的原因。
func TestLibraryGateAllModeIgnoresBuiltinCount(t *testing.T) {
	dir := t.TempDir()
	writeLibraryFile(t, dir, "applied_factors.json", "[]")
	writeLibraryFile(t, dir, "applied_patterns.json", "[]")
	o := &Options{Strategy: "all", DataDir: dir}
	err := runBuild(t, o)
	if err == nil {
		n, _, berr := o.buildAdapters(nil)
		if berr == nil && len(n) >= len(BuiltinStrategies()) {
			t.Fatalf("all 模式空库必须判红（内置 %d 条在填数，len(ads)=%d 非零＝旧代码失明的那处判据）",
				len(BuiltinStrategies()), len(n))
		}
		t.Fatalf("all 模式空库必须判红，实得 nil（读数 %s）", o.Library.String())
	}
	if !strings.Contains(o.Library.ZeroReason, "factor:no_entries+pattern:no_entries") {
		t.Fatalf("all 模式两侧都空时成因要两侧都点名，实得 %q", o.Library.ZeroReason)
	}
	// 反证二：一侧有规则即整体放行（all 的语义是"库规则+内置"，不许因子侧有货却因为形态侧空而红）。
	writeLibraryFile(t, dir, "applied_factors.json", factorEntryJSON(t, "fac_5", true, "Mom20"))
	o2 := &Options{Strategy: "all", DataDir: dir}
	if err := runBuild(t, o2); err != nil {
		t.Fatalf("一侧有规则时 all 不应判红: %v（读数 %s）", err, o2.Library.String())
	}
	if o2.Library.RulesF != 1 || o2.Library.RulesP != 0 || o2.Library.Gate != "ok" {
		t.Fatalf("all 放行时读数应 rulesF=1 rulesP=0 gate=ok，实得 %+v", o2.Library)
	}
}

// TestLibraryGatePatternSideParity 形态侧对称锁：只跑 pattern 时判红口、成因、读数与因子侧同构
// （只测一侧的门最容易漏对侧——本仓 §ADJ-BASIS-2P 就是因子侧先修、形态侧后补的同型欠账）。
func TestLibraryGatePatternSideParity(t *testing.T) {
	dir := t.TempDir()
	writeLibraryFile(t, dir, "applied_factors.json", factorEntryJSON(t, "fac_4", true, "Mom20"))
	writeLibraryFile(t, dir, "applied_patterns.json", patternEntryJSON(t, "pat_3", false))
	o := &Options{Strategy: "pattern", DataDir: dir}
	err := runBuild(t, o)
	if err == nil {
		t.Fatalf("形态侧零条必须判红（因子侧有货不许替形态侧背书），读数 %s", o.Library.String())
	}
	if o.Library.ZeroReason != "pattern:all_disabled" || o.Library.Gate != "enforced" {
		t.Fatalf("形态侧成因/门应为 all_disabled/enforced，实得 %+v", o.Library)
	}
	if o.Library.RulesF != 0 {
		t.Fatalf("单跑 pattern 时因子侧不该被装配，实得 RulesF=%d", o.Library.RulesF)
	}
	// 反证：同目录下启用形态条目 → 绿。
	writeLibraryFile(t, dir, "applied_patterns.json",
		patternEntryJSON(t, "pat_2", true, map[string]any{"factor": "Mom20", "min": 1, "max": 100}))
	o2 := &Options{Strategy: "pattern", DataDir: dir}
	if err := runBuild(t, o2); err != nil {
		t.Fatalf("形态侧有启用条目时不应判红: %v", err)
	}
	if o2.Library.RulesP != 1 || o2.Library.Gate != "ok" {
		t.Fatalf("形态侧读数应为 rulesP=1 gate=ok，实得 %+v", o2.Library)
	}
}

// TestLibraryGateNotApplicablePaths 两条"本来就不读库"的路径必须为绿，且读数标得不一样：
// 单内置战法回放 = not_applicable；候选直读 = candidate_direct_exempt。
// 混成一个词的话，日后想锁"库门不许被绕过"就得连这两条正常路径一起摘掉。
func TestLibraryGateNotApplicablePaths(t *testing.T) {
	// 空目录（连文件都没有）也不许把单内置战法回放判红——它压根不声明库覆盖。
	o := &Options{Strategy: "double_bump", DataDir: t.TempDir()}
	if err := runBuild(t, o); err != nil {
		t.Fatalf("单内置战法回放不受库门约束: %v", err)
	}
	if o.Library.Gate != "not_applicable" {
		t.Fatalf("单内置战法门状态应为 not_applicable，实得 %q", o.Library.Gate)
	}
	// 候选直读需要研究库行，本用例只验门标签：CandidateID>0 且 strategy=pattern 时分支先落标签，
	// 之后读候选失败报的是"读取候选"错，绝不能报成库门红（那会把人引去查库文件）。
	o2 := &Options{Strategy: "pattern", CandidateID: 999, DataDir: t.TempDir()}
	err := runBuildWithDB(t, o2, newMinuteReplayDB(t))
	if err == nil || !strings.Contains(err.Error(), "读取候选") {
		t.Fatalf("候选直读应报读候选失败而不是库门红，实得 %v", err)
	}
	if o2.Library.Gate != "candidate_direct_exempt" {
		t.Fatalf("候选直读门状态应为 candidate_direct_exempt，实得 %q", o2.Library.Gate)
	}
	if o2.Library.ZeroReason != "" {
		t.Fatalf("候选直读不得伪造库零条成因，实得 %q", o2.Library.ZeroReason)
	}
}

// runBuildWithDB 候选直读分支要真读一行候选（store.(*DB) 的 nil 接收者会段错误），故单开一口。
func runBuildWithDB(t *testing.T, o *Options, db *store.DB) error {
	t.Helper()
	if o.Start == "" {
		o.Start = "20230801"
		o.End = "20230810"
	}
	_, _, err := o.buildAdapters(db)
	return err
}

// TestDirFromClassification 目录来源三档各判一次（"读到了什么"必须连"从哪读的"一起出门）。
func TestDirFromClassification(t *testing.T) {
	if got := resolveDirFrom(""); got != "unset" {
		t.Fatalf("空目录应判 unset，实得 %q", got)
	}
	home := DefaultDataDir()
	if got := resolveDirFrom(home); got != "home_default" {
		t.Fatalf("机器缺省目录应判 home_default，实得 %q（缺省目录没带上线上库正是那次数值误用的现场）", got)
	}
	other := t.TempDir()
	if got := resolveDirFrom(other); got != "explicit" {
		t.Fatalf("临时目录应判 explicit，实得 %q", got)
	}
	// env 档：QUANT_DATA_DIR 与实参等值时优先判 env（顺序不能反：env 在 home_default 之前判）。
	t.Setenv("QUANT_DATA_DIR", other)
	if got := resolveDirFrom(other); got != "env" {
		t.Fatalf("环境变量等值时应判 env，实得 %q", got)
	}
}

// TestLibraryReadingsShipInText 出门面：三档读数和门状态必须进 String()/近似说明文本，
// 且**没有库门读数的报告不许把动量的兄弟集写成"跑过库规则"**。
func TestLibraryReadingsShipInText(t *testing.T) {
	l := LibraryLoad{Dir: "/tmp/x", DirFrom: "explicit", EntriesF: 3, EnabledF: 2, RulesF: 2,
		EntriesP: 1, EnabledP: 1, RulesP: 1, Gate: "ok"}
	s := l.String()
	for _, must := range []string{"/tmp/x", "explicit", "因子=2", "形态=1", "文件条目 3/1", "启用 2/1", "门=ok"} {
		if !strings.Contains(s, must) {
			t.Fatalf("出门行缺字段 %q，实得 %q", must, s)
		}
	}
	// 零条时成因必须替掉那串没意义的读数（读的人只要知道为什么没东西）。
	z := LibraryLoad{Dir: "/tmp/x", DirFrom: "home_default", Gate: "enforced", ZeroReason: "file_missing"}
	zs := z.String()
	if !strings.Contains(zs, "成因=file_missing") || !strings.Contains(zs, "门=enforced") {
		t.Fatalf("零条出门行必须带成因与门状态，实得 %q", zs)
	}
	// 动量近似说明必须带上"这轮真吃到了几条兄弟"——两次 all 回放差 2.1× 的唯一可核线索。
	o := &Options{Library: LibraryLoad{Dir: "/data/applied", RulesF: 3, Gate: "ok"}}
	o.fallbackPeers = []adapter{&ruleEvalAdapter{name: "a"}, &ruleEvalAdapter{name: "b"}, &ruleEvalAdapter{name: "c"}}
	n := o.approxNote("momentum")
	for _, must := range []string{"sibling set actually loaded this run: 3", "factor=3", "gate=ok", "/data/applied"} {
		if !strings.Contains(n, must) {
			t.Fatalf("动量近似说明缺 %q，实得 %q", must, n)
		}
	}
	// 反证：非动量战法的说明不许被这段追加污染（否则"近似口径"标签会挂到完整回放上）。
	if n2 := o.approxNote("double_bump"); n2 != "" {
		t.Fatalf("完整回放战法不应带近似说明，实得 %q", n2)
	}
}
