// survey_test.go — strategy-survey 命令层自动化测试：用 2 只股票的临时研究库
// （ths 双表路由，纯内存装配，不碰任何真实数据目录）驱动一次完整排摸，校验
// strategy_survey.json 产物契约：
//
//	a) 每条被排摸战法一条记录（内置五形态含动量 + 库内两条 fac_*），verdict 落在枚举集内；
//	c) 分层差为零的成分因子（EP_ttm 在无 daily_basic 的库里全 NaN）判 dead_component；
//	c2) 成分健康度按**条目自身 horizon** 度量（fac_t2 落库 10 ⇒ 尺子 10，全局 --h 5 不改写它）；
//	d) 存储 adj_basis ≠ 当前口径（及缺字段）的条目 stale_basis=true；
//	e) 白名单与默认回放集合的差集以 unsurveyable 计数显形：2026-09-24 动量判据按实盘语义重写后
//	   差集为空（计数须等于 UnsurveyedLiveForms 的真实长度），但分类机制仍在测（未实现的 ID 仍报
//	   no_replay_adapter）；动量必须在表里、enabled=true、notes 带近似口径说明（日线 MACD 代分钟）。
//
// English: end-to-end test of the strategy-survey command on a 2-stock temp DB —
// record-per-strategy contract, dead-component flagging on zero layer spread,
// and stale_basis detection from the raw adj_basis JSON field.
package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/btreplay"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// fixtureTradingDays 生成 n 个工作日（周一~周五）的 YYYYMMDD 序列。
func fixtureTradingDays(start time.Time, n int) []string {
	out := make([]string, 0, n)
	d := start
	for len(out) < n {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			out = append(out, d.Format("20060102"))
		}
		d = d.AddDate(0, 0, 1)
	}
	return out
}

// buildSurveyFixtureDB 2 只股票 × 160 个交易日的临时研究库：
// 走 ths 双表路由（有导出的 Upsert 写入口），一只缓涨一只缓跌，保证动量因子有截面差异。
func buildSurveyFixtureDB(t *testing.T) (*store.DB, string, []string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "trading.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// 研究库路由装配为 ths 双表（测试结束复位，避免污染同包其他测试）。
	store.ConfigureSource("hithink", true)
	t.Cleanup(func() { store.ConfigureSource("", false) })

	dates := fixtureTradingDays(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), 160)
	var daily []store.ThsDailyRow
	var factors []store.ThsAdjFactorRow
	mk := func(tsCode string, growth float64) {
		// 指数趋势 + 正弦扰动：纯线性价格会让 Mom20 单调递减（末值分位恒 0、永不触发），
		// 必须让动量序列有起伏才能驱动回放路径。
		for i, dt := range dates {
			p := 10 * math.Pow(1+growth, float64(i)) * (1 + 0.02*math.Sin(float64(i)/7.0))
			open := p * 0.998
			high := p * 1.004
			low := open * 0.996
			daily = append(daily, store.ThsDailyRow{
				TsCode: tsCode, TradeDate: dt, Open: open, High: high, Low: low, Close: p,
				Vol: 1e6 + float64(i)*1000, Amount: 1e6 * p,
			})
			factors = append(factors, store.ThsAdjFactorRow{TsCode: tsCode, TradeDate: dt, Factor: 1.0})
		}
	}
	mk("600001.SH", 0.0015) // 缓涨+波动：Mom20 分位有高点 → fac_t1 有触发
	mk("600002.SH", -0.001) // 缓跌：提供截面反向
	if _, err := db.UpsertThsDailyRows(daily); err != nil {
		t.Fatalf("insert ths_daily: %v", err)
	}
	if _, err := db.UpsertThsAdjFactorRows(factors); err != nil {
		t.Fatalf("insert ths_adj_factor: %v", err)
	}
	return db, dbPath, []string{"600001.SH", "600002.SH"}, dates[len(dates)-1]
}

