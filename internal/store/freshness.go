// Package store — SQLite 历史数据存储层。
// freshness.go：§MANUAL-BOOK-UNIFY（2026-11-06）把"柜台还在报数吗"这把尺收到一处。
//
// 为什么要收：owner 2026-11-06 裁决 1「QMT 连不上就以手工维护的持仓与可用资金为准」之后，
// 同一件事（柜台读数可不可信）有四个读者：§M12-A 的自动买入 fail-close、风控闸的可用资金两闸、
// 集中度/日亏/板块的总资产分母、以及引擎 resolver 的两维选源。
// 四处各抄一份"多久算过期"必然漂（§165 段序单点那条教训同形：按开关推算的那份会漏掉一段），
// 而漂的方向是放钱——漂成 60 分钟的那一处会在桥断的第一小时继续拿昨天的柜台余额放行买入。
//
// 本文件只搬既有判据，不新造第三条尺：
//   - 资金维度 = §M12-A 的 30 分钟窗（真源原在 internal/trading/controller.go:AvailableCash）；
//   - 持仓维度 = §A1-FRESH 的"快照属于当前交易日"（真源原在 internal/risk/gate.go:positionSnapshotFresh，
//     该函数保留名字与调用点，函数体转调 store.SnapshotBelongsToDay）。
package store

import (
	"time"

	"quant-trading-v2/internal/cntime"
)

// CashReportWindow §M12-A 的资金回报新鲜窗：超过即"口径不可得"，调用方 fail-close 不自动买。
const CashReportWindow = 30 * time.Minute

// CashReportFresh 柜台资金回报是否还在新鲜窗内（北京墙钟口径）。
//
// 三条与改前 controller 内联判据逐字一致的行为：
//  1. 解析用 cntime.Loc，**不用宿主机 Local**——UTC 主机上北京墙钟串会被读成"未来 8 小时"，
//     Since 为负恒判新鲜，40 分钟前的过期资金照旧放行（§0925EVE-W3-F 那条事故原文）；
//  2. 解析失败 ⇒ false（判不可得，不判新鲜）；
//  3. 恰好等于窗口边界 ⇒ 仍算新鲜（`>` 而非 `>=`，与改前一致）。
//
// now 由调用方给（测试注入时钟；生产传 cntime.Now()）。
func CashReportFresh(updatedAt string, now time.Time) bool {
	if updatedAt == "" {
		return false
	}
	updated, err := time.ParseInLocation("2006-01-02 15:04:05", updatedAt, cntime.Loc)
	if err != nil {
		return false
	}
	return now.Sub(updated) <= CashReportWindow
}

// CounterPositionsReporting 柜台是否还在报持仓：**全部非空 updated_at 的行都属于当前交易日**。
//
// 为什么从"至少一行"收紧成"全部"（2026-11-09 实锤）：旧判据的假设是"桥断＝整表一起冻住、
// 恢复＝逐行刷新"——但 11-09 实测一笔后台任务把整表冻结（账本停在 11-05）里的**一行**摸成了
// 当日时间戳，"至少一行属今日"立即误判"柜台在报数"，两行 11-05 的旧仓被当作真柜台展示
// （owner 已清仓、余额 9 万 1，屏上却是 3 只旧仓 49k）。帧式写入下健康回报本来就把全表
// 一起戳新，"全部属今日"与健康形态不冲突，却能抓住任何单行漂移。
//
// 三处共用（risk.Gate 的敞口/持仓数与 T+1 基线、engine 的 resolver、server 的 book_source 装配）：
// 判据只许在这里定义一次，抄第三份就是抄出一份会漂的尺。
// 空表（零行）返回 false——"柜台说空仓"的真值由资金帧的新鲜度与 empty-frame 事件承载，
// 本判据只回答"这张持仓表是不是今天 freshly 写出来的"。
func CounterPositionsReporting(rows []RealPosition, day string) bool {
	seen := false
	for i := range rows {
		if rows[i].UpdatedAt == "" {
			continue
		}
		seen = true
		if !SnapshotBelongsToDay(rows[i].UpdatedAt, day) {
			return false
		}
	}
	return seen
}

