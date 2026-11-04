package research

// derive_carrier.go —— 情绪相位阈值的**只读导出**。
//
// 为什么加这一处（2026-11-04）
//   QMT 内置策略与本地执行器要把同一套相位判定阈值烘进自己的参数文件。那份内置默认
//   本来就在本包（emotionPhaseDefaultCfg），但它是非导出的——于是外部进程只有两条路：
//   要么调这个导出读数，要么自己再抄一份数。第二条路正是本仓反复锤过的"同一个量两处各算一遍"，
//   而且抄的那份没有主人：改了内置默认，载体侧不会有任何东西响。
//
// 边界
//   这里只做**读出**：不填默认、不改区间、不加新常量。任何数字都留在
//   emotion_phase.go 那份真源里，本文件一个数字都没有。
//   English: read-only export of the built-in phase thresholds; no new numbers live here.

import "quant-trading-v2/internal/config"

// EmotionPhaseDefaultThresholds 返回内置默认阈值的副本。
// 返回副本而不是指针：调用方（离线导出工具）改了也不该动到进程内的那份默认。
func EmotionPhaseDefaultThresholds() config.EmotionConfig {
	return *emotionPhaseDefaultCfg()
}

// EmotionPhaseNames 返回六个相位的中文名（与 DetectEmotionPhaseV2 同一套阶段）。
// 载体侧的相位标签表由这条自证：标签拼错会在导出对账时被认出来，而不是静默少一相。
func EmotionPhaseNames() []string {
	out := make([]string, len(emotionPhases))
	copy(out, emotionPhases)
	return out
}