// buildSurveyLibrary 战法库目录：两条因子条目（一启用一停用、其中一条带过期 adj_basis）。
func buildSurveyLibrary(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	entries := []map[string]any{
		{
			"id": "fac_t1", "name": "survey-test-1", "enabled": true, "candidate_id": 901,
			"factors": []string{"Mom20"}, "weights": map[string]float64{"Mom20": 1},
			"directions": map[string]int{"Mom20": 1}, "buy_threshold": 55, "horizon": 5,
			"excess": 0.01, // 落库历史期望 +1%（flipped_sign 判据的"前值"）
		},
		{
			"id": "fac_t2", "name": "survey-test-2", "enabled": false, "candidate_id": 902,
			// horizon=10：条目自己的前瞻期与全局 --h 5 不同，用于锁「尺子跟条目走」§SURVEY-HORIZON
			"factors":       []string{"Mom20", "EP_ttm"},
			"weights":       map[string]float64{"Mom20": 0.5, "EP_ttm": 0.5},
			"directions":    map[string]int{"Mom20": 1, "EP_ttm": 1},
			"buy_threshold": 55, "horizon": 10,
			"adj_basis": "some-old-basis-0", // ≠ research.AdjBaselineVersion → stale
		},
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "applied_factors.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func TestStrategySurveyArtifact(t *testing.T) {
	db, dbPath, codes, lastDate := buildSurveyFixtureDB(t)
	dataDir := buildSurveyLibrary(t)

	poolFile := filepath.Join(t.TempDir(), "codes.txt")
	pool := ""
	for _, c := range codes {
		pool += c + "\n"
	}
	if err := os.WriteFile(poolFile, []byte(pool), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), "out")

	cmdStrategySurvey(db, dbPath, []string{
		"-start", "20240101", "-end", lastDate,
		"-h", "5", "-quantiles", "2", "-min-stocks", "2",
		"-codes", poolFile, "-applied", dataDir, "-out", outDir,
		"-min-spread", "0.5",
	})

	raw, err := os.ReadFile(filepath.Join(outDir, "strategy_survey.json"))
	if err != nil {
		t.Fatalf("读排摸产物失败: %v", err)
	}
	var art surveyArtifact
	if err := json.Unmarshal(raw, &art); err != nil {
		t.Fatalf("产物非合法 JSON: %v", err)
	}

	// (a) 一策略一记录：内置五形态（含动量近似回放）+ 库内两条（含停用条目），且 verdict 全在枚举集内。
	wantIDs := map[string]bool{
		"double_bump": true, "dragon": true, "dragon_return": true, "n_shape": true,
		"momentum": true, "fac_t1": true, "fac_t2": true,
	}
	seen := map[string]surveyRecord{}
	enumOK := map[string]bool{"ok": true, "no_edge": true, "flipped_sign": true, "no_trigger": true, "stale_params": true}
	for _, r := range art.Records {
		if !wantIDs[r.ID] {
			t.Errorf("意外记录 %s", r.ID)
		}
		if !enumOK[r.Verdict] {
			t.Errorf("记录 %s verdict=%q 不在枚举集内", r.ID, r.Verdict)
		}
		seen[r.ID] = r
	}
	for id := range wantIDs {
		if _, ok := seen[id]; !ok {
			t.Errorf("缺少记录 %s（得到 %d 条）", id, len(art.Records))
		}
	}
	if art.AdjBasisCurrent != research.AdjBaselineVersion {
		t.Errorf("口径基准应为 %s，得到 %s", research.AdjBaselineVersion, art.AdjBasisCurrent)
	}

	// 回放走通了：启用条目须有触发（600001 缓涨 + 门槛 55），且期望为正。
	f1 := seen["fac_t1"]
	if f1.Replay.Signals == 0 {
		t.Errorf("fac_t1 零触发，fixture 未能驱动回放路径")
	}
	if f1.Replay.ExpectancyPct <= 0 {
		t.Errorf("fac_t1 期望应为正（缓涨池），得到 %.2f", f1.Replay.ExpectancyPct)
	}
	// 停用的 fac_t2 也被排摸（全库口径），但允许零触发。
	if seen["fac_t2"].Enabled {
		t.Errorf("fac_t2 应保持库内 enabled=false 原样透出")
	}

	// (c) 分层差为零的成分因子判 dead_component：
	// EP_ttm 是估值因子，本 fixture 无 daily_basic → 全 NaN → spread=0 → 死。
	var ep *surveyComponent
	for i := range seen["fac_t2"].Components {
		if seen["fac_t2"].Components[i].Factor == "EP_ttm" {
			ep = &seen["fac_t2"].Components[i]
		}
	}
	if ep == nil {
		t.Fatal("fac_t2 成分明细缺 EP_ttm")
	}
	if !ep.DeadComponent || ep.SpreadPP != 0 {
		t.Errorf("零分层差成分应判 dead_component，得到 %+v", ep)
	}

	// (c2) §SURVEY-HORIZON 尺子跟条目走：fac_t2 落库 horizon=10 ⇒ 其成分必须按 10 日前瞻度量，
	// 即使本次命令行传的是 --h 5（拿 5 日尺子量 10 日战法会把健康成分误判成死成分——
	// 现网 fac_117/fac_118 的"死成分"结论当初就是这么量出来的）。
	if art.Window.Horizon != 5 {
		t.Fatalf("全局前瞻档应为 5（命令行 --h），得到 %d", art.Window.Horizon)
	}
	for _, c := range seen["fac_t2"].Components {
		if c.Horizon != 10 {
			t.Errorf("fac_t2 成分 %s 应按条目 horizon=10 度量，得到 %d", c.Factor, c.Horizon)
		}
	}
	for _, c := range f1.Components {
		if c.Horizon != 5 {
			t.Errorf("fac_t1（horizon=5）成分 %s 度量档应为 5，得到 %d", c.Factor, c.Horizon)
		}
	}

	// (d) adj_basis ≠ 当前口径 → stale_basis=true；缺字段（fac_t1）同样判 stale。
	f2 := seen["fac_t2"]
	if !f2.StaleBasis || f2.AdjBasis != "some-old-basis-0" {
		t.Errorf("fac_t2 应 stale_basis=true，得到 %+v", f2)
	}
	if !f1.StaleBasis {
		t.Errorf("fac_t1 无 adj_basis 字段亦应判 stale（旧口径拟合）")
	}

	// fac_t1 回放期望为正 + stale → verdict=stale_params（判据顺序的定点回归）。
	if f1.Verdict != "stale_params" {
		t.Errorf("fac_t1 verdict=%q, want stale_params", f1.Verdict)
	}

	// 锚点一致性：unhealthy = 非 ok 记录数（内置五法在此 fixture 上不可能全 ok）。
	nBad := 0
	for _, r := range art.Records {
		if r.Verdict != "ok" {
			nBad++
		}
	}
	if art.Unhealthy != nBad {
		t.Errorf("unhealthy=%d 与非 ok 记录数 %d 不一致", art.Unhealthy, nBad)
	}

	// §LIB-GATE ①：这批数字的前提（本轮回放到底吃到了几条线上战法）必须随产物出门，
	// 而且必须是**这一轮真的读出来的**——不是事后补的漂亮字符串。fixture 库里有两条因子条目
	// （一条停用）且没有形态库文件，所以读数只能是 factor 侧 2/1/2、pattern 侧 0/0/0。
	// 三个数各管一件事：entries=文件里有几条、enabled=其中启用几条、rules=真建成适配器几条
	// （排摸带 IncludeDisabled=true，故停用条目也进 rules；生产回放那条链路 rules 只会数启用条目）。
	if art.Library.Gate != "ok" {
		t.Errorf("库门状态=%q，期望 ok（有可用规则却报别的＝门自己读错了）", art.Library.Gate)
	}
	if art.Library.Dir != dataDir {
		t.Errorf("库目录=%q，期望回显本轮实际使用的 %q（读者要能核到目录）", art.Library.Dir, dataDir)
	}
	if art.Library.EntriesF != 2 || art.Library.EnabledF != 1 || art.Library.RulesF != 2 {
		t.Errorf("factor 侧三段读数=%d/%d/%d，期望 条目/启用/建成=2/1/2（停用条目在排摸口径里也建适配器）",
			art.Library.EntriesF, art.Library.EnabledF, art.Library.RulesF)
	}
	if art.Library.EntriesP != 0 || art.Library.EnabledP != 0 || art.Library.RulesP != 0 {
		t.Errorf("pattern 侧三段读数=%d/%d/%d，期望 0/0/0（fixture 没写形态库文件）",
			art.Library.EntriesP, art.Library.EnabledP, art.Library.RulesP)
	}
	// 门=ok 时成因必须留空：把"没成因"写成字符串会让巡检脚本把一轮正常回放读成降级。
	if art.Library.ZeroReason != "" {
		t.Errorf("门=ok 时 zero_reason 应为空，得到 %q", art.Library.ZeroReason)
	}
	// 锚点行必须与运维脚本的正锁逐字符对得上（脚本按这条 grep 取值，格式漂移＝读不到＝判链没接）。
	if got := libraryAnchorLine(art.Library); !surveyLibraryAnchorRE.MatchString(got) {
		t.Errorf("survey_library 锚点行不合运维正则:\n%s", got)
	}

	// 盲区锚点 §SURVEY-COVERAGE：白名单在跑、但默认回放集合量不到的形态战法必须被计数。
	// 期望值取 btreplay.UnsurveyedLiveForms() 的**真实差集**而非硬编码数字：锚点的契约是
	// "产物里的计数等于差集"，差集内容由 btreplay 侧的三份清单（白名单 / 有适配器 / 默认停用）算出。
	// 2026-09-24 §MOMENTUM-LIVE-REPLAY 起差集为空（动量判据已按实盘语义重写、真进回放）。
	realMissing := btreplay.UnsurveyedLiveForms()
	if art.Unsurveyable != len(realMissing) {
		t.Errorf("unsurveyable=%d 与真实差集 %d（%v）不一致——产物计数与 UnsurveyedLiveForms 脱钩",
			art.Unsurveyable, len(realMissing), realMissing)
	}
	if art.Unsurveyable != len(art.UnsurveyableIDs) {
		t.Errorf("unsurveyable=%d 与 ID 清单 %v 不一致", art.Unsurveyable, art.UnsurveyableIDs)
	}
	// 状态必须可分辨："没写适配器"（要补代码）与"写好但默认停用"（要么按实盘语义重写判据、
	// 要么承认量不了）是两种处置，压成同一个数字就会派错工。差集空了不代表分类器可以烂掉，
	// 拿一个不存在的 ID 直接敲它（否则这段分支永不自然执行，机制死了也测不出）。
	if got := btreplay.UnsurveyedLiveFormStatus("form_that_nobody_implemented"); got != "no_replay_adapter" {
		t.Errorf("未实现战法的状态=%q，期望 no_replay_adapter（盲区分类机制失效）", got)
	}
	if got := btreplay.UnsurveyedLiveFormStatus("momentum"); got != "" {
		t.Errorf("momentum 的缺失状态=%q，期望空串（判据已按实盘语义重写、默认参与回放）", got)
	}
	if got := btreplay.UnsurveyedLiveFormStatus("double_bump"); got != "" {
		t.Errorf("double_bump 默认就在回放集合里，状态应为空串，得到 %q", got)
	}
	// 动量必须仍在表里、且这一行是"真跑出来的"（enabled=true）：一行都没有等于盲区换个形态出现，
	// enabled 说谎则运维把"停用没跑"读成"跑过了"。数字本身随 fixture 的行情走，不在此硬编。
	mo, ok := seen["momentum"]
	if !ok {
		t.Error("records 缺 momentum：动量从排摸表里消失了，盲区会伪装成「没这个战法」")
	} else if mo.Kind != "builtin" || mo.Name != "动量" {
		t.Errorf("momentum 记录应为内置战法、显示名「动量」（名称不依赖回放结果），得到 %+v", mo)
	} else if !mo.Enabled {
		t.Errorf("momentum 记录 enabled=false，与「判据已按实盘语义重写、默认回放」不符")
	}
	// 近似口径仍须随行落产物：动量虽然进了回放，用的仍是**日线 MACD 代替分钟 MACD**、
	// 每日判一次代替盘中多轮，读数字的人必须看得见这层近似（否则会和 double_bump 那种
	// 纯日K完整回放的数字直接比大小）。
	if note := btreplay.ReplayApproxNote("momentum"); note == "" {
		t.Error("ReplayApproxNote(momentum) 为空，近似口径说明被摘")
	} else if !strings.Contains(mo.Notes, note) {
		t.Errorf("momentum 记录 notes 未带近似口径说明\n got: %s\nwant 含: %s", mo.Notes, note)
	}
	// 已被实盘语义重写的事实要写在说明里，且不得再留着"默认停用/恒不触发"的旧口径文本
	// （旧文本会让运维把这一行读回"没量"，而它现在是真的量过了）。
	if note := btreplay.ReplayApproxNote("momentum"); !strings.Contains(note, "criteria rewritten to live semantics") {
		t.Error("momentum 近似说明未声明「判据已按实盘语义重写」")
	} else if strings.Contains(note, "not replayed by default") {
		t.Error("momentum 近似说明仍带「not replayed by default」旧口径")
	}
	// 纯日K完整回放的战法不得被挂上近似/停用说明（否则这类标签失去区分力）。
	if bb, okBump := seen["double_bump"]; okBump &&
		(strings.Contains(bb.Notes, "approx replay") || strings.Contains(bb.Notes, "not replayed by default")) {
		t.Errorf("double_bump 是完整回放，不应带近似/停用说明：%s", bb.Notes)
	}
	// 只有"根本没写适配器"的战法才允许缺席 records（它想出现也没数据）；
	// "有适配器但停用"的必须出现（见上）——两种缺失在表里的形态本就不同，别写成一条判据。
	for _, id := range art.UnsurveyableIDs {
		if btreplay.UnsurveyedLiveFormStatus(id) == "no_replay_adapter" {
			if _, ok := seen[id]; ok {
				t.Errorf("无适配器的战法 %s 不该出现在排摸表里（混进去就是编了一个没跑过的战法）", id)
			}
		}
	}
}

