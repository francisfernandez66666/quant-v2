// library_watch.go — 战法库版本戳轮询（§0929LIB-WATCH，09-29 全量审计批 P1-3）。
//
// 这条腿补的是什么（缺陷报告的因果链）：
//   - 夜间研究（独立进程 cmd/research）判某条已应用战法失效后，把它在 applied_factors.json /
//     applied_patterns.json 里置 Enabled=false —— 文件与库确实变了；
//   - 但**正在跑的引擎不知道**：战法 runner 只在建引擎时读盘（engine.Registry.build →
//     newAccountRunners → LoadEnabled{Factor,Pattern}Rules），注册表内没有任何失效/重建入口，
//     真正的重建入口 reloadLibraries 只由 HTTP 处理器触发；
//   - 结果是一条"该停"的战法可以在整个交易日内继续出信号、继续下单，直到引擎重启或有人在前端
//     点一次重载。反向同样成立（人工放开一条战法，运行引擎不吃）。
//   - 过去有两处注释把这写成"即时生效"（research/lifecycle_run.go 文件头、cmd/research/auto.go），
//     错误的不变量声明正是这条缝一直没被怀疑的原因——本批已把那句改成事实描述。
//
// 为什么选版本戳轮询而不是让研究进程回调 HTTP：跨机（研究侧广州 Windows / 引擎侧首尔 Ubuntu，
// 迁移期两台机器、路径不同源）要处理鉴权、重试与失败语义，风险面比"两侧读同一份库"大得多。
// 轮询判据是文件内容指纹，因此天然覆盖"同一台机器上另一个进程改了库"这种同机跨进程场景。
//
// 三条安全边界（本文件的自证口径）：
//   - 只在指纹**真的变化**时才动引擎——避免把"全量重建 + 重设模拟盘资金池"变成每分钟抖动
//     （reloadLibraries 会重建分仓模板，高频触发会打乱战法扫描与分仓显示）；
//   - 重载失败**不推进锚**：下一轮仍判"有变化"继续重试，绝不在引擎还吃着旧库的时候把坏库
//     认成新库（那等于把"降级已生效"这句话写进一个没生效的状态里）；
//   - 读指纹/注入失败都有节流告警（首轮 + 每 10 轮，约 1 分钟/10 分钟一次），
//     既不会静默烂掉，也不会把推送通道刷爆。
//
// English: strategy-library version-stamp polling. The nightly research process disables a
// strategy by editing applied_factors.json / applied_patterns.json; engines only read those files
// when building runners, so a disabled strategy used to keep trading until a restart or a manual
// reload. This watcher compares a content fingerprint of the two library files once a minute and
// hot-reloads every engine only when it actually changes; failures keep the old fingerprint (so it
// retries) and raise a throttled high-level alert.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"quant-trading-v2/internal/opslog"
)

// libraryPollInterval 版本戳轮询节奏。取 1 分钟的理由：夜间降级发生在盘后，最需要盯的是
// **盘中**有人（或脚本）改了库；60s 既远小于一个交易时段，又不会让"读两个小 JSON"变成热点。
const libraryPollInterval = 60 * time.Second

// libraryFingerprintFiles 参与指纹计算的库文件（与引擎实际读取的两库一一对应）。
// 名单必须与 LoadEnabled{Factor,Pattern}Rules 的读盘对象一致：把不影响引擎的文件
// 纳入指纹会让轮询空转，漏掉影响的则让"生效"永远等不到。
var libraryFingerprintFiles = []string{"applied_factors.json", "applied_patterns.json"}

