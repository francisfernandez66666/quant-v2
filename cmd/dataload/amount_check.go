// amount_check.go — §0929SCALE-⑩ 落库后量纲抽检的装载侧接线（dataload 子命令 + 收尾自检）。
//
// 本文件只放"装载程序怎么用抽检"，判据本体在 internal/store/amount_scale_probe.go。
// 两个入口：
//  1. `dataload [flags] amount-check [--date YYYYMMDD] [--rows N] [--json]`
//     —— 独立可执行腿：ok/no-data 退 0，判红退 1。夜间校验与部署后验收拨这一条
//     （见 scripts/verify_nightly_guangzhou.sh 的量纲腿）。
//  2. `checkLoadedAmountScale` —— 日线装载（tushare 腿）收尾自动抽最近一个已落交易日，
//     判红只打 WARN：数据已经写进去了、装载也确实成功，把成功改判失败会让断点续传
//     误以为"那天没拉"从而重复整批重写，风险大于收益；红要由**独立腿**去拦停。
//
// English: §0929SCALE-⑩ — the loader-side wiring of the post-load caliber probe:
// a standalone `amount-check` subcommand plus a non-fatal self-check after daily loads.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// cmdAmountCheck 实现 `dataload amount-check`：抽样判定 daily.amount 是元还是千元口径。
// 参数取自子命令自身 flag 集（与全局 --db/--provider 共存，写在子命令后即可覆盖）。
// 退出码语义钉死：0＝口径正常或无数据（无数据由新鲜度腿负责报警，这里不重复判红），
// 1＝判红（千元/双重换算/混源），2＝读取失败。
// English: `dataload amount-check` — exit 0 ok, 1 caliber failure, 2 probe error.
func cmdAmountCheck(db *store.DB, args []string) {
	fs := flag.NewFlagSet("amount-check", flag.ExitOnError)
	date := fs.String("date", "", "抽检交易日 YYYYMMDD（空＝表内最近交易日）")
	rows := fs.Int("rows", 800, "抽样上限（按 ts_code 升序，确定性取样）")
	asJSON := fs.Bool("json", false, "以 JSON 输出读数（供脚本读 verdict，不再解析日志行）")
	_ = fs.Parse(args)

	p, err := db.ProbeDailyAmountScale(*date, *rows)
	if err != nil {
		log.Printf("[amount-check] 抽检失败: %v", err)
		os.Exit(2)
	}
	if *asJSON {
		// JSON 模式给脚本消费：字段名与 struct tag 一致，改动会连带 verify 腿读不到 verdict。
		if encErr := json.NewEncoder(os.Stdout).Encode(p); encErr != nil {
			log.Printf("[amount-check] JSON 输出失败: %v", encErr)
			os.Exit(2)
		}
	} else {
		log.Printf("[amount-check] 表=%s 日=%s 取样=%d/%d 千元行=%d 正常行=%d 超带行=%d 中位均价=%.4f 结论=%s %s",
			p.Table, p.Date, p.Rows, p.WantRows, p.Low, p.Normal, p.High, p.MedianRatio, p.Verdict, p.Reason)
	}
	if p.Red() {
		// 判红文案要点名"后果"：只说单位不对，值班的人不知道该不该停池。
		log.Printf("[amount-check][P1] 成交额量纲判红：%s ⇒ 股池流动性质控（阈值按元）与回放成本模型会整体失真，"+
			"且不会有任何上游报错。先核对装载腿是否绕过写侧归一（internal/data/amountscale.go）。", p.Reason)
		os.Exit(1)
	}
	log.Printf("[amount-check] 口径校验通过（%s）", p.Verdict)
}

// checkLoadedAmountScale 在日线装载收尾抽最近一个已落交易日，判红只 WARN、不改判装载结果。
// 语义依据见文件头：断点续传按日期整批重写，把成功改成失败会诱发重复写盘；
// 需要拦停的场景请拨 `amount-check` 独立腿。
// English: post-load self-check — warns on a bad caliber, never fails a successful load.
func checkLoadedAmountScale(db *store.DB) {
	// 兜底窗口给最近 30 天：库里最新日期就在其中，取不到则 probe 自然回 no-data。
	from := cntime.DayCompactOf(time.Now().AddDate(0, 0, -30))
	p, err := db.ProbeDailyAmountScale("", 0)
	if err != nil {
		log.Printf("[dataload][WARN] §0929SCALE 收尾量纲抽检未跑成（不影响已落库数据）：%v", err)
		return
	}
	switch {
	case p.Red():
		log.Printf("[dataload][P1] §0929SCALE 量纲抽检判红：%s（取样窗口自 %s 起，%d 行）⇒ 请立即人工核对装载腿与历史混源",
			p.Reason, from, p.Rows)
	case p.Verdict == store.AmountScaleNoData:
		log.Printf("[dataload] §0929SCALE 量纲抽检无样本（daily 表空或日期异常），跳过")
	default:
		log.Printf("[dataload] §0929SCALE 量纲抽检通过：%s 中位日均价 %.2f 元（取样 %d 行）", p.Date, p.MedianRatio, p.Rows)
	}
}

// scaleHint 给 tushare 写入路径用的一行量纲留痕：把换算行数打进日志。
// 归一本身由 data.NormalizeTushareAmount 完成；白名单表有行却一行都没换，说明上游
// 字段名或形态变了，必须 WARN 出来让人确认，不能当"正常"。
// English: logs how many rows had their amount converted (and warns when a whitelisted
// table wrote rows but converted none).
func scaleHint(table string, changed, total int) {
	switch {
	case changed == 0 && total > 0:
		log.Printf("[dataload][WARN] §0929SCALE %s 共 %d 行但 amount 换算 0 行——请确认上游字段仍是千元口径", table, total)
	case changed > 0:
		log.Printf("[dataload] §0929SCALE %s amount 千元→元 换算 %d 行（×1000）", table, changed)
	}
}
