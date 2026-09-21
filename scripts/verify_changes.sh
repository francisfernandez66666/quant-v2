#!/usr/bin/env bash
# 改动全量验证脚本（含 2026-08-05 实时链路改动专项 + 2026-09-11 回测自动增强专项 + 2026-09-12 做空策略链路专项
#   + 2026-09-13 UAT 修复批/研究升级批/情绪面板 A/B/C 专项 + 2026-09-14 §DATA-OUTAGE 数据管道根治专项
#   + 2026-09-15 §ICP 备案合规 + §LIFECYCLE-WIRING 衰退降级接线专项 + 2026-09-17 §SIGNAL_CONTROLLER 信号控制器专项
#   + 2026-09-18 §UAT_FULLCHAIN 全链路修复批（费用腿/权限门控/死代码清理/CI-seed 守卫）专项
#   + 2026-09-18 §AUDIT20260918 全栈审计批（A1-A9）+ 生产实录 §PROD-T1/§PROD-LLM2 专项
#   + 2026-09-19 §ENH-0~5 增强批 A~E 专项 + §RFIX-1~5/§ENH-A/B 战法层修复增强批专项（见 18/18）
#   + 2026-09-20 §ENH-A 生产回填承载脚本静态守卫（见 19/19）+ §A7 版本漂移根治（部署脚本同步 web/dist，见 20/20）
#   + 2026-09-20 §FIX-5 UAT 自举脚本静态守卫（见 21/21）+ §FIX-20260920 今日修复批 FIX-1~7（见 22/22）
#   + 2026-09-20 §FIX-20260920 数据管道修复批（THS 单域熔断/f62 主站信任/板块列表镜像分页/EM-FFLOW 真值口径/THS-KLINE 5分钟/QUOTE-CHAIN 同花顺首选源，见 24/24）
#   + 2026-09-20 §A7-B/§A7-C 部署健壮性（产物指纹落盘 + 停服兜底 + 变量名吞噬，见 23/23）
#   + 2026-09-21 §CONSULT-UX 咨询页体验修复（见 25）+ §CB-TICKWINDOW/§CB-RUNSTART 盘前误熔根治（见 26）
#   + 2026-09-21 §SELLPOINT-UNIFY 卖出并轨统一裁决层 P1~P4 + §D1 护栏 + BUGFIX 缺陷1~4（见 27）
#   + 2026-09-22 §C1/§C1b 冻结账日期过滤 + 跨日陈旧买单无条件清扫（见 28）+ §C2 深破任意线型无条件升级（并入 27 锁⑩）+ §F1/§F12 实盘费用腿入盈亏/成本与成交额单口径（见 29）
#     + §H1/§H2 交割日期归一/卖出状态快照防别名（见 30）+ §H3 全局写端点收权 admin（见 31）+ §H4/§H5 卖单吞错/新鲜度主判据（见 32）+ §H6/§H7 SSE 续传/陈旧闭包（见 33）
#     + §H9/§M16 outbox 错位/网关 inflight 收割（见 34）+ §F2/§M4/§F5/§M5/§F3 回报接入段（见 35）+ §M14/§M15 同因熔断/夜链自愈（见 36）+ §H8/§F6 探针同源/构建指纹（见 37）
#     + 二波（2026-09-22 当日续）：§M1 golden 源契约单源（见 38）+ §M2/§M3 降级报成功族（见 39）+ §M6/§TZ/§REJECT 数据管道 py 批（见 40）+ §M8 推送三通道内聚/EXPVAR 收权（见 41）+ §M9/§M10/M11 快照与落盘批（见 42）+ §M7 部署清单收编（见 43）+ §M12/§M13 前端与移动壳一致性 + researchd 冒烟（见 44））
# ...
# §全链路 UAT 修复批（2026-09-18 §UAT_FULLCHAIN_VERIFY）专项（见 11/11）：
#   费用腿       ：成交回报 fee/stamp_tax 五路径透传（xt 回调/桥行/网关装配/mock/Go 落库），
#                  三方对账费用差腿首次可观测；旧格式回报缺省 0 保持字节兼容
#   权限门控     ：MsgCenter/Signals/Positions/LLMDebug 对成员隐藏 admin 守卫写入口（vitest + e2e 双锚）
#   占位可观测   ：dragon/double_bump/n_shape 的占位 Evaluate 返回 Level=stub（区别于 nodata）
#   死代码       ：internal/calendar 包、Agent.Scan 通用入口删除并设复活守卫
#   CI-seed      ：nightly 工作流 admin id 改读 auth.json（users 表引用设 grep 守卫）
#   桥防线       ：_record_seen 落盘失败 → _handle_cmd 拒执行（fail-closed，drill-3 补全）
#
# §买卖方向权威化（2026-09-18 §TRADE_SIDE）专项（见 12/14）：
#   现象         ：600580.SH 一笔真实手动卖出（800 股 @26.71）在成交流水里显示为「买入」。
#   桥侧         ：_deal_side 多字段投票（m_nDirection 0/48=买、1/49/50=卖；m_nOffsetFlag 48=买/50=卖；
#                  order_type 交叉否决；冲突不盲判、未命中组合留痕不抛异常）+ embed/xt 两路径同源
#   网关侧       ：本端派发过的单，dispatch.side 是方向唯一权威（旧 setdefault 从未生效——桥行恒带
#                  side）；未派发过的成交保持回报方向；派发项定位链 seq → 交易所委托号 → signal_id
#   纯净度       ：qmt_bridge_strategy.py 纯 ASCII 守卫（GBK 沙箱，中文注释同样乱码）
#
# §单日买入笔数改「已成交」口径（2026-09-18 §BUY_COUNT_FILLED）专项（见 13/14）：
#   口径         ：笔数闸从「orders 表已报笔数」改为「fills 表当日买入成交按委托去重」；
#                  金额闸同日升级为**动态冻结账模型**（§BUDGET_FREEZE_LEDGER）：
#                  占用 = 已成交（fills）+ 在途冻结（LocalBuyFrozen：报单冻结/成交扣除/
#                  撤单解冻）− 卖出回款（SumSellFilledAmountByDay，卖出即回血、钳 0 不放大预算）；
#                  闸3 近似资金 = 本金 − 持仓成本 − 冻结 + 今日已实现盈亏（成交成本已入持仓，
#                  不另扣成交额——修掉 held+filledAmt 双扣）；卖出方向永不设量闸
#   买入终点     ：唯一硬终点是闸1 今日已成交笔数达上限；预算/资金闸全部动态伸缩
#   附带效果     ：同一委托多次部分成交算 1 笔；QMT 客户端手工单（本地无 orders 行）计入额度
#   回归         ：三处锁旧口径的断言（gate/guards/engine）改写为钉新语义 + store 层边界用例
#                  + 冻结账端到端（TestGuardBudgetFreezeLedger 含卖出回血步）
#                  + 闸3 双扣修复与卖出回血（TestGuardCashDynamicSell）
#
# §LLM 热更新稳定性 + 地址规范化（2026-09-18 §LLM_HOTUPDATE / §LLM_BASEURL）专项（见 14/14）：
#   地址规范化   ：供应商 base URL（/v1、/v1beta、裸主机）自动补 /chat/completions；
#                  完整 endpoint 与自建网关非标准路径原样不动（不猜）
#   健康探测     ：ProbeConfig 逐把 key 并发发一次真实最小 chat 调用，一次验完鉴权+地址形态+模型名；
#                  429 算可用、仅 auth/model/quota 算「配置错」、超时夹逼 [15s,60s]
#   热更新语义   ：探测 → 切换 → 落库（落库失败只影响重启自愈、不回滚运行时）；
#                  拒绝必须有确凿证据（network/5xx/400 不算）；三入口共用 runLLMApplyFor；
#                  哨兵不落库也不成钥；force 不污染回滚点；探测端点只读
#   前端         ：拒绝时不得谎报「已热生效」，逐把展示探测结论
#
# §全栈审计批 + 生产实录修复（2026-09-18 §AUDIT20260918 / §PROD-T1 / §PROD-LLM2）专项（见 15/15）：
#   A1/A2        ：网关侧消费 strategy_type/max_order_amount（缺省字段显式策略、拒绝不耗幂等槽）；
#                  Go↔qmt-mock↔gateway 下单字段集 golden 契约文件锁死（TestOrderContractGolden）
#   A3/A4        ：SSE 慢客户端丢弃计数+越阈断连走补发；下单回报落库事务化（含乱序回报/用户隔离）
#   A5/A7        ：前端路由守卫等 /api/auth/me 服务端权威角色对账；/api/status 透传 build_commit +
#                  vite define 同指纹 + 版本漂移横幅（哨兵值静默）
#   PROD-T1      ：603468 当日买入即误推「持仓超期离场」根因三连修——ExecLog.EntryAt 零值守卫、
#                  real_positions.buy_date 透传建仓日、建议层 SellableQty T+1 硬闸（同源下单闸口径）
#   PROD-LLM2    ：「no response from LLM」黑盒修白——流式/非流式 content 全空时 reasoning_content
#                  兜底应答；仍为空则错误携带 finish_reason/usage/响应摘录（保留 no response 前缀）
#
# §信号控制器（2026-09-17 §SIGNAL_CONTROLLER）专项（见 10/10）：
#   组件层       ：internal/signalctl 全包——白名单（空白名单=内置四形态+库规则默认全集、动量/未知严格 opt-in）、
#                  个股/板块黑名单与影子模式、买入持续性确认窗（探针+双窗+两通道两账号隔离+消失重置）、
#                  非买入直通、批量 Evaluate 下标对齐、裁定留痕环（9 用例）+ risk.Gate 同源回归
#   引擎编排层  ：dispatchLive 单点收口——确认窗首探针受阻/满窗放行/消失重置、白名单外拦截标注、
#                  动量空白名单必拦且显式列名才交易（09-17 实盘误交易事故路径回归）
#   信号产生层  ：动量与其他战法同权恒产信号（交易裁决唯一归控制器白名单）
#   HTTP 契约层 ：GET/POST /api/paper/strategies（模拟盘战法开关）+ GET /api/signalctl/verdicts（裁定留痕）
#
# 用实盘数据快照（internal/e2e/testdata/fixtures.json / fixtures_600580.json）离线 mock 全部外部数据源，
# 专项验证改动：
#   1) LLM 超时配置   ：llm.Config.Timeout 默认 60s、自定义生效
#   2) D1 回退        ：mock LLM 对 D1 返回 500 → 3 次轮询重试失败 → 回退上一轮评分
#   3) N形门槛放开     ：D1>0 且总分≥60 → Valid（D2/D3/D4 仅贡献总分）
#   4) 咨询专业模式   ：注入真实实时行情（东财 quote/资金流/分钟 MACD），缺失严禁编造
#   5) HTTP 级全端点  ：/api/news、/api/signals change_pct、/api/signal-logs、/api/sector/hot 兜底、consult API
#   6) 政策反制/confrontation 落盘、ths 昨收推算、GetStockList 新浪→东财兜底、updateHotPool、resolveConflict、PE 预取
#
# §回测自动增强（2026-09-11 A0+A+B+C+D）专项（见 3/3）：
#   A0 配置管线   ：config.ValidateBacktest 区间/档位单调性 + FillDefaults；backtest_settings 读写；
#                  server GET/PUT /api/research/backtest-config + 三处入队注入 + runtask payload 回退链
#   A 动态滑点    ：paper_trades 实测校准中位数（战法级→全局→配置回退）+ 非对称买差 clamp
#   B 流动性约束  ：涨停封死/打开、跌停封死顺延、部分成交 fillRate、amount 千元单位自校
#   C Pareto      ：四维非支配前沿（vs 朴素参照）、推荐解（门槛全过取 Sharpe 最高）、payload 落库
#   D 前端        ：ParetoChart 渲染/交互 + BacktestConfigPanel 读写 + approveOptimization 推荐解请求体
#   回归保证      ：增强关闭（nil/Enabled=false）时旧行为逐字节一致（各包 _test 已含短路用例）
#
# §做空策略链路（2026-09-12 §SHORT 1-5）专项（见 4/4）：
#   四战法包       ：high_churn/break_down/leader_decay/good_news_fade + shortbase 共享输入（全用例跑）
#   接线           ：ScanShort 两层门控/ST 拦截/评分日志（TestScanShort*、TestBuildShortDataDerives）
#   自动卖出       ：SellAction→close、shortSellMarks 日幂等、止损级 advice（TestShortTacticCloseAdvices、
#                   TestAutoExecuteRealSellsShortTactic、TestPaperSellSignalsIncludeShortTactic）
#   融券做空簿     ：保证金/隔离/T+1/利息/止损/路由/e2e 权益连续（TestShort* paper 8 组）
#   全链路 e2e     ：真 runners→ScanShort→SellAction→paper 开空→关门静默（TestShortPipelineTactics）
#
# §市场风险因子（2026-09-12 §MARKET_RISK_GATE B1+B2+B3）专项（见 5/5）：
#   P0 数据源切换   ：涨停/跌停/炸板池 hithink 主源+东财永远兜底、涨跌家数真实弃权（GetBreadth 不编造
#                    1500/1500 假中性）、hithinkItemsToLimitUp 映射、双跑背离 opslog（TestRiskPool*、TestPool*）
#   P1 情绪广度纠偏 ：涨跌家数对涨停池口径单向纠偏（阈值未配/家数缺失=弃权；只降不升；反配兜底）
#                    （TestEmotionBreadthCorrection、TestEmotionBreadthReversedConfig）
#   P2 状态机接线   ：真实炸板率/上涨占比/指数 MA20·MA60 斜率灌入 MarketStateObserve + DetectEmotionPhaseV2
#                    （masLowSlope 口径 + 斜率日级缓存 NaN 弃权）
#   P3 风险档合成   ：情绪+市场状态+宏观三源→Red/Yellow/None + applyRiskTier 信号收紧（门槛/拦N形·动量/
#                    板块映射上浮）+ 总开关/情绪开关热回退（TestSynthesize*、TestApplyRiskTier*、TestComputeAndSet*）
#   P4/P5/P7        ：auto-buy 谨慎层(默认关)拒Red/缩Yellow、系统性风险持仓提醒(SellAction 不命中→绝不自动卖)、
#                    做空风险日置信加成（TestAutoCaution*、TestMarketRiskAlertsNotAuto、TestRiskTierShortBoost）
#
# §UAT 修复/研究升级/情绪面板（2026-09-13）专项（见 6/6）：
#   会话安全链 D1/D3/D7：PublicUser 剥凭据、Enabled 恒序列化、RevokeSession 自助吊销
#   部署自检 D5：verifyDeployment 四分支（禁用/缺 token/密钥池/nil auth）
#   情绪面板 A/B：历史端点日期归一化（YYYYMMDD→ISO）+ days 钳制 250 + 矩阵六相位/thin 纪律
#   研究升级 W6/W7：objective→ref_id 固定槽位（990~994/哈希 1000+）、min_triggers 按目标门槛
#   前端（见 vitest sentiment.test.jsx）：F45 Card actions 插槽、矩阵懒加载、hit_rate×100
#
# §数据管道停摆根治（2026-09-14 §DATA-OUTAGE）专项（见 7/7）：
#   market_risk_daily 历史回放：ths 池统计+日线广度装配回补行、引擎权威行跳过/--force 覆盖、
#   连板高度/炸板率百分转小数/无日线弃权、幂等落库（TestRiskBackfill* 3 组）
#   dataload 盘后保活：target 工作日回退、缺表安全弃权、三表全新鲜零调用短路、
#   日线落后触发补数且池未到位不判成（unittest 4 组）
#
# §备案合规 + 生命周期接线（2026-09-15 §ICP + §GAP-P1）专项（见 8/8）：
#   ICP 页脚       ：备案号常量（沪ICP备2026045551）+ 工信部外链 vitest；登录页/仪表盘两入口挂同一组件
#   衰退降级接线  ：PoolDailyStats 分池逐日聚合（strategy_type 池键修复）+ DemoteAppliedRules
#                  禁用落库/dry-run/无观测保守 keep（TestPoolDailyStats|TestDemoteAppliedRules 3 组）
#                  + stepTask lifecycle 映射与默认 Steps 含 lifecycle（TestLifecycleStepMapped）
#
# 用法:
#   ./scripts/verify_changes.sh                # 编译 + 全部专项（12 个历史专项 + 今日 3 个：方向权威化/笔数成交口径/LLM 热更新）
#   ./scripts/verify_changes.sh -full          # 再连相关全量单测 + QMT 网关 py 全量 + 前端 vitest 一起跑
#
# 说明：本机通常没有 pytest，脚本内 py_tests() 会自动退回标准库 unittest（CI 仍走 pytest）。
set -euo pipefail
cd "$(dirname "$0")/.."

