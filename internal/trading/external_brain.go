// external_brain.go — 外脑模式判定（2026-11-04，§EXT-BRAIN-1104）。
//
// 背景：东莞证券 2026-11-05 起关闭 QMT 内置策略的文件读写与外部网络（公告 A），
// 量仔 ↔ 柜台的自动通道随之断开。此时量仔必须"知道自己已经不能自动动手了"，
// 并把这件事变成**可观测读数**，而不是一堆超时、重试与幽灵占位行。
//
// 为什么不叫"降级模式"：降级在这仓已经被行情链用死了（J1/J2/J3/J0、st.Degrade），
// 再造一个同名概念会让值班的人分不清是哪一条链。外脑模式只说一件事：
// 量仔不再试图自动动手。
//
// 四档语义（数字即读数，前端只按数字分形，不解析文案）：
//
//	0 不适用      —— 本账号压根没配网关（纯纸面/纯研究账号）
//	1 正常        —— 通道通、柜台读数新鲜，可以自动动手
//	2 读不到柜台  —— 通道还通，但资金/持仓读数已过窗（此时不该开新仓）
//	3 通道断      —— 网关不可达，或桥心跳已断（11-05 断桥形态）
//	4 显式影子态  —— owner 显式把 qmt.enabled 置 false（既有键，不新增配置）
//
// 纪律（与本仓 §FIX-20261028-F02 同族，三条都不许破）：
//   - 判定函数**只读**：不推进任何基线、不写任何状态；
//   - 推进状态的写入点**唯一**：HealthCheck 里采集桥心跳那��处；
//   - 显式覆盖（档 4）必须赢过一切自动判定。
//
// English: external-brain mode — the read-only four-state judgement telling whether the
// engine may still act automatically. Explicit shadow state (qmt.enabled=false) always wins.
package trading

import (
	"time"

	"quant-trading-v2/internal/cntime"
)

// 外脑模式档位常量。数值即对外契约（前端按数字分形），改动需同批改前端映射。
// English: external-brain mode tiers; the numeric values are the external contract.
const (
	// ExternalBrainNA 不适用：没有配置网关的账号不参与这套判定。
	ExternalBrainNA int64 = 0
	// ExternalBrainNormal 正常：通道通、读数新鲜，允许自动动手。
	ExternalBrainNormal int64 = 1
	// ExternalBrainReadStale 读不到柜台：通道在，但资金/持仓读数已过窗。
	ExternalBrainReadStale int64 = 2
	// ExternalBrainChannelDown 通道断：网关不可达，或桥心跳已断。
	ExternalBrainChannelDown int64 = 3
	// ExternalBrainShadow 显式影子态：owner 主动关掉 qmt.enabled。
	ExternalBrainShadow int64 = 4
)

// §READ-STALE-20261112：原「固定 1 小时窗」已废弃。柜台资金帧是稀疏上报
// （今日 08:52 / 08:54 / 08:57 三帧后一整天无变化是 by design——导出内容没变，
// uplink 按内容哈希去重，不会重复 POST），按秒数判新鲜会把 08:54 的开盘帧在
// 09:54 后误判「柜台读数过期」→ 档 2 拦住全天买入。
// 现改为「资金帧是否属于当前北京自然日」：同日帧全天可用，跨日帧才判过期。
// 与持仓维度的 SnapshotBelongsToDay 同族口径，不再引入第三把尺。
// English: freshness is day-keyed, not second-based — a counter cash frame stays
// valid for the whole trading day; only a previous-day frame is stale.
//
// 保留常量名以兼容既有测试与文档引用，但它不再参与档 2 判定。
const externalBrainReadStaleSec = 3600

// queuedConnReader 网关可选能力：暴露桥心跳读数（queued_connected）。
// 做成**可选接口**而不是给 Executor 加方法，是为了不破坏既有 Executor 实现
// （mock/降级实现/测试替身都实现了 Executor）；拿不到就当"读数未知"，不假装知道。
//
// 为什么需要它：11-05 断的是"QMT 内置策略（桥）"，网关进程本身还活着，
// 所以 /health 仍然 ok、broker_connected 仍可能为真——只有 queued_connected
// 会掉假。它是"桥还在不在"的唯一权威读数，且这个秒数口径在网关侧（15s），
// Go 侧不另写一个（§两套账纪律）。
//
// English: optional capability exposing the bridge heartbeat; unknown stays unknown.
type queuedConnReader interface {
	QueuedConnected() (value bool, known bool)
}

