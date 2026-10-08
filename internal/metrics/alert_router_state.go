// alert_router_state.go — §W7-E（2026-10-06 修复批 波 7）：把告警路由器里"跨重启就没了"的四份状态落盘。
//
// 修的是哪一条（AUDIT_20261005 P3 / FIX_PLAN_20261006 波 7，实测比报告写得更重）：
// alert_routing.go 的路由器全部状态都在内存（newAlertRouter 建 map、:497 进程级单例），
// 于是停机再起来会同时丢两件事：
//  1. day + daily —— 前一日聚到一半的日汇总桶。rollDayLocked 只在「内存里已经有上一日」时
//     才产出汇总，重启后 r.day 是空串，首轮 tick 走 `if r.day == "" { r.day = cur; return nil }`
//     这一支**直接把前一日吞掉**：那一天永远不会有任何汇总出现（不是延迟，是永久丢失）。
//     而 alert_routing.go:19 的头注释一直声称「进程停机则重启后首个 tick 补发（按记录的日期标注）」
//     ——这句承诺在本批之前从未实现，属"注释与实现矛盾"（§DEADGAUGE 家族：注释担保、机器不认）。
//  2. pendingR + announced —— 被恢复冷却窗挡下、等着补发的销案，以及"已报未销"标记。
//     丢掉 pendingR 只是少了那条补发；**丢掉 announced 更糟**：重启后同一规则再报 recover 时，
//     routePushLocked 的 `if !r.announced[key] { return nil }` 判定为"没报过"，孤立 resolved 被吞，
//     于是那条 p1 告警在 owner 的推送记录里**永远只有开没有销**（只报不销）。
//
// 落点选择（为什么是 dataDir 下一个 JSON 而不是 sqlite/opslog）：
//   - metrics 是被 notify/engine/store 三方共用的度量面，本包此前对内部包零依赖
//     （见 alert_routing.go:25-30 的依赖方向说明，AlertSink 因此以函数类型注入）。
//     为了四份合计不超过几 KB 的对账状态去 import internal/store，等于把"指标评估"焊死在账本上，
//     还会带来 engine→metrics→store 的注入环风险——收益不抵代价。
//   - 本仓同类"小状态跨重启"的既有姿势就是 dataDir 下的 JSON + fileutil.AtomicWrite：
//     notify 的推送补投队列（notify_outbox.json，SetPersistPath 注入路径、损坏则从空态起、
//     连续失败经 opslog.DayOnce 升级可见）与本条完全同形，照抄它的三条纪律而不是自创一套。
//   - opslog 是**只追加的日志**，不是可回读的状态存储：把 day/daily 写进日志再靠启动时反解
//     最新一行，是把"日志"当"数据库"用（本仓 §0929DRILL-A 刚锤过"赌读法"的代价）。
//
// 三条照抄来的纪律：①路径由装配期注入且必须在首轮 Route 之前；②文件缺失/损坏/版本不符
// 一律退回空态并打日志（安全侧宁可少一条汇总，也不要拿半截状态当真实读数）；
// ③落盘失败不静默——连续失败首报 + 每 10 次一报，并经 opslog.DayOnce 记一条"重启会丢销案/汇总"。
//
// 为什么每轮 Route 都写、不做脏标记：脏标记要"每一个改状态的地方都记得置位"，
// 漏一处就是本缺陷复活（recordDailyLocked / rollDayLocked / routePushLocked / flushPendingLocked
// 四个改点加上将来新增的路径）。改成"整份快照序列化后与上一次落盘的字节比对"，
// 比对口径覆盖全部字段，新增字段自动被覆盖，不需要任何人再记住置位。
// 一轮 tick 的载荷只有几百字节，且同一份内容会因字节相同直接跳过写盘，实际写频远低于 30s 一次。
//
// English: §W7-E — persist the alert router's four cross-restart state groups (day bucket,
// daily aggregation, deferred resolutions, announced flags) to one atomic JSON file under the data
// dir, so a restart no longer silently swallows yesterday's summary or leaves a p1 alert
// un-resolved forever. Re-uses the notify/outbox persistence discipline (injected path, corrupt →
// empty, failures visible) and writes on every tick with a byte-compare instead of a dirty flag,
// because a dirty flag must be remembered at every mutation site while this one cannot be forgotten.
package metrics

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"time"

	"quant-trading-v2/internal/fileutil"
	"quant-trading-v2/internal/opslog"
)

// alertRouterStateVersion 落盘格式版本。版本不符即退回空态（不猜旧格式的含义）：
// 本文件字段一旦增删，旧 journal 读出来的语义就不可信，安全侧是丢弃而不是凑合用。
const alertRouterStateVersion = 1

// alertRouterStateOpsTag 落盘失败时写 opslog 的 tag（与 notify outbox 的落盘失败记录同一姿势）。
const alertRouterStateOpsTag = "metrics"

