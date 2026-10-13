// pool_probe.go — §POOL-STAMP 盘口池子的**只读**日期探针（dataload 子命令）。
//
// 用法:
//
//	dataload [全局 flags] pool-probe --kind limit-up            # 拨今日
//	dataload [全局 flags] pool-probe --kind all --date 20261012 --json
//
// 为什么要一条只读腿而不只靠同步链的日志：
//   - 池子回包里的 `data.timestamp` 到底是"数据日"还是"请求时刻"，本仓**没有现网取证**。
//     同步链里那条 payload_day/match 读数已经落码，但默认不拦停（理由见 poolSyncOpts 注释）；
//     要把"warn"提级成"judge"，需要先有一串与写库无关的读数——现网拨一条命令就能取，
//     且不会因为探针而改动任何一天的池子（§审计只读、§实现不删除 两条纪律同时满足）。
//   - 影子日（幻影日）判据需要"库里上一日"作对照，只读腿照样能算：探针构造的行集与同步链
//     共用 limitUpRowsFromItems，验的与写的是同一份口径。
//
// 退出码语义钉死（与 amount-check 同一取向）：0＝读数已取得（**不管读数内容是什么**），
// 2＝拉取或读库失败。这里刻意没有 1：把"命令行打错"或"上游给的是昨天"回成 1，
// 现网就会看到一条并不存在的数据故障；判红的权力留给人看完读数之后再授予。
//
// English: read-only pool-date probe — payload timestamp, three-state date match,
// and the phantom-day (shadow) comparison against the nearest earlier stored day.
// Exit 0 whenever a reading was obtained, 2 only on fetch/DB failure; there is no exit 1.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// poolProbeRow 一条池子的探针读数（字段名即现网日志与 --json 的契约，改动会连带门禁腿读不到）。
type poolProbeRow struct {
	Pool              string `json:"pool"`                // limit_up|limit_down|break_pool
	Requested         string `json:"requested"`           // 请求的交易日 yyyyMMdd
	PayloadMS         int64  `json:"payload_ms"`          // 上游 data.timestamp（毫秒，0=未给出）
	PayloadDay        string `json:"payload_day"`         // 时间戳换成的北京日（""=无从判定）
	Match             string `json:"match"`               // yes|no|unknown
	Rows              int    `json:"rows"`                // 本次回包行数
	StoredOnRequested int    `json:"stored_on_requested"` // 库里该交易日已有的行数（0=这一天还没落过）
	ShadowOf          string `json:"shadow_of"`           // 影子日对照源（库内严格早于请求日的最近有数据日）
	ShadowState       string `json:"shadow_state"`        // same|different|no_earlier_day|not_supported
}

// String 一行固定形状：所有键都在，值可以为 <none>，但**不能整键消失**——
// 键消失与键值为空在 grep 里同形，会把"探针没跑到这一步"读成"这一步没有数据"。
func (r poolProbeRow) String() string {
	return fmt.Sprintf("pool=%s requested=%s payload_ms=%d payload_day=%s match=%s rows=%d stored_on_requested=%d shadow_of=%s shadow_state=%s",
		r.Pool, r.Requested, r.PayloadMS, orNone(r.PayloadDay), r.Match, r.Rows,
		r.StoredOnRequested, orNone(r.ShadowOf), r.ShadowState)
}

// orNone 空值的可见形（同 dayOr，探针侧独立命名以免两处口径互改后悄悄不一致）。
func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

// fetchPoolByKind 按池名取回包（三种之外的名字直接报错，不降级成涨停池）。
func fetchPoolByKind(client *data.HithinkClient, kind string, dateMs int64) (data.HithinkPool, error) {
	switch kind {
	case "limit-up":
		return client.LimitUpPoolStamped(dateMs)
	case "limit-down":
		return client.LimitDownPoolStamped(dateMs)
	case "break-pool":
		return client.LimitBreakPoolStamped(dateMs)
	}
	return data.HithinkPool{}, fmt.Errorf("未知池名 %q（可选 limit-up|limit-down|break-pool|all）", kind)
}

// poolInternalName 池名到内部读数名的映射（日志里用后者，与同步链的 pool= 取值保持一致）。
func poolInternalName(kind string) string {
	switch kind {
	case "limit-up":
		return "limit_up"
	case "limit-down":
		return "limit_down"
	default:
		return "break_pool"
	}
}