// ExternalBrain 返回当前外脑模式档位与原因文案（只读，不改任何状态）。
// 读数面板与引擎的自动下单闸共用这一个判据，避免两处各判一套。
// English: the current external-brain tier and reason; read-only.
func (c *Controller) ExternalBrain() (int64, string) {
	return c.externalBrainAt(time.Now())
}

// externalBrainAt 是 ExternalBrain 的可注入时钟版本（测试用）。
// 档位判定顺序即优先级：显式影子态 → 不适用 → 通道断 → 读数过期 → 正常。
// English: tier judgement with an injectable clock (tests); priority order documented above.
func (c *Controller) externalBrainAt(now time.Time) (int64, string) {
	// 取锁读状态字：探测是否发生过、最近一次探测结果、桥心跳读数、配置。
	c.mu.RLock()
	enabled := c.cfg.Enabled
	gatewayURL := c.cfg.GatewayURL
	probed := !c.lastHealthAt.IsZero()
	healthy := c.lastHealthy
	queuedKnown := c.lastQueuedKnown
	queuedConnected := c.lastQueuedConnected
	c.mu.RUnlock()

	// 档 4：显式影子态赢过一切自动判定。
	// 理由：owner 手动关停是"确定性的意图"，任何自动判定都不许推翻它
	//（否则会出现"owner 关了、系统自己又打开"的诡异行为）。
	if !enabled {
		return ExternalBrainShadow, "显式影子态（qmt.enabled=false）"
	}
	// 档 0：没有网关的账号不适用（例如纯纸面账号、尚未启用实盘的新账号）。
	if gatewayURL == "" {
		return ExternalBrainNA, "未配置网关，外脑模式不适用"
	}
	// 档 3：通道断的两种证据。
	// ① 探测失败：lastHealthy=false 且确实探过（没探过时初值也是 false，
	//    不看 probed 会把"进程刚起还没探"误报成断链）。
	// ② 探过去了但桥心跳断：11-05 断桥形态——网关活着，桥死了，
	//    /health 仍 ok，只有 queued_connected 掉假。
	if probed && !healthy {
		return ExternalBrainChannelDown, "网关探测失败，通道已断"
	}
	if queuedKnown && !queuedConnected {
		return ExternalBrainChannelDown, "桥心跳已断（queued_connected=false）"
	}
	// 档 2：柜台读数过期。只有真的存在账户行才判（没有行 = 还没开实盘，不算过期）。
	// §READ-STALE-20261112：改按北京自然日判 —— 同日开盘帧全天可用，跨日帧才过期。
	// 08:54 的资金帧不再在 09:54 被误判成「读不到柜台」而拦住全天买入。
	if asof := c.cashAsOf(); asof != "" {
		if t, ok := parseSnapshotTime(asof); ok {
			if cntime.DayOf(t) != cntime.DayOf(now) {
				return ExternalBrainReadStale, "柜台读数过期（资金帧不属于当前交易日）"
			}
		}
	}
	// 档 1：正常。
	return ExternalBrainNormal, "正常"
}

// ExternalBrainBlocksAutoBuy 判断外脑模式是否禁止**自动买入**。
// 口径只覆盖买入，不覆盖卖出：卖单（止损/止盈/清仓）走的是既有熔断链 fail-closed，
// 再叠一层只会挡住本来该出的货；买入才是"钱和通道都不确定时不该开新仓"的那一侧。
// 显式影子态（档 4）由 qmt.enabled=false 自己挡住，这里也一并覆盖，双保险。
// English: only auto-buys are blocked; exits keep their existing fail-closed path.
func (c *Controller) ExternalBrainBlocksAutoBuy() (bool, int64, string) {
	code, reason := c.ExternalBrain()
	if code >= ExternalBrainReadStale {
		return true, code, reason
	}
	return false, code, reason
}

// cashAsOf 读资金行的落库时刻（空串 = 没有账户行 / 读不到）。
// 只读判据，不写。
// English: the cash row timestamp; empty means no row or unreadable. Read-only.
func (c *Controller) cashAsOf() string {
	if c.store == nil {
		return ""
	}
	acc, err := c.store.GetRealAccountScoped(c.userID, c.legacyCashGlobal)
	if err != nil {
		return ""
	}
	return acc.UpdatedAt
}

// parseSnapshotTime 解析落库时刻串。本仓历史上出现过两种写法：
// 北京时间墙钟串（"2006-01-02 15:04:05"）与 RFC3339。都试，都失败就当"读数未知"
// （不许拿零值冒充"很久没更新"，也不许因为解析失败就误报过期）。
// English: parse the cash-row timestamp; unknown stays unknown.
func parseSnapshotTime(s string) (time.Time, bool) {
	layouts := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