// DailyStatSnapshot dailyStat 的可序列化形态（字段与 internal 结构逐一对应）。
// 单独立一个导出 DTO 的理由：dailyStat 是包内私有类型，json 不认私有字段；
// 把私有结构整体导出会对外暴露聚合桶的实现细节，快照类型才是稳定的落盘契约。
type DailyStatSnapshot struct {
	Level       string    `json:"level"`        // p1 | p2
	Message     string    `json:"message"`      // 规则文案
	Count       int       `json:"count"`        // 当日触发次数
	Peak        float64   `json:"peak"`         // 当日峰值
	First       time.Time `json:"first"`        // 首次触发（零值=从未触发过，仅有 recover）
	Last        time.Time `json:"last"`         // 末次事件时刻
	StillFiring bool      `json:"still_firing"` // 截至快照时刻是否仍在触发
}

// AlertRouterState 路由器四份状态的完整快照（落盘单位，也是 rehydrate 的输入）。
// 键名与 alertRouter 字段一一对应，rehydrate 时逐个装回，不做任何"聪明"的补全。
type AlertRouterState struct {
	Version int `json:"version"`
	// SavedAt 快照生成时刻（用注入时钟 r.now()，测试可复跑）。诊断用：
	// 一眼看出这份 journal 是刚写的还是重启前很久写的。
	SavedAt        time.Time                    `json:"saved_at"`
	Day            string                       `json:"day"`
	Daily          map[string]DailyStatSnapshot `json:"daily"`
	Announced      map[string]bool              `json:"announced"`
	LastFire       map[string]time.Time         `json:"last_fire"`
	LastResolve    map[string]time.Time         `json:"last_resolve"`
	PendingResolve map[string]AlertEvent        `json:"pending_resolve"`
	Suppressed     map[string]int               `json:"suppressed"`
}

// SetAlertRouterStatePath 启用路由器状态落盘，并把既有 journal 读回内存（rehydrate）。
//
// 必须在首轮 Route 之前调用（装配期，紧挨 SetAlertSink）：rehydrate 只填当前为空的 map，
// 若已有运行态再灌入会把两条时间线混成一份不可信的读数。传空串=禁用落盘（单测默认形态，
// 也是旧行为——行为不变，只是不再有跨重启承诺）。
// journal 不存在=首次运行，静默从空态起；读失败/JSON 损坏/版本不符=打一条明确日志后退回空态。
// English: enables persistence and rehydrates any existing journal; must run before the first
// Route. Missing file is a normal first run; corrupt or version-mismatched files fall back to empty.
func SetAlertRouterStatePath(path string) { globalAlertRouter.setStatePath(path) }

// setStatePath 实例级实现（单测自建路由器也走这条，保证跑的就是生产那段代码）。
func (r *alertRouter) setStatePath(path string) {
	if r == nil || path == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statePath = path
	data, err := os.ReadFile(path)
	if err != nil {
		// 首次运行或路径暂不可读：从空态起，后续每轮 Route 仍会把状态写出来。
		return
	}
	var snap AlertRouterState
	if err := json.Unmarshal(data, &snap); err != nil {
		log.Printf("[metrics][WARN] §W7-E 告警路由状态文件损坏，退回空态重建（前一日汇总与待补发销案已不可恢复）: %v（path=%s）", err, path)
		return
	}
	if restored := r.restoreLocked(snap); restored {
		return
	}
	// restoreLocked 返回 false 时已把状态复位为空态，这里只补一条可见日志。
	log.Printf("[metrics] §W7-E 告警路由状态版本不符（journal=%d 期望=%d），按空态起（path=%s）",
		snap.Version, alertRouterStateVersion, path)
}

// restoreLocked 把快照装回路由器（调用方须持锁）。返回是否成功装载。
//
// 逐份装回而非"整体覆盖"的理由：装载发生在装配期，此刻 map 本就该是空的；
// 但若将来有人把调用点后移到运行中，把 nil map 直接赋进 r.daily 会让后续 recordDailyLocked
// 在 nil map 上写入并 panic。这里对每个字段都先建好再灌，空字段跳过。
func (r *alertRouter) restoreLocked(snap AlertRouterState) bool {
	if snap.Version != alertRouterStateVersion {
		r.resetStateLocked()
		return false
	}
	r.day = snap.Day
	r.daily = map[string]*dailyStat{}
	for name, st := range snap.Daily {
		r.daily[name] = &dailyStat{
			Level: st.Level, Message: st.Message, Count: st.Count, Peak: st.Peak,
			First: st.First, Last: st.Last, StillFiring: st.StillFiring,
		}
	}
	r.announced = map[string]bool{}
	for name, on := range snap.Announced {
		r.announced[name] = on
	}
	r.lastF = map[string]time.Time{}
	for name, at := range snap.LastFire {
		r.lastF[name] = at
	}
	r.lastR = map[string]time.Time{}
	for name, at := range snap.LastResolve {
		r.lastR[name] = at
	}
	r.pendingR = map[string]AlertEvent{}
	for name, e := range snap.PendingResolve {
		r.pendingR[name] = e
	}
	r.suppressed = map[string]int{}
	for name, n := range snap.Suppressed {
		r.suppressed[name] = n
	}
	// 启动可见性：恢复了多少条必须当场知道，否则"持久化到底生没生效"只能靠几天后的事故反推
	// （本仓 §DEADGAUGE 的教训：有实现没读数＝等于没实现）。
	log.Printf("[metrics] §W7-E 已恢复告警路由状态：聚合日=%s 汇总桶 %d 条 / 已报未销 %d 条 / 待补发销案 %d 条",
		orNone(r.day), len(r.daily), r.unresolvedCountLocked(), len(r.pendingR))
	return true
}