// TestSurveyHorizonRuler §SURVEY-HORIZON 尺子选择的纯函数定点测试：同一个因子在 5 日与 10 日
// 前瞻下是两份结论——若恒用全局 --h，10 日战法的成分会被 5 日尺子误判成死成分（现网 fac_117/118
// 的"死成分"就是这么来的）。这里把"查表键含档位 + 档位来源 + 阈值命中"三件事钉在一起测。
func TestSurveyHorizonRuler(t *testing.T) {
	report := map[string]*research.FactorReport{
		// Brk60：5 日几乎无边际（0.2pp），10 日有 2.4pp ⇒ 只有按条目档位度量才判得对。
		reportKey("Brk60", 5):  {ID: "Brk60", Layers: []research.LayerSummary{{MeanReturn: 0}, {MeanReturn: 0.002}}},
		reportKey("Brk60", 10): {ID: "Brk60", Layers: []research.LayerSummary{{MeanReturn: 0}, {MeanReturn: 0.024}}},
	}
	comps5, dead5 := componentHealth([]string{"Brk60"}, 5, report, 0.5)
	if dead5 != 1 || !comps5[0].DeadComponent {
		t.Errorf("5 日尺子下应判死成分，得到 %+v dead=%d", comps5, dead5)
	}
	comps10, dead10 := componentHealth([]string{"Brk60"}, 10, report, 0.5)
	if dead10 != 0 || comps10[0].DeadComponent {
		t.Errorf("10 日尺子下不该判死成分，得到 %+v dead=%d", comps10, dead10)
	}
	if comps10[0].Horizon != 10 {
		t.Errorf("成分明细须如实标出所用尺子（horizon=10），得到 %+v", comps10[0])
	}
	// 未注册 / 无该档报告 ⇒ Registered=false 且判死（"没有证据"与"证据为零"同义）。
	if c, d := componentHealth([]string{"Nope"}, 10, report, 0.5); d != 1 || c[0].Registered {
		t.Errorf("缺档位报告应判死成分且 registered=false，得到 %+v dead=%d", c, d)
	}
	// 档位来源：条目自身 horizon 优先；0/负值视为未记录，退回全局，全局也坏才退到 5。
	if got := entryHorizon(10, 5); got != 10 {
		t.Errorf("entryHorizon(10,5)=%d, want 10", got)
	}
	if got := entryHorizon(0, 7); got != 7 {
		t.Errorf("entryHorizon(0,7)=%d, want 7（条目未记录 ⇒ 用全局）", got)
	}
	if got := entryHorizon(-1, 0); got != 5 {
		t.Errorf("entryHorizon(-1,0)=%d, want 5（双坏值兜底，不能让尺子变成 0 日）", got)
	}
}