// 选源结果的三种取值（引擎 resolver、持仓页读面、前端分流共用同一组字面量）。
//
// 为什么要有 third 值 unknown，而不是继续把"两本账都不可用"标成 counter：
// 标 counter 等于替柜台担保一个它并没有给的读数——11-18 的现场就是屏上写「柜台：可达」
// 却挂着 11-16 冻住的旧行，被当成今天的持仓去改账/下单。unknown 让"我读不到"
// 成为一个能被看见了的事实（前端按它显示「读数未知」并交出空表，绝不画成 0 或旧行）。
const (
	BookSourceCounter = "counter"
	BookSourceManual  = "manual"
	BookSourceUnknown = "unknown"
)

// BookSources 一本账在两维上各自该信谁。
type BookSources struct {
	Cash          string // counter | manual | unknown
	Positions     string // counter | manual | unknown
	CashAsOf      string
	PositionsAsOf string
	TakenOverAt   string
	AnyManual     bool
	// Carried 手动读数虽可用，但记的不是今天的账（跨日带过来的）。
	// 只喂读数面文案（「手动维护，更新于 X（非今日）」）：可用性不按日，呈现必须分得清。
	CashCarried      bool
	PositionsCarried bool
}

// SelectBookSources 选源规则的唯一真源（§MANUAL-BOOK-UNIFY 2026-11-06 裁决 1/5）。
//
// 引擎 resolver 与持仓页读面都调它。规则（owner 2026-11-18 改判后的顺序）：
//  1. 该维手动账可用（判据在 manualAsofUsable：有时刻、非未来、未被接管覆盖；**不看是哪天**）
//     ⇒ 信手动——手动账高于柜台账，owner 明说前夜维护的那本不许过零点就作废；
//  2. 否则该维柜台在报数 ⇒ 信柜台；
//  3. 两者皆否 ⇒ unknown（调用方据此把这一维当"读不到"，绝不拿旧柜台行顶上去）。
//
// 柜台"能证明自己还活着"这件事仍有两个出口，所以第 1 条不会把柜台永久顶掉：
//   - 柜台回报成功落库时 server 会打 taken_over_at，此后 asof 早于接管的手动账自动让位；
//   - owner 重新手敲一笔时 asof 更新，手动账重新赢（判据只看时刻先后）。
//
// 为什么不各写一份：这是钱路径上"哪个数是谁给的"的唯一判据。抄第二份的地方迟早会严一档或松一档，
// 而松的那一处会放行买入、严的那一处会让 owner 看见一张空仓表——两个方向都出过事故。
//
// linkLive＝实盘链路在场（控制器已装配）。它必须是显式入参而不是"看时间戳就够了"：
// real_account 是回报直接写库的，qmt 未启用时库里照样可能留着一行时间戳很新的旧数据。
func SelectBookSources(counterAcc RealAccount, counterAccErr error, counterPos []RealPosition,
	counterPosErr error, view ManualBookView, day string, now time.Time, linkLive bool) BookSources {
	bs := BookSources{
		Cash:        BookSourceUnknown,
		Positions:   BookSourceUnknown,
		TakenOverAt: view.TakenOverAt,
	}
	cashFromCounter := linkLive && counterAccErr == nil && CashReportFresh(counterAcc.UpdatedAt, now)
	posFromCounter := linkLive && counterPosErr == nil && CounterPositionsReporting(counterPos, day)
	if view.CashUsable {
		bs.Cash = BookSourceManual
		bs.CashAsOf = view.CashAsOf
		bs.CashCarried = view.CashCarried
		bs.AnyManual = true
	} else if cashFromCounter {
		bs.Cash = BookSourceCounter
		// 柜台读数时刻照样下发：前端据此判定"确有柜台读数证据"，不必再自己猜一遍。
		bs.CashAsOf = counterAcc.UpdatedAt
	}
	if view.PositionsUsable {
		bs.Positions = BookSourceManual
		bs.PositionsAsOf = view.PositionsAsOf
		bs.PositionsCarried = view.PositionsCarried
		bs.AnyManual = true
	} else if posFromCounter {
		bs.Positions = BookSourceCounter
	}
	return bs
}
