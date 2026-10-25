// sizing_evo — §SIZING-EVO 动态仓位 + 因子加权自动回测研究回路
// （2026-10-25，docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.7）。
//
// 定位：把现「自动研究」（internal/research）的**方法论**同构平移到动态仓位合成器
// （signalctl.ComputeSizing）的全部可配权重与参数上：
//
//	L1 离线快筛（周度）：候选生成（坐标上升+网格+变异）→ 目标=scale 与前向收益的
//	                    横截面 IC/IR → 真 hold-out + MinIR/MinDays/Welch t 护栏；
//	L2 灰度实证（日频）：达标候选 → 灰度配置（不改生效配置）→ shadow 照算留痕 +
//	                    paper 观测 N 交易日 → 晋升（写账号 sizing 生效键）/ 回拒；
//	衰退降级（夜间链）：已生效配置逐日滚动 IR/胜率 → 连续 N 日衰退 → 自动回默认 + 告警。
//
// 三层执行模型（§2.7.0，owner 定调）：战法 ~ 因子加权 ~ 动态仓位 ~ 最终买卖执行，
// **三层正相关不是连乘**。本回路只覆盖"因子加权层 + 动态仓位层"两类进化对象；
// 战法层归现有 internal/research，本次不改不动。
//
// ── 回路独立铁律（架构测试锁定，arch_test.go） ────────────────────────────────
//
//  1. **零 import internal/research**：方法论同构、代码独立。IC/IR、灰度判据、
//     降级判据、护栏全部在本包内独立实现（复刻口径，不借函数）。
//  2. **不读战法库文件**：不读候选库/灰度库/applied_rules.json，不用战法回测的
//     IC/IR 作寻优目标；候选/灰度/晋升/降级全走本包自己的 JSONL 影子库。
//  3. **进化器只产候选，不直改生效配置**：晋升是唯一写入口，且走账号 sizing 生效键
//     （热加载通道），sizing.evo.enabled=false 一键冻结回默认。
//  4. **SignalQualityTable 表内权重不进搜索空间**（R6）：那已有自己的回路自留地；
//     本回路只进化合成器参数与因子 scale 映射（§2.7.1 三层搜索空间）。
//
// 与 signalctl 的关系：本包**允许** import internal/signalctl —— ComputeSizing 正是
// 被优化的对象，反事实重算（"历史信号在新配置下会拿到多少 scale"）必须复用它。
// 禁的只有 internal/research 与战法生命周期状态。
//
// English: the independent auto-backtest-research loop for the sizing synthesizer.
// Methodology mirrors internal/research; code, data and lifecycle are fully independent
// (architecture-tested). Importing signalctl is allowed and required — ComputeSizing is
// the very thing being optimized.
package sizing_evo
