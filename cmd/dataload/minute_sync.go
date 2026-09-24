// §MINUTE-K 分钟 K 线装载（2026-09-24 owner 裁决「分钟 K 落库升级：建分钟表 + 回填 + 动量回放换真 5 分钟」）。
//
// 子命令：dataload minute-sync [--scale 5] [--count 5025] [--codes 文件] [--since YYYYMMDD]
//
//	[--limit 500] [--incremental] [--max-fail-pct 10]
//
//	（--codes 写在子命令**之前**也认：dataload --db x --codes 清单 minute-sync —— 全局 flag 与
//	子 flag 同源一个文件，不会因为 Go flag 包在子命令处停解析就把用户给的清单丢掉。）
//
// 两种模式（同一条写入路径，幂等由主键保证）：
//
//	回填（缺省）：从分钟上游一次拉最近 count 根（新浪 5 分钟实测上限 5025 根 ≈ 5 个月，
//	              **没有分页**，所以"回填 3 年"这件事在数据源层面就不成立——不编，见下面的出门打印）。
//	              取数清单缺省 = 近 --since 起打过板/炸过板的票（store.MinutePoolUniverse），
//	              按出现次数降序截 --limit（缺省 500 只 ≈ 250 万行，与 owner 批准的规模一致）；
//	              想指定别的清单就 --codes。
//	日增（--incremental）：只对**库里已有**的票各拉最近 --count 根（缺省给 60 根足够覆盖当日 48 根
//	              + 集合竞价尾量），清单自维护、不需要人记着改名单。收盘后跑（调度器夜间步骤）。
//
// 出门口径（本仓主题：降级不许报成功）：
//
//	· 取数只走**严格不复权链**（新浪→同花顺→腾讯；末腿东财是前复权，写进本表会在除权日造出假跳水，
//	  宁可该票计成失败）。清单里写 ts_code 也取得到数：数据层归一为裸 6 位代码再请求，
//	  落库主键仍是 ts_code（与 daily.ts_code 同一写法）。
//	· 逐票失败只计数不中断（一两只票上游抽风不该让整轮回填作废），但失败率超过 --max-fail-pct
//	  判成本轮失败（退出码非 0），因为那种形态通常是封 IP/接口改版，不是偶发；
//	· 库里最终 0 行 ⇒ 直接失败退出（"跑完了但没有数据"绝不能算成功）；
//	· 打印 rows/codes/span/avg_bars_per_code_day：平均根数明显低于 48 就说明窗口被上游截断，
//	  读者一眼看得见，而不是拿到一个"看起来成功"的数字。
//
// English: minute-bar loader — one bounded last-N window per code (upstreams have no pagination),
// pool-ranked default universe, self-maintaining incremental list, and an honest exit code.
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// minuteSyncOpts minute-sync 的入参（独立成结构体是为了让单测能直接调 runMinuteSync，
// 不必经过 flag 解析；命令行与将来的调度器共用这一份定义）。
type minuteSyncOpts struct {
	Scale       int    // 分钟周期（本批只跑 5）
	Count       int    // 每票拉取的根数（上游给多少算多少，5 分钟实测封顶 5025）
	CodesFile   string // 取数清单文件（每行一个 ts_code，# 注释）；空=按池
	Since       string // 池统计起点 YYYYMMDD（--codes 为空时生效）
	Limit       int    // 池清单截断（防手滑把全市场灌进来）
	Incremental bool   // true=只补库里已有的票（日增），false=回填
	MaxFailPct  int    // 失败率上限（百分比），超过则整轮判失败
}

// minuteFetcher 取数抽象（真实实现 = *data.DataCoordinator 的 GetUnadjustedMinuteKLine）。
// 为什么收成接口：装载器的行为（0 行判失败、失败率上限、清单解析）必须能单测，
// 而真实上游只有"最近 5025 根"这一扇窗口、且会因封 IP 随机失败——拿真接口测出来的
// 红绿不可复现，等于没有测试。
// 为什么绑的是**严格不复权**那条链而不是通用 GetMinuteKLine：通用链的末腿是东财前复权
// （fqt=1），写进 minute_klines（承诺不复权）就等于在除权日给分钟 MACD 埋一根假跳水。
// 见 §MINUTE-K / internal/data/source.go。
type minuteFetcher interface {
	GetUnadjustedMinuteKLine(code string, scale, count int) ([]data.KLine, error)
}