// resetStateLocked 把四份状态清回新建路由器的形态（调用方须持锁）。
func (r *alertRouter) resetStateLocked() {
	r.day = ""
	r.daily = map[string]*dailyStat{}
	r.announced = map[string]bool{}
	r.lastF = map[string]time.Time{}
	r.lastR = map[string]time.Time{}
	r.pendingR = map[string]AlertEvent{}
	r.suppressed = map[string]int{}
}

// snapshotLocked 生成当前状态的完整快照（调用方须持锁）。
func (r *alertRouter) snapshotLocked() AlertRouterState {
	snap := AlertRouterState{
		Version:        alertRouterStateVersion,
		SavedAt:        r.now(),
		Day:            r.day,
		Daily:          map[string]DailyStatSnapshot{},
		Announced:      map[string]bool{},
		LastFire:       map[string]time.Time{},
		LastResolve:    map[string]time.Time{},
		PendingResolve: map[string]AlertEvent{},
		Suppressed:     map[string]int{},
	}
	for name, st := range r.daily {
		if st == nil {
			continue // 理论上不会出现（recordDailyLocked 只存非 nil），防御性跳过而不是 panic
		}
		snap.Daily[name] = DailyStatSnapshot{
			Level: st.Level, Message: st.Message, Count: st.Count, Peak: st.Peak,
			First: st.First, Last: st.Last, StillFiring: st.StillFiring,
		}
	}
	for name, on := range r.announced {
		snap.Announced[name] = on
	}
	for name, at := range r.lastF {
		snap.LastFire[name] = at
	}
	for name, at := range r.lastR {
		snap.LastResolve[name] = at
	}
	for name, e := range r.pendingR {
		snap.PendingResolve[name] = e
	}
	for name, n := range r.suppressed {
		snap.Suppressed[name] = n
	}
	return snap
}

// unresolvedCountLocked "已报未销"的规则条数（日志与摘要用，调用方须持锁）。
func (r *alertRouter) unresolvedCountLocked() int {
	n := 0
	for _, on := range r.announced {
		if on {
			n++
		}
	}
	return n
}

// writeState 原子落盘整份快照（在 Route 里于 mu 释放后调用，saveMu 串行化）。
//
// 为什么必须另立 saveMu：路由器是进程级单例，而 RunAlertEvaluation 由**每个引擎实例**的
// 30s 节流各自调用（internal/engine/scoring_loop.go:96，多账号注册表下并存多个引擎），
// 因此同一份 journal 可能同时被多个 tick 写。fileutil.AtomicWrite 用唯一临时名，
// 不会互相踩临时文件，但"取旧快照者后写覆盖取新快照者"的顺序倒挂仍会发生——
// saveMu 把「比对 + 写盘」整段串行，后写者拿到的必然是更新的快照（与 notify §N-7 同一条纪律）。
//
// 落盘放在 mu 之外：Route 的 mu 同时被 ConfigureAlertRouting 与状态自检共用，
// 占着它做文件 IO 等于让路由表热更新排队等一次磁盘写。
func (r *alertRouter) writeState(path string, snap AlertRouterState) {
	if r == nil || path == "" {
		return
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	data, err := json.Marshal(snap)
	if err != nil {
		log.Printf("[metrics][WARN] §W7-E 告警路由状态序列化失败（跳过本次落盘）: %v", err)
		return
	}
	// 内容未变即跳过写盘（bytes.Equal 比的是整份序列化结果）：一轮 tick 里没有任何事件时快照逐字节相同，避免无意义的磁盘churn。
	// 注意这不是"脏标记"——比对的是整份序列化结果，新增字段自动纳入比对，漏置位这件事不会发生。
	if bytes.Equal(r.lastWritten, data) {
		return
	}
	if err := fileutil.AtomicWrite(path, data, 0o600); err != nil {
		r.persistFailSeq++
		if r.persistFailSeq == 1 || r.persistFailSeq%10 == 0 {
			log.Printf("[metrics][WARN] §W7-E 告警路由状态落盘失败（连续第 %d 次）: %v（path=%s）",
				r.persistFailSeq, err, path)
		}
		// 持续落盘失败的含义要说白：下次重启会丢前一日汇总 + 待补发销案（只报不销），
		// 这与本批要消灭的静默失效同族，所以按天升级一条 opslog 而不只是 stderr。
		opslog.DayOnce("alert-router-state-persist-fail", func() {
			opslog.Logf(alertRouterStateOpsTag, "告警路由状态落盘失败（重启将丢前一日汇总与待补发销案）: %v path=%s", err, path)
		})
		return
	}
	r.lastWritten = data
	r.persistFailSeq = 0
}
