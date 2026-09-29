// library_staleness.go — 「战法库陈旧」业务心跳读数（§0929HB-3，09-29 全量审计批 ⑪-4 的第三条腿）。
//
// 审计原文要的是"库条数与昨日不一致且未经审批"，落码按**库陈旧**口径改写，理由是把误报面
// 先削掉（docs/FIX_PLAN_20260929.md ⑪-4 与 §七-6 的判例口径）：
//   - "条数变化"是夜间寻优+审批链的**正常产出**：每晚应用一批候选就会变一次条数。
//     把"变了"直接告警成 p2，等于每天早上一条固定噪音，真告警会被它淹掉（§0923 那批
//     把日汇总从推送里摘出去就是同一条纪律）；文件本身也不携带"谁改的"，无法在现网
//     区分"审批过的变化"与"手工 vim 的变化"。
//   - 真正没人报的是**反向失效**：夜间研究链整段停摆（任务队列卡住 / 研究进程 OOM /
//     云端没跑），库停在两周前的那一版，而引擎照常按旧规则出信号、监控全绿。
//     这正是审计报告点名的危险组合"引擎活着、监控绿着、战法库是昨天的"。
//   - 变化本身并非无人知晓：§0929LIB-WATCH 的版本戳轮询在指纹变化时已经做了
//     自动热加载 + opslog.Audit("library_auto_reload") 留痕，"未经审批的变更"那道由它负责。
//     本条只补它看不见的那半边——"长期没有变更"。
//
// 口径三条：
//  1. 读 applied_factors.json / applied_patterns.json 的 mtime，取**较近**的一侧
//     （任一库刚更新过就说明链还活着；两库各自独立更新是常态）；
//  2. 两份都不存在 → 写 0 不判陈旧：库里没规则由 p1 规则 live_strategy_no_enabled_rules
//     负责，两种成因的文案必须严格分家（§0925EVE-C1 立的规矩）；
//  3. 只有一份存在 → 按存在那份算（另一侧允许缺，与 libraryFingerprint 的 "absent" 口径一致）。
//
// 阈值 14 个日历日（owner 待裁决项 #13 的落码缺省）：夜间寻优的正常节奏是每晚一轮，
// 长假/主动暂停研究一般不超过一周；两周没有任何更新只能是链停了。常量集中在此，
// 裁决后改一行即可，不散落。
//
// English: absence-type heartbeat for a *stale* applied-strategy library. The audit asked for
// "count changed without approval", but a changed count is the nightly pipeline's normal output,
// so that form would fire daily; the silent failure nobody reports is the library never changing
// while engines keep trading on old rules. Change itself is already covered by §0929LIB-WATCH
// (auto hot-reload + audit line). Threshold: 14 calendar days since either library file's mtime.
package server

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/opslog"
)

// 量规键名在写入点写死字面量 "library_stale_days"（规则读端同名；不立 const 别名，理由见
// internal/engine/heartbeat.go 头部——§DEADGAUGE 通用守卫按「SetGauge("<键名>"」扫赋值点）。

// libraryStaleThresholdDays 陈旧判据阈值（日历日），理由见文件头。
const libraryStaleThresholdDays = 14

// libraryStalenessDays 纯函数判据：给两份库文件的 mtime（缺失传 ok=false），返回陈旧天数。
// 取较近的一侧；两份都缺 → 0（不伪造陈旧，也不伪造健康——这条交给 no_enabled 那条 p1 说）。
// English: pure predicate — days since the most recent mtime of the two library files; 0 when both
// are absent (that cause belongs to the no-enabled rule).
func libraryStalenessDays(now time.Time, mtimeA time.Time, okA bool, mtimeB time.Time, okB bool) int64 {
	if !okA && !okB {
		return 0
	}
	last := mtimeA
	if !okA || (okB && mtimeB.After(last)) {
		last = mtimeB
	}
	d := int64(now.Sub(last).Hours() / 24)
	if d < 0 {
		return 0 // 未来 mtime（时钟/文件系统异常）不得算成陈旧
	}
	return d
}

// refreshLibraryStalenessGauge 量两份已应用库文件的 mtime 并喂陈旧天数量规。
// 调用点在版本戳轮询的每一轮（60s），因此休市日也在喂——陈旧是"多长期没动"，
// 只在盘中喂会让长假后的第一个交易日才读数，正好错过它该报的那天。
// English: stats the two applied library files and feeds the staleness gauge; runs every poll
// round (60s) including non-trading days.
func (s *Server) refreshLibraryStalenessGauge(now time.Time) {
	if s.researchDir == "" {
		return
	}
	var (
		days   int64
		live   int
		latest time.Time
	)
	for _, name := range libraryFingerprintFiles {
		fi, err := os.Stat(filepath.Join(s.researchDir, name))
		if err != nil || fi == nil {
			continue
		}
		live++
		m := fi.ModTime()
		if m.After(latest) {
			latest = m
		}
	}
	if live == 0 {
		metrics.SetGauge("library_stale_days", 0)
		return // 两份都缺：成因不同、由 p1 no_enabled 规则说，这里不重复判陈旧
	}
	days = libraryStalenessDays(now, latest, true, time.Time{}, false)
	metrics.SetGauge("library_stale_days", days)
	// 越界时留一条人可读行（每 24h 至多一条）：量规只有天数，日志要能直接回答
	// "夜间研究链是不是根本没跑"。
	if days >= int64(libraryStaleThresholdDays) {
		opslog.OncePer("hb-library-stale", 24*time.Hour, func() {
			log.Printf("[P2][hb] §0929HB-3 已应用战法库 %d 天未更新（最近 mtime=%s，dir=%s）："+
				"夜间寻优/审批链可能整段停摆，引擎仍在按旧规则出信号", days, latest.Format("2006-01-02 15:04:05"), s.researchDir)
		})
	}
}
