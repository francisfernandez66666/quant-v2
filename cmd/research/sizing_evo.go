// sizing_evo.go — §SIZING-EVO 命令行入口（周度 L1 快筛 / 夜间 L2 灰度+降级）。
//
// 调度形态（§2.7.4）：周度跑 `sizing-evo --weekly`，夜间链尾部跑 `sizing-evo --nightly`。
// 与 research 生命周期**完全独立**：不读战法库/灰度库/候选库，自己的状态与影子库都在
// <dataDir>/sizing_evo 下（§2.7.0 铁律；架构测试 internal/sizing_evo/arch_test.go 锁定）。
//
// 与引擎的关系：本进程只写状态文件与影子库；正在跑的引擎按 TTL(30s) 读到生效条目后
// 热加载（internal/engine/sizing_evo_track.go），与战法库轮询同一套"研究进程改库、
// 引擎进程读库"的分工。
//
// English: CLI entry for the sizing evolution loop — weekly L1 screening and the nightly
// L2/demotion pass; fully independent from the research lifecycle (own state + shadow store).
package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/sizing_evo"
	"quant-trading-v2/internal/store"
)

// cmdSizingEvo 动态仓位进化：周度 L1 / 夜间 L2。
// db 未使用（本回路只消费影子库），保留签名与其它子命令一致。
func cmdSizingEvo(db *store.DB, dbPath string, args []string) {
	fs := flag.NewFlagSet("sizing-evo", flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "数据目录（缺省=研究库所在目录，即 config.json 与 sizing_evo/ 的父目录）")
	weekly := fs.Bool("weekly", false, "跑周度 L1 离线快筛（候选→护栏→进灰度）")
	nightly := fs.Bool("nightly", false, "跑夜间 L2（灰度晋升/回拒 + 生效配置衰退降级）")
	dryRun := fs.Bool("dry-run", false, "只评估不落库（状态写入临时目录，跑完丢弃）")
	fs.Parse(args)
	if !*weekly && !*nightly {
		log.Fatalf("[sizing-evo] 需指定 --weekly 或 --nightly")
	}
	dir := *dataDir
	if dir == "" {
		dir = filepath.Dir(dbPath)
	}
	opslog.Init(dir, 0)

	// 生效配置 = 账号级 sizing（R7 首期全局单回路：空账号取全局键）。
	cm := config.NewManager(filepath.Join(dir, "config.json"))
	base, _ := cm.GetSizingConfigFor("")
	base = base.WithDefaults()
	if !base.EvoOn() {
		log.Printf("[sizing-evo] sizing.evo.enabled 未开启，跳过（这是出厂默认：必须显式开）")
		return
	}

	evoDir := filepath.Join(dir, "sizing_evo")
	storeDir := evoDir
	if *dryRun {
		tmp, err := os.MkdirTemp("", "sizing-evo-dry")
		if err != nil {
			log.Fatalf("[sizing-evo] 建临时目录失败: %v", err)
		}
		defer os.RemoveAll(tmp) // 尽力清理；清理失败不影响结论
		storeDir = tmp
		log.Printf("[sizing-evo] dry-run：状态写 %s（跑完丢弃）", tmp)
	}
	snaps := sizing_evo.NewSnapshotStore(storeDir)
	state := sizing_evo.NewEvoStateStore(storeDir)
	now := time.Now()

	if *weekly {
		rep := sizing_evo.RunL1Weekly(snaps, state, base, now)
		log.Printf("[sizing-evo] %s", rep.Summary())
	}
	if *nightly {
		rep := sizing_evo.RunL2Nightly(snaps, state, base, now)
		log.Printf("[sizing-evo] %s", rep.Summary())
		for _, id := range rep.Promoted {
			log.Printf("[sizing-evo] ✅ 已晋升生效：%s（引擎 30s 内热加载）", id)
		}
		for _, id := range rep.AwaitingHuman {
			log.Printf("[sizing-evo] ⏳ 灰度达标待人工审批：%s（auto_promote=false）", id)
		}
		for _, id := range rep.Rejected {
			log.Printf("[sizing-evo] ❌ 灰度回拒：%s", id)
		}
		if rep.Demoted != nil {
			log.Printf("[sizing-evo] ⚠️ %s", sizing_evo.DemoteSummary(*rep.Demoted))
		}
	}
	// 库规模观测（冷启动判定依据）。
	if c, err := snaps.Counts(); err == nil {
		log.Printf("[sizing-evo] 影子库：%d 快照 / %d 已结算 / %d 交易日", c.Total, c.Settled, c.Days)
	}
}
