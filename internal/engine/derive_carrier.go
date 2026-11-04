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