// probeOnePool 拨一个池子并组成读数；失败上抛（调用方按池继续，坏池不挡其它池的取证）。
func probeOnePool(client *data.HithinkClient, db *store.DB, kind, tradeDate string) (poolProbeRow, error) {
	var dateMs int64
	if t, err := time.ParseInLocation("20060102", tradeDate, time.Local); err == nil {
		dateMs = t.UnixMilli()
	}
	pool, err := fetchPoolByKind(client, kind, dateMs)
	if err != nil {
		return poolProbeRow{}, err
	}
	row := newPoolProbeRow(kind, tradeDate, pool)
	if kind == "limit-up" {
		// 影子日判据目前只有涨停池建行签名：它是情绪链（家数/最高连板/炸板率）的输入，
		// 也是 2026-10-12 那 72 行幻影数据的实证对象。另两池先只出日期读数，不假装有三态判定。
		cnt, cerr := db.LimitUpCountOnDate(tradeDate)
		if cerr != nil {
			return row, fmt.Errorf("读取请求日已落行数失败(%s): %w", tradeDate, cerr)
		}
		row.StoredOnRequested = cnt
		shadow, same, serr := db.LimitUpShadowOf(tradeDate, limitUpRowsFromItems(pool.Items, tradeDate))
		if serr != nil {
			return row, fmt.Errorf("影子日判据读取失败(%s): %w", tradeDate, serr)
		}
		// 影子日三态落行：影子日命中且签名相同=same（回包只是同一天的重复投递）、
		// 命中但签名不同=different（那 72 行幻影数据的同族形态）、无更早日可比=no_earlier_day。
		row.ShadowOf = shadow
		switch {
		case shadow == "":
			row.ShadowState = "no_earlier_day"
		case same:
			row.ShadowState = "same"
		default:
			row.ShadowState = "different"
		}
		return row, nil
	}
	row.ShadowState = "not_supported"
	return row, nil
}

// newPoolProbeRow 由回包组出与同步链同形的日期读数（时间戳→北京日→三态命中）。
func newPoolProbeRow(kind, tradeDate string, pool data.HithinkPool) poolProbeRow {
	name := poolInternalName(kind)
	ro := newPoolStampReadout(name, tradeDate, pool)
	return poolProbeRow{Pool: ro.Pool, Requested: ro.Requested, PayloadMS: ro.PayloadMS,
		PayloadDay: ro.PayloadDay, Match: ro.Match, Rows: ro.Rows}
}

// cmdPoolProbe 实现 `dataload pool-probe`：只读取数，绝不写库。
func cmdPoolProbe(db *store.DB, args []string) {
	fs := flag.NewFlagSet("pool-probe", flag.ExitOnError)
	kind := fs.String("kind", "limit-up", "池名: limit-up|limit-down|break-pool|all")
	dateF := fs.String("date", "", "请求的交易日 yyyyMMdd（缺省=今日）")
	asJSON := fs.Bool("json", false, "以 JSON 输出读数（供脚本读 match/shadow_state）")
	if err := fs.Parse(args); err != nil {
		log.Printf("[pool-probe] 参数解析失败: %v", err)
		os.Exit(2)
	}
	tradeDate := *dateF
	if tradeDate == "" {
		tradeDate = cntime.DayCompactOf(time.Now())
	}
	client, err := data.NewHithinkClient()
	if err != nil {
		log.Printf("[pool-probe] 同花顺（新）客户端初始化失败: %v", err)
		os.Exit(2)
	}
	kinds := []string{*kind}
	if *kind == "all" {
		kinds = []string{"limit-up", "limit-down", "break-pool"}
	}
	var rows []poolProbeRow
	failed := 0
	for _, k := range kinds {
		r, perr := probeOnePool(client, db, k, tradeDate)
		if perr != nil {
			// 单池失败也要打一行：整池静默失踪比一条错误更难归因（§降级链会兜死分支）。
			log.Printf("[pool-probe] pool=%s requested=%s probe_error=%v", k, tradeDate, perr)
			failed++
			continue
		}
		rows = append(rows, r)
		if !*asJSON {
			log.Printf("[pool-probe] %s", r.String())
		}
	}
	if *asJSON {
		if encErr := json.NewEncoder(os.Stdout).Encode(map[string]any{
			"requested": tradeDate, "pools": rows, "failed": failed}); encErr != nil {
			log.Printf("[pool-probe] JSON 输出失败: %v", encErr)
			os.Exit(2)
		}
	}
	if failed > 0 {
		log.Printf("[pool-probe] %d 个池子取数失败（见上面的 probe_error= 行）", failed)
		os.Exit(2)
	}
}
