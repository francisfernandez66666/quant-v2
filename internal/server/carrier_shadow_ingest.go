// carrier_shadow_ingest.go — §CARRIER-SHADOW（2026-11-05 载体影子对照批）上行落库这一腿。
//
// 这条腿是"影子对照 5 日窗口"的写面：盒子普通进程（qmt_gateway/report_uplink.py）把执行器
// 本地账本里今天的 shadow 判定按日打成一批，走**既有的** POST /api/qmt/report 上行（新事件类型
// carrier_shadow），这里落进 carrier_shadow_decisions 一张表。
//
// 五条边界，逐条都是本仓咬过的形状：
//  1. **归属只由那把钥匙决定**（§安全 T1 / §TOKEN-PERAACCT）：uid 来自 qmtReportMiddleware
//     反查，回报体里行上自报的 user_id 一律**丢弃**——写入时的账号统一是这把钥匙的主人
//     （信了自报值，就是让任何一根腿都能往别人账本里写"载体判定"，5 日窗口按谁的数算从此说不清）。
//  2. **空批次整条拒收（400）**：上行侧本来就规定"今天没有行就一个字节都不发并记 skip 原因"。
//     这里若把空批次记成"今天报了，零条"，就把 missing 折成了 empty——那一天会被对照表
//     当成真实的一天，5 天窗口能被一串"从没报过"的日子凑满。
//  3. **超上限整条拒收而不是截断**：静默截断＝悄悄丢证据；400 会让上行侧把它记成死信
//     （可取证、不卡队首，§P1-8 毒丸口径），运维看得见"哪天丢了什么"。
//  4. **未来的 day 逐行摘掉**：一行 "20991231" 会在窗口里自成一天，而那一天永远不会被
//     日历承认（no_report/no_hits 的分界就此失真）。
//  5. **观测腿不许有钱的动作**（§OBS-GUARD）：本函数只写一张表 + 留痕。
//     不碰 ctrl（熔断/在途/资金）、不碰持仓与委托、不推业务事件、不参与 /health。
//     落库失败回 500 让上行侧按可重试路径处理——与 §M4 的成交/委托腿同口径：
//     "吞错回 200"会让上行把没落库的一批标成已报，那一天的分子就永久丢了。
//
// English: ingest the carrier's shadow batch into one observation table. Attribution comes from
// the key only (a self-reported user_id on a row is dropped, never trusted); an empty batch is
// refused with 400 because recording it would fold "nothing reported" into "reported zero";
// over-limit batches are refused instead of silently truncated; a write failure answers 500 so
// the uplink keeps the batch on its retry path rather than marking unsent data as sent.
package server

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// carrierShadowIngestResult 落库统计：回给上行侧，也进日志（上行侧据此知道自己那批落了几行）。
type carrierShadowIngestResult struct {
	OK       bool     `json:"ok"`
	Written  int      `json:"written"`
	Rejected []string `json:"rejected,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// ingestCarrierShadow 处理 carrier_shadow 事件：校验 → 逐行准入 → upsert → 200/400/500。
// now 由调用方传入（回报入口给 cntime.Now()）——"未来的 day"那一支必须有可注入的时钟才测得动。
func (s *Server) ingestCarrierShadow(w http.ResponseWriter, db *store.DB, uid string,
	ev qmtReportEvent, now time.Time) {
	if strings.TrimSpace(uid) == "" {
		// 归属没定就写不了：这张表按账号分账，空 uid 会把不同腿的判定灌进同一格
		// （§FIX-20261028-F01 读面纪律在写面的镜像，与 store 侧同一条判据）。
		writeError(w, http.StatusBadRequest, "carrier_shadow 无归属账号（钥匙未解析出用户）")
		return
	}
	if len(ev.Shadow) == 0 {
		// 空批次＝"今天有上报但一条都没有"的假事实（见文件头边界 2）。
		// 上行侧的纪律是"没行就一个字节都不发并记 skip 原因"，走到这里说明有一侧漂了，
		// 当场 400 比事后从对照表里猜"这天到底报没报"便宜得多。
		writeError(w, http.StatusBadRequest, "carrier_shadow 批次为空（missing 不许当 empty 上报）")
		return
	}
	if len(ev.Shadow) > carrierShadowMaxRows {
		writeError(w, http.StatusBadRequest, "carrier_shadow 单批行数超上限，整条拒收（不截断）")
		return
	}
	today := cntime.DayCompactOf(now)
	// 未来的 day 逐行摘掉、其余行照常收：载体写错一个日期不该让这一天所有证据一起丢
	// （§MANUAL-SYNC 的"部分失败不标 done"同取向），但也绝不让它进窗口。
	accepted := make([]store.CarrierShadowDecision, 0, len(ev.Shadow))
	future := 0
	for _, r := range ev.Shadow {
		if r.Day > today {
			future++
			continue
		}
		// 行上的 user_id 在这里被自然丢弃：写入用的账号只有 uid 这一个来源（§安全 T1）。
		accepted = append(accepted, r)
	}
	if len(accepted) == 0 {
		writeJSON(w, http.StatusOK, carrierShadowIngestResult{
			OK: true, Written: 0, Rejected: []string{"future_day x" + strconv.Itoa(future)},
			Reason: "整批日期晚于今天，逐行拒收（未落库）",
		})
		return
	}
	written, rejected, err := db.UpsertCarrierShadowRows(uid, now.Format(time.RFC3339), accepted)
	if err != nil {
		// 500 让上行侧把这批留在重试路径上（§M4 成交/委托腿同口径）。
		log.Printf("[carrier-shadow] 落库失败(用户=%s): %v", uid, err)
		writeError(w, http.StatusInternalServerError, "carrier shadow upsert: "+err.Error())
		return
	}
	if future > 0 {
		// 摘掉的行数必须点名回给上行侧：只回 written 会把"发了一百条、落了几条"糊成一句"成功"。
		rejected = append(rejected, "future_day x"+strconv.Itoa(future))
		log.Printf("[carrier-shadow] 用户=%s 摘除未来日期 %d 行", uid, future)
	}
	// 一天一行留痕（不是每批一行）：对照窗口按天看，逐批写会把每日核心记录淹掉。
	firstDay := accepted[0].Day
	opslog.DayOnce("carrier-shadow:"+uid+":"+firstDay, func() {
		opslog.Logf("quant", "载体影子决策上行 用户=%s 首批日=%s 收=%d 逐行拒=%d（只进观测表，不参与任何下单判定）",
			uid, firstDay, written, len(rejected))
	})
	log.Printf("[carrier-shadow] 用户=%s 收=%d 逐行拒=%v", uid, written, rejected)
	writeJSON(w, http.StatusOK, carrierShadowIngestResult{OK: true, Written: written, Rejected: rejected})
}
