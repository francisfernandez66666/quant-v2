// leg_status.go — §EXT-BRAIN-UP（2026-11-05 外脑上行腿）本地执行腿的"装的是不是跑的是不是同一份"读数。
//
// 背景（docs/PLAN_EXTERNAL_BRAIN_IMPL_20261103.md §2.3 / IMPL_QMT_INTERNAL_LOOP_20261103 §4.1、§5.1）：
// 券商 11-05 起 QMT 内置策略断网断文件，柜台读数改由**盒子本地的普通 Python 进程**
// （qmt_gateway/report_uplink.py）解析导出文件后经既有 POST /api/qmt/report 上行。
// 上行信封新带一枚 strategy_version——已安装内置策略文件的 STRATEGY_VERSION 常量，
// 心跳的 reason 里还捎带执行器自身版本与文件指纹（SHA256 前 12 位）。
//
// 本文件负责 Go 侧的全部三件事：
//  1. 把每账号"最后一次非空 strategy_version + 到达时刻"落到账号目录
//     `<dataDir>/accounts/<uid>/leg_status.json`（先例：t_rounds_live.json 同根同姿势，
//     见 internal/engine/tlive_state.go 与 internal/server/t_rounds_api.go）；
//  2. **空版本绝不覆盖已知版本**——上行进程读不到策略文件时宁可不报，
//     Go 也不许把已知的旧版本抹成空（"读不到"≠"没有版本"，broker_reads 边界 2 的 Go 侧镜像）；
//  3. 以**纯增量**方式并进 GET /api/qmt/state：内嵌 trading.StateSnapshot、只追加
//     leg_status 一键，既有字段逐字节不变（§INDEX-BLIND/§M12-A 一贯的"新读数只做加法"纪律，
//     纯观测——不参与熔断、不改任何既有语义）。
//
// English: per-account leg status — persists the last non-empty strategy_version (plus executor
// version/fingerprint carried in the heartbeat reason) into <dataDir>/accounts/<uid>/leg_status.json,
// never lets an empty version overwrite a known one, and surfaces it additively on
// GET /api/qmt/state as an embedded superset of trading.StateSnapshot. Pure observability:
// no breaker, no health, no money.
package server

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/trading"
)

// legStatusFileName 账号目录下的落盘文件名（与 t_rounds_live.json 同根：accounts/<uid>/ 下）。
const legStatusFileName = "leg_status.json"

// legStatusMu 串行化"读-改-写"：回报是多线程进来的（网关 outbox 发送线程 + 心跳线程），
// 同一账号两条腿同时到，不加锁会出现"后写的把前写的 executor 字段抹掉"的丢更新。
// 一把全局小锁就够——这条链每账号每分钟最多几条，不值得做分账号锁表。
var legStatusMu sync.Mutex

// legStatus 一账号一份的本地腿读数。字段全部只进观测面，任何资金/持仓/熔断判定都不许消费它。
// JSON 形态即落盘形态（账号目录里就是一份可直接取证的快照），无凭证字段——密钥结构上
// 不可能进来（report_uplink 侧同样有"状态文件逐字节不含钥匙"的红线测试）。
type legStatus struct {
	// StrategyVersion 已安装内置策略文件的 STRATEGY_VERSION（如 "v02-20261104"）。
	StrategyVersion string `json:"strategy_version"`
	// SeenAt 携带该版本（或执行器指纹）的事件到达时刻，RFC3339 北京串。
	SeenAt string `json:"seen_at"`
	// ExecutorVersion/ExecutorFingerprint 本地执行器（local_executor.py）的版本与文件指纹，
	// 从 heartbeat 的 reason 键值串里解出——指纹对不上＝"装的≠跑的"最直接的证据。
	ExecutorVersion     string `json:"executor_version,omitempty"`
	ExecutorFingerprint string `json:"executor_fingerprint,omitempty"`
	// UplinkVersion/UplinkFingerprint 上行客户端自身的版本与指纹（排查断在哪一段）。
	UplinkVersion     string `json:"uplink_version,omitempty"`
	UplinkFingerprint string `json:"uplink_fingerprint,omitempty"`
}

