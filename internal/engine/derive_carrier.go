package engine

// derive_carrier.go —— 做 T 的时序常量**只读导出**。
//
// 为什么加这一处（2026-11-04）
//   幽灵宽限、腿龄门、行情新鲜度门、反 T 必须留下的底仓——这四枚数决定了 T 腿
//   "什么时候不许判、判错了要不要收摊"。它们在 Go 里已经是**带依据注释的常量**
//   （intraday_t_live.go 逐条写了为什么是这个值），但都是非导出的，
//   外部进程（QMT 内置策略 / 本地执行器）拿不到，只能抄。
//
//   抄的后果不是"抄错"这么简单：抄对了也仍然有两本账。Go 侧哪天把幽灵宽限从 90s
//   调到 120s，载体侧那一份不会跟着动，两边就在同一轮里用不同的时序判同一笔 T。
//
// 边界
//   本文件一个数字都没有，全部转调既有常量；单位一律换算成**秒/手**这种载体侧的口径，
//   换算用的是 time.Duration 自己的 Seconds()，不是这里手写 60。
//   English: no literal here; every value delegates to the existing constant, and the
//   seconds conversion uses Duration.Seconds() rather than a hand-written 60.

import "time"

// TGhostGraceSeconds 幽灵 open 判定宽限（秒）。真源：tLGhostGrace。
func TGhostGraceSeconds() float64 { return seconds(tLGhostGrace) }

// TPremiseGraceSeconds 腿龄门——开腿后这么久内不许判"本笔止损"（秒）。真源：tLPremiseGrace。
func TPremiseGraceSeconds() float64 { return seconds(tLPremiseGrace) }

// TRegimeStaleSeconds 行情研判读数的新鲜度门（秒）。真源：tRegimeStale。
func TRegimeStaleSeconds() float64 { return seconds(tRegimeStale) }

// TMinBaseKeepQty 反 T 卖腿必须留下的底仓（手数为单位的最小数量）。真源：tLMinBaseKeep。
func TMinBaseKeepQty() int { return tLMinBaseKeep }

// TRegimeMinVotes 研判票数"够了"线。真源：tRegimeMinVotes（Go 侧只用于日志分档、不做门，
// 载体侧如果要用它做门，必须先看这条注释——那不是 Go 的语义）。
func TRegimeMinVotes() int { return tRegimeMinVotes }

func seconds(d time.Duration) float64 { return d.Seconds() }

// ── §DERIVE-CARRIER-1105 第二批：T 的**词表 / 比例 / 钟点**只读导出 ──────────────────
//
// 为什么这批也要从 Go 出（而不是让载体自己写）：rules_t.py 是从本文件逐行移植的判定链，
// 它按 params 读走"哪些市场状态放行、哪些拦截""卖腿必须留下多少底仓""什么字样算被撤"
// "开盘冷静期从几点起算"。这四件事在 Go 都是**判据本身**（switch 的 case、quota 的除法、
// Contains 的字样、tLDayStart 的钟点），不是配置项——判据在两处各写一遍，
// 就是"引擎改了门、载体还在按旧门跑"，而两边都自检不出来。
// 本文件仍然一个数字都没有：每一项都转调产品代码正在比较的那一枚常量。
// English: the vocabulary / ratio / clock literals the live T gate itself compares against,
// exported read-only so the ported carrier cannot grow a second copy of any of them.

// TRegimeAllowStates 行情门**放行**的市场状态档（真源：regimeBlock 的 case 列表）。
// 只有一档：盘整。空串不在这里——它在 Go 里是"弃权"（放行并出声），不是一档读数，
// 把它并进放行表会让"从未产生读数"看起来像"读到了放行档"。
func TRegimeAllowStates() []string { return []string{tLRegimeRange} }

// TRegimeBlockStates 行情门**拦截**的市场状态档（真源同上：牛市 / 熊市）。
// 未知状态在 Go 走 default 分支 fail-closed，因此它既不在放行表也不在拦截表——
// 载体侧同样按"两张表都不在 ⇒ 不做 T"判，语义才对齐。
func TRegimeBlockStates() []string { return []string{tLRegimeBull, tLRegimeBear} }

// TSellKeepPositionPct 反 T 卖腿必须留下的原持仓比例（%）。真源：tLSellKeepPositionPct。
func TSellKeepPositionPct() float64 { return float64(tLSellKeepPositionPct) }

// TCancelStatusToken 委托状态里"算被撤"的字样。真源：tLStatusCancelToken（tLUnfilledReasonOf 比较的就是它）。
// 载体必须收**字符串**而不是自己写中文：产物里的中文字面量会被 GBK 终端换成 ?。
func TCancelStatusToken() string { return tLStatusCancelToken }

// TFeeBufferPct 冻结款费用缓冲（%）。真源：tLFeeBufferPctDefault——
// 本包在"配置层给不出该值"时用的就是它（不是 0：0 缓冲会让"差几毛钱买不回"变成一次半态）。
func TFeeBufferPct() float64 { return tLFeeBufferPctDefault }

// TDayStartClock 开盘冷静期的起算钟点（"HH:MM"，北京时）。
// 真源是 tLDayStart 本身：这里不写 09:30、也不写 9 与 30，而是**调用那个函数**再把结果渲染成
// 载体要的写法（time.Format 的布局串是格式说明，不是数值）。
// 为什么载体需要它：移植腿没有 Go 的时钟，只有 ctx["hhmm"] 这一个读数，
// 它必须自己判"现在离开盘够不够冷静期"——起算点必须与引擎同一枚。
// English: renders tLDayStart (the very function the cooling gate uses) into the carrier's
// "HH:MM" spelling; no clock literal is written here.
func TDayStartClock() string { return tLDayStart(time.Time{}).Format("15:04") }
