#!/usr/bin/env bash
# 改动全量验证脚本（含 2026-08-05 实时链路改动专项 + 2026-09-11 回测自动增强专项 + 2026-09-12 做空策略链路专项
#   + 2026-09-13 UAT 修复批/研究升级批/情绪面板 A/B/C 专项 + 2026-09-14 §DATA-OUTAGE 数据管道根治专项
#   + 2026-09-15 §ICP 备案合规 + §LIFECYCLE-WIRING 衰退降级接线专项 + 2026-09-17 §SIGNAL_CONTROLLER 信号控制器专项
#   + 2026-09-18 §UAT_FULLCHAIN 全链路修复批（费用腿/权限门控/死代码清理/CI-seed 守卫）专项）
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
#                  金额闸（单日预算 / 可用资金）仍按「已报金额 + 在途冻结」——在途报单确实占着钱
#   附带效果     ：同一委托多次部分成交算 1 笔；QMT 客户端手工单（本地无 orders 行）计入额度
#   回归         ：三处锁旧口径的断言（gate/guards/engine）改写为钉新语义 + store 层边界用例
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
#   只查真实代码行——注释里引用旧 SQL 文案作说明用途，不算违规（先剔除 # 注释行再匹配）
grep -vE '^\s*#' .github/workflows/nightly-e2e.yml | grep -q 'FROM users' && { echo "FAIL - nightly seed 仍引用 users 表"; exit 1; } || true
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

echo "==> 13/14 单日买入笔数改「已成交」口径专项（2026-09-18 §BUY_COUNT_FILLED）..."
# 口径分工（设计决定，勿混成一个）：
#   笔数闸 → fills 表当日买入成交，按委托去重（order_id → 券商交割流水号 → 行 ID 依次兜底）
#   金额闸 → orders 表当日已报金额 + 在途冻结（在途报单确实占着钱，改成只看成交等于可无限挂单）
# A store 层计数边界：部分成交去重 / 卖出不计 / 非当日不计 / 他账号不计 / 无委托号不合并
go test -count=1 ./internal/store/ -run 'TestCountBuyFilled' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# B 闸口层：成交笔数达上限才拦（旧口径是报单即占额度，被废单会锁死当天买入权）
go test -count=1 ./internal/risk/ -run 'TestGateBuyDiscipline' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# C 交易层：达上限后拒绝新买入、卖出不受限
go test -count=1 ./internal/trading/ -run 'TestGuardDailyBuysCap' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
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

echo ""
echo "==> 全部通过"