// TestSurveyHelpers 档位去重与小工具行为：排摸只为实际用到的几档各算一次 Summarize
// （全池每档都是分钟级开销，重复档位就是白跑）。
func TestSurveyHelpers(t *testing.T) {
	xs := []int{10, 5, 10}
	for _, v := range []int{5, 10} {
		if !containsInt(xs, v) {
			t.Errorf("containsInt(%v,%d) 应为 true", xs, v)
		}
	}
	if containsInt(xs, 7) {
		t.Errorf("containsInt(%v,7) 应为 false", xs)
	}
	got := []int{10, 3, 7, 1}
	sortInts(got)
	if got[0] != 1 || got[3] != 10 || got[1] != 3 || got[2] != 7 {
		t.Errorf("sortInts 结果应升序，得到 %v", got)
	}
	// 查表键形态是产物契约的一部分（同一因子不同档位 = 两份结论），改动会连带 verify 锁失效。
	if reportKey("Brk60", 10) != "Brk60|h10" {
		t.Errorf("reportKey 形态变了，应为 Brk60|h10，得到 %s", reportKey("Brk60", 10))
	}
}

// TestSurveyVerdictRule verdict 判据纯函数单测：五种枚举各命中一次（含优先级边界）。
func TestSurveyVerdictRule(t *testing.T) {
	cases := []struct {
		signals        int
		expr, prior    float64
		stale, builtin bool
		want           string
	}{
		{0, 5, 5, true, false, "no_trigger"},            // 零触发优先级最高（即便 stale）
		{10, -0.29, 1.13, false, false, "flipped_sign"}, // 龙头 A/B 实录：+1.13% → -0.29%
		{10, -0.5, 0, false, false, "no_edge"},          // 无历史记录的正向可比 → 直接无边际
		{10, 0.8, 0.5, true, false, "stale_params"},     // 仍赚钱但旧口径拟合
		{10, 0.8, 0.5, false, false, "ok"},
		{10, 0.8, 0, true, true, "ok"}, // 内置战法不吃 stale 规则
	}
	for i, c := range cases {
		if got := verdict(c.signals, c.expr, c.prior, c.stale, c.builtin); got != c.want {
			t.Errorf("case %d: verdict(%d,%.2f,%.2f,%v,%v)=%s, want %s",
				i, c.signals, c.expr, c.prior, c.stale, c.builtin, got, c.want)
		}
	}
}