// libraryFingerprint 计算战法库内容指纹（sha256，按文件名顺序混入"名字/长度/内容"）。
// 文件不存在按 "absent" 计入（缺库是稳定状态，不是错误——引擎侧读到的是空规则集）；
// 真读不动（权限、磁盘故障）才返回 error，交由调用方保持旧锚并重试。
// English: content fingerprint of the two library files; a missing file hashes as "absent"
// (a stable state), while an unreadable file is an error the caller must retry on.
func libraryFingerprint(dataDir string) (string, error) {
	h := sha256.New()
	for _, name := range libraryFingerprintFiles {
		// 文件名本身进摘要：两个库内容相同但互换了名字，也应判为"库变了"。
		fmt.Fprintf(h, "name=%s|", name)
		f, err := os.Open(filepath.Join(dataDir, name))
		if err != nil {
			if os.IsNotExist(err) {
				h.Write([]byte("absent|")) // 缺库：按稳定标记计入，不当失败
				continue
			}
			return "", fmt.Errorf("读取战法库 %s 失败: %w", name, err)
		}
		// 逐段写入内容 + 结尾长度分隔符，避免"两个文件内容拼接相同"的指纹碰撞。
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", fmt.Errorf("读取战法库 %s 中断: %w", name, err)
		}
		st, serr := f.Stat()
		f.Close()
		if serr != nil {
			return "", fmt.Errorf("统计战法库 %s 失败: %w", name, serr)
		}
		fmt.Fprintf(h, "|%d|", st.Size())
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// StartLibraryWatcher 启动战法库版本戳轮询协程（ctx 随进程退出；调用点在 cmd/quant/main.go）。
// 语义：指纹相对锚变化 → 调 reloadLibraries 全量注入；成功才把锚推进到新指纹并留一行审计。
// 首轮只建锚不动引擎（进程启动时 build 已经读过同一份库，开局再重建一次分仓属无谓抖动）。
// English: starts the polling goroutine — it anchors on the current fingerprint and only reloads
// the engines when it changes, advancing the anchor solely on success so failures keep retrying.
func (s *Server) StartLibraryWatcher(ctx context.Context) {
	if s.researchDir == "" {
		log.Printf("[library-watch] 未接入研究目录，跳过战法库版本戳轮询（战法规则变更只能靠重启加载）")
		return
	}
	go func() {
		tk := time.NewTicker(libraryPollInterval)
		defer tk.Stop()
		anchor, err := libraryFingerprint(s.researchDir)
		if err != nil {
			// 建锚失败不退出：留空锚，下一轮读到什么就按"有变化"处理一次（幂等重载，无副作用风险）。
			log.Printf("[P1][library-watch] 启动建锚失败（将按轮询重试）: %v", err)
		}
		failStreak := 0 // 连续失败轮数（告警节流用：首轮 + 每 10 轮推一次）
		// §0929HB-3：启动即喂一次陈旧度，别让首个读数等满一个轮询周期。
		s.refreshLibraryStalenessGauge(time.Now())
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				// §0929HB-3：陈旧度读数按轮喂（指纹错误也要喂——库读不出内容时"多久没动过"
				// 仍然问得出 mtime，两件事各自独立出门）。细节见 library_staleness.go。
				s.refreshLibraryStalenessGauge(time.Now())
				cur, err := libraryFingerprint(s.researchDir)
				if err != nil {
					// 库读不出指纹＝无法判断有没有变。保持旧锚（引擎继续吃上一份好库），
					// 按节流推告警；这里**绝不**推进锚，否则等于宣称"新库已生效"。
					failStreak++
					if failStreak == 1 || failStreak%10 == 0 {
						s.alertLibraryReloadFailure(fmt.Sprintf("战法库指纹读取失败（第 %d 轮，引擎继续按上一份库运行）: %v", failStreak, err))
					}
					continue
				}
				if failStreak > 0 {
					opslog.Logf("library", "§0929LIB-WATCH 战法库指纹读取恢复（此前连续 %d 轮失败）dir=%s", failStreak, s.researchDir)
					failStreak = 0
				}
				if cur == anchor {
					continue // 库没变：不动引擎、不动分仓模板
				}
				// 变了才重建：reloadLibraries 内部会自行读库并逐引擎注入，失败已自带告警。
				if err := s.reloadLibraries(); err != nil {
					// 不推进锚 → 下一轮仍判"有变化"继续重试（引擎此刻仍吃旧库，这是已知且已告警的态）。
					log.Printf("[P1][library-watch] 战法库已变更但注入失败，保持旧锚待重试: %v", err)
					continue
				}
				prev := anchor
				anchor = cur
				log.Printf("[library-watch] 战法库版本戳变化，已自动注入全部引擎 dir=%s old=%.12s new=%.12s",
					s.researchDir, prev, cur)
				opslog.Audit("library_auto_reload", "system", s.researchDir,
					fmt.Sprintf("fingerprint %s -> %s", shortStamp(prev), shortStamp(cur)))
			}
		}
	}()
}

// shortStamp 指纹缩略（审计/日志只留前 12 位，够定位是哪一次变更，也不把摘要刷成噪音）。
// English: first 12 chars of a fingerprint (or "none" for the empty anchor).
func shortStamp(v string) string {
	if v == "" {
		return "none"
	}
	if len(v) > 12 {
		return v[:12]
	}
	return v
}