// parseLegStatusReason 把 heartbeat reason 的 "k=v k=v" ASCII 串解进 legStatus 的执行器段。
// reason 里的键集合是自由文本域（只被本函数按白名单 switch 解出，其余原样忽略——
// 解错一个键就把垃圾当成了事实）。
// 解析失败/键缺失一律保持原值（缺就别说），绝不整串塞进某个字段冒充解析成功。
func parseLegStatusReason(reason string, st *legStatus) bool {
	updated := false
	for _, tok := range strings.Fields(reason) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || v == "" {
			continue
		}
		switch k {
		case "executor_version":
			st.ExecutorVersion = v
		case "executor_fingerprint":
			st.ExecutorFingerprint = v
		case "uplink_version":
			st.UplinkVersion = v
		case "uplink_fingerprint":
			st.UplinkFingerprint = v
		default:
			continue // 未知键：不动任何字段，也不算"有更新"
		}
		updated = true
	}
	return updated
}

// legStatusPath 账号目录下的落盘路径。cacheDir/uid 任一为空即返回空串——
// 空 uid 落盘会把不同账号的腿状态灌进同一个文件（§FIX-20261028-F01 读面纪律的写面镜像），
// 宁可这条腿整体不工作，也不制造跨账号混淆账。
func (s *Server) legStatusPath(uid string) string {
	if s.cacheDir == "" || strings.TrimSpace(uid) == "" {
		return ""
	}
	return filepath.Join(s.cacheDir, "accounts", uid, legStatusFileName)
}

// recordLegStatus 回报入口的纯观测挂钩：事件携带非空 strategy_version（或心跳 reason 里的
// 执行器指纹）才落账；空版本**不覆盖**已知版本（只更新既有 executor 字段）。
// 任何 IO 失败只打日志——腿状态读不到不是交易事故，绝不让观测写失败反噬回报主链。
// English: record the last non-empty strategy version + arrival time; an empty version never
// overwrites a known one; IO failure is log-only and can never break the report main chain.
func (s *Server) recordLegStatus(uid string, ev qmtReportEvent, now time.Time) {
	version := strings.TrimSpace(ev.StrategyVersion)
	legStatusMu.Lock()
	defer legStatusMu.Unlock()
	path := s.legStatusPath(uid)
	if path == "" {
		return
	}
	prev := readLegStatusFromFile(path)
	if prev == nil {
		prev = &legStatus{}
	}
	changed := false
	if version != "" {
		// 只有非空版本才推进 strategy_version 与 seen_at——"上行进程这轮没带版本"不许
		// 被记成"版本没变、依然新鲜"，seen_at 只在真的收到读数时推进。
		prev.StrategyVersion = version
		changed = true
	}
	if parseLegStatusReason(ev.Reason, prev) {
		changed = true
	}
	if !changed {
		return
	}
	prev.SeenAt = now.Format(time.RFC3339)
	if err := writeLegStatusFile(path, prev); err != nil {
		log.Printf("[leg-status] 落盘失败(用户=%s): %v", uid, err)
	}
}

// readLegStatus 观测读面：返回该账号最近的腿状态（nil=从未收到过任何版本读数）。
func (s *Server) readLegStatus(uid string) *legStatus {
	path := s.legStatusPath(uid)
	if path == "" {
		return nil
	}
	return readLegStatusFromFile(path)
}

// readLegStatusFromFile 读一份腿状态文件；缺失/坏 JSON 一律 nil（观测面按"没有读数"处理，
// 不把解析失败伪装成空读数——那正是本批要防的 missing≠empty 折叠）。
func readLegStatusFromFile(path string) *legStatus {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st legStatus
	if json.Unmarshal(raw, &st) != nil {
		return nil
	}
	return &st
}

// writeLegStatusFile 原子写（tmp+rename，0600 与账号目录其他状态文件同权）：
// 半截 JSON 会被下一次读收成 nil，等于把已知版本弄丢——原子替换杜绝这个形状。
func writeLegStatusFile(path string, st *legStatus) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	body, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// qmtStateWithLeg §EXT-BRAIN-UP 的 /api/qmt/state 响应包装：**内嵌** trading.StateSnapshot，
// 既有字段逐字节不变（encoding/json 对匿名字段做扁平合并），只追加 leg_status 一键。
// leg_status 恒在位（无读数为 null），前端/探针不用在"字段缺失"与"没有读数"之间猜。
// English: embedding keeps today's JSON byte-for-byte and appends exactly one key.
type qmtStateWithLeg struct {
	trading.StateSnapshot
	LegStatus *legStatus `json:"leg_status"`
}