// surveyLibraryAnchorRE 与 scripts/survey_live_rules.sh 里那条 grep -E 逐字符同源
// （§95 静态锁两侧必须同时出现这串正则）。为什么要在 Go 侧再存一份：巡检脚本按前缀取值，
// 格式一漂移脚本只是"读不到"，而读不到在这套纪律里等于"这条链没接"——那是比红更坏的静默。
var surveyLibraryAnchorRE = regexp.MustCompile(
	`^survey_library gate=[a-z_]+ factor_rules=[0-9]+ pattern_rules=[0-9]+ entries=[0-9]+/[0-9]+ enabled=[0-9]+/[0-9]+ dir_from=[a-z_]+ zero_reason=`)

// TestLibraryAnchorLineContract 锚点行纯函数单测：三种门态各命中一次，且**零值也要出行**。
// English: contract test for the survey_library anchor — every gate state emits a line, and
// an all-zero reading still prints a line (missing line means "not wired", not "zero").
func TestLibraryAnchorLineContract(t *testing.T) {
	// ① 正常态：五段读数逐字段等值（不是"含关键字"这种松断言）。
	got := libraryAnchorLine(btreplay.LibraryLoad{
		Dir: "/tmp/x", DirFrom: "explicit", EntriesF: 3, EntriesP: 1,
		EnabledF: 2, EnabledP: 0, RulesF: 2, RulesP: 1, Gate: "ok",
	})
	want := "survey_library gate=ok factor_rules=2 pattern_rules=1 entries=3/1 enabled=2/0 dir_from=explicit zero_reason=none"
	if got != want {
		t.Errorf("锚点行等值不符\n got: %s\nwant: %s", got, want)
	}
	if !surveyLibraryAnchorRE.MatchString(got) {
		t.Errorf("正常态锚点行不合运维正则: %s", got)
	}
	// ② 零值（旧库文件/字段缺失时 JSON 解出的就是它）：必须出行，缺的字段填 none，
	//    绝不能因为"没数据"就返回空串——空串在打印侧极易被 if 掉。
	zero := libraryAnchorLine(btreplay.LibraryLoad{})
	if !surveyLibraryAnchorRE.MatchString(zero) {
		t.Errorf("零值锚点行不合运维正则: %q（零也照样打这一行是契约）", zero)
	}
	if !strings.Contains(zero, "gate=none") || !strings.Contains(zero, "zero_reason=none") {
		t.Errorf("零值锚点行应把空字段填成 none，得到 %s", zero)
	}
	// ③ 判红态：成因原样带上（含 `+` 与 `:`，运维正则的零_reason= 前缀不许被吃掉）。
	red := libraryAnchorLine(btreplay.LibraryLoad{
		DirFrom: "unset", Gate: "enforced", ZeroReason: "factor:file_missing+pattern:all_disabled",
	})
	if !surveyLibraryAnchorRE.MatchString(red) {
		t.Errorf("判红态锚点行不合运维正则: %s", red)
	}
	if !strings.Contains(red, "zero_reason=factor:file_missing+pattern:all_disabled") {
		t.Errorf("判红态锚点行丢了成因: %s", red)
	}
	// ④ 反证：正则自己不是恒绿——把 dir_from 段摘掉必须不匹配（否则 ①②③ 都在给摆设打分）。
	broken := strings.Replace(red, " dir_from=unset", "", 1)
	if surveyLibraryAnchorRE.MatchString(broken) {
		t.Errorf("运维正则成了摆设：缺 dir_from 段的行仍匹配\n%s", broken)
	}
}
