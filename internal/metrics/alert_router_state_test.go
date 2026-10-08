// alert_router_state_test.go — §W7-E（2026-10-06 修复批 波 7）告警路由状态跨重启持久化的行为锁。
//
// 六把尺子（对应 FIX_PLAN_20261006 的 P26/P27 + 落盘自身的三条承重件 + 装配入口一条）：
//
//	P26  跨日 + 重启 ⇒ 重启后首个 tick 补发**前一日**汇总，标题日期是停机那天而不是重启那天；
//	P26b 同一日重启既不凭空造汇总，也不把当日计数/峰值清零；
//	P27  被恢复冷却窗挂起的销案活过重启，仍成对补发，且补发后不再重复补；
//	P27b announced（已报未销标记）活过重启——丢它比丢 pendingR 更糟（recover 被当孤立销案吞掉，
//	     p1 从此只开不销）；同一用例保留"不灌状态就没有销案"的反证腿，证明承重件是 rehydrate；
//	落盘 损坏/版本不符 ⇒ 退回空态且日志可见；内容未变不重复写、内容变了必须重写
//	     （"跳过写盘"一旦放宽成"按时间跳过"，本缺陷换个马甲回来）；
//	接线 SetAlertRouterStatePath 这个装配入口本身要真走一次（只测实例方法＝有实现没接线）。
//
// 时钟全部注入、绝不 sleep；"重启"= 新建路由器 + 读同一份 journal（进程内存不存在）。
package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// journalPath 在临时目录里取一个状态文件路径（各用例互不串台）。
func journalPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "alert_router_state.json")
}

// restartRouter 模拟进程重启：新建一个用新时钟的路由器，只靠 journal 把状态带过来。
// 刻意不复用停机前那个实例的任何字段——复用就等于把要验的东西先送进去。
func restartRouter(path string, at time.Time) (*alertRouter, *fakeClock) {
	clk := &fakeClock{t: at}
	r := newAlertRouter(clk.now)
	r.setStatePath(path)
	return r, clk
}

// readStateFile 读回落盘快照（断言磁盘上真有什么，而不是内存里"应该写了"）。
func readStateFile(t *testing.T, path string) AlertRouterState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读状态文件: %v", err)
	}
	var snap AlertRouterState
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("状态文件不是合法 JSON: %v（前 80 字节：%q）", err, head(data, 80))
	}
	return snap
}

