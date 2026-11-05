package store

// derive_carrier.go —— 「开 T 腿资格」锚点来源清单的**只读导出**（§DERIVE-CARRIER-1105）。
//
// 为什么加这一处：QMT 内置策略与本地执行器把做 T 的判定链整段移植了过去（rules_t.py），
// 其中"锚点来源必须是可证那一档"这条前置在 Go 是 `RealPosition.AnchorTicket()` 里的一次
// 清单比较。方法在载体上没有对应物——它拿不到 store 包，只能拿一张烘进 params 的表。
// 以前这张表没有主人：载体侧要么按注释手抄两个字符串（抄对了也是第二本账，这里加一档
// 那边不会跟着动），要么自己猜（猜成把 cost_fallback 也放行＝裁决二终结过的缺陷从移植侧
// 绕回来）。现在判据与导出读的是同一个包级清单（real_positions.go 的 anchorTicketSources）。
//
// 边界：只读、不写盘、不改判据；返回一份新切片的拷贝，调用方改它不会动到进程内的判据。
// English: read-only export of the anchor-provenance list that AnchorTicket itself compares
// against, so the ported carrier never grows a second copy of that decision.

// TAnchorOkSources 返回"可开日内回转腿"的锚点来源档位清单。
// 真源：anchorTicketSources（RealPosition.AnchorTicket 判据用的就是它）。
func TAnchorOkSources() []string {
	return append([]string(nil), anchorTicketSources...)
}