# py_tests <目标文件或目录> [-k 过滤表达式]
#
# 运行 Python 测试。CI 装了 pytest，本机（macOS 开发机）通常没有，因此做一次能力探测后
# 退回标准库 unittest —— 否则脚本在本机会因「No module named pytest」而假失败，
# 让人误以为是被测代码坏了（二者现象一样、成因完全不同，最耗排查时间）。
py_tests() {
	local target="$1"
	local filter="${2:-}"
	if python3 -c 'import pytest' >/dev/null 2>&1; then
		if [ -n "$filter" ]; then
			python3 -m pytest "$target" -q -k "$filter"
		else
			python3 -m pytest "$target" -q
		fi
		return
	fi
	# 过滤串统一接受 pytest 的 "a or b" 写法：unittest 的 -k **不支持** or 表达式，
	# 每个 -k 是一个独立模式（pytest 的 '-k "a or b"' 在 unittest 下会静默匹配 0 个用例），
	# 因此在这里拆成多个 -k。$kstr 有意不加引号——模式都是脚本里写死的单词，依赖分词展开。
	local kstr=""
	for p in ${filter// or/ }; do kstr="$kstr -k $p"; done
	if [ -d "$target" ]; then
		# 目录形态：unittest 用 discover
		python3 -m unittest discover -s "$target" -p 'test_*.py' $kstr -v
	else
		# 文件形态：<dir>/tests/test_x.py → cd <dir> 后按模块 tests.test_x 运行
		# （unittest 的模块名相对 cwd 解析，直接把完整路径转模块名会导入失败）
		local pkg base
		pkg=$(basename "$(dirname "$target")")
		base=$(basename "$target" .py)
		( cd "$(dirname "$target")/.." && python3 -m unittest "$pkg.$base" $kstr -v )
	fi
}

echo "==> 1/8 编译检查..."
go build ./...
go vet ./internal/llm ./internal/combat_agent ./internal/engine ./internal/e2e ./internal/server ./internal/data ./internal/display ./cmd/quant \
	./internal/config ./internal/btreplay ./internal/store ./internal/scheduler ./internal/research ./cmd/research

echo "==> 2/8 实时链路改动专项 e2e（实盘快照 mock）..."
go test -count=1 -v ./internal/e2e/ \
	-run 'TestLLMTimeoutConfig|TestD1RetryQueueAcrossRuns|TestNShapeGateD1AndTotal|TestEndToEndFullPipeline|TestConsult|TestHTTP|TestAttachLiveBar' 2>&1 \
	| grep -E '^(=== RUN|--- (PASS|FAIL)|PASS|FAIL|ok)'

echo "==> 3/8 回测自动增强专项（A0+A+B+C+D，2026-09-11）..."
go test -count=1 -v ./internal/config ./internal/btreplay ./internal/store ./internal/server ./internal/scheduler ./cmd/research \
	-run 'TestValidateBacktest|TestFillDefaults|TestFillBacktestDefaults|TestBacktestSettingsRoundTrip|TestPaperSlippageCalib|TestBacktestConfigEndpoints|TestInjectBacktestPayload|TestPayloadBacktest|TestCostRoundTripPnlExCompat|TestSlippageTiers|TestSlippageBpsAsymmetric|TestFillRate|TestLimitBoardGating|TestCalibAudit|TestEntrySlipGating|TestUniformExit|TestFixAmountScale|TestAvgAmountWan|TestBuildSlipCtx|TestPareto|TestCapFront|TestRecommended|TestPointJSON|TestSaveSweepResultsParetoCarry' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 4/8 做空策略链路专项（§SHORT 2026-09-12：四战法/接线/自动卖出/融券/e2e）..."
go test -count=1 ./internal/strategies/high_churn/... ./internal/strategies/break_down/... ./internal/strategies/leader_decay/... ./internal/strategies/good_news_fade/... ./internal/strategies/shortbase/... 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/combat_agent/... ./internal/paper/... ./internal/engine/... ./internal/e2e/ \
	-run 'Short|BuildShort|StrategyDisplay|AutoExecuteRealSells|AutoExitReportSells|PaperSellSignals' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 5/8 市场风险因子专项（§MARKET_RISK_GATE 2026-09-12：P0 数据源 + P1 广度 + P2 状态机 + P3~P7 风险档）..."
go test -count=1 ./internal/data/ -run 'RiskPool|PoolLimit|PoolHelpers|Breadth|MasLowSlope|EmotionPhase|EmotionBreadth' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/config/ -run 'Data|Risk|Emotion|Macro|Scheduler|AutoCaution' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/research/ -run 'MarketState|StateTracker|Classify' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/combat_agent/ -run 'RiskTier|ApplyRiskTier|ComputeAndSet|Synthesize|MarketRisk|AutoCaution|MacroGate' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 6/8 今日修复专项（2026-09-13：UAT D1/D3/D5/D7 + 情绪面板 A/B + 研究升级 W6/W7）..."
go test -count=1 ./internal/auth/ ./cmd/quant/ -run 'TestPublicUserStripsSessionCredentials|TestEnabledSerializesWhenFalse|TestRevokeSessionSelfLogout|TestVerifyDeployment' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/store/ -run 'TestEmotionStrategyMatrixBuckets|TestEmotionMatrixFallbackPhase|TestEmotionMatrixBadJSONSkipped' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ -run 'TestEmotionHistoryDateNormalize|TestEmotionStrategyMatrixShape|TestOptRefIDForSlots' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/btreplay/ -run 'TestMinTriggersForObjDefaults' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 7/8 数据管道停摆根治专项（§DATA-OUTAGE 2026-09-14：market_risk_daily 历史回放 + dataload 盘后保活）..."
go test -count=1 ./cmd/research/ -run 'TestRiskBackfill' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
py_tests qmt_gateway/tests/test_dataload_keepalive.py 2>&1 \
	| grep -E "passed|failed|error|Ran [0-9]+ test|OK"

echo "==> 8/8 备案合规 + 生命周期接线专项（2026-09-15 §ICP + §GAP-P1）..."
# ICP 备案号合规文案守护：常量改动即失败（管局备案文案，变更需先核对备案回执）
grep -q '沪ICP备2026045551' web/src/components/IcpFooter.jsx && grep -q 'beian.miit.gov.cn' web/src/components/IcpFooter.jsx \
	&& echo "ok - icp 备案常量在位" || { echo "FAIL - icp 备案常量缺失"; exit 1; }
( cd web && npm test -- icp_footer ) 2>&1 | grep -E 'Test Files|passed|failed'
go test -count=1 ./internal/research/ -run 'TestPoolDailyStats|TestDemoteAppliedRules' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/scheduler/ -run 'TestLifecycleStepMapped' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 9/9 加固件1/2 专项（2026-09-15 §HARDENING：ntfy 通道 + 备份/监控脚本在位）..."
go vet ./internal/notify ./cmd/quant
go test -count=1 ./internal/notify/ -run 'TestNtfy|TestPushGatewayDualDispatch|TestPushGatewayOnlyOneChannel' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
# 备份链路三件套在位：广州快照 ps1+py、Mac 拉取器、launchd 清单
[ -f deploy/qmt-win/backup_snapshot.ps1 ] && [ -f deploy/qmt-win/backup_snap.py ] \
	&& [ -f deploy/mac/restic_pull_backup.sh ] && [ -f deploy/mac/com.quant.backup.plist ] \
	&& echo "ok - 备份链路脚本在位" || { echo "FAIL - 备份链路脚本缺失"; exit 1; }
# Kuma 无头初始化脚本在位（监控重建的最小物料）
[ -f deploy/mac/kuma_seed.js ] && echo "ok - kuma_seed.js 在位" || { echo "FAIL - kuma_seed.js 缺失"; exit 1; }

echo "==> 10/10 信号控制器专项（2026-09-17 §SIGNAL_CONTROLLER：动量误交易根治 + 白名单/黑名单/持续性单点）..."
# A 组件层：白名单（空白名单=内置四形态+库规则全集、动量/未知严格 opt-in、矛盾语义回归）、
#           个股/板块黑名单与影子模式、买入持续性确认窗（探针/双窗/双通道双账号隔离/消失重置）、
#           非买入直通、批量 Evaluate 对齐与清理、裁定留痕环（internal/signalctl 全包 9 用例）
# B 引擎编排层：dispatchLive 收口——确认窗首探针受阻/满窗放行/消失重置（TestDispatchLiveBuyConfirmWindow、
#           TestDispatchLivePruneOnAbsence）；白名单外不下单且标注拦截、动量空白名单必拦/显式列名
#           才交易（TestDispatchLiveSkipsWhitelist——09-17 事故路径回归）
# C 信号产生层：动量与其他战法同权恒产信号、准入裁决归控制器（TestScorePoolMomentumAlwaysEmitted）
# D HTTP 契约层：模拟盘战法开关端点读写/未知战法 400/裁定审计端点形状（TestPaperStrategiesEndpoints）
# E 黑名单口径  ：gate 与控制器同源（checkWhitelist 委托 signalctl.AdmitStrategy，risk 全包回归）
go test -count=1 ./internal/signalctl/ 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/engine/ ./internal/combat_agent/ \
	-run 'TestDispatchLive|TestScorePoolMomentumAlwaysEmitted|TestScorePoolMomentumNoTradePreOpen|TestScorePoolMomentumBelowThreshold' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ ./internal/risk/ ./internal/config/ \
	-run 'TestPaperStrategiesEndpoints|TestGateWhitelistAndMaxPositions|TestGateSTAndBlacklist' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 11/14 全链路 UAT 修复批专项（2026-09-18 §UAT_FULLCHAIN_VERIFY：费用腿 + 权限门控 + 死代码 + CI-seed 守卫）..."
# A 费用腿（P2-FEE）：Go 回报→ApplyRealFill 落 fills.fee（旧格式缺省 0 兼容）、
#   mock /settlement 费用腿非零、网关 store 入库/输出、handler 多字段名探测、桥 fail-closed
go test -count=1 ./internal/server/ ./cmd/qmt-mock/ -run 'TestHandleQMTReportTradeFeeLeg|TestMockSettlementEndpoint' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
py_tests qmt_gateway/tests/test_gateway.py 'fee or settlement_endpoint or trade_push' 2>&1 \
	| grep -E "passed|failed|error|Ran [0-9]+ test|OK"
py_tests qmt_gateway/tests/test_bridge_strategy_adapter.py 'record_seen' 2>&1 | grep -E "passed|failed|error|Ran [0-9]+ test|OK"
# B 占位可观测（P2-STUB）：dragon/double_bump/n_shape 占位 Evaluate 必返回 Level=stub
go test -count=1 ./internal/strategies/dragon/ ./internal/strategies/double_bump/ ./internal/strategies/n_shape/ \
	-run 'TestEvaluateStubLevel|TestEvaluatePlaceholder' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C 前端权限门控（P1-PERM1）：成员隐藏 admin 守卫写入口（MsgCenter 删除/清空/模拟卖出）
( cd web && npm test -- perm_gate ) 2>&1 | grep -E 'Test Files|passed|failed'
# D CI-seed 守卫（P1-CI1）：nightly 禁止引用已废弃的 trading.db users 表（auth 已迁 auth.json）
#   只查真实代码行——注释里引用旧 SQL 文案作说明用途，不算违规（先剔除 # 注释行再匹配）。
#   注意用 POSIX 字符类 [[:space:]] 而非 \s：\s 不是 POSIX ERE，BSD/toybox grep 不支持，
#   会导致缩进注释剔除失败 → 注释里的旧 SQL 文案被误判成真实引用（2026-09-18 实测踩坑）。
grep -vE '^[[:space:]]*#' .github/workflows/nightly-e2e.yml | grep -q 'FROM users' && { echo "FAIL - nightly seed 仍引用 users 表"; exit 1; } || true
grep -q 'auth.json' .github/workflows/nightly-e2e.yml && echo "ok - nightly seed 读 auth.json" || { echo "FAIL - nightly seed 未接 auth.json"; exit 1; }
# E 死代码在位守卫（P2-BE-DEAD）：calendar 包与 Agent.Scan 通用入口已删除，防止半成品复活
[ ! -d internal/calendar ] && echo "ok - internal/calendar 已移除（真实现在 data/trade_time.go）" || { echo "FAIL - calendar 包复活"; exit 1; }
grep -q 'func (a \*Agent) Scan(' internal/combat_agent/agent.go && { echo "FAIL - 无调用方的 Agent.Scan 通用入口复活"; exit 1; } || true

echo "==> 12/14 买卖方向权威化专项（2026-09-18 §TRADE_SIDE：派发项方向唯一权威 + 桥侧多字段投票）..."
# A 桥侧方向解析（qmt_gateway/tests/test_deal_direction.py）：
#   m_nDirection 双枚举空间（0/48=买、1/49/50=卖，柜台既有 offset 口径也有 ASCII '0'/'1' 口径）、
#   m_nOffsetFlag 48=买/50=卖（08-31 现金流出实证口径，49 从未被真实卖出验证过）、
#   order_type 交叉否决、冲突不盲判落到下一权威、未命中组合留痕且不抛异常、描述串保持纯 ASCII
py_tests qmt_gateway/tests/test_deal_direction.py
# B 网关侧方向权威化（qmt_gateway/tests/test_file_bridge.py）：
#   本端派发过的单 → dispatch.side 覆盖桥的误判方向（最恶劣形态：归因对、方向反）；
#   未派发过的成交（客户端手工单/对账来源）→ 无权威方向可依，回报方向原样保留
py_tests qmt_gateway/tests/test_file_bridge.py 'dispatch or unattributed'
# C 桥脚本纯 ASCII 守卫：该脚本在 GBK 沙箱执行，任何非 ASCII 字节（含中文注释）都会乱码
python3 - <<'PY'
b = open('qmt_gateway/qmt_bridge_strategy.py', 'rb').read()
n = sum(1 for c in b if c > 127)
print('ok - qmt_bridge_strategy.py 非 ASCII 字节 %d' % n if n == 0 else
      'FAIL - qmt_bridge_strategy.py 出现非 ASCII 字节 %d（GBK 沙箱会乱码）' % n)
raise SystemExit(0 if n == 0 else 1)
PY

echo "==> 13/14 单日买入笔数「已成交」口径 + 动态冻结账专项（2026-09-18 §BUY_COUNT_FILLED / §BUDGET_FREEZE_LEDGER）..."
# 口径分工（动态冻结账模型，勿混成一个）：
#   笔数闸 → fills 表当日买入成交，按委托去重（order_id → 券商交割流水号 → 行 ID 依次兜底）；
#            **买入唯一硬终点**：今日已成交笔数达上限
#   金额闸 → 占用 = 已成交金额（SumBuyFilledAmountByDay）+ 在途冻结（LocalBuyFrozen 状态派生）
#            − 卖出回款（SumSellFilledAmountByDay，钳 0）——撤单释放、成交扣除、卖出回血；
#            闸3 = 本金 − 持仓成本 − 冻结 + 今日已实现盈亏（成交成本已入持仓，不另扣成交额）
# A store 层计数边界：部分成交去重 / 卖出不计 / 非当日不计 / 他账号不计 / 无委托号不合并
go test -count=1 ./internal/store/ -run 'TestCountBuyFilled' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# A2 store 层冻结账金额聚合：买入占用半边 + 卖出回款半边（旧数据 amount=0 回落 price×qty）
go test -count=1 ./internal/store/ -run 'TestSum.*FilledAmountByDay' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# B 闸口层：成交笔数达上限才拦（旧口径是报单即占额度，被废单会锁死当天买入权）
go test -count=1 ./internal/risk/ -run 'TestGateBuyDiscipline' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C 交易层：达上限后拒绝新买入、卖出不受限
go test -count=1 ./internal/trading/ -run 'TestGuardDailyBuysCap' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C2 交易层动态冻结账：报单冻结 → 撤单解冻 → 成交扣除 → 卖出回款释放预算；
#    闸3 不双扣成交成本、随卖出回血
go test -count=1 ./internal/trading/ -run 'TestGuardBudgetFreezeLedger|TestGuardLocalFrozen|TestGuardCashDynamicSell' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# D 引擎层：auto 下单路径同口径（0 成交时不拦，成交满额才拦）
go test -count=1 ./internal/engine/ -run 'TestAutoPlaceDailyCap' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 14/14 LLM 热更新稳定性 + 地址规范化专项（2026-09-18 §LLM_HOTUPDATE / §LLM_BASEURL）..."
# A 地址规范化（internal/llm）：供应商 base URL（/v1、/v1beta、裸主机、带尾斜杠）自动补
#   /chat/completions；已是完整 endpoint 或自建网关非标准路径**原样不动**（不猜）；
#   端到端断言真实请求路径，防止只改了字符串却仍打到 base 路径（供应商 404/405）
go test -count=1 ./internal/llm/ -run 'TestProviderBaseURLIsNormalized|TestChatHitsChatCompletionsPath' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
# B 候选配置健康探测：逐把 key 真实最小调用；429 算可用（证明鉴权已过）、
#   仅 auth/model/quota 算「配置错」、超时夹逼 [15s,60s]、max_tokens 过小自动放宽、不泄漏密钥
go test -count=1 ./internal/llm/ -run 'TestProbe' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C 热更新语义（internal/server）：探测 → 切换 → 落库；拒绝时运行时与磁盘**都不动**；
#   network/5xx/400 不构成拒绝证据（采用 + 标记未验证）；三入口共用一份实现；
#   全哨兵且无原值 → 400 且不重建；force 不污染回滚点；探测/回滚端点只读语义
go test -count=1 ./internal/server/ -run 'TestHotUpdate|TestSetLLMConfig|TestAdminSetLLM' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'
# D 前端不得谎报（web/src/__tests__/llm_hotupdate_ui.test.jsx）：拒绝时不得出现「已热生效」，
#   逐把展示探测结论；只有 applied 才更新基线与「已配置」状态
( cd web && npm test -- llm_hotupdate_ui ) 2>&1 | grep -E 'Test Files|passed|failed'

echo "==> 15/17 全栈审计批 + 生产实录修复（2026-09-18 §AUDIT20260918 A1-A9 / §PROD-T1 / §PROD-LLM2）..."
# A1/A2 下单契约：Go↔qmt-mock↔gateway 字段集 golden 文件锁死；网关侧消费
#   strategy_type/max_order_amount（缺字段显式策略），拒绝不消耗幂等槽
go test -count=1 ./internal/trading/ -run 'TestOrderContractGolden' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
py_tests qmt_gateway/tests/test_order_gates.py 2>&1 | grep -E "passed|failed|error|Ran [0-9]+ test|OK"
# A3 SSE 慢客户端：越阈断连淘汰 + 丢弃计数排空归零
go test -count=1 ./internal/server/ -run 'TestSSEEvictsStalledClient|TestSSEDropCounterResetsOnDrain' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# A4 下单主链事务化：回报落库 UPDATE+INSERT 同事务、乱序回报、用户隔离
go test -count=1 ./internal/store/ -run 'TestApplyOrderReportTx' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# A5 前端强对账门：/api/auth/me 回读前不放行 admin 壳；错误状态码透出
( cd web && npm test -- a5_auth_reconcile ) 2>&1 | grep -E 'Test Files|passed|failed'
# A7 版本可见性：/api/status 透传 build_commit（未注入=空串）+ 前端漂移告警纯函数（哨兵静默）
go test -count=1 ./internal/server/ -run 'TestStatusBuildCommitField' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
( cd web && npm test -- utils.test ) 2>&1 | grep -E 'Test Files|passed|failed'
# PROD-T1 当日买入误报超期 + 建议层 T+1 闸：EntryAt 零值守卫、buy_date 透传、
#   卖出五路只喂可卖持仓（缺数据 fail-open）；真实远古建仓仍判超期（防过度抑制）
go test -count=1 ./internal/combat_agent/ -run 'TestBuildExitContextZeroEntryAtIsEmpty|TestGenericTrailingExitStillTimeoutsForOldEntry' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/trading/ -run 'TestExecLogsFromRealEntryDate|TestAdviseT1LockedSkipsSellSide' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/store/ -run 'TestRealPositionsBuyDateRoundTrip' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# PROD-LLM2 空响应诊断：reasoning_content 兜底（流式/非流式）、错误携带
#   finish_reason/usage/响应摘录且保留 "no response from LLM" 前缀（§FIX-0921 回落判定）
go test -count=1 ./internal/llm/ -run 'TestStreamChatReasoningOnlyFallback|TestStreamChatEmptyCarriesEvidence|TestNonStreamEmptyChoicesEvidence|TestNonStreamReasoningFallback|TestNonStreamEmptyContentEvidence' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'

if [ "${1:-}" = "-full" ]; then
	echo ""
	echo "==> 附加：QMT 网关 Python 全量单测（文件桥/保活/降级/清仓护栏/成交方向解析）..."
	py_tests qmt_gateway/tests/
	echo ""
	echo "==> 附加：实时链路相关全量单测..."
	go test -count=1 ./internal/combat_agent/... ./internal/engine/... ./internal/llm/... ./internal/strategies/... ./internal/paper/... \
		./internal/data/... ./internal/display/... ./internal/newsagent/... ./internal/e2e/ ./internal/server/ ./cmd/quant/...
	echo ""
	echo "==> 附加：回测增强全量单测（含回归短路）..."
	go test -count=1 ./internal/config/... ./internal/btreplay/... ./internal/store/... ./internal/scheduler/... ./cmd/research/...
	echo ""
	echo "==> 附加：前端回测增强 + 组件测试（vitest run）..."
	( cd web && npm test )
fi

echo "==> 16/17 §ENH-0~4 增强批 A~D 专项（日历缓存 / 咨询检索增强 / 资金流第二源 / 事件因子）..."
# A 交易日历落盘缓存（§ENH-0）：写读回环、空集合与"无文件"分离、坏文件分流、QUANT_DATA_DIR 路径口径
go test -count=1 ./internal/data/ -run 'TestTradingCalendarCacheRoundTrip|TestLoadTradingCalendarCacheAbsentAndCorrupt|TestCalendarCacheFilePathUsesDataDirEnv' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# B 历史事件归档 + 咨询检索增强（§ENH-3）：历史同类事件注入 ⟦DATA⟧、无归档文件静默降级（e2e 全链路）
go test -count=1 ./internal/e2e/ -run 'TestConsultHistoryEventsInjected|TestConsultNoHistoryFileSilent' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C 同花顺资金流第二源（§ENH-2）：capital-flow 装配（单位=元）、null 缺数拒拼凑、东财失败回退合并错误、sina/腾讯富集
go test -count=1 ./internal/data/ -run 'TestHithinkStockMoneyFlow|TestGetStockMoneyFlowFallbackToHithink|TestEnrichFlowFromHithinkOnSina' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# D 事件因子（§ENH-4）：news_score 聚合口径（同日取 max|score|、板块互含匹配、宏观排除）+ 面板对齐 NaN 语义
go test -count=1 ./internal/research/ -run 'TestEventFactor' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 17/17 §ENH-5 批E QMT Level-1 行情 feed 专项（qmt_feed 合并注入 + /quotes 契约 + 交易链路隔离）..."
# A Go feed：命中才注入/共享指针复制/缺失字段保留/超龄与停牌丢弃（-race 锁竞态）
go test -count=1 -race ./internal/data/ -run 'TestQMTFeed' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# B 客户端契约：codes 带后缀出网、Bearer 携带、响应 key 归一裸码、非 200 透传
go test -count=1 -race ./internal/trading/ -run 'TestQMTClientQuotes' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C mock /quotes 契约（nightly 的 L1 数据源）+ feed_connected 观察字段
go test -count=1 ./cmd/qmt-mock/ -run 'TestMockQuotesEndpoint|TestMockAuthRequired' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# D Python feed：单元降级 + /quotes 鉴权/400/空ticks + broker 断连行情仍 200（隔离铁律回归锁）
py_tests qmt_gateway/tests/test_quote_feed.py '' 2>&1 | grep -E "passed|failed|error|Ran [0-9]+ test|OK"

echo "==> 18/18 §RFIX-1~5 + §ENH-A/B 战法层修复增强批专项（2026-09-19：MACD逐股/方向拟合/阈值闭环/口径治理/崩溃fail-fast/涨停微结构/DSR+PBO）..."
# RFIX-1 btreplay n_shape MACD 逐股重算：跨股污染修复 + curIdx 越界钳位退化（生产 #264 panic 实录）
go test -count=1 ./internal/btreplay/ -run 'TestBacktestStockRefreshesMacdPerStock|TestNShapeTriggerClampsOutOfRange|TestNShapeRunTwoStocksNoPanic' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# RFIX-2 样本内方向拟合：负制度翻转收割（两内核）、防未来函数（IS正OOS负仍拒）、裁决纯函数
go test -count=1 ./internal/research/ -run 'TestDirFitFlipsNegativeRegime|TestDirFitNoLookahead|TestFitDirsByInSampleSign|TestWindowedDirFitFlipsNegativeRegime' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# RFIX-3 阈值闭环：预期触发率估算（runner 分位口径/无未来函数）+ lifecycle 零观测反静默 + apply 守卫 warning + reason 特征 token
go test -count=1 ./internal/research/ -run 'TestTriggerRate|TestFactorTrigEst|TestDemoteAppliedRulesZeroObsAlert' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./cmd/research/ -run 'TestFactorTrigNote' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ -run 'TestThresholdOverrideWarning' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# RFIX-4 口径治理：寻优 pending 30 天过期状态机 + backtest_jobs 僵尸行启动恢复（MarkRunningInterrupted 复活）
go test -count=1 ./internal/store/ -run 'TestExpireStalePendingOptimizations|TestMarkRunningInterruptedRevived' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# RFIX-5 确定性崩溃 fail-fast：panic/fatal 特征提取（含生产 #264 形态回归）
go test -count=1 ./internal/scheduler/ -run 'TestCrashMarkerLine' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# ENH-A 涨停微结构：三池→StockSeries 四列装配 + CatLimit 因子族事件日掩码 + 注册表 9 大类
go test -count=1 ./internal/research/ -run 'TestAssembleLimitMicroColumns|TestLimitMicroFactorsOnPanel' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/factor/ -run 'TestRegistry' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# ENH-B 多重检验：DeflatedIR 公式数值/白噪声海选分层判据/真信号不误杀 + PBO-lite 分块 + 内核字段产出
go test -count=1 ./internal/research/ -run 'TestDeflatedIR|TestPBOSignConsistency|TestDiscoveryRobustnessFields' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# 前端：寻优列表 expired 默认过滤契约用例登记于 web/e2e/uat_full.spec.mjs（RW-1/RW-2，随 E2E 全量跑）

echo "==> 19/19 §ENH-A 生产回填承载脚本静态守卫（2026-09-20 回填实录：PS5.1 无控制台吞 stderr 首跑失败教训）..."
# run_ths_backfill.ps1 四条铁律回归锁：①恰好一个 UTF-8 BOM（双 BOM 令 PS 报「?# 不是 cmdlet」）；
# ②输出必须走 cmd /c 重定向（& exe 2>&1 管道在 ErrorActionPreference=Stop 下把原生 stderr 转
#    NativeCommandError 静默终止——09-20 首跑退出码1日志0字节实录）；③密钥只从机器注册表读、
# 脚本内不得内嵌任何 key 字面量；④退出码显式上抛（计划任务 LastTaskResult 可信）。
rb=deploy/qmt-win/run_ths_backfill.ps1
python3 - "$rb" <<'PY' || { echo "--- FAIL: $rb BOM 检查"; exit 1; }
import sys
d = open(sys.argv[1], 'rb').read()
boms = 0
while d.startswith(b'\xef\xbb\xbf'):
    boms += 1
    d = d[3:]
assert boms == 1 and b'\r\n' in d and b'\n\n' not in d.replace(b'\r\n', b''), 'BOM/CRLF 不合规'
PY
grep -q 'cmd /c' "$rb" || { echo "--- FAIL: $rb 缺 cmd /c 重定向承载"; exit 1; }
grep -q "GetEnvironmentVariable('HITHINK_FINANCE_API_KEY', 'Machine')" "$rb" || { echo "--- FAIL: $rb 密钥未走机器注册表"; exit 1; }
! grep -qE 'sk-[A-Za-z0-9]{8,}' "$rb" || { echo "--- FAIL: $rb 疑似内嵌密钥字面量"; exit 1; }
grep -q 'exit \$LASTEXITCODE' "$rb" || { echo "--- FAIL: $rb 未上抛退出码"; exit 1; }
echo "ok"

echo "==> 20/20 §A7 版本漂移根治专项（2026-09-20：部署脚本必须同步前端 web/dist）..."
# 根因：旧版 deploy_guangzhou.sh 只同步二进制/gateway/pydata，从不传 web/dist，
#       云端 Caddy 前端冻结在旧 buildCommit，浏览器端误报「前端与服务器版本不一致」。
#       静态守卫：脚本必须含 web/dist 同步段（漏传即失败，防回归）。
dg=scripts/deploy_guangzhou.sh
grep -q 'web/dist' "$dg" && echo "ok - $dg 含 web/dist 同步段（版本漂移回归锁）" \
  || { echo "--- FAIL: $dg 未同步前端 web/dist（§A7 版本漂移根因）"; exit 1; }

echo "==> 21/21 §FIX-5 UAT 自举脚本静态守卫（2026-09-20：全栈 UAT 必须可从本地一键复跑）..."
# 根因：mock+engine+前端+seed 的启动流程此前只内联在 .github/workflows/nightly-e2e.yml，
#       本机无处可跑——只能照抄 YAML，漏 seed 情绪/持仓即用例 skip 或假红。
#       守卫：脚本存在 + 可执行 + 语法通过 + 关键 seed 步齐备（退化成空脚本即失败）。
ub=scripts/uat_bootstrap.sh
[ -f "$ub" ] || { echo "--- FAIL: 缺 ${ub}（UAT 不可一键复跑）"; exit 1; }
[ -x "$ub" ] || { echo "--- FAIL: $ub 不可执行（需 chmod +x）"; exit 1; }
bash -n "$ub" || { echo "--- FAIL: $ub 语法错误"; exit 1; }
grep -q 'api/paper/buy' "$ub" || { echo "--- FAIL: $ub 缺模拟盘持仓 seed（卖出分支会 skip）"; exit 1; }
grep -q 'market_risk_daily' "$ub" || { echo "--- FAIL: $ub 缺情绪日线 seed"; exit 1; }
grep -q 'api/tenants/t_default' "$ub" || { echo "--- FAIL: $ub 缺租户配额上调（429 假红根因）"; exit 1; }
echo "ok - $ub 自举流程完整（构建 + 起栈 + seed + 配额）"

echo "==> 22/22 §FIX-20260920 今日修复批（候选 nil panic / QMT 待生效可观测性 / 租户 429 头 / panic trace-id / vitest 并发超时 / sqlite 关闭）..."
# ① FIX-1 候选 store 层哨兵错误：CandidateByID 的 no-rows 分支必须返回 ErrCandidateNotFound。
#    旧实现 return nil,nil → 只判 err 的调用方解引用 panic（server/research.go、cmd/research/auto.go、
#    internal/btreplay/replay.go 三处）。注：LatestCandidate 的 (nil,nil) 是既有显式契约（调用方已全判 nil），不在此列。
sc=internal/store/candidates.go
grep -q 'ErrCandidateNotFound' "$sc" || { echo "--- FAIL: $sc 缺 ErrCandidateNotFound（FIX-1 回归）"; exit 1; }
grep -q 'return nil, ErrCandidateNotFound' "$sc" || { echo "--- FAIL: $sc CandidateByID 未返回哨兵错误"; exit 1; }
# ② FIX-2 QMT 待生效可观测性：Snapshot 暴露 PendingEnabled；诊断日志区分 ctrlEnabled
grep -q 'PendingEnabled' internal/trading/controller.go || { echo "--- FAIL: controller.go 缺 PendingEnabled（FIX-2）"; exit 1; }
grep -q 'ctrlEnabled' internal/server/qmt.go || { echo "--- FAIL: qmt.go 诊断未含 ctrlEnabled（FIX-2）"; exit 1; }
# ③ FIX-3 租户级 429 必须带 Retry-After（统一走 rejectRateLimit）
grep -q 'rejectRateLimit(w, time.Minute)' internal/server/server.go || { echo "--- FAIL: 租户限流未走 rejectRateLimit（FIX-3）"; exit 1; }
# ③b FIX-3 收口：429 只允许出现在统一出口 rejectRateLimit 内部（全文件恰好 1 处 writeError(w, 429）
#      ——登录/setup/租户三档若各自裸吐 429，就会缺 Retry-After 头（2026-09-20 实测残留两处，已收口）
[ "$(grep -c 'writeError(w, 429' internal/server/server.go)" = "1" ] || { echo "--- FAIL: 存在绕过 rejectRateLimit 的裸 429（FIX-3）"; exit 1; }
# ④ FIX-4 panic 可追溯：genTraceID + X-Trace-Id 回写
grep -q 'func genTraceID' internal/server/server.go || { echo "--- FAIL: 缺 genTraceID（FIX-4）"; exit 1; }
grep -q 'X-Trace-Id' internal/server/server.go || { echo "--- FAIL: recover 未回写 X-Trace-Id（FIX-4）"; exit 1; }
# ⑤ FIX-6 vitest 并发超时收敛：Testing Library asyncUtilTimeout 放宽
grep -q 'asyncUtilTimeout' web/src/__tests__/setup.js || { echo "--- FAIL: setup.js 未放宽 asyncUtilTimeout（FIX-6）"; exit 1; }
# ⑥ FIX-7 python sqlite 关闭：reset_test_row.py 用 contextlib.closing
grep -q 'closing(' scripts/reset_test_row.py || { echo "--- FAIL: reset_test_row.py 未用 closing（FIX-7）"; exit 1; }
echo "ok - 静态守卫 10/10 通过"
# 动态：FIX-1 回归用例——候选不存在须 404 且不 panic→500
go test -count=1 ./internal/store/ -run 'TestCandidateByID_NotFoundReturnsErr' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ -run 'TestResearchApproveMissingCandidate' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 23/23 §A7-B/§A7-C 部署健壮性专项（2026-09-20：前端产物指纹 + 停服兜底 + shell 变量名吞噬）..."
# ① §A7-B 产物指纹必须落盘：只有 dist/BUILD_COMMIT 才能让部署脚本在**上传前**判定
#    "这个前端是哪一版"（注入 JS 的那份被压缩混淆，只能猜）。缺它则「先 build 后 commit」
#    导致的漂移无法被自动拦住（2026-09-20 两次踩中，用户端报版本不一致横幅）。
grep -q 'buildCommitMarker' web/vite.config.js || { echo "--- FAIL: vite.config.js 缺 buildCommitMarker（§A7-B）"; exit 1; }
grep -q 'BUILD_COMMIT' web/vite.config.js || { echo "--- FAIL: vite.config.js 未落盘 dist/BUILD_COMMIT（§A7-B）"; exit 1; }
grep -q 'BUILD_COMMIT' "$dg" || { echo "--- FAIL: $dg 未校验 dist/BUILD_COMMIT（§A7-B）"; exit 1; }
grep -q 'BUILD_COMMIT' scripts/build_apk.sh || { echo "--- FAIL: build_apk.sh 未校验内嵌指纹（§A7-B）"; exit 1; }
grep -q 'web:fingerprint' scripts/verify_deploy_guangzhou.sh || { echo "--- FAIL: 验证脚本缺前端指纹探针 4b（§A7-B）"; exit 1; }
# ② §A7-C 停服必须可逆：[2/5] 的 net stop 之后只有 [4/5] 会拉起，而 `-s` 跳过 [4/5]、
#    任何提前退出在 set -e 下直接结束 —— 两处都会把线上引擎留在停机态（2026-09-20 实录，
#    引擎停了约 3 分钟且无提示）。守卫锁死「EXIT 兜底 + -s 分支显式拉起」两个出口。
grep -q 'trap restore_services_on_exit EXIT' "$dg" || { echo "--- FAIL: $dg 缺停服 EXIT 兜底（§A7-C）"; exit 1; }
grep -q '^start_engine_services()' "$dg" || { echo "--- FAIL: $dg 缺 start_engine_services（§A7-C）"; exit 1; }
grep -q 'SERVICES_STOPPED=1' "$dg" || { echo "--- FAIL: $dg 未在停服处置位 SERVICES_STOPPED（§A7-C）"; exit 1; }
# ③ shell 变量名吞噬：`$VAR` 紧跟全角字符（如 `$ds）`）时 bash 会把多字节并入变量名，
#    在 set -u 下报 unbound variable 直接中止（2026-09-20 实测：部署在「指纹校验通过」那行
#    崩溃，前端因此漏传）。全仓 12 处，已全部改为 ${VAR}；此守卫防新增写法回归。
#    注：必须用 python 扫——grep 无法可靠匹配 Unicode 区间（toybox grep 对 -P/码位静默失灵）。
python3 - <<'PY' || { echo "--- FAIL: shell 脚本存在「变量名后紧跟非 ASCII 字符」写法（bash 会吞进变量名）"; exit 1; }
import pathlib, re, sys
pat = re.compile(r'\$[A-Za-z_][A-Za-z0-9_]*(?=[^\x00-\x7F])')
bad = []
for p in sorted(pathlib.Path('scripts').rglob('*.sh')):
    for i, line in enumerate(p.read_text(encoding='utf-8').splitlines(), 1):
        if line.lstrip().startswith('#'):
            continue
        for m in pat.finditer(line):
            bad.append("%s:%d: %s   <-- 应写成 ${%s}" % (p, i, m.group(0), m.group(0)[1:]))
if bad:
    print("\n".join(bad))
    sys.exit(1)
PY
echo "ok - 静态守卫 9/9 通过（产物指纹 5 + 停服兜底 3 + 变量名吞噬全仓扫描 1）"

echo "==> 24/24 §FIX-20260920 数据管道修复批专项（THS 单域熔断 + 东财 f62 主站信任 + 板块列表镜像分页 + EM-FFLOW 真值口径 + THS-KLINE 5分钟 + QUOTE-CHAIN 同花顺首选源）..."
# A THS 单域熔断（per-operation isolation）：分钟 K 不支持档位不得误伤其他域；空 op 是 no-op；
#   分钟源失败只熔断「分钟」域，不得污染 quote/kline/boards/boardstocks 域（2026-09-20 实测：
#   旧整源熔断会让一次 unsupported-period 把整个同花顺出口打死，咨询页行情链全盘失效）。
go test -count=1 ./internal/data/ -run 'TestTHSBreaker|TestTHSMinuteUnsupportedPeriodError|TestTripThsEmptyOp|TestMinuteKLineUnsupportedPeriod|TestMinuteKLineProviderFailureTrips|TestQuoteChainUnaffectedByMinuteBreaker' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# B 东财 f62 主站信任 / 镜像弃用：主站 stock/get 的 f62 是真实主力净流入；push2 镜像的 f62
#   是已知占位值 2，绝不可采信（否则净流入恒为 2 元）。按「服务主机」判定，而非供应商。
go test -count=1 ./internal/data/ -run 'TestEastMoneyQuoteF62' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C 板块列表镜像分页：主站 pz=500 行为字节不变；镜像按 pn 显式分页取全 496（100/100/100/100/96）；
#   跨页按 f12 去重；任一页失败整体拒绝（不返回半成品）；页数控卫防失控请求。
go test -count=1 ./internal/data/ -run 'TestSectorList' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# D EM-FFLOW 真值口径：东财 fflow 实测 6 列（日期 + 主力/小/中/大/超大 净额），不返回 in/out 对；
#   四档净额之和≈0（资金守恒）、主力净=大净+超大净。主源 hithink 落库时一并算好 Net 统一口径。
go test -count=1 ./internal/data/ -run 'TestParseMoneyFlowRealRowShape|TestEmFFlowURLHasRequiredParams' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# E THS-KLINE 5 分钟档：咨询页要的是 5 分钟 MACD，GetTHSMinuteKLine 必须把 5 传下去；
#   不支持档位（非 1/5/30/60）在发请求前返回哨兵 ErrTHSUnsupportedPeriod（不熔断、不 panic）。
go test -count=1 ./internal/data/ -run 'TestGetTHSMinuteKLine|TestTHSKLine' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# F QUOTE-CHAIN 回归：同花顺注入行情客户端后成为咨询页行情链**首选源**（东财不行用同花顺兜底，
#   换手率只有同花顺能给）。引擎 buildStockBlock + e2e 全链路双锚。
go test -count=1 ./internal/engine/ -run 'TestConsultBlock' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/e2e/ -run 'TestConsultRealtimeQuoteNetInflow|TestConsultNetInflowMissingHint|TestConsultNetInflowTrueZero' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'

echo "==> 25 §CONSULT-UX 咨询页体验修复专项（2026-09-21：markdown 渲染 + 双口径防误判 + 防编造语气）..."
# 背景：用户实测反馈咨询 tab 三个问题——①数据丢失（满屏 [数据缺失]）②一大段不分点、结构差
#       ③太枯燥不易读。根因与修复：
#   A 防误判  ：auditNumbers 只认白名单里的原样数字；模型把"股"改写成"手"/加逗号/换算单位
#              就被判编造、整段隐成 [数据缺失]。修复=数据块给"股/手"双口径 + 提示词"数字引用铁律"
#              （原样照抄、禁改写）。引擎双口径与提示词须同步——任一方漏改都会让 [数据缺失] 复发。
#   B 结构化  ：前端此前把 AI 回复当纯文本渲染（markdown 不可见），加自写轻量 Markdown 渲染器
#              （React 节点输出、不 dangerouslySetInnerHTML，杜绝 XSS）+ 提示词要求 2-4 个 ## 小标题。
#   C 防编造  ：盘前主力净流入等常"数据源未返回"，提示词要求直接说"暂未取到"而非补数字。
# A 引擎双口径（internal/engine/engine.go）：成交量/近5日量能同时给 股 与 手，断言双口径锁定
go test -count=1 ./internal/engine/ -run 'TestConsultBlockCarriesTurnoverAndIndustryOnPrimaryOutage' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# B 提示词铁律（internal/llm/llm.go → consult_prompt_test.go）：必须含 原样照抄/回答结构/数据源未返回/手/300字
go test -count=1 ./internal/llm/ -run 'TestConsultSystemPromptConsultUX' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C 前端 Markdown 渲染器（web/src/components/Markdown.jsx → markdown.test.jsx）：标题/列表/粗斜体/
#   行内代码/数字原样/无 XSS（不渲染 <img onerror>）
( cd web && npm test -- markdown ) 2>&1 | grep -E 'Test Files|passed|failed'

echo "==> 26 §CB-TICKWINDOW/§CB-RUNSTART 盘前误熔+真断线漏熔修复批（2026-09-21 实录）..."
# 背景：广州 09:10:27 误熔 / 09:26:08 二熔 / 09:30:17 开盘自愈。三处根因对应三道锁：
#   A 窗口语义：lastFailAt=「本轮连续失联起点」，成功探测必须清零（否则被成功隔开的两次
#     失败凑窗误熔）；连续失败满 miss 才熔（旧实现相邻失败间隔=探测周期恒 < miss → 真断线永不熔断）。
#   B 探测门控：健康探测只在连续竞价窗口跑（9:30-11:30/13:00-14:57）——QMT 桥心跳是
#     handlebar-tick 驱动，盘前/竞价/午休静默属正常，计入失联天天误熔。
#   C 桥时钟：XtItClient 内嵌解释器可能是 UTC 钟，ts 必须 epoch+8h 展开（现网 "02:25+08:00"
#     实际 09:25，落后 8 小时）；且 strategy 文件必须保持纯 ASCII（QMT 编辑器按 GBK 加载）。
go test -count=1 ./internal/trading/ -run 'TestBreakerFailRunStart|TestControllerTripAndIdempotent' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/data/ -run 'TestIsContinuousTrade' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# A 静态锁：成功分支清零 lastFailAt；探测失败分支不得再每轮覆写窗口起点
grep -q 'c.lastFailAt = time.Time{}' internal/trading/controller.go || { echo "--- FAIL: HealthCheck 成功探测未清零失联窗口（§CB-RUNSTART 回归）"; exit 1; }
# B 静态锁：pushRealAdvice 的 HealthCheck 必须包在 IsContinuousTrade 门内
grep -q 'data.IsContinuousTrade(time.Now())' internal/engine/scoring_loop.go || { echo "--- FAIL: 健康探测未受连续竞价窗口门控（§CB-TICKWINDOW 回归）"; exit 1; }
# C 静态锁：桥端 ts 走 _now_cn_str/_now_bj，不得回到裸 strftime 本地钟面；strategy 保持纯 ASCII
# （用 if 而非 `grep && {fail}`：无 set -e 时后者会静默吞掉判定，且 §A7-E 实录该形态易打死脚本）
if grep -q 'time.strftime("%Y-%m-%dT%H:%M:%S+08:00")' qmt_gateway/qmt_bridge_strategy.py; then echo "--- FAIL: strategy 仍用本地钟面+硬编码 +08:00（桥 ts 回归）"; exit 1; fi
grep -q '_now_bj().strftime' qmt_gateway/qmt_bridge.py || { echo "--- FAIL: qmt_bridge.py ts 未走 _now_bj（桥 ts 回归）"; exit 1; }
python3 - <<'PY' || { echo "--- FAIL: qmt_bridge_strategy.py 混入非 ASCII（QMT GBK 编辑器会撕裂源码）"; exit 1; }
import sys
d = open('qmt_gateway/qmt_bridge_strategy.py', 'rb').read()
sys.exit(0 if all(b < 128 for b in d) else 1)
PY
grep -q 'qmt_bridge_strategy.py' scripts/deploy_guangzhou.sh || { echo "--- FAIL: 部署 [2b] 清单缺 qmt_bridge_strategy.py（桥修复不会随部署下发）"; exit 1; }
echo "ok - §CB 专项守卫通过（行为回归 2 组 + 静态锁 6 道）"

echo "==> 27 §SELLPOINT-UNIFY 卖出并轨统一裁决层专项（2026-09-21：P1 signalctl 状态机 / P1-b 影子 / P2 live 切闸 / P3 模拟盘并轨 / P4 探测器修复 + §D1 护栏 + BUGFIX 缺陷1~4）..."
# 背景（docs/REFACTOR_UNIFIED_SELL_20260921.md + docs/BUGFIX_SELLPOINT_FALSEPOSITIVE_20260921.md）：
#   五路卖出信号收编为 signalctl 单一裁决通道（键=(channel,账号,代码)状态机）：
#   触硬线→锁线+固定观察窗→窗结算只认新鲜做多信号(边界⑥)→深破−12无条件全清(④)→
#   触线+双源验证利空(bearTier=dual)当轮即时硬清、未触线只预警(①)，废除 reason 利空/抛售
#   子串→止损高。paper/report 双账与 live 同口径（P3），三档灰度 ""/shadow→off→on，
#   切闸前 shadow 全量观察、影子零资金行为变化。P4 探测器修复：派发判定加跌幅下限−1.5%
#   +缩量地板+上午阈值2.2+连续两轮确认；做空/超期顶「止盈」帽改结构化 SellLevel 定帽；
#   正值回撤不再渲染。§D1 护栏1：受益个股改确定性来源（源 stock_list ∪ 标题全称匹配 ∪
#   板块成分股"名称(代码)"标签），CleanBatch 输出幂等可再清洗。
# A 裁决状态机 + 影子行为 + 护栏4 端到端（engine 侧留痕/延持/清零/双源硬清单源不硬清）
go test -count=1 ./internal/signalctl/ 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/engine/ -run 'TestSellShadow' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# B P2/P3 切闸与双账：paper 影子不执行/on 走唯一出口 ApplyUnifiedSell、report @report 账本
#   处置与镜像去重、13e 旧路在 on 下关闭
go test -count=1 ./internal/engine/ -run 'TestUnifiedSellGateSigs|TestRunPaperUnifiedJudge|TestJudgePaperLedgers|TestApplyReportVerdicts|TestJudgeReportLedger' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/paper/ -run 'TestApplyUnifiedSell|TestSellProbes' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C P4 探测器回归锁四把：缩量任何时段不命中派发 / 跌幅>-1.5 不命中 / 做空减仓级不产止盈帽 /
#   利空子串升级已废除（含否定句式）+ 两轮确认 + 正值回撤不渲染
go test -count=1 ./internal/combat_agent/ -run 'TestSellFactor|TestAssessSellSide' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/trading/ -run 'TestFromSignal' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/engine/ -run 'TestSyncLiveAdviceAlerts' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# D §D1 护栏1 确定性归因：cleaner 幂等（名称(代码)/名称|代码 混合格式全清洗）+ 咨询/归因链
go test -count=1 ./internal/data/ -run 'TestClean' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/newsagent/ 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# E 全链路 e2e（事件归因/板块传播/个股分流/行情/N形 全绿才算并轨无回归）
go test -count=1 ./internal/e2e/ -run 'TestEndToEndFullPipeline' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# F 静态锁①：reason 利空/抛售子串→止损高 的映射必须已物理删除（owner 口径①，防复活）
if grep -q 'Contains(sig.Reason, "利空")' internal/trading/advice.go; then echo "--- FAIL: advice.go 利空子串→止损映射复活（§SELLPOINT 护栏①）"; exit 1; fi
# G 静态锁②：派发判据三要素在位（跌幅下限/缩量地板/时段阈值）+ 两轮确认状态机
grep -q 'md.ChangePct <= -1.5' internal/combat_agent/sell_side.go || { echo "--- FAIL: 派发跌幅下限丢失（P4 缺陷1）"; exit 1; }
grep -q 'q.Volume >= avgV' internal/combat_agent/sell_side.go || { echo "--- FAIL: 缩量地板丢失（P4 缺陷2）"; exit 1; }
grep -q 'distributionRatioThreshold' internal/combat_agent/sell_side.go || { echo "--- FAIL: 时段阈值未接线（P4 缺陷2）"; exit 1; }
grep -q 'distConfirmed' internal/combat_agent/sell_side.go || { echo "--- FAIL: 两轮确认状态机丢失（P4 缺陷1）"; exit 1; }
# H 静态锁③：三档灰度模式与影子默认在位（默认空=shadow，绝不默认 on）
grep -q 'SellUnifiedMode' internal/config/config.go || { echo "--- FAIL: sell_unified_mode 配置项缺失"; exit 1; }
grep -q 'func (e \*Engine) sellUnifiedModeEngine' internal/engine/sell_shadow.go || { echo "--- FAIL: 统一模式读取口缺失"; exit 1; }
# I 静态锁④：paper 自动卖出唯一入口 + 主循环每轮裁决钩子
grep -q 'func (e \*Engine) ApplyUnifiedSell' internal/paper/unified_sell.go || { echo "--- FAIL: paper 统一出口缺失（P3）"; exit 1; }
grep -q 'judgePaperLedgers' internal/engine/engine.go || { echo "--- FAIL: 主循环 paper/report 每轮裁决未接线（P3）"; exit 1; }
# J 静态锁⑤（§C2 2026-09-22 负向）：深破升级判定不得残留 `&& st.Line == SellLineStopLoss` 前置——
#   残留即止盈/移动止盈锁线砸穿 −12% 绕过边界④（FIX#12 事故形态）；行为锁见
#   TestSellDeepBreachUpgrades{TakeProfit,Trail} / TestSellDeepBreachUpgradeIsOneWay 三例
if grep -q 'SellLineDeepBreach && st.Line == SellLineStopLoss' internal/signalctl/sell.go; then echo "--- FAIL: 深破升级复活 StopLoss 前置条件（§C2 回归，边界④被架空）"; exit 1; fi
go test -count=1 ./internal/signalctl/ -run 'TestSellDeepBreachUpgradesTakeProfit|TestSellDeepBreachUpgradesTrail|TestSellDeepBreachUpgradeIsOneWay' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
echo "ok - §SELLPOINT-UNIFY 专项守卫通过（行为回归 6 组 + 静态锁 10 道）"

echo "==> 28 §C1/§C1b 冻结账日期过滤 + 跨日陈旧买单无条件清扫（2026-09-22 修复批，AUDIT_FULL_UAT_20260921C CRITICAL#1）..."
# 背景（docs/FIX_PLAN_20260922.md §2.1）：LocalBuyFrozen 旧实现接收 day 参数却从不用于过滤，
# 在途冻结按全历史累计——前日网关崩溃遗留的 已报 买单僵尸行永久占用当日预算闸（闸2/闸3），
# 且读取错误被吞成 frozen=0 放水。修复三件套：
#   A 签名 (float64,error) + SQL 加 substr(created_at,1,10)=? 当日过滤（orders 两种建单格式日期前缀均在 1~10）；
#   B gate.go 冻结读取错误 fail-closed 拒绝买入，与相邻两本账同姿势；
#   C §C1b SweepOrders 最前置跨日已报/部成买单无条件降级废单——纯本地账，不受
#     Enabled/Tripped/cancel_stale_sec=-1 早退管辖，独立 30s 节流戳 lastStaleSweepAt。
go test -count=1 ./internal/store/ -run 'TestLocalBuyFrozenDayFilter|TestSweepStaleBuyOrders' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/risk/ -run 'TestGateBuyDisciplineFailClosedOnReadError' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/trading/ -run 'TestSweepOrdersStaleBuyUnconditional' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# A 静态锁①：LocalBuyFrozen 的 SQL 必须含当日日期过滤（缺了就是 C1 原缺陷复活）
grep -A6 'func (d \*DB) LocalBuyFrozen' internal/store/real_positions.go | grep -q "substr(created_at,1,10)=?" || { echo "--- FAIL: LocalBuyFrozen 日期过滤丢失（§C1 回归）"; exit 1; }
# A 静态锁②（负向）：gate 侧不得回到单值吞错形态 `frozen := g.st.LocalBuyFrozen(...)`
if grep -q 'frozen := g.st.LocalBuyFrozen' internal/risk/gate.go; then echo "--- FAIL: 冻结账读取错误被吞回单值形态（§C1 fail-open 复活）"; exit 1; fi
# C 静态锁③：跨日清扫必须接在 SweepOrders 早退之前（引用 + 独立节流戳同时在位）
grep -q 'SweepStaleBuyOrders(c.userID, beforeDay)' internal/trading/controller.go || { echo "--- FAIL: §C1b 跨日陈旧买单清扫未接线"; exit 1; }
grep -q 'lastStaleSweepAt' internal/trading/controller.go || { echo "--- FAIL: §C1b 独立节流戳丢失（会与 Enabled 早退共享节流而失效）"; exit 1; }
echo "ok - §C1 专项守卫通过（行为回归 3 组 + 静态锁 4 道）"

echo "==> 29 §F1/§F12 实盘费用腿入盈亏与成本 + 成交额单口径（2026-09-22 修复批，UAT_BYTE_LEVEL F1/F12）..."
# 背景（docs/FIX_PLAN_20260922.md §6.1）：实盘三处费用腿凭空蒸发——①ApplyRealFill 买入
# 成本只记成交均价（不含佣金），②/api/qmt/trades 统计重放 sellPnl 不扣 fee/stamp_tax、
# 买入摊成本不含费，③RealFills SELECT 丢列 + outFills 流水不回显费用。账面系统性偏乐观、
# 实盘/paper 两账口径分裂、settlement_diff.fee_diff 永不收敛。§F12：统计金额改取落库
# Amount（回退 Price×Qty），消除双口径。paper 侧本就是含费正解（paper.go:1480/:1735-1742），
# 本批全部向 paper 口径对齐。
go test -count=1 ./internal/store/ -run 'TestApplyRealFillBuyCostIncludesFee' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ -run 'TestQMTTradesFeeInclusiveReplay' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# A 静态锁①：卖出 pnl 必须含费用减项（回归旧形态 (f.Price-ps.cost)*qty 无减项即 FAIL）
grep -q 'sellPnl := (f.Price-ps.cost)\*float64(sellQty) - (f.Fee + f.StampTax)' internal/server/qmt.go || { echo "--- FAIL: trades 重放卖出 pnl 费用减项丢失（§F1 回归）"; exit 1; }
# A 静态锁②：买入摊成本必须含佣金
grep -q 'costAmt := f.Price\*float64(f.Qty) + f.Fee' internal/server/qmt.go || { echo "--- FAIL: trades 重放买入成本佣金摊入丢失（§F1 回归）"; exit 1; }
grep -q 'f.Price\*float64(f.Qty) + f.Fee) / float64(newQty)' internal/store/real_positions.go || { echo "--- FAIL: ApplyRealFill 加仓含费摊薄丢失（§F1 回归）"; exit 1; }
# B 静态锁③：RealFills SELECT 必须带回费用腿列、outFills 必须回显
grep -q 'COALESCE(fee,0), COALESCE(stamp_tax,0)' internal/store/real_positions.go || { echo "--- FAIL: RealFills 费用腿列丢失（§F1 回归）"; exit 1; }
grep -q '"stamp_tax": f.StampTax' internal/server/qmt.go || { echo "--- FAIL: trades 流水 fee/stamp_tax 回显丢失（§F1 回归）"; exit 1; }
# C 静态锁④（§F12）：统计金额以落库 Amount 为准（回退重算），双口径不得复活
grep -q 'if f.Amount > 0 {' internal/server/qmt.go || { echo "--- FAIL: 统计金额落库单口径丢失（§F12 回归）"; exit 1; }
echo "ok - §F1/§F12 专项守卫通过（行为回归 2 组 + 静态锁 6 道）"


echo "==> 30 §H1/§H2 交割单日期归一 + 卖出状态机快照防别名（2026-09-22 修复批，代理A/E批次收尾核验）..."
# §H1：TradingDayDate 产 20060102 而网关只收 YYYY-MM-DD——normalizeSettleDay 归一 + 失败
# 计数 metrics.SettleFailed + opslog 留痕；§H2：sellProbe 原地改写并回传同一指针，旧实现
# prev 与 st 别名导致跃迁判定恒 false（VerdictHold 永不留痕），现先值拷贝 prevSnap 再探针。
go test -count=1 ./internal/trading/ -run 'TestSettleDayFormatNormalized' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/signalctl/ -run 'TestSellTransitionRecords' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'func normalizeSettleDay' internal/trading/settlement.go || { echo "--- FAIL: 交割单日期归一函数丢失（§H1 回归）"; exit 1; }
grep -q 'SettleFailed()' internal/trading/settlement.go || { echo "--- FAIL: 交割对账失败计数接线丢失（§H1 回归）"; exit 1; }
grep -q 'prevSnap = \*prev' internal/signalctl/sell.go || { echo "--- FAIL: 跃迁判定值快照丢失，prev/st 别名复活（§H2 回归）"; exit 1; }
echo "ok - §H1/§H2 专项守卫通过（行为回归 2 组 + 静态锁 3 道）"

echo "==> 31 §H3 全局写端点收权 admin（/api/action + news showall/reanalyze，2026-09-22 修复批）..."
# ctrlFor 恒取运营账号引擎——成员写 showall/reanalyze/ignore 都在改全局状态（ignore 写
# 运营信号簿、reanalyze 烧全局 LLM 额度），路由整体升 adminMiddleware + 前端按钮隐藏 +
# 实盘下单受理 opslog.Audit("live_order") 留痕。
go test -count=1 ./internal/server/ -run 'TestH3ActionAdminOnly|TestH3NewsShowAllAndReanalyzeAdminOnly' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'POST /api/action", s.adminMiddleware(s.handleFixAction)' internal/server/server.go || { echo "--- FAIL: /api/action 路由回退 authMiddleware（§H3 越权复活）"; exit 1; }
grep -q 'POST /api/news/reanalyze", s.adminMiddleware(s.handleNewsReanalyze)' internal/server/server.go || { echo "--- FAIL: reanalyze 路由回退 authMiddleware（§H3）"; exit 1; }
grep -q 'POST /api/news/showall", s.adminMiddleware(s.handleNewsShowAllToggle)' internal/server/server.go || { echo "--- FAIL: showall 路由回退 authMiddleware（§H3）"; exit 1; }
grep -q 'admin && row.can_open' web/src/pages/Signals.jsx || { echo "--- FAIL: 信号页买入/忽略按钮管理员门控丢失（§H3 前端面）"; exit 1; }
grep -q 'api.isAdmin() &&' web/src/pages/Hotspot.jsx || { echo "--- FAIL: 资讯页手动补推按钮管理员门控丢失（§H3 前端面）"; exit 1; }
echo "ok - §H3 专项守卫通过（行为回归 1 组 + 静态锁 5 道）"

echo "==> 32 §H4/§H5 实盘卖单吞错/假成功 + 做多信号新鲜度主判据（2026-09-22 修复批，代理A）..."
# §H4：网关 200+ok:false 业务拒单不再按成功返回（旧只打日志回 nil 烧掉减仓槽）；减仓幂等槽
# 「先成功后烧」，失败 opslog.OncePer 节流留痕、下一轮可重试；M8 兜底清仓同修吞错。
# §H5：新鲜度主判据改打分自身 StockScores.UpdatedAt（5s/5min 两轮都写入），e.scoresAt 降级
# 兜底基准且 5min 批量轮同样推进——批量轮信号不再被 5s 轮时钟误判过期。
go test -count=1 ./internal/engine/ -run 'TestH4TrimSlotRetryableAfterSellFailure|TestH5BullFreshnessFromOwnTimestamp|TestH5BullFreshnessFallbackToGlobalClock|TestH5StaleSignalAgeOutDespiteFreshGlobalClock' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
if grep -q '_ = e.sellRealPosition' internal/engine/scoring_loop.go; then echo "--- FAIL: 卖单错误回退 _ = 吞错形态（§H4 回归）"; exit 1; fi
grep -q 'realTrimDone\[p.TsCode\] = trimDay' internal/engine/scoring_loop.go || { echo "--- FAIL: 减仓槽「成功后烧」回写丢失（§H4 回归）"; exit 1; }
grep -q 'sell %s rejected' internal/engine/scoring_loop.go || { echo "--- FAIL: 网关业务拒单错误上抛丢失（§H4 假成功复活）"; exit 1; }
grep -q 'at := sc.UpdatedAt' internal/engine/sell_shadow.go || { echo "--- FAIL: 新鲜度主判据回退全局时钟（§H5 回归）"; exit 1; }
grep -q 'e.scoresAt = time.Now()' internal/engine/engine.go || { echo "--- FAIL: 5min 批量轮未推进兜底打分时钟（§H5 回归）"; exit 1; }
echo "ok - §H4/§H5 专项守卫通过（行为回归 4 例 + 静态锁 5 道）"

echo "==> 33 §H6/§H7 SSE 续传通道保全 + auth:expired 陈旧闭包（2026-09-22 修复批，代理B）..."
# §H6：onerror 不再手动 close+新建——CONNECTING 态留给浏览器原生重连（唯一携带
# Last-Event-ID 的通道，服务端补发环只认它）；仅 CLOSED 才换票重建（去重定时器），
# disconnectSSE 同步清定时器防登出后复活。§H7：once-registered 监听器经 ref 转发壳
# 读最新 loggedIn/闭包，首帧 false 冻结闭包不再吞掉登出提示。
( cd web && npm test -- sse_h6_reconnect auth_expired_h7 )
grep -q 'sseReconnectTimer' web/src/api/index.js || { echo "--- FAIL: CLOSED 兜底重建定时器丢失（§H6 回归）"; exit 1; }
grep -q 'readyState !== 2' web/src/api/index.js || { echo "--- FAIL: 原生重连让位判定丢失，onerror 回到无条件 close+new（§H6 回归）"; exit 1; }
grep -q 'loggedInRef.current' web/src/App.jsx || { echo "--- FAIL: auth:expired 回到渲染态闭包读取（§H7 回归）"; exit 1; }
grep -q 'authExpiredHandler.current' web/src/App.jsx || { echo "--- FAIL: 转发壳未读最新闭包（§H7 回归）"; exit 1; }
echo "ok - §H6/§H7 专项守卫通过（vitest 2 文件 + 静态锁 4 道）"

echo "==> 34 §H9/§M16 outbox 批量错位 + 网关 inflight 收割（2026-09-22 修复批，代理C）..."
# §H9：outbox pump 回写按条目唯一 id 定位（旧数组下标在同批前序出队后整体移位，身份校验
# 必失败→整批每秒重投）；出队行定位改 id 线性查找。注意：修复落位 internal/notify/outbox.go
# （FIX_PLAN 原文写 internal/trading 系笔误）。§M16：dispatch inflight 超时收割
# （inflight_at 龄判据 + 启动收割 + 60s 巡检线程，判废不重排防重复下单）；HTTP 桥补
# diag kind 处理 + unknown kind 负回执，派发行不再永挂。
go test -count=1 ./internal/notify/ -run 'TestOutboxBatchPumpSingleDelivery|TestOutboxBatchPumpMixedOutcome' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
py_tests qmt_gateway/tests 'dispatch_reap_stale_inflight or unknown_kind or diag_dispatch' 2>&1 | grep -E "passed|failed|error|Ran [0-9]+ test|OK"
if grep -q 'jobs = append(jobs, job{idx: i' internal/notify/outbox.go; then echo "--- FAIL: pump 回退数组下标定位（§H9 风暴复活）"; exit 1; fi
grep -q 'id: o.nextID' internal/notify/outbox.go || { echo "--- FAIL: 出队条目唯一 ID 分配丢失（§H9 回归）"; exit 1; }
grep -q 'inflight_at' qmt_gateway/store.py || { echo "--- FAIL: inflight 取单时刻落列丢失（§M16 回归）"; exit 1; }
grep -q '_reap_dispatch_inflight' qmt_gateway/gateway.py || { echo "--- FAIL: 网关 inflight 收割线程未接线（§M16 回归）"; exit 1; }
grep -q '_execute_diag' qmt_gateway/qmt_bridge.py || { echo "--- FAIL: 桥 diag kind 处理丢失（§M16 永挂复活）"; exit 1; }
echo "ok - §H9/§M16 专项守卫通过（Go 2 例 + py 3 组 + 静态锁 5 道）"

echo "==> 35 §F2/§M4/§F5/§M5/§F3 回报接入段五修（2026-09-22 修复批，代理D）..."
# §F2 持仓快照 ts_code 整批字段校验（ErrInvalidPositionReport→400→网关死信）；
# §M4 委托状态腿落库失败回 500 让 outbox 重推（旧吞错仍回 ok 永久丢回报）；
# §F5 缺 order_id 显式拒收留痕、仅缺 signal_id 用 ext:<order_id> 占位键落最小状态行
# （旧双键齐全才进块，手工单状态永久隐身）；§M5 strategy/signal_id COALESCE 防对账洗空；
# §F3 muxWithJSONErrors 统一 404/405 JSON 信封（本工具链 ServeMux 无 NotFound 字段，
# 用 mux.Handler 预判 + 3xx 放行）；golden 契约 report_fields.json ↔ qmtReportEvent 反射锁。
go test -count=1 ./internal/server/ -run 'TestReportContractGolden|TestHandleQMTReportPositionsInvalidTsCode400|TestHandleQMTReportOrderStoreError500|TestHandleQMTReportOrderMissingKeys' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/store/ -run 'TestReconcileRejectsInvalidTsCode' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
py_tests qmt_gateway/tests/test_channel_position_fields.py
grep -q 'ErrInvalidPositionReport' internal/store/real_positions.go || { echo "--- FAIL: 持仓快照字段校验哨兵丢失（§F2 回归）"; exit 1; }
grep -q 'errors.Is(err, store.ErrInvalidPositionReport)' internal/server/qmt.go || { echo "--- FAIL: 400 映射丢失，校验失败回退 500 无限重推（§F2 回归）"; exit 1; }
if grep -q 'if ev.OrderID != "" && ev.SignalID != "" {' internal/server/qmt.go; then echo "--- FAIL: 双键齐全才进块复活，缺键回报静默丢弃（§F5 回归）"; exit 1; fi
grep -q '"ext:" + ev.OrderID' internal/server/qmt.go || { echo "--- FAIL: 占位信号键丢失（§F5 回归）"; exit 1; }
grep -q 'COALESCE(NULLIF(excluded.strategy' internal/store/real_positions.go || { echo "--- FAIL: 对账洗空保护丢失（§M5 回归）"; exit 1; }
grep -q 'muxWithJSONErrors' internal/server/server.go || { echo "--- FAIL: 404/405 JSON 信封出口丢失（§F3 回归）"; exit 1; }
grep -q 'can_use_qty' qmt_gateway/broker.py || { echo "--- FAIL: xt 直连通道 T+1 可卖量字段丢失（§M5 通道字段对齐回归）"; exit 1; }
echo "ok - §F2 回报接入段专项守卫通过（Go 5 例 + py 1 文件 + 静态锁 7 道）"

echo "==> 36 §M14/§M15 队列同因连败熔断 + 夜链半截自愈（2026-09-22 修复批，代理E）..."
# §M14：RequeueFailedTask 以 error 前 200 字为指纹，同因连败 SameReasonFailLimit=10 挂起
# failed_needs_attention（不再重排，人工 RequeueTask 复活清零计数）；异因不设上限保留旧
# 设计；worker 三处失败收口统一 notifySameCauseBreaker（留痕+清冷却+state+高优告警一次，
# researchd 经 SetAlertFunc 接 PushGateway）。§M15：夜链改「计划序位即 chain_seq +
# ChainTaskSeqs 占用补缺」，单环入队失败不中断整链、后续 tick 自动补投，state 记
# chain_issued/chain_total，缺额 opslog.OncePer(30min) 留痕。
go test -count=1 ./internal/scheduler/ -run 'TestSameCauseBreakerParksTask|TestDifferentCauseFailuresNeverBreaker|TestNightlyChainHalfEnqueueBackfill|TestNightlyChainMissingSeqBackfilledAcrossRestart' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/store/ -run 'TestTaskSameCauseBreaker' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'SameReasonFailLimit = 10' internal/store/research_tasks.go || { echo "--- FAIL: 同因熔断阈值常量丢失（§M14 回归）"; exit 1; }
if grep -q 'researchTaskRetryCap' internal/store/research_tasks.go; then echo "--- FAIL: 旧异因混计上限复活（§M14 设计为同因取代）"; exit 1; fi
grep -q 'failed_needs_attention' internal/store/research_tasks.go || { echo "--- FAIL: 挂起终态缺失（§M14 回归）"; exit 1; }
grep -q 'ChainTaskSeqs' internal/scheduler/worker.go || { echo "--- FAIL: 夜链序位补缺丢失，半截链复活（§M15 回归）"; exit 1; }
grep -q 'SetAlertFunc' cmd/researchd/main.go || { echo "--- FAIL: researchd 告警通道未接线（§M14 静默死亡复活）"; exit 1; }
echo "ok - §M14/§M15 专项守卫通过（行为回归 5 例 + 静态锁 5 道）"

echo "==> 37 §H8/§F6 运维探针同源 + UAT 构建指纹（2026-09-22 修复批，代理F）..."
# §H8：watchdog/daily_ops_check/register 三方探针 URL 收敛 service_probe_config.ps1 单源
# （quant→:8081/setup 无鉴权、web→Caddy :8080、researchd→scheduler_status.json mtime≤5min
# 文件心跳）；旧三连错（:8080/api/status 鉴权 401 误熔、虚构 :9091/health、探引擎根路径）
# 静态锁死不得复活；deploy_guangzhou.sh 同步清单必须带上新文件（教训=quote_feed 漏列）。
# §F6：uat_bootstrap 构建注入与生产同款 -ldflags buildCommit，A7 指纹用例口径对齐。
test -f deploy/qmt-win/service_probe_config.ps1 || { echo "--- FAIL: 探针同源配置文件缺失（§H8）"; exit 1; }
grep -q 'service_probe_config.ps1' deploy/qmt-win/all_service_watchdog.ps1 || { echo "--- FAIL: watchdog 未 dot-source 同源配置（§H8 回归）"; exit 1; }
grep -q 'service_probe_config.ps1' scripts/daily_ops_check.ps1 || { echo "--- FAIL: daily_ops_check 未 dot-source 同源配置（§H8 回归）"; exit 1; }
grep -q 'service_probe_config.ps1' deploy/qmt-win/register_engine_services.ps1 || { echo "--- FAIL: register 未接同源配置（§H8 回归）"; exit 1; }
if grep -qE '127\.0\.0\.1:9091|127\.0\.0\.1:8080/api/status' deploy/qmt-win/all_service_watchdog.ps1 scripts/daily_ops_check.ps1; then echo "--- FAIL: 旧误熔探针形态复活（§H8：:9091 虚构口/鉴权口探活）"; exit 1; fi
grep -q 'service_probe_config.ps1' scripts/deploy_guangzhou.sh || { echo "--- FAIL: 探针配置未入部署同步清单（§H8/§ENH-5 教训）"; exit 1; }
grep -q 'main.buildCommit' scripts/uat_bootstrap.sh || { echo "--- FAIL: UAT 构建指纹注入丢失（§F6 回归）"; exit 1; }
echo "ok - §H8/§F6 专项守卫通过（静态锁 8 道）"

echo "==> 38 §M1 quote_source 契约单源化 golden 双向锁（2026-09-22 修复批二波，代理G/K）..."
# M1：行情源名散落六处（Go 枚举 / 前端下拉 / 网关日志文本 / 桥策略 / E2E 断言 / mock 注入）
# 过去各写各的，源名一改即静默失配。现收敛单源 qmt_gateway/contract/quote_sources.json：
# Go 侧 AST 双向锁（golden≡AllQuoteSources()≡各站点字面量）、E2E 加载器与 uat_bootstrap
# 注入值同读 golden（§3.1-1），前端 dropdown 亦由 enum 派生。反例=任何一处绕过 golden。
go test -count=1 ./internal/data/ -run 'TestQuoteSourcesGolden|TestQuoteSourceSitesMatchEnum' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
test -f qmt_gateway/contract/quote_sources.json || { echo "--- FAIL: golden 源清单文件缺失（M1 回到各写各的）"; exit 1; }
grep -q 'QMT-L1' qmt_gateway/contract/quote_sources.json || { echo "--- FAIL: golden 内容漂移（缺 QMT-L1）"; exit 1; }
grep -q '同花顺（新）' qmt_gateway/contract/quote_sources.json || { echo "--- FAIL: golden 内容漂移（缺 同花顺（新））"; exit 1; }
grep -q 'quote_sources.json' web/e2e/quote_sources.mjs || { echo "--- FAIL: E2E golden 加载器丢失（M1 盲区复活）"; exit 1; }
grep -q 'quote_sources.json' scripts/uat_bootstrap.sh || { echo "--- FAIL: uat_bootstrap 注入未读 golden（§3.1-1 复活）"; exit 1; }
grep -q 'E2E_QUOTE_SOURCE' scripts/uat_bootstrap.sh || { echo "--- FAIL: E2E_QUOTE_SOURCE 注入通道丢失（M1）"; exit 1; }
echo "ok - §M1 专项守卫通过（行为锁 2 例 + 静态锁 6 道）"

echo "==> 39 §M2/§M3 降级报成功族：last-known-good 保底 + 新闻源真实探测（2026-09-22 修复批二波，代理G）..."
# M2：行情整轮全源失败旧实现清空快照并把 lastOK 刷成"刚成功"——面板显示旧数据却报新鲜。
# 现保留上一份 last-known-good、不推进 lastOK、60s 节流告警；从未拿到过才回空。
# M3：/api/news/source-health 旧实现写死三源全 ok（探测函数根本没调上游）——收编为
# NewsSourceHealth 真实探测结构（unknown≠ok），键名 cainanshe 笔误更正 cailanshe，前端仪表盘对齐。
go test -count=1 ./internal/data/ -run 'TestGetIndexDataLastKnownGoodFallback|TestGetSectorsStaleCacheFallback|TestGetSectorStocksStaleCacheFallback' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/data/ -run 'TestNewsSourceHealthUnknownBeforeAnyProbe|TestNewsSourceHealthDrivenByRealFetches' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'last-known-good' internal/data/fetcher.go || { echo "--- FAIL: M2 全源失败保留上一份语义丢失"; exit 1; }
grep -q 'NewsSourceHealth' internal/data/source.go || { echo "--- FAIL: M3 真实探测结构丢失"; exit 1; }
grep -q '"cailanshe"' internal/data/market.go || { echo "--- FAIL: 财联社正名键常量丢失（M3 契约键）"; exit 1; }
# 负锁只扫非注释、非测试行：修复说明注释与 M3 测试自身的"永绝后迹"断言合法含该拼写。
if grep -rn 'cainanshe' internal web/src cmd 2>/dev/null | grep -v '//' | grep -v '_test\.go' | grep -v Binary >/dev/null; then echo "--- FAIL: 笔误键 cainanshe 复活（M3 已正名 cailanshe）"; exit 1; fi
grep -q 'cailanshe' web/src/pages/Dashboard.jsx || { echo "--- FAIL: 仪表盘新闻源未对齐 M3 键名口径"; exit 1; }
echo "ok - §M2/M3 专项守卫通过（行为锁 5 例 + 静态锁 4 道）"

echo "==> 40 §M6/§TZ/§REJECT 数据管道 py 批：pctChg 大小写 / 时区单源 / 双空回报拒收（2026-09-22 修复批二波，代理H）..."
# M6：dataload_keepalive 读 csv 键 "pctchg"，服务端契约键是驼峰 "pctChg"（DictReader 大小写
#   敏感）→ change/pct_chg 恒 0 且"写入成功"；现读 0 值预校验，整轮不健康则 ok=False、rc≠0。
# TZ：全仓 .py 手拼 "+08:00" 的 time.strftime 假时区收编为 store._now_cn()/桥 _now_cn_str()
#   单源（静态锁见下），时间格式契约 golden 化 qmt_gateway/contract/time_formats.json。
# REJECT：/dispatch/result 的 trade 回报 trade_id 与 order_id 皆空 → 400 显式拒收不落垃圾行；
#   outbox seq 先落空串、拿 rowid 后回填，杜绝"回报存在但无法归属派发"。
py_tests qmt_gateway/tests/test_time_formats.py '' 2>&1 | grep -E "passed|failed|error|Ran [0-9]+ test|OK"
py_tests qmt_gateway/tests/test_trade_identity_reject.py '' 2>&1 | grep -E "passed|failed|error|Ran [0-9]+ test|OK"
py_tests qmt_gateway/tests/test_dataload_keepalive.py '' 2>&1 | grep -E "passed|failed|error|Ran [0-9]+ test|OK"
grep -q 'INDEX_PCT_COL = "pctChg"' scripts/dataload_keepalive.py || { echo "--- FAIL: M6 pctChg 契约键修正回退（大小写错键复活）"; exit 1; }
test -f qmt_gateway/contract/time_formats.json || { echo "--- FAIL: 时间格式 golden 缺失（§TZ/§3.1-5）"; exit 1; }
# 负锁与 H 的 test_time_formats 静态锁同 regex 口径：只拦「time.strftime( 裸调用喂 +08:00」，
# 放行收敛点自身（datetime.now(CN_TZ).strftime(...)，钟面与标签恒一致）。
if grep -rn --include='*.py' -E 'time\.strftime\([^)\n]*\+08:00' qmt_gateway scripts 2>/dev/null | grep -v '/tests/' >/dev/null; then
	echo "--- FAIL: .py 假时区字面量复活（§TZ：+08:00 只许出自 _now_cn/_now_cn_str 单源）"; exit 1; fi
grep -q '§REJECT' qmt_gateway/handler.py || { echo "--- FAIL: 双空回报拒收逻辑移除（M-REJECT 静默垃圾行复活）"; exit 1; }
grep -q '§REJECT' qmt_gateway/gateway.py || { echo "--- FAIL: /dispatch/result 侧 400 拒收移除（M-REJECT）"; exit 1; }
echo "ok - §M6/§TZ/§REJECT 专项守卫通过（py 行为锁 3 组 + 静态锁 5 道）"

echo "==> 41 §M8 推送三通道内聚（禁双发铁律）+ EXPVAR 收权（2026-09-22 修复批二波，代理L/J）..."
# M8：JPush 网关通道过去挂在业务调用方 Push 之后再补一刀 PushGateway——两处调用点两处漏，
# 且 researchd 侧干脆没接（推送分裂）。现 Push 内聚三通道（WS/webhook/网关），网关扇出在
# 释放 RLock 后执行（RWMutex 重入死锁防线），级别闸 gatewayMinLevel 默认中级别。
# 禁双发铁律：业务侧（internal/engine、cmd/quant）出现 Push 后再调 .PushGateway( 即回归。
# EXPVAR：/debug/vars 与 /metrics 旧实现对成员全开（持仓/资金暴露面）——现 admin-only。
go test -count=1 ./internal/notify/ -run 'TestM8' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ -run 'TestExpvarMetricsAdminOnly|TestDebugVarsNotMounted' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
if grep -rn '\.PushGateway(' internal/engine cmd/quant --include='*.go' 2>/dev/null | grep -v _test.go | grep -v 'NewJPushGateway\|NewNtfyGateway\|NewWebhookGateway' >/dev/null; then
	echo "--- FAIL: 禁双发铁律回归——业务调用方在 Push 之外又直调 PushGateway（M8 双发/分裂复活）"; exit 1; fi
grep -q 'gatewayMinLevel' internal/notify/notify.go || { echo "--- FAIL: 网关级别闸丢失（M8）"; exit 1; }
echo "ok - §M8/EXPVAR 专项守卫通过（行为锁 7 例 + 静态锁 2 道）"

echo "==> 42 §M9/§M10/§M11 轮首快照 + 卖出锚点落盘 + 买入队列防丢 + InsertRows 面校验（2026-09-22 修复批二波，代理J）..."
# M9：sell_unified_mode 一轮内被多处各自重读——轮中翻转会前半轮影子后半轮执行。
#   现轮首快照一次、以参数贯传到全部同轮消费方（param-plumbed，禁中途重读）。
# M10：模拟盘移动止损高点锚过去每轮自抬、重启即清零（止损位漂移）。现原子落盘
#   <acctDir>/paper_sell_anchors.json，重启回读合并取较高者（锚单调不降，防重启倒退）。
# M11：内存买队列停机即丢在途单——StopBuyDispatcher 排空落盘、StartBuyDispatcher 回读
#   并按 SignalID 去重；InsertRows 表面对象（列名裸标识符+白名单/PRAGMA 兜底）收口注入面。
go test -count=1 ./internal/engine/ -run 'TestSellRoundModeShadowToOnFlip|TestSellRoundModeOnToShadowFlip|TestSellRoundModeParamAuthoritative' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/engine/ -run 'TestPaperSellAnchorSurvivesRestart|TestPaperSellAnchorAccountIsolation' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/engine/ -run 'TestBuyQueueShutdownDrainAndRestore|TestBuyQueueRestoreDedupUnit' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/store/ -run 'TestInsertRowsWhitelist' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'paper_sell_anchors.json' internal/engine/sell_anchor.go || { echo "--- FAIL: M10 锚点文件持久化移除（重启清零复活）"; exit 1; }
grep -q '轮首快照' internal/engine/engine.go || { echo "--- FAIL: M9 轮首快照语义移除（轮中翻转复活）"; exit 1; }
grep -q 'validateInsertSurface' internal/store/store.go || { echo "--- FAIL: M11 InsertRows 表面校验移除"; exit 1; }
echo "ok - §M9/M10/M11 专项守卫通过（行为锁 7 例 + 静态锁 3 道）"

echo "==> 43 §M7 部署清单收编：quant-web 注册 / 网关配置生成 / 静态锁总闸（2026-09-22 修复批二波，代理L）..."
# M7a/b/c：verify_deploy 要求的 quant-web(Caddy) 站点与网关 config.xt.json 过去全是手工
# 一次性操作（部署面与校验面各说各话）——现 register_web_service.ps1 / ensure_gateway_config.ps1
# 幂等收编进 deploy_guangzhou.sh；29 道部署口径静态锁收敛在 check_deploy_static_locks.sh，
# 本段直接执行该总闸（端口单源/清单缺项/GUANGZHOU_CADDY 手工步骤残留等一次全验）。
test -f deploy/qmt-win/register_web_service.ps1 || { echo "--- FAIL: web 站点注册脚本缺失（M7b 手工步骤复活）"; exit 1; }
test -f deploy/qmt-win/ensure_gateway_config.ps1 || { echo "--- FAIL: 网关配置生成脚本缺失（M7c 秒起秒死复活）"; exit 1; }
grep -q 'register_web_service.ps1' scripts/deploy_guangzhou.sh || { echo "--- FAIL: web 注册未接入部署链（M7b）"; exit 1; }
grep -q 'ensure_gateway_config.ps1' scripts/deploy_guangzhou.sh || { echo "--- FAIL: 网关配置生成未接入部署链（M7c）"; exit 1; }
bash ./scripts/check_deploy_static_locks.sh || { echo "--- FAIL: 部署静态锁总闸未过（M7 口径漂移）"; exit 1; }
echo "ok - §M7 专项守卫通过（静态锁 4 道 + 部署口径总闸）"

echo "==> 44 §M12/§M13 移动壳凭据面 + 前端权限一致性 + researchd 主链冒烟（2026-09-22 修复批二波，代理K/L/J）..."
# M13：成员账号进 Quant/Paper 页——①403 后轮询定时器照跑（opslog 灌噪声）；②判 403 用
#   e.message.indexOf('无权限')，permMiddleware 回英文必漏判。现 isForbidden(状态码) 单口径
#   + 403 即停全部轮询；vitest 反例锁四把 + 源码静态锁防 indexOf('无权限') 复活。
# M12：Android 壳 WebView 无条件 setWebContentsDebuggingEnabled(true)（release 也可被
#   chrome://inspect 摘 token）→ BuildConfig.DEBUG 门控；注入 JS 字符串全走 jsQuote 转义。
# 冒烟：researchd main 装配链（SetAlertFunc→PushGateway 等接线）过去只有编译期保证——
#   现 TestSmokeResearchdMainChain 行为级冒烟（M14 告警接线不再可能"编译过=接线对"）。
go test -count=1 ./internal/scheduler/ -run 'TestSmokeResearchdMainChain' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
( cd web && npm test -- m13_forbidden_poll ) 2>&1 | grep -E 'Test Files|passed|failed'
# 负锁过滤注释行（:197/:267/:433 的"旧写法不得复活"说明合法含该字面量），只拦真代码。
if grep -nE "indexOf\('无权限'\)|includes\('无权限'\)" web/src/pages/Quant.jsx web/src/pages/Paper.jsx | grep -vE ':[0-9]+:[[:space:]]*//' >/dev/null; then
	echo "--- FAIL: M13 中文文案判 403 复活（permMiddleware 英文形态漏判）"; exit 1; fi
grep -q 'isForbidden' web/src/pages/Quant.jsx || { echo "--- FAIL: Quant 页状态码判定丢失（M13）"; exit 1; }
grep -q 'isForbidden' web/src/pages/Paper.jsx || { echo "--- FAIL: Paper 页状态码判定丢失（M13）"; exit 1; }
grep -q 'BuildConfig.DEBUG' mobile/app/src/main/java/com/liangzai/quant/MainActivity.kt || { echo "--- FAIL: M12 调试门控回退为无条件开启"; exit 1; }
grep -q 'jsQuote' mobile/app/src/main/java/com/liangzai/quant/MainActivity.kt || { echo "--- FAIL: M12 注入转义通道丢失"; exit 1; }
echo "ok - §M12/M13/SMOKE 专项守卫通过（行为锁 2 组 + 静态锁 5 道）"

echo ""
echo "==> 全部通过"