// head 截前 n 字节用于报错文案（整份打进日志会把正文刷屏）。
func head(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

// fixedRouter 固定时钟路由器（不需要推进时钟时用，省掉一个未使用变量）。
func fixedRouter(at time.Time) *alertRouter {
	clk := &fakeClock{t: at}
	return newAlertRouter(clk.now)
}

// TestDailySummarySurvivesRestart P26：日汇总活过重启。
// 停机前（9-22 23:00）只攒了桶、还没跨日；重启落在 9-23 09:00。
// 旧实现在这里会把 9-22 整天直接吞掉：r.day 是空串，首轮 tick 只是把 day 设成今天就返回。
func TestDailySummarySurvivesRestart(t *testing.T) {
	path := journalPath(t)
	// 停机前那一段走真实 Route——落盘必须由生产路径写出来，测试不许手拼 JSON。
	before, _ := newTestRouter(dayStart(2026, 9, 22, 23, 0))
	before.setStatePath(path)
	if got := before.Route([]AlertEvent{fireEv("llm_cooldown", "p2", 7)}); len(got) != 0 {
		t.Fatalf("日汇总类不该即时出站，got %+v", got)
	}

	after, _ := restartRouter(path, dayStart(2026, 9, 23, 9, 0))
	got := after.Route(nil)
	if len(got) != 1 || got[0].Kind != KindDailySummary {
		t.Fatalf("重启后首个 tick 应补发 1 条前一日汇总，got %+v", got)
	}
	if !strings.Contains(got[0].Title, "2026-09-22") {
		t.Errorf("汇总标题应带停机那天的日期（按记录的日期标注），got %q", got[0].Title)
	}
	if strings.Contains(got[0].Title, "2026-09-23") {
		t.Errorf("标题写成重启当天＝把补发冒充成当天汇总，got %q", got[0].Title)
	}
	if !strings.Contains(got[0].Body, "llm_cooldown") || !strings.Contains(got[0].Body, "触发 1 次") {
		t.Errorf("正文应带回停机前攒下的规则与计数，got %q", got[0].Body)
	}
	if got[0].FireCount != 1 {
		t.Errorf("FireCount=%d，期望 1（跨重启不丢聚合）", got[0].FireCount)
	}
	// 补发完必须清空桶，否则再来一次重启会重复补同一条（推送不能双份）。
	if again := after.Route(nil); len(again) != 0 {
		t.Fatalf("汇总只该补发一次，重复出站 got %+v", again)
	}
}

// TestPendingResolveSurvivesRestart P27：被恢复冷却窗挂起的销案活过重启，仍成对补发。
//
// 先把"挂起"这个形态真实造出来（不是假设它能出现）：
// 10:00 报 ⇒ alert 出站（lastF=10:00，announced=true）
// 10:25 销 ⇒ resolved 出站（announced=false，lastR=10:25）
// 10:31 再报 ⇒ 距 lastF 满 30 分钟触发窗 ⇒ 第二条 alert 出站（announced=true，lastF=10:31）
// 10:33 再销 ⇒ 距 lastR 仅 8 分钟 < 恢复窗 10 分钟 ⇒ 挂起 pendingR（本例要防的就是这一刻停机）
func TestPendingResolveSurvivesRestart(t *testing.T) {
	path := journalPath(t)
	before, clk := newTestRouter(dayStart(2026, 9, 22, 10, 0))
	before.setStatePath(path)
	if got := before.Route([]AlertEvent{fireEv("quote_stale", "p1", 1)}); len(got) != 1 || got[0].Kind != KindAlert {
		t.Fatalf("首次破线应出 alert，got %+v", got)
	}
	clk.advance(25 * time.Minute)
	if got := before.Route([]AlertEvent{recoverEv("quote_stale", "p1", 0)}); len(got) != 1 || got[0].Kind != KindResolved {
		t.Fatalf("首次恢复应出 resolved，got %+v", got)
	}
	clk.advance(6 * time.Minute)
	if got := before.Route([]AlertEvent{fireEv("quote_stale", "p1", 1)}); len(got) != 1 || got[0].Kind != KindAlert {
		t.Fatalf("触发冷却窗到期后应再出一条 alert（挂起形态的前提），got %+v", got)
	}
	clk.advance(2 * time.Minute)
	if got := before.Route([]AlertEvent{recoverEv("quote_stale", "p1", 0)}); len(got) != 0 {
		t.Fatalf("恢复冷却窗内应挂起而非出站，got %+v", got)
	}
	if len(before.pendingR) != 1 {
		t.Fatalf("夹具没造出挂起形态，pendingR=%+v", before.pendingR)
	}
	snap := readStateFile(t, path)
	if e, ok := snap.PendingResolve["quote_stale"]; !ok || e.Kind != "recover" {
		t.Fatalf("挂起的销案没落盘，pending_resolve=%+v", snap.PendingResolve)
	}
	if !snap.Announced["quote_stale"] {
		t.Fatalf("已报未销标记没落盘，announced=%+v", snap.Announced)
	}

	// 重启到 10:45（距 lastR 10:25 已超 10 分钟恢复窗）⇒ 首个 tick 补发那条销案。
	after, _ := restartRouter(path, dayStart(2026, 9, 22, 10, 45))
	got := after.Route(nil)
	if len(got) != 1 || got[0].Kind != KindResolved || got[0].Rule != "quote_stale" {
		t.Fatalf("重启后应补发 1 条悬置销案，got %+v", got)
	}
	if !strings.Contains(got[0].Body, "补发") {
		t.Errorf("补发的销案要说清来历，got %q", got[0].Body)
	}
	if n := after.unresolvedCountLocked(); n != 0 {
		t.Errorf("销案补发后不该仍挂『已报未销』，got %+v", after.announced)
	}
	if len(after.pendingR) != 0 {
		t.Errorf("补发后 pendingR 应清空（不许重复补发），got %+v", after.pendingR)
	}
	// 放行也得跟上落盘：再走一轮后 journal 里的 pending_resolve 必须归零，
	// 否则下一次重启会第三条补发同一条销案。
	after.Route(nil)
	if snap := readStateFile(t, path); len(snap.PendingResolve) != 0 {
		t.Errorf("放行后仍把销案写回 journal：pending_resolve=%+v", snap.PendingResolve)
	}
}

// TestAnnouncedSurvivesRestart P27b：重启前"已报未销"，重启后的 recover 仍要销案。
// 反证腿走在同一用例里：不灌状态（＝旧行为 / rehydrate 被摘掉）时同一条 recover 被吞。
func TestAnnouncedSurvivesRestart(t *testing.T) {
	path := journalPath(t)
	before, clk := newTestRouter(dayStart(2026, 9, 22, 14, 0))
	before.setStatePath(path)
	if got := before.Route([]AlertEvent{fireEv("breaker_open", "p1", 1)}); len(got) != 1 {
		t.Fatalf("首次破线应出 1 条 alert，got %+v", got)
	}
	clk.advance(2 * time.Minute)

	after, _ := restartRouter(path, dayStart(2026, 9, 22, 14, 2))
	if !after.announced["breaker_open"] {
		t.Fatalf("announced 未恢复，got %+v", after.announced)
	}
	if got := after.Route([]AlertEvent{recoverEv("breaker_open", "p1", 0)}); len(got) != 1 || got[0].Kind != KindResolved {
		t.Fatalf("重启后恢复应成对销案，got %+v", got)
	}
	// 反证：同一份 journal 不灌 ⇒ 这条 p1 永远开而不销（只报不销的成因）。
	// 这一腿同时证明上面那条 resolved 的来历是 rehydrate，而不是别的路径顺手补的。
	fresh := fixedRouter(dayStart(2026, 9, 22, 14, 2))
	if got := fresh.Route([]AlertEvent{recoverEv("breaker_open", "p1", 0)}); len(got) != 0 {
		t.Fatalf("没灌状态却销了案 ⇒ 承重件不是 rehydrate，got %+v", got)
	}
}

// TestSameDayRestartKeepsAccumulating P26b：同日重启既不凭空造汇总，也不清零当日计数。
func TestSameDayRestartKeepsAccumulating(t *testing.T) {
	path := journalPath(t)
	before, _ := newTestRouter(dayStart(2026, 9, 22, 10, 0))
	before.setStatePath(path)
	before.Route([]AlertEvent{fireEv("buy_queue_high", "p2", 3)})

	// 重启仍落在 9-22 当天（进程当天挂过一次又起来）。
	after, _ := restartRouter(path, dayStart(2026, 9, 22, 15, 0))
	if got := after.Route(nil); len(got) != 0 {
		t.Fatalf("同日重启不该产出汇总（把当天当昨天发出去就是假报），got %+v", got)
	}
	if st := after.daily["buy_queue_high"]; st == nil || st.Count != 1 {
		t.Fatalf("当日计数应活过重启，got %+v", after.daily)
	}
	// 当天再触发一次 ⇒ 累计 2 次、峰值取两次里的更大值；跨日后汇总要带这份累计。
	after.Route([]AlertEvent{fireEv("buy_queue_high", "p2", 4)})
	after2, _ := restartRouter(path, dayStart(2026, 9, 23, 8, 0))
	got := after2.Route(nil)
	if len(got) != 1 || !strings.Contains(got[0].Body, "触发 2 次") {
		t.Fatalf("跨日汇总应带累计 2 次，got %+v", got)
	}
	if !strings.Contains(got[0].Body, "峰值 4.00") {
		t.Errorf("峰值也要跨重启累计，got %q", got[0].Body)
	}
}

// TestCorruptAndVersionMismatchFallBackEmpty 损坏 / 版本不符 ⇒ 退回空态，且必须留下可见日志。
func TestCorruptAndVersionMismatchFallBackEmpty(t *testing.T) {
	// ① 非法 JSON：不 panic、不造投递，退回空态并打 WARN。
	path := journalPath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("写损坏文件: %v", err)
	}
	buf, restore := captureLog(t)
	r, _ := restartRouter(path, dayStart(2026, 9, 22, 10, 0))
	if got := r.Route(nil); len(got) != 0 {
		restore()
		t.Fatalf("损坏 journal 不该直接造出投递，got %+v", got)
	}
	if !strings.Contains(buf.String(), "损坏") {
		t.Errorf("损坏 journal 必须可见（否则静默丢汇总），日志=%q", buf.String())
	}
	restore()

	// ② 版本不符：同样退回空态，日志点名版本号（现场要能分清"格式换代"和"数据没了"）。
	raw, err := json.Marshal(AlertRouterState{Version: alertRouterStateVersion + 1, Day: "2026-09-21"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path2 := journalPath(t)
	if err := os.WriteFile(path2, raw, 0o600); err != nil {
		t.Fatalf("写 journal: %v", err)
	}
	buf2, restore2 := captureLog(t)
	defer restore2()
	r2, _ := restartRouter(path2, dayStart(2026, 9, 22, 10, 0))
	if r2.day != "" || len(r2.daily) != 0 {
		t.Fatalf("版本不符却灌进了状态：day=%q daily=%+v", r2.day, r2.daily)
	}
	if !strings.Contains(buf2.String(), "版本不符") {
		t.Errorf("版本不符必须可见，日志=%q", buf2.String())
	}

	// ③ 空路径＝禁用落盘（保持旧行为）：Route 照常工作、不报错、不写盘。
	path3 := journalPath(t)
	r3 := fixedRouter(dayStart(2026, 9, 22, 10, 0))
	r3.setStatePath("")
	if got := r3.Route([]AlertEvent{fireEv("llm_cooldown", "p2", 1)}); len(got) != 0 {
		t.Fatalf("未启用落盘时路由行为不该变，got %+v", got)
	}
	if _, err := os.Stat(path3); !os.IsNotExist(err) {
		t.Errorf("statePath 为空却写了文件，err=%v", err)
	}
}

// TestWriteSkippedOnlyWhenUnchanged 落盘两半：内容没变不重复写、内容真变了必须重写。
func TestWriteSkippedOnlyWhenUnchanged(t *testing.T) {
	path := journalPath(t)
	r, clk := newTestRouter(dayStart(2026, 9, 22, 10, 0))
	r.setStatePath(path)
	r.Route([]AlertEvent{fireEv("llm_cooldown", "p2", 3)})
	// 手工把文件改成哨兵：下一轮状态与时钟都没变 ⇒ 不写 ⇒ 哨兵还在。
	if err := os.WriteFile(path, []byte("SENTINEL"), 0o600); err != nil {
		t.Fatalf("写哨兵: %v", err)
	}
	r.Route(nil)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件: %v", err)
	}
	if !strings.Contains(string(data), "SENTINEL") {
		t.Fatalf("内容未变却重写了盘（无意义磁盘churn 回来了）: %q", string(head(data, 80)))
	}
	// 状态真变了（同日再多一次触发）⇒ 必须重写，且新计数落盘。
	clk.advance(30 * time.Second)
	r.Route([]AlertEvent{fireEv("llm_cooldown", "p2", 5)})
	snap := readStateFile(t, path)
	st, ok := snap.Daily["llm_cooldown"]
	if !ok || st.Count != 2 {
		t.Fatalf("状态变更后没重写字节，daily=%+v", snap.Daily)
	}
	if st.Peak != 5 {
		t.Errorf("落盘峰值=%v，期望 5", st.Peak)
	}
}

// TestProductionEntryPathHydratesGlobal 装配入口 SetAlertRouterStatePath 本身要真走一次：
// 只测实例方法而生产那一行没人碰过，就是本仓锤过的「有实现没接线」形态。
// 路由器是进程级单例，跑完必须把动过的字段复位，别把 journal 状态带给同包其它用例。
func TestProductionEntryPathHydratesGlobal(t *testing.T) {
	path := journalPath(t)
	seed := fixedRouter(dayStart(2026, 9, 22, 23, 30))
	seed.setStatePath(path)
	seed.Route([]AlertEvent{fireEv("trading_calendar_not_loaded", "p2", 1)})

	globalAlertRouter.mu.Lock()
	savedPath, savedDay := globalAlertRouter.statePath, globalAlertRouter.day
	savedDaily, savedAnnounced := globalAlertRouter.daily, globalAlertRouter.announced
	savedWritten := globalAlertRouter.lastWritten
	globalAlertRouter.mu.Unlock()
	defer func() {
		globalAlertRouter.mu.Lock()
		globalAlertRouter.statePath, globalAlertRouter.day = savedPath, savedDay
		globalAlertRouter.daily, globalAlertRouter.announced = savedDaily, savedAnnounced
		globalAlertRouter.lastWritten = savedWritten
		globalAlertRouter.mu.Unlock()
	}()

	buf, restore := captureLog(t)
	defer restore()
	SetAlertRouterStatePath(path)
	globalAlertRouter.mu.Lock()
	day, nBucket := globalAlertRouter.day, len(globalAlertRouter.daily)
	globalAlertRouter.mu.Unlock()
	if day != "2026-09-22" {
		t.Fatalf("装配入口没把聚合日灌回来，day=%q", day)
	}
	if nBucket != 1 {
		t.Fatalf("装配入口没把汇总桶灌回来，桶数=%d", nBucket)
	}
	if !strings.Contains(buf.String(), "已恢复告警路由状态") {
		t.Errorf("启动必须回显恢复了什么，日志=%q", buf.String())
	}
}