// runMinuteSync 执行一轮分钟装载并返回落库行数。real 取数走 DataCoordinator
// （严格不复权链：新浪→同花顺→腾讯，末腿东财前复权按口径拒用），落库走
// store.UpsertMinuteBars（主键幂等）。清单里写 ts_code（600000.SH）还是裸代码（600000）都可以，
// 数据层会归一成上游认得的裸代码再去请求，落库主键仍是清单里那份 ts_code 形态。
func runMinuteSync(db *store.DB, dc minuteFetcher, o minuteSyncOpts, now time.Time) (int64, error) {
	if o.Scale <= 0 {
		o.Scale = 5
	}
	if o.Count <= 0 {
		o.Count = 5025
	}
	codes, err := minuteTargetCodes(db, o)
	if err != nil {
		return 0, err
	}
	if len(codes) == 0 {
		return 0, fmt.Errorf("minute-sync: 取数清单为空（回填请用 --codes 或先跑池同步；日增模式要求库里已有分钟数据）")
	}
	log.Printf("[minute-sync] scale=%d count=%d codes=%d mode=%s", o.Scale, o.Count, len(codes), minuteModeName(o.Incremental))
	var written, failed int64
	for i, code := range codes {
		kls, err := dc.GetUnadjustedMinuteKLine(code, o.Scale, o.Count)
		if err != nil || len(kls) == 0 {
			failed++
			log.Printf("[minute-sync] %s 取数失败（第 %d/%d 只）: %v", code, i+1, len(codes), err)
			continue
		}
		n, err := db.UpsertMinuteBars(toMinuteBars(code, o.Scale, kls))
		if err != nil {
			return written, fmt.Errorf("minute-sync %s 落库: %w", code, err)
		}
		written += n
		if (i+1)%50 == 0 {
			log.Printf("[minute-sync] 进度 %d/%d，累计落库 %d 行", i+1, len(codes), written)
		}
	}
	st, err := db.MinuteTableStats(o.Scale)
	if err != nil {
		return written, fmt.Errorf("minute-sync 收尾统计: %w", err)
	}
	log.Printf("[minute-sync] 本轮写入 %d 行；失败 %d 只 / %d 只；表内 %s", written, failed, len(codes), st)
	if st.Rows == 0 {
		return 0, fmt.Errorf("minute-sync: 本轮 0 行落库（上游全失败或清单里的代码取不到分钟线），不得算成功")
	}
	if pct := float64(failed) * 100 / float64(len(codes)); pct > float64(o.MaxFailPct) {
		return written, fmt.Errorf("minute-sync: 失败率 %.1f%% 超过上限 %d%%（多为封 IP/接口改版，不是偶发），本轮判失败", pct, o.MaxFailPct)
	}
	return written, nil
}

// minuteTargetCodes 解出本轮清单：--codes 文件优先；日增模式用库里已有的票（自维护）；
// 回填模式用池排名。三条路互不重叠，避免"忘了传 --codes 就把全市场灌进来"。
func minuteTargetCodes(db *store.DB, o minuteSyncOpts) ([]string, error) {
	if o.CodesFile != "" {
		return readCodesFile(o.CodesFile)
	}
	if o.Incremental {
		codes, err := db.MinuteCodes(o.Scale)
		if err != nil {
			return nil, fmt.Errorf("读取已存分钟代码失败: %w", err)
		}
		return codes, nil
	}
	since := o.Since
	if since == "" {
		since = time.Now().AddDate(0, 0, -90).Format("20060102") // 缺省近 90 日历日的池
	}
	return db.MinutePoolUniverse(since, o.Limit)
}

// toMinuteBars 把上游 KLine（time.Time）转成落库行。
// ts 一律格式化成**北京时间墙钟**字符串：上游解析时已经用 cst（Asia/Shanghai）定位时区，
// 这里再显式 In(cst 等价固定 +8) 一次，防止某个源返回 UTC 时区的时间把同一天的根劈成两半
// （回放按 ts 前缀切日，劈半就是当天少一半根、MACD 口径静默改变）。
func toMinuteBars(code string, scale int, kls []data.KLine) []store.MinuteBar {
	out := make([]store.MinuteBar, 0, len(kls))
	for _, k := range kls {
		if k.Close <= 0 {
			continue // 零价根（停牌/占位）不落库：它们会让 MACD 出现假跳水
		}
		out = append(out, store.MinuteBar{
			TsCode: code, Scale: scale, Ts: k.Date.In(cstZone).Format("2006-01-02 15:04:05"),
			Open: k.Open, High: k.High, Low: k.Low, Close: k.Close,
			Vol: k.Volume, Amount: k.Amount,
		})
	}
	return out
}

// cstZone 北京时区（与 internal/data 的 cst 同一定义；这里自带一份避免依赖包内私有变量）。
var cstZone = time.FixedZone("CST", 8*3600)

func minuteModeName(inc bool) string {
	if inc {
		return "incremental(日增)"
	}
	return "backfill(回填)"
}

// parseMinuteFlags 从子命令参数里解析 minute-sync 的入参（独立 flag 集合，
// 不与 dataload 顶层的 --start/--end 抢名字）。
func parseMinuteFlags(args []string) (minuteSyncOpts, error) {
	fs := flag.NewFlagSet("minute-sync", flag.ContinueOnError)
	o := minuteSyncOpts{}
	fs.IntVar(&o.Scale, "scale", 5, "分钟周期（分钟数）")
	fs.IntVar(&o.Count, "count", 5025, "每票拉取根数（5 分钟上游封顶约 5025 根≈5 个月）")
	fs.StringVar(&o.CodesFile, "codes", "", "取数清单文件（每行一个 ts_code）；留空=按池/按已存代码")
	fs.StringVar(&o.Since, "since", "", "池统计起点 YYYYMMDD（缺省近 90 日历日，仅回填生效）")
	fs.IntVar(&o.Limit, "limit", 500, "池清单只数上限（仅回填生效）")
	fs.BoolVar(&o.Incremental, "incremental", false, "只补库里已有的票（收盘后日增）")
	fs.IntVar(&o.MaxFailPct, "max-fail-pct", 10, "失败率上限（百分比），超过则整轮判失败")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	return o, nil
}
