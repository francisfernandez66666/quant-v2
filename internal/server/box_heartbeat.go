// box_heartbeat.go — §BOX-HEARTBEAT（2026-11-23）：盒子心跳的落盘与读面。
//
// 现场（owner）：「我的 57516 账户登录后前端不显示进程，那我怎么知道我开没开 qmt 并开启
// 自动交易，有没有连云端。」盒上 QMT 跑在 SYSTEM/session 0，登录用户看不到窗口；前端也
// 没有客户端进程读数。修法不是让前端去拨盒子（15s 轮询 × 跨 WG 太重），而是让已有的 5 分钟
// 上行心跳顺带两枚读数，云端落一份 accounts/<uid>/box_heartbeat.json，读面只渲染。
//
// 三条边界（与 carrier_shadow / leg_status 同取向）：
//  1. 只读观测：不碰持仓/委托/资金/熔断，不参与任何判定。
//  2. missing ≠ empty：文件不存在/读不出 ⇒ known=false + 原因，前端只能显示「读数未知」；
//     绝不把"没收到心跳"渲染成"客户端没在跑"（那是两个不同的处置动作）。
//  3. 心跳本身是稀疏的（5 分钟一跳）：age_sec 是云端按**收到时刻**算的，盒子自报时刻
//     另存 box_at 供钟差参考——拿盒子钟算新鲜度会让一台钟慢的机器永远"过期"。
//
// English: box heartbeat persistence + readout. Observation only; missing is reported as
// unknown, never folded into "the client is stopped"; freshness uses the cloud receive time.
package server

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
)

const boxHeartbeatFileName = "box_heartbeat.json"

// boxHeartbeat 是落盘形状（一次心跳覆盖一次；心跳本身就是"最新状态"）。
type boxHeartbeat struct {
	At                string `json:"at"`               // 云端收到时刻（RFC3339）
	BoxAt             string `json:"box_at,omitempty"` // 盒子自报时刻（钟差参考）
	ClientProc        string `json:"client_proc,omitempty"`
	StrategyLogAt     string `json:"strategy_log_at,omitempty"`
	StrategyLogAgeSec *int64 `json:"strategy_log_age_sec,omitempty"`
	StrategyVersion   string `json:"strategy_version,omitempty"`
}

// boxHeartbeatReadout 是 GET /api/qmt/state 的 box_heartbeat 键。
type boxHeartbeatReadout struct {
	Known             bool   `json:"known"`
	At                string `json:"at,omitempty"`
	AgeSec            *int64 `json:"age_sec,omitempty"`
	BoxAt             string `json:"box_at,omitempty"`
	ClientProc        string `json:"client_proc,omitempty"`
	StrategyLogAt     string `json:"strategy_log_at,omitempty"`
	StrategyLogAgeSec *int64 `json:"strategy_log_age_sec,omitempty"`
	StrategyVersion   string `json:"strategy_version,omitempty"`
	UnknownReason     string `json:"unknown_reason,omitempty"`
}

// boxHeartbeatPath 拼出该账号的心跳文件路径；uid 为空即空串（绝不落到别的账号桶里）。
func (s *Server) boxHeartbeatPath(uid string) string {
	if s.cacheDir == "" || strings.TrimSpace(uid) == "" {
		return ""
	}
	return filepath.Join(s.cacheDir, "accounts", uid, boxHeartbeatFileName)
}

// normalizeClientProc 只认三态；不认识的串归 unknown（手改文件/旧脚本写脏值都不许
// 被读成"确认在跑"）。
func normalizeClientProc(v string) string {
	switch strings.TrimSpace(v) {
	case "running", "stopped", "unknown":
		return strings.TrimSpace(v)
	default:
		return "unknown"
	}
}

// recordBoxHeartbeat 落一次心跳。只在 heartbeat 事件上调用；失败只打日志
// （观测腿不许把上行判红——心跳丢了下一跳还会来）。
func (s *Server) recordBoxHeartbeat(uid string, ev qmtReportEvent, now time.Time) {
	path := s.boxHeartbeatPath(uid)
	if path == "" {
		// 账号目录不可用＝这本账没地方记；不报错（心跳不是资金动作）。
		return
	}
	doc := &boxHeartbeat{
		At:              now.In(cntime.Loc).Format(time.RFC3339),
		BoxAt:           strings.TrimSpace(ev.At),
		ClientProc:      normalizeClientProc(ev.ClientProc),
		StrategyVersion: strings.TrimSpace(ev.StrategyVersion),
	}
	if raw := strings.TrimSpace(ev.StrategyLogAt); raw != "" {
		if _, err := time.Parse(time.RFC3339, raw); err == nil {
			doc.StrategyLogAt = raw
		}
	}
	if ev.StrategyLogAgeSec != nil && *ev.StrategyLogAgeSec >= 0 {
		v := *ev.StrategyLogAgeSec
		doc.StrategyLogAgeSec = &v
	}
	if err := writeBoxHeartbeat(path, doc); err != nil {
		log.Printf("[box-heartbeat] 落盘失败(用户=%s): %v", uid, err)
	}
}

// writeBoxHeartbeat 原子写（tmp + rename，0600 与账号目录其它状态文件同权）。
func writeBoxHeartbeat(path string, doc *boxHeartbeat) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	body, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// boxHeartbeatReadout 读一份心跳。读不到 ⇒ known=false + 原因；不把"没有文件"
// 折成"客户端 stopped"。
func (s *Server) boxHeartbeatReadout(uid string, now time.Time) *boxHeartbeatReadout {
	path := s.boxHeartbeatPath(uid)
	if path == "" {
		return &boxHeartbeatReadout{Known: false, UnknownReason: "account_unavailable"}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		reason := "heartbeat_unreadable"
		if os.IsNotExist(err) {
			reason = "heartbeat_absent"
		}
		return &boxHeartbeatReadout{Known: false, UnknownReason: reason}
	}
	var doc boxHeartbeat
	if err := json.Unmarshal(raw, &doc); err != nil {
		return &boxHeartbeatReadout{Known: false, UnknownReason: "heartbeat_bad_json"}
	}
	out := &boxHeartbeatReadout{
		Known:             true,
		At:                doc.At,
		BoxAt:             doc.BoxAt,
		ClientProc:        normalizeClientProc(doc.ClientProc),
		StrategyLogAt:     doc.StrategyLogAt,
		StrategyLogAgeSec: doc.StrategyLogAgeSec,
		StrategyVersion:   doc.StrategyVersion,
	}
	if ts, err := time.Parse(time.RFC3339, doc.At); err == nil {
		age := int64(now.Sub(ts).Seconds())
		if age < 0 {
			age = 0 // 钟差：未来时刻按 0 算，不显示负数年龄
		}
		out.AgeSec = &age
	}
	return out
}
