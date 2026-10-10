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
#     + 二波（2026-09-22 当日续）：§M1 golden 源契约单源（见 38）+ §M2/§M3 降级报成功族（见 39）+ §M6/§TZ/§REJECT 数据管道 py 批（见 40）+ §M8 推送三通道内聚/EXPVAR 收权（见 41）+ §M9/§M10/M11 快照与落盘批（见 42）+ §M7 部署清单收编（见 43）+ §M12/§M13 前端与移动壳一致性 + researchd 冒烟（见 44）
#     + C批（2026-09-22 晚间，owner 裁决清单四件套）：§XCHECK 价格复核闸接线/CrossCheckPrice 收编（见 45）+ §NATIVEAUTH 登录 token 迁原生加密存储（见 46）+ §ROOTQMT 根级死键防回潮 + §APPVER APK 服务端驱动强制更新（见 47））
#     + 夜间批（2026-09-23，owner 裁决"报警不是修复，兜住才是"四件套）：§KLINE-CHAIN-3 日K三级兜底链+库内腿四道守卫（见 82）+ §SIDE-AUTH-2 方向权威补漏/待核对通道/方向必填（见 83）+ §FILL-AMEND 追加式人工勘误+fills_effective 单点收敛+只读守恒自检+前端逐笔入口（见 84）+ §FILL-AMEND 只读取证脚本纪律（见 85）
#     + 09-24 批（owner 裁决 1「成交回报 signal_id 被截到 24 字符，要修；先定回填还是读取端兼容」）：
#       §SIGID-TRUNC 写路径单一截断点 wire_ref + 网关第四级前缀归因（歧义不猜、还原留痕）+
#       Go 读取端两边兼容（双向前缀 + 反向腿交易日闸，**不回填历史行**——编号是事实主键，
#       判重索引与勘误台账挂在它上面，改长度即新旧行分裂）（见 86）
#     + 09-24 批（owner 裁决 2「momentum 战法缺回放适配器」三选一 → 按实盘语义重写判据）：
#       §MOMENTUM-LIVE-REPLAY 兜底互斥（同标的当日兄弟出过信号即不让动量入场，回查用兄弟裸判据）+
#       当日撮合（入场=触发当日收盘，缺省战法仍次日开盘）+ 只计实盘买入档（观察档不算交易信号）；
#       分钟 MACD/盘中多轮/跨轮提升门三条数据不支持，只在注释与出门文本里标为残余近似（见 87）
#     + 09-24 批（「白天只龙头出信号」是 owner 肉眼看出来的、24 条探针一条都没红 ⇒ 观测面隐形）：
#       §SIGNAL-DIST 部署面加第 22 探针（当日固化信号按战法分布 + leader_only 读数）+ INFO 观测通道
#       （绿也回显读数、不进 PASS/FAIL 判数）；判红只认解析失败/缺 signals 数组，no-file、跨日桶、
#       当日零信号一律合法态（见 88）
#     + 09-24 收尾批（§88 自己把一轮 verify 静默跑死：计数锁的"绿色取值"是 0，`grep -c` 零命中退出码 1，
#       `set -euo pipefail` 下整条赋值失败 ⇒ 无 FAIL 无 ok 直接中止）：
#       §GATE-COUNT-LOCK 门禁自查——凡 `v=$(... grep ...)` 必须写 `|| true`（判红交给后面的等值判断），
#       加固面设下限，且两头钉住 `set -euo pipefail` 本身（见 89）
#     + 09-24 批（任务 #43 根因半：仓库根反复长出 Windows 路径形态的怪文件，09-23/09-24 各清一次又回来）：
#       §BRIDGE-PATH 桥目录改由 QMT_BRIDGE_DIR 覆盖、**未设置时默认值逐字不变**（seen 判重账本一搬家
#       ＝重启可重放同一笔委托），五个常量统一 os.path.join，POSIX 写前 _dir_ready 拒写
#       （trace 可丢，report/seen 抛错走既有 False 分支 fail-closed），测试会话 conftest 指临时目录（见 90）
#     + 09-24 批（owner 裁决「分钟 K 落库升级：建表 + 回填 + 动量回放换真 5 分钟」）：
#       §MINUTE-K minute_klines 建表三定（不复权 / ts 北京墙钟前缀切片 / 主键幂等）+
#       装载器诚实出门（0 行判失败、失败率闸、平均根数读数；上游只有"最近 N 根"无分页，
#       所以"回填三年"在数据源层面不成立——不编）+ 回放侧动量优先真分钟 MACD
#       （根数不足即报不可用，闸门唯一，半日数据不许冒充升级；覆盖率随出门文本回显）+
#       夜间日增环 minute_sync 紧跟 dataload、分钟表从未回填过时如实跳过并留痕（见 91）
#   §MINUTE-K-CHAIN 分钟链两修（09-24 首次真跑锤出）：链入口把 ts_code 归一成上游认得的裸 6 位
#       代码（旧行为＝新浪空返回＋腾讯解析错，看着像"三源全坏"其实一条没取到），装载器改绑新增的
#       严格不复权链（末腿东财 fqt=1 前复权按口径拒用，防除权日假分钟 MACD 跳水）（见 92）
#   §MINUTE-OPS 生产侧两条"历来只能手敲 ssh"的通道收编成正规脚本（09-24 owner 令「把这两步变成正规脚本」）：
#       scripts/place_qmt_bridge.sh（桥策略落位到 QMT 实际加载的策略文件 + 三值 SHA 判定）+
#       scripts/backfill_minute_guangzhou.sh（dataload minute-sync 由一次性计划任务承载，ssh 断开不影响）；
#       两条同口径：缺省只预览（预览一次网都不碰）、动手须显式 -Apply、BatchMode 预探测不挂起、
#       判据全走纯 ASCII 锚点行（Go 侧新增 MinuteStats.ASCII() 与 MINUTE-SYNC START/PROGRESS/SUMMARY），
#       日志"没有 SUMMARY"一律判未收尾——半态（进程没了/GBK 把锚点行吞掉半截）绝不读成成功（见 93）
#   §FILL-AMEND-CLI 历史错账改判的正规通道 scripts/amend_fill_guangzhou.sh：缺省只预览（量出来的零 POST）、
#       动手须 --apply 与 --yes 两个开关同时给、令牌只走 stdin、远端体纯 ASCII，
#       并用本地夹具真跑通 create→apply→revoke 与守恒翻转（见 94）
#   §LIB-GATE 战法库零条启用规则即判红（2026-09-25 缺陷：全量回放没带线上战法库副本时，
#       会静默按"零条线上战法"跑完一整轮，出门的数字被当成线上口径引用）：
#       ① 库读数（目录/来源/条目·启用·建成三段计数/零条成因/门状态）随回放报告与排摸产物 JSON 出门；
#       ② 声明跑库规则却零条即判红，唯一正规出口是命令行 --allow-empty-library（或 payload 同名键），
#          放行后成因读数仍随行打印——放行不等于抹掉事实；
#       ③ 死分支负锁（旧代码在追加内置五形态之后才判 len(ads)==0，永远走不到）+ 门测试 + §95；
#       ④ 研究驱动脚本在开算前打 LIB_PREMISE 逐侧读数，零可用条目直接失败、副本解析不了也失败
#          （绝不把"取回来的东西坏了"折成"现网没战法"）（见 95）
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
# §备案合规 + 生命周期接线（2026-09-15 §ICP + §GAP-P1；2026-09-30 增 §POLICE 公安备案）专项（见 8/8）：
#   ICP 页脚       ：备案号常量（沪ICP备2026045551）+ 工信部外链 vitest；登录页/仪表盘两入口挂同一组件
#   公安备案页脚    ：备案号常量（沪公网安备31011302009737号）+ beian.mps.gov.cn 查询页深链（号码数字派生，
#                   不留第二处字面量）+ 本地托管警徽资源在位；vitest 与 Playwright 两条腿各自登记
#   衰退降级接线  ：PoolDailyStats 分池逐日聚合（strategy_type 池键修复）+ DemoteAppliedRules
#                  禁用落库/dry-run/无观测保守 keep（TestPoolDailyStats|TestDemoteAppliedRules 3 组）
#                  + stepTask lifecycle 映射与默认 Steps 含 lifecycle（TestLifecycleStepMapped）
#
# 用法:
#   ./scripts/verify_changes.sh                # 编译 + 全部专项（12 个历史专项 + 今日 3 个：方向权威化/笔数成交口径/LLM 热更新）
#   ./scripts/verify_changes.sh -full          # 再连相关全量单测 + QMT 网关 py 全量 + 前端 vitest 一起跑
#   ./scripts/verify_changes.sh -collect       # §P2-L（2026-10-07）收集模式：跑完**全部**段、
#       # 收集所有 FAIL 再退。默认模式第一条红就把整轮带走（10-06 实录：§104 红 ⇒ §105–107 零读数），
#       # 于是"改了代码却从没验过专项锁"这件事被机制本身埋掉。-collect 与 -full 可叠加。
#       # 末尾打印 PASS/FAIL/SKIP/SILENT 四桶计数，并钉「四桶之和 == 派生总段数」等值锁。
#
# 说明：本机通常没有 pytest，脚本内 py_tests() 会自动退回标准库 unittest（CI 仍走 pytest）。
set -euo pipefail

# >>> GATE-COLLECT-DRIVER
# ════════════════════════════════════════════════════════════════════════════
# §P2-L（2026-10-07 修复批 波 4）`-collect` 收集模式驱动
#
# 缺陷本体：本脚本在 `set -euo pipefail` 下顺序跑 108 段，**第一条红就把整轮带走**，后面的段
# 一行都不跑。10-06 实录：§104 gofmt 判红 ⇒ §105–107 当轮零读数，而"改了 Go 文件、专项锁却从没
# 验过"这件事正是被这个机制埋掉的（报告里 P1-C 与 P2-L 是同一条因果链的两端）。
# owner 裁决：加 `-collect`＝跑完全部段、收集所有 FAIL、末尾统一报数并以非零退出；
# **默认模式语义一字不改**（首红即退仍是默认，避免动 100+ 段的退出契约）。
#
# 实现姿势——为什么是"文本级装配"而不是"每段包一个函数"：段体里到处是 `exit 1`、`$1`（§15 判
# -full）、跨段变量交接（§107→§110）与 `set -e` 隐式语义；函数化会同时改掉这三样，
# 风险远大于收益。所以一遍只跑"从某段起到文件尾"的**尾体**：由 scripts/gate_sections.py 把
# 「前言里的函数定义 + 前置段 helper + 带 GATE-SECTION-START 标记的尾体」装成临时脚本再执行，
# 仓库里的段体一个字都不动。全绿时整轮只跑一遍（与默认模式同成本），有 N 条红才跑 N+1 遍。
#
# 四种失效形态与各自的接法（本段 §111 逐枚配反证）：
#   ① 装配坏了 ⇒ "根本没跑"被读成"跑过了、全绿"：每遍分类若**一个段标记都没有**记 EMPTY，
#      当场判红并停轮；另有 `gate_sections.py check` 钉"前言除 set/cd/本标记区/顶格函数外
#      不许有副作用语句"——前言里的普通语句不会被搬进内层脚本，少搬一句就是安静地少做一次检查。
#   ② 段内静默退出（没判红、后面也没有别的段开始、日志里也没有收尾标记）：单列 SILENT 桶，
#      **绝不折算成 PASS**。把"没验过"说成"验过了"正是本枚改造要消灭的形状。
#   ③ 后段依赖前段产物：段体用 `gate_need <段号> <判据 1/0> <为什么依赖>` 声明。collect 下打
#      `GATE-SKIP <n> · 原因` 并 exit 0（本遍到此为止，下遍从后一段起），默认模式仍判红——
#      默认模式下走不到这里，走到就说明依赖被挪走/删掉了，必须现形。汇总里 SKIP 且 FAIL=0
#      同样非零退出（依赖没满足却没有一条红＝依赖声明本身坏了）。
#   ④ 某遍把仓库工作树改脏、后面的段读的是脏树：每遍跑完对 `git status --porcelain` 做差分，
#      有漂移即整轮判红（门禁只读，写树＝读数归属不再可信）。自检夹具可用
#      GATE_COLLECT_ALLOW_DRIFT=1 显式豁免（只在镜像里用，仓库内没有任何段会走到这条路）。
#
# 等值锁（H2，不许把红洗成绿）：末尾打印 PASS_SECTIONS / FAIL_SECTIONS / SKIP_SECTIONS /
# SILENT_SECTIONS / TOTAL_SECTIONS 五个数，且四桶之和必须等于**派生**总段数；缺一段、重一段都红。
# 总段数从 `gate_sections.py sections` 派生，不写死数字——写死的那份锁对下一个新增段天生失明
# （§BOM-REPO-DERIVE、§DEADGAUGE 同族教训）。
#
# 收尾歧义锁：整轮四桶全绿、日志里却出现过 `^--- FAIL` 行 ⇒ GATE_AMBIGUOUS 判红。
# "报绿却印着红字"意味着某段的退出码没代表它的判据（或有人在绿段里打印了判红文案），两种都要现形。
# ════════════════════════════════════════════════════════════════════════════

# 路径三件套。**本驱动块必须待在 `cd "$(dirname "$0")/.."` 之前**：`$0` 可能是相对形式
# （`./scripts/x.sh`、也可能在脚本自己的目录里直接 `bash x.sh`），前言那句 cd 一旦跑过，
# `$0` 的相对基准就变了——本仓 2026-10-07 夹具第一轮实跑就是被这个咬到的：镜像里 `bash fixture.sh`
# 从 gate/ 目录起，cd 之后 `dirname $0`="." 指的是仓根，于是装配器路径算成了上一层的兄弟目录。
# 现在按"调用时的 $PWD + $0"定死绝对路径，cd 前后都无所谓，镜像和真仓同一套解析。
case "$0" in
	/*) GATE_SELF_RAW="$0" ;;
	*) GATE_SELF_RAW="$PWD/$0" ;;
esac
GATE_SELF_ABS="$(cd "$(dirname "$GATE_SELF_RAW")" && pwd)/$(basename "$GATE_SELF_RAW")"
GATE_ROOT_ABS="$(cd "$(dirname "$GATE_SELF_ABS")/.." && pwd)"
GATE_SECTIONS_PY="$GATE_ROOT_ABS/scripts/gate_sections.py"

# 最多跑几遍：每跑一遍就是"从某个红段起到文件尾"，红得越早、后面越贵。上限是防失控的兜底
# （正常情况下红段数量远小于它）；越限时没拿到读数的段由汇总的 MISSING 面显式现形，不当绿。
GATE_COLLECT_MAX_PASSES="${GATE_COLLECT_MAX_PASSES:-60}"

GATE_ARG_COLLECT=0
GATE_ARG_INNER=""
for _gate_arg in "$@"; do
	# 必须用 `if` 而不是 `[ x = y ] && VAR=1`：条件为假时整个赋值语句返回 1，`set -e` 下前言当场
	# 裸死（§89 自指锁抓的就是这一族的第三种写法：末句条件语句＝静默退出、零读数）。
	if [ "$_gate_arg" = "-collect" ]; then GATE_ARG_COLLECT=1; fi
	if [ "$_gate_arg" = "-full" ]; then GATE_ARG_INNER="-full"; fi
done

# gate_kv <cls 文件> <键名> —— 从分类结果里取一个键值（制表符分列）。
# 写成函数而不是每处 awk：四桶判定要读五六个键，散着写迟早有一处拼错键名（拼错＝空串＝假绿）。
gate_kv() {
	awk -F'\t' -v k="$2" '$1 == k {print $2}' "$1" | head -1
}

# gate_need <段号> <判据是否为真（1/0）> <为什么本段依赖前段产物>
#
# 用在哪：本脚本里有三处真实的跨段产物依赖（§91 的 MK_LOAD 给 §92 用、§107 的 IP 扫描读数给 §110 用，
# 另外 §20 的 dg 已被 §23 改成自带定义）。前两处里"文件路径变量"已改成各段自带定义（那种依赖没有
# 语义，纯粹是省一次打字，不该换来一个 SKIP）；只有 §107→§110 是**语义依赖**——§110 故意不自己再扫
# 一遍字面公网 IP，就是要继承 §107 的扫描读数，所以它必须走 gate_need。
#
# 判据写成"1/0 字符串"而不是命令退出码：调用点要的是"变量在不在位"这种可回显的读数，
# `[ -n "${X+set}" ]` 直接塞进 $2 会让 FAIL 文案里看不到实际值。
gate_need() {
	local sid="$1" ok="$2" reason="$3"
	if [ "$ok" = "1" ]; then return 0; fi
	if [ "${GATE_COLLECT_INNER:-0}" = "1" ]; then
		echo "GATE-SKIP ${sid} · ${reason}"
		exit 0
	fi
	echo "--- FAIL: §${sid} 前置依赖不成立：${reason}"
	echo "    （默认模式首红即退时本段根本走不到；走到了就说明依赖被挪到后头或整条删掉了，必须现形）"
	exit 1
}

gate_collect_main() {
	local work idx start rc nxt done_flag empty over_cap final drift amb
	local fails silencs skips passn total bal skipmark
	final=0
	over_cap=0
	idx=0
	work=$(mktemp -d /tmp/gate-collect-XXXXXX 2>/dev/null || true)
	if [ -z "$work" ] || [ ! -d "$work" ]; then
		echo "--- FAIL: §P2-L -collect 连临时工作目录都建不出来（各遍日志与分类都要落盘，落不了盘就没有汇总读数）"
		return 1
	fi
	if [ ! -f "$GATE_SECTIONS_PY" ]; then
		echo "--- FAIL: §P2-L -collect 找不到装配器 ${GATE_SECTIONS_PY}（没有它就等于"随机挑几段跑一下"，读数没有意义）"
		return 1
	fi
	echo "==> [collect] 工作目录 ${work}（每遍：inner_<n>.sh / log_<n>.txt / cls_<n>.txt）"
	# 跑前工作树指纹；`|| true` 是必需的——不是 git 仓库时 git 返回非零，pipefail 下会把前言打死。
	git -C "$GATE_ROOT_ABS" status --porcelain 2>/dev/null | sort > "$work/tree.before" || true

	start=$(python3 "$GATE_SECTIONS_PY" sections "$GATE_SELF_ABS" 2> "$work/sections.err" | awk -F'\t' '$1 ~ /^[0-9]+$/ {print $1; exit}') || start=""
	if [ -z "$start" ]; then
		echo "--- FAIL: §P2-L -collect 派生不出段清单（首段都没有＝装配器与脚本格式脱节）：$(head -1 "$work/sections.err" 2>/dev/null)"
		return 1
	fi
	echo "==> [collect] 派生段清单首段=§${start}，总段数=$(python3 "$GATE_SECTIONS_PY" sections "$GATE_SELF_ABS" 2>/dev/null | awk -F'\t' '$1=="TOTAL"{print $2}')"

	while : ; do
		idx=$((idx + 1))
		if [ "$idx" -gt "$GATE_COLLECT_MAX_PASSES" ]; then
			echo "--- FAIL: §P2-L 遍数超过上限 GATE_COLLECT_MAX_PASSES=${GATE_COLLECT_MAX_PASSES}，停轮（未拿到读数的段由下面 MISSING 面点名）"
			over_cap=1
			final=1
			break
		fi
		rc=0
		python3 "$GATE_SECTIONS_PY" emit "$GATE_SELF_ABS" "$start" "$work/inner_$idx.sh" "$GATE_ROOT_ABS" 2> "$work/emit.err" || rc=$?
		if [ "$rc" != "0" ]; then
			echo "--- FAIL: §P2-L 第 $idx 遍装配失败（起点 §${start}）：$(head -1 "$work/emit.err" 2>/dev/null)"
			final=1
			break
		fi
		rc=0
		GATE_COLLECT_INNER=1 bash "$work/inner_$idx.sh" $GATE_ARG_INNER > "$work/log_$idx.txt" 2>&1 || rc=$?
		if ! python3 "$GATE_SECTIONS_PY" classify "$GATE_SELF_ABS" "$work/log_$idx.txt" "$rc" > "$work/cls_$idx.txt"; then
			echo "--- FAIL: §P2-L 第 $idx 遍分类失败（分类器读不懂自己的标记＝汇总会静默少一段）"
			final=1
			break
		fi
		empty=$(gate_kv "$work/cls_$idx.txt" EMPTY)
		done_flag=$(gate_kv "$work/cls_$idx.txt" DONE)
		nxt=$(gate_kv "$work/cls_$idx.txt" NEXT)
		passn=$(gate_kv "$work/cls_$idx.txt" STARTED)
		if [ "$empty" = "1" ]; then
			echo "--- FAIL: §P2-L 第 $idx 遍（起点 §${start}）一个段标记都没有 ⇒ 装配坏了，$work/log_$idx.txt 里跑的不是段体"
			echo "    这是收集模式最坏的失效形态：什么都没跑却可能被汇总读成'全绿'。停轮。"
			tail -n 20 "$work/log_$idx.txt" 2>/dev/null | sed 's/^/    | /' || true
			final=1
			break
		fi
		echo "==> [collect] pass #$idx 起点 §$start 跑过段：$passn ⇒ rc=$rc done=$done_flag 下一段起点=${nxt:-（无）}"
		if [ "$rc" != "0" ]; then
			echo "    —— 本遍判红段 §$(gate_kv "$work/cls_$idx.txt" FAIL) 的日志尾部（全文：$work/log_$idx.txt）："
			tail -n 40 "$work/log_$idx.txt" 2>/dev/null | sed 's/^/    | /' || true
		fi
		# SKIP 的原文也要照抄到 stdout：段是"自己声明依赖不成立"才跳的，那句成因只有段体知道。
		# 汇总里的 SKIP_REASON 是从 cls 文件里重排出来的，读的是同一个标记；这里把原文一并回显，
		# 现网才不至于"知道少了一段、却不知道那段到底写了什么"（§111 行为腿断的就是这行原文）。
		skipmark=$(gate_kv "$work/cls_$idx.txt" SKIP)
		if [ -n "$skipmark" ]; then
			echo "    —— 本遍记 SKIP（成因由段自己声明，原文照抄）："
			grep -F 'GATE-SKIP ' "$work/log_$idx.txt" 2>/dev/null | sed 's/^/    | /' || true
		fi
		git -C "$GATE_ROOT_ABS" status --porcelain 2>/dev/null | sort > "$work/tree.$idx" || true
		drift=$(diff "$work/tree.before" "$work/tree.$idx" 2>/dev/null | grep -c '^[<>]' || true)
		if [ "${drift:-0}" != "0" ] && [ "${GATE_COLLECT_ALLOW_DRIFT:-0}" != "1" ]; then
			echo "--- FAIL: §P2-L 第 $idx 遍把仓库工作树改动了 ${drift} 行（门禁只读；后面的段读的是脏树，读数归属不再可信）"
			diff "$work/tree.before" "$work/tree.$idx" 2>/dev/null | sed 's/^/    | /' || true
			final=1
		fi
		if [ "$done_flag" = "1" ]; then break; fi
		if [ -z "$nxt" ]; then break; fi
		start="$nxt"
	done

	git -C "$GATE_ROOT_ABS" status --porcelain 2>/dev/null | sort > "$work/tree.after" || true
	if ! python3 "$GATE_SECTIONS_PY" summary "$GATE_SELF_ABS" "$work" > "$work/summary.txt"; then
		echo "--- FAIL: §P2-L 汇总失败（装配器 summary 子命令报错）"
		return 1
	fi
	echo "==> [collect] 汇总（H2 等值锁：四桶之和必须等于派生总段数）"
	awk -F'\t' 'NF>=2 {print $1"="$2}' "$work/summary.txt"
	fails=$(awk -F'\t' '$1=="FAIL_SECTIONS"{print $2}' "$work/summary.txt")
	silencs=$(awk -F'\t' '$1=="SILENT_SECTIONS"{print $2}' "$work/summary.txt")
	skips=$(awk -F'\t' '$1=="SKIP_SECTIONS"{print $2}' "$work/summary.txt")
	total=$(awk -F'\t' '$1=="TOTAL_SECTIONS"{print $2}' "$work/summary.txt")
	bal=$(awk -F'\t' '$1=="BALANCE"{print $2}' "$work/summary.txt")
	# 四桶键名缺失也要当红：空串进 `-eq` 会报 shell 错，所以先归零再比。
	: "${fails:=0}" "${silencs:=0}" "${skips:=0}" "${total:=0}"
	if [ "$bal" != "OK" ]; then
		echo "--- FAIL: §P2-L 分桶不闭合（${bal}）：四桶 PASS/FAIL/SKIP/SILENT 之和必须等于派生总段数 ${total}，"
		echo "    缺的那段/重的那段由上面 MISSING/DUPLICATE 点名——把'没跑到'折进'跑过了'就是本枚改造要防的形状"
		final=1
	fi
	if [ "${fails:-0}" != "0" ] || [ "${silencs:-0}" != "0" ]; then final=1; fi
	if [ "${skips:-0}" != "0" ] && [ "${fails:-0}" = "0" ] && [ "${silencs:-0}" = "0" ]; then
		echo "--- FAIL: §P2-L 有段被记 SKIP 却没有任何 FAIL/SILENT ⇒ 依赖声明本身坏了（前段全绿时本段凭什么没跑到）"
		final=1
	fi
	if [ "$over_cap" = "0" ] && [ "${fails:-0}" = "0" ] && [ "${silencs:-0}" = "0" ]; then
		amb=$(cat "$work"/log_*.txt 2>/dev/null | grep -c '^--- FAIL' || true)
		if [ "${amb:-0}" != "0" ]; then
			echo "--- FAIL: §P2-L 收尾歧义：四桶全绿却出现 ${amb} 行 '--- FAIL' 文案 ⇒ 某段的退出码没代表它的判据"
			final=1
		fi
	fi
	if [ "${drift:-0}" != "0" ] && [ "${GATE_COLLECT_ALLOW_DRIFT:-0}" != "1" ]; then final=1; fi
	if [ "$final" = "0" ]; then
		echo "==> [collect] 全部 $total 段都有读数且无红：四桶闭合（PASS=全部，FAIL/SKIP/SILENT=0），耗时遍数 $idx"
	else
		echo "==> [collect] 本批 FAIL/SILENT/SKIP 段号见上面 FAIL_IDS/SILENT_IDS/SKIP_IDS；原始日志目录 $work"
	fi
	# 三个键各占一行、行首即键名：汇总消费方（含 §111 的镜像腿）按 `^KEY=` 取值，
	# 挤在同一行里就成了"只有第一个键取得到"——同一个坏法在 summary 侧靠 awk 分行规避，这里手动分行是把它对齐。
	echo "GATE_COLLECT_RC=${final}"
	echo "PASSES=${idx}"
	echo "WORK=${work}"
	return "$final"
}

if [ "$GATE_ARG_COLLECT" = "1" ]; then
	gate_collect_main
	exit $?
fi
# <<< GATE-COLLECT-DRIVER
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

echo "==> 8/8 备案合规 + 生命周期接线专项（2026-09-15 §ICP + §GAP-P1；2026-09-30 增 §POLICE）..."
# ICP 备案号合规文案守护：常量改动即失败（管局备案文案，变更需先核对备案回执）
grep -q '沪ICP备2026045551' web/src/components/IcpFooter.jsx && grep -q 'beian.miit.gov.cn' web/src/components/IcpFooter.jsx \
	&& echo "ok - icp 备案常量在位" || { echo "FAIL - icp 备案常量缺失"; exit 1; }
# 公安备案（2026-09-30 通过）文案守护：备案号 + 平台外链 + 本地警徽资源三件齐备才算在位
grep -q '沪公网安备31011302009737号' web/src/components/IcpFooter.jsx && grep -q 'beian.mps.gov.cn' web/src/components/IcpFooter.jsx \
	&& [ -s web/public/police-emblem.png ] \
	&& echo "ok - 公安备案常量与警徽资源在位" || { echo "FAIL - 公安备案常量或警徽资源缺失"; exit 1; }
# 深链同源锁：查询页的 code 参数必须由备案号派生（写死第二处号码＝改了号却仍跳旧记录，且不会有任何报错）
grep -q 'code=${POLICE_CODE}' web/src/components/IcpFooter.jsx \
	&& echo "ok - 公安备案深链由号码派生（无第二处字面量）" || { echo "FAIL - 公安备案深链未由 POLICE_CODE 派生"; exit 1; }
# 验收腿登记锁：vitest 与 Playwright 两条腿都要真断言公安备案链接，只改组件不改腿＝合规没有验收
grep -q 'police-link' web/src/__tests__/icp_footer.test.jsx && grep -q 'police-link' web/e2e/icp_check.spec.mjs \
	&& echo "ok - 公安备案验收腿已登记（vitest + Playwright）" || { echo "FAIL - 公安备案验收腿缺失"; exit 1; }
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
# §P2-L（2026-10-07 波 4）：本段原来用 §20 定义的 `$dg`。纯路径变量跨段引用在收集模式下会变成
# "前段判红 ⇒ 本段读到空串 ⇒ grep 报错被判红"，那条红会被归属成本段的缺陷，而真正坏的是 §20。
# 修法选"本段自带定义"而不是 gate_need：路径变量没有任何语义依赖，省一次打字不值得换来一个 SKIP，
# 更不值得让一个绿段在前段红的时候被记成 SKIP（那会让"到底哪些段有真依赖"这件事失去唯一读数面）。
dg=scripts/deploy_guangzhou.sh
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
# ③ shell 变量名吞噬：`$VAR` 紧跟全角字符（如 `${ds}）`）时 bash 会把多字节并入变量名，
#    在 set -u 下报 unbound variable 直接中止（2026-09-20 实测：部署在「指纹校验通过」那行
#    崩溃，前端因此漏传）。全仓 12 处，已全部改为 ${VAR}；此守卫防新增写法回归。
#    注：必须用 python 扫——grep 无法可靠匹配 Unicode 区间（toybox grep 对 -P/码位静默失灵）。
python3 - <<'PY' || { echo "--- FAIL: shell 脚本存在「变量名后紧跟非 ASCII 字符」写法（bash 会吞进变量名）"; exit 1; }
import pathlib, re, sys
pat = re.compile(r'\$[A-Za-z_][A-Za-z0-9_]*(?=[^\x00-\x7F])')
bad = []
for p in sorted(list(pathlib.Path('scripts').rglob('*.sh')) + list(pathlib.Path('deploy').rglob('*.sh'))):
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
# A 静态锁①：冻结账的 SQL 必须含当日日期过滤（缺了就是 C1 原缺陷复活）
#   §STRATEGY-FIX（2026-10-06 波 1）换形说明：本批把公开口径拆成 LocalBuyFrozen /
#   LocalBuyFrozenByStrategy 两个薄壳 + 唯一实现 localBuyFrozen（两本账共用一份 SQL，防止
#   "改了全局那本、战法那本忘改"）。锚点因此从 `LocalBuyFrozen` 挪到 localBuyFrozen——
#   薄壳体内本来就没有 SQL，照旧名点这条锁会在**健康代码**上恒红（§0929DRILL-C 同族：
#   判据按想象中的代码形状写、而不是按要防的失效形态写）。日期过滤这条不变量一字未改。
grep -A6 'func (d \*DB) localBuyFrozen' internal/store/real_positions.go | grep -q "substr(created_at,1,10)=?" || { echo "--- FAIL: LocalBuyFrozen 日期过滤丢失（§C1 回归）"; exit 1; }
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
# ★ 10-10 现网首拨实录锁（读法供给侧第五犯）：nssm 往 stdout 写 UTF-16（register_engine_services.ps1
#   :214 早有记录），PS 单字节解码后是夹 NUL 的文本，对原文直接 -match 状态词恒不命中 ⇒
#   Get-NssmStatus 恒空 ⇒ 四条 NSSM 腿全误判 DOWN——同一轮日志里探针字段全是通过，健康服务
#   被连环重启 + 单实例治理杀子进程，quant 引擎被杀三次才靠退避上限停下。四枚锁钉死修法：
#   剥 NUL 先于匹配／读不出状态词落 SCM 而不是判 DOWN／坏形态（对原文匹配后直接 return）不得复活。
grep -qF "replace '\\x00', ''" deploy/qmt-win/all_service_watchdog.ps1 || { echo "--- FAIL: §H8 nssm 状态读法缺 NUL 剥离（UTF-16 输出按单字节解码，不剥 NUL 恒误判 DOWN＝10-10 首拨事故形态）"; exit 1; }
grep -qF 'SERVICE_[A-Z_]+' deploy/qmt-win/all_service_watchdog.ps1 || { echo "--- FAIL: §H8 nssm 状态读法缺「读出别的状态词＝确实没在跑」分支（剥完 NUL 只认 RUNNING 的话，STOPPED 会被当成读不出而落 SCM，真停服反而漏报）"; exit 1; }
grep -qF '$st = (Get-Service -Name $name' deploy/qmt-win/all_service_watchdog.ps1 || { echo "--- FAIL: §H8 nssm 状态读不出时的 SCM 兜底腿不在位（fail 方向必须是往 SCM 落而不是往 DOWN 判＝10-10 事故的反面）"; exit 1; }
if grep -qF 'return ($out -match "SERVICE_RUNNING")' deploy/qmt-win/all_service_watchdog.ps1; then echo "--- FAIL: §H8 坏形态复活（对夹 NUL 原文直接匹配后 return＝四腿恒 DOWN，10-10 首拨把健康服务连环重启）"; exit 1; fi
echo "ok - §H8/§F6 专项守卫通过（静态锁 12 道）"

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

echo "==> 45 §XCHECK 价格复核闸接线（CrossCheckPrice 收编，2026-09-22 C批，代理A）..."
# §XCHECK：下单守卫第 12 道闸 price_cross_check——委托参考价 vs 独立复核源价
# （DataCoordinator.CrossCheckPrice，新浪→腾讯→东财多源链）偏离超阈值即命中；
# 默认关（cross_check_pct=0）+ 默认影子（命中仅 risk_gates 留痕放行），零配置零行为变化。
# 死代码不得复活：CrossCheckPrice 必须保有生产消费者（registry 接线）。
go test -count=1 ./internal/risk/ -run 'TestGatePriceCrossCheck' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'price_cross_check' internal/risk/gate.go || { echo "--- FAIL: 价格复核闸从闸清单消失（§XCHECK）"; exit 1; }
grep -q 'checkPriceCross' internal/risk/gate.go || { echo "--- FAIL: 复核闸判定函数丢失（§XCHECK）"; exit 1; }
grep -q 'SetCrossPriceSource' internal/engine/registry.go || { echo "--- FAIL: registry 装配注入丢失（§XCHECK 接线断裂，复核源永不生效）"; exit 1; }
grep -q 'cross_check_pct' internal/config/config.go || { echo "--- FAIL: 复核闸配置键丢失（§XCHECK）"; exit 1; }
if ! grep -rn 'CrossCheckPrice' internal cmd --include='*.go' 2>/dev/null | grep -v _test.go | grep -v 'internal/data/source.go' | grep -q .; then
	echo "--- FAIL: CrossCheckPrice 又成死代码（§XCHECK 消费链断裂，§LOW(a) 死代码形态复活）"; exit 1; fi
echo "ok - §XCHECK 专项守卫通过（行为锁 1 组 6 态 + 静态锁 5 道）"

echo "==> 46 §NATIVEAUTH 登录 token 迁原生加密存储（2026-09-22 C批，代理A2/A3）..."
# token 出 WebView localStorage（明文域文件，root/adb backup 可读）→ AndroidAuth 桥
# + EncryptedSharedPreferences（AndroidKeyStore 托管主密钥）；纯浏览器/旧 APK 无桥自然回落。
( cd web && npm test -- native_auth ) 2>&1 | grep -E 'Test Files|passed|failed'
test -f mobile/app/src/main/java/com/liangzai/quant/SecureAuthStore.kt || { echo "--- FAIL: 原生加密存储实现缺失（§NATIVEAUTH）"; exit 1; }
grep -q '"AndroidAuth"' mobile/app/src/main/java/com/liangzai/quant/MainActivity.kt || { echo "--- FAIL: AndroidAuth 桥未注册（§NATIVEAUTH 前端迁而无门）"; exit 1; }
grep -q 'security-crypto' mobile/app/build.gradle.kts || { echo "--- FAIL: EncryptedSharedPreferences 依赖丢失（§NATIVEAUTH）"; exit 1; }
grep -q 'nativeAuthBridge' web/src/api/index.js || { echo "--- FAIL: 前端桥探测丢失（§NATIVEAUTH）"; exit 1; }
# 负锁：业务代码不得再直读 liangzai_token 字面量（api/index.js 存储层单点之外、滤注释行与测试）。
if grep -rn "liangzai_token" web/src --include='*.js' --include='*.jsx' 2>/dev/null | grep -v __tests__ | grep -v 'src/api/index.js' | grep -vE ':[0-9]+:[[:space:]]*//' | grep -q .; then
	echo "--- FAIL: localStorage token 直读复活（§NATIVEAUTH 必须收敛在 api 存储层）"; exit 1; fi
echo "ok - §NATIVEAUTH 专项守卫通过（行为锁 1 组 + 静态锁 4 道 + 直读负锁）"

echo "==> 47 §ROOTQMT 根级死键防回潮 + §APPVER 强制更新通道（2026-09-22 C批，代理A4/A5）..."
# §ROOTQMT：config.json 根级 qmt 死键（旧生成器层级错误产物，§M7a）——清理脚本收编进
# 部署链 [3a] 常态执行，防回潮；引擎 Save 只写 {rules,d1}，清理无回写竞态。
test -f deploy/qmt-win/clean_root_qmt.ps1 || { echo "--- FAIL: 根级 qmt 清理脚本缺失（§ROOTQMT 手工告警复活）"; exit 1; }
grep -q 'clean_root_qmt' scripts/deploy_guangzhou.sh || { echo "--- FAIL: 清理未接入部署链（§ROOTQMT 防回潮失效）"; exit 1; }
# §APPVER：公开版本端点（登录前检查必须免鉴权）+ Caddy /dl 分发块 + v2 原生更新闸。
go test -count=1 ./internal/server/ -run 'TestAppVersionPublicEndpoint' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'GET /api/app/version' internal/server/server.go || { echo "--- FAIL: 版本端点注册丢失（§APPVER）"; exit 1; }
# 负锁：该端点注册行若被 authMiddleware 包裹=登录前更新检查死锁（v1 登不进即永远收不到更新单）。
if grep -n 'GET /api/app/version' internal/server/server.go | grep -q 'authMiddleware'; then
	echo "--- FAIL: /api/app/version 被套鉴权（§APPVER 登录前检查死锁复活）"; exit 1; fi
grep -q 'handle /dl/\*' deploy/caddy/guangzhou.conf || { echo "--- FAIL: Caddy APK 分发块丢失（§APPVER 下载 404）"; exit 1; }
grep -q 'quant-latest.apk' scripts/deploy_guangzhou.sh || { echo "--- FAIL: APK 分发同步步丢失（§APPVER [3c]）"; exit 1; }
grep -q 'UpdateGate' mobile/app/src/main/java/com/liangzai/quant/MainActivity.kt || { echo "--- FAIL: 原生强制更新闸未接线（§APPVER）"; exit 1; }
grep -qE 'versionCode = ([2-9]|[1-9][0-9])' mobile/app/build.gradle.kts || { echo "--- FAIL: APK 版本仍停在无更新通道的 1（§APPVER 跃迁回退）"; exit 1; }
echo "ok - §ROOTQMT/§APPVER 专项守卫通过（行为锁 1 组 + 静态锁 7 道 + 鉴权负锁）"

echo "==> 48 §UPDLINK SSE 双关死锁根因修复 + 上行自监控（2026-09-22 PM批 H-4，P0）..."
# 根因：同一客户端 channel 被 §A3 evict 与 handleFixSSE defer 两条路径双关 → 持 b.mu 时 panic →
# 广播锁永久泄漏 → POST /api/qmt/report 必经的 BroadcastTo 全线挂死（生产实录 1h45m 静默）。
# 行为锁：幂等注销/跨分组回退/并发双关不泄漏锁/有界广播放弃/回报端点存活。
go test -count=1 ./internal/server/ -run 'TestUnsubscribeFor|TestEvictAndHandlerConcurrent|TestBroadcastToWithinGivesUp|TestQMTReportSurvivesStuckSSELock' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# 静态锁①：注销必须走"注册表命中才关"的幂等路径（无条件 close(ch) 形态即 H-4 本体，不得复活）。
# 取 UnsubscribeFor 函数体做范围断言，避免整文件计数被文档注释里的 close(ch) 字样干扰。
UNSIB=$(awk '/^func \(b \*SSEBroker\) UnsubscribeFor/{f=1} f{print} f&&/^}$/{exit}' internal/server/sse.go)
[ -n "$UNSIB" ] || { echo "--- FAIL: 找不到 UnsubscribeFor 函数体（§UPDLINK 静态锁失效）"; exit 1; }
echo "$UNSIB" | grep -q 'unregisterLocked' || { echo "--- FAIL: 幂等注销判定丢失（§UPDLINK 双关复活）"; exit 1; }
[ "$(echo "$UNSIB" | grep -c 'close(ch)')" = "1" ] || { echo "--- FAIL: UnsubscribeFor 内 close(ch) 不是唯一一处（§UPDLINK）"; exit 1; }
grep -q 'func (b \*SSEBroker) unregisterLocked' internal/server/sse.go || { echo "--- FAIL: 幂等注销实现丢失（§UPDLINK 双关复活）"; exit 1; }
# 静态锁②：UnsubscribeFor 必须 defer 解锁——锁内 panic 跳过 Unlock 是 H-4 的第二根因。
echo "$UNSIB" | grep -q 'defer b\.mu\.Unlock()' || { echo "--- FAIL: UnsubscribeFor 不再 defer 解锁（持锁 panic 会再次泄漏广播锁，§UPDLINK）"; exit 1; }
# 静态锁③：上行回报入口必须用有界广播（无界 BroadcastTo 在同族锁事故里会再次把资金账本陪葬）。
grep -q 'BroadcastToWithin' internal/server/qmt.go || { echo "--- FAIL: /api/qmt/report 退回无界广播（§UPDLINK 兜底失效）"; exit 1; }
# 静态锁④：告警规则必须有真实数据源——audit N-1 的形态就是"有规则、无 SetGauge"。
grep -q 'SetGauge("quote_staleness_sec"' internal/engine/scoring_loop.go || { echo "--- FAIL: quote_staleness_sec 又成死规则（§UPDLINK/audit N-1）"; exit 1; }
grep -q 'SetGauge("uplink_staleness_sec"' internal/engine/scoring_loop.go || { echo "--- FAIL: 上行新鲜度量规断供（H-4 那 1h45m 将再次无人知晓）"; exit 1; }
grep -q 'refreshStalenessGauges()' internal/engine/scoring_loop.go || { echo "--- FAIL: 量规喂养未接入 scoreCycle（告警永不自愈）"; exit 1; }
go test -count=1 ./internal/engine/ -run 'TestRefreshStalenessGauges|TestUplinkStaleRuleRegistered' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
echo "ok - §UPDLINK 专项守卫通过（行为锁 2 组 + 静态锁 6 道）"

echo "==> 49 §SIDEGATE 下单方向三层白名单（2026-09-22 PM批 M-1/N-4，资金安全）..."
# 缺陷原文：网关 /order 只判 `side=="卖出"`，其余任意串按买入整手校验后被 broker/桥的
# 三元式（`STOCK_BUY if side=="买入" else STOCK_SELL`）下成**卖单**；Go 侧 handleExecuteAction
# 同样只把空串缺省成买入；risk.Gate 十余道闸按 Side 精确匹配，非法串让 T+1 卖出限制、
# 涨停拒买、跌停拒卖三道方向闸同时静默跳过。方向是全部方向性守卫的判定前提，前提不可信
# 时唯一安全姿势是拒单——故 HTTP 入口 400、通道层 fail-close、风控闸入口 side_unknown 三层各拦一次。
go test -count=1 ./internal/risk/ -run 'TestGateUnknownSide' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ -run 'TestExecuteRejectsNonCanonicalSide|TestExecuteSellSideStillAccepted' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/trading/ -run 'TestPlaceOrderUnknownSideNeverReachesExecutor' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
py_tests qmt_gateway/tests/test_order_gates.py 'side' 2>&1 | grep -E "passed|failed|error|Ran [0-9]+ test|OK"
grep -q 'ORDER_SIDES = ("买入", "卖出")' qmt_gateway/gateway.py || { echo "--- FAIL: 网关方向白名单常量丢失（§SIDEGATE-PY）"; exit 1; }
grep -q 'def is_valid_side' qmt_gateway/broker.py || { echo "--- FAIL: 通道层方向校验丢失（§SIDEGATE-PY 第二道闸）"; exit 1; }
grep -q 'side != trading.SideBuy && side != trading.SideSell' internal/server/qmt.go || { echo "--- FAIL: 手动单 HTTP 入口白名单丢失（§SIDEGATE-GO）"; exit 1; }
grep -q 'o.Side != SideBuy && o.Side != SideSell' internal/risk/gate.go || { echo "--- FAIL: 风控闸方向 fail-close 丢失（§N-4）"; exit 1; }
grep -q 'side_unknown' internal/risk/gate.go || { echo "--- FAIL: 未知方向留痕标识丢失（§N-4 命中不可归因）"; exit 1; }
# 负向锁：非法方向绝不许"缺省成买入"继续往下走（滤注释行，只拦真代码形态）。
if grep -nE '^\s*side = trading\.SideBuy\s*$' internal/server/qmt.go | grep -vE ':[0-9]+:\s*//' | grep -q .; then
	if ! grep -q 'side != trading.SideBuy && side != trading.SideSell' internal/server/qmt.go; then
		echo "--- FAIL: 手动单方向又只剩空串缺省（§SIDEGATE-GO 白名单被绕开）"; exit 1; fi; fi
echo "ok - §SIDEGATE 专项守卫通过（行为锁 4 组 + 静态锁 5 道 + 缺省回退负锁）"

echo "==> 50 §SETTLE 日终结算失败当日可重试 + §M13 熔断广播载荷 + §D5 注释对齐（2026-09-22 PM批）..."
# 旧实现把 `c.lastSettleDay = day` 放在 SettleDay **之前**，一次网关超时即永久烧掉当日唯一一次
# 三方对账（失败分支只 log+计指标、无补偿路径）；现改为「成功才记账 + 10 分钟节流重试 + 当日
# 失败次数进 opslog 与 settle_fail_streak 量规」。顺序断言比文本断言可靠：置位行必须在调用之后。
go test -count=1 ./internal/trading/ -run 'TestSettleFailure' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# 注意过滤注释行：§D4 注释里引用了「旧实现把 c.lastSettleDay = day 放在调用之前」的缺陷原文，
# 不过滤会命中注释行造成顺序假红（负向/顺序锁须滤注释——本仓既有教训）。
SET_ASSIGN=$(grep -n 'c.lastSettleDay = day' internal/trading/settlement.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
SET_CALL=$(grep -n ':= c.SettleDay(' internal/trading/settlement.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
# 锚写法说明（2026-10-06 波 5 §P2-E 同步）：这一句原本点着「diff, err :=」两个返回值，而本波给
# SettleDay 加了第三个返回值（SettleOutcome），行首整串一变、锚就找不到＝顺序锁报「找不到置位/调用行」
# 直接假红（-collect 首轮实录）。顺序锁要钉的是**赋值与调用的先后**，不是调用的返回值清单，
# 所以锚只取「:= c.SettleDay(」这一段——将来再加返回值也不会误伤，而把置位挪回调用之前照样判红。
[ -n "$SET_ASSIGN" ] && [ -n "$SET_CALL" ] || { echo "--- FAIL: 找不到结算置位/调用行（§D4 静态锁失效）"; exit 1; }
[ "$SET_ASSIGN" -gt "$SET_CALL" ] || { echo "--- FAIL: lastSettleDay 又回到 SettleDay 之前置位（失败当日永久不再对账，§D4 复活）"; exit 1; }
grep -q 'settleRetryInterval' internal/trading/settlement.go || { echo "--- FAIL: 失败重试节流窗丢失（§D4 会打爆网关或不再重试）"; exit 1; }
grep -q 'c.settleFailCount++' internal/trading/settlement.go || { echo "--- FAIL: 当日失败计数丢失（§D4 连续失败不可数）"; exit 1; }
grep -q 'metrics.SetGauge("settle_fail_streak"' internal/trading/settlement.go || { echo "--- FAIL: settle_fail_streak 量规断供（§N-1 死规则形态复活）"; exit 1; }
grep -q '"settle_failed"' internal/metrics/alerter.go || { echo "--- FAIL: settle_failed 告警规则丢失（§D4）"; exit 1; }
# §M13：熔断状态必须随 qmt_report 广播带出（前端只在字段存在时才更新徽标）。
grep -q '"tripped": ctrl != nil && ctrl.Tripped()' internal/server/qmt.go || { echo "--- FAIL: qmt_report 载荷熔断字段丢失（§M13 徽标会被无关回报瞬清）"; exit 1; }
go test -count=1 ./internal/server/ -run 'TestQMTReportBroadcastCarriesTripped' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# §D5：注释与实现对齐后的命名不得回退（tripReasonLocked 自称不加锁却自己 RLock=自锁死锁陷阱）。
grep -q 'func (c \*Controller) currentTripReason()' internal/trading/controller.go || { echo "--- FAIL: currentTripReason 改名回退（§D5）"; exit 1; }
if grep -n 'tripReasonLocked' internal/trading/controller.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: tripReasonLocked 真代码复活（§D5 命名/注释漂移陷阱）"; exit 1; fi
echo "ok - §SETTLE/§M13/§D5 专项守卫通过（行为锁 2 组 + 顺序断言 + 静态锁 6 道 + 命名负锁）"

echo "==> 51 §UX-TRUTH 前端错误呈现层与移动壳推送定向（2026-09-22 PM批 H-2/M-6/M-9/M-11/M-13/N-2/N-3）..."
# 主题=「失败绝不伪装成空态/成功」：保存失败要回滚+真 toast、脏余额不进 localStorage、
# 403 止血必须覆盖在飞轮询链尾、四个页面的首屏失败要有错误态、推送别名按账号派生、
# 空 apk_url 不能只剩「退出」。
# §0927AUDIT 门禁自修（2026-09-28 实录）：旧写法把 vitest 直接管道进 grep，pipefail 下
# 真红时脚本**裸死**——只留下被 grep 过滤后的汇总行，连 "--- FAIL" 归属都没有（本轮
# 51 段首跑即撞冷启动假红，排查全靠人工复现）。改为先收全文再判红，✕ 行随段输出。
# §0927AUDIT 二修（2026-09-28 凌晨，同段连续两轮门禁内假红 × 门禁外必绿）：
#  ① --no-file-parallelism：三文件各拖一份 TDesign+React 重组件，并行 worker 冷 transform
#     同时挤满核心时，用例内 5s 级 DOM 轮询被拖挂出假红（三例独立/冷缓存复跑全绿，红项
#     只在门禁全量语境复现＝负载产物）；串行跑只动调度不动任何断言阈值（D5 教训：不放宽
#     阈值来掩盖基建脆弱）。
#  ② 判红时改输出「Failed Tests」全段（旧 ✕ 行筛选看不到 AssertionError/Unable to find 正文，
#     两次假红都只能靠人工复现定位，违反"红项当场可读"排障纪律）。
UX_VITEST=$( cd web && npm test -- h2_positions_balance m9_error_tri_state m13_forbidden_poll --no-file-parallelism 2>&1 || true )
printf '%s\n' "$UX_VITEST" | grep -E 'Test Files|Tests +[0-9]' || true
if printf '%s\n' "$UX_VITEST" | /usr/bin/grep -qE '[1-9][0-9]* (failed|error)'; then
	echo "--- FAIL: §51 §UX-TRUTH 前端行为锁（h2/m9/m13 三文件）判红，失败全段如下："
	printf '%s\n' "$UX_VITEST" | sed -n '/Failed Tests/,$p' | head -80
	exit 1
fi
grep -q "import { showToast } from '../ui.jsx'" web/src/pages/Positions.jsx || { echo "--- FAIL: showToast 导入再次缺失（H-2 失败分支 ReferenceError 复活）"; exit 1; }
grep -q 'balancePendingRef' web/src/pages/Positions.jsx || { echo "--- FAIL: 脏余额不入缓存守卫丢失（H-2 第三腿）"; exit 1; }
grep -q 'pollingDeadRef' web/src/pages/Quant.jsx || { echo "--- FAIL: 在飞轮询链止血标志丢失（M-6：stopPolling 管不住链尾 /api/risk/gates）"; exit 1; }
grep -q 'noteForbidden(e)' web/src/pages/Quant.jsx || { echo "--- FAIL: 挂载探测又吞 403（M-6 第二半）"; exit 1; }
grep -q 'QUANT_POLL_EP' web/e2e/uat_full.spec.mjs || { echo "--- FAIL: MP-3 用例端点集又混入壳层轮询（N-2 假红/假绿源）"; exit 1; }
grep -q 'pushAliasFor' mobile/app/src/main/java/com/liangzai/quant/MainActivity.kt || { echo "--- FAIL: 推送别名不再按账号派生（M-11 广播面复活）"; exit 1; }
grep -q 'alias_desired' mobile/app/src/main/java/com/liangzai/quant/MainActivity.kt || { echo "--- FAIL: 别名对账幂等键丢失（M-11 换号竞态）"; exit 1; }
grep -q '版本更新提醒' mobile/app/src/main/java/com/liangzai/quant/UpdateGate.kt || { echo "--- FAIL: 空 apk_url 无软提示兜底（N-3 只剩「退出」）"; exit 1; }
echo "ok - §UX-TRUTH 专项守卫通过（行为锁 1 组 + 静态锁 8 道）"

echo "==> 52 §MONEYGATE 资金三态 fail-close + 撤单零成交可重放（2026-09-22 PM批 M-12/H-1，owner 裁决 A）..."
# M-12：旧两态口径把「真 0」与「口径不可得」折叠成同一个 0——H-4 实录证明两个方向都是事故
# （冻结碎钱→全拦当日买入；冻结值恰 0→无资金约束放行）。三态化后消费端必须显式判 fresh。
# H-1：已撤+零成交的保护性卖单旧口径下同幂等键整天 duplicate 猝死，放行集合须含该支。
go test -count=1 ./internal/engine/ -run 'TestAutoPlaceCashThreeStates' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/trading/ -run 'TestAvailableCashThreeStates' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/store/ -run 'TestResetCancelledZeroFillReplayable|TestResetSendFailedStillReplayable' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'cash, cashFresh := ctrl.AvailableCash()' internal/engine/engine.go || { echo "--- FAIL: 自动买入腿未走三态消费形态（§M12）"; exit 1; }
grep -q 'if !cashFresh {' internal/engine/engine.go || { echo "--- FAIL: 资金口径不可得的 fail-close 分支丢失（§M12）"; exit 1; }
grep -q 'json:"cash_stale"' internal/trading/controller.go || { echo "--- FAIL: /api/qmt/state 的 cash_stale 暴露丢失（§M12 前端降级横幅数据源断供）"; exit 1; }
grep -q 'state.cash_stale' web/src/pages/Quant.jsx || { echo "--- FAIL: 前端资金口径不可得降级横幅丢失（§M12）"; exit 1; }
# §H1-MG 放行集须同时覆盖「发送失败」与「已撤+零成交」，且成交判定走 fills 相关子查询（与 SumFilledQty 同前缀口径）。
grep -q "status='发送失败'" internal/store/real_positions.go || { echo "--- FAIL: 发送失败可重放腿丢失（§GAP2-W1 回归）"; exit 1; }
grep -q "status='已撤' AND NOT EXISTS" internal/store/real_positions.go || { echo "--- FAIL: 已撤零成交可重放腿丢失（§H1-MG 撤单猝死复活）"; exit 1; }
# 负向锁（滤注释行，只拦真代码形态）：旧两态消费 `if cash := ctrl.AvailableCash(); cash > 0` 不得复活。
if grep -nE 'if cash := ctrl\.AvailableCash\(\); cash > 0' internal/engine/engine.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: 旧两态资金消费形态复活（真 0 又被当成不设限，§M12 复活）"; exit 1; fi
echo "ok - §MONEYGATE 专项守卫通过（行为锁 3 组 + 静态锁 6 道 + 两态复活负锁）"

echo "==> 53 §CONTRACT 回报契约双向锁 + 财务报告期字段 + SSE 续传（2026-09-22 PM批 M-4/N-5/M-5）..."
# 主题=「契约的两条腿都要有人看」：M-4 旧 golden 只有 Go→网关单向，网关常年发的
# trade_id/name/created_at 在 Go 信封无 tag、被 encoding/json 静默丢弃（丢腿）且 fills
# 复合唯一键把同秒两笔真部成判成重放；N-5 FinancialData 没有报告期字段、财务新鲜度无从
# 判定且查库错误被当成"没有财报"静默吞掉；M-5 票据消费即废让原生重连必然 401、手动重建
# 又收不到续读位置，补发环两条路都不可达。
# ── M-4 行为锁：Go 侧丢腿回归 + 契约文档 + Python 侧 emitted==golden ──
go test -count=1 ./internal/server/ -run 'TestReportContractGolden|TestReportTradeNameLegPersists|TestReportTradeIDAnchorsPartialFills|TestReportOrderCreatedAtLeg|TestReportEnvelopeDecodesAllEmittedLegs' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
py_tests qmt_gateway/tests/test_report_contract.py 2>&1 | grep -E "passed|failed|error|Ran [0-9]+ test|OK"
# ── M-4 静态锁：信封补 tag、fills 判重键两段化、幂等去重按 trade_id 优先 ──
grep -q 'json:"trade_id"' internal/server/qmt.go || { echo "--- FAIL: qmtReportEvent 的 trade_id tag 丢失（网关成交编号又被静默丢弃，§M4 丢腿复活）"; exit 1; }
grep -q 'json:"created_at"' internal/server/qmt.go || { echo "--- FAIL: 委托创建时间 created_at tag 丢失（§M4 丢腿复活）"; exit 1; }
grep -q 'ALTER TABLE fills ADD COLUMN trade_id' internal/store/store.go || { echo "--- FAIL: fills.trade_id 迁移丢失（§M4）"; exit 1; }
grep -q 'idx_fills_trade ON fills(trade_id) WHERE' internal/store/store.go || { echo "--- FAIL: trade_id 部分唯一索引丢失（§M4 判重锚）"; exit 1; }
grep -q 'idx_fills_idem_notid' internal/store/store.go || { echo "--- FAIL: 无编号行的复合键部分索引丢失（§M4 旧行保护）"; exit 1; }
grep -q 'DROP INDEX IF EXISTS idx_fills_idem' internal/store/store.go || { echo "--- FAIL: 旧复合唯一索引未拆除（同秒两笔真部成又会 500，§M4 复活）"; exit 1; }
grep -q 'WHERE trade_id=?' internal/store/real_positions.go || { echo "--- FAIL: ApplyRealFill 的 trade_id 优先判重腿丢失（§M4）"; exit 1; }
grep -q '"gateway_emitted_fields"' qmt_gateway/contract/report_fields.json || { echo "--- FAIL: 契约金标又退回单向（只锁 Go→网关，不锁网关→Go，§M4 根因）"; exit 1; }
# ── N-5 行为锁 + 静态锁：报告期字段存在、查库错误不再当"没有财报" ──
go test -count=1 ./cmd/quant/ -run 'TestFinaCache' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'EndDate string' internal/strategy_engine/types.go || { echo "--- FAIL: FinancialData 报告期字段丢失（M-7 新鲜度闸又没有可判之物）"; exit 1; }
grep -q 'AnnDate string' internal/strategy_engine/types.go || { echo "--- FAIL: FinancialData 披露日字段丢失（§N-5）"; exit 1; }
grep -q 'COALESCE(ann_date' internal/store/store.go || { echo "--- FAIL: FinaHistory 对 NULL ann_date 的 COALESCE 丢失（一个空值即令整查询报错、财务因子全 0，§N-5 根因复活）"; exit 1; }
# 负向锁（滤注释行）：fina_cache 旧「err == nil && len(rows) > 0」把查库错误折叠成"没有财报"的写法不得复活。
if grep -n 'err == nil && len(rows) > 0' cmd/quant/fina_cache.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: fina_cache 又用单一条件吞掉查库错误（§N-5 静默因子丢失复活）"; exit 1; fi
# ── M-5 行为锁：TTL 内可复用 + query 续传 + e2e 建链 ──
go test -count=1 ./internal/server/ -run 'TestSSETicketReusableWithinTTL|TestSSEQueryLastEventIDReplaysRing' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/e2e/ -run 'TestHTTPSSeTicket' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
( cd web && npm test -- sse_h6_reconnect sse_ticket ) 2>&1 | grep -E 'Test Files|passed|failed'
# ── M-5 静态锁：服务端收 query、票据不作废、前端重建带续读位置 ──
grep -q 'Query().Get("last_event_id")' internal/server/handlers_fix.go || { echo "--- FAIL: SSE 不再收 ?last_event_id= query（手动重建路径补发环又不可达，§M5 复活）"; exit 1; }
grep -q 'func (s \*Server) useSSETicket' internal/server/sse.go || { echo "--- FAIL: useSSETicket 改名回退（票据又回到消费即废语义，§M5 复活）"; exit 1; }
grep -q 'last_event_id=' web/src/api/index.js || { echo "--- FAIL: 前端重建 URL 不带续读位置（§M5 前端腿丢失）"; exit 1; }
# 负向锁（滤注释行）：消费即废的旧函数形态不得复活（改名即报警）。
if grep -rn 'consumeSSETicket' internal/ --include='*.go' | grep -vE ':[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: consumeSSETicket 真代码复活（§M5 语义回退）"; exit 1; fi
echo "ok - §CONTRACT 专项守卫通过（行为锁 4 组 + 静态锁 14 道 + 吞错/作废负锁 2 道）"

echo "==> 54 §H3 打分链日K复权优先 + 不复权拒参与 + 腾讯静默回退拒收（2026-09-22 PM批 H-3）..."
# 旧链 新浪(不复权)第一、只判 len>0、腾讯 qfqday 缺失静默拿不复权 day 冒充前复权——
# 除权日 MA/动量/止损价系统性失真（全系统 qfq 契约的漏网链，§D6 收口后剩余那条）。
# 现（2026-09-23 §KLINE-CHAIN-3 换序后）：腾讯(仅 qfq)→东财(qfq)→库内日K 三条复权腿依次优先，
# 每源过 ValidateKLine；新浪/同花顺只作**带标记**的末位兜底，不复权序列不进 md.KLines（因子战法自然拒参与）。
# 本条只锁"复权优先于不复权"这条底线；链的完整顺序与库内腿守卫由 82 专锁。
go test -count=1 ./internal/strategy_engine/ -run 'TestFetchDayKLine|TestApplyDayKLine|TestFetchMinuteKLine' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/data/ -run 'TestGetTencentKLineRefusesUnadjustedFallback|TestParseTencent' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# 顺序锁（比文本断言可靠）：fetchDayKLine 内东财 qfq 腿必须排在新浪不复权腿之前。
H3_QFQ=$(grep -n 'GetKLine(code, "101", 120)' internal/strategy_engine/engine.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
H3_UNADJ=$(grep -n 'GetSinaKLine(code, 120)' internal/strategy_engine/engine.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
[ -n "$H3_QFQ" ] && [ -n "$H3_UNADJ" ] || { echo "--- FAIL: 找不到复权/不复权腿（§H3 静态锁失效）"; exit 1; }
[ "$H3_QFQ" -lt "$H3_UNADJ" ] || { echo "--- FAIL: 日K链又是不复权优先（除权日因子失真复活，§H3）"; exit 1; }
# 每源校验闸：fetchDayKLine 的四条腿都要过 ValidateKLine（旧实现只判 len>0）。
H3_VALIDATE=$(grep -c 'err == nil && data.ValidateKLine(klines)' internal/strategy_engine/engine.go || true)
[ "$H3_VALIDATE" -ge 4 ] || { echo "--- FAIL: ValidateKLine 闸数量 $H3_VALIDATE < 4（有腿退回只判 len>0，§H3/§D8 复活）"; exit 1; }
grep -q 'KLineUnadj  *bool' internal/strategy_engine/types.go || { echo "--- FAIL: StockMarketData 不复权标记字段丢失（§H3 拒参与不可见）"; exit 1; }
grep -q 'dayk-unadjusted-fallback' internal/strategy_engine/engine.go || { echo "--- FAIL: 复权链降级的 opslog 告警丢失（§H3 降级不可观测）"; exit 1; }
# 负向锁（滤注释行）：① 腾讯 qfqday 缺失静默回退不复权；② 日K不经 applyDayKLine 闸门直写。
if grep -n 'rows = stk.Day' internal/data/tencent.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: 腾讯无 qfqday 又静默回退不复权 day（冒充前复权，§H3 旁支复活）"; exit 1; fi
if grep -n 'md.KLines = e\.fetchDayKLine\|md.KLines, md.MoneyFlow = e\.cachedKLine' internal/strategy_engine/engine.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: 日K又绕过 applyDayKLine 直写 KLines（不复权兜底会静默进因子计算，§H3 复活）"; exit 1; fi
echo "ok - §H3 专项守卫通过（行为锁 2 组 + 顺序断言 + 静态锁 3 道 + 负锁 2 道）"

echo "==> 55 §ROBUST 财务新鲜度停用闸 + 降级报成功族留痕/非零退出 + outbox 单写者（2026-09-22 PM批 M-7/M-8/N-6/N-7）..."
# M-7：研究库 fina_indicator 断更时打分照用半年前财报且无人知晓 → 报告期滞后 >240 天停用（按缺失计入）。
# M-8/N-6：dataload 估值/财务两同步与 research 逐窗装配、sector_agent 成分股验证，旧实现吞错仍报
#          "完成/验证 N"且 exit 0——失败计数>0 一律降级文案，CLI 侧非零退出，库侧留痕降级行。
# N-7：outbox saveLocked 每次变更各起一个写协程并发 AtomicWrite 同一文件（Windows rename 互相踩踏
#      Access is denied）+ 旧快照可能后写覆盖新快照 → 收敛为单写者 + 最新快照胜出 + Stop 终刷。
go test -count=1 ./cmd/quant/ -run 'TestFina' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/notify/ -run 'TestOutbox' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/research/ -run 'TestNoteWindowFailTrace' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# M-7 静态锁：闸必须被 Lookup 实际调用（定义了不调用=死代码假修复）+ 告警键在位。
# §B7-PIT（2026-09-26）把 240 这个数收进 strategy_engine.FinaStaleMaxDays 单源（实盘/回放共用
# 同一谓词），cmd/quant 只留别名引用——阈值判红随迁：数只准出现在 fina_pit.go，别名只准引用。
grep -q 'const FinaStaleMaxDays = 240' internal/strategy_engine/fina_pit.go || { echo "--- FAIL: §M-7 阈值常量丢失（单源家 fina_pit.go）"; exit 1; }
grep -q 'const finaStaleMaxDays = strategy_engine.FinaStaleMaxDays' cmd/quant/fina_cache.go || { echo "--- FAIL: §M-7 实盘侧未走单源别名（要么常量回潜要么引用断线）"; exit 1; }
grep -q 'finaReportStale(fina' cmd/quant/fina_cache.go || { echo "--- FAIL: §M-7 新鲜度闸未被 Lookup 调用（定义了个寂寞）"; exit 1; }
grep -q 'fina-stale-report' cmd/quant/fina_cache.go || { echo "--- FAIL: §M-7 停用告警 opslog 键丢失"; exit 1; }
if grep -n 'YYYY-MM-DD，如 2026-06-30' internal/strategy_engine/types.go | grep -q .; then
	echo "--- FAIL: FinancialData 报告期注释又写回 YYYY-MM-DD（研究库实际 YYYYMMDD，M-7 解析口径会被误导）"; exit 1; fi
# M-8/N-6 CLI：两同步函数必须返回 error 且分发点转成非零退出（log.Fatalf）。
grep -q 'func cmdHithinkSyncValuations(client \*data.HithinkClient, db \*store.DB) error {' cmd/dataload/hithink_sync.go || { echo "--- FAIL: 估值同步又无返回值（批次失败无法非零退出，§M-8 复活）"; exit 1; }
grep -q 'func cmdHithinkSyncFinIndicators(client \*data.HithinkClient, db \*store.DB, args \[\]string) error {' cmd/dataload/hithink_sync.go || { echo "--- FAIL: 财务指标同步又无返回值（§M-8/N-6 复活）"; exit 1; }
H_ERR=$(grep -c 'if err := cmdHithinkSync\(Valuations\|FinIndicators\)' cmd/dataload/hithink_sync.go || true)
[ "$H_ERR" -ge 2 ] || { echo "--- FAIL: 分发点吃 err 的非零退出腿 $H_ERR < 2（§M-8 降级错误又被吞）"; exit 1; }
# M-8/N-6 research：五处逐窗装配失败计数 + 统一降级行；ckpt save 双吞（_ =）必须绝迹。
R_FAIL=$(grep -c 'failed++' internal/research/windowed.go || true)
[ "$R_FAIL" -ge 5 ] || { echo "--- FAIL: 窗口装配失败计数点 $R_FAIL < 5（有腿又静默 continue，§N-6 复活）"; exit 1; }
grep -q 'func noteWindowFail' internal/research/windowed.go || { echo "--- FAIL: 缺窗降级留痕函数丢失（§N-6）"; exit 1; }
if grep -nE '_ = c\.db\.PutWindowCkpt' internal/research/windowed.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: 断点落库又 \`_ =\` 吞错（整晚断点没存上不可见，§N-6 复活）"; exit 1; fi
# M-8/N-6 sector_agent：成分股验证失败计数 + 降级文案。
S_FAIL=$(grep -c 'verifyFailed' internal/sector_agent/agent.go || true)
[ "$S_FAIL" -ge 3 ] || { echo "--- FAIL: 板块成分股验证失败计数点 $S_FAIL < 3（又零留痕报「验证 N 个板块」，§M-8 复活）"; exit 1; }
# N-7：单写者三要素——flushPending 存在、pending 快照最新胜出、saveWG.Add 先于 go o.loop（顺序锁）。
grep -q 'func (o \*Outbox) flushPending' internal/notify/outbox.go || { echo "--- FAIL: outbox 单写者 flushPending 丢失（§N-7 复活）"; exit 1; }
grep -q 'o.pending = items' internal/notify/outbox.go || { echo "--- FAIL: 最新快照胜出登记丢失（旧快照可后写覆盖新状态，§N-7）"; exit 1; }
N7_ADD=$(grep -n 'o\.saveWG\.Add(1)' internal/notify/outbox.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
N7_GO=$(grep -n 'go o\.loop(stopCh)' internal/notify/outbox.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
[ -n "$N7_ADD" ] && [ -n "$N7_GO" ] || { echo "--- FAIL: saveWG/go loop 锚点丢失（§N-7 顺序锁失效）"; exit 1; }
[ "$N7_ADD" -lt "$N7_GO" ] || { echo "--- FAIL: WaitGroup 计数又落在 loop 内（Stop 的 Wait 可在 Add 前返回，§N-7 -race 实录）"; exit 1; }
# 负锁：每次变更各起一个写协程（并发 rename 互踩的根形）必须绝迹。
if grep -nE 'go func\(path string, items \[\]outboxPersistItem\)' internal/notify/outbox.go | grep -q .; then
	echo "--- FAIL: saveLocked 又按变更各起写协程（Windows 并发 AtomicWrite Access denied 根因，§N-7 复活）"; exit 1; fi
echo "ok - §ROBUST 专项守卫通过（行为锁 2 组 + 静态锁 9 道 + 顺序断言 + 负锁 3 道）"

echo "==> 56 §清扫批 写端点收权普查 + C6 幂等键同源 + C9 ntfy 通道短路 + A5 真日历 + notify-test 实探（2026-09-22 PM批 M-14/C6/C9/A5）..."
# M-14：/api/news/test-attribution 从成员可写收权 admin；防回潮升级为全量普查锁——
# C6：买入幂等键与 signalctl 准入探针同源（StrategyKeyOf），杜绝「探针合、幂等键分」双单敞口。
# C9：ntfy 通道整体宕机时逐条吃 5s 超时+逐条入补投（风暴放大），且通道自哑无人知——
#     现连续 3 败开短路窗 + 经站内通道自监控播报。A5：网关时段判定接 Go 落盘真交易日历。
go test -count=1 ./internal/server/ -run 'TestWriteEndpointsAllGated|TestH3' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/notify/ -run 'TestNtfyBreaker|TestPushGatewayShortCircuitSkipsEnqueue|TestAlertChannelDown' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/engine/ -run 'TestAutoPlaceIdempotencyKey' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
py_tests qmt_gateway/tests/test_trading_calendar.py
# M-14 静态锁：test-attribution 必须挂 adminMiddleware（负锁：authMiddleware 形态绝迹）。
grep -E 'test-attribution.*adminMiddleware' internal/server/server.go >/dev/null || { echo "--- FAIL: §M-14 test-attribution 收权丢失（成员又可无限烧 LLM Stage2）"; exit 1; }
if grep -qE '"POST /api/news/test-attribution", s\.authMiddleware' internal/server/server.go; then
	echo "--- FAIL: §M-14 又回退成 authMiddleware（普通成员可越权写全局信号簿）"; exit 1; fi
# C6 静态锁：买入幂等键战法分量必须由 StrategyKeyOf 单点派生；旧 StrategyID?:Strategy 分叉形态绝迹。
grep -q 'stratKey := signalctl.StrategyKeyOf(sig)' internal/engine/engine.go || { echo "--- FAIL: §C6 幂等键又绕开 StrategyKeyOf（与准入探针分叉复活）"; exit 1; }
if grep -nE 'if sig\.StrategyID != "" \{[[:space:]]*stratKey' internal/engine/engine.go | grep -q .; then
	echo "--- FAIL: §C6 旧键派生（StrategyID 优先回退显示名）复活"; exit 1; fi
# C9 静态锁：短路三要素在位（哨兵错误 / 阈值常量 / 自监控播报走站内两路）。
grep -q 'ErrNtfyShortCircuit = ' internal/notify/ntfy.go || { echo "--- FAIL: §C9 短路哨兵错误丢失"; exit 1; }
grep -q 'alertChannelDown' internal/notify/notify.go || { echo "--- FAIL: §C9 通道自监控播报丢失（报丧鸟哑了没人知道）"; exit 1; }
grep -q 'errors.Is(err, ErrNtfyShortCircuit)' internal/notify/gateway.go || { echo "--- FAIL: §C9 短路窗内仍逐条入补投队列（风暴放大复活）"; exit 1; }
# notify-test 实探：空 stub（只 log 就回 ok）不得复活。
if grep -A 3 'func (s \*Server) handleFixNotifyTest' internal/server/handlers_fix.go | grep -q 'writeJSON(w, 200, map\[string\]string{"status": "ok"})$'; then
	echo "--- FAIL: /api/notify-test 又回退成永远 ok 的空 stub（假反馈，§F-3 同族）"; exit 1; fi
# A5 静态锁：网关两处时段判定都必须消费 trading_calendar；旧「仅 weekday」裸启发式绝迹（注释行除外）。
grep -q 'from trading_calendar import' qmt_gateway/handler.py || { echo "--- FAIL: handler.py 又断开真日历接线（§A5 节假日误判复活）"; exit 1; }
grep -q 'from trading_calendar import' qmt_gateway/qmt_bridge.py || { echo "--- FAIL: qmt_bridge.py 又断开真日历接线（§A5）"; exit 1; }
if grep -nE '^    if now\.weekday\(\) >= 5:$' qmt_gateway/qmt_bridge.py | grep -vE '^[0-9]+:\s*#' | grep -q .; then
	echo "--- FAIL: qmt_bridge 工作日启发式又做主判定（应只在无日历兜底分支）"; exit 1; fi
grep -q 'closed_days' qmt_gateway/trading_calendar.py || { echo "--- FAIL: 交易日历读取模块丢失（§A5）"; exit 1; }
# §A5 部署清单锁（09-21 qmt_bridge_strategy 漏列同族教训）：日历模块必须随 [2b] 下发，
# 否则现网网关 ImportError 静默降级 weekday 启发式——修复形同虚设且无任何报错。
grep -q 'qmt_gateway/trading_calendar.py' scripts/deploy_guangzhou.sh || { echo "--- FAIL: 部署 [2b] 清单缺 trading_calendar.py（§A5 现网不会生效）"; exit 1; }
# M-10 行为锁：交错轮询的迟到响应整包丢弃（工具语义 3 例 + Signals 整页交错回归 1 例）。
( cd web && npm test -- m10_stale_guard )
# M-10 静态锁：守卫工具在位；三个轮询页均 import createStaleGuard 且真正 begin/isStale（漏一页=该页倒挂复活）。
grep -q 'export function createStaleGuard' web/src/utils/staleGuard.js || { echo "--- FAIL: §M-10 staleGuard 工具丢失"; exit 1; }
for f in Dashboard Signals Positions; do
	grep -q "import { createStaleGuard }" "web/src/pages/$f.jsx" || { echo "--- FAIL: §M-10 $f 页未接入陈旧守卫（交错覆盖复活）"; exit 1; }
	grep -q 'isStale(' "web/src/pages/$f.jsx" || { echo "--- FAIL: §M-10 $f 页只建守卫不用（begin/isStale 半接线）"; exit 1; }
done
echo "ok - §清扫批 专项守卫通过（行为锁 5 组 + 静态锁 12 道 + 负锁 4 道）"

# ══════════════════════════════════════════════════════════════════════════════
# 57~68：2026-09-22 傍晚/夜间审计批（AUDIT_REPORT_20260922EVE + FIX_PLAN_20260922EVE）
# 编号说明：FIX_PLAN 施工时把本节占位写作「锁 57~66」，实际落地为 57~68 共 12 节。
# English: sections 57-68 lock the 2026-09-22 evening batch fixes (P0-A/B/C, N-1..N-8, 高-3).
# ══════════════════════════════════════════════════════════════════════════════

echo "==> 57 §ADJ 后复权因子前向填充 + 数据源路由唯一入口（傍晚批 P0-A）..."
# 现象：adj_factor 是【事件稀疏】表（只在分红实施日有行），HfqBars 却按「因子日==行情日」等值
# LEFT JOIN → 非除权日全部落空 → COALESCE 兜成 1 → 后复权价退化为不复权价，回测/因子/图表全链路
# 失真且零报错。路由开关（PrimarySourceThsDaily/ThsFactorsReady）曾是包级导出变量，装配点只有
# cmd/quant ⇒ researchd/dataload/replay/backtest/research 各自按默认值（旧表）跑，同一份数据
# 两条口径。修法：前向填充子查询（与 LegacyAdjFactorAt 语义同源）+ 开关收私有、唯一入口装配。
go test -count=1 ./internal/store/ -run 'TestHfqBarsAdj|TestConfigureSourceSingleEntry' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'ORDER BY a.trade_date DESC LIMIT 1), 1) AS adj' internal/store/store.go || { echo "--- FAIL: §P0-A 因子前向填充子查询丢失（后复权又退化成不复权）"; exit 1; }
# 负锁：baostock 侧 daily×adj_factor 的等值 JOIN 绝迹。ths 侧（ths_adj_factor 日累计全覆盖表）
# 等值语义正确，由 allow-legacy-adj-join-eq 显式豁免——把两个同名 JOIN 一起判红会造出永久性假红。
if grep -qE 'FROM daily d LEFT JOIN adj_factor a ON a\.ts_code=d\.ts_code AND a\.trade_date=d\.trade_date' internal/store/store.go; then
	echo "--- FAIL: §P0-A 旧等值 JOIN 复活（除权日之外因子恒为 1）"; exit 1; fi
[ "$(grep -c 'trade_date=b\.trade_date' internal/store/store.go)" -eq 1 ] || { echo "--- FAIL: 等值因子 JOIN 处数≠1（ths 日累计表那一处之外又冒出一处），§P0-A"; exit 1; }
# 路由唯一入口：导出变量形态绝迹 + 任何进程不得直改 + 装配点覆盖 8 个入口文件。
if grep -rqE '^var (PrimarySourceThsDaily|ThsFactorsReady) ' internal/store/*.go; then
	echo "--- FAIL: 路由开关又被导出成包级变量（cmd 可绕过唯一入口裸赋值，§P0-A）"; exit 1; fi
if grep -rn 'store\.PrimarySourceThsDaily\|store\.ThsFactorsReady' --include='*.go' cmd internal 2>/dev/null | grep -q .; then
	echo "--- FAIL: 又出现 store.PrimarySourceThsDaily 直改（§P0-A 唯一入口失效）"; exit 1; fi
[ "$(grep -rlE 'store\.ConfigureSource' --include='*.go' cmd internal | wc -l | tr -d ' ')" -ge 8 ] || { echo "--- FAIL: 路由装配点 < 8 个文件（有进程又走默认旧表口径）"; exit 1; }
echo "ok - §ADJ 专项守卫通过（行为锁 3 例 + 静态锁 5 道 + 负锁 3 道）"

echo "==> 58 §PARTFILL 部成在途按未成交余量占额（傍晚批 N-3）..."
# 现象：卖出「剩余量 = 持仓 − Σ已成交 − 在途量」里的在途量按**整笔委托量**计，部成 500/1000 的
# 单子既进了 Σ已成交、又整笔留在在途里 = 同一段成交被扣两次 → 补卖量被压成 0/半量，该退的仓位
# 留过夜（止损单尤其致命）。修法：在途按净额（qty − 该单 fills 之和）计，成交归属 order_id 或
# signal_id 双键（order_id 回填失败时行仍是 pend: 前缀，只按 order_id 关联恒得 0 → 又退化整笔）。
go test -count=1 ./internal/engine/ -run 'TestN3' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/store/ -run 'TestSumOpenSellQty|TestRealPosition' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'COALESCE((SELECT SUM(f.qty) FROM fills f' internal/store/real_positions.go || { echo "--- FAIL: §N-3 在途净额子查询丢失（又按整笔委托量占额，部成单被双扣）"; exit 1; }
# 双键归属（order_id OR signal_id）必须在位：只按 order_id 是本缺陷的隐蔽半态。
grep -q "OR (o.signal_id <> '' AND f.signal_id = o.signal_id)" internal/store/real_positions.go || { echo "--- FAIL: §N-3 成交归属又只认 order_id（pend: 占位行恒得 0 成交）"; exit 1; }
# 净额下限钳 0：负在途量会把剩余量抬高 → 超卖敞口。
grep -q 'if remain := qty - filled; remain > 0' internal/store/real_positions.go || { echo "--- FAIL: §N-3 负在途量钳 0 丢失（filled>qty 异常行会抬高剩余量）"; exit 1; }
# 查询失败必须留痕（本函数是卖出剩余量与 T+1 可卖量两道闸的共同输入，静默回 0 = 两道闸同盲）。
grep -q '§N-3 在途卖量查询失败' internal/store/real_positions.go || { echo "--- FAIL: §N-3 fail-open 又静默（降级不得无痕迹）"; exit 1; }
echo "ok - §PARTFILL 专项守卫通过（行为锁 2 组 + 静态锁 4 道）"

echo "==> 59 §COSTBASIS 对账不得用不含费成本覆盖本地含费账（傍晚批 N-6）..."
# 现象：柜台/网关快照的 cost_price 是**不含手续费**口径（券商摊薄算法另算），旧 Reconcile 无条件
# 用快照值裸写本地 cost_price/amount → 本地含费成本被洗成不含费，且**字段缺失时把成本清零并永久
# 落库**；下游 ProfitPct 从虚低成本起算 → 止损/止盈判定线整体错位（资金安全，非显示问题）。
# 裁决 11：本地含费基准优先；快照仅在本地为 0 时回填；丢弃必须留痕（CostGuardDrops + loud log）。
go test -count=1 ./internal/trading/ -run 'TestN6' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
[ "$(grep -c 'cost_price=CASE WHEN real_positions.cost_price > 0' internal/store/real_positions.go)" -ge 2 ] || { echo "--- FAIL: §N-6 成本守卫只在一处生效（另一条对账写路径又裸覆盖）"; exit 1; }
[ "$(grep -c 'amount=(CASE WHEN real_positions.cost_price > 0' internal/store/real_positions.go)" -ge 2 ] || { echo "--- FAIL: §N-6 amount 未与成本同源（数量×新成本，账实自相矛盾）"; exit 1; }
grep -q 'func (d \*DB) CostGuardDrops()' internal/store/real_positions.go || { echo "--- FAIL: §N-6 丢弃计数丢失（静默保护也算静默失效）"; exit 1; }
# 负锁：快照成本直写形态（SET cost_price=?）绝迹。
if grep -nE 'SET[[:space:]]+cost_price=\?[[:space:]]*,?[[:space:]]*amount=\?' internal/store/real_positions.go | grep -q .; then
	echo "--- FAIL: §N-6 又出现 cost_price=? 裸写（快照不含费值覆盖本地含费账）"; exit 1; fi
echo "ok - §COSTBASIS 专项守卫通过（行为锁 2 例 + 静态锁 3 道 + 负锁 1 道）"

echo "==> 60 §LIVEANCHOR 移动止盈锚点回写落账（傍晚批 N-7）..."
# 现象：移动止盈锚点（持仓期最高价）只活在 signalctl 内存态，实盘三本账（positions/paper/anchors）
# 不记 ⇒ 引擎重启即把锚点退回「建仓价」，回撤容忍度被重置为满格——重启后一波正常回撤直接触发
# 卖出，或该止盈的票永不止盈。修法：裁决时回读 ctl.SellHighAnchor 写回本地持仓，回写失败只留痕
# 不阻断裁决（宁可锚点滞后，不可交易停摆）。
go test -count=1 ./internal/engine/ -run 'TestN7' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'func (c \*Controller) SellHighAnchor(' internal/signalctl/sell.go || { echo "--- FAIL: §N-7 锚点回读接口丢失（内存态无处落地）"; exit 1; }
grep -q 'ctl.SellHighAnchor(signalctl.ChannelLive' internal/engine/sell_anchor.go || { echo "--- FAIL: §N-7 实盘锚点回写未被调用（定义了个寂寞）"; exit 1; }
echo "ok - §LIVEANCHOR 专项守卫通过（行为锁 2 例 + 静态锁 2 道）"

echo "==> 61 §CLAIMRELEASE 占位释放不得拆掉幂等防线 + 测试静音卫生（傍晚批 N-8）..."
# 现象：桥通道「入队即成功」，随后 ids.settle 抛错时 finally 无条件 _release_order_claim 删占位
# = 把 orders.signal_id UNIQUE 这条**唯一**幂等防线拆掉；调用方重试再次 claim 成功，而 dispatch
# 表当时没有 signal_id 约束 → 同信号第二笔入队 = 双卖/双买。修法：未交给通道才删占位（§M-2 原
# 语义保留），已交给通道转第三态「待核对」+ 同 sid 一律 409 + error 告警 + 超时清理不碰第三态，
# 收敛出口＝回报到达或 POST /admin/order-confirm；dispatch 侧另有部分唯一索引做 DB 层纵深。
py_tests qmt_gateway/tests/test_claim_release.py
grep -q 'UNRESOLVED_STATUS = "待核对"' qmt_gateway/store.py || { echo "--- FAIL: §N-8 第三态常量丢失（又回到删占位）"; exit 1; }
grep -q 'def release_unresolved_pending' qmt_gateway/store.py || { echo "--- FAIL: §N-8 人工收敛出口丢失（待核对成为死态）"; exit 1; }
grep -q "_ensure_dispatch_signal_guard" qmt_gateway/store.py || { echo "--- FAIL: §N-8 dispatch signal_id 纵深唯一索引丢失"; exit 1; }
grep -q 'def _alert_unresolved_pending' qmt_gateway/gateway.py || { echo "--- FAIL: §N-8 待核对告警丢失（挂起态无人知晓）"; exit 1; }
# 负锁：超时清理只认 status='pending'，不得把第三态当陈旧占位删掉（删了就等于回到旧缺陷）。
if grep -nE "DELETE FROM orders WHERE status[[:space:]]+IN[[:space:]]*\([^)]*待核对" qmt_gateway/store.py | grep -q .; then
	echo "--- FAIL: §N-8 超时清理又把「待核对」当陈旧占位删除"; exit 1; fi
# 测试卫生锁（本批真实踩坑）：同目录别的模块在 import 期 logging.disable(CRITICAL)，pytest 单进程
# 收集后 assertLogs 会假阴性——**凡是断言日志的测试类必须逐个继承 _LogCaptureMixin**，
# 并按「日志断言处数」核对（只数类数会放过「整类一条断言都没挂 mixin」的形态）。
LOG_ASSERTS=$(grep -cE 'self\.assertLogs\(' qmt_gateway/tests/test_claim_release.py || true)
MIXED_CLASSES=$(grep -cE '^class Test[A-Za-z0-9_]*\(_LogCaptureMixin\)' qmt_gateway/tests/test_claim_release.py || true)
BARE_LOG=$(awk '/^class Test/{inh=($0 ~ /_LogCaptureMixin/)} /self\.assertLogs\(/{if (!inh) n++} END{print n+0}' qmt_gateway/tests/test_claim_release.py)
[ "$LOG_ASSERTS" -gt 0 ] && [ "$MIXED_CLASSES" -gt 0 ] || { echo "--- FAIL: §N-8 日志卫生 mixin 或断言丢失（$MIXED_CLASSES/${LOG_ASSERTS}）"; exit 1; }
[ "$BARE_LOG" -eq 0 ] || { echo "--- FAIL: §N-8 有 $BARE_LOG 处 assertLogs 挂在未继承 _LogCaptureMixin 的类里（全局静音下会假绿）"; exit 1; }
echo "ok - §CLAIMRELEASE 专项守卫通过（行为锁 1 套 + 静态锁 4 道 + 负锁 1 道 + 卫生锁 1 道）"

echo "==> 62 §DISCIPLINE 延持态终失明止血 + 重评估非对称守卫（傍晚批 P0-C 走 B）..."
# 现象：纪律引擎一旦置位 Settled&&!Confirmed（延持），下一轮直接 early-return 不再出卡，
# 价格继续跌破更深一档线也视而不见 = 终态失明（该走的仓位永远不走）。修法：延持态每轮重评估，
# 出卡需「本轮仍破线且不轻于原始锁定线」（reevalAllowsSettle）；反向情形（止损延持后反弹进止盈
# 区）一律不收卡，避免把失明换成「按反弹后的止盈价挂止损标签卖」。
go test -count=1 ./internal/trading/ -run 'TestDiscipline' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'func reevalAllowsSettle(' internal/trading/discipline.go || { echo "--- FAIL: §P0-C 重评估准入判据丢失"; exit 1; }
grep -q 'if extendHold && !reevalAllowsSettle(st.Line, line)' internal/trading/discipline.go || { echo "--- FAIL: §P0-C 准入判据未被调用（定义了个寂寞）"; exit 1; }
grep -q 'func isLossLine(' internal/trading/discipline.go || { echo "--- FAIL: §P0-C 损失族判定丢失（止盈延持跌进损失线无法识别）"; exit 1; }
# 负锁：延持态无条件 early-return 的旧形态绝迹（同族教训：只在注释里说改过、代码没改）。
if grep -nE 'if st\.Settled && !st\.Confirmed \{[[:space:]]*return' internal/trading/discipline.go | grep -q .; then
	echo "--- FAIL: §P0-C 延持态又无条件 early-return（终态失明复活）"; exit 1; fi
echo "ok - §DISCIPLINE 专项守卫通过（行为锁 3 例 + 静态锁 3 道 + 负锁 1 道）"

echo "==> 63 §ALERTROUTE 指标型告警出口接线（傍晚批 高-3 收窄版）..."
# 现象：内部指标告警（9 条规则）只写内存/日志，生产无任何推送出口 = 等价于没有告警；
# 同时缺「推给谁、多久推一次、恢复通知配对」的显式路由。修法：AlertRoute(push/daily/log) +
# 冷却（P1 30min / 其余 10min）+ 日切汇总 + SetAlertSink 注入；未注入 sink 时 loud warn
# （告警系统自己哑了必须吵）。范围按 owner 裁决收窄：只接指标型，事件型不动。
go test -count=1 ./internal/metrics/ -run 'TestPushRule|TestResolved|TestDailySummary|TestUnwiredSink|TestSinkReceives|TestRoutingCovers|TestRunAlertEvaluation|TestConfigureAlertRouting' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -race -count=1 ./internal/metrics/ 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q 'func DefaultAlertRouting()' internal/metrics/alert_routing.go || { echo "--- FAIL: §高-3 默认路由表丢失（规则无出口）"; exit 1; }
grep -q 'metrics.SetAlertSink(' cmd/quant/main.go || { echo "--- FAIL: §高-3 生产进程未注入 sink（路由表成为死码）"; exit 1; }
grep -q 'AlertSinkInjected()' internal/metrics/alert_routing.go || { echo "--- FAIL: §高-3 未接线自检丢失"; exit 1; }
echo "ok - §ALERTROUTE 专项守卫通过（行为锁 8 例 + -race + 静态锁 3 道）"

echo "==> 64 §CFGSMASH 战法参数稀疏 merge + 版本戳 + 并发加锁（傍晚批 N-4/中-6）..."
# 现象（三重叠加，缺一不至于丢参数）：① 前端加载失败被 catch 吞 → 表单落在空对象；
# ② 数字字段 `?? 0` 把「键缺失」渲染成 0（缺失与真实 0 混同）；③ 后端把 body 反序列化成完整
# StrategyConfig 后**全量替换**落盘 ⇒ 一次「加载失败 + 保存」即把五套战法阈值清零、重启救不回。
# 另：GetStrategyConfig 返回内部指针、Set 系无锁写 map，与打分/热更新并发（-race 已复现）。
# 修法：逐字段 JSON 递归稀疏 merge（没传=保留旧值，要清 0 请明写 0）+ updated_at 乐观锁 409
# + 全部 getter 改快照拷贝、setter 加锁 + 前端缺失渲空并保存前必填校验。
go test -count=1 ./internal/config/ -run 'TestMergeStrategyConfig|TestSetStrategyConfig|TestStrategyConfigSaveWhileScoringRace' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -race -count=1 ./internal/config/ 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ -run 'TestSetStrategyConfig' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
( cd web && npm test -- n4_cfg_smash )
grep -q 'func (m \*Manager) MergeStrategyConfig(patch map\[string\]json.RawMessage' internal/config/config.go || { echo "--- FAIL: §N-4 稀疏 merge 入口签名变更（回到整 struct 反序列化=缺键即清零）"; exit 1; }
grep -q 'var ErrStrategyVersionConflict' internal/config/config.go || { echo "--- FAIL: §中-6 乐观锁哨兵错误丢失"; exit 1; }
[ "$(grep -c 'next.UpdatedAt = time.Now().UTC()' internal/config/config.go)" -ge 3 ] || { echo "--- FAIL: 版本戳推进点 < 3 处（有写路径不刷新 updated_at，409 形同虚设）"; exit 1; }
# 负锁 1：handler 又整份反序列化到 StrategyConfig（全量替换形态）——函数体内必须仍有 RawMessage 稀疏 merge。
if ! grep -q 'json.RawMessage' <(sed -n '/func (s \*Server) handleSetStrategyConfig/,/^}/p' internal/server/server.go); then
	echo "--- FAIL: §N-4 handleSetStrategyConfig 不再走稀疏 merge（缺键清零复活）"; exit 1; fi
# 负锁 2：前端 renderField 的 `?? 0` 兜底绝迹（缺失渲染成 0 是本缺陷的第二重）。
if grep -nE '\?\? 0[[:space:]]*\}[[:space:]]*$|value=\{[^}]*\?\? 0\}' web/src/pages/Settings.jsx | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: §N-4 前端又用 ?? 0 渲染缺失字段（空表单被提交成全 0）"; exit 1; fi
echo "ok - §CFGSMASH 专项守卫通过（行为锁 4 组含 -race + 静态锁 3 道 + 负锁 2 道）"

echo "==> 65 §NOTIFYADMIN 全局推送探测端点抬档 + 频控（傍晚批 N-2）..."
# 现象：/api/notify-test 在 §C9 从空 stub 升级为**逐通道实弹探测**（消息级 LevelHigh），
# 但档位仍是 authMiddleware ⇒ 任何登录成员一次 POST 就能向 owner 的全部推送通道发实弹，
# 用噪声淹没真告警（告警通道本身成为攻击面）。修法：抬 adminMiddleware + 全进程 60s 最小间隔
# （探测打的是 server 级单例通道，按账号限流挡不住多管理员合流）+ 全路径 opslog 审计。
go test -count=1 ./internal/server/ -run 'TestNotifyTestAdminOnlyAndRateLimited' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
grep -q '"POST /api/notify-test", s.adminMiddleware' internal/server/server.go || { echo "--- FAIL: §N-2 notify-test 抬档丢失（成员可轰炸 owner 推送通道）"; exit 1; }
if grep -q '"POST /api/notify-test", s.authMiddleware' internal/server/server.go; then
	echo "--- FAIL: §N-2 notify-test 又回退 authMiddleware"; exit 1; fi
grep -q 'Retry-After' internal/server/handlers_fix.go || { echo "--- FAIL: §N-2 频控未回 Retry-After（429 无语义，调用方盲重试）"; exit 1; }
# 空 stub 绝迹（§C9 的实探升级不得被回退掉）。
if grep -A 3 'func (s \*Server) handleFixNotifyTest' internal/server/handlers_fix.go | grep -q 'writeJSON(w, 200, map\[string\]string{"status": "ok"})$'; then
	echo "--- FAIL: §N-2 notify-test 又回退成永远 ok 的空 stub"; exit 1; fi
echo "ok - §NOTIFYADMIN 专项守卫通过（行为锁 1 组 + 静态锁 3 道 + 负锁 2 道）"

echo "==> 66 §LINTGATE 前端 no-undef 静态门禁（傍晚批 N-1）..."
# 现象：Dashboard.jsx 轮询回调写 setQMTState（声明是 setQmtState）→ ReferenceError 被同函数
# 空 catch 吞掉 → qmtState 恒 null → 「实盘链路」健康指示永不渲染，且 15s 轮询每 tick 静默抛
# 一次。类型检查不覆盖 .jsx、vitest 未渲染该卡片 ⇒ 只有静态 lint 能抓，而仓库没有 lint 门。
# 取舍（owner 裁决 7）：最小集起步——no-undef 锁 error（运行时炸弹），no-unused-vars 降 warn
# （存量 104 条多为无害死码，首日判红会让门禁失去可用性）。
grep -q "'no-undef': 'error'" web/eslint.config.js || { echo "--- FAIL: §N-1 no-undef 未锁 error（同类拼写错误又能静默上线）"; exit 1; }
grep -q '"lint": "eslint src"' web/package.json || { echo "--- FAIL: §N-1 lint 脚本丢失（门禁无从挂起）"; exit 1; }
grep -q 'npm run lint -- --quiet' .github/workflows/ci.yml || { echo "--- FAIL: §N-1 CI 未跑 lint（本地门禁不约束合并）"; exit 1; }
# 行为锁：实盘链路卡片真的渲染出来（改名回归即刻可见）。
( cd web && npm test -- n1_qmt_link )
if grep -n 'setQMTState' web/src/pages/Dashboard.jsx | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: §N-1 又出现 setQMTState 实调用（未声明符号，被空 catch 吞掉）"; exit 1; fi
( cd web && npm run lint -- --quiet ) || { echo "--- FAIL: §N-1 eslint --quiet 判红（只允许 error 阻塞）"; exit 1; }
echo "ok - §LINTGATE 专项守卫通过（静态锁 4 道 + 行为锁 1 组 + lint 实跑）"

echo "==> 67 §NSSMENV 服务运行环境缺键即部署判失败（傍晚批 N-5）..."
# 现象（三重叠加）：① -LLMApiKey/-HithinkApiKey 默认 ""；② 只在值非空时追加对应键（条件追加）；
# ③ nssm set AppEnvironmentExtra 是**整体替换**语义 ⇒ 任何一次不带密钥参数的重跑（改端口/修故障/
# 二次部署）都会静默删掉上次注入的 LLM_*/HITHINK_*，而且照打 "engine services registered"。
# RUNBOOK 甚至把「注册脚本不要顺手重跑」写成运维纪律——那是用规矩绕脚本缺陷。
# 修法：密钥解析优先级（显式参数 > 磁盘密钥文件 > 机器级环境变量 > 内置默认）+ 写前读回现值做
# **并集**（未被提及的键原样保留 ⇒ 从根上取消「少传一个参数=删一个变量」这条路径）+ 尾部按**键名**
# 断言、缺键 Warn + exit 1。**全程只打印键名，任何路径不得回显密钥值。**
grep -q 'function Set-ServiceEnvExtra' deploy/qmt-win/register_engine_services.ps1 || { echo "--- FAIL: §N-5 服务 env 唯一写入点丢失（并集语义退化为整体替换）"; exit 1; }
grep -q 'function Get-ExistingEnvExtra' deploy/qmt-win/register_engine_services.ps1 || { echo "--- FAIL: §N-5 写前读回现值丢失（并集无从谈起）"; exit 1; }
grep -q 'function Read-SecretFile' deploy/qmt-win/register_engine_services.ps1 || { echo "--- FAIL: §N-5 磁盘密钥文件解析丢失（缺省即跳过复活）"; exit 1; }
# 语句位置（行首缩进后直接调用）才是真实写入点：函数定义行与注释里的提及都不算。
[ "$(grep -cE '^[[:space:]]*Set-ServiceEnvExtra ' deploy/qmt-win/register_engine_services.ps1)" -ge 2 ] || { echo "--- FAIL: Set-ServiceEnvExtra 实调用点 < 2（quant 之外的注册路径绕过并集写入）"; exit 1; }
# 负锁：AppEnvironmentExtra 的裸 set 必须只剩 Set-ServiceEnvExtra 内部那一条实现点
# （注释里出现的「旧版裸 nssm set」说明文字按 # 起行排除）。
RAW_SET=$(grep -nE 'nssm[[:space:]]+(set)[[:space:]]+\$?[a-zA-Z"]*[[:space:]]*AppEnvironmentExtra' deploy/qmt-win/register_engine_services.ps1 | grep -vE '^[0-9]+:[[:space:]]*#' | wc -l | tr -d ' ' || true)
[ "$RAW_SET" -eq 1 ] || { echo "--- FAIL: 裸 nssm set AppEnvironmentExtra 出现 $RAW_SET 处（期望仅函数内 1 处，§N-5）"; exit 1; }
# 部署面独立复核（校验面不得依附施工面，§M7 同族教训）：第 15 号探针 + 只看键名。
grep -q 'quant env HITHINK key + LLM source' scripts/verify_deploy_guangzhou.sh || { echo "--- FAIL: §N-5 部署后键名复核探针丢失"; exit 1; }
# ⚠ 口径修正锁（2026-09-23 部署实录：这条探针首跑把自己判红）：LLM_* 三元组**不得**当硬 env 键要求。
#   LLM 的权威源是设置页保存（auth.json per-account 配置项，internal/llmcfg/llmcfg.go 解析链
#   ①设置页>②env>…），env 只是 bootstrap；把 ② 写成必需键 = 永久性假红，还会诱使操作人为了
#   凑绿把密钥再抄一份进 env/密钥文件（扩大泄露面）。两侧断言都必须承认「auth.json 已保存」这条路。
grep -q 'function Test-LlmSavedInAuthJson' deploy/qmt-win/register_engine_services.ps1 || { echo "--- FAIL: §N-5 LLM 权威源判定丢失（注册步会把 bootstrap-only 的 env 当硬要求）"; exit 1; }
grep -q "llm_api_keys" scripts/verify_deploy_guangzhou.sh || { echo "--- FAIL: §N-5 探针不再认 auth.json 已保存密钥（LLM 假红复活，部署后验证将永久红）"; exit 1; }
# 负锁：硬必需键清单里不得再出现 LLM_API_KEY（HITHINK 才是 env-only 的真硬键）。
if grep -qE '^\s*"(quant|quant-research)"[[:space:]]*=[[:space:]]*@\(.*LLM_API_KEY.*\)' deploy/qmt-win/register_engine_services.ps1; then
	echo "--- FAIL: §N-5 LLM_API_KEY 又被写回硬必需键清单（§UI-AUTHORITATIVE 假红复活）"; exit 1; fi
if grep -qE '^\$envNeed = @\(.*LLM_API_KEY' scripts/verify_deploy_guangzhou.sh; then
	echo "--- FAIL: §N-5 探针 envNeed 又含 LLM_API_KEY（同上）"; exit 1; fi
if grep -nE 'Get-BaseEnvExtra|AppEnvironmentExtra' scripts/verify_deploy_guangzhou.sh | grep -qE 'Write-Output.*\$raw|echo.*\$l\b'; then
	echo "--- FAIL: §N-5 探针疑似回显环境变量值（密钥明文泄露按事故处理）"; exit 1; fi
# ── 09-23 08:2x 现网实录补的两道锁（本批自曝：注册步把**自己刚写进去的键**读成不存在）──
# 现象：全量部署 [4/5] 尾部断言 Warn「quant 缺 QUANT_DATA_DIR / QUANT_ADDR / HITHINK」→ exit 1，
#       部署在 [5/5] 健康检查之前就中止（[6/6] 更没跑到），而同一天 verify 的第 15 探针判绿。
# 根因：两侧共用同一个错误解析 `("$raw" | Out-String) -split "\`r?\`n"`——Out-String 会按控制台
#       宽度折行（无主机 120 列）且 REG_MULTI_SZ 以 NUL 分隔不保证有换行，于是 nssm 的 UTF-16 输出
#       里只有落在行首的键能被 `^KEY=` 认出（实录只剩 TZ）。探针之所以绿：它只要求 HITHINK 一个键，
#       而 HITHINK 由**机器级环境变量**兜住了 ⇒ 解析缺陷被完全掩盖，注册步却拿它判红。
# 前置：①必需键集合两侧同源（否则一侧独绿＝假象），②解析不得经 Out-String（负锁，注释里的反面
#       说明按 # 起行排除——本仓「负向 grep 命中自曝注释」已复犯多次）。
regQ=$(grep -E '^[[:space:]]*"quant"[[:space:]]*=' deploy/qmt-win/register_engine_services.ps1 | grep -oE '"[A-Z][A-Z_0-9]*"' | tr -d '"' | LC_ALL=C sort -u | tr '\n' ',' || true)
verQ=$(grep -E '^\$envNeed = @\(' scripts/verify_deploy_guangzhou.sh | grep -oE '"[A-Z][A-Z_0-9]*"' | tr -d '"' | LC_ALL=C sort -u | tr '\n' ',' || true)
[ -n "$regQ" ] && [ -n "$verQ" ] \
	|| { echo "--- FAIL: §N-5 必需键清单写法变更（同源锁取不到数：注册=${regQ:-∅} 探针=${verQ:-∅}）"; exit 1; }
[ "$regQ" = "$verQ" ] \
	|| { echo "--- FAIL: §N-5 必需键集合漂移 注册步=[$regQ] 探针=[$verQ]（一侧独绿＝另一侧的解析缺陷无人看见）"; exit 1; }
if grep -nE '\|[[:space:]]*Out-String' deploy/qmt-win/register_engine_services.ps1 scripts/verify_deploy_guangzhou.sh \
	| grep -vE ':[0-9]+:[[:space:]]*#' | grep -q .; then
	echo "--- FAIL: §N-5 服务 env 解析又经 Out-String（按 120 列折行 + NUL 不换行 ⇒ 键凭空消失）"; exit 1; fi
# 读法必须是**注册表直读**（HKLM Services\<svc> 的 AppEnvironmentExtra，原生 string[]）：
# 09-23 第一次修复改成"按 换行|NUL 双重切分"仍错——nssm stdout 是 UTF-16，PS 按 OEM 码页解码后
# 每个 ASCII 字符后都跟一个 NUL，按 NUL 切分＝把每个字符劈开，一个键都认不出（探针实测只剩机器级
# 兜底的 HITHINK）。凡"解析子进程的控制台文本"都在重犯同一族错误，所以直接钉住注册表口径。
for f in deploy/qmt-win/register_engine_services.ps1 scripts/verify_deploy_guangzhou.sh; do
	grep -qE 'GetValue\(.AppEnvironmentExtra' "$f" \
		|| { echo "--- FAIL: $f 的服务 env 不再是注册表直读（回到解析 nssm 控制台文本＝UTF-16/折行两坑复犯）"; exit 1; }
done
# 负锁：按 NUL 切分文本的"第一版修复"形态不得复活（注释里的反面说明按 # 起行排除）。
if grep -nE -- "-split \"\[\`r\`n\`0\]" deploy/qmt-win/register_engine_services.ps1 scripts/verify_deploy_guangzhou.sh \
	| grep -vE ':[0-9]+:[[:space:]]*#' | grep -q .; then
	echo "--- FAIL: 又用 NUL 当分隔符切分 nssm 文本输出（UTF-16 解码残留会让每字符被劈开，键全丢）"; exit 1; fi
echo "ok - §NSSMENV 专项守卫通过（静态锁 4 道 + 负锁 5 道 + 探针锁 3 道 + 必需键同源等值锁 1 + 注册表直读锁 2 + 读源披露锁 1，含 §N-5 LLM 来源口径锁）"
# 探针必须自报"这次是从哪儿读到的"（registry / nssm-text / registry-unavailable）与读到几个键：
# 判绿但读的是空气（keys=0 且靠机器级兜住）正是本次两侧结论相反的直接成因。
grep -qF 'read=" + $envReadVia + " keys=" + $haveKeys.Count' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: §N-5 探针不再披露读取来源/键数（判绿与判红同样不可解释）"; exit 1; }

echo "==> 68 §LIVEBACKUP 广州灾备纳入 live.db + accounts（跨机集合逐相等，傍晚批 P0-B）..."
# 现象：live.db（实盘持仓/委托/成交/资产四本账，cmd/quant 独立打开）**此前没有任何一份灾备方案
# 覆盖它**——Mac 侧 scripts/backup.sh 有，广州侧 backup_snapshot.ps1 只快照 trading.db；
# 而广州是唯一的实盘执行机。广州盘坏 = 实盘账本全损且无补救。accounts/（per-user 模拟盘账本 +
# 移动止盈锚点 + 当日信号留痕）同理：Mac 有、广州没有。
# 铁律一：禁止把 sqlite3 backup API 换成裸 cp/Copy-Item（两库都是 WAL 且引擎在写，裸拷贝会得到
# 大小正常、能打开、账本却错位的撕裂快照）。铁律二：备份对象集合两侧必须逐相等（本节即该锁）。
# 铁律三：缺库即失败并写 ok:false，不得静默跳过（降级不得报成功）。
# 跨机集合逐相等（铁律二）：两行的库集合必须字面一致，任一侧增删库都要同步改这里。
grep -q 'for DB in trading.db live.db' scripts/backup.sh || { echo "--- FAIL: §P0-B Mac 侧库集合锚点变更（锁与 $DbItems 需同步）"; exit 1; }
grep -q '\$DbItems = @("trading.db", "live.db")' deploy/qmt-win/backup_snapshot.ps1 || { echo "--- FAIL: §P0-B 广州侧库集合与 Mac 不逐相等（有一库无人备）"; exit 1; }
grep -q '\$AccountsDirName = "accounts"' deploy/qmt-win/backup_snapshot.ps1 || { echo "--- FAIL: §P0-B 广州侧 accounts/ 目录未纳入快照（per-user 账本+锚点全丢且无报错）"; exit 1; }
grep -q 'cp -r "${DATA_DIR}/accounts"' scripts/backup.sh || { echo "--- FAIL: §P0-B Mac 侧 accounts 锚点变更（锁需同步）"; exit 1; }
grep -qE 'dbs[[:space:]]*=[[:space:]]*\$dbBytes' deploy/qmt-win/backup_snapshot.ps1 || { echo "--- FAIL: §P0-B SNAPSHOT_OK 未记录逐库字节数（产物侧无法复核「哪几个库真被快照」）"; exit 1; }
# 铁律三：源库缺失必须 throw（静默跳过 = 当晚少备一个库而产物仍标 ok）。
grep -q 'source db missing' deploy/qmt-win/backup_snapshot.ps1 || { echo "--- FAIL: §P0-B 缺库硬失败腿丢失（live.db 缺失又静默报成功）"; exit 1; }
grep -qE 'ok[[:space:]]*=[[:space:]]*\$false' deploy/qmt-win/backup_snapshot.ps1 || { echo "--- FAIL: §P0-B 失败分支不再写 ok:false（Mac 拉取器读不到降级信号）"; exit 1; }
# 铁律一：两库快照必须经 backup_snap.py（SQLite backup API），裸拷贝形态绝迹。
if grep -nE 'Copy-Item.*(trading|live)\.db' deploy/qmt-win/backup_snapshot.ps1 | grep -vE ':[[:space:]]*#' | grep -q .; then
	echo "--- FAIL: §P0-B 又用 Copy-Item 拷库（WAL 撕裂快照，账本错位且能正常打开）"; exit 1; fi
# 恢复演练必须认识两种产物布局（用 Mac 结构验广州产物 = 自己验自己，全绿而广州其实没这些文件）。
grep -q 'detect_artifact_layout' scripts/restore_drill.sh || { echo "--- FAIL: §P0-B 恢复演练产物布局分支丢失（跨机假绿复活）"; exit 1; }
[ -x deploy/mac/verify_restore.sh ] || [ -f deploy/mac/verify_restore.sh ] || { echo "--- FAIL: §P0-B restic 恢复复核脚本丢失"; exit 1; }
python3 -m py_compile qmt_gateway/../deploy/qmt-win/backup_snap.py && echo "  ok backup_snap.py 语法通过"
echo "ok - §LIVEBACKUP 专项守卫通过（等值锁 2 组 + 静态锁 7 道 + 负锁 2 道 + 语法锁 1 道）"

echo "==> 69 §DEADGAUGE 每条告警规则必须有真实赋值点（死规则通用守卫）..."
# 现象：9 条默认告警规则里有 3 条（order_fail_rate / settlement_diff / llm_cooldown）自 09-15
#       注册以来全仓找不到一处 SetGauge 赋值 → 评估器每轮读到 0 → gt 规则永不触发。这不是"没出过
#       事"，是"出了事也不会响"：p1「交割单对账出现差异」「下单失败率>5%」在现网等价于不存在。
#       同一形态此前已被抓过两次（audit N-1 的 quote_staleness_sec、§CB 的 uplink_staleness_sec），
#       每次都靠人肉发现——本段把它变成机器锁。
# 修法：① 三条各接真实源（order_rate.go 用 §R4-9 既有累计计数器做 5 分钟窗增量换算、
#         settlement.go 用三方对账三类差异条数之和、scoring_loop 用 llm.Client.KeysInCooldown）；
#       ② 通用守卫：从规则表反解出每条 Metric 名，逐条要求非测试代码里存在 SetGauge("<名>") 赋值点，
#          以后新增"只有规则没有数据源"直接判红；③ 负锁锁住两个已知假绿形态。
# 行为锁：三条出口 + 键名一致性 + 窗口换算。用例名单点定义（go test 与下面的点名自检共用同一个串；
# 抄成两本的结局就是刚踩过的形态——改了跑测那一本、点名那一本继续认旧名字，锁自己空转还报 ok）。
RUN69='TestSettleDayFeedsDiffGauge|TestSettleDaySkipBranchesZeroDiffButNotVerified'
go test -count=1 ./internal/trading/ -run "$RUN69" 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# 行为腿不许空转（2026-10-08 波 5 实锤）：上面这条 `-run` 原来点的是 TestSettleDaySkipBranchesWriteZero，
# 而本批把这个用例改名成 …ZeroDiffButNotVerified（口径改判见用例头注释）。名字一失效，
# `go test -run` 打印的是 `ok … [no tests to run]`，而本段只 grep `^(--- FAIL|FAIL|ok)` ⇒ 恒绿，
# 于是"跳过腿量规"这条行为锁在门禁里**一行代码都没跑过**，却以 ok 的形态存在了一整轮。
# 修法不是把 grep 改严（真红同样会被 `[no tests to run]` 的 ok 掩护），而是逐个要求 `-run` 里的
# 用例名在测试文件里真以 `func Test<名>(` 存在——改名、删用例、把用例搬去没登记的文件都当场现形。
DEAD69_N=$(printf '%s\n' "$RUN69" | tr '|' '\n' | grep -c . || true)
[ "${DEAD69_N:-0}" = "2" ] || { echo "--- FAIL: §DEADGAUGE 行为腿清单派生为空/过短（读到 ${DEAD69_N}，应为 2＝点名自检自己失明了）"; exit 1; }
DEAD69=""
for _t in $(printf '%s\n' "$RUN69" | tr '|' '\n'); do
	grep -qE "^func ${_t}\(" internal/trading/settlement_gauge_test.go || DEAD69="${DEAD69} ${_t}"
done
[ -z "$DEAD69" ] || { echo "--- FAIL: §DEADGAUGE 行为腿点名失效（用例不存在，go test 会打 ok [no tests to run] 骗过本段）：${DEAD69}"; exit 1; }
go test -count=1 ./internal/metrics/ -run 'TestOrderFailRate|TestRunAlertEvaluationRefreshesDerivedGauge' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/llm/ -run 'TestKeysInCooldown' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/engine/ -run 'TestRefreshFeedsLLMCooldownGauge|TestRefreshStalenessGauges' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# 静态锁：三条赋值点的具体形态（不只看"有没有"，还看"算得对不对"）。
grep -q 'SetGauge("settlement_diff_count", int64(len(diff.MissingInLocal)+len(diff.ExtraInLocal)+len(diff.Mismatch)))' internal/trading/settlement.go \
	|| { echo "--- FAIL: §DEADGAUGE 交割差异量规不再等于三类条数之和（退化成布尔/单类计数会漏报）"; exit 1; }
grep -q 'func (c \*Client) KeysInCooldown() int' internal/llm/llm.go \
	|| { echo "--- FAIL: §DEADGAUGE LLM 冷却计数入口丢失（llm_cooldown 又成死规则）"; exit 1; }
grep -q 'metrics.SetGauge("llm_cooldown_count", llmCool)' internal/engine/scoring_loop.go \
	|| { echo "--- FAIL: §DEADGAUGE 打分链未再喂 llm_cooldown_count"; exit 1; }
grep -q 'SetGauge("order_fail_rate_milli", rate)' internal/metrics/order_rate.go \
	|| { echo "--- FAIL: §DEADGAUGE 下单失败率量规赋值丢失"; exit 1; }
# 通用守卫：规则表里每条 Metric 都要有赋值点。扫描面排除测试文件（测试直接 SetGauge 造场景，
# 算赋值点就是假绿）与规则/路由定义本体（alerter.go 里的 Metric: "x" 不是赋值，但防有人把
# 赋值塞进规则文件糊弄守卫）。
rule_metrics=$(grep -oE 'Metric: "[a-z0-9_]+"' internal/metrics/alerter.go | sed -E 's/.*"([^"]+)"/\1/' || true)
[ -n "$rule_metrics" ] || { echo "--- FAIL: §DEADGAUGE 规则表解析为空（规则被搬走 = 守卫失明）"; exit 1; }
dead_rules=""
for m in $rule_metrics; do
	n=$(find internal cmd -name '*.go' ! -name '*_test.go' ! -name 'alerter.go' ! -name 'alert_routing.go' \
	    -print0 | xargs -0 grep -l "SetGauge(\"$m\"" 2>/dev/null | wc -l | tr -d ' ' || true)
	[ "$n" -ge 1 ] || dead_rules="$dead_rules $m"
done
if [ -n "$dead_rules" ]; then
	echo "--- FAIL: §DEADGAUGE 以下量规有规则无赋值点（永不触发）：$dead_rules"; exit 1
fi
echo "  ok 通用守卫：$(echo "$rule_metrics" | wc -w | tr -d ' ') 条规则量规全部有赋值点"
# 负锁①：派生量规必须在取快照之前刷新（写在后面 = 每轮读到的都是上一轮值，等于没修）。
run_body=$(awk '/^func RunAlertEvaluation\(\)/{f=1} f{print} f&&/^}$/{exit}' internal/metrics/alerter.go)
pos_refresh=$(printf '%s\n' "$run_body" | grep -n 'refreshOrderFailRateGauge()' | head -1 | cut -d: -f1 || true)
pos_snap=$(printf '%s\n' "$run_body" | grep -n 'gaugeSnapshot()' | head -1 | cut -d: -f1 || true)
{ [ -n "$pos_refresh" ] && [ -n "$pos_snap" ] && [ "$pos_refresh" -lt "$pos_snap" ]; } \
	|| { echo "--- FAIL: §DEADGAUGE 刷新未发生在 gaugeSnapshot 之前（读到旧值）"; exit 1; }
# 负锁②：禁止用「累计值直接相除」冒充窗口失败率（那样一次进程内早期失败会永久挂着 5% 红线）。
if grep -nE 'ordersRejected\.Load\(\) \* 1000 / \(ordersPlaced\.Load\(\) \+ ordersRejected\.Load\(\)\)' internal/metrics/*.go | grep -q .; then
	echo "--- FAIL: §DEADGAUGE 又用全生命周期累计比冒充 5 分钟窗口失败率"; exit 1
fi
# 负锁③：静默跳过分支不得"什么都不写"（不写 = 保留昨天/上一轮的残值，对账降级日会持续误报）。
if ! grep -q 'SetGauge("settlement_diff_count", 0)' internal/trading/settlement.go; then
	echo "--- FAIL: §DEADGAUGE 对账跳过分支不再清零（残值冒充当日差异）"; exit 1
fi
echo "ok - §DEADGAUGE 专项守卫通过（行为锁 4 组 + 行为腿点名自检 2 道 + 静态锁 4 道 + 通用死规则守卫 1 条 + 负锁 3 道）"

echo "==> 70 §LIVEBACKUP-DEPLOY 备份链随部署下发 + 第 16 探针（P0-B 收编）..."
# 现象：§P0-B 把广州夜间快照从「trading.db + 9 个 JSON」扩到「+ live.db + accounts/」，但
#       backup_snapshot.ps1 / backup_snap.py **从来不在 deploy_guangzhou.sh 的 scp 清单里**
#       （历史上手工安装）。结果：仓库里改对了，现网 04:00 跑的还是只快照 trading.db 的旧版，
#       实盘四本账仍然无灾备——而且"任务在跑、每晚有产物、Mac 能拉到"三项全绿。
# 同族教训：§ENH-5 quote_feed.py 漏列、§A5-DEPLOY trading_calendar.py 漏列（都是"新增部署文件
#       必须入清单"）。本段把它变成清单正锁 + 内容版本判据，光查"任务存在"不再算通过。
grep -q 'deploy/qmt-win/backup_snapshot.ps1' scripts/deploy_guangzhou.sh \
	|| { echo "--- FAIL: 部署清单缺 backup_snapshot.ps1（§P0-B 现网不会生效，同 §A5 漏列形态）"; exit 1; }
grep -q 'deploy/qmt-win/backup_snap.py' scripts/deploy_guangzhou.sh \
	|| { echo "--- FAIL: 部署清单缺 backup_snap.py（缺库快照器=备份脚本上机即 throw）"; exit 1; }
grep -q 'deploy/qmt-win/register_backup_task.ps1' scripts/deploy_guangzhou.sh \
	|| { echo "--- FAIL: 部署清单缺 register_backup_task.ps1（任务无法随部署注册）"; exit 1; }
# BOM 归一必须做：两份 ps1 含中文注释，PS5.1 读无 BOM 的 UTF-8 会按 GBK 解析直接 ParserError。
grep -q 'ps1_bom deploy/qmt-win/backup_snapshot.ps1' scripts/deploy_guangzhou.sh \
	|| { echo "--- FAIL: backup_snapshot.ps1 未走 ps1_bom 归一（PS5.1 GBK 撕裂中文注释）"; exit 1; }
grep -q 'ps1_bom deploy/qmt-win/register_backup_task.ps1' scripts/deploy_guangzhou.sh \
	|| { echo "--- FAIL: register_backup_task.ps1 未走 ps1_bom 归一"; exit 1; }
# 仓库**字节**也必须已经是单 BOM：只锁"部署脚本会调 ps1_bom"锁不住本批实际发生的事——
# Write/Edit 工具重写 ps1 会把 BOM 静默剥掉（register_backup_task.ps1 就复犯过一次），而部署链
# 上传前会补回来，于是现网正常、仓库里的文件却是坏的：走 RUNBOOK §2 手工安装 = 首跑 ParserError。
# 09-29 再复犯一次并锤实"硬编码清单"本身就是漏检成因：本段原来只点名 2 个文件，
# deploy/qmt-win/all_service_watchdog.ps1 自 §C7 入库起仓库字节就无 BOM，每次部署由 ps1_bom 在
# 工作区悄悄补上（git status 只见 1 字节 M），现网一直正常、仓库里那份照 RUNBOOK 手跑必 ParserError。
# ⇒ 目标清单改为**从部署脚本派生**（凡 `ps1_bom <path>.ps1` 都进锁面），新增归一目标自动被覆盖。
bomTargets=$(grep -oE 'ps1_bom [^ ]+\.ps1' scripts/deploy_guangzhou.sh | awk '{print $2}' | sort -u || true)
# 派生式空清单＝本段全体退化成绿灯（§"TOTAL 0 in 0 files"双关同族）⇒ 先钉住清单非空且下限达标。
nBomTargets=$(printf '%s\n' "$bomTargets" | grep -c . || true)
[ "$nBomTargets" -ge 15 ] \
	|| { echo "--- FAIL: 部署侧 ps1_bom 目标派生为空/过少（实得 ${nBomTargets}，应≥15）——派生清单失效，BOM 自检退化成恒绿"; exit 1; }
for ps1 in $bomTargets; do
	[ -f "$ps1" ] \
		|| { echo "--- FAIL: 部署脚本对不存在的文件做 ps1_bom 归一（${ps1}）——上传步骤会把失败推到现网"; exit 1; }
	python3 - "$ps1" <<'PY' || { echo "--- FAIL: $ps1 仓库字节 BOM/首行不合规（手工安装路径首跑必炸）"; exit 1; }
import sys
d = open(sys.argv[1], 'rb').read()
n = 0
while d[n:].startswith(b'\xef\xbb\xbf'):
	n += 3
assert n == 3, 'BOM 个数=%d（需恰好 1 个）' % (n // 3)
d.decode('utf-8')
# §PS1-FIRSTLINE（2026-10-09 发版实录，波 3 提交 e3fac8b 造成的现网 abort）：
# 批量改写把两个 dot-source 文件的**首行行首 `#` 吞掉**（register_engine_services.ps1 掉
# `# Re`、service_definitions.ps1 掉 `# `），于是仓库里那行不再是注释而是"执行一个叫
# service_definitions.ps1 的命令"。后果不是文本难看：dot-source 时 PowerShell 抛
# CommandNotFoundException，ensure_gateway_config.ps1 那条 ssh 因此非零返回，
# 整轮 deploy_guangzhou.sh 在 [2b] 之后中止（服务停在停机态、前端与 [2e]/[3b] 都没跑）。
# 为什么必须由机器钉：本机没有 PowerShell，ps1 的**语法**在这个仓库里永远无法被解析器看见，
# BOM 锁只保证"字节可被 PS5.1 正确解码"，解码之后首行是注释还是命令它看不见。
# 判据形状＝剥掉单个前导 BOM 后首行以 `#` 起（这些文件都是 dot-source 片段或 -File 执行体，
# 首行按仓规必须是自述注释；纯代码开头的诊断脚本不在本清单射程，因为本清单从 ps1_bom 派生）。
first = d[3:].decode('utf-8').split('\n', 1)[0].lstrip('\ufeff')
assert first.startswith('#'), '首行不是注释（%r）——dot-source 时 CommandNotFoundException 会中止部署链' % first[:48]
PY
done
# 落盘目录三方同源：部署上传位 == 任务默认指向 == RUNBOOK 手工安装位（否则"更新一份、执行另一份"）。
grep -qF 'BACKUP_DIR="${DEPLOY_DIR}/deploy/qmt-win"' scripts/deploy_guangzhou.sh \
	|| { echo "--- FAIL: 部署侧快照目录不再与任务指向同源（双份脚本漂移风险复活）"; exit 1; }
grep -qF 'C:\opt\quant\deploy\qmt-win\backup_snapshot.ps1' deploy/qmt-win/register_backup_task.ps1 \
	|| { echo "--- FAIL: register_backup_task.ps1 默认路径变更（与部署落盘位脱钩）"; exit 1; }
# 校验面：第 16 探针在位，且带**内容版本判据**（旧版脚本只查文件存在会假绿）。
grep -qF 'backup:snap task+script(live.db)+artifacts' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 第 16 灾备探针丢失（§P0-B 现网生效无人复核）"; exit 1; }
grep -qF 'script 为旧版' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 探针丢失脚本内容版本判据（退化成只查任务在位=假绿）"; exit 1; }
grep -qF 'quant-backup-snap' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 探针不再核对计划任务名"; exit 1; }
# 负锁：注册步不得改成"文件不在也照样建任务"（那会把失败推到第二天 04:00 的静默期）。
grep -qF 'backup snapshot script not found' deploy/qmt-win/register_backup_task.ps1 \
	|| { echo "--- FAIL: register_backup_task.ps1 丢失脚本在位前置校验"; exit 1; }
# 负锁②：备份链注册失败必须可见（|| echo 提示可以，但不得静默吞成成功）。
if ! grep -qE '快照计划任务注册未通过' scripts/deploy_guangzhou.sh; then
	echo "--- FAIL: 部署步 [2e] 注册失败不再打印可见告警"; exit 1; fi
# §RESTIC-LOCK（09-23 现网首跑前置排障锤实的两个缺陷，都属"任务在跑、看起来正常、其实早已停更"）：
# ①陈旧锁只允许"命中 already locked 才 unlock 并重试一次"——无条件 unlock 等于拆掉并发互斥。
grep -qF 'already locked' deploy/qmt-win/backup_snapshot.ps1 \
	|| { echo "--- FAIL: 快照脚本丢失 restic 陈旧锁判别（Mac 拉取器崩溃留下的锁会让备份每晚静默停更）"; exit 1; }
grep -qF 'Invoke-Native' deploy/qmt-win/backup_snapshot.ps1 \
	|| { echo "--- FAIL: 快照脚本丢失 Invoke-Native（Stop 语义下原生 stderr 会变终止错误）"; exit 1; }
# ①b 09-23 08:2x 现网第 16 探针实录：自愈**起初只包住了 backup 这一条腿**，`forget --prune` 拿着
#    同一把 182h 的 Mac 遗留锁直接 exit=11 抛出 ⇒ 备份成功、产物仍 ok:false、异地半边仍停更。
#    所以自愈逻辑必须收成一个函数、两条腿共用，且 unlock 自身的退出码必须判（只看输出不看码 =
#    "锁没解开"被降级成"再试一次应该就好了"）。以下三锁钉住这个形状。
grep -qF 'function Invoke-Restic' deploy/qmt-win/backup_snapshot.ps1 \
	|| { echo "--- FAIL: restic 陈旧锁自愈不再是单一函数（backup 修好、forget 复发的成因）"; exit 1; }
# 计数必须先落到变量再比较：**不能**写成 `[ "$(grep -cE '"pat"' f)" -eq 1 ]` 这种"双引号内嵌命令
# 替换、模式里再带双引号"的形态——该形态在本机 shell 下模式会被吃掉、实跑得 0，锁把好代码判红。
ulCnt=$(grep -cE '"unlock", "-r", \$RepoDir' deploy/qmt-win/backup_snapshot.ps1 || true)
[ "$ulCnt" -eq 1 ] \
	|| { echo "--- FAIL: unlock 调用点=${ulCnt}（期望 1；两条腿各写一份重试＝必有一条腿漏）"; exit 1; }
grep -qE 'if \(\$ul\.code -ne 0\) \{ throw' deploy/qmt-win/backup_snapshot.ps1 \
	|| { echo "--- FAIL: unlock 的退出码不再被判定（陈旧锁未清时重试只是自我安慰）"; exit 1; }
for leg in backup forget; do
	grep -qE "Invoke-Restic @\(\"$leg\"" deploy/qmt-win/backup_snapshot.ps1 \
		|| { echo "--- FAIL: restic $leg 腿绕过 Invoke-Restic（陈旧锁自愈只覆盖一条腿）"; exit 1; }
done
# 负锁③：不得回到"裸管道把原生命令输出直接喂 Log"的写法——那正是 restic 成功却判失败的成因。
# （只判非注释行：注释里会原样提到旧形态作为反面说明。）
if grep -nE '& \$Restic [a-z]+ .*2>&1 \| ForEach-Object' deploy/qmt-win/backup_snapshot.ps1 \
	| grep -vE '^[0-9]+:[[:space:]]*#' | grep -q .; then
	echo "--- FAIL: 快照脚本又出现裸管道调用 restic（ErrorActionPreference=Stop 下 stderr 即终止错误）"; exit 1; fi
# 负锁④：首跑必须是显式开关触发的运维动作，不得变成每次部署自动搬 GB 级快照。
grep -qF 'LIVEBACKUP_FIRST_RUN:-0' scripts/deploy_guangzhou.sh \
	|| { echo "--- FAIL: 部署步 [2e] 首跑开关失去默认关闭语义（每次部署自动 GB 级快照+restic 写入）"; exit 1; }
echo "ok - §LIVEBACKUP-DEPLOY 专项守卫通过（清单正锁 3 + 同源锁 2 + 探针锁 3 + 锁面正锁 2 + restic 自愈形状锁 5 + 负锁 4 + 仓库字节 BOM 同源锁：派生目标逐文件自检 + 空清单正锁 1）"

# ── 71. §ADJ-BASIS 复权口径进断点键（2026-09-23 本机 A/B 锤实的"重算覆盖"前置缺陷）──
# resume_key 原本只含 区间/参数/股票池，**不含数值口径**。于是 §ADJ（因子前向填充）这种
# "入口不变、数值全变"的修复不会让任何旧断点失效：夜间链照旧逐窗命中改前装配好的面板并跳过
# 重算——即"已经修好复权"与"研究结论仍是复权前口径"可以同时为真，且全绿无声。
# 实测代价（本机同二进制双构建 A/B，604 只抽样 / 20230801~20260922）：
#   HighLow20 分层首末差 3.877%→-0.179%、Volatility20 3.729%→-0.126%、Brk60 3.030%→0.030%，
#   即"高波动/突破类因子有 3~4pp 分层能力"整条历史结论是除权日假跳空造出来的。
# 本段把"口径位必须在键里"钉成静态契约（新增键生成点若漏掉口径位即判红）。
echo ""
echo "==> 71 §ADJ-BASIS 复权口径位进断点键（防「修好了但结论没重算」）..."
for site in internal/research/windowed.go internal/research/pattern.go; do
	grep -qF 'adjBasisTag' "$site" \
		|| { echo "--- FAIL: $site 的断点键不再携带复权口径位（复权修复后夜间链会照旧复用改前面板）"; exit 1; }
done
# 三处键生成点逐点核对（discoveryResumeKey / pfac-dedup / 形态 dp），漏一处就等于那一路永不重算。
grep -qE 'return fmt\.Sprintf\("df\|.*%s%s"' internal/research/windowed.go \
	|| { echo "--- FAIL: 因子发现主键 df| 模板丢失口径位"; exit 1; }
grep -qE '"pfac-dedup:" \+ start \+ ":" \+ end \+ adjBasisTag' internal/research/windowed.go \
	|| { echo "--- FAIL: 逐因子去重缓存键 pfac-dedup 丢失口径位"; exit 1; }
grep -qE 'fmt\.Sprintf\("dp\|.*%s%s"' internal/research/pattern.go \
	|| { echo "--- FAIL: 形态扫描键 dp| 丢失口径位"; exit 1; }
# 口径常量必须存在且被测试认识（改口径不 bump＝换键失败＝沿用旧面板）。
grep -qE 'AdjBaselineVersion = "hfq-forward-fill-1"' internal/research/windowed.go \
	|| { echo "--- FAIL: AdjBaselineVersion 常量形态变更（请同步本锁与 docs/HFQ_BASELINE_RECOMPUTE_20260923.md）"; exit 1; }
if ! go test ./internal/research -run 'TestDiscoveryResumeKeyCarriesAdjBasis|TestCkptRotationOnBasisBump' -count=1 >/dev/null 2>&1; then
	echo "--- FAIL: §ADJ-BASIS 断点键回归测试未通过（旧键复用/新键不稳定）"; exit 1; fi
echo "ok - §ADJ-BASIS 守卫通过（键位正锁 3 + 常量锁 1 + 回归测试 2）"

# ── 72. §P0-B-HEADROOM 磁盘余量探针 + 护栏阈值等值锁（2026-09-23 现网首跑实录）──
# 现象：LIVEBACKUP_FIRST_RUN=1 首跑被 backup_snapshot.ps1 step 0 护栏挡下——`C: free space
#       6.9GB < 8GB guard`。同一天 04:00 那次计划任务却过了护栏（那时余量 ≥8GB）。
# 根因：磁盘余量是**单调递减**的前置条件，而它只在"备份真跑的那一刻"才被检查；部署面第 16 探针
#       读的是 SNAPSHOT_OK 的 ok/err，要人主动去读那一行 err 才知道是盘不够。余量不足时备份
#       永远不会发生 ⇒ 标记要么陈旧要么 ok=false，实盘账本照旧无灾备。
# 为什么不能直接把护栏调小：护栏守的是"GB 级快照写到一半盘满"这种比不备更糟的形态（撕裂快照
#       比缺快照更难发现），阈值属于资金安全侧，只能扩盘或清盘，不能改数凑跑。
# 修法前置：新增第 17 探针（只读）把余量抬成部署面日检项，并用**等值锁**钉住两处阈值同源——
#       单向锁（"探针必须 ≥8"）会让两侧各自漂移，故这里比对的是两侧解析出的同一个数。
echo ""
echo "==> 72 §P0-B-HEADROOM 余量探针与护栏阈值等值锁..."
grep -qF 'backup:disk headroom (guard=8GB)' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 第 17 余量探针丢失（盘不足将再次只在备份失败当晚才知情）"; exit 1; }
# 明细必须带四个数，否则判红后还要上机二查是谁吃的盘。
grep -qF 'free=" + $freeGB + "GB snapshot="' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 余量探针不再输出 free/snapshot/relay/datadir 明细"; exit 1; }
# 等值锁：脚本护栏与探针阈值必须是同一个 GB 数（各自 grep 出数字再比相等）。
guardN=$(grep -oE '\$c\.Free -lt [0-9]+GB' deploy/qmt-win/backup_snapshot.ps1 | grep -oE '[0-9]+' | head -1 || true)
probeN=$(grep -oE '\$pc\.Free -lt [0-9]+GB' scripts/verify_deploy_guangzhou.sh | grep -oE '[0-9]+' | head -1 || true)
[ -n "$guardN" ] && [ -n "$probeN" ] \
	|| { echo "--- FAIL: 护栏/探针阈值写法变更（等值锁取不到数，两侧判据失去同源）"; exit 1; }
[ "$guardN" = "$probeN" ] \
	|| { echo "--- FAIL: 磁盘阈值漂移 护栏=${guardN}GB vs 探针=${probeN}GB（探针判绿而备份必失败）"; exit 1; }
# 负锁：探针不得退化成"再读一次 SNAPSHOT_OK"（那与第 16 项重复，永远看不到未来的失败）。
if grep -qE '^\s*\$freeGB = .*SNAPSHOT_OK' scripts/verify_deploy_guangzhou.sh; then
	echo "--- FAIL: 余量探针改从 SNAPSHOT_OK 取数（自证式假绿）"; exit 1; fi
# §RESTIC-LOCK 补锁：Invoke-Native 的参数名绝不能叫 ${Args}——PS 自动变量，占用会静默改语义。
if grep -qE 'function Invoke-Native[\s\S]{0,80}\$Args' deploy/qmt-win/backup_snapshot.ps1; then
	echo "--- FAIL: Invoke-Native 参数占用 \$Args 自动变量"; exit 1; fi
grep -qE 'param\(\[string\]\$Exe, \[string\[\]\]\$CmdArgs\)' deploy/qmt-win/backup_snapshot.ps1 \
	|| { echo "--- FAIL: Invoke-Native 参数签名形态变更（请同步本锁）"; exit 1; }
# ps1_bom 只管 BOM：文档/注释口径必须与实现一致，"UTF-8 单 BOM + CRLF"是错话，出现在这个文件里
# 任何位置（含注释）都会诱导人工去转行尾、制造仓库与现网字节不一致（§BOM-REPO 同族）。
if grep -qF 'ps1_bom 归一（UTF-8 单 BOM + CRLF）' deploy/qmt-win/backup_snapshot.ps1; then
	echo "--- FAIL: backup_snapshot.ps1 重新声称 ps1_bom 会转 CRLF"; exit 1; fi
echo "ok - §P0-B-HEADROOM 守卫通过（探针正锁 2 + 阈值等值锁 1 + 负锁 3）"

# ── 73. §SNAP-LOCK 快照单写者锁（2026-09-23 当日自曝缺陷的收口 + 防"保护自己变成新停更入口"）──
# 现象：部署步 [2e] 里直跑的一次首跑被 SSH 中断留下孤儿 powershell，随后计划任务又被触发，第二次的
#       `restic forget --prune` 撞上第一次的仓库锁（exit=11 repo already locked），SNAPSHOT_OK 写成
#       ok=false——而两个快照进程此刻正在**同时往 SnapRoot\trading.db 覆盖写**。
# 根因：产物是固定名覆盖 ⇒ "谁在跑"这个不变量只有被调用脚本自己看得见；入口有三种（04:00 计划任务 /
#       部署触发 / 运维手工 RUNBOOK §2），调用方互相看不见。我第一版写的正是调用方守卫
#       （`schtasks /Query` 状态 + grep "正在运行"），而且是道**恒绿的假守卫**：远端回传的是 GBK
#       字节，UTF-8 模式下的中文 grep 永不命中（本仓 verify 明细一直有乱码即同一现象）。
# 因此本段锁三件事：①锁文件两侧同名（脚本写、探针读，路径漂移=探针自证式假绿）；②接管龄上界两侧
#       同数（等值锁，单向锁会让"探针判绿、脚本永久拒跑"）；③拒跑分支不得释别人的锁。
echo ""
echo "==> 73 §SNAP-LOCK 单写者锁与锁文件同源..."
# ① 锁文件名同源（脚本端 Join-Path ${SnapRoot}，探针端 Join-Path ${SnapDir}）。
grep -qE '\$Lock = Join-Path \$SnapRoot "\.backup\.lock"' deploy/qmt-win/backup_snapshot.ps1 \
	|| { echo "--- FAIL: 脚本端锁文件名/位置形态变更（探针将读到不存在的锁=恒绿）"; exit 1; }
grep -qE '\$Lk = Join-Path \$SnapDir "\.backup\.lock"' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 探针端锁文件路径与脚本不再同源"; exit 1; }
grep -qF 'backup:single-writer lock' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 第 18 单写者探针丢失"; exit 1; }
# 探针必须同时看得见"认锁的跑批"和"不认锁的孤儿进程"——只看锁文件就等于继续看不见 09-23 那类孤儿。
# （模式不以 - 开头：`grep -qF "-match ..."` 会被 BSD grep 当成选项，报 Invalid argument 直接红。）
grep -qF 'writers += 1' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 第 18 探针不再按命令行统计快照进程数（只剩锁文件视野）"; exit 1; }
grep -qF 'if ($writers -ge 2)' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 第 18 探针丢失 writers>=2 判据（两份快照互相覆盖看不见）"; exit 1; }
grep -qF 'writer without lock (unguarded run)' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 第 18 探针丢失「不认锁的进程」判据（09-23 那类孤儿复犯将无信号）"; exit 1; }
# ② 接管龄上界等值：脚本用分钟（${LockMaxMin}），探针用小时，换算后必须相等。
lockMin=$(grep -oE '\$LockMaxMin = [0-9]+' deploy/qmt-win/backup_snapshot.ps1 | grep -oE '[0-9]+' | head -1 || true)
probeH=$(grep -oE 'if \(\$lkAgeH -ge [0-9]+\)' scripts/verify_deploy_guangzhou.sh | grep -oE '[0-9]+' | head -1 || true)
[ -n "$lockMin" ] && [ -n "$probeH" ] \
	|| { echo "--- FAIL: 锁龄上界写法变更（等值锁取不到数，两侧判据失去同源）"; exit 1; }
[ "$lockMin" -eq $((probeH * 60)) ] \
	|| { echo "--- FAIL: 锁龄上界漂移 脚本=${lockMin}min vs 探针=${probeH}h（探针判绿而脚本永久接管/永久拒跑）"; exit 1; }
# ③ 释锁：成功出口必须无条件释，失败出口必须带 holderTaken 条件（否则拒跑那次会删掉真跑着的锁）。
python3 - deploy/qmt-win/backup_snapshot.ps1 <<'PY' || { echo "--- FAIL: §SNAP-LOCK 释锁位置不符（成功出口缺释锁 / catch 无条件释锁）"; exit 1; }
import re, sys
t = open(sys.argv[1], encoding='utf-8').read()
ok_leg = re.search(r'snapshot\+restic done.*?exit 0', t, re.S)
bad_leg = re.search(r'\ncatch \{.*?exit 1', t, re.S)
assert ok_leg and 'Remove-Item -LiteralPath $Lock' in ok_leg.group(0), '成功出口未释锁'
assert bad_leg and 'if ($Lock -and $holderTaken) { Remove-Item -LiteralPath $Lock' in bad_leg.group(0), 'catch 未条件释锁'
PY
# ④ 负锁：不靠 finally 释锁（try 内 exit 是否执行 finally 在 PS 各版本语义不一，本机无 pwsh 可验）。
if grep -qE '^\s*finally\s*\{' deploy/qmt-win/backup_snapshot.ps1; then
	echo "--- FAIL: 出现 finally 释锁（未实跑验证过的语义，改成两出口显式释）"; exit 1; fi
# ⑤ 负锁：调用方那道"看任务状态再触发"的假守卫不得复活（GBK 回传 + 看不见非任务入口）。
if grep -qF 'schtasks /Query /TN quant-backup-snap /FO LIST' scripts/deploy_guangzhou.sh; then
	echo "--- FAIL: 部署步重新用 schtasks 状态做触发前守卫（中文状态 grep 恒不命中=假守卫）"; exit 1; fi
echo "ok - §SNAP-LOCK 守卫通过（同源锁 2 + 探针视野锁 3 + 龄上界等值锁 1 + 释锁锁 2 + 负锁 2）"

# ── 74. §ADJ-BASIS-2/-2P 复权基线失效可见性（因子侧 + 形态侧必须对称）──
# 现象：§ADJ 把 HfqBars 的复权因子从"等值 JOIN"改成事件日回溯前向填充后，CloseHfq 整体换基线
#       （实测改前 99.2% 的 股票-交易日 按因子 1 出数）。此前审批落盘的战法权重/buy_threshold 全部
#       悬在旧口径上，但从库文件本身**完全看不出来**——fac_1 四个成分里三个的分层能力塌到 ≈0，
#       页面上却仍是一条正常的"已应用战法"。
# 根因：口径是取数层的隐式约定，条目里没有承载它的字段 ⇒ 任何人（含告警链）都无法判定"依据已失效"。
# 修法：条目落盘盖口径戳（adj_basis）+ 载入侧算派生标记（StaleAdjBasis）+ 量规/p1 告警 + 前端红标
#       + 处置开关（缺省 shadow：修口径这件事不该顺带把钱撤了）。
# 本段锁四件事：①三件套成对（赋值点/规则/路由）——本轮新增的**形态侧**最容易只写规则忘赋值点
#   （§DEADGAUGE 老坑：规则永不触发）；②因子侧与形态侧对称（半边盲区就是本缺陷的原始形态）；
# ③缺省与未知值都必须落 shadow（"改口径"顺手变成"停策略"是不可接受的越权）；④Go/JS 口径版本串等值。
echo ""
echo "==> 74 §ADJ-BASIS-2/-2P 基线失效可见性（含 pat_* 对称）..."
for g in applied_factor_stale_basis applied_pattern_stale_basis; do
	grep -q "metrics.SetGauge(\"${g}_count\"" internal/research/apply.go \
		|| { echo "--- FAIL: $g 无量规赋值点（§DEADGAUGE：有规则无数据源，永不响）"; exit 1; }
	grep -q "{Name: \"$g\", Metric: \"${g}_count\", Op: \"gt\", Threshold: 0" internal/metrics/alerter.go \
		|| { echo "--- FAIL: $g 缺 p1 规则或阈值不是「>0 即报」"; exit 1; }
	grep -qE "\"$g\": *RoutePush" internal/metrics/alert_routing.go \
		|| { echo "--- FAIL: $g 未显式 RoutePush（告警只进列表不推送，无人值守看不见）"; exit 1; }
done
# ② 对称性：载入侧 fail-close 与端点载荷两侧都必须各命中 2 次（因子 + 形态）。
nGate=$(grep -c "failClose && e.StaleAdjBasis" internal/research/apply.go || true)
[ "$nGate" -eq 2 ] || { echo "--- FAIL: 失效闸只覆盖一侧（fail-close 命中 $nGate 处，应为 2=fac+pat）"; exit 1; }
nAPI=$(grep -c "StaleAdjBasis: e.StaleAdjBasis" internal/server/library.go || true)
[ "$nAPI" -eq 2 ] || { echo "--- FAIL: /api/research/library 载荷只带一侧标记（命中 ${nAPI}，应为 2）"; exit 1; }
# ③ 缺省处置=shadow；未知值也必须归一为 shadow（不得"猜个更安全的默认"把策略停掉）。
python3 - internal/research/apply.go <<'PY' || { echo "--- FAIL: §ADJ-BASIS 缺省处置不符（未知值→disable 或静态缺省非 shadow）"; exit 1; }
import re, sys
t = open(sys.argv[1], encoding='utf-8').read()
fn = re.search(r'func normalizeStaleAdjBasisAction\(action string\) string \{(.*?)\n\}', t, re.S)
assert fn, 'normalizeStaleAdjBasisAction 不见了（未知值归一失去落点）'
body = fn.group(1)
assert body.rstrip().endswith('return StaleAdjBasisShadow'), '兜底 return 必须是 shadow（未知值不得 fail-close）'
assert 'StaleAdjBasisDisable' in body, '显式 disable 分支不见了'
assert re.search(r'staleAdjAction\s*=\s*StaleAdjBasisShadow', t), '包级静态缺省必须=shadow'
PY
# ④ 口径版本串 Go/前端等值（前端 CURRENT_ADJ_BASELINE 是后端常量的手工副本，漂开即整页红标失灵）。
goBasis=$(grep -oE 'AdjBaselineVersion = "[^"]+"' internal/research/windowed.go | sed 's/.*"\(.*\)"/\1/' || true)
jsBasis=$(grep -oE "CURRENT_ADJ_BASELINE = '[^']+'" web/src/pages/Research.jsx | sed "s/.*'\(.*\)'/\1/" || true)
[ -n "$goBasis" ] && [ -n "$jsBasis" ] || { echo "--- FAIL: 口径版本串取不到数（写法变更，等值锁失去落点）"; exit 1; }
[ "$goBasis" = "$jsBasis" ] || { echo "--- FAIL: 口径版本串漂移 Go=$goBasis vs 前端=${jsBasis}（红标判定两边不一）"; exit 1; }
# ⑤ 负锁：前端不得再按 kind 豁免形态战法（上一版正是这句留了半边盲区）。
python3 - web/src/pages/Research.jsx <<'PY' || { echo "--- FAIL: 前端 isAdjBasisStale 重新出现按 kind 豁免（pat_* 盲区复犯）"; exit 1; }
import re, sys
t = open(sys.argv[1], encoding='utf-8').read()
fn = re.search(r'export function isAdjBasisStale\(s\) \{\n(.*?)\n\}', t, re.S)
assert fn, 'isAdjBasisStale 不见了（红标判定失去落点）'
assert "kind === 'pattern'" not in fn.group(1), '按 kind 豁免形态战法的写法复活'
PY
echo "ok - §ADJ-BASIS-2/-2P 守卫通过（三件套成对锁 6 + 对称计数锁 2 + 缺省归一锁 1 + 版本等值锁 1 + 负锁 1）"

# ── 75. §ADJ-BASIS-3 事件缓存复权口径位（主键重建 + 降级保守 + 聚合过滤）──
# 现象：backtest_event_results 是逐事件回测结果的断点缓存，旧主键 (candidate_id, event_date,
#       industry) 不含口径位 ⇒ §ADJ 换基线后重跑同一候选，会把新口径数值写进旧口径的行，
#       且旧结果照样命中——"增量续跑"变成"新旧混装还自称已完成"。
# 根因：口径隐式约定的第二个承载点（第一个是战法库）。光加一列不够：本轮实测四列 UNIQUE 索引存在时
#       INSERT 仍被旧的表级三列主键拒（SQLite error 1555），必须重建表。
# 修法：主键重建为四列 + 旧行回填空串哨兵 + 守恒守卫；守卫中止时进降级模式（读恒未命中、写拒绝），
#       而不是让 store.Open 失败把整个引擎带停。
echo ""
echo "==> 75 §ADJ-BASIS-3 事件缓存口径位与降级保守..."
grep -qE 'PRIMARY KEY \(candidate_id, event_date, industry, adj_basis\)' internal/store/store.go \
	|| { echo "--- FAIL: 事件缓存主键不再含 adj_basis（新旧口径数值继续互相覆盖）"; exit 1; }
grep -q 'func (d \*DB) migrateBacktestEventResultsAdjBasis() error' internal/store/store.go \
	&& grep -q 'd.migrateBacktestEventResultsAdjBasis()' internal/store/store.go \
	|| { echo "--- FAIL: 旧库主键重建迁移或其调用点丢失（现网旧表升不上去）"; exit 1; }
# 守恒守卫：重建前后必须核对行数 + 逐三元组的行数与内容长度双向 EXCEPT，缺一即"迁移自己造差异"。
python3 - internal/store/store.go <<'PY' || { echo "--- FAIL: §ADJ-BASIS-3 守恒守卫被削弱（三守卫缺一）"; exit 1; }
import re, sys
t = open(sys.argv[1], encoding='utf-8').read()
fn = re.search(r'func \(d \*DB\) migrateBacktestEventResultsAdjBasis\(\) error \{(.*?)\n\}\n', t, re.S)
assert fn, '迁移函数体取不到'
b = fn.group(1)
assert 'EXCEPT' in b, '缺少逐组双向 EXCEPT 内容守恒核对'
assert b.count('COUNT(*)') >= 2, '缺少重建前后行数守恒核对'
assert 'markEventBasisDegraded' in b, '守卫不通过时必须转降级模式（而非硬失败或悄悄继续）'
assert 'SUM(LENGTH(' in b, '缺少逐组内容长度指纹（只比行数比不出数值被换掉）'
PY
# 降级=读未命中/写拒绝：两处都必须先看 EventBasisDegraded，且空串口径一律不当当前口径。
grep -qE 'if adjBasis == "" \|\| d.EventBasisDegraded\(\)' internal/store/backtest_jobs.go \
	|| { echo "--- FAIL: 读侧降级判断丢失（表结构不可用时仍查缓存=拿旧口径行当新结论）"; exit 1; }
grep -q '改前旧证据行的哨兵值' internal/store/backtest_jobs.go \
	|| { echo "--- FAIL: 写侧空串哨兵拒绝判据丢失（哨兵值可被当作当前口径写入）"; exit 1; }
# 情绪×战法矩阵跨全表聚合：不过滤口径就把改前/改后两套数值平均进同一格并对外发布。
grep -qE 'FROM backtest_event_results WHERE adj_basis = \?' internal/store/emotion_matrix.go \
	|| { echo "--- FAIL: 情绪矩阵聚合未按口径过滤（混桶=假统计）"; exit 1; }
grep -q 'if adjBasis == ""' internal/store/emotion_matrix.go \
	&& grep -q 'd.EventBasisDegraded()' internal/store/emotion_matrix.go \
	|| { echo "--- FAIL: 情绪矩阵缺「口径未装配/降级即拒绝聚合」的保守闸"; exit 1; }
# 负锁：读侧 SQL 不得出现字面量 adj_basis=''（空串是旧证据行哨兵，永远不能当查询目标口径）。
# 只用裸 grep 会误伤说明性文字（store.go 的注释与日志里就在描述"旧行落成 adj_basis=''"这件事，
# 那是正确行为，不是查询条件），故先剔掉 // 行注释与 log.Printf 文案，只查真 SQL 字符串。
python3 - internal/store/backtest_jobs.go internal/store/store.go internal/store/emotion_matrix.go <<'PY' || { echo "--- FAIL: 出现按空串口径查询（把改前旧行当成可复用的当前口径结果）"; exit 1; }
import re, sys
for p in sys.argv[1:]:
    t = open(p, encoding='utf-8').read()
    t = re.sub(r'(?m)^\s*//.*$', '', t)          # 剔说明性注释（注释里出现 '' 是在描述旧行哨兵，属正确）
    t = re.sub(r'(?s)\`.*?\`', lambda m: m.group(0) if re.search(r'(?i)select|where|update|delete', m.group(0)) else '', t)
    t = re.sub(r'log\.(Printf|Println)\(.*?\)\n', '', t, flags=re.S)   # 剔日志文案（同上，只描述不查询）
    t = re.sub(r'(?m)^\s*//.*$', '', t)
    if re.search(r"adj_basis\s*=\s*''", t):
        print("命中文件:", p, file=sys.stderr)
        sys.exit(1)
PY
echo "ok - §ADJ-BASIS-3 守卫通过（主键锁 1 + 迁移锁 1 + 守恒锁 4 + 降级锁 2 + 聚合锁 3 + 负锁 1）"

# ── 76. §CKPT-PRUNE 死断点清理（缺省 dry-run + 双拒绝门 + 删除只在守卫之后）──
# 现象：research_ckpts 里绝大多数断点行属于改前口径（resume_key 不含当前口径位），永不再被命中，
#       却持续占库、持续出现在"已完成候选"的统计里。
# 根因：断点表按键字符串索引，口径一换键就换，旧行成了孤儿，且没有任何清理入口。
# 为什么不能直接 DELETE：删断点=删研究进度。新基线还没落过一行时删旧行，等于没有对照地销毁证据；
#       夜间寻优正在写这张表时删，那一轮跑到一半的进度永久丢失。故四道门缺一不可。
echo ""
echo "==> 76 §CKPT-PRUNE 清理闸..."
grep -q 'apply := fs.Bool("apply", false' cmd/research/prune_checkpoints.go \
	|| { echo "--- FAIL: --apply 不再是缺省 false（命令变成跑即删）"; exit 1; }
grep -q 'marker := fs.String("adj-marker", research.AdjBasisMarker' cmd/research/prune_checkpoints.go \
	|| { echo "--- FAIL: 口径位缺省不再取代码常量（手填历史值=按错误基线删进度）"; exit 1; }
grep -q 'strings.HasPrefix(marker, "|adj=")' internal/store/research_ckpts.go \
	|| { echo "--- FAIL: 口径位格式闸丢失（空串/畸形 marker 会让「含口径位」判定退化为全表匹配）"; exit 1; }
grep -q 'rep.MarkedRows == 0 || rep.CutoffAt == ""' internal/store/research_ckpts.go \
	|| { echo "--- FAIL: 「无新基线断点即拒删」门丢失"; exit 1; }
grep -q 'TaskDiscoverFactors, TaskDiscoverPatterns' internal/store/research_ckpts.go \
	|| { echo "--- FAIL: 在写任务拒绝门不再按寻优任务类型取数（看不见正在跑的写入方）"; exit 1; }
# 删除语句必须排在 dry-run/无可删/在跑三道门之后（顺序错一位，dry-run 也会真删）。
python3 - internal/store/research_ckpts.go <<'PY' || { echo "--- FAIL: §CKPT-PRUNE 删除先于守卫（三道门形同虚设）"; exit 1; }
import re, sys
t = open(sys.argv[1], encoding='utf-8').read()
fn = re.search(r'func \(d \*DB\) PruneStaleCheckpoints\(marker string, apply bool\) \(\*CkptPruneReport, error\) \{(.*?)\n\}\n', t, re.S)
assert fn, 'PruneStaleCheckpoints 函数体取不到'
b = fn.group(1)
guard = re.search(r'if !apply \|\| rep\.StaleRows == 0 \|\| len\(rep\.Blocked\) > 0 \{\n\s*return rep, nil', b)
dele = re.search(r'DELETE FROM research_ckpts', b)
assert guard and dele, '删除前的三合一守卫或 DELETE 语句丢失'
assert guard.start() < dele.start(), 'DELETE 排在守卫之前'
assert b.index('rep.Blocked = blocked') < guard.start(), '在跑统计必须先于守卫赋值（否则门永远看不到 blocked）'
PY
echo "ok - §CKPT-PRUNE 守卫通过（dry-run 锁 2 + 格式/双拒门锁 3 + 顺序锁 2）"

# ── 77. §C-OPS 运维遗留三件套（mock 退役 / token 轮换 / keystore 口令备份）──
# 现象：①实盘机仍留着 UAT 期的 qmt-mock 产物与监听端口（与真网关争端口、是误成交的候选来源）；
#       ②网关 token 自建仓以来未轮换，且值同时存在于 4 处（config 文件 / 服务 env / 桥配置 / 部署参数）
#         ——手工改一处就会造成"网关起得来但鉴权全 401"；
#       ③APK 签名口令 keystore.pass 只有一份、在仓库工作树里（虽被 gitignore），盘坏了已发布版本
#         永不可复现。
# 本段锁"不可逆动作的安全阀"：退役=改名不删除、写操作=显式 -Apply、备份=显式目的地且拒仓库内。
# §OPS-ALIGN（2026-09-23，owner 裁决 4）：两个运维 .ps1 的**缺省方向**原是一动一静（退役脚本不带
#   参数就停进程改名，token 轮换不带参数只预览），操作人按另一个脚本的肌肉记忆敲命令就会误改现网。
#   现已统一成"缺省只预览、显式 -Apply 才动手"。方向翻转带来的新风险是**调用点漏 -Apply**：
#   退出码照样 0、照样打 done，退役却根本没发生——故本段除脚本自身的顺序锁外，另钉调用点与探针。
echo ""
echo "==> 77 §C-OPS 运维安全阀..."
for ps in decommission_qmt_mock rotate_qmt_token; do
	grep -q "ps1_bom deploy/qmt-win/${ps}.ps1" scripts/deploy_guangzhou.sh \
		|| { echo "--- FAIL: deploy/qmt-win/${ps}.ps1 不在 ps1_bom 清单（现网 PowerShell 按 ANSI 读，中文注释吞语法）"; exit 1; }
	grep -qE "^[^#]*\\\$SCP [^|;]*deploy/qmt-win/${ps}\.ps1" scripts/deploy_guangzhou.sh \
		|| { echo "--- FAIL: ${ps}.ps1 未被上传命令带走（归一好却进不了现网=探针永远红）"; exit 1; }
done
# 两个 .ps1 的仓库字节形态：恰好一个 UTF-8 BOM 在文件头（§BOM-REPO：重复归一 = 现网解析炸）。
python3 - deploy/qmt-win/decommission_qmt_mock.ps1 deploy/qmt-win/rotate_qmt_token.ps1 <<'PY' || { echo "--- FAIL: 运维 .ps1 的 BOM 字节不符（必须恰好一个 UTF-8 BOM 在文件头）"; exit 1; }
import sys
for p in sys.argv[1:]:
    raw = open(p, 'rb').read()
    assert raw[:3] == b'\xef\xbb\xbf', p + ' 缺头部 BOM'
    assert raw[3:].count(b'\xef\xbb\xbf') == 0, p + ' 正文中再次出现 BOM（重复归一的典型形态）'
PY
# 退役=改名不删除（.disabled-<ts> 可回滚）；真删除动词不得出现。
grep -q 'Rename-Item -LiteralPath $t.Exe -NewName ($t.Name + ".disabled-' deploy/qmt-win/decommission_qmt_mock.ps1 \
	|| { echo "--- FAIL: mock 退役不再改名（回滚能力丢失）"; exit 1; }
if grep -qE '^[^#]*Remove-Item' deploy/qmt-win/decommission_qmt_mock.ps1; then
	echo "--- FAIL: 退役脚本出现 Remove-Item（不可逆删除，UAT 资产应改名保留）"; exit 1; fi
grep -q 'QMT_MOCK_DECOMMISSION=1' scripts/deploy_guangzhou.sh \
	|| { echo "--- FAIL: 退役步不再由显式开关门控（默认就在实盘机上停进程改名）"; exit 1; }
# §OPS-ALIGN 正锁①：退役脚本与轮换脚本同一口径——缺省即预览，改名原语必须排在 dry-run 早退之后。
# （旧实现只有轮换侧有这条顺序锁；缺省方向翻过来后，退役侧一旦被人改回"缺省动手"就无人报警。）
python3 - deploy/qmt-win/decommission_qmt_mock.ps1 <<'PY' || { echo "--- FAIL: mock 退役脚本的缺省方向不再是「只预览」（不带 -Apply 就会改现网文件名）"; exit 1; }
import re, sys
t = open(sys.argv[1], encoding='utf-8').read()
assert re.search(r'\[switch\]\$Apply', t), '-Apply 开关丢失（动手侧再无显式入口）'
assert re.search(r'if \(-not \$Apply\) \{ \$DryRun = \$true \}', t), '缺省即预览的归一语句丢失'
early = re.search(r'if \(\$DryRun\) \{.*?exit 0', t, re.S)
assert early, 'dry-run 早退分支丢失'
assert 'Rename-Item' in t, '改名原语都不见了（本锁与实现同时失配，请同步）'
assert early.end() < t.index('Rename-Item'), '改名动作排在 dry-run 早退之前（缺省调用就会动生产文件）'
PY
# §OPS-ALIGN 正锁②：**调用点必须显式带 -Apply**。缺省方向一改，部署步 [3d] 若沿用旧命令就只会打印
# 预览并退出 0——"脚本跑成功了"与"退役发生了"从此是两件事，这是本批最容易复犯的静默假成功形态。
grep -qE 'decommission_qmt_mock\.ps1 -Apply' scripts/deploy_guangzhou.sh \
	|| { echo "--- FAIL: 部署步 [3d] 未显式带 -Apply（退役根本不会发生，却在打 OK）"; exit 1; }
# 轮换=缺省 dry-run：写盘动作必须排在 $DryRun 早退之后。
python3 - deploy/qmt-win/rotate_qmt_token.ps1 <<'PY' || { echo "--- FAIL: rotate 的 dry-run 早退不再先于写盘（不加 -Apply 也会改配置）"; exit 1; }
import re, sys
t = open(sys.argv[1], encoding='utf-8').read()
assert re.search(r'if \(-not \$Apply\) \{ \$DryRun = \$true \}', t), '缺省即 dry-run 的归一语句丢失'
early = re.search(r'if \(\$DryRun\) \{.*?exit 0', t, re.S)
assert early, 'dry-run 早退分支丢失'
writes = [t.index(x) for x in ('Copy-Item -LiteralPath $ConfigFile', '[System.IO.File]::WriteAllText') if x in t]
assert writes, '写盘原语都不见了（本锁与实现同时失配，请同步）'
assert early.end() < min(writes), '写盘动作排在 dry-run 早退之前'
PY
# 负锁：轮换脚本不得把新 token 明文写进任何输出（本仓库铁律：输出只准键名/计数/指纹）。
if grep -qE '\+ \$newToken|\$\{newToken\}' deploy/qmt-win/rotate_qmt_token.ps1; then
	echo "--- FAIL: 轮换脚本出现回显新 token 明文的路径"; exit 1; fi
# 口令备份：目的地必填、拒凭空目录、拒仓库内。
grep -q 'BACKUP_TARGET_DIR:-}' scripts/backup_keystore_pass.sh \
	&& grep -q 'git rev-parse --show-toplevel' scripts/backup_keystore_pass.sh \
	&& grep -q '拒绝 mkdir' scripts/backup_keystore_pass.sh \
	|| { echo "--- FAIL: keystore 口令备份的安全阀缺一（必填目的地/仓库内拒写/不自动建目录）"; exit 1; }
# 现网两条新探针必须在位（第 19 mock 退役、第 20 token 指纹一致性）。
grep -q 'qmt:mock retired' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 第 19 探针（mock 退役复核）丢失"; exit 1; }
grep -q 'qmt:token fp agree across readable sources' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: 第 20 探针（token 指纹一致性）丢失或标题回退成"四源"（③b 账号级腿在位时源数是 5）"; exit 1; }
# §TOKEN-BLIND（2026-09-24）三条读法锁：这条探针 09-23 连红两晚，红的是探针自己读不到、不是口令漂移
# （同刻 gw:/health 绿）。判据语义一个字不许动，动的只有"怎么读"和"读不到时怎么写"。
# ① 网关 config.xt.json 是 ensure_gateway_config.ps1 刻意**无 BOM UTF-8** 落盘的（网关 json.load 见 BOM 抛），
#    PS 5.1 缺省按 GBK 解会把 JSON 字符串闭合劈开 ⇒ 必须显式 -Encoding UTF8，与同段读引擎 config.json 对齐。
grep -q '\$tk1RawTxt = Get-Content -Path $GatewayCfg -Raw -Encoding UTF8' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: token ① 腿又用缺省码页读无 BOM UTF-8 网关配置（GBK 吞引号 ⇒ unknown 失明复犯）"; exit 1; }
# ② 引擎侧权威 token 在**账号快照**（auth.json configs[].key=quant_config_json_v1 → .qmt.token，
#    见 internal/config/config.go GetQMTConfigFor 三级优先级），全局 rules.qmt 只是兜底、允许长期为空。
grep -q "if (\$tkC.key -ne 'quant_config_json_v1') { continue }" scripts/verify_deploy_guangzhou.sh \
	&& grep -q '$tkR = "$($tkC.value)" | ConvertFrom-Json' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: token ③b 账号级读法丢失（引擎腿会重新把"合法为空的全局字段"当成缺配）"; exit 1; }
# ③ 多账号本就各配各的网关/口令：两个以上不同快照指纹时**不参与判红**，只如实写 multi-account(N)。
grep -q 'multi-account(' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: ③b 的多账号护栏丢失（拿别的账号口令来比 = 凭空造红）"; exit 1; }
# ④ 空态必须分字（no-file / empty-field / key-absent / no-bridge-proc），且明细带"可读源数/期望源数"——
#    否则下次失明又只剩一串语义不同的 missing。
grep -q 'readable=" + \$tkPres.Count + "/expect="' scripts/verify_deploy_guangzhou.sh \
	&& grep -q '"empty-field"' scripts/verify_deploy_guangzhou.sh \
	|| { echo "--- FAIL: token 探针明细不再自证读不到的原因（失明只能靠再跑一晚归因）"; exit 1; }
# 负锁：两条新探针的判据数组只准追加 ASCII 明细（本仓库实录：PowerShell→SSH→bash 回传 GBK 字节，
# 中文 detail 在 grep 判据里恒不命中 = 把假绿写进探针）。
if grep -nE '\$(mockMiss|tkBad) \+= "[^"]*[^ -~]' scripts/verify_deploy_guangzhou.sh | grep -q .; then
	echo "--- FAIL: mock/token 探针的 detail 文案含非 ASCII 字符（SSH/GBK 假绿陷阱复犯）"; exit 1; fi
echo "ok - §C-OPS 守卫通过（清单锁 4 + BOM 锁 1 + 安全阀锁 7（含 §OPS-ALIGN 缺省方向锁 2）+ 明文/删除负锁 2 + 探针锁 3）"

# ── 78. §EXIT-RETAIN 出场覆盖跟随持仓，不跟随启用开关 ──
# 现象：停用一条战法库规则（人工点停用，或 stale_adj_basis_action=disable 这类**无人值守**自动路径）
#       时，它名下**已经持有的持仓**的止盈/最长持有会被一并摘掉，回退到全局 8%/15 天。
# 根因：SetRuleExitOverrides 旧实现按 !Enabled 直接清表——把"停用"理解成"这条战法的一切都不作数"。
# 为什么是资金安全缺陷：回退方向未知（可能更紧也可能更松），等于未经授权静默改风险参数；而 disable
#       这条自动路径正是本轮 §ADJ-BASIS 为避免"修口径顺带撤钱"才引入的，两处必须同源。
# 修法：重建注册表时传入开放持仓策略键计数；停用但仍持仓的条目继续入表并打 WARN；删除仍立即撤销
#       （条目已不存在，无参数可留）；战法库页面逐条下发 open_positions，确认框如实说明。
echo ""
echo "==> 78 §EXIT-RETAIN 持仓感知停用..."
grep -q 'func SetRuleExitOverrides(factors \[\]research.AppliedFactorEntry, patterns \[\]research.AppliedPatternEntry, held HeldStrategyKeys)' internal/combat_agent/rule_exit_overrides.go \
	|| { echo "--- FAIL: 出场覆盖重建签名回退成「不带持仓」（停用即摘覆盖复犯）"; exit 1; }
grep -q 'cAgent.SetExitHeldProvider(r.OpenPositionStrategyCounts)' internal/engine/registry.go \
	|| { echo "--- FAIL: 热重载持仓回调丢失（后续 Reload 用的是启动那一刻的持仓快照）"; exit 1; }
grep -q 'func (r \*Registry) OpenPositionStrategyCounts() combat_agent.HeldStrategyKeys' internal/engine/registry.go \
	|| { echo "--- FAIL: 跨账本持仓计数单一真相源丢失（按账号判持仓会清掉别的账号该保留的覆盖）"; exit 1; }
# 策略键归一化必须两侧同源（入表键与查询键同一函数），否则"持仓命中"靠运气。
python3 - internal/combat_agent/rule_exit_overrides.go <<'PY' || { echo "--- FAIL: §EXIT-RETAIN 键归一化不再同源（入表/查询各写一遍 lower+trim）"; exit 1; }
import re, sys
t = open(sys.argv[1], encoding='utf-8').read()
assert re.search(r'func normalizeExitKey\(s string\) string \{ return strings\.ToLower\(strings\.TrimSpace\(s\)\) \}', t), 'normalizeExitKey 定义形态变更'
assert t.count('strings.ToLower(strings.TrimSpace(') == 1, '出现第二处手工归一化（两侧口径迟早漂开）'
assert 'next[normalizeExitKey(id)] = ov' in t, '入表侧不再走同源归一化'
PY
# 停用保留必须留痕（无人值守路径悄悄留一条已停战法的覆盖，也得看得见）。
grep -q 'WARN 规则 %s(%s) 已停用但仍有 %d 笔开放持仓' internal/combat_agent/rule_exit_overrides.go \
	|| { echo "--- FAIL: 持仓保留分支不再打 WARN"; exit 1; }
# 端点逐条下发 open_positions，且与出场覆盖用同一归一化（否则页面"N 笔"与实际不符）。
grep -q 'combat_agent.NormalizeStrategyKey' internal/server/library.go \
	|| { echo "--- FAIL: 战法库 open_positions 不再复用同源归一化"; exit 1; }
# 负锁：不得出现"只要有持仓就一律保留"的全局兜底（那样删除的规则覆盖摘不掉）。
if grep -qE 'if len\(held\) > 0 \{' internal/combat_agent/rule_exit_overrides.go; then
	echo "--- FAIL: 出现按持仓存在性的全局兜底（已删除规则的覆盖撤销不掉）"; exit 1; fi
echo "ok - §EXIT-RETAIN 守卫通过（签名/装配锁 3 + 归一同源锁 1 + 留痕锁 1 + 端点锁 1 + 负锁 1）"

# ── 79. §SURVEY-HORIZON / §SURVEY-COVERAGE 排摸尺子、产物落点与盲区显形 ──
# 现象（同一轮排摸踩出的三则）：
#   ① 成分健康度恒用全局 --h 5 度量，而 fac_117/fac_118 落库 horizon=10 ⇒ 表里"1/1、2/2 死成分"
#      是拿 5 日尺子量 10 日战法的结果，不能作为"该战法已失效"的依据；
#   ② `research --db … --out …` 的全局 --out 被子命令同名缺省盖回 ./research_out，含战法权重/阈值的
#      strategy_survey.json 掉进可被 git add 的工作树，而脚本打印 /tmp 路径并报成功
#      ——本仓库 §M8/§N-6 主题「降级报成功」的又一实例；
#   ③ momentum 在实盘白名单能下单，却没有回放适配器 ⇒ 排摸表里**根本没有这一行**，
#      "没排摸"与"排摸过且没问题"在只看表的人眼里长得一样。
# 修法：按条目自身 horizon 建 report 键并如实标尺；写产物前硬闸拒落工作树 + 脚本复核锚点行；
#       盲区由代码算出差集并计成非零锚点 survey_unsurveyable。
echo ""
echo "==> 79 §SURVEY-HORIZON/§SURVEY-COVERAGE 排摸尺子、产物落点与盲区..."
grep -q 'func componentHealth(ids \[\]string, horizon int, report map\[string\]\*research.FactorReport, minSpreadPP float64)' cmd/research/survey.go \
	|| { echo "--- FAIL: 成分健康度不再按传入 horizon 度量（全局尺子复犯=误判战法已死）"; exit 1; }
grep -qF 'report[reportKey(d.ID, h)] = research.Summarize(panels, d, *start, *end, h,' cmd/research/survey.go \
	|| { echo "--- FAIL: 面板汇总不再逐档 Summarize（多档位 report 键永远查不到东西）"; exit 1; }
grep -qF 'entryHorizon(e.Horizon, *horizon)' cmd/research/survey.go \
	|| { echo "--- FAIL: 因子条目不再按自身 horizon 选尺子"; exit 1; }
grep -qF 'survey_unsurveyable=' cmd/research/survey.go \
	&& grep -qF 'art.UnsurveyableIDs = btreplay.UnsurveyedLiveForms()' cmd/research/survey.go \
	|| { echo "--- FAIL: 盲区锚点行或其取数丢失（momentum 类「量不到」又退回一句 note）"; exit 1; }
grep -qF 'func UnsurveyedLiveForms() []string' internal/btreplay/replay.go \
	|| { echo "--- FAIL: 白名单与适配器差集不再由代码算（手写清单迟早与实盘脱节）"; exit 1; }
grep -qF 'func TestLiveWhitelistFormsMatchSurveyCoverage' internal/server/known_strategy_forms_test.go \
	|| { echo "--- FAIL: 白名单↔排摸覆盖面等值测试丢失"; exit 1; }
# 产物不得落仓库工作树：硬闸 + 缺省目录 + 脚本侧锚点复核，三者缺一不可。
grep -qF 'if root, ok := goModuleRoot(*outDir); ok' cmd/research/survey.go \
	|| { echo "--- FAIL: 排摸产物落仓库的硬闸丢失"; exit 1; }
grep -qF 'outDir := fs.String("out", defaultSurveyOutDir()' cmd/research/survey.go \
	|| { echo "--- FAIL: --out 缺省不再走临时目录（硬闸只拦显式传参，拦不住缺省值）"; exit 1; }
grep -qF 'for anchor in survey_unhealthy survey_unsurveyable' scripts/survey_live_rules.sh \
	|| { echo "--- FAIL: 现网排摸脚本不再复核两条锚点行（跑完没产物照样报成功）"; exit 1; }
# 负锁：--out 缺省不得再写工作树相对路径（research_out 那次就是从这里掉进 git add 候选的）。
if grep -qE 'fs.String\("out", "\./' cmd/research/survey.go; then
	echo "--- FAIL: --out 缺省又回到仓库相对路径"; exit 1; fi
echo "ok - §SURVEY 守卫通过（尺子锁 3 + 盲区锁 3 + 落点锁 3 + 负锁 1）"

# ── 80. §PICKILL-SCOPE UAT 自举的兜底 kill 只圈本项目拉起的进程 ──
# 现象（2026-09-23 实测误伤）：`uat_bootstrap.sh stop` 的兜底 `pkill -f "quant.*QUANT_ADDR=:18080"`
#       杀掉了同机另一份 checkout（quant-binance）的 UAT 引擎。
# 根因：macOS 的 pkill/pgrep -f 把**进程环境变量一并计入匹配串**（`pgrep -fl` 输出里命令行后拖着整段 env 即证），
#       于是"端口名"成了跨项目的通配；两台机器共用 18080 端口约定 ⇒ 一条 stop 停掉别人的栈，
#       对方的守护拉起再把本方刚起的引擎踢死 ⇒ 本方表现为"引擎未就绪（/setup 非 200）"的假故障。
# 前置：kill 判据必须含数据目录绝对路径（$PIDDIR 由 $DATA_DIR 派生，checkout 间天然不同），
#       且不能再按 env 里的端口名匹配。
if ! grep -qF 'pkill -f -- "$PIDDIR/quant"' scripts/uat_bootstrap.sh; then
	echo "--- FAIL: 兜底 kill 不再按本项目二进制绝对路径匹配（跨项目误伤的入口）"; exit 1; fi
if ! grep -qF 'pkill -f -- "$PIDDIR/qmt-mock"' scripts/uat_bootstrap.sh; then
	echo "--- FAIL: 假柜台的兜底 kill 缺失（只清引擎会留下占端口的 mock）"; exit 1; fi
# §VITE-ORPHAN：pid 记的是 npx 外壳，真占端口的 node(.bin/vite) 会活下来让下次 --strictPort 失败。
if ! grep -qF 'pkill -f -- "$ROOT/web/node_modules/\.bin/vite --port ${FRONT_PORT}' scripts/uat_bootstrap.sh; then
	echo "--- FAIL: vite 子进程兜底 kill 缺失（stop 后再 up 会卡在端口占用）"; exit 1; fi
# 负锁：端口名/env 形式的匹配串不得复活。只判**真正执行的 pkill 行**（行首无 #），
# 否则"描述旧写法为何危险"的注释本身会被这条锁打死（§静态负锁自伤形态）。
if grep -E '^[[:space:]]*pkill' scripts/uat_bootstrap.sh | grep -q 'QUANT_ADDR'; then
	echo "--- FAIL: pkill 又按 env 端口名匹配（会在任意 checkout 间互杀）"; exit 1; fi
echo "ok - §PICKILL-SCOPE 守卫通过（同源路径锁 3 + env 匹配负锁 1）"

# ── 81. §UAT-PORTS e2e 打的是"本次自举的那套栈"，不是同机任意一套 ──
# 现象（2026-09-23）：同机并存第二份 checkout（quant-binance）已占 18080/18789，本项目自举只能挪端口；
#       而 spec 里硬编 http://127.0.0.1:18789 + 「连不上 mock 就 skip」⇒ L1-2/QS-2 打到**别人那套 mock**
#       上拿到 200 照样绿（QS-2 首跑即为此形态）。断言的对象都不是本仓库代码。
# 前置：mock 地址单一来源 = E2E_MOCK_URL（自举脚本与 CI 各自导出）；且"传了该变量=跑在自举栈上"
#       时连不上必须判红，软跳过只留给外部部署场景。
grep -qF 'const MOCK_URL = process.env.E2E_MOCK_URL' web/e2e/uat_full.spec.mjs \
	|| { echo "--- FAIL: mock 地址不再单源自 E2E_MOCK_URL"; exit 1; }
grep -qF 'function mockUnavailableOrFail' web/e2e/uat_full.spec.mjs \
	|| { echo "--- FAIL: mock 不可达的两种口径（自举=红／外部=skip）没有收在一处"; exit 1; }
grep -qF 'if (!resp) mockUnavailableOrFail(lastErr)' web/e2e/uat_full.spec.mjs \
	|| { echo "--- FAIL: 至少一处用例未接上 mockUnavailableOrFail"; exit 1; }
grep -qF 'export E2E_MOCK_URL=http://127.0.0.1:${MOCK_PORT}' scripts/uat_bootstrap.sh \
	|| { echo "--- FAIL: 自举脚本不再按本次 MOCK_PORT 导出 E2E_MOCK_URL（spec 会退回默认口）"; exit 1; }
grep -qF 'E2E_MOCK_URL="http://127.0.0.1:${MOCK_PORT}"' scripts/uat_bootstrap.sh \
	|| { echo "--- FAIL: run 模式未把 E2E_MOCK_URL 传给 playwright"; exit 1; }
grep -qF 'E2E_MOCK_URL: http://127.0.0.1:18789' .github/workflows/nightly-e2e.yml \
	|| { echo "--- FAIL: CI 未导出 E2E_MOCK_URL（CI 本来就要求零 skip，软跳过在这里该失效）"; exit 1; }
# 负锁①：不得再有直连硬编端口的请求/断言字面量（注释里描述历史形态不在此列 ⇒ 只查调用式）。
if grep -qE "request\.get\('[^']*18789|toContainText\('127\.0\.0\.1:18789" web/e2e/uat_full.spec.mjs; then
	echo "--- FAIL: spec 里又出现硬编 18789 的调用"; exit 1; fi
# 负锁②：`test.skip(!resp...)` 的无条件软跳过不得复活（它会连"栈根本没起来"一起洗白）。
if grep -q 'test.skip(!resp' web/e2e/uat_full.spec.mjs; then
	echo "--- FAIL: 复活了无条件 skip(!resp)"; exit 1; fi
echo "ok - §UAT-PORTS 守卫通过（单源锁 1 + 口径锁 2 + 导出锁 3 + 负锁 2）"


echo "==> 82 §KLINE-CHAIN-3 日K三级兜底链：腾讯主源 + 东财降到复权链末尾 + 库内日K四道守卫（2026-09-23 夜间批，owner 裁决 1/2）..."
# 现象（09-23 全天）：腾讯与东财两条复权腿同时不通 → fetchDayKLine 只剩"带标记的不复权兜底"，
#       而 §H3 的守卫正确地拒绝让不复权序列进 md.KLines → 结果是日K为空、
#       N形/双响炮/因子这批吃日K的战法整轮零分，当日只有不依赖日K的龙头战法出信号。
# 定性（owner 原话）："报警有啥用啊，又不能解决问题"——所以本条不是加告警，是把兜底做实。
# 三条前置：① 链序 腾讯(仅qfq) → 东财(qfq，降级但**不摘掉**) → 库内日K → 不复权(带标记)；
#       ② 库内价是**后复权**，必须按实时昨收定锚归一 + 量纲(手→股) + 新鲜度 + 丢当日行，
#          四道守卫任一不过即拒用（锚错位的序列比空序列更坏：它整体平移却不报错）；
#       ③ 库内腿靠引擎注入才有数据——装配漏掉时表现与"根本没有兜底"完全一致（静默跳过）。
go test -count=1 ./internal/strategy_engine/ -run 'TestFetchDayKLine|TestStoreDayKLine|TestCachedKLine|TestLastCloseSkipsStoreLeg|TestApplyDayKLine' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# 链序锁（行号严格递增比文本断言可靠；沿用 54 的滤注释姿势）：腾讯 < 东财 < 库内 < 新浪。
K3_TC=$(grep -n 'GetTencentKLine(code, 120)' internal/strategy_engine/engine.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
K3_EM=$(grep -n 'GetKLine(code, "101", 120)' internal/strategy_engine/engine.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
K3_DB=$(grep -n 'e.storeDayKLine(code, prevClose)' internal/strategy_engine/engine.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
K3_SINA=$(grep -n 'GetSinaKLine(code, 120)' internal/strategy_engine/engine.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
[ -n "$K3_TC" ] && [ -n "$K3_EM" ] && [ -n "$K3_DB" ] && [ -n "$K3_SINA" ] \
	|| { echo "--- FAIL: 日K四条腿少了一条（§KLINE-CHAIN-3 链序锁失效）"; exit 1; }
[ "$K3_TC" -lt "$K3_EM" ] && [ "$K3_EM" -lt "$K3_DB" ] && [ "$K3_DB" -lt "$K3_SINA" ] \
	|| { echo "--- FAIL: 日K链序回退（必须 腾讯→东财→库内→不复权；东财不得被摘掉，也不得排到库内腿之后）"; exit 1; }
# 东财降级但仍在场：owner 裁决 1 明确"降到复权链末尾、不摘掉"，摘掉等于少一条独立复权源。
grep -q 'e.bumpKLineSrc("东财")' internal/strategy_engine/engine.go \
	|| { echo "--- FAIL: 东财复权腿被摘掉（owner 裁决是降级不是删除）"; exit 1; }
# 库内腿四道守卫（缺一道即"错锚/错量纲/陈旧K/当日半成品"进因子计算）。
grep -q 'raw\[len(raw)-1\].Date.Format("20060102") >= today' internal/strategy_engine/engine.go \
	|| { echo "--- FAIL: 守卫⓪（丢当日及未来日期的行）丢失——实时昨收锚当日半成品K会把今天涨幅摊进整条基准"; exit 1; }
grep -q 'scale := prevClose / last.Close' internal/strategy_engine/engine.go \
	|| { echo "--- FAIL: 守卫①（按实时昨收定锚归一）丢失——后复权价会整体抬高 LastClose/止损价"; exit 1; }
grep -q 'lotsToShares' internal/strategy_engine/engine.go \
	|| { echo "--- FAIL: 守卫②（库内 Vol 手→股）丢失"; exit 1; }
grep -q 'const storeBarsMaxStale' internal/strategy_engine/engine.go \
	|| { echo "--- FAIL: 守卫③（新鲜度上限）常量丢失——夜间同步断了会长期用旧K"; exit 1; }
grep -q 'if prevClose <= 0 {' internal/strategy_engine/engine.go \
	|| { echo "--- FAIL: 无锚（昨收取不到）不再拒用库内腿（宁可无兜底也不出错锚）"; exit 1; }
# 拒用必须留痕：noteStoreBarsRejected 至少覆盖 无历史序列/末根非法/scale 非法/过期 四类分支。
K3_REJ=$(grep -c 'noteStoreBarsRejected(' internal/strategy_engine/engine.go || true)
[ "$K3_REJ" -ge 5 ] || { echo "--- FAIL: 库内腿拒用留痕只剩 $K3_REJ 处（<5）——兜底静默失效不可见（§M-8/§N-6）"; exit 1; }
# 装配锁：库内腿靠注入取数，装配点漏了就是永久静默跳过（形态同"没有兜底"）。
grep -q 'strategyEngine.SetDayBarsLookup(dayBarsLookup.Lookup)' cmd/quant/main.go \
	|| { echo "--- FAIL: 库内日K读取器未注入引擎（兜底腿形同虚设且零报错）"; exit 1; }
grep -q 'func (l \*DayBarsLookup) Lookup' internal/engine/day_bars_lookup.go \
	|| { echo "--- FAIL: 库内日K读取器实现丢失"; exit 1; }
# 缓存锁：命中缓存时必须回查昨收锚（锚一天一换，沿用旧锚=整条基准错位）。
grep -q 'func (ent \*klineCacheEntry) cacheReusable(prevClose float64) bool' internal/strategy_engine/engine.go \
	|| { echo "--- FAIL: 日K缓存的锚校验函数丢失"; exit 1; }
grep -q 'ent.cacheReusable(prevClose)' internal/strategy_engine/engine.go \
	|| { echo "--- FAIL: 缓存命中路径不再校验昨收锚（跨轮换锚后仍拿旧序列）"; exit 1; }
# 负锁①：库内腿只读——兜底链里绝不允许出现写库调用（按「到下一个顶层 func」圈定函数体，
# 比固定行窗口可靠；滤注释行，防打死"为何只准读"的说明）。
if LC_ALL=C awk '/^func \(e \*Engine\) storeDayKLine/{f=1;next} /^func /{if(f)exit} f' internal/strategy_engine/engine.go \
	| grep -vE '^[[:space:]]*//' | grep -qE 'UPDATE |DELETE |INSERT |\.Exec\('; then
	echo "--- FAIL: 库内日K腿里出现写库调用（打分链取数只准读）"; exit 1; fi
# 负锁②：不复权兜底不得重新进 KLines（§H3 的收口成果不许被本批"加兜底"顺手抵消）。
if grep -nE 'md\.KLines = .*GetSinaKLine|md\.KLines = .*GetTHSKLine' internal/strategy_engine/engine.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: 不复权腿又直写 md.KLines（除权日因子失真复活，§H3）"; exit 1; fi
echo "ok - §KLINE-CHAIN-3 守卫通过（行为锁 1 组 + 链序锁 2 + 四道守卫锁 5 + 留痕计数锁 1 + 装配锁 2 + 缓存锁 2 + 负锁 2）"

echo "==> 83 §SIDE-AUTH-2 方向权威补漏：xt 直连同源作保 + side_unverified 契约字段 + 「待核对」通道 + 方向必填（2026-09-23 夜间批）..."
# 现象（09-22 实账 603468.SH）：一笔真实卖出在本地账里记成买入 → 回款不释放、当日预算被自己占满、
#       已实现盈亏恒 0，三本纪律账同时污染；而 §TRADE_SIDE（09-18）的方向权威只在**查到派发行**时成立。
# 根因：① 派发行查不到时，桥/xt 两条通道都拿"柜台枚举推断的方向"照常入库（fail-open）；
#       ② §TRADE_SIDE 只补在桥/HTTP 入口 _apply_trade，现网实盘主通道 xt 回调 on_trade 那条**根本没接**；
#       ③ Go 侧手动下单缺方向时缺省成买入（同族 fail-open 的第二处）。
# 前置：未证方向一律不入账本——网关落「待核对」通道（保留全部成交证据、不动持仓），
#       Go 侧留痕拒入账本并广播 side_unverified，历史错账走 §FILL-AMEND 人工勘误（见 84）。
go test -count=1 ./internal/server/ -run 'TestExecuteRejectsNonCanonicalSide|TestExecuteSellSideStillAccepted|TestReportSideUnverified|TestReportEnvelopeDecodesSideUnverified' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
python3 -m pytest qmt_gateway/tests/test_side_auth2.py qmt_gateway/tests/test_report_contract.py -q 2>&1 | tail -3
# 双通道同源锁：桥/HTTP 入口与 xt 直连入口必须各自做一次派发行作保，少一处就留半边 fail-open。
grep -q 'def _apply_trade' qmt_gateway/gateway.py \
	&& grep -q 'req\["side_unverified"\] = True' qmt_gateway/gateway.py \
	|| { echo "--- FAIL: 桥/HTTP 成交入口不再给未证方向打标记（§TRADE_SIDE 半边复活）"; exit 1; }
grep -q 'def _vouch_trade_side' qmt_gateway/handler.py \
	|| { echo "--- FAIL: xt 直连通道的派发行作保丢失（现网主通道 = 本批要补的那半边）"; exit 1; }
grep -q 'self.on_trade(self._vouch_trade_side(ev))' qmt_gateway/handler.py \
	|| { echo "--- FAIL: _vouch_trade_side 定义了却没接进 on_trade（死代码假修复）"; exit 1; }
# 「待核对」通道：未证方向的成交在网关本地账走第三态，绝不动持仓（动持仓=按猜的方向改资金账）。
grep -q 'fill_side = UNRESOLVED_STATUS if unverified else' qmt_gateway/store.py \
	|| { echo "--- FAIL: 未证方向的成交没有落「待核对」通道"; exit 1; }
# Go 侧接收：缺方向的回报留痕拒入账本，并按契约回 ok+side_unverified（回 ok=1 而不留痕=假成功）。
grep -q 'SideUnverified bool' internal/server/qmt.go \
	|| { echo "--- FAIL: 回报信封不再解析 side_unverified（契约字段单方面消失，网关标记被吞）"; exit 1; }
grep -q 'writeJSON(w, 200, map\[string\]string{"ok": "1", "side_unverified": "1"})' internal/server/qmt.go \
	|| { echo "--- FAIL: side_unverified 回报的响应契约回退（网关侧幂等判定会失据）"; exit 1; }
# 方向必填：空串**不再**缺省成买入，而是 400 拒单 + 安全审计留痕（非规范值 buy/SELL/单字"买"仍走 §SIDEGATE-GO 白名单拒）。
grep -q 'writeError(w, 400, "缺少下单方向(side)：必须显式传 买入/卖出")' internal/server/qmt.go \
	|| { echo "--- FAIL: 缺方向的手动下单不再被 400 拒（方向必填复活成缺省买入）"; exit 1; }
grep -q 'opslog.Audit("live_order_side_missing"' internal/server/qmt.go \
	|| { echo "--- FAIL: 缺方向拒单不留痕（谁在漏传 side 无从追查）"; exit 1; }
if grep -nE 'if side == "" \{\s*side = "买入"|side = "买入" // 缺省' internal/server/qmt.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: 手动下单又给空方向缺省成买入（§SIDE-AUTH-2 残余 fail-open 复活）"; exit 1; fi
# 前端镜像防线：封装层 refuse-to-send（方向非 买入/卖出 一个请求都不发）。
grep -q "body.side !== '买入' && body.side !== '卖出'" web/src/api/index.js \
	|| { echo "--- FAIL: 前端 executeRealAction 方向守卫丢失（后端 400 的镜像防线）"; exit 1; }
echo "ok - §SIDE-AUTH-2 守卫通过（行为锁 2 套件 + 双通道同源锁 3 + 通道锁 1 + 契约锁 2 + 必填正锁 2 + 缺省负锁 1 + 前端锁 1）"

echo "==> 84 §FILL-AMEND 历史错账勘误通道：追加式决定 + fills_effective 单点收敛 + 只读守恒自检 + 前端逐笔入口（2026-09-23 夜间批，owner 裁决 4）..."
# 现象：84 的前半段（§SIDE-AUTH-2）只挡住"往后不再记错"，09-22 那条已经落错方向的行**不会自动改**——
#       成交是资金事实，任何"按猜测自动重放历史账本"都可能把对的改成错的。
# 修法：只追加、不覆写。人工逐笔勘误（提交=待批准影子条目，账不动 → 批准=读取侧方向生效 → 撤销=回原始方向），
#       所有按方向取数的口径统一走视图 fills_effective；账本守恒自检只报差异线索、绝不平账。
# 前置（顺序有讲究）：视图定义引用 fills.trade_id，该列对旧库靠 ALTER 补 → 建视图必须排在列补齐之后，
#       否则 Open 直接失败、服务起不来；一笔成交最多一条活跃勘误 → 靠两个 partial unique index 兜住
#       （少了它，两条 applied 会让视图把一行扇成两行，买入笔数/金额直接翻倍而原始 fills 一字未动）。
go test -count=1 ./internal/store/ -run 'TestFillAmendment|TestFillEffectiveNoFanOut|TestConservation' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ -run 'TestFillAmendment' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# 迁移顺序锁：列补齐(trade_id) 必须早于 migrateFillAmendments 调用。
K4_COL=$(grep -n 'ALTER TABLE fills ADD COLUMN trade_id' internal/store/store.go | head -1 | cut -d: -f1 || true)
K4_VIEW=$(grep -n 'd.migrateFillAmendments()' internal/store/store.go | head -1 | cut -d: -f1 || true)
[ -n "$K4_COL" ] && [ -n "$K4_VIEW" ] || { echo "--- FAIL: 找不到 trade_id 补列或建视图调用（§FILL-AMEND 顺序锁失效）"; exit 1; }
[ "$K4_COL" -lt "$K4_VIEW" ] || { echo "--- FAIL: 建视图排到了补列之前（旧库 Open 失败、服务起不来）"; exit 1; }
# 视图不扇行的两道保障（active 与 applied 各一对；谓词含 'revoked' 时 SQLite 推不出 JOIN 至多一行）。
K4_IDX=$(grep -c 'CREATE UNIQUE INDEX IF NOT EXISTS idx_fa_' internal/store/fill_amendments.go || true)
[ "$K4_IDX" -ge 4 ] || { echo "--- FAIL: 勘误唯一索引只剩 $K4_IDX 个（<4）——视图可能把一笔成交扇成多行" ; exit 1; }
grep -q "DROP VIEW IF EXISTS fills_effective" internal/store/fill_amendments.go \
	|| { echo "--- FAIL: 视图不再是 DROP+CREATE（旧定义会被 IF NOT EXISTS 永久钉死）"; exit 1; }
# 收敛点锁：按方向取数的六个读取口必须全部读视图（漏一个=同一笔改判在两处给出互相矛盾的数字）。
K4_VIEWED=$(grep -rc 'FROM fills_effective' internal/store/*.go | LC_ALL=C awk -F: '{s+=$2} END {print s+0}' || true)
[ "$K4_VIEWED" -ge 6 ] || { echo "--- FAIL: 读视图的口径只有 $K4_VIEWED 处（<6）——纪律闸/成交簿出现分叉" ; exit 1; }
for f in risk_gates.go real_positions.go settlement.go; do
	grep -q 'fills_effective' internal/store/$f || { echo "--- FAIL: internal/store/$f 未接生效方向视图"; exit 1; }
done
# 反方向保障：幂等判重必须**留在原始 fills**（锚在柜台证据上），改走视图会让勘误生效后的同笔回报判不出重复。
grep -q "SELECT COUNT(\*) FROM fills WHERE trade_id=?" internal/store/real_positions.go \
	|| { echo "--- FAIL: ApplyRealFill 的 trade_id 判重不再查原始 fills（双倍记账入口）"; exit 1; }
grep -q "SELECT COUNT(\*) FROM fills WHERE order_id=? AND traded_at=? AND price=? AND qty=?" internal/store/settlement.go \
	|| { echo "--- FAIL: FillExists 的事实键判重不再查原始 fills"; exit 1; }
# 端点收口：五条路由全在 adminMiddleware 下（实盘账本写端点，§M-14 同口径）。
for r in 'GET /api/qmt/fill-amendments' 'POST /api/qmt/fill-amendments' 'POST /api/qmt/fill-amendments/{id}/apply' 'POST /api/qmt/fill-amendments/{id}/revoke' 'GET /api/qmt/fills/conservation'; do
	grep -qF "s.mux.HandleFunc(\"$r\", s.adminMiddleware(" internal/server/server.go \
		|| { echo "--- FAIL: $r 未挂 adminMiddleware（勘误是资金账写端点）"; exit 1; }
done
# 锚点单源：提交体只有 fill_id（前端自报锚点=可造匹配不到成交的死勘误）。
grep -q 'FillID  int64  `json:"fill_id"`' internal/server/fill_amendments.go \
	|| { echo "--- FAIL: 勘误提交体不再是「只带 fill_id」"; exit 1; }
grep -q 'db.RawFillForUser(uid, req.FillID)' internal/server/fill_amendments.go \
	|| { echo "--- FAIL: 未先按归属读原始行（越权面 + 锚点来源不唯一）"; exit 1; }
# 守恒自检只读：整份实现不许出现写账语句（"顺手让它自动修"是本条要防的那类改动；滤注释行）。
K4_CONS_FN=$(grep -n 'func (d \*DB) CheckBookConservation' internal/store/fill_conservation.go | head -1 | cut -d: -f1 || true)
[ -n "$K4_CONS_FN" ] || { echo "--- FAIL: 守恒自检实现丢失"; exit 1; }
if grep -nE '^\s*(d\.db|tx)\.(Exec|Begin)' internal/store/fill_conservation.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | grep -q .; then
	echo "--- FAIL: fill_conservation.go 出现写库调用（守恒自检只准 SELECT）"; exit 1; fi
# 前端逐笔入口（owner 裁决"连勘误入口一起上"）：面板挂载 + 流水行有改判按钮 + 提交只带三键。
grep -q '<FillAmendPanel' web/src/pages/Quant.jsx \
	|| { echo "--- FAIL: 勘误面板未挂进量化交易页（后端有通道而没人能用）"; exit 1; }
grep -q "onClick={() => setAmendTarget(row)}" web/src/pages/Quant.jsx \
	|| { echo "--- FAIL: 成交流水行的「改判」入口丢失"; exit 1; }
grep -q 'data: { fill_id: fillId, new_side: newSide, reason }' web/src/api/index.js \
	|| { echo "--- FAIL: 前端提交体又带上锚点字段"; exit 1; }
grep -q 'async function transition(a, action)' web/src/components/FillAmendPanel.jsx \
	|| { echo "--- FAIL: 批准/撤销共用的处置入口丢失（两态各写一份迟早只改一份）"; exit 1; }
echo "ok - §FILL-AMEND 守卫通过（行为锁 3 组 + 顺序锁 1 + 视图保障锁 2 + 收敛锁 4 + 判重反向锁 2 + 路由锁 5 + 锚点锁 2 + 只读负锁 1 + 前端锁 4）"

echo "==> 85 §FILL-AMEND 只读取证脚本：现网证据四道只读机制 + 降级不报成功（2026-09-23 夜间批）..."
# 为什么先取证再改判：09-22 那批错账的"该改成什么方向"只有三方证据能回答——
#       本地 fills / 网关 fills / 网关日志的 dispatch 方向 / 柜台回报。少了 dispatch 这一方就下判语，
#       等于把"我猜"写成"人工已核对"，而勘误通道是全权信任人工输入的。
# 纪律：只走现网已有的 sqlite3 只读查询 + 拷副本，绝不 SSH 手敲、绝不在原库上加锁或写任何东西；
#       拿不到证据（缺 sqlite3 / 网关库没拷到 / 查询报错）一律降级为 INCONCLUSIVE 或非 0 退出，
#       绝不"没取到也报成功"（§M-8/§N-6）。
grep -q 'FORENSIC_FILL_DONE' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 取证脚本的完成锚点丢失（跑没跑完无法判定）"; exit 1; }
grep -q 'sqlite3 "file:$1?mode=ro"' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 本地副本不再以 mode=ro 打开（只读第一道机制丢失）"; exit 1; }
grep -q "('-readonly', \$dbPath" scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 远端 sqlite3 调用丢了 -readonly（现网库第二道只读机制丢失）"; exit 1; }
grep -q 'trap cleanup EXIT' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 临时副本清理未挂 trap（现网留残留库文件）"; exit 1; }
grep -q '^audit_sql() {' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 装配 SQL 的词法复核函数丢失"; exit 1; }
grep -q 'run_round2_dispatch() {' scripts/forensic_fill.sh \
	&& grep -q 'run_round2_dispatch || exit $?' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 第二轮 dispatch 取数没有接进主流程（权威方向那一路证据是死代码）"; exit 1; }
# 方向 token 化必须排在 printable 过滤**之前**（C locale 的 [:print:] 不含 CJK 字节，顺序颠倒会把
# dispatch=卖出 洗成 dispatch=，制造"日志里没有方向"的假线索）。
grep -q '| side_tok | LC_ALL=C tr -cd' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 中文方向 token 化没有排在 printable 过滤之前（方向会被洗成空）"; exit 1; }
# UNKNOWN 不得被当成"存在冲突记录"（判据取不到值时宁可少说，不可把缺数据读成结论）。
grep -q 'cnt_gt0() { case "${1:-}"' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 计数判据不再区分 UNKNOWN（缺数据会被判成有冲突）"; exit 1; }
# 五档保守判语齐备（少一档就会用别的档凑答）。
for w in MISLABEL_SUSPECT NO_DISPATCH_ROW UNDETERMINED INCONCLUSIVE CONSISTENT; do
	grep -q "$w" scripts/forensic_fill.sh || { echo "--- FAIL: 判语 $w 丢失"; exit 1; }
done
# 预检三件套：缺 sqlite3/sha256sum/awk 直接非 0 退出（不静默跳过）。
grep -q 'need_cmd sqlite3 || exit 3' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 工具闸缺失（没有 sqlite3 会一路降级成"看起来跑完了"）"; exit 1; }
# ── 兜底取数腿（2026-09-24 补）：09-23 21:45 现网首跑退 3 的根因是远端 PATH 没有 sqlite3.exe，
#       不是权限也不是通道。取证要 owner 手给路径 = 改判链条卡在工具上。同一台机器本来就在跑
#       Python（qmt_gateway/pydata），标准库自带 sqlite3，用现成能力当只读客户端。
#       这条锁锁的是"兜底腿的只读强度不低于主腿"，不是锁它存在——少了任何一道闸就等于
#       往现网副本里开了一个可写口。
grep -q "sqlite3.connect('file:%s?mode=ro' % db, uri=True" scripts/forensic_fill.sh \
	|| { echo "--- FAIL: Python 兜底腿没有用 mode=ro URI 打开副本（与主腿只读强度不再持平）"; exit 1; }
grep -q "if stmt.count(';') != 1 or not stmt.endswith(';')" scripts/forensic_fill.sh \
	|| { echo "--- FAIL: Python 兜底腿的「恰好一条语句」闸丢失（可夹带第二条写语句）"; exit 1; }
grep -q "if stmt.split(None, 1)\[0\].lower() != 'select'" scripts/forensic_fill.sh \
	|| { echo "--- FAIL: Python 兜底腿的「首词必须是 select」闸丢失"; exit 1; }
# 两腿都在位时必须走主腿； SQLITE_MISSING 只能在"两条腿都没有"时打——若回退成按 $ver 判定，
# 就等于恢复 09-23 那个"缺 sqlite3.exe 就交不出证据"的老死路。
grep -q "if (-not \$execMode) { Write-Output 'SQLITE_MISSING'" scripts/forensic_fill.sh \
	|| { echo "--- FAIL: SQLITE_MISSING 不再按「两腿皆不可用」判定（兜底腿失效或又被当成硬前置）"; exit 1; }
grep -q "Write-Output ('EXEC sqlite|'" scripts/forensic_fill.sh \
	&& grep -q "Write-Output ('EXEC python|'" scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 取数腿不再自报走了哪条（owner 看日志无法判断证据出自哪条腿）"; exit 1; }
# 兜底腿脚本要真的上传到远端，否则 EXEC python 只能以"文件不存在"失败收场。
grep -q 'files+=("$TMP/sqlrunner.py")' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: sqlrunner.py 没有进上传清单（Python 腿是生成出来的死代码）"; exit 1; }
# 两腿"产物同源/判语同源"三条锁（都是 2026-09-24 实测锤出来的，不是推的）：
#   a) 写库关键字按整词比对——列名 updated_at 含子串 update，子串匹配会让兜底腿把持仓/账户
#      两方证据降级成 UNKNOWN，主腿却正常出数（本地夹具实测复现）；
#   b) out 与 err 两个产物在 main 入口就建出来——CLI 腿靠重定向**成功也生成空 err**，
#      兜底腿若只在出错时建 err，"查询成功"反倒缺文件，上游 out+err 成对检查直接判断链：
#      08:05 现网首跑 7 条查询全 RAN、out 全部取回，就因 7 个 err 不存在而退 5；
#   c) 0 行结果 CLI 连表头都不打（实测零字节），兜底腿若坚持写表头，`[ -s out_xxx ]`
#      这类"该方是否取到数"的判据在两腿间给出不同答案。
grep -q 'for tok in _re.findall' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 兜底腿的写库关键字检查退回子串匹配（updated_at 会被误拒）"; exit 1; }
grep -q "open(outf, 'w', encoding='utf-8').close()" scripts/forensic_fill.sh \
	&& grep -q "open(errf, 'w', encoding='utf-8').close()" scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 兜底腿不再成对预建 out/err 产物（查询成功会被缺项检查判成断链）"; exit 1; }
grep -q 'if rows:' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: 兜底腿的 0 行输出不再与 CLI 对齐（空结果也会带表头，下游 -s 判据两腿不同源）"; exit 1; }
# 副本拷贝状态行必须能进日志（09-24 现网日志实测：整轮没有一行 STAGED/MISSING，因为
# `$haveLive = Stage ...` 把函数里的 Write-Output 全吸进变量了）。这条锁的不是写法好看，
# 是两条判据的可信度：grep 'STAGED gw.db' 决定网关证据算不算"有"，grep 'MISSING live.db'
# 决定"现网库拷不出来"是判失败还是静默继续——两者在旧写法下恒为假（前者永远降级、后者永远失明）。
grep -q "\$script:stageMsgs += ('STAGED ' + \$name)" scripts/forensic_fill.sh \
	&& grep -q 'foreach ($m0 in $script:stageMsgs) { Write-Output $m0 }' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: Stage 的状态行又回到函数内 Write-Output（会被赋值吞掉，拷库判据恒假）"; exit 1; }
if grep -qE "Write-Output \('(STAGED|REUSED|MISSING|STAGE_FAIL) ' \+ \\\$name\)" scripts/forensic_fill.sh; then
	echo "--- FAIL: Stage 函数体内仍有 Write-Output 状态行（与上一条锁同源，日志会丢拷库证据）"; exit 1; fi
# 负锁：脚本不得内嵌任何写语句或凭据值（只读 + 零新增凭据）。
if grep -nE '^[[:space:]]*(INSERT|UPDATE|DELETE|DROP|VACUUM) ' scripts/forensic_fill.sh | grep -vE '^[0-9]+:[[:space:]]*#' | grep -q .; then
	echo "--- FAIL: 取证脚本出现写库语句（本脚本只准 SELECT）"; exit 1; fi
if grep -qE 'Password|ConvertTo-SecureString' scripts/forensic_fill.sh; then
	echo "--- FAIL: 取证脚本出现口令原语（纪律：绝不新增凭据，SSH 复用既有 key）"; exit 1; fi
# 失败必须自带原因（09-24 现网第一次退 5 只说"缺 6 个"，靠再跑一遍才看清缺的是 err_*）：
# 缺项要逐个列名、已取回的 err 要回显首行、取数腿要自报，否则每一轮排查都是一次现网往返。
grep -q '缺项:${miss_list}' scripts/forensic_fill.sh \
	&& grep -q '取数腿自报' scripts/forensic_fill.sh \
	|| { echo "--- FAIL: live 侧缺项失败路径不再回显原因（每轮排查都要重跑一次现网）"; exit 1; }
# 兜底腿负锁：Python 侧只准 execute/fetchall（注释里"不 executescript、不 commit"的说明会被
# 字面命中，故先按行号剔注释）。出现 executescript/commit 即等于给只读腿开了写口。
if grep -nE '\.executescript\(|\.commit\(' scripts/forensic_fill.sh | grep -vE '^[0-9]+:[[:space:]]*#' | grep -q .; then
	echo "--- FAIL: Python 兜底腿出现 executescript/commit（只读三闸之外的写口）"; exit 1; fi
echo "ok - §FILL-AMEND 取证脚本守卫通过（锚点锁 1 + 只读机制锁 4 + 接线锁 3 + 判语锁 6 + 工具闸锁 1 + 兜底腿锁 9 + 副本状态锁 2 + 写库/凭据/写口负锁 3）"

echo "==> 86 §SIGID-TRUNC 成交回报 signal_id 被柜台截到 24 字符：写路径单一截断点 + 读路径第四级归因 + Go 两边兼容（2026-09-24，owner 裁决 1）..."
# 现象（现网实录，2026-09-24 用 scripts/forensic_fill.sh 2026-09-22 603468.SH 取证锤实）：
#   fills 两行 signal_id = `buy:603468:fac_1:2026092`（24 字符，末位"2"没了），
#   同票 orders/dispatch 行 = `buy:603468:fac_1:20260922`（25 字符完整）。
#   引擎编号 = `buy:<码>:<因子>:<TradingDayDate()` 紧凑 8 位日>`，恰好踩在柜台 userOrderId 槽的
#   24 字符上限上 ⇒ **凡带日期买入编号的成交，落库即残缺**。
# 三处静默失效（一条残缺编号同时打断三条链，全都不报错）：
#   ① 网关三级派发行回查（seq→交易所委托号→signal_id 精确）恒落空 → 方向权威 §SIDE-AUTH-2 判
#      "未证"，现网每一笔真成交都掉进 side_unverified/待核对（09-22"卖出记成买入"事故的源头解释）；
#   ② 桥 embed_resolve / resolve_order_id 用 `remark == signal_id` 等值比对归属委托，柜台回的
#      是截断值 → 比对恒假，静默降级成"价格+数量指纹猜"；
#   ③ Go 侧 SumFilledQty 与 ResetFailedRealOrder 的"已撤+零成交可重放"按单向前缀 LIKE 配对，
#      残缺行永不命中 → 部成量算 0（补卖叠加发单、卖出敞口超额）、已部成的撤单被判零成交
#      （同键**再发一次真单**＝凭空敞口）。
# 修法取舍（owner 给的约束就是决策依据）：编号是**已写进资金账本的事实主键**，fills 判重索引与
#   §FILL-AMEND 勘误台账都挂在它上面 → 回填历史（改长度）会让新旧行分裂、可能同一笔双倍记账。
#   因此：**历史行走读取端两边兼容（不回填、不动 fills 一列）**，**新行在写路径收口**（桥以
#   wire_ref 为唯一截断点、网关以第四级前缀唯一归因还原成完整编号后入账）。
python3 -m pytest qmt_gateway/tests/test_sigid_trunc.py -q 2>&1 | tail -3
go test -count=1 ./internal/store/ -run 'TestSumFilledQtyMatchesCounterTruncatedID|TestSumFilledQtyReverseLegNeedsSameDay|TestSumFilledQtyEmptySignalIDIsZero|TestResetCancelledWithTruncatedFillNotReplayable' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# ① 截断只准有一处：一个常量 + 一个函数（三处截断点各写一份 = 上限变更时必然漏改一处）。
grep -q 'WIRE_REF_MAX = 24' qmt_gateway/qmt_bridge_strategy.py \
	|| { echo "--- FAIL: 线上截断上限常量丢失（24 这个事实又散回字面量）"; exit 1; }
wt=$(grep -c 'return str(signal_id or "")\[:WIRE_REF_MAX\]' qmt_gateway/qmt_bridge_strategy.py || true)
[ "$wt" = "1" ] || { echo "--- FAIL: 截断函数体不是唯一一处（计数=${wt}，出现两处就有第二份口径）"; exit 1; }
# ② 三个调用点全部过 wire_ref（下单侧截断、归属比对侧同样截断，两侧口径同源）。
grep -q 'signal_id = wire_ref(req.get("signal_id", ""))' qmt_gateway/qmt_bridge_strategy.py \
	|| { echo "--- FAIL: embed_place 不再用 wire_ref 生成线上传值（下单侧与回查侧口径分叉）"; exit 1; }
grep -q 'if sig and remark == wire_ref(sig):' qmt_gateway/qmt_bridge_strategy.py \
	|| { echo "--- FAIL: embed_resolve 的 remark 比对退回原值（截断值 vs 完整值恒假，静默降级指纹猜）"; exit 1; }
grep -q 'if remark == wire_ref(signal_id):' qmt_gateway/qmt_bridge_strategy.py \
	|| { echo "--- FAIL: resolve_order_id 的 remark 比对退回原值（同上一条静默失效）"; exit 1; }
# 负锁：字面 `[:24]` 与裸等值比对都不得在文件里复活（旧写法本身即本批修的三处失效之一）。
if grep -nE '\[:24\]' qmt_gateway/qmt_bridge_strategy.py | grep -vE '^[0-9]+:[[:space:]]*#' | grep -q .; then
	echo "--- FAIL: 桥里又出现字面 [:24] 截断（绕过 WIRE_REF_MAX 单一截断点）"; exit 1; fi
if grep -nE 'remark == signal_id' qmt_gateway/qmt_bridge_strategy.py | grep -vE '^[0-9]+:[[:space:]]*#' | grep -q .; then
	echo "--- FAIL: 又拿截断前的 signal_id 与柜台 remark 直接等值比对（恒假归属）"; exit 1; fi
# ③ 第四级归因（store）：必须 substr 逐字前缀而不是 LIKE（编号里有 `_`，LIKE 当通配符 ⇒ 误配），
#    且候选不一致时返回 ambiguous 而不是"取最新一条"。
grep -q "sql = \[\"kind = 'order'\", \"substr(signal_id, 1, length(?)) = ?\"\]" qmt_gateway/store.py \
	|| { echo "--- FAIL: 前缀反查不再用 substr 逐字比对（LIKE 会把 signal_id 里的下划线当通配符）"; exit 1; }
grep -q 'return None, "ambiguous"' qmt_gateway/store.py \
	|| { echo "--- FAIL: 前缀候选不一致时不再报歧义（跨日塌成同前缀时取最新=张冠李戴）"; exit 1; }
# ④ 网关第四级回落：只在前三级落空时才用（不得抢在精确查前面），歧义"不猜"，还原要留痕。
grep -q '_sig_wire = "" if drow else str(req.get("signal_id", "") or "")' qmt_gateway/gateway.py \
	|| { echo "--- FAIL: 第四级前缀归因不再让位于精确查（弱归因覆盖强归因）"; exit 1; }
grep -q 'if _drow is None and _why == "ambiguous":' qmt_gateway/gateway.py \
	|| { echo "--- FAIL: 歧义分支丢失（歧义时会被当成"查不到"静默继续，或反之当成命中乱改编号）"; exit 1; }
grep -qF 'req["signal_id"] = _full or _sig_wire' qmt_gateway/gateway.py \
	|| { echo "--- FAIL: 命中后不再把编号还原成派发项完整值（账本继续落残缺编号，判重键分裂）"; exit 1; }
# ⑤ Go 读取端两边兼容：谓词单一事实源 + 两处钱查询共用 + 反向腿带交易日闸 + 空编号判 0。
grep -q "instr(%s, replace(substr(%s,1,10),'-','')) > 0" internal/store/real_positions.go \
	|| { echo "--- FAIL: 反向腿的交易日闸丢失（跨两日塌成同前缀时会把别日的成交算进这笔）"; exit 1; }
gm=$(grep -c 'func fillSignalMatchSQL' internal/store/real_positions.go || true)
[ "$gm" = "1" ] || { echo "--- FAIL: 配对谓词构造函数不是唯一一处（计数=${gm}）"; exit 1; }
gu=$(grep -c 'fillSignalMatchSQL(' internal/store/real_positions.go || true)
# 1 处定义 + 2 处调用（SumFilledQty / ResetFailedRealOrder）：少一处调用就等于两条资金查询又各写一份 SQL。
[ "$gu" = "3" ] || { echo "--- FAIL: 配对谓词调用点 ≠ 2（计数=$gu 含定义行；两处钱查询必须同源）"; exit 1; }
grep -q 'eligibleWhere := `signal_id=? AND user_id=? AND (status=' internal/store/real_positions.go \
	|| { echo "--- FAIL: 「已撤+零成交可重放」的谓词不再现算配对 SQL（退回编译期常量即漏掉反向腿）"; exit 1; }
if grep -qE 'const eligibleWhere' internal/store/real_positions.go; then
	echo "--- FAIL: eligibleWhere 又变回 const（常量拼不进运行期双向谓词）"; exit 1; fi
grep -q 'if strings.TrimSpace(signalID) == "" {' internal/store/real_positions.go \
	|| { echo "--- FAIL: 空编号不再直接判 0（前缀口径下 LIKE '%%' 会把全账成交算成这一笔）"; exit 1; }
# 负锁：读取端兼容**不得**演变成写历史行（回填即 owner 明令禁止的双倍记账风险）。
if grep -nE 'UPDATE fills[[:space:]]+SET[[:space:]]+signal_id' internal/store/*.go | grep -vE ':[[:space:]]*//' | grep -q .; then
	echo "--- FAIL: 出现回填 fills.signal_id 的写语句（本批裁决=不回填，只读端兼容）"; exit 1; fi
echo "ok - §SIGID-TRUNC 守卫通过（行为锁 2 套件 + 单一截断点锁 3 + 调用点锁 3 + 歧义锁 2 + 回落锁 3 + Go 兼容锁 6 + 字面量/回填负锁 3）"

echo "==> 87 §MOMENTUM-LIVE-REPLAY 动量判据按实盘语义重写（兜底互斥 + 当日撮合 + 只计买入档）（2026-09-24，owner 三选一拍板「按实盘语义重写判据」）..."
# 现象（就是 §SURVEY-COVERAGE 立盲区锚点时点名的那一个）：动量在实盘白名单里能下单，但 btreplay 的
#   momentumAdapter.Trigger 是个恒不触发的停用桩——回放框架的三条前提（收盘判一次 / 次日开盘入场 /
#   各战法独立跑）与实盘的三条（盘中触发那一刻进池 / 当日撮合 / 「前四战法均未出信号才兜底」）
#   逐条对不上，硬放行量出来的不是"实盘由动量下单的那批票"的表现。
# 为什么不能直接改数字：把 Trigger 改活、按次日开盘入场，会得到一批实盘根本轮不到下单的单子，
#   排摸表和扫参冠军参数同时被污染（owner 因此否掉"放行近似"和"承认量不了"两条路）。
# 本批落地的三条（可精确重放，不掺近似）：
#   ① 兜底互斥 FallbackTier()：同标的当日任一兄弟战法出过信号即丢弃这条动量信号；回查用兄弟的
#      **裸判据** Trigger——实盘 agent.go:1208 数的是 sigs 条数（信号），不是成交量；
#   ② 当日撮合 SameDayEntry()：entryIdx=i、入场价=触发当日收盘（缺省战法仍 entryIdx=i+1 次日开盘）；
#   ③ 只计买入档：Trigger 只在 score ≥ 买入档（出厂 75）时为真，落在观察档 60 只观察不算交易信号。
# 数据不支持、只能标注不能伪造的三条残余近似（研究库无分钟 K 落库）：5 分钟 MACD 用日线 MACD 替代、
#   盘中 N 轮判成每日 1 轮（只会漏触发，不会凭空多触发）、跨轮"动量提升"门不可重建。偏差方向写进
#   momentumAdapter 注释与 ReplayApproxNote("momentum") 出门文本：兄弟用裸判据 ⇒ 占用日偏多
#   ⇒ 动量入场数偏少，是保守方向的偏差。
# 顺带收口：散在两处的手写类型分支改为 dayScoped 可选接口（prepareStock/setDay），§RFIX-1"MACD 序列
#   必须逐股重算 + 游标推进"的教训从此由装配点统一保证，不再依赖每处记得写。
go test -count=1 ./internal/btreplay/ -run 'TestMomentumLiveSemanticsInReplay|TestMomentumFallbackExclusivityInSweepPrecompute|TestMomentumScoreDayBuyThresholdOnly|TestMomentumDeclaresLiveCapabilities|TestMomentumRegisteredAndSurveyable|TestSetFallbackPeersKeepsOnlySiblings|TestEntrySlipAtNextDayEqualsEntrySlip|TestEntrySlipAtSameDayCloseEntry' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./cmd/research/ -run 'TestStrategySurveyArtifact' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go test -count=1 ./internal/server/ -run 'TestLiveWhitelistFormsMatchSurveyCoverage' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
# ① 两条实盘能力必须由动量自己声明：不声明＝门控根本不启动，动量会被当成与四形态并列的独立战法跑。
grep -q 'func (a \*momentumAdapter) FallbackTier() bool { return true }' internal/btreplay/replay.go \
	|| { echo "--- FAIL: 动量不再声明 FallbackTier（兜底互斥失效，会抢走兄弟战法的名额）"; exit 1; }
grep -q 'func (a \*momentumAdapter) SameDayEntry() bool { return true }' internal/btreplay/replay.go \
	|| { echo "--- FAIL: 动量不再声明 SameDayEntry（退回次日开盘入场，实盘当日撮合失真）"; exit 1; }
# ② Trigger 必须真接打分判据（此前它是停用桩）；判据本体 scoreDay 不许被绕过或另起一份。
grep -q 'return a.scoreDay(klines, prevClose)' internal/btreplay/replay.go \
	|| { echo "--- FAIL: momentumAdapter.Trigger 不再走 scoreDay（恒不触发的停用桩复活）"; exit 1; }
# ③ 兜底互斥与当日撮合必须**两处同门**：回放主循环与扫参预计算漏任一处，网格就会给一条实盘轮不到
#    下单的路径寻优，选出的"冠军参数"对应的是一批实盘不存在的单子。
pbc=$(grep -c 'o.fallbackBlockedByPeer(' internal/btreplay/replay.go internal/btreplay/sweep.go | awk -F: '{s+=$2} END{print s}' || true)
[ "$pbc" = "2" ] || { echo "--- FAIL: 兜底互斥回查调用点 ≠ 2（回放/扫参各一处，计数=${pbc}）"; exit 1; }
sdc=$(grep -c 'ad.(sameDayEntryAdapter)' internal/btreplay/replay.go internal/btreplay/sweep.go | awk -F: '{s+=$2} END{print s}' || true)
[ "$sdc" = "2" ] || { echo "--- FAIL: 当日撮合分支不是两处都有（两条路径入场口径分叉，计数=${sdc}）"; exit 1; }
esc=$(grep -c ', i, entryIdx)' internal/btreplay/replay.go internal/btreplay/sweep.go | awk -F: '{s+=$2} END{print s}' || true)
[ "$esc" = "2" ] || { echo "--- FAIL: 入场定档没有按 entryIdx 传（滑点/可成交性仍按信号日+1 算，计数=${esc}）"; exit 1; }
# ③b 单独跑动量时没有兄弟可回查＝这道门整体不存在，数字必须自带口径警告（否则两张同名表差几倍
#     没人知道警告在哪一步丢了）。
grep -q '兜底档战法单独回放' internal/btreplay/replay.go \
	|| { echo "--- FAIL: 无兄弟清单时的口径警告丢失（动量单独回放会静默偏高）"; exit 1; }
# ④ 入场定档必须按**入场日**取流动性滑窗与可成交性；同时 entrySlip 仍是"次日开盘"薄壳——
#    四形态战法的既有回测数字必须逐字节不变（重写只动动量，不许顺手搬家历史数字）。
grep -q 'avg := avgAmountWan(kls, entryIdx-1)' internal/btreplay/cost.go \
	|| { echo "--- FAIL: 流动性滑窗不再按入场日取（当日撮合的票会看错一天的成交额）"; exit 1; }
grep -q 'return sc.entrySlipAt(code, kls, i, i+1)' internal/btreplay/cost.go \
	|| { echo "--- FAIL: entrySlip 不再是次日入场薄壳（历史回测数字会被这次重写带跑）"; exit 1; }
# ⑤ 出场引擎第 3 参数语义已由"信号日"改成"入场日"：旧签名委托须补 +1、扫参模拟须传 entryIdx，
#    漏一侧就是所有回放的持仓期整体错一天（胜率/平均盈亏全变而无人察觉）。
grep -q 'uniformExitV2Full(kls, "", sigIdx+1,' internal/btreplay/sweep.go \
	|| { echo "--- FAIL: 旧签名委托不再补 +1（信号日被当成入场日＝持仓期整体前移一天）"; exit 1; }
grep -q 'uniformExitV2Full(klines\[t.code\], t.code, t.entryIdx,' internal/btreplay/sweep.go \
	|| { echo "--- FAIL: 扫参模拟没按 entryIdx 出场（当日撮合的动量仍按次日开盘结算）"; exit 1; }
# ⑥ 盲区机制必须原样保留：动量这一格归零 ≠ 这条链可以拆。下一个进白名单却没有适配器（或适配器
#    又因语义对不上须停用）的战法，仍只能靠它显形。
grep -q '"adapter_disabled_by_default"' internal/btreplay/replay.go \
	|| { echo "--- FAIL: 未排摸状态分类被摘掉（缺代码与缺语义两种处置重新压成一个信号）"; exit 1; }
grep -q 'survey_unsurveyable=' cmd/research/survey.go \
	|| { echo "--- FAIL: 排摸盲区锚点行被删（「没量到」重新长得和「没问题」一样）"; exit 1; }
awk '/^func DefaultDisabledBuiltins/,/^}/' internal/btreplay/replay.go | grep -q 'return nil' \
	|| { echo "--- FAIL: DefaultDisabledBuiltins 不再返回空集（动量被重新停用＝盲区回来了）"; exit 1; }
# 负锁：旧的"默认不回放"出门文本不得复活——这一行现在是真量过的数字，留着旧文本会被运维读回
#   "没量"，与 §SURVEY-COVERAGE 要防的静默降级同形（只是方向反过来）。
if grep -q 'not replayed by default' internal/btreplay/replay.go; then
	echo '--- FAIL: 动量近似说明里又出现「not replayed by default」（与已接入回放的事实矛盾）'; exit 1; fi
grep -q 'criteria rewritten to live semantics' internal/btreplay/replay.go \
	|| { echo "--- FAIL: 动量近似说明不再声明「判据已按实盘语义重写」（数字失去口径出处）"; exit 1; }
echo "ok - §MOMENTUM-LIVE-REPLAY 守卫通过（行为锁 3 套件 + 能力声明锁 2 + 判据锁 1 + 两处同门锁 3 + 口径警告锁 1 + 入场定档锁 2 + 引擎参数锁 2 + 机制保留锁 3 + 旧文本负锁 1）"

echo "==> 88 §SIGNAL-DIST 部署面信号分布探针：判红只认解析/形状错 + 当日买卖两档十读数 + 桶完整性跨语言锁 + INFO 不进判数（2026-09-24 扩桶批）..."
# 为什么要给一条**部署面**探针单独设锁：09-23「白天只龙头出信号」是 owner 用肉眼在前端看出来的，
# 现网 24 条探针一条都没红——缺陷在观测面上是隐形的，修没修好同样没人知道。这条探针就是那双眼睛；
# 而"眼睛"本身没有守卫，就会在下一次改动里被悄悄换成一只只看不到东西的眼睛（判据被挪去判合法态、
# 明细被合并回全桶、INFO 混进 PASS/FAIL 计数），且**不会有任何测试变红**。故按取值链逐条钉住。
# 同日扩桶批（owner 令「other:9 要么扩桶、要么把原始类型原样列出来，必须和现有桶名守卫同批改」）
# 把本段锤实了一件事：**桶名守卫守不住桶的正确性**。旧 SgKey 用 `'dragon|龙头'` 子串规则，
# 于是「龙头断板」（做空战法 leader_decay 的展示名）被静默并进 dragon——09-24 现网 dragon:14 虚高、
# 买入侧覆盖窄这个真问题被卖出信号的数量盖住。新增的 ⑦-b/⑦-c/⑩/⑪ 四条锁因此守的是
# "并桶这条通道不存在"＋"Go 里有、桶里没有必红"，而不是"当前这一对的顺序恰好对"。
VD=scripts/verify_deploy_guangzhou.sh
# ① 探针本体与判据同源：红 == 「读不出可信内容」，不是「今天没信号」。
grep -qF 'Probe "engine:today pinned signals spread across strategies" ($sgBad.Count -eq 0)' "$VD" \
	|| { echo "--- FAIL: §SIGNAL-DIST 探针丢失或判据被换掉（红一旦不等价于「解析/形状失败」，这条眼睛就废了）"; exit 1; }
# ② 判红来源必须恰好两处（parse-error / shape-error）。多一处＝有人把合法态判成红，这条会每天清晨自找一红。
sgb=$(grep -c 'sgBad += (' "$VD" || true)
[ "$sgb" = "2" ] || { echo "--- FAIL: §SIGNAL-DIST 判红来源不是 2 处（计数=${sgb}，只允许 parse-error 与 shape-error）"; exit 1; }
if grep -nE 'sgBad \+= \(' "$VD" | grep -vE ':[0-9]+:[[:space:]]*#' | grep -vE 'parse-error|shape-error' | grep -q .; then
	echo '--- FAIL: §SIGNAL-DIST 出现第三种判红来源（no-file／跨日残留桶／当日零信号都是合法态，一律不得进 sgBad）'; exit 1; fi
# ③ 三种合法态必须各自留下明细（删掉读数＝把"看不到"重新变成隐形）。
grep -qF 'day=none n=0 empty' "$VD" \
	|| { echo "--- FAIL: 空 signals 数组的合法态明细丢失（绿但读不出内容＝跟没修一样）"; exit 1; }
grep -qF 'shape-error(no-signals-array' "$VD" \
	|| { echo "--- FAIL: 结构缺 signals 数组的判红分支丢失"; exit 1; }
# ④ 当日聚合读数：owner 问的"今天有几类战法出了信号、在买还是在卖、没进桶的是谁"只能由这几个回答，
#    缺一个就退回肉眼盯。扩桶批（09-24 下午）把 kinds 拆成买/卖两档并补 unmatched/side_unknown。
for k in today_signals today_strategies leader_only today_files buy_types sell_types kinds_buy kinds_sell unmatched side_unknown; do
	grep -q "$k=" "$VD" || { echo "--- FAIL: §SIGNAL-DIST 缺聚合读数 ${k}（明细必须直接回答「今天」，不是「所有桶加起来」）"; exit 1; }
done
# ⑤ 只龙头判据三条件缺一不可：当日有信号 + **买入侧**种类==1 + 那一类确实是 dragon
#    （少第一条会在零信号日谎报 leader_only=true，少第三条会把"只出 fac_1"当成只出龙头；
#     09-24 扩桶后判据域收窄到买入档——卖出/做空桶出得再多也不该把 leader_only 打成 false，
#     否则这条眼睛又看不见它本来要看的东西了。）
grep -qE '\$sgTodayN -gt 0 -and \$sgBuyTypes\.Count -eq 1 -and \$sgBuyTypes\[0\] -eq .dragon.' "$VD" \
	|| { echo '--- FAIL: leader_only 判据被改写（须同时满足 当日 n>0 / 买入侧种类==1 / 该类==dragon）'; exit 1; }
# ⑥ 按文件分行而不是合并计数：DataDir 下 Recurse 会收到根目录 + 每账号各一份（09-24 首跑实测 4 份），
#    合并＝把昨日残留和别人的账号混进同一个数字。取数路径出现第二处即口径分叉。
sgf=$(grep -c "Filter 'signals_today.json'" "$VD" || true)
[ "$sgf" = "1" ] || { echo "--- FAIL: 固化信号取数路径不是唯一一处（计数=${sgf}，出现第二处就有第二套口径）"; exit 1; }
# ⑦ 战法桶名必须 ASCII：本仓实录过 PS→SSH→bash 回传时中文 detail 被 GBK 字节打乱 ⇒ grep 判据恒不命中
#    （＝把假绿写进探针）。中文只允许出现在匹配侧（switch 的 case / -match 的右侧），
#    不允许出现在 return 的取值侧——扩桶批新增的中文别名表因此也全部映射到 ASCII 桶名。
#    `|| true` 不能省：grep -c 在**零命中**（正是本锁要 passes 的那个值）时退出码为 1，本脚本开着
#    `set -euo pipefail`，命令替换的非 0 会让整条赋值语句失败 ⇒ 整轮 verify 无 FAIL 无 ok 直接中止
#    （09-24 实跑锤出：§88 只打印了标题就 VERIFY_EXIT=1，后面所有段都没跑）。判红仍交给下面那句等值判断。
badKey=$(LC_ALL=C awk '/^function SgKey/,/^}$/' "$VD" | grep -o 'return "[^"]*"' | LC_ALL=C grep -c '[^ -~]' || true)
[ "$badKey" = "0" ] || { echo "--- FAIL: SgKey 的 return 值含非 ASCII 桶名（计数=${badKey}，中文桶名会让判据恒不命中）"; exit 1; }
ordR=$(grep -n 'return "dragon_return"' "$VD" | head -1 | cut -d: -f1 || true)
[ -n "$ordR" ] || { echo '--- FAIL: dragon_return 桶从 SgKey 里消失了（Go 侧还在产这个类型，探针会把它算进 other）'; exit 1; }
# ⑦-b 子串匹配负锁（09-24 扩桶批的根因锁）：SgKey 里只准有 `^fac_` / `^pat_` 这种**行首锚定**的前缀判断，
#     出现任何非锚定 -match 就是回到旧写法——`'dragon|龙头'` 当年把 leader_decay（展示名「龙头断板」）
#     静默吞进 dragon 桶，现网读数 dragon:14 因此虚高、owner 要的答案被一个子串规则吃掉。
#     顺序型守卫（"A 必须写在 B 之前"）在这里救不了：补一条 if 只是把下一次撞名推迟，
#     所以钉的是"这条通道整体不存在"，而不是"当前这一对的顺序恰好对"。
sk=$(awk '/^function SgKey/,/^}$/' "$VD" | grep -- '-match' | grep -vE -- "-match '\\^(fac|pat)_" || true)
if [ -n "$sk" ]; then
	echo '--- FAIL: SgKey 里出现非锚定的 -match（子串匹配＝下一次战法改名时静默并桶；改成精确等值查表，匹不上就记 other 并回显原始值）'
	printf '%s\n' "$sk"
	exit 1
fi
# ⑦-c 桶完整性锁（跨语言）：internal/strategy/types.go 的 SignalType 常量表是战法类型的**唯一真源**，
#     每一个 ASCII 取值都必须在 SgKey 里有对应的 return 桶。新增战法时这里必红，
#     于是"忘了同步部署探针"从"现网静默落进 other、三个月后才发现"变成"门禁当场点名"。
got=$(grep -oE 'SignalType = "[a-z_]+"' internal/strategy/types.go | sed -E 's/.*"([a-z_]+)".*/\1/' | sort -u || true)
ggn=$(printf '%s\n' "$got" | grep -c '[a-z]' || true)
[ "${ggn:-0}" -ge 12 ] \
	|| { echo "--- FAIL: 从 internal/strategy/types.go 只读到 ${ggn} 个 SignalType 常量（少于 12 说明常量表形态变了，本锁的取法要跟着改，不能当'全都覆盖了'）"; exit 1; }
sgmiss=""
for ty in $got; do
	grep -qF "return \"$ty\"" "$VD" || sgmiss="$sgmiss $ty"
done
[ -z "$sgmiss" ] \
	|| { echo "--- FAIL: 这些战法类型在 Go 侧存在、在部署探针 SgKey 里没有桶（会被静默算进 other，现网读数答不出是谁）：$sgmiss"; exit 1; }
# ⑦-d 中文别名映射锁（比 ⑦-c 更狠的一层，专门钉 09-24 那次实际的错法）：
#     「桶存在」挡不住「桶存在但映射错」——leader_decay 的桶当年就在，是它的展示名「龙头断板」被
#     `'dragon|龙头'` 抢走了。故这里从 Go 侧机械取出 (ASCII 类型 → 规范中文展示名) 的配对表
#     （internal/strategy/types.go 常量值 × internal/combat_agent/types.go StrategyDisplayName），
#     要求 SgKey 里对每一对都存在「'中文名' → return "ASCII"」这一条精确映射；
#     有人把某个中文名挂到别的桶上（或新增战法只挂 ASCII 不挂别名）即红。
CNMAP=$(perl -0777 -CSD -ne '
	if ($ARGV =~ m{internal/strategy/types\.go$}) { while (/Signal([A-Za-z0-9_]+)\s+SignalType\s*=\s*"([a-z_]+)"/g) { $v{$1} = $2 } }
	else { while (/case\s+strategy\.Signal([A-Za-z0-9_]+):\s*\n\s*return\s*"([^"]+)"/g) { print "$v{$1}\t$2\n" if $v{$1} } }
' internal/strategy/types.go internal/combat_agent/types.go)
cnn=$(printf '%s\n' "$CNMAP" | grep -c $'\t' || true)
[ "${cnn:-0}" -ge 9 ] \
	|| { echo "--- FAIL: 只从 Go 侧读到 ${cnn} 对 (战法类型→中文展示名)（少于 9 说明取值写法变了，本锁会漏覆盖，必须跟着改而不是当通过）"; exit 1; }
cnmiss=""
while IFS=$'\t' read -r ty cn; do
	[ -n "$ty" ] && [ -n "$cn" ] || continue
	grep -qE "'${cn}'[[:space:]]*\{[[:space:]]*return \"${ty}\"[[:space:]]*\}" "$VD" || cnmiss="$cnmiss ${cn}->${ty}"
done <<< "$CNMAP"
[ -z "$cnmiss" ] \
	|| { echo "--- FAIL: 这些中文展示名在 SgKey 里没有映射到它在 Go 侧对应的桶（挂错桶＝静默并类，09-24 的 dragon:14 虚高就是这个形状）：$cnmiss"; exit 1; }
# ⑩ 买/卖档口径锁：分档轴必须是 direction 的**等值**判断（固化存储 Upsert 只收 做多/做空，见
#     internal/engine/signal_store.go），且必须有显式兜底档 side_unknown——上游出现第三种方向词时
#     要在明细里数得出来，不能静默并进 buy 或 sell。
grep -qE '\$sgTodaySideUnknown = \$sgTodaySideUnknown \+ 1' "$VD" \
	|| { echo '--- FAIL: 买卖档丢了 side_unknown 兜底计数（第三种方向词会被静默漏计，两档读数看着正常其实失真）'; exit 1; }
grep -qF "switch -Exact (([string]\$o.direction).Trim())" "$VD" \
	|| { echo '--- FAIL: SgSide 不再按 direction 等值分档（换成 action 就完蛋：它在不同战法里有 buy/sell/卖出/减仓/关注 五套写法）'; exit 1; }
if awk '/^function SgSide/,/^}$/' "$VD" | grep -qE '\.action'; then
	echo '--- FAIL: SgSide 里出现 .action（分档轴被换成动作词＝把五套历史写法当成两套，买卖档必然错分）'; exit 1; fi
# ⑪ 未归类原始值必须消毒后才进明细（中文原始值会让判据恒不命中，同 ⑦ 的理由），
#     且 unmatched 只在当日文件上统计（跨日残留会把"今天谁没进桶"淹掉）。
grep -qF -- "-replace '[^ -~/]', ''" "$VD" \
	|| { echo '--- FAIL: SgRaw 不再剔除非 ASCII 字符（原始战法名直接进 detail＝GBK 打乱判据，探针自己变假绿）'; exit 1; }
grep -qF "if (\$k -eq 'other' -or \$k -eq 'unknown') { SgInc \$sgRawUnmatched (SgRaw \$sg) }" "$VD" \
	|| { echo '--- FAIL: 未归类原始值的回显口径被改写（要么不再列原始值，要么把跨日残留也算进来）'; exit 1; }
# ⑫ 计数与格式化各只有一份实现：本探针维护 5 张计数表（全桶/当日桶/买入桶/卖出桶/未归类原始值），
#     内联写五遍必错一处，故抽成 SgInc/SgTop；出现第二处实现就是第二套口径（同 ⑥ 的取数路径唯一理由）。
for fn in SgInc SgTop; do
	fnc=$(grep -c "^function ${fn}(" "$VD" || true)
	[ "$fnc" = "1" ] || { echo "--- FAIL: function ${fn} 的定义不是唯一一处（计数=${fnc}，两套计数/格式化实现迟早分叉）"; exit 1; }
done
# ⑧ INFO 是观测通道不是判据通道：bash 侧只 echo、不进 PASS/FAIL 计数（判数口径见 verify_deploy 头注）。
#    一旦有人把 INFO 接成 PASS，绿的数量就会凭空增长，而红绿语义没变——这是最隐蔽的一种假绿。
#    行数=5→6→7：逐文件空态/逐文件明细/当日聚合（§SIGNAL-DIST 原三条）+ 日历读数（§CAL-READOUT，
#    2026-09-26 owner 令新增第 27 探针的恒回显腿；§101 另有 INFO|cal_readout 在位锁）
#    + 跌停闸现值读数（§A5-CURRENT，2026-09-26 深夜批第 28 探针；§101 ②b 有专属锁）
#    + 成交额量纲抽检读数（§0929SCALE-⑩，2026-09-29 批第 30 探针；§106 有 INFO|amount_scale_readout 在位锁）
#    + 快照目录 ACL 读数（§0929OPS-⑪-3，2026-09-29 深夜批第 31 探针；§107 有 INFO|snapshot_acl_readout 在位锁）。
#    本仓纪律：**新增 INFO 腿必须与这条计数锁同日同步**（09-26 的 4→5、09-29 的 5→6、同日晚的 6→7 都是这么走的），
#    否则计数锁会在别人加观测腿时判红——那是好事，前提是红项能一眼看出该同步哪一条，故枚举必须写全。
grep -qF 'INFO\|*) echo' "$VD" \
	|| { echo '--- FAIL: bash 侧 INFO 分支丢失（观测读数会被当成未知行，或被误接进 PASS/FAIL 计数）'; exit 1; }
infop=$(grep -c 'Write-Output ("INFO|' "$VD" || true)
[ "$infop" = "7" ] || { echo "--- FAIL: INFO 观测行数不是 7（逐文件空态/逐文件明细/当日聚合/日历读数/跌停闸现值/成交额量纲抽检/快照 ACL 读数，计数=${infop}）"; exit 1; }
# ⑨ 负锁：本探针不得用 `| Out-String` 读 JSON 字段（PS 控制台按 120 列折行会劈开值，§N-5 的教训本体；
#    全局负锁在 §67，这里钉的是"这条腿自己的取值方式"，防止有人日后为省事把它换回去）。
grep -qF '([string]$sgJson.trading_day)' "$VD" \
	|| { echo '--- FAIL: trading_day 不再用 [string] 直转（退回 | Out-String 即重新引入折行失明）'; exit 1; }
echo "ok - §SIGNAL-DIST 守卫通过（探针判据锁 1 + 判红来源等值锁 1 + 来源白名单负锁 1 + 合法态明细锁 2 + 聚合读数锁 10 + leader_only 三条件锁 1 + 取数路径唯一锁 1 + 桶名 ASCII 负锁 1 + 桶存在锁 1 + 子串匹配负锁 1 + 跨语言桶完整性锁 2 + 买卖档口径锁 3 + 原始值消毒锁 2 + 计数实现唯一锁 2 + INFO 通道锁 2 + Out-String 负锁 1）"

echo "==> 89 §GATE-COUNT-LOCK 门禁自身的地雷：计数锁零命中会把整轮 verify 静默跑死（2026-09-24，§88 自曝同类）..."
# 为什么给门禁脚本自己设锁：本段是 09-24 用一轮真红换来的——`var=$(... | grep -c 'pat')` 在**零命中**
# 时退出码 1（计数为 0 恰恰是负锁要的绿色），而本脚本开着 `set -euo pipefail`，命令替换非 0 ⇒ 整条赋值
# 语句失败 ⇒ 脚本当场中止，日志里**既没有 `--- FAIL` 也没有 `ok -`**，只有末尾一个 `VERIFY_EXIT=1`。
# 这比"少一条锁"更糟：① 它把该段之后的所有段整体吞掉（第 6 轮就是这么丢掉最后一段的），
# ② 有人日后**删掉被守护的代码**时，本该报红的锁会以"跑死"的形态出现，读日志的人分不清是环境问题还是缺陷。
# 规则因此定成一条机械口径：**门禁里凡 `var=$(... grep ...)` 一律在替换末尾写 `|| true`**，
# 判红只准交给紧随其后的等值/非空判断——加了 `|| true` 不改变任何一次红绿结论，只把"中止"变回"报红"。
# 本段用 awk 单趟判定（awk 即使零命中也退出码 0 ⇒ 锁自己不会重犯它要防的雷），并且**先把反斜杠续行
# 拼成一条逻辑行**再判：循环体里的赋值带缩进、`n=$(find … \` 的 `|| true` 落在下一行，逐行扫会漏判。
GS=scripts/verify_changes.sh
# 踩雷的形态有三种：
#   A `v=$(... grep -c ...)`（09-24 §88 实跑锤出的那个）、B `v=$(... grep -l ... | wc -l ...)`、
#   C `v=$(... grep -n ... | head -1 | cut -d: -f1)`（被守护代码一旦被删，grep 零命中＝该报红，实际跑死）。
# 尺子取最宽的一条机械口径：**凡赋值替换里出现 grep 都必须写 `|| true`**——因为 grep 的退出码在这三种
# 形态里都不参与判红（判红交给后面的等值/非空判断），留着它只会把"报红"变成"中止"。
# 本段自己那两行必然写着 `grep` 字面量，统一以行尾 `GATE-SCAN-SELF` 标记豁免（注释不参与匹配）。
CL=$(awk '{ b=$0; while (b ~ /\\$/ && (getline x) > 0) b = b " " x; if (b ~ /^[ \t]*[A-Za-z_][A-Za-z0-9_]*=\$\(/ && b ~ /grep/ && b !~ /\|\| true/ && b !~ /GATE-SCAN-SELF/) printf "%d: %s\n", NR, b }' "$GS") # GATE-SCAN-SELF
if [ -n "$CL" ]; then
	echo '--- FAIL: §GATE-COUNT-LOCK 发现未加固的计数赋值（零命中即静默中止整轮 verify；请在替换末尾补 `|| true`，判红交给后面的等值判断）：'
	printf '%s\n' "$CL"
	exit 1
fi
# 加固面读数：本批（09-24 收尾）一次性把 50 处历史计数赋值补齐 `|| true`（扫描面 52 处，余 2 处是本段
# 自身），只准这个数**变多**（新写的锁按同一口径加固），变少＝有人把加固删了；总数一起回显，
# 方便看出"还剩几处裸奔"。
CN=$(awk '{ b=$0; while (b ~ /\\$/ && (getline x) > 0) b = b " " x; if (b ~ /^[ \t]*[A-Za-z_][A-Za-z0-9_]*=\$\(/ && b ~ /grep/ && b !~ /GATE-SCAN-SELF/) { t++; if (b ~ /\|\| true/) h++ } } END { printf "%d %d\n", h+0, t+0 }' "$GS") # GATE-SCAN-SELF
HARD=${CN% *}
TOTAL=${CN#* }
[ "${HARD:-0}" -ge 50 ] \
	|| { echo "--- FAIL: 已加固的 grep 计数赋值不足 50 处（读到 ${HARD}／共 ${TOTAL}，说明历史加固被删）"; exit 1; }
# 前提锁：本段的整套修法建立在「脚本开着失败即中止」上——一旦有人把 set 放宽，
# 加固不再必要，但整轮门禁的失败可见性也没了（静默跑绿比静默跑死更坏）。所以两头都钉住。
grep -q '^set -euo pipefail$' "$GS" \
	|| { echo '--- FAIL: §GATE-COUNT-LOCK 的前提没了（门禁开头必须仍开着 set -euo pipefail）'; exit 1; }
if grep -nE '^set \+e|^set -u$|^set \+o pipefail' "$GS" > /dev/null; then
	echo '--- FAIL: 门禁脚本里出现放宽 set 的写法（不得用「关掉失败即中止」来绕开计数锁的加固）'; exit 1; fi
echo "ok - §GATE-COUNT-LOCK 守卫通过（未加固计数赋值负锁 1 + 加固面下限锁 1（当前 ${HARD}/${TOTAL}） + set 前提锁 1 + set 放宽负锁 1）"

echo "==> 90 §BRIDGE-PATH 桥目录跨平台落点：Windows 默认逐字保留 + POSIX 拒写（不长反斜杠垃圾文件）（2026-09-24，任务 #43 根因）..."
# 这段锁的是"每跑一次 Python 测试，仓库根就长出一个名字里带反斜杠的文件"这个反复回潮的形态
# （09-23、09-24 各手工清理过一次又回来）。缺陷本体不值一段锁——值钱的是它牵住的**资金通道**：
# BRIDGE_DIR 里的 bridge_report.jsonl（桥→网关成交/心跳）、bridge_cmd.json（网关→桥指令）、
# bridge_seen.jsonl（重启判重账本）是真实传输，不是日志。于是两头都必须钉住：
#   ① 生产侧：目录默认值要和改前**逐字相同**，且换目录只能靠显式设 QMT_BRIDGE_DIR。
#      一旦有人为了"本机测试方便"把默认值改成相对路径/POSIX 路径，seen 文件跟着搬家
#      ＝桥重启后可以把同一笔委托再执行一遍（drill-3 重放形态的资金事故）。
#   ② 本机侧：POSIX 上 `open("C:\\...\\bridge_boot.log","ab")` **不会失败**，它在当前工作目录
#      创建一个带反斜杠的文件——trace 丢在没人看的地方＝诊断失明，工作树被测试产物污染。
#      正确形态是写盘前判路径在本机构不成"目录/文件"两段就**拒写**：trace 属可丢的诊断，
#      report/seen 走既有 False 分支让调用方拒单（fail-closed，宁停一单不重放一笔）。
# 读法顺序有意为之：先静态写法 → 再看仓库根**此刻**的现场（最直接的证据，不必等 pytest） →
# 最后真跑一遍路径用例（防"跑完这一轮自己又长出来"）。
BS=qmt_gateway/qmt_bridge_strategy.py
# ① 取值链两半：env 覆盖 + 未覆盖时的 Windows 绝对目录，用 -qF 做整串等值（改任一半即红）。
grep -qF 'BRIDGE_DIR = os.environ.get("QMT_BRIDGE_DIR") or r' "$BS" \
	|| { echo '--- FAIL: §BRIDGE-PATH 桥目录不再读 QMT_BRIDGE_DIR（测试会话只能靠它指到临时目录）'; exit 1; }
grep -qF 'or r"C:\qmt\quant-trading-v2\qmt_gateway"' "$BS" \
	|| { echo '--- FAIL: §BRIDGE-PATH 桥目录默认值被改（广州那台机器没有这个环境变量，靠默认值接线上桥；改默认值＝seen 判重账本搬家）'; exit 1; }
# ② 五个桥文件常量一律经 _p()（os.path.join）：数量必须正好 5。少一个就是那一处退回字面反斜杠相加，
#    而"哪一处"正是要命的地方——report/seen 少一处就是资金通道被写到 CWD。
PC=$(grep -c '= _p(' "$BS" || true)
[ "$PC" = "5" ] \
	|| { echo "--- FAIL: 走 _p() 拼路径的桥文件常量不是 5 个（读到 ${PC}；TRACE/REPORT/CFG/CMD/SEEN 必须同源一个目录）"; exit 1; }
# ③ 负锁：字面反斜杠拼接不得复活（同 §SIGID-TRUNC 的"单一截断点"写法锁口径；注释里的说明文本不算）。
if grep -n 'BRIDGE_DIR +' "$BS" | grep -vE '^[0-9]+:[[:space:]]*#' | grep -q .; then
	echo '--- FAIL: 桥路径又出现 BRIDGE_DIR + 字符串相加（POSIX 上反斜杠不是分隔符，会在 CWD 造出带反斜杠的文件）'; exit 1
fi
# ④ _dir_ready 的三态实现：分隔符判定必须在。只查 isdir 会漏掉中间那一态——
#    POSIX 上 os.path.dirname("C:\\a\\b.log") == ""、basename 返回整串，CWD "存在" 却正是 bug 本体
#    （首版实现就只查了 dirname/isdir，实测仓库根照长文件）。
grep -qF 'if "\\" in base or "/" in base:' "$BS" \
	|| { echo '--- FAIL: _dir_ready 丢了分隔符判定（只查 isdir 时，Windows 路径落在 POSIX 上被判成"可写"）'; exit 1; }
# ⑤ 三个写点各按自己的后果分级：trace 早退丢弃，report/seen 抛错→既有的 False 分支（fail-closed）。
TC=$(grep -c 'if not _dir_ready(TRACE_PATH):' "$BS" || true)
[ "$TC" = "1" ] || { echo "--- FAIL: _trace 的拒写早退不唯一（读到 ${TC}，路径不可用时诊断会写进 CWD）"; exit 1; }
IC=$(grep -c 'raise IOError("bridge dir not a directory on this host: "' "$BS" || true)
[ "$IC" = "2" ] \
	|| { echo "--- FAIL: report/seen 的拒写不再是抛错（读到 ${IC}，期望 2）——资金通道写不下去必须返回 False 让调用方拒单，绝不能静默成功"; exit 1; }
# ⑥ 现场锁：仓库根此刻不得存在"名字里带反斜杠或盘符"的文件（本批要收口的现象本身）。
#    静态锁只防回潮，这条防"某个新测试模块绕过 conftest 直接 import 桥策略"。
JF=$(find . -maxdepth 1 -type f \( -name '*\\*' -o -name '*:*' \) | head -5 || true)
[ -z "$JF" ] \
	|| { echo '--- FAIL: 仓库根有 Windows 形态的怪文件（桥 trace/report 被写进了 CWD，先查是哪个测试模块绕过 conftest 导入了 qmt_bridge_strategy）：'; printf '%s\n' "$JF"; exit 1; }
# ⑦ 测试会话前置：conftest 必须在任何 test 模块 import 桥策略之前把目录指走
#    （模块顶层就重算五个常量，晚设 env 来不及），且会话结束回收临时目录——测试产物不落仓库工作树。
[ -f qmt_gateway/tests/conftest.py ] \
	|| { echo '--- FAIL: qmt_gateway/tests/conftest.py 缺失（pytest 没有会话前置，导入桥策略即污染工作树）'; exit 1; }
grep -qF 'os.environ["QMT_BRIDGE_DIR"] = _TMP' qmt_gateway/tests/conftest.py \
	|| { echo '--- FAIL: conftest 不再设置 QMT_BRIDGE_DIR'; exit 1; }
grep -qF 'def pytest_unconfigure' qmt_gateway/tests/conftest.py \
	|| { echo '--- FAIL: conftest 丢了会话结束回收（临时桥目录留在磁盘上）'; exit 1; }
# ⑧ 运行时实证（比上面所有静态锁都硬）：路径用例自己在临时 CWD 里试过——被拒的三次写一个文件
#    都不长、被拒的 seq 不进判重集合、env 未设时默认值逐字等于改前、仓库根导入后依旧干净。
BP=$(py_tests qmt_gateway/tests/test_bridge_paths.py 2>&1 | tail -6 || true)
printf '%s\n' "$BP"
if printf '%s' "$BP" | grep -qE 'FAILED|ERROR|failed|error'; then
	echo '--- FAIL: §BRIDGE-PATH 路径用例跑红（见上面输出）'; exit 1
fi
printf '%s' "$BP" | grep -qE 'passed|^OK|Ran [0-9]+ tests' \
	|| { echo '--- FAIL: §BRIDGE-PATH 路径用例没跑到（输出里没有 passed/OK——文件被移走或改名？）'; exit 1; }
echo "ok - §BRIDGE-PATH 守卫通过（取值链两半锁 2 + 拼接唯一锁 1 + 字面反斜杠负锁 1 + _dir_ready 分隔符锁 1 + 三写点分级锁 2 + 仓库根现场锁 1 + conftest 前置锁 3 + 运行时用例实证 2）"

echo "==> 91 §MINUTE-K 分钟 K 落库升级：建表口径 + 装载诚实出门 + 动量换真 5 分钟 MACD + 夜间日增环门控（2026-09-24 owner 裁决「分钟 K 落库升级：建表+回填+换真分钟」）..."
# 这段锁钉的是"分钟 K 从口头升级变成落库升级"的四个易碎点。背景：动量战法此前按日线 MACD 近似
# （§87 把它列为残余近似），而实盘引擎用的是 5 分钟 48 根——两边口径不同，回放结论和实盘判断
# 就不是同一把尺子。09-24 的修法是把 5 分钟线落库（minute_klines）并让回放优先用它。
# 四个易碎点，每一个都对应一种"看起来升级了、其实没有"的形态：
#   ① 表口径：主键 (ts_code,scale,ts) 撑幂等；**故意没有复权列**——分钟线全是不复权，
#      与实盘 md.MinuteMACD 同源。一旦有人加 hfq 列或把分钟和日线 hfq 混用，同一笔判断
#      在两条链上会得出不同 MACD（复权口径系列缺陷 §ADJ-BASIS-* 就是这么来的）。
#   ② 半日不许冒充升级：窗口闸门只有一处（>=48 根**有效**根才给真分钟 MACD），
#      写成两处就会有一处漏；出现第二处即红。
#   ③ 装载器诚实出门：0 行判失败、失败率超上限判失败、平均根数打印——上游只有"最近 N 根"
#      窗口（新浪 5 分钟实测封顶 5025 根），所以"回填三年"在数据源层面不成立，不许编。
#   ④ 夜间日增环门控：分钟表从没回填过时该环**不入队**（否则每晚 0 行判失败淹掉真告警），
#      且调度侧 scale 必须与回放侧同源（两边不一致＝表里有数据却永远查不到，静默退回日线近似）。
MK_STORE=internal/store/minute_klines.go
MK_LOAD=cmd/dataload/minute_sync.go
MK_REP=internal/btreplay/replay.go
MK_WRK=internal/scheduler/worker.go
# ① 建表三件套 + 无复权列（负锁）
grep -q 'CREATE TABLE IF NOT EXISTS minute_klines' internal/store/store.go \
	|| { echo '--- FAIL: §MINUTE-K minute_klines 建表语句没了（回放侧永远查不到分钟线）'; exit 1; }
grep -q 'CREATE INDEX IF NOT EXISTS idx_minute_scale_ts ON minute_klines(scale, ts)' internal/store/store.go \
	|| { echo '--- FAIL: §MINUTE-K (scale,ts) 索引没了：按日切片与门控探针退化成整表扫描（250 万行级）'; exit 1; }
grep -q 'PRIMARY KEY (ts_code, scale, ts)' internal/store/store.go \
	|| { echo '--- FAIL: §MINUTE-K 落库主键不再声明 (ts_code,scale,ts)（幂等靠它，重复写会把一日 48 根变成 96 根）'; exit 1; }
# 复权列负锁只看**建表列定义那几行**（上面的中文注释里就有"hfq/复权"字样，整段匹配必假红）
if awk '/CREATE TABLE IF NOT EXISTS minute_klines \(/{f=1} f{print} f && /PRIMARY KEY/{exit}' internal/store/store.go | grep -qiE 'adj|hfq|qfq'; then
	echo '--- FAIL: §MINUTE-K minute_klines 建表里出现复权列（分钟线口径必须与实盘 MinuteMACD 同为不复权，混用＝两条链两套 MACD）'; exit 1
fi
# ② 按日切片只吃 ts 前缀；半日冒充升级的唯一闸门必须只有一处
QB=$(grep -c 'FROM minute_klines WHERE ts_code=? AND scale=? AND ts>=? AND ts<?' "$MK_STORE" || true)
[ "$QB" = "1" ] \
	|| { echo "--- FAIL: §MINUTE-K 按日切片的 SQL 形状变了（匹配到 ${QB} 处；必须是 ts_code+scale+ts 前缀区间 ORDER BY ts，走主键索引——换成 substr() 即全表扫，回放按日取数会退化成几千次扫描）"; exit 1; }
grep -q 'day+" 24:00:00"' "$MK_STORE" \
	|| { echo '--- FAIL: §MINUTE-K 日切片右边界不再是 day+" 24:00:00"（改回 23:59:59 会漏掉边界根；"24:" 字典序天然大于任何真实时刻，所以前缀区间等价于"这一天的全部行"）'; exit 1; }
WG=$(grep -c 'len(mkl) >= s.window' "$MK_REP" || true)
[ "$WG" = "1" ] \
	|| { echo "--- FAIL: 分钟 MACD「根数不足即报不可用」的闸门不再是唯一一处（读到 ${WG}：写两处必有一处漏，半日数据就会冒充升级）"; exit 1; }
# ③ 装载器三条诚实出门 + 上游封顶口径写死在缺省值里
grep -q 'st.Rows == 0' "$MK_LOAD" \
	|| { echo '--- FAIL: §MINUTE-K minute-sync 丢了「0 行落库判失败」（跑完没数据绝不能算成功）'; exit 1; }
grep -q '超过上限' "$MK_LOAD" \
	|| { echo '--- FAIL: §MINUTE-K minute-sync 丢了失败率闸（整片封 IP/接口改版会被当成"偶发失败"混过去）'; exit 1; }
grep -q 'fs.IntVar(&o.Count, "count", 5025' "$MK_LOAD" \
	|| { echo '--- FAIL: §MINUTE-K --count 缺省不再是 5025（上游只有最近 N 根窗口，缺省值就是"能回填多久"的事实）'; exit 1; }
grep -q 'fs.IntVar(&o.Limit, "limit", 500' "$MK_LOAD" \
	|| { echo '--- FAIL: §MINUTE-K --limit 缺省不再是 500（清单不设闸＝一次手滑把全市场 2GB 灌进例行回填）'; exit 1; }
# ④ 回放侧优先分钟 + 注入点三处齐（§0925EVE-W3-J/B6 起：backtestStock 的全部入口都必须先装
#    自己的分钟口径——回放主循环、网格触发预算 2a、冠军复核 simulateCombo。缺一处该入口就按
#    另一条取数路径选参数/复核；B6 修前复核链沿用 2a 循环 map 末票序列，属跨股串台级缺陷。
#    判据按文件分计数等值（AI1=1/AI2=2），比总和更抗"乱凑第三处"）
grep -q 'minuteMACDScale      = 5\|minuteMACDScale = 5' "$MK_REP" \
	|| { echo '--- FAIL: §MINUTE-K 回放侧分钟周期常量丢了（窗口尺寸 48 根是 5 分钟口径，周期一改即失配）'; exit 1; }
AI1=$(grep -c 'o.applyMinuteScope(' "$MK_REP" || true)
AI2=$(grep -c 'o.applyMinuteScope(' internal/btreplay/sweep.go || true)
[ "$AI1" = "1" ] \
	|| { echo "--- FAIL: §MINUTE-K 回放主循环注入点数不是 1（读到 ${AI1}；backtestStock 前的逐股装口径只能有一处）"; exit 1; }
[ "$AI2" = "2" ] \
	|| { echo "--- FAIL: §MINUTE-K sweep.go 注入点数不是 2（读到 ${AI2}；网格 2a 预算与 B6 冠军复核 simulateCombo 必须同源，少一处就是拿别的票的分钟序列选参数/出复核数字）"; exit 1; }
grep -q 'tsOfCode map\[string\]string' internal/btreplay/sweep.go \
	|| { echo '--- FAIL: §MINUTE-K simulateCombo/verifyChampion 不再透传 tsOfCode 反查表（B6 逐股重注入的原料，删了第三注入点必退化）'; exit 1; }
# ⑤ 调度侧接线：任务类型 + 步骤映射 + 紧跟 dataload + 门控 + 参数组装
grep -q 'TaskMinuteSync' internal/store/research_tasks.go \
	|| { echo '--- FAIL: §MINUTE-K 夜间分钟任务类型常量没了'; exit 1; }
grep -q 'case "minute_sync":' "$MK_WRK" \
	|| { echo '--- FAIL: §MINUTE-K 夜间步骤 minute_sync 不再映射（默认链里写了也没人接）'; exit 1; }
grep -q '"dataload", "minute_sync", "sector_rebuild"' internal/config/config.go \
	|| { echo '--- FAIL: §MINUTE-K 默认夜间链不再让 minute_sync 紧跟 dataload（先有日线再刷分钟，回放读的是当天刷新的表）'; exit 1; }
grep -q 'db.MinuteHasBars(minuteSyncScale)' "$MK_WRK" \
	|| { echo '--- FAIL: §MINUTE-K 夜间日增环的空表门控没了（分钟表从未回填时会每晚 0 行判失败，天天失败＝真告警被淹）'; exit 1; }
grep -q '"minute-sync"' "$MK_WRK" \
	|| { echo '--- FAIL: §MINUTE-K taskCommand 不再走 dataload 的 minute-sync 子命令'; exit 1; }
# ⑥ 跨文件 scale 同源：调度下发 5 分钟、回放查 5 分钟，两边各自写死就要有人改一边
SS=$(grep -c 'minuteSyncScale      = 5' "$MK_WRK" || true)
[ "$SS" = "1" ] \
	|| { echo "--- FAIL: §MINUTE-K 调度侧分钟周期常量不再是 5（读到 ${SS}；与回放侧 minuteMACDScale 必须同值，否则表里有数据却永远查不到，动量静默退回日线近似）"; exit 1; }
# ⑦ §87 锚点不得被分钟升级顶掉（动量判据仍是实盘语义重写版，且没有退回"默认不回测"）
grep -q 'criteria rewritten to live semantics' "$MK_REP" \
	|| { echo '--- FAIL: §MINUTE-K 动量出门文本被改写（§87 的等值锚点：criteria rewritten to live semantics 必须在）'; exit 1; }
if grep -q 'not replayed by default' "$MK_REP"; then
	echo '--- FAIL: §MINUTE-K 动量又出现「not replayed by default」（§87 已锤：动量必须进回放，不许缺席）'; exit 1
fi
# ⑧ 现场锁：分钟装载/回放的测试数据一律落临时库，仓库工作树不得留下 trading.db 或 codes 清单
STRAY=$(find . -maxdepth 2 -type f \( -name 'minute_codes*.txt' -o -name 'trading.test.db' \) 2>/dev/null | head -5 || true)
[ -z "$STRAY" ] \
	|| { echo '--- FAIL: §MINUTE-K 仓库工作树里留下分钟回填产物（测试数据不许落工作树）：'; printf '%s\n' "$STRAY"; exit 1; }
# ⑨ 运行时实证：四个包的分钟用例必须真跑到（静态锁只防回潮，用例才是把语义钉住的那一层）
MT=$(go test -count=1 ./internal/store/ ./cmd/dataload/ ./internal/btreplay/ ./internal/scheduler/ \
	-run 'Minute|HasBars|ApproxNote|MACD' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' || true)
printf '%s\n' "$MT"
if printf '%s' "$MT" | grep -qE 'FAIL'; then
	echo '--- FAIL: §MINUTE-K 分钟用例跑红（见上面输出）'; exit 1
fi
OKPKG=$(printf '%s' "$MT" | grep -c '^ok' || true)
[ "$OKPKG" = "4" ] \
	|| { echo "--- FAIL: §MINUTE-K 分钟用例没有四包全跑（ok 行数 ${OKPKG} != 4：包被改名/用例会话没匹配上，等于这段锁没生效）"; exit 1; }
echo "ok - §MINUTE-K 守卫通过（建表三件套 3 + 无复权列负锁 1 + 日切片前缀锁 2/负锁 1 + 窗口唯一闸 1 + 装载诚实出门 4 + 回放注入点 1 + 调度接线 5 + 跨文件 scale 同源 1 + §87 锚点 2 + 现场锁 1 + 运行时实证 2）"

echo "==> 92 §MINUTE-K-CHAIN 分钟链两修：代码形态归一 + 装载器只走严格不复权链（2026-09-24 回填实跑锤出）..."
# 这段锁钉的是 09-24 **第一次真跑** minute-sync 才暴露的两个坑（干跑与单测都测不出来，因为桩上游
# 不认代码形态、也不区分复权口径）：
#   ① 代码形态：新浪/腾讯/同花顺三条分钟腿只认裸 6 位代码。装载器传的是 ts_code（"600000.SH"），
#      于是新浪把 "sh600000.SH" 当代码直接回 null（**0 根、无错误**），腾讯回数组壳（解到 map 上
#      报 unmarshal 错）。日志长得像"三个源都坏了所以降级到只给 140 根的同花顺"，实际一条都没
#      真取到数——失败率 66.7% 判红才是唯一线索。归一放在链入口（所有调用方受益），落库主键不动。
#   ② 复权口径：链尾东财腿固定 fqt=1（前复权，全系统日线口径），而 minute_klines 的承诺是**不复权**
#      （与实盘 md.MinuteMACD 同源）。落库窗口跨 5 个月，前复权根一旦进来，除权日之前整段价格被
#      平移，回放会算出一根假跳水——这正是 §H3 在日 K 链上拒收过的"跨口径静默兜底"。所以装载器
#      改绑新增的 GetUnadjustedMinuteKLine（三腿之外宁可计成失败），实盘看当日分时的 GetMinuteKLine
#      保留末腿不动（当日 bars 前复权＝不复权，摘掉它等于把实盘兜底也削了）。
MK_SRC=internal/data/source.go
# §P2-L（2026-10-07 波 4）：本段用 `"$MK_LOAD"` 做静态 grep，而它原来只在 §91 定义。
# 收集模式下 §91 判红时本段会读到空串 ⇒ `grep -c '…' ""` 报错 ⇒ 归属变成"§92 缺陷"，
# 而真正坏的是 §91。路径变量没有语义依赖，直接本段自带定义（理由同 §23 的 dg）。
MK_LOAD=cmd/dataload/minute_sync.go
MK_SRC_TEST=internal/data/source_minute_chain_test.go
# ① 归一必须发生在分钟链函数体内（写在别的函数里等于没写）
awk '/^func \(dc \*DataCoordinator\) minuteKLineChain\(/{f=1} f{print} f && /^}$/{exit}' "$MK_SRC" | grep -q 'code := normalizeCode(rawCode)' \
	|| { echo '--- FAIL: §MINUTE-K-CHAIN 分钟链入口不再把入参归一成裸代码（ts_code 形态喂进去＝新浪空返回、腾讯解析错，全线假降级）'; exit 1; }
NC=$(grep -c 'normalizeCode(rawCode)' "$MK_SRC" || true)
[ "$NC" = "1" ] \
	|| { echo "--- FAIL: §MINUTE-K-CHAIN 分钟链的代码归一点不再是 1 处（读到 ${NC}：多处各归一必有一处漏）"; exit 1; }
# 两条链各自唯一：通用链带末腿、严格链不带
CW1=$(grep -c 'return dc.minuteKLineChain(code, scale, count, true)' "$MK_SRC" || true)
CW2=$(grep -c 'return dc.minuteKLineChain(code, scale, count, false)' "$MK_SRC" || true)
{ [ "$CW1" = "1" ] && [ "$CW2" = "1" ]; } \
	|| { echo "--- FAIL: §MINUTE-K-CHAIN 分钟链两个入口不再是「一真一假」各一处（GetMinuteKLine=${CW1} GetUnadjustedMinuteKLine=${CW2}）"; exit 1; }
# ② 严格链的失败文案必须点名"前复权腿按口径拒用"——只说"所有源均失败"会把人往"源坏了"方向带
grep -q '前复权腿按口径拒用' "$MK_SRC" \
	|| { echo '--- FAIL: §MINUTE-K-CHAIN 严格不复权链的失败原因不再点名东财末腿被口径拒用（排障时会去查源，其实是被主动摘掉的）'; exit 1; }
# 装载器必须绑严格链，且工作树里不得再出现"装载器调通用链"的活代码
L1=$(grep -c 'dc.GetUnadjustedMinuteKLine(code, o.Scale, o.Count)' "$MK_LOAD" || true)
[ "$L1" = "1" ] \
	|| { echo '--- FAIL: §MINUTE-K-CHAIN minute-sync 不再走严格不复权链（回到通用链＝前复权根能写进不复权表）'; exit 1; }
if grep -rq '\.GetMinuteKLine(' cmd/dataload/; then
	echo '--- FAIL: §MINUTE-K-CHAIN cmd/dataload 里又出现通用分钟链调用（前复权末腿会漏进落库路径）'; exit 1
fi
# ③ 运行时实证：三条分钟链用例（URL 形态 / 末腿零请求 / 逐腿记账）必须真跑绿
CT=$(go test -count=1 ./internal/data/ -run 'TestMinuteChain|TestUnadjustedMinuteChain' 2>&1 | grep -E '^(--- FAIL|FAIL|ok|no test files)' || true)
printf '%s\n' "$CT"
if printf '%s' "$CT" | grep -qE 'FAIL|no test files'; then
	echo '--- FAIL: §MINUTE-K-CHAIN 分钟链用例跑红或没跑到（见上面输出）'; exit 1
fi
TN=$(grep -c '^func Test' "$MK_SRC_TEST" || true)
[ "$TN" = "3" ] \
	|| { echo "--- FAIL: §MINUTE-K-CHAIN 分钟链用例数不再是 3 条（读到 ${TN}：URL 形态/末腿拒用/逐腿记账 缺一即失效）"; exit 1; }
echo "ok - §MINUTE-K-CHAIN 守卫通过（链入口代码归一 2 + 两入口一真一假 1 + 拒用文案 1 + 装载器绑严格链 2 + 运行时实证 2）"

echo ""
echo "==> 93 §MINUTE-OPS 生产侧两条正规通道（桥策略落位 + 分钟 K 回填）：缺省只预览 + 动手须 -Apply + 判据纯 ASCII + 半态不读成成功（2026-09-24 owner 令「把这两步变成正规脚本」）..."
OPS_SH=scripts/backfill_minute_guangzhou.sh
BR_SH=scripts/place_qmt_bridge.sh
OPS_LOAD=cmd/dataload/minute_sync.go
OPS_STORE=internal/store/minute_klines.go
# ① 两条通道必须在位、可执行、语法过（历史上这两步只存在于人脑与 RUNBOOK 的手敲命令里）
for f in "$OPS_SH" "$BR_SH"; do
	{ [ -f "$f" ] && [ -x "$f" ]; } \
		|| { echo "--- FAIL: §MINUTE-OPS $f 缺失或没有执行位（缺省预览型脚本也要能直接跑）"; exit 1; }
	bash -n "$f" || { echo "--- FAIL: §MINUTE-OPS $f 语法不过（bash -n）"; exit 1; }
done
# ② 缺省方向＝只预览：APPLY 缺省 0、只认 -Apply、预览分支一条 SSH 都不发
OPS_A0=$(grep -c '^APPLY=0$' "$OPS_SH" || true)
OPS_APPLY=$(grep -c -- '-Apply) APPLY=1' "$OPS_SH" || true)
OPS_GUARD=$(grep -c 'MODE=$MODE 会动生产' "$OPS_SH" || true)
BR_A0=$(grep -c '^APPLY=0$' "$BR_SH" || true)
BR_APPLY=$(grep -c -- '-Apply) APPLY=1' "$BR_SH" || true)
{ [ "$OPS_A0" = "1" ] && [ "$OPS_APPLY" = "1" ] && [ "$BR_A0" = "1" ] && [ "$BR_APPLY" = "1" ]; } \
	|| { echo "--- FAIL: §MINUTE-OPS 缺省方向不再是「只预览」（回填=${OPS_A0}/${OPS_APPLY} 落位=${BR_A0}/${BR_APPLY}；动手必须显式 -Apply，见 §OPS-ALIGN）"; exit 1; }
[ "$OPS_GUARD" = "1" ] \
	|| { echo "--- FAIL: §MINUTE-OPS 回填脚本的「MODE=apply 还差 -Apply」降级守卫不在了（读到 ${OPS_GUARD}：MODE 与 -Apply 两个开关必须同时给才动手）"; exit 1; }
OPS_MODE_DEF=$(grep -c 'MODE="${MODE:-plan}"' "$OPS_SH" || true)
[ "$OPS_MODE_DEF" = "1" ] \
	|| { echo "--- FAIL: §MINUTE-OPS 回填脚本 MODE 缺省不再是 plan（读到 ${OPS_MODE_DEF}：缺省即动手＝安全阀失效）"; exit 1; }
# ③ 判据必须纯 ASCII：远端脚本体（送进 PowerShell 的那段串）零非 ASCII 字节 + Go 侧机读孪生在位
#    取法：REMOTE_PS=' 到下一处单独一行的 ' 之间。掺中文＝GBK 回传乱码＝脚本把"读不到"当"跑过了"。
for f in "$OPS_SH" "$BR_SH"; do
	BODY=$(sed -n "/^REMOTE_PS='/,/^'$/p" "$f" | LC_ALL=C grep -c '[^ -~]' || true)
	[ "${BODY:-0}" = "0" ] \
		|| { echo "--- FAIL: §MINUTE-OPS $f 的远端脚本体掺了 ${BODY} 行非 ASCII（会被 GBK 回传打乱，判据必须走 ASCII 锚点/码位拼名）"; exit 1; }
done
grep -q 'func (s MinuteStats) ASCII()' "$OPS_STORE" \
	|| { echo '--- FAIL: §MINUTE-OPS 分钟统计表读数丢了机读孪生 ASCII()（脚本侧只能按空格切字段，中文读数＝读不到）'; exit 1; }
OPS_ANCH=$(grep -c 'MINUTE-SYNC \(SUMMARY\|PROGRESS\|START\)' "$OPS_LOAD" || true)
{ [ "$OPS_ANCH" -ge 3 ]; } \
	|| { echo "--- FAIL: §MINUTE-OPS 装载器的 ASCII 锚点行少于 3 类（读到 ${OPS_ANCH}：START/PROGRESS/SUMMARY 少了任何一类，运维脚本就只能猜）"; exit 1; }
# 锚点行在日志里不在行首（dataload 的 log.SetFlags 打时间戳前缀），所以取值一律用**不带 ^** 的 grep
OPS_CARET=$(grep -c 'grep "\^MINUTE-SYNC' "$OPS_SH" || true)
[ "$OPS_CARET" = "0" ] \
	|| { echo "--- FAIL: §MINUTE-OPS 回填脚本又用 ^ 锚定锚点行（读到 ${OPS_CARET}：日志行有时间戳前缀，^ 恒不命中＝永远判「未收尾」，反过来也永远拿不到成功）"; exit 1; }
# ④′ CRLF 判据锁（09-24 首次真跑锤出的假红）：Windows 回传每行 CRLF，命令替换只吃 \n，
#    行尾最后一个字段必然带 \r——两条脚本都必须在解析前 tr -d '\r'，否则 SHA/字段比较恒不等。
for crlf in "$BR_SH" "$OPS_SH"; do
	OPS_CR=$(grep -c "tr -d '\\\\r'" "$crlf" || true)
	[ "$OPS_CR" -ge 1 ] \
		|| { echo "--- FAIL: §MINUTE-OPS ${crlf} 解析远端回传前没有去 CR（tr -d '\\r' 计数 ${OPS_CR}：CRLF 会让行尾最后一个字段带 \\r，等值比较假红）"; exit 1; }
done
# ④″ 一次性计划任务的「日期写法 + 真的会自触」锁（09-24 首次真装连踩两次，都是"报了成功但活不会干"）：
#    ① 日期必须**数值拼装**。写格式串有两条死路：.NET 自定义格式串里 mm 是**分钟**，所以 yyyy/mm/dd 在
#       09-24 当天写出 /SD 2026-06-24 —— schtasks 照单收下，装了个起始日期在**过去**的一次性任务：
#       任务存在、永不触发，脚本却回打 MINOPS STARTED；改用 (Get-Culture).ShortDatePattern 又不行，
#       ssh/EncodedCommand 会话里的文化不是交互文化，给出 MM/dd/yyyy，schtasks 直接判「无效的起始日期」。
#    ② 装完必须用**文化无关**的 StartBoundary（任务 XML 里是 ISO 8601）证明触发点就在眼前；读不到、
#       或不在 now-2min ~ now+10min 之间，就删掉任务判红。/FO LIST 的列名是本地化文本，只配当"在不在"检查。
OPS_SD=$(grep -cF -- '-f $now.Year, $now.Month, $now.Day' "$OPS_SH" || true)
[ "$OPS_SD" = "1" ] \
	|| { echo "--- FAIL: §MINUTE-OPS /SD 不再是数值拼装的四位年-月-日（读到 ${OPS_SD}：格式串会把 mm 当月份，或按会话文化给出 schtasks 不收的写法）"; exit 1; }
OPS_CULT=$(grep -c 'Get-Culture' "$OPS_SH" || true)
[ "$OPS_CULT" = "0" ] \
	|| { echo "--- FAIL: §MINUTE-OPS 又回到按会话文化取日期格式（读到 ${OPS_CULT} 处 Get-Culture：远程会话的文化不等于机器文化，schtasks 会拒）"; exit 1; }
# 只锁「/SD 由格式串算出来」这一件事（HHmm 一类合法时间格式串不误伤）
OPS_SD_FMT=$(grep -cE '^\$sd = .*ToString' "$OPS_SH" || true)
[ "$OPS_SD_FMT" = "0" ] \
	|| { echo "--- FAIL: §MINUTE-OPS 的 /SD 又改回用格式串算（读到 ${OPS_SD_FMT} 处：格式串里 mm 是分钟，09-24 那次「装了个过去日期」的假 STARTED 就这么来的）"; exit 1; }
for need in '<StartBoundary>' 'trigger-not-upcoming' 'trigger-unreadable'; do
	OPS_TB=$(grep -cF "$need" "$OPS_SH" || true)
	[ "$OPS_TB" -ge 1 ] \
		|| { echo "--- FAIL: §MINUTE-OPS 装任务后不再核 StartBoundary（缺 ${need}：把「建了个任务」当成「回填会在一分钟后自触」，正是首装的误报）"; exit 1; }
done
# ④ 离线实跑：预览模式一次网络都不碰（bogus IP 也要 0 退出），动手模式对不可达通道必须 fail-closed
BR_PLAN=$(GZ_IP=127.0.0.1 "$BR_SH" 2>&1 || true)
printf '%s\n' "$BR_PLAN" | grep -q 'BRIDGE_PLACE_PLAN' \
	|| { echo '--- FAIL: §MINUTE-OPS 落位脚本预览模式没打 BRIDGE_PLACE_PLAN（缺省方向被改坏，或预览里混进了网络调用）'; exit 1; }
OPS_PLAN=$(GZ_IP=127.0.0.1 "$OPS_SH" 2>&1 || true)
printf '%s\n' "$OPS_PLAN" | grep -q 'MINUTE_OPS_PLAN' \
	|| { echo '--- FAIL: §MINUTE-OPS 回填脚本预览模式没打 MINUTE_OPS_PLAN（同上）'; exit 1; }
# 动手分支必须"先本机自检、再连生产"，且连不上就判红（绝不落到可能挂起的密码认证）
# ⚠ 本枚锁有一条**提交顺序依赖**（2026-10-07 波 2 实录，别把它当成代码坏了）：落位脚本 :89 的
#    未提交守卫查的就是 BRIDGE_SRC=qmt_gateway/qmt_bridge_strategy.py，于是**凡本批改过桥策略文件**
#    （哪怕只加注释），预提交跑 -full 时这条必红——它在 -Apply 之前就 exit 1，ARMED 那行压根不打印。
#    本轮实测：VERIFY_EXIT=1、唯一 FAIL 停在本枚；把桥文件提交后 clean tree 复跑即绿。
#    这不是判据错（守卫本来就是"半成品不许进实盘加载位"），但它是 P2-L 那一族「锁读的是工作区
#    状态而不是提交态」的实例：门禁在**未提交**树上自证，读到的永远是"下一版之前的世界"。
#    因此本枚不做放宽（改安全阀方向＝拿验证便利换资金安全）；正确处置是按上面的顺序跑，
#    真正的修法收进波 4 的 -collect：把这类"前置条件不满足"计成 SKIP-PRECONDITION 而不是 FAIL，
#    并在末尾计数里单列（判据缺失要显形，不许静悄悄少跑一段）。
BR_ARM=$(GZ_IP=127.0.0.1 "$BR_SH" -Apply 2>&1 || true)
{ printf '%s\n' "$BR_ARM" | grep -q 'BRIDGE_PLACE_ARMED' && printf '%s\n' "$BR_ARM" | grep -q 'BatchMode'; } \
	|| { echo '--- FAIL: §MINUTE-OPS 落位脚本的 -Apply 分支不再「先 ASCII 自检后 BatchMode 预探测」（ARMED/预探测判红缺一：要么自检被绕过，要么会挂起）'; exit 1; }
if GZ_IP=127.0.0.1 "$BR_SH" -Apply >/dev/null 2>&1; then
	echo '--- FAIL: §MINUTE-OPS 落位脚本在通道不通时居然 0 退出（降级报成功，§M2 族）'; exit 1
fi
OPS_ARM=$(GZ_IP=127.0.0.1 MODE=status "$OPS_SH" 2>&1 || true)
{ printf '%s\n' "$OPS_ARM" | grep -q 'MINUTE_OPS_ARMED' && printf '%s\n' "$OPS_ARM" | grep -q 'BatchMode'; } \
	|| { echo '--- FAIL: §MINUTE-OPS 回填脚本的状态分支不再「先自检后预探测」（同上）'; exit 1; }
if GZ_IP=127.0.0.1 MODE=status "$OPS_SH" >/dev/null 2>&1; then
	echo '--- FAIL: §MINUTE-OPS 回填脚本连不上生产却 0 退出（状态未知当成功）'; exit 1
fi
# ⑤ 半态不读成成功：造一个假 ssh 回放远端七种回传，逐态核「退出码 + 状态标记」两值
#    这是本组唯一能证明"判读逻辑本身不是恒绿"的探针（④ 只证明 fail-closed）。
OPS_FAKE=$(mktemp -d)
cat >"$OPS_FAKE/ssh" <<'FAKE'
#!/usr/bin/env bash
# 假 ssh：给 §MINUTE-OPS 探针回放远端 PowerShell 的六种回传（不碰网络）。
# ★ 外面套一层 `{ … } | sed 's/$/\r/'`：Windows 侧回传每行是 **CRLF**，而 bash 命令替换只吃 \n，
#   于是行尾最后一个字段会留一个 \r——09-24 桥落位首次真跑就是被它判成假红（三条 SHA 肉眼全同、
#   脚本说 dst_sha != 本机）。回放不带 \r 的"干净"文本等于把这条真实缺陷排除在测试之外。
{
if printf '%s' "$*" | grep -q EncodedCommand; then
	case "${OPS_FAKE_STATE:-done}" in
		done)
			echo 'MINOPS STATE task=1 procs=0 log=backfill-minute-x.log bytes=98765 mtime=20260924-224110'
			echo '2026/09/24 21:59:01.123456 MINUTE-SYNC START mode=backfill scale=5 count=5025 universe=500'
			echo '2026/09/24 22:03:11.123456 MINUTE-SYNC PROGRESS done=50 universe=500 written=248000'
			echo '2026/09/24 22:41:10.500000 MINUTE-SYNC SUMMARY scale=5 rows=2480713 codes=500 first=2026-04-08T13:50:00 last=2026-09-24T15:00:00 avg_bars=47.6 written=2480713 failed=0 universe=500 exit=0'
			;;
		failed)
			echo 'MINOPS STATE task=1 procs=0 log=backfill-minute-x.log bytes=1200 mtime=20260924-220000'
			echo '2026/09/24 22:00:00.500000 MINUTE-SYNC SUMMARY scale=5 rows=0 codes=0 first= last= avg_bars=0.0 written=0 failed=500 universe=500 exit=1 reason=zero_rows'
			;;
		running)
			echo 'MINOPS STATE task=1 procs=1 log=backfill-minute-x.log bytes=4096 mtime=20260924-220500'
			echo '2026/09/24 22:03:11.123456 MINUTE-SYNC PROGRESS done=50 universe=500 written=248000'
			;;
		stalled)
			echo 'MINOPS STATE task=1 procs=0 log=backfill-minute-x.log bytes=4096 mtime=20260924-220500'
			echo '2026/09/24 22:03:11.123456 MINUTE-SYNC PROGRESS done=50 universe=500 written=248000'
			;;
		pending)
			echo 'MINOPS STATE task=1 procs=0 log=none dir=C:\var\lib\quant-trading-v2'
			;;
		notinstalled)
			echo 'MINOPS STATE task=0 procs=0 log=none dir=C:\var\lib\quant-trading-v2'
			;;
		garbled)
			echo 'MINOPS STATE task=1 procs=0 log=backfill-minute-x.log bytes=4096 mtime=20260924-220500'
			echo '2026/09/24 22:03:11.123456 MINUTE-SYNC PROGRESS done=50 universe=500 written=248000'
			echo '2026/09/24 22:41:10.5 MINUTE-SYNC SUMM'
			;;
	esac
	exit 0
fi
echo ok
exit 0
} | sed 's/$/\r/'
FAKE
chmod +x "$OPS_FAKE/ssh"
for spec in done:done:0 failed:failed:1 running:running:0 stalled:stalled:1 pending:pending:1 notinstalled:not_installed:1 garbled:stalled:1; do
	fstate=${spec%%:*}
	rest=${spec#*:}
	want_mark=${rest%%:*}
	want_code=${rest##*:}
	if out=$(PATH="$OPS_FAKE:$PATH" GZ_IP=127.0.0.1 MODE=status OPS_FAKE_STATE="$fstate" "$OPS_SH" 2>&1); then got_code=0; else got_code=1; fi
	got_mark=$(printf '%s\n' "$out" | grep -o "MINUTE_OPS_STATE [a-z_]*" | tail -1 || true)
	{ [ "$got_code" = "$want_code" ] && [ "$got_mark" = "MINUTE_OPS_STATE $want_mark" ]; } \
		|| { echo "--- FAIL: §MINUTE-OPS 远端回传「${fstate}」被判成 ${got_mark:-无状态标记}/exit=${got_code}（应为 MINUTE_OPS_STATE ${want_mark}/exit=${want_code}：半态/失败态被读成成功，正是本仓 §M2 族最忌的降级报成功）"; rm -rf "$OPS_FAKE"; exit 1; }
done
rm -rf "$OPS_FAKE"
# ⑥ 负锁：不新增凭据、不碰实盘账本、不许出现密码认证兜底
OPS_NEG=$(grep -c 'live\.db' "$OPS_SH" || true)
[ "$OPS_NEG" = "0" ] \
	|| { echo "--- FAIL: §MINUTE-OPS 回填脚本里出现了 live.db（读到 ${OPS_NEG}：回填只准写研究库 trading.db，碰实盘账本即资金链路事故）"; exit 1; }
if grep -qiE 'sshpass|PreferredAuthentications=password' "$OPS_SH" "$BR_SH"; then
	echo '--- FAIL: §MINUTE-OPS 两条脚本又引入密码认证兜底（本仓纪律：BatchMode 不通即判红，绝不挂起等人输密码）'; exit 1
fi
BR_BOM=$(grep -c 'place_bridges.ps1' "$BR_SH" || true)
{ [ "$BR_BOM" -ge 2 ]; } \
	|| { echo "--- FAIL: §MINUTE-OPS 落位脚本不再复用仓库版 place_bridges.ps1（读到 ${BR_BOM}：自造远端拷贝会把 BOM/路径口径散成两套）"; exit 1; }
# ⑦ 运行时实证：锚点行用例（成功/零行/清单为空/进度节拍 × 纯 ASCII 断言）必须真跑绿
OT=$(go test -count=1 ./cmd/dataload/ -run 'TestMinuteSyncAnchorLinesMachineReadable' 2>&1 | grep -E '^(--- FAIL|FAIL|ok|no test files)' || true)
printf '%s\n' "$OT"
if printf '%s' "$OT" | grep -qE 'FAIL|no test files'; then
	echo '--- FAIL: §MINUTE-OPS 锚点行用例跑红或没跑到（见上面输出）'; exit 1
fi
OSUB=$(grep -c 't.Run(' cmd/dataload/minute_sync_test.go || true)
[ "$OSUB" = "4" ] \
	|| { echo "--- FAIL: §MINUTE-OPS 锚点行用例的子用例数不再是 4（读到 ${OSUB}：成功/零行/清单为空/进度节拍 缺一即失效）"; exit 1; }
echo "ok - §MINUTE-OPS 守卫通过（脚本在位+语法 4 + 缺省只预览 6 + ASCII 判据 5 + 计划任务触发锁 6 + 离线实跑 6 + 半态七态联调 14 + 负锁 3 + 运行时实证 2）"

echo ""
echo "==> 94 §FILL-AMEND-CLI 历史错账改判的正规通道 scripts/amend_fill_guangzhou.sh：缺省只预览（**量出来**的零 POST）+ 动手须 --apply 与 --yes 两个开关 + 令牌只走 stdin + 远端体纯 ASCII + 本地夹具跑通 create→apply→revoke（2026-09-25 owner 令「落笔这件事你来做，全部做完」）..."
AM_SH=scripts/amend_fill_guangzhou.sh
AM_SRV_ROUTES=internal/server/server.go
# ① 在位 / 可执行 / 语法过（这条通道存在的意义就是"不必开浏览器点四下"，所以它自己必须能直接跑）
{ [ -f "$AM_SH" ] && [ -x "$AM_SH" ]; } \
	|| { echo "--- FAIL: §FILL-AMEND-CLI $AM_SH 缺失或没有执行位"; exit 1; }
bash -n "$AM_SH" || { echo "--- FAIL: §FILL-AMEND-CLI $AM_SH 语法不过（bash -n）"; exit 1; }
# ② 缺省方向＝只预览；写动作要 MODE 与 --yes 两个开关同时给（误敲一个 flag 不能碰到钱账）
AM_DEF=$(grep -c '^MODE="preview"' "$AM_SH" || true)
[ "$AM_DEF" = "1" ] \
	|| { echo "--- FAIL: §FILL-AMEND-CLI MODE 缺省不再是 preview（读到 ${AM_DEF}：缺省即动手＝安全阀失效）"; exit 1; }
AM_YES_SW=$(grep -c -- '--yes) YES=1' "$AM_SH" || true)
AM_YES_GUARD=$(grep -c '必须同时给 --yes' "$AM_SH" || true)
{ [ "$AM_YES_SW" = "1" ] && [ "$AM_YES_GUARD" = "1" ]; } \
	|| { echo "--- FAIL: §FILL-AMEND-CLI 的 --yes 双开关不再唯一（开关=${AM_YES_SW} 守卫=${AM_YES_GUARD}）"; exit 1; }
# ③ 令牌只走 stdin："$TOKEN" 的每一次展开都只能落在三种形态里——
#    `printf '%s' "$TOKEN" | …`（送进远端/夹具的标准输入）、泄漏自检的 `grep -aF -- "$TOKEN" "$OUTFILE"`、
#    以及取到值后的空判 `[ -z "$TOKEN" ]`。出现别的形态（拼进 curl -H / ssh 命令行 / echo）
#    就等于把管理员凭据打进 ps 与日志——那是本仓口令纪律的一次违例，不是风格问题。
AM_TOK_BAD=$(grep -nF '"$TOKEN"' "$AM_SH" | grep -v -e 'TOKEN" | ' -e 'TOKEN" "$OUTFILE"' -e '\[ -z "$TOKEN" \]' | wc -l | tr -d ' ' || true)
[ "${AM_TOK_BAD:-1}" = "0" ] \
	|| { echo "--- FAIL: §FILL-AMEND-CLI 有 ${AM_TOK_BAD} 处令牌变量不在 stdin 腿上（凭据可能进命令行/日志）："; grep -nF '"$TOKEN"' "$AM_SH"; exit 1; }
# ③′ 反证：上面那条锁自己不是恒绿——造一行"echo 令牌"喂进同一条管道，必须被判出来。
AM_TOK_PROOF=$(printf '%s\n' '	  echo "$TOKEN" 这是故意造的违例行' \
	| grep -nF '"$TOKEN"' | grep -v -e 'TOKEN" | ' -e 'TOKEN" "$OUTFILE"' -e '\[ -z "$TOKEN" \]' | wc -l | tr -d ' ' || true)
[ "${AM_TOK_PROOF:-0}" = "1" ] \
	|| { echo "--- FAIL: §FILL-AMEND-CLI 的令牌形态锁自己失效（反证行被判 ${AM_TOK_PROOF} 处，应为 1：这条锁已经是恒绿摆设）"; exit 1; }
AM_TOK_PERM=$(grep -c '先 chmod 600' "$AM_SH" || true)
AM_TOK_TREE=$(grep -c '令牌文件在仓库工作树内' "$AM_SH" || true)
{ [ "$AM_TOK_PERM" -ge 1 ] && [ "$AM_TOK_TREE" -ge 1 ]; } \
	|| { echo "--- FAIL: §FILL-AMEND-CLI 的令牌门被拆（权限 600 校验=${AM_TOK_PERM}、工作树外校验=${AM_TOK_TREE}：凭据落进工作树就有被 commit 的口子）"; exit 1; }
# ④ 远端脚本体必须纯 ASCII（掺中文＝GBK 回传乱码＝脚本把"读不到"当"跑过了"）
#    反证前置：先证明"抽取"这一步真的抽到了两段脚本体（抽不到时非 ASCII 计数恒 0＝锁变摆设）。
AM_BODY_LINES=$(sed -n '/cat <<PSEOF/,/^PSEOF$/p' "$AM_SH" | wc -l | tr -d ' ' || true)
{ [ "${AM_BODY_LINES:-0}" -ge 40 ]; } \
	|| { echo "--- FAIL: §FILL-AMEND-CLI 抽不到远端脚本体（读到 ${AM_BODY_LINES} 行，应 ≥40：heredoc 标记被改，下面的 ASCII 锁就成恒绿摆设）"; exit 1; }
AM_BODY_BAD=$(sed -n '/cat <<PSEOF/,/^PSEOF$/p' "$AM_SH" | LC_ALL=C grep -c '[^ -~]' || true)
[ "${AM_BODY_BAD:-0}" = "0" ] \
	|| { echo "--- FAIL: §FILL-AMEND-CLI 远端 PowerShell 脚本体掺了 ${AM_BODY_BAD} 行非 ASCII（中文注释/字面量必须挪出 heredoc 或走码位/Base64）"; exit 1; }
AM_ENC=$(grep -c -- '-EncodedCommand' "$AM_SH" || true)
# ④′ ASCII 判据自身的反证：BSD grep 没有 -P（PCRE），所以这里用 POSIX 字符范围；
#     范围写法若在某个平台上不成立，这条反证会立刻红，而不是让 ④ 变成恒绿。
AM_ASCII_PROOF=$(printf 'a \345\215\226 b\n' | LC_ALL=C grep -c '[^ -~]' || true)
[ "${AM_ASCII_PROOF:-0}" = "1" ] \
	|| { echo "--- FAIL: §FILL-AMEND-CLI 的 ASCII 判据在本机不成立（含中文样本被判 ${AM_ASCII_PROOF} 行，应为 1：④ 那条锁不可信）"; exit 1; }
AM_CR=$(grep -c "tr -d '\\\\r'" "$AM_SH" || true)
{ [ "$AM_ENC" -ge 1 ] && [ "$AM_CR" -ge 1 ]; } \
	|| { echo "--- FAIL: §FILL-AMEND-CLI 的远端执行姿势变了（EncodedCommand=${AM_ENC}、去 CR=${AM_CR}：四层转义与 CRLF 都是实录锤出来的，退回原样必踩）"; exit 1; }
# ⑤ 不新增任何写能力：只打 HTTP 端点，端点名必须与 server.go 注册行逐字同源（路由改名要立刻红）
if grep -qE 'sqlite3|UPDATE fills|DELETE FROM fills' "$AM_SH"; then
	echo '--- FAIL: §FILL-AMEND-CLI 出现直连库或改写成交行的路径（勘误只能是"追加一条决定"，柜台证据不可改写）'; exit 1
fi
for amep in '/api/qmt/trades' '/api/qmt/fill-amendments' '/api/qmt/fills/conservation'; do
	AM_IN_SH=$(grep -cF "$amep" "$AM_SH" || true)
	AM_IN_SRV=$(grep -cF "$amep" "$AM_SRV_ROUTES" || true)
	{ [ "$AM_IN_SH" -ge 1 ] && [ "$AM_IN_SRV" -ge 1 ]; } \
		|| { echo "--- FAIL: §FILL-AMEND-CLI 端点 ${amep} 两侧对不上（脚本 ${AM_IN_SH} 处 / 路由注册 ${AM_IN_SRV} 处：后端改名或脚本自己造口，都会指向不存在的路）"; exit 1; }
done
# ⑥ 运行时实证（本地夹具 = 假 admin 服务，只监听 127.0.0.1，不碰生产也不碰任何库文件）：
#    把 ②③④ 的判据**跑一遍**，尤其是"预览一个 POST 都不发"——静态 grep 证不了这件事。
AM_TMP=$(mktemp -d)
cat >"$AM_TMP/stub.py" <<'AMEOF'
# §FILL-AMEND-CLI 夹具：复刻五个 admin 端点的语义（未鉴权 401 / 同向 400 / 活跃冲突 409），
# 并把每次 POST 的路径追加进 posts 文件——"预览零 POST"这条断言完全依赖这份记账。
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
tok = open(sys.argv[1]).read().strip()
posts_path = sys.argv[3]
FILLS = [{"id": 14, "code": "603468.SH", "side": "买入", "orig_side": "买入", "price": 22.55,
          "qty": 900, "amount": 20295.0, "traded_at": "2026-09-22 10:08:32", "trade_id": "T14",
          "amend_key": "t:T14"}]
AMENDS = []


def eff(f):
    for a in AMENDS:
        if a["fill_id"] == f["id"] and a["status"] == "applied":
            return a["new_side"]
    return f["side"]


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def okauth(self):
        return self.headers.get("Authorization", "") == "Bearer " + tok

    def send(self, code, obj):
        b = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if not self.okauth():
            self.send(401, {"error": "missing authorization token"}); return
        if self.path.startswith("/api/qmt/trades"):
            out = [dict(f, side=eff(f)) for f in FILLS]
            self.send(200, {"ok": "1", "fills": out, "summary": {}}); return
        if self.path.startswith("/api/qmt/fill-amendments"):
            self.send(200, {"ok": "1", "amendments": list(AMENDS)}); return
        if self.path.startswith("/api/qmt/fills/conservation"):
            # 持仓不变量按"生效方向"重放：603468 记成买入时账上少 900 股（勘误生效即归零）。
            lines = [{"code": "603468.SH", "replayed_qty": 900, "book_qty": 0, "diff": -900, "note": "stub"}] \
                if eff(FILLS[0]) == "买入" else []
            self.send(200, {"ok": "1", "report": {
                "day": "2026-09-25", "ok": not lines,
                "applied_amendments": sum(1 for a in AMENDS if a["status"] == "applied"),
                "position_lines": lines,
                "cash": {"checked": True, "diff": 0.0, "book_cash": 0.03, "expected_cash": 0.03}}}); return
        self.send(404, {"error": "no route"})

    def do_POST(self):
        open(posts_path, "a").write(self.path + "\n")
        if not self.okauth():
            self.send(401, {"error": "missing authorization token"}); return
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0) or 0))
        try:
            req = json.loads(raw or b"{}")
        except Exception:
            self.send(400, {"error": "bad json"}); return
        if self.path.endswith("/apply"):
            aid = int(self.path.split("/")[4])
            for a in AMENDS:
                if a["id"] == aid:
                    if a["status"] != "pending":
                        self.send(409, {"error": "仅待批准可批准"}); return
                    a["status"] = "applied"; a["applied_at"] = "2026-09-25 08:00:00"
                    self.send(200, {"ok": "1", "amendment": a}); return
            self.send(404, {"error": "not found"}); return
        if self.path.endswith("/revoke"):
            aid = int(self.path.split("/")[4])
            for a in AMENDS:
                if a["id"] == aid:
                    a["status"] = "revoked"
                    self.send(200, {"ok": "1", "amendment": a}); return
            self.send(404, {"error": "not found"}); return
        if self.path.endswith("/api/qmt/fill-amendments"):
            fid, side, reason = req.get("fill_id"), req.get("new_side"), (req.get("reason") or "").strip()
            if not reason:
                self.send(400, {"error": "理由必填"}); return
            if side not in ("买入", "卖出") or side == FILLS[0]["side"]:
                self.send(400, {"error": "方向非法"}); return
            for a in AMENDS:
                if a["fill_id"] == fid and a["status"] != "revoked":
                    self.send(409, {"error": "已有活跃勘误"}); return
            AMENDS.append({"id": len(AMENDS) + 1, "fill_id": fid, "code": FILLS[0]["code"], "qty": 900,
                           "orig_side": FILLS[0]["side"], "new_side": side, "status": "pending",
                           "operator": "fixture", "created_at": "2026-09-25 08:00:00", "applied_at": ""})
            self.send(201, {"ok": "1", "amendment": AMENDS[-1]}); return
        self.send(404, {"error": "no route"})


HTTPServer(("127.0.0.1", int(sys.argv[2])), H).serve_forever()
AMEOF
printf 'fixture-amend-token-verify-only' >"$AM_TMP/tok"
chmod 600 "$AM_TMP/tok"
AM_PORT=$(python3 -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()')
python3 "$AM_TMP/stub.py" "$AM_TMP/tok" "$AM_PORT" "$AM_TMP/posts.log" >"$AM_TMP/stub.out" 2>&1 &
AM_PID=$!
am_wait=$(python3 - "$AM_PORT" "$AM_TMP/tok" <<'PY' || true
import sys, time, urllib.request
port, tokf = sys.argv[1], sys.argv[2]
tok = open(tokf).read().strip()
for _ in range(25):
    try:
        r = urllib.request.urlopen(urllib.request.Request(
            "http://127.0.0.1:%s/api/qmt/trades" % port, headers={"Authorization": "Bearer " + tok}))
        if r.status == 200:
            print("up"); sys.exit(0)
    except Exception:
        time.sleep(0.2)
print("down"); sys.exit(1)
PY
)
# 夹具自己先自证"未鉴权必 401"：否则后面的"零 POST/改判生效"全都不是在真鉴权下测出来的
AM_401=$(python3 - "$AM_PORT" <<'PY' || true
import sys, urllib.error, urllib.request
try:
    urllib.request.urlopen("http://127.0.0.1:%s/api/qmt/trades" % sys.argv[1])
    print("200")
except urllib.error.HTTPError as e:
    print(e.code)
PY
)
# 逐条跑用例（结果全部先收下，最后统一判定：任何一条红也保证夹具进程与临时目录被收掉）
AM_BASE="http://127.0.0.1:${AM_PORT}"
am_run() { # am_run <令牌文件> <参数...> → 全局 OUT / RC
	local t="$1"; shift
	if OUT=$(AMEND_LOCAL_BASE="$AM_BASE" ADMIN_TOKEN_FILE="$t" AMEND_EVIDENCE_DIR="$AM_TMP" "$AM_SH" "$@" 2>&1); then RC=0; else RC=1; fi
}
AM_REASON="夹具理由：验证通道，不落生产"
am_run "$AM_TMP/tok" --fill-id 14 --new-side 卖出 --reason "$AM_REASON";              C1=$RC; O1="$OUT"
am_run "$AM_TMP/tok" --fill-id 14 --new-side 买入 --reason "$AM_REASON" --apply --yes; C2=$RC; O2="$OUT"
# 预览 + 同向被拦两条用例跑完，夹具应当一条 POST 都没收到（"缺省只预览"只有在这里才是**量出来**的）
POSTS_AFTER2=$(cat "$AM_TMP/posts.log" 2>/dev/null | wc -l | tr -d ' ' || true); POSTS_AFTER2=${POSTS_AFTER2:-0}
am_run "$AM_TMP/tok" --fill-id 14 --new-side 卖出 --reason "$AM_REASON" --apply --yes; C3=$RC; O3="$OUT"
am_run "$AM_TMP/tok" --fill-id 14 --new-side 卖出 --reason "$AM_REASON" --apply --yes; C4=$RC; O4="$OUT"
am_run "$AM_TMP/tok" --revoke 1 --yes;                                                C5=$RC; O5="$OUT"
cp "$AM_TMP/tok" "$AM_TMP/tok_loose" && chmod 644 "$AM_TMP/tok_loose"
am_run "$AM_TMP/tok_loose" --fill-id 14 --new-side 卖出 --reason "$AM_REASON";         C6=$RC
am_run "$AM_TMP/no_such_token" --fill-id 14 --new-side 卖出 --reason "$AM_REASON" --apply --yes; C7=$RC; O7="$OUT"
am_run "$AM_TMP/no_such_token" --fill-id 14 --new-side 卖出 --reason "$AM_REASON";     C8=$RC; O8="$OUT"
POSTS_TOTAL=$(cat "$AM_TMP/posts.log" 2>/dev/null | wc -l | tr -d ' ' || true); POSTS_TOTAL=${POSTS_TOTAL:-0}
LEAK=$(grep -rlF 'fixture-amend-token-verify-only' "$AM_TMP" 2>/dev/null | grep -v -e '/tok$' -e '/tok_loose$' | wc -l | tr -d ' ' || true); LEAK=${LEAK:-0}
kill "$AM_PID" 2>/dev/null || true
wait "$AM_PID" 2>/dev/null || true
AM_ERRS=""
am_chk() { if [ "$2" != "$3" ]; then AM_ERRS="${AM_ERRS}
  · ${1}（读到 ${2}，应为 ${3}）"; fi; }
am_chk "夹具未鉴权 401 自证" "$AM_401" "401"
am_chk "夹具就绪" "$am_wait" "up"
am_chk "预览退出码" "$C1" "0"
am_chk "预览打 AMEND_PREVIEW_ONLY" "$(printf '%s' "$O1" | grep -c 'AMEND_PREVIEW_ONLY' || true)" "1"
am_chk "预览守恒显示改前差异" "$(printf '%s\n' "$O1" | grep -c '^    CONSERVE .*pos_lines=1' || true)" "1"
am_chk "同向空改判必拦" "$C2" "1"
am_chk "同向拦在本地（不发 POST）" "$(printf '%s' "$O2" | grep -c '原始方向已经是' || true)" "1"
am_chk "apply 全链退出码" "$C3" "0"
am_chk "apply 拿到 applied 回执" "$(printf '%s' "$O3" | grep -c '^    APPLIED id=1 status=applied' || true)" "1"
am_chk "apply 后守恒归零（改判真的动了读数）" "$(printf '%s' "$O3" | grep -c 'conservation_after=CONSERVE day=2026-09-25 ok=True applied_amend=1 pos_lines=0' || true)" "1"
am_chk "重复勘误必拦" "$C4" "1"
am_chk "重复拦在本地（不发 POST）" "$(printf '%s' "$O4" | grep -c '已有活跃勘误' || true)" "1"
am_chk "revoke 全链退出码" "$C5" "0"
am_chk "revoke 后守恒翻回改前" "$(printf '%s' "$O5" | grep -c 'conservation_after=CONSERVE day=2026-09-25 ok=False applied_amend=0 pos_lines=1' || true)" "1"
am_chk "组/其他可读的令牌文件必拒" "$C6" "1"
am_chk "缺令牌的写模式必非 0（绝不把没做成报成做成）" "$C7" "1"
am_chk "缺令牌的预览仍按计划打印" "$C8" "0"
am_chk "缺令牌预览打 AMEND_PLAN_ONLY" "$(printf '%s' "$O8" | grep -c 'AMEND_PLAN_ONLY' || true)" "1"
# 写动作总共只该有 3 次 POST：create + apply + revoke（预览与两类拦截都必须是 0）
am_chk "夹具收到的 POST 次数" "$POSTS_TOTAL" "3"
am_chk "预览+拦截阶段零 POST（第 2 条用例后仍为 0）" "$POSTS_AFTER2" "0"
am_chk "令牌值出现在留档里（必须 0）" "$LEAK" "0"
[ -z "$AM_ERRS" ] && { rm -rf "$AM_TMP"; echo "ok - §FILL-AMEND-CLI 守卫通过（脚本在位+语法 2 + 缺省只预览 3 + 令牌门 4（含反证）+ ASCII/转义 5（含两条反证）+ 端点同源 4 + 夹具鉴权 2 + 夹具用例断言 21）"; } \
	|| { echo "--- FAIL: §FILL-AMEND-CLI 断言不符:${AM_ERRS}"; rm -rf "$AM_TMP"; exit 1; }

echo ""
echo "==> 95 §LIB-GATE 战法库零条启用规则即判红：库读数随报告/排摸产物出门 + 缺省判红（唯一出口是显式开关）+ 用例 + 研究驱动脚本把前提写在开算之前（2026-09-25 缺陷「全量回放如果没带上线上战法库的副本，会静默按零条线上战法跑完一整轮」，owner 令「按四层改」）..."
LG_SRC=internal/btreplay/replay.go
LG_BTS=cmd/research/btstrategy.go
LG_SVY=cmd/research/survey.go
LG_TSK=cmd/research/runtask.go
LG_DRV=scripts/survey_live_rules.sh
LG_SVT=cmd/research/survey_test.go
# 运维锚点行的正锁字面量：三处（驱动脚本 grep -E / Go 用例正则 / survey 拼串）必须同源。
# 这里带 ^ 是脚本与 Go 用例共用的那一串；拼串侧另用 "survey_library gate= 单独锁（见 ③）。
LG_ERE='^survey_library gate=[a-z_]+ factor_rules=[0-9]+ pattern_rules=[0-9]+ entries=[0-9]+/[0-9]+ enabled=[0-9]+/[0-9]+ dir_from=[a-z_]+ zero_reason='
LG_ERRS=""
lg_chk() { if [ "$2" != "$3" ]; then LG_ERRS="${LG_ERRS}
  · ${1}（读到 ${2}，应为 ${3}）"; fi; }
lg_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then LG_ERRS="${LG_ERRS}
  · ${1}（读到 ${2}，应 ≥ ${3}）"; fi; }
# 子串判据一律用 case 而不是 grep：本段是"运行一次真 CLI"的实证，grep 的退出码在 set -e 下
# 只会把结论变成"中止"，而 §89 已经为这件事付过一轮学费。
lg_has() { case $LG_OUT in *"$2"*) echo 1 ;; *) echo 0 ;; esac; }

# ── ① 门的结构锁：判红/放行/正常三种门态各只有一处写入点，且"库侧读数"必须先于内置战法拼装 ──
LG_FN=$(grep -cF 'func (o *Options) libraryGate(' "$LG_SRC" || true)
LG_CALL=$(grep -cF 'o.libraryGate(&o.Library' "$LG_SRC" || true)
LG_RED=$(grep -cF 'll.Gate = "enforced"' "$LG_SRC" || true)
LG_WAIV=$(grep -cF 'll.Gate = "waived"' "$LG_SRC" || true)
LG_OKG=$(grep -cF 'll.Gate = "ok"' "$LG_SRC" || true)
LG_NA=$(grep -cF 'Gate: "not_applicable"' "$LG_SRC" || true)
LG_CAND=$(grep -cF 'Gate: "candidate_direct_exempt"' "$LG_SRC" || true)
lg_chk "库门函数唯一" "$LG_FN" "1"
lg_chk "库门调用点（all + 单侧）" "$LG_CALL" "2"
lg_chk "判红态写入点唯一" "$LG_RED" "1"
lg_chk "放行态写入点唯一" "$LG_WAIV" "1"
lg_chk "正常态写入点唯一" "$LG_OKG" "1"
lg_chk "单内置战法豁免标注唯一" "$LG_NA" "1"
lg_chk "候选直读豁免标注唯一" "$LG_CAND" "1"
# 负锁（本缺陷的结构根因）：all 模式旧代码把"一条规则都没装配"的判断写在**追加内置五形态之后**，
# 而 builtins 恒有 5 条 ⇒ 那个分支永远走不到，库空时既不报错也不留痕。它一旦被抄回来，
# 「静默按零条线上战法跑完一轮」就原地复活。
LG_DEAD=$(grep -cF 'if len(ads) == 0 {' "$LG_SRC" || true)
lg_chk "负锁：append 内置之后才判空的死分支不得复活" "$LG_DEAD" "0"

# ── ② 零条成因必须齐备：六种成因对应六种完全不同的修法，压成一个"库是空的"就派错工 ──
LG_MISS=""
for lgc in dir_unset file_missing file_unreadable file_blank no_entries all_disabled no_usable_rule unknown; do
	lgcn=$(grep -cF "\"$lgc\"" "$LG_SRC" || true)
	[ "${lgcn:-0}" -ge 1 ] || LG_MISS="${LG_MISS} ${lgc}"
done
[ -z "$LG_MISS" ] || LG_ERRS="${LG_ERRS}
  · 零条成因缺失:${LG_MISS}"

# ── ③ 出门事实：报告首行 / 日志 / 排摸锚点三条腿，且锚点拼串只准一处（第二处必然漂移） ──
LG_PUB=$(grep -cF 'fmt.Printf("%s\n", o.Library.String())' "$LG_SRC" || true)
LG_LOG=$(grep -cF 'log.Printf("§LIB-GATE %s"' "$LG_SRC" || true)
LG_ADEF=$(grep -cF 'func libraryAnchorLine(' "$LG_SVY" || true)
LG_AUSE=$(grep -cF 'libraryAnchorLine(art.Library)' "$LG_SVY" || true)
LG_AFMT=$(grep -cF '"survey_library gate=' "$LG_SVY" || true)
lg_chk "回放报告首行打库读数" "$LG_PUB" "1"
lg_chk "日志行打库读数" "$LG_LOG" "1"
lg_chk "锚点行拼串函数唯一" "$LG_ADEF" "1"
lg_chk "锚点行两处出口（表头 + 收尾）" "$LG_AUSE" "2"
lg_chk "锚点字面量只在拼串函数里" "$LG_AFMT" "1"
# 跨语言同源：驱动脚本与 Go 用例里那串正锁必须逐字符相同（任一侧改格式，另一侧立刻红）。
LG_ERE_DRV=$(grep -cF "$LG_ERE" "$LG_DRV" || true)
LG_ERE_TST=$(grep -cF "$LG_ERE" "$LG_SVT" || true)
lg_chk "运维正锁与驱动脚本同源" "$LG_ERE_DRV" "1"
lg_chk "运维正锁与 Go 用例同源" "$LG_ERE_TST" "1"

# ── ④ 放行开关的三个出口 + 负锁：仓库内不许有第二处代传 ──
LG_SW1=$(grep -cF 'fs.Bool("allow-empty-library"' "$LG_BTS" || true)
LG_SW2=$(grep -cF 'fs.Bool("allow-empty-library"' "$LG_SVY" || true)
LG_W1=$(grep -cF 'AllowEmptyLibrary: *allowEmpty' "$LG_BTS" || true)
LG_W2=$(grep -cF 'AllowEmptyLibrary: *allowEmpty' "$LG_SVY" || true)
LG_W3=$(grep -cF 'AllowEmptyLibrary: payloadBool(p, "allow_empty_library")' "$LG_TSK" || true)
lg_chk "backtest-strategy 有放行开关" "$LG_SW1" "1"
lg_chk "strategy-survey 有放行开关" "$LG_SW2" "1"
lg_chk "backtest-strategy 已接线" "$LG_W1" "1"
lg_chk "strategy-survey 已接线" "$LG_W2" "1"
lg_chk "run-task 从 payload 读取" "$LG_W3" "1"
# 负锁：夜间任务链里不许预置放行（缺省必须走判红）。带引号的键出现一次＝只有 runtask 的读取端；
# 多出来一处就是某个调度侧开始替 owner 表态了。
LG_PRESET=$(grep -rn '"allow_empty_library"' --include='*.go' --include='*.json' internal cmd scripts 2>/dev/null | grep -v '_test.go' | grep -v 'runtask.go' | wc -l | tr -d ' ' || true)
lg_chk "负锁：调度/装配侧不得预置放行键" "${LG_PRESET:-0}" "0"

# ── ⑤ 运行时实证：真二进制 × 六种库形态（门只读代码不算数，读到什么才算数） ──
LG_TMP=$(mktemp -d /tmp/libgate-XXXXXX)
LG_DB="$LG_TMP/empty.db"
: > "$LG_DB"
mkdir -p "$LG_TMP/nolib" "$LG_TMP/alldis" "$LG_TMP/withlib"
# 夹具条目形状 = AppliedFactorEntry 的 JSON 契约（weights/directions 是 map 不是数组；
# adj_basis 必须等于当前口径，否则条目被 §ADJ-BASIS-2 判 stale，成因就成了另一种）。
lg_entry() {
	printf '%s\n' '[{"id":"fac_lg","name":"gate-fixture","enabled":'"$1"',"candidate_id":998,"factors":["vol_20d"],"weights":{"vol_20d":1.0},"directions":{"vol_20d":1},"buy_threshold":60,"horizon":5,"ir":1.2,"excess":0.05,"adj_basis":"hfq-forward-fill-1"}]'
}
lg_entry false > "$LG_TMP/alldis/applied_factors.json"
lg_entry true > "$LG_TMP/withlib/applied_factors.json"
# 形态侧留一份"文件在、内容是空数组"的副本：现网最常见就是这种（从未审批过形态条目），
# 它和"文件根本没有"是两种成因（no_entries vs file_missing），⑥ 用例正是拿它验证侧名与成因。
printf '[]\n' > "$LG_TMP/withlib/applied_patterns.json"
LG_BIN="$LG_TMP/research"
if ! go build -o "$LG_BIN" ./cmd/research; then
	echo "--- FAIL: §LIB-GATE 运行时实证的前置构建失败（研究二进制建不出来，下面的六态用例全部没跑）"
	rm -rf "$LG_TMP"
	exit 1
fi
lg_run() {
	LG_C=0
	if LG_OUT=$("$LG_BIN" --db "$LG_DB" backtest-strategy "$@" 2>&1); then
		LG_C=0
	else
		LG_C=$?
	fi
}
lg_run -strategy all -datadir "$LG_TMP/nolib" -start 20260901 -end 20260910 -maxstocks 1
lg_chk "①空库判红退出码" "$LG_C" "1"
lg_chk "①空库成因带两侧侧名" "$(lg_has "$LG_OUT" '回放判红（factor:file_missing+pattern:file_missing）')" "1"
lg_chk "①空库读数回显目录来源" "$(lg_has "$LG_OUT" '来源 explicit')" "1"
lg_run -strategy all -datadir "$LG_TMP/nolib" -allow-empty-library -start 20260901 -end 20260910 -maxstocks 1
lg_chk "②显式放行后照跑" "$LG_C" "0"
lg_chk "②放行后门态=waived" "$(lg_has "$LG_OUT" '门=waived')" "1"
# 反证（放行不等于抹掉事实）：waived 那一行仍必须带着"其实一条都没加载到"的成因读数。
lg_chk "②放行后成因仍随读数出门" "$(lg_has "$LG_OUT" '成因=factor:file_missing+pattern:file_missing')" "1"
lg_run -strategy all -datadir "$LG_TMP/alldis" -start 20260901 -end 20260910 -maxstocks 1
lg_chk "③全停用判红" "$LG_C" "1"
lg_chk "③成因=all_disabled" "$(lg_has "$LG_OUT" 'factor:all_disabled')" "1"
lg_run -strategy all -datadir "$LG_TMP/withlib" -start 20260901 -end 20260910 -maxstocks 1
lg_chk "④有启用规则即绿" "$LG_C" "0"
lg_chk "④门态=ok" "$(lg_has "$LG_OUT" '门=ok')" "1"
lg_chk "④三段读数出门" "$(lg_has "$LG_OUT" '文件条目 1/0')" "1"
lg_run -strategy momentum -datadir "$LG_TMP/nolib" -start 20260901 -end 20260910 -maxstocks 1
lg_chk "⑤单内置战法不受库门约束" "$LG_C" "0"
lg_chk "⑤标注=not_applicable" "$(lg_has "$LG_OUT" '门=not_applicable')" "1"
lg_run -strategy pattern -datadir "$LG_TMP/withlib" -start 20260901 -end 20260910 -maxstocks 1
lg_chk "⑥单侧判据判红" "$LG_C" "1"
lg_chk "⑥只报本侧成因" "$(lg_has "$LG_OUT" '回放判红（pattern:no_entries）')" "1"
# 反证：本轮没要求因子侧，对侧文件明明有货也不许被拖进成因（把两侧压成一团＝读的人分不清修哪一侧）。
lg_chk "⑥负锁：单侧判据不得带上对侧" "$(lg_has "$LG_OUT" 'factor:')" "0"
rm -rf "$LG_TMP"

# ── ⑥ 用例面下限：门测试 + 锚点契约测试必须真跑到（`ok` 对"没测试可跑"同样成立，故数 RUN 行） ──
LG_GT=$(go test -count=1 ./internal/btreplay/ -run 'TestLibraryGate|TestLibraryReadings|TestDirFromClassification' -v 2>&1 | grep -c '^=== RUN' || true)
LG_GF=$(go test -count=1 ./internal/btreplay/ -run 'TestLibraryGate|TestLibraryReadings|TestDirFromClassification' 2>&1 | grep -cE 'FAIL|no test files' || true)
LG_ST=$(go test -count=1 ./cmd/research/ -run 'TestStrategySurveyArtifact|TestLibraryAnchorLineContract' -v 2>&1 | grep -c '^=== RUN' || true)
LG_SF=$(go test -count=1 ./cmd/research/ -run 'TestStrategySurveyArtifact|TestLibraryAnchorLineContract' 2>&1 | grep -cE 'FAIL|no test files' || true)
lg_min "btreplay 库门用例数下限" "$LG_GT" "13"
lg_chk "btreplay 库门用例跑绿" "$LG_GF" "0"
lg_min "survey 出门/锚点用例下限" "$LG_ST" "2"
lg_chk "survey 出门/锚点用例跑绿" "$LG_SF" "0"

# ── ⑦ 驱动脚本（第四层）：前提在开算前落纸面，零可用条目直接失败，绝不"跑几十分钟再发现库是空的" ──
bash -n "$LG_DRV" || { echo "--- FAIL: §LIB-GATE $LG_DRV 语法不过（bash -n）"; exit 1; }
LG_D_SCAN=$(grep -cF 'lib_scan() {' "$LG_DRV" || true)
LG_D_PRINT=$(grep -cF 'echo "   LIB_PREMISE $f.json ${SCAN}"' "$LG_DRV" || true)
LG_D_PARSE=$(grep -cF '副本无法解析' "$LG_DRV" || true)
LG_D_ZERO=$(grep -cF '没有任何可用战法条目' "$LG_DRV" || true)
LG_D_APPLIED=$(grep -cF -- '--applied "$RULES_DIR"' "$LG_DRV" || true)
LG_D_ANCHOR=$(grep -cF 'survey_library gate=' "$LG_DRV" || true)
lg_chk "前提扫描函数唯一" "$LG_D_SCAN" "1"
lg_chk "LIB_PREMISE 逐侧打印" "$LG_D_PRINT" "1"
lg_chk "解析失败不折成空库" "$LG_D_PARSE" "1"
lg_chk "零可用条目判失败" "$LG_D_ZERO" "1"
lg_chk "排摸必须显式带 --applied" "$LG_D_APPLIED" "1"
lg_min "驱动脚本核锚点行" "$LG_D_ANCHOR" "1"
# 负锁：驱动脚本自己永远不许代传放行（真要跑"空库对照口径"，得由人在命令行上显式表态）。
LG_D_WAIVE=$(grep -nF -- '--allow-empty-library' "$LG_DRV" | grep -v '^[0-9]*:[[:space:]]*#' | grep -v 'echo ' | wc -l | tr -d ' ' || true)
lg_chk "负锁：驱动脚本不得代传 --allow-empty-library" "${LG_D_WAIVE:-0}" "0"

if [ -z "$LG_ERRS" ]; then
	rm -rf "$LG_TMP"
	echo "ok - §LIB-GATE 守卫通过（门结构 7 + 死分支负锁 1 + 成因齐备 1 + 出门三腿 6 + 跨语言同源 2 + 放行出口 5 + 调度侧预置负锁 1 + 运行时六态实证 17 + 用例下限 4 + 驱动脚本 7）"
else
	echo "--- FAIL: §LIB-GATE 断言不符:${LG_ERRS}"
	rm -rf "$LG_TMP"
	exit 1
fi

echo ""
# ════════════════════════════════════════════════════════════════════════════
# §96 §CAL-GATE（2026-09-25 缺陷 D-25-1，owner 令「不走法定休市日表这个bug修了」）
# 锁定四件事：
# ① 结构锁：internal/data/trade_time.go 全部 14 个时段判据的函数体必须调 IsTradingDay，
#    且体内不得再出现"星期几"字面量（权威闸口只留 IsTradingDay/TradingDayDate/
#    AddTradingDays/DurationToNextActiveSession 四处合法位置，逐字计数钉死）；
# ② 可见性锁：fail-open 是刻意方向 ⇒ 健康读数 TradingCalendarHealth + 两个量规 +
#    trading_calendar_not_loaded 规则/路由必须都在（§DEADGAUGE 同族的"规则有、赋值没"防线）；
# ③ 同闸门锁：newsagent 窗口裁剪与 dataload 半根K线回退都改走 data.IsTradingDay，
#    全仓不许留第二套"只看周末"的交易日写法；
# ④ 真跑锁：中秋（2026-09-25 恰为周五）形态的注入日历用例必须真跑到且跑绿（数 === RUN 行，
#    §95 教训：ok 对"没测试可跑"同样成立）。
# 反证（本轮人工锤，不常驻脚本）：把 CurrentSession 改回周末写法后整段+用例即红。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 96 §CAL-GATE 法定休市日不再被当盘中：14 个时段判据统一走 IsTradingDay + fail-open 可见性（量规/健康读数/告警规则）+ newsagent/dataload 同闸门 + 中秋注入日历用例真跑（2026-09-25 D-25-1，owner 令「这个bug修了」）..."
CG_TT=internal/data/trade_time.go
CG_CAL=internal/data/trade_calendar.go
CG_ENG=internal/engine/scoring_loop.go
CG_NA=internal/newsagent/tracker.go
CG_DL=cmd/dataload/baostock.go
CG_TST=internal/data/trade_time_calendar_test.go
CG_ERRS=""
cg_chk() { if [ "$2" != "$3" ]; then CG_ERRS="${CG_ERRS}
  · ${1}（读到 ${2}，应为 ${3}）"; fi; }
cg_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then CG_ERRS="${CG_ERRS}
  · ${1}（读到 ${2}，应 ≥ ${3}）"; fi; }

# ── ① 结构锁 ──
# 14 个受闸判据（与本批改写的函数清单一一对应；新增时段判据必须进这张清单，否则 ①b 循环不覆盖它）。
CG_FNS="IsTradeTime IsFullTradingHours IsPreOpen IsMorningHighFreq IsMidFreqWindow IsAfternoonHighFreq IsPreMarket IsPreAfternoon IsAfterMarket IsTradingWindow CurrentSession BeforeOpenTrade IsContinuousTrade NextTradeOpen"
# ①a 函数体级正锁+体内负锁：逐个抽体检查"有 IsTradingDay、无 Saturday"（比全文件计数强：
#     把闸口塞进别的函数也骗不过逐体抽取）。抽取用 perl -0777（跨行；§静态负锁教训：macOS BSD
#     grep 无 -P，多行匹配只有 perl 一条路）。
#     ⚠ 正则必须自带捕获括号 `(…)`：\Q$n\E 与 \( 都不是捕获组，$1 会静默为空串＝恒假象
#     （本轮搭锁时实测踩过一次；非空判定由 len 检查兜底）。
for cgf in $CG_FNS; do
	if ! perl -0777 -E '
		my $s = do { local $/; <STDIN> };
		my $n = $ARGV[0];
		if ($s =~ /(func \Q$n\E\(.*?\n\})/s) {
			my $b = $1;
			exit((length($b) > 20 && $b =~ /IsTradingDay\(/ && $b !~ /Saturday/) ? 0 : 1);
		}
		exit 2;
	' "$cgf" < "$CG_TT"; then
		CG_ERRS="${CG_ERRS}
  · 判据 ${cgf} 函数体未过闸（应含 IsTradingDay 且体内无 Saturday）"
	fi
done
CG_CALLS=$(grep -c '!IsTradingDay(' "$CG_TT" || true)
cg_chk "受闸判据调用点总数（14 个判据各一处）" "$CG_CALLS" "14"
# ①b 权威闸口自身唯一且不得自我递归。
CG_DEF=$(grep -c 'func IsTradingDay(' "$CG_TT" || true)
cg_chk "IsTradingDay 定义唯一" "$CG_DEF" "1"
CG_SELF=$(perl -0777 -ne 'print /func IsTradingDay\(.*?\n\}/s ? ($& =~ /!IsTradingDay\(/ ? 1 : 0) : 0' "$CG_TT" || true)
cg_chk "负锁：IsTradingDay 体内不得再调自身" "${CG_SELF:-0}" "0"
# ①c 等值锁：文件里合法保留的"星期几"字面量必须恰是 3 处 == + 1 处 !=
#     （TradingDayDate 回退、DurationToNextActiveSession 跳过、IsTradingDay 本体、AddTradingDays 正向）。
#     多了＝有新判据绕闸手写周末；少了＝有人把日历腿本身也拆了。
CG_SAT_EQ=$(grep -c 'Weekday() == time.Saturday' "$CG_TT" || true)
CG_SAT_NE=$(grep -c 'Weekday() != time.Saturday' "$CG_TT" || true)
cg_chk "合法周末字面量（==）恰 3 处（闸口本体+两处日历腿）" "$CG_SAT_EQ" "3"
cg_chk "合法周末字面量（!=）恰 1 处（AddTradingDays）" "$CG_SAT_NE" "1"
CG_WD=$(grep -c 'wd := now.Weekday()' "$CG_TT" || true)
cg_chk "负锁：旧写法 wd := now.Weekday() 早退不得复活" "$CG_WD" "0"
CG_14=$(grep -c 'for i := 0; i < 14; i++' "$CG_TT" || true)
CG_7=$(grep -c 'for i := 0; i < 7; i++' "$CG_TT" || true)
cg_chk "NextTradeOpen 长假 14 天窗口正锁" "$CG_14" "1"
cg_chk "负锁：7 天旧窗口不得复活（长假退化返回 0）" "$CG_7" "0"

# ── ② 可见性锁（fail-open 必须被看见） ──
CG_HDEF=$(grep -c 'func TradingCalendarHealth(' "$CG_CAL" || true)
CG_HUSE=$(grep -c 'data.TradingCalendarHealth()' "$CG_ENG" || true)
cg_chk "健康读数函数唯一" "$CG_HDEF" "1"
cg_chk "健康读数有真实消费者（§DEADGAUGE 教训：诊断函数不许零消费挂死）" "$CG_HUSE" "1"
CG_WDEF=$(grep -c 'func setCalendarWindow(' "$CG_CAL" || true)
CG_WUSE=$(grep -c 'setCalendarWindow(minD, maxD)' "$CG_CAL" || true)
cg_chk "覆盖窗口写入点唯一（仅 API 成功刷新）" "$CG_WUSE" "1"
cg_chk "覆盖窗口函数唯一" "$CG_WDEF" "1"
# SetClosedDays 覆盖即旧窗口作废：清窗腿必须在注入函数里（否则缓存/测试注入会顶着上轮 API 的窗口读数）。
CG_WCLR=$(perl -0777 -ne 'print /func SetClosedDays\(.*?\n\}/s ? ($& =~ /calWindow = ""/ ? 1 : 0) : 0' "$CG_CAL" || true)
cg_chk "SetClosedDays 内必须清空旧窗口" "${CG_WCLR:-0}" "1"
CG_G1=$(grep -c 'SetGauge("trading_calendar_loaded"' "$CG_ENG" || true)
CG_G2=$(grep -c 'SetGauge("today_is_trading_day"' "$CG_ENG" || true)
CG_BG=$(grep -c 'func boolGauge(' "$CG_ENG" || true)
cg_chk "量规 trading_calendar_loaded 赋值点唯一" "$CG_G1" "1"
cg_chk "量规 today_is_trading_day 赋值点唯一" "$CG_G2" "1"
cg_chk "boolGauge 辅助唯一" "$CG_BG" "1"
# 规则↔路由↔赋值三腿齐在（赋值腿由 §DEADGAUGE 通用守卫反解 Metric 名兜底，这里锁规则与路由本身）。
CG_RULE=$(grep -c 'Name: "trading_calendar_not_loaded"' internal/metrics/alerter.go || true)
CG_ROUTE=$(grep -c '"trading_calendar_not_loaded": RouteDaily' internal/metrics/alert_routing.go || true)
cg_chk "未加载告警规则唯一" "$CG_RULE" "1"
cg_chk "告警规则已进路由表（RouteDaily）" "$CG_ROUTE" "1"

# ── ③ 同闸门锁：全仓不许留第二套"只看周末"的交易日写法 ──
CG_NA1=$(grep -c '!data.IsTradingDay(start)' "$CG_NA" || true)
CG_NA2=$(grep -c 'time.Saturday' "$CG_NA" || true)
cg_chk "newsagent 窗口裁剪走统一闸口" "$CG_NA1" "1"
cg_chk "负锁：tracker.go 不得残留周末字面量" "$CG_NA2" "0"
CG_DL1=$(grep -c 'for !data.IsTradingDay(yest)' "$CG_DL" || true)
CG_DL2=$(grep -c 'time.Saturday' "$CG_DL" || true)
cg_chk "dataload 半根K线回退走统一闸口" "$CG_DL1" "1"
cg_chk "负锁：baostock.go 不得残留周末字面量" "$CG_DL2" "0"
# 生产侧（QMT 桥 python）同口径的独立锁在 qmt_gateway/tests/test_trading_calendar.py（pytest 231 覆盖），
# 此处不做跨语言字面量锁——两侧语义相同但实现独立，锁字面量只会互相绊脚（§95 那次锁的是共享锚点行格式）。

# ── ④ 真跑锁：注入日历的中秋形态用例（2026-09-25 恰为周五＝本缺陷的实录形状） ──
CG_RUN=$(go test -count=1 ./internal/data/ -run 'TestHolidaySessionPredicates|TestHolidayLongBreakNextTradeOpen|TestNormalTradingDayPredicates|TestCalendarFailOpenDirection|TestTradingCalendarHealth' -v 2>&1 | grep -c '^=== RUN' || true)
CG_FAIL=$(go test -count=1 ./internal/data/ -run 'TestHolidaySessionPredicates|TestHolidayLongBreakNextTradeOpen|TestNormalTradingDayPredicates|TestCalendarFailOpenDirection|TestTradingCalendarHealth' 2>&1 | grep -cE 'FAIL|no test files' || true)
cg_min "§CAL-GATE 用例实跑数下限（5 条函数各≥1）" "$CG_RUN" "5"
cg_chk "§CAL-GATE 用例跑绿" "$CG_FAIL" "0"
CG_NR=$(go test -count=1 ./internal/newsagent/ -run 'TestTradingDayStart' -v 2>&1 | grep -c '^=== RUN' || true)
CG_NF=$(go test -count=1 ./internal/newsagent/ -run 'TestTradingDayStart' 2>&1 | grep -cE 'FAIL|no test files' || true)
cg_min "newsagent 窗口裁剪用例实跑" "$CG_NR" "1"
cg_chk "newsagent 窗口裁剪用例跑绿" "$CG_NF" "0"
CG_TST_EXIST=$(test -f "$CG_TST" && echo 1 || echo 0)
cg_chk "§CAL-GATE 用例文件在位" "$CG_TST_EXIST" "1"

if [ -z "$CG_ERRS" ]; then
	echo "ok - §CAL-GATE 守卫通过（逐体正/负锁 14×2 + 结构计数 8 + 可见性 10 + 同闸门 4 + 用例实跑 5）"
else
	echo "--- FAIL: §CAL-GATE 断言不符:${CG_ERRS}"
	exit 1
fi


# ════════════════════════════════════════════════════════════════════════════
# §97 §CAND-PUSH（2026-09-25，owner 令「重训结果推到云端」）
# 锁定 scripts/push_candidates_guangzhou.sh 的六条安全口径——这条通道**会写现网研究库**，
# 所以每一句"只写 proposed / 先备份 / 不覆盖既有行"都必须有锁，而不是只写在头注释里：
# ① 写入口径锁：远端 INSERT 第三列必须是字面量 'proposed'（状态由远端写死，本机载荷说了不算）；
#    全脚本对 research_candidates 不得出现 UPDATE/DELETE；INSERT 列首必须是 created_at
#    （显式 id 进列名＝撞号事故的开端，钉死为 0 次）。
# ② 备份先行锁：备份必须走 sqlite3 backup API（backup( 调用在位），ps1 里不得出现 Copy-Item
#    裸拷库文件（WAL 半态备份＝没备）；backup 失败必须能打 ABORT=backup-failed。
# ③ 转义链锁：ps1 组装后有 LC_ALL=C 非 ASCII 自检；解析远端回传前必须 tr -d '\r'
#    （09-25 首跑实录：**自己加的 CRLF 被自己的 ASCII 闸判红**——两条锁都在防这一族）。
# ④ 缺省方向锁：预览模式判定行 CAND_PUSH_PLAN 在位；运行时用离网 SRC_DB 真跑一次预览，
#    要求 exit 0 且输出**不含** CAND_PUSH_ARMED（ARMED 只在 -Apply 后出现＝"没连生产"可证）。
# ⑤ 载荷校验锁（含常驻反证）：临时库里混一行 approved ⇒ 预览必须非 0（拒推在任何写入之前）；
#    空载荷/缺库同样非 0——"0 行也算成功"是 §MINUTE 同款假绿，禁止。
# ⑥ 写后复核锁：VERIFY 行必须从库里回读 status（不是复述刚写的变量）；等式 total/inserted/dup
#    的解析与数字校验同在。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 97 §CAND-PUSH 候选推送云端通道：只写 proposed / 备份先行 / 不碰既有行 / 缺省预览零连接 / 写后逐行回读（2026-09-25 owner 令「重训结果推到云端」）..."
CP_SCR=scripts/push_candidates_guangzhou.sh
CP_ERRS=""
cp_chk() { if [ "$2" != "$3" ]; then CP_ERRS="${CP_ERRS}
  · ${1}（读到 ${2}，应为 ${3}）"; fi; }
cp_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then CP_ERRS="${CP_ERRS}
  · ${1}（读到 ${2}，应 ≥ ${3}）"; fi; }
cp_absent() { if [ "${2:-0}" -ne "0" ]; then CP_ERRS="${CP_ERRS}
  · ${1}（应彻底没有，实得 ${2} 处）"; fi; }


cp_chk "§CAND-PUSH 脚本在位" "$(test -f "$CP_SCR" && echo 1 || echo 0)" "1"
# 本脚本开头是 `set -euo pipefail`（§89 钉死）：凡是**预期可能非 0** 的子进程（反证要它失败）
# 必须先 `|| rc=$?` 收进变量再断言，直跑会让整轮 verify 无 FAIL 无 ok 静默中止（本节首跑实录）。
CP_SY=0; bash -n "$CP_SCR" 2>/dev/null || CP_SY=$?
cp_chk "push_candidates 语法自检 bash -n" "$CP_SY" "0"

# ── ① 写入口径 ──
CP_PROP="$(grep -c "r\['kind'\], 'proposed', r\['factors'\]" "$CP_SCR" || true)"
cp_chk "远端 INSERT 状态写死 'proposed'（唯一落点）" "$CP_PROP" "1"
CP_UPD="$(grep -c "UPDATE research_candidates" "$CP_SCR" || true)"
cp_absent "对候选表的 UPDATE（本通道只增不改）" "$CP_UPD"
CP_DEL="$(grep -c "DELETE FROM research_candidates" "$CP_SCR" || true)"
cp_absent "对候选表的 DELETE（本通道只增不删）" "$CP_DEL"
CP_IDI="$(grep -c "INSERT INTO research_candidates (id" "$CP_SCR" || true)"
cp_absent "INSERT 显式带 id（防撞号口径）" "$CP_IDI"
CP_INS="$(grep -c "INSERT INTO research_candidates (created_at,kind,status," "$CP_SCR" || true)"
cp_chk "INSERT 列集与 store 侧 13 列同构（列首 created_at）" "$CP_INS" "1"

# ── ② 备份先行 ──
CP_BAK="$(grep -c "src.backup(tgt" "$CP_SCR" || true)"
cp_min "sqlite3 backup API 调用在位（一致性快照）" "$CP_BAK" "2"
# 负锁只查 **ps1 体内**有没有裸拷库文件：整文件计数会命中"为什么不用 Copy-Item"这条
# 说明注释本身（§静态负锁教训：旧写法禁用注释≠旧写法复活）。
CP_PS1BODY="$(awk '/push_run.ps1" <<.PYEOF\./{f=1;next} f&&/^PYEOF$/{exit} f' "$CP_SCR")"
CP_CI="$(printf '%s' "$CP_PS1BODY" | grep -c "Copy-Item" || true)"
cp_absent "ps1 体内裸拷库文件（Copy-Item＝WAL 半态备份）" "$CP_CI"
CP_AB="$(grep -c "ABORT=backup-failed" "$CP_SCR" || true)"
cp_min "备份失败的中止判定行在位" "$CP_AB" "1"
CP_DST="$(grep -c "BACKUP_ERR=dst-exists" "$CP_SCR" || true)"
cp_min "非 0 字节的同名历史备份拒绝覆盖（dst-exists）" "$CP_DST" "1"

# ── ③ 转义链 ──
CP_ASCII="$(grep -c "LC_ALL=C grep -n '\[^ -~\]'" "$CP_SCR" || true)"
cp_min "ps1 组装后的非 ASCII 自检" "$CP_ASCII" "1"
CP_CR="$(grep -c "tr -d '\\\\r'" "$CP_SCR" || true)"
cp_min "解析远端回传前去 CR" "$CP_CR" "1"

# ── ④⑤ 运行时：离网预览 + 载荷反证（全部只碰 mktemp 的一次性小库，不连任何生产；
#     段尾统一 rm，不留跨轮垃圾——本仓纪律：测试数据不落工作树、也不留 /tmp 常驻）──
CP_TMP="$(mktemp -d /tmp/verify97_XXXXXX)"
CP_DB="$CP_TMP/src.db"
sqlite3 "$CP_DB" "CREATE TABLE research_candidates (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'proposed', factors TEXT, weights TEXT, metric REAL, ic_mean REAL, ir REAL, avg_excess REAL, horizon INTEGER, reason TEXT, guard TEXT DEFAULT 'standard', params TEXT DEFAULT '', fidelity TEXT DEFAULT '');"
sqlite3 "$CP_DB" "INSERT INTO research_candidates (created_at,kind,status,factors,params,horizon,guard,ir) VALUES ('2026-09-25 10:00:00','factor','proposed','[\"T1\"]','{}',5,'strong',0.9),('2026-09-25 11:00:00','factor','proposed','[\"T2\"]','{}',10,'weak',0.5);"
CP_OUT="$CP_TMP/preview.out"
CP_PV=0
GZ_IP=203.0.113.7 SRC_DB="$CP_DB" bash "$CP_SCR" > "$CP_OUT" 2>&1 || CP_PV=$?
cp_chk "预览模式退出码为 0（离网、不连生产）" "$CP_PV" "0"
CP_PLAN="$(grep -c 'CAND_PUSH_PLAN' "$CP_OUT" || true)"
cp_min "预览判定行 CAND_PUSH_PLAN" "$CP_PLAN" "1"
CP_ARMED="$(grep -c 'CAND_PUSH_ARMED' "$CP_OUT" || true)"
cp_absent "预览输出里出现 CAND_PUSH_ARMED（ARMED 只准在 -Apply 后）" "$CP_ARMED"
CP_ROWS="$(grep -c '本机id=' "$CP_OUT" || true)"
cp_chk "预览清单行数==临时库行数（2）" "$CP_ROWS" "2"
# 反证 A（常驻）：**显式指定 CAND_IDS** 混进一行 approved ⇒ 必须被本机腿在任何连接之前拒掉
# （缺省选行只取 proposed，approved 会被静默不选——那是选行口径，不是校验腿；
#  校验腿钉的是"点名要推的行里混了非 proposed 就整轮拒绝"这条 fail-close）。
sqlite3 "$CP_DB" "INSERT INTO research_candidates (created_at,kind,status,factors,params,horizon,guard,ir) VALUES ('2026-09-25 12:00:00','factor','approved','[\"T3\"]','{}',5,'strong',0.9);"
CP_RC=0
GZ_IP=203.0.113.7 SRC_DB="$CP_DB" CAND_IDS=1,2,3 bash "$CP_SCR" > "$CP_TMP/rej.out" 2>&1 || CP_RC=$?
cp_chk "点名行混入 approved 后判红（退出码非 0）" "$([ "$CP_RC" -ne 0 ] && echo 1 || echo 0)" "1"
CP_REJ="$(grep -c '不是 proposed' "$CP_TMP/rej.out" || true)"
cp_min "拒推原因行（状态校验在导表腿）" "$CP_REJ" "1"
# 反证 B：来源库不存在 ⇒ 显式失败（绝不"没库＝0 行＝成功"）
CP_RC=0
GZ_IP=203.0.113.7 SRC_DB="$CP_TMP/nope.db" bash "$CP_SCR" > "$CP_TMP/nodb.out" 2>&1 || CP_RC=$?
cp_chk "缺来源库判红（退出码非 0）" "$([ "$CP_RC" -ne 0 ] && echo 1 || echo 0)" "1"
# 反证 C：空 proposed 集合 ⇒ 空载荷判失败（§MINUTE「0 行不判成功」同族）
CP_EMPTY="$CP_TMP/empty.db"
# §0926E2E-12A 同日同步：导出腿列集合已含 fidelity（保真水印随候选走），
# 空载荷夹具建表口径必须与 CP_DB 主夹具逐列一致——少一列会让导出 SELECT 先于
# "载荷为空"判语崩在 no such column 上，本锁读到的原因行归 0（2026-09-27 全量跑锤出）。
sqlite3 "$CP_EMPTY" "CREATE TABLE research_candidates (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'proposed', factors TEXT, weights TEXT, metric REAL, ic_mean REAL, ir REAL, avg_excess REAL, horizon INTEGER, reason TEXT, guard TEXT DEFAULT 'standard', params TEXT DEFAULT '', fidelity TEXT DEFAULT '');"
CP_RC=0
GZ_IP=203.0.113.7 SRC_DB="$CP_EMPTY" bash "$CP_SCR" > "$CP_TMP/empty.out" 2>&1 || CP_RC=$?
cp_chk "空候选库判红（退出码非 0）" "$([ "$CP_RC" -ne 0 ] && echo 1 || echo 0)" "1"
CP_E="$(grep -c '载荷为空' "$CP_TMP/empty.out" || true)"
cp_min "空载荷的显式原因行" "$CP_E" "1"

# ── ⑥ 写后复核 ──
CP_VERQ="$(grep -c "SELECT status FROM research_candidates WHERE id=?" "$CP_SCR" || true)"
cp_min "VERIFY 从库里回读 status（不复述刚写的变量）" "$CP_VERQ" "1"
CP_EQ="$(grep -c 'inserted+dup==total' "$CP_SCR" || true)"
cp_min "等式复核文案在位（脚本按等式判红）" "$CP_EQ" "1"
CP_NUM="$(grep -c '\*\[!0-9\]\*' "$CP_SCR" || true)"
cp_min "远端数字字段先验数字再进算术（非数字一律判红）" "$CP_NUM" "1"

rm -rf "$CP_TMP" 2>/dev/null || true

if [ -z "$CP_ERRS" ]; then
	echo "ok - §CAND-PUSH 守卫通过（写入口径 5 + 备份先行 4 + 转义链 2 + 预览运行时 4 + 常驻反证 3 组 + 写后复核 3）"
else
	echo "--- FAIL: §CAND-PUSH 断言不符:${CP_ERRS}"
	exit 1
fi

# ════════════════════════════════════════════════════════════════════════════
# §98 §0925EVE-W1（2026-09-25 晚批，owner 令「根据修改文档，开始修代码」——第一波四条）
# 全量评价批（docs/FIX_PLAN_20260925EVE.md）第一波的落地锁，一物一锁防复活：
# ① §A2 kill-switch 撤单失败明细：HaltAll 必须返回 HaltAllResult（成功数+失败数组，
#    Failed 恒非 nil），量规 halt_cancel_fail_count 每轮覆写（含清零供 resolved），
#    p1 规则+路由+HTTP/SSE 两腿+前端「笔未撤成」回显全在位——旧 `HaltAll() int`
#    裸计数签名（「降级报成功」在安全闸上的最后残留）钉死为 0 次。
# ② §B1 财务续传键：季度→期末日必须是显式映射表（0331/0630/0930/1231），旧三元
#    嵌套（季度序号错放月份位 ⇒ Q3 键 YYYY0331 撞 Q1 真期被永久跳过）负锁为 0；
#    装载器与夜间验证都必须按报告期打印 distinct 分布（COUNT(*) 单值看不见整季塌方）；
#    运行时断言四键真值（import 载具跑 report_period，__main__ 有守卫不会误触装载）。
# ③ §C1 实盘战法库闸（§95/LIB-GATE 回放判红的实盘对偶）：唯一判定点
#    gateLiveStrategyLibrary 定义+调用在位，not_loaded/no_enabled 两条 p1 规则与
#    路由四条齐、两份文案严格分家（读库故障 vs 库里没规则），三态+反证四用例。
# ④ §D1 配置热重载补锁：Rules/D1 已转私有，全局读口径唯一（Get/GetD1Config/
#    RulesD1Snapshot），Load 发布段 rules+d1 各一次指针替换；负锁（滤注释行，
#    §静态负锁教训：旧写法说明注释≠旧写法复活）钉住 `m.Rules`/`CfgMgr.Rules.`
#    活代码零残留；-race 反证用例 TestLoadWatchPublishRace 在位。
# 本段只跑静态判据 + 一个轻量 import 断言；Go/前端的真实回归由 -full 附加段与
# 各包 test 文件承担（halt_a2 / registry_library_gate / load_watch_race 三份新用例）。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 98 §0925EVE-W1 第一波四修：A2 kill-switch 失败明细 / B1 财务续传键改映射表+期分布 / C1 实盘战法库三态闸 / D1 配置热重载补锁（2026-09-25 owner 令「根据修改文档开始修代码」）..."
EV_ERRS=""
ev_chk() { if [ "$2" != "$3" ]; then EV_ERRS="${EV_ERRS}
  · ${1}（读到 ${2}，应为 ${3}）"; fi; }
ev_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then EV_ERRS="${EV_ERRS}
  · ${1}（读到 ${2}，应 ≥ ${3}）"; fi; }
ev_absent() { if [ "${2:-0}" -ne "0" ]; then EV_ERRS="${EV_ERRS}
  · ${1}（应彻底没有，实得 ${2} 处）"; fi; }
# 负锁的「注释行不计」helper（§BRIDGE-PATH/§89 同族坑：set -euo pipefail 下
# 管道里 grep 零命中退 1 会静默中止整轮，两头都要 || true 兜掉）
ev_code_hits() { # $1=文件 $2=扩展正则 → 非注释命中行数
	{ grep -nE "$2" "$1" 2>/dev/null || true; } | { grep -Ev '^[0-9]+:[[:space:]]*(//|\*)' || true; } | wc -l | tr -d ' '
}

# ── ① §A2 kill-switch 撤单失败明细 ──
ev_chk "HaltAll 结构化返回签名（唯一落点）" "$(grep -c 'func (c \*Controller) HaltAll() HaltAllResult' internal/trading/controller.go || true)" "1"
ev_absent "旧裸计数签名 HaltAll() int（降级报成功形态不得复活）" "$(grep -c 'HaltAll() int {' internal/trading/controller.go || true)"
ev_min "量规 halt_cancel_fail_count 覆写腿（含清零供 resolved，≥3 分支）" "$(grep -c 'metrics.SetGauge("halt_cancel_fail_count"' internal/trading/controller.go || true)" "3"
ev_chk "p1 规则 halt_cancel_failed 在位" "$(grep -c '"halt_cancel_failed"' internal/metrics/alerter.go || true)" "1"
ev_chk "halt_cancel_failed 路由 RoutePush" "$(grep -Ec '"halt_cancel_failed":[[:space:]]*RoutePush' internal/metrics/alert_routing.go || true)" "1"
ev_min "HTTP/SSE 腿消费失败明细（qmt.go HaltAllFailure）" "$(grep -c 'HaltAllFailure' internal/server/qmt.go || true)" "2"
ev_chk "A2 后端用例文件在位" "$(test -f internal/trading/halt_a2_test.go && echo 1 || echo 0)" "1"
ev_min "A2 用例数（主用例+全成功反证）" "$(grep -c '^func Test' internal/trading/halt_a2_test.go || true)" "2"
ev_min "前端 Quant 页失败回显文案" "$(grep -c '笔未撤成' web/src/pages/Quant.jsx || true)" "1"
ev_min "SSE 运维告警腿消费 failed 明细" "$(grep -c '笔未撤成' web/src/utils.js || true)" "1"

# ── ② §B1 财务续传键 ──
ev_chk "季度→期末日显式映射表（四值字面量唯一落点）" "$(grep -c 'QUARTER_END_MMDD = {1: "0331", 2: "0630", 3: "0930", 4: "1231"}' scripts/load_finance.py || true)" "1"
ev_absent "旧三元嵌套错键（季度序号进月份位＝Q3 永久跳过的根子）" "$(grep -c '31 if q == 1 else' scripts/load_finance.py || true)"
ev_min "report_period 定义+调用两腿" "$(grep -c 'report_period(year, q)' scripts/load_finance.py || true)" "1"
ev_min "装载器尾部按报告期 distinct 分布" "$(grep -c 'COUNT(DISTINCT ts_code)' scripts/load_finance.py || true)" "1"
ev_min "夜间验证按报告期 distinct 分布（不再只有 COUNT(*)）" "$(grep -c 'COUNT(DISTINCT ts_code)' scripts/verify_nightly.sh || true)" "1"
# 运行时断言（轻量，不连网不写库）：四键真值，重点锤 Q3==YYYY0930（旧键的坑恰好在这季）
EV_PYOUT="$(python3 -c '
import sys
sys.path.insert(0, "scripts")
import load_finance as lf
got = [lf.report_period(2024, q) for q in (1, 2, 3, 4)]
assert got == ["20240331", "20240630", "20240930", "20241231"], got
print("PYOK")
' 2>&1 || true)"
ev_chk "B1 运行时键断言（q=1..4 → 0331/0630/0930/1231）" "$(printf '%s' "$EV_PYOUT" | grep -c 'PYOK' || true)" "1"

# ── ③ §C1 实盘战法库闸 ──
ev_chk "闸唯一判定点定义在位" "$(grep -c 'func gateLiveStrategyLibrary' internal/engine/registry.go || true)" "1"
ev_chk "定义+调用两腿齐（各恰 1）" "$(grep -c 'gateLiveStrategyLibrary(dataDir' internal/engine/registry.go || true)" "2"
ev_chk "not_loaded 规则在位" "$(grep -c '"live_strategy_library_not_loaded"' internal/metrics/alerter.go || true)" "1"
ev_chk "no_enabled 规则在位" "$(grep -c '"live_strategy_no_enabled_rules"' internal/metrics/alerter.go || true)" "1"
ev_chk "not_loaded 路由" "$(grep -Ec '"live_strategy_library_not_loaded":[[:space:]]*RoutePush' internal/metrics/alert_routing.go || true)" "1"
ev_chk "no_enabled 路由" "$(grep -Ec '"live_strategy_no_enabled_rules":[[:space:]]*RoutePush' internal/metrics/alert_routing.go || true)" "1"
ev_min "两条告警文案分家：读库故障措辞" "$(grep -c '实盘战法库读取失败' internal/metrics/alerter.go || true)" "1"
ev_min "两条告警文案分家：零条启用措辞" "$(grep -c '零条启用规则' internal/metrics/alerter.go || true)" "1"
ev_min "三态+反证用例（not_loaded/no_enabled/OK/skipped）" "$(grep -c '^func Test' internal/engine/registry_library_gate_test.go || true)" "4"

# ── ④ §D1 配置热重载补锁 ──
ev_chk "全局 rules 唯一加锁读口径 Get()" "$(grep -c 'func (m \*Manager) Get() \*Rules {' internal/config/config.go || true)" "1"
ev_chk "成对快照 RulesD1Snapshot 在位" "$(grep -c 'func (m \*Manager) RulesD1Snapshot' internal/config/config.go || true)" "1"
ev_chk "Load 发布段 rules 指针替换恰 1 次" "$(grep -c 'm\.rules = wrapper\.Rules' internal/config/config.go || true)" "1"
ev_chk "Load 发布段 d1 指针替换恰 1 次" "$(grep -c 'm\.d1 = wrapper\.D1' internal/config/config.go || true)" "1"
ev_absent "config.go 活代码裸读 m.Rules/m.D1（导出字段已私有化；说明注释行不计）" "$(ev_code_hits internal/config/config.go 'm\.Rules\b|m\.D1\b')"
EV_D1X="$({ grep -rn 'CfgMgr\.Rules\.\|cfgMgr\.Rules\.' --include='*.go' cmd internal 2>/dev/null || true; } | { grep -Ev ':[0-9]+:[[:space:]]*(//|\*)' || true; } | wc -l | tr -d ' ')"
ev_absent "全仓活代码仍裸读 CfgMgr.Rules.（一律改走 Get()/GetRulesFor 族）" "$EV_D1X"
ev_chk "-race 反证用例在位（锤 Watch→Load 发布 vs 三读口径）" "$(grep -c 'func TestLoadWatchPublishRace' internal/config/load_watch_race_test.go || true)" "1"

if [ -z "$EV_ERRS" ]; then
	echo "ok - §0925EVE-W1 守卫通过（A2 锁 10 + B1 锁 6 含运行时键断言 + C1 锁 9 + D1 锁 7）"
else
	echo "--- FAIL: §0925EVE-W1 断言不符:${EV_ERRS}"
	exit 1
fi

# ════════════════════════════════════════════════════════════════════════════
# §99 §0925EVE-W3（2026-09-25 深夜批，owner 令「做完以后…全部做完 + 部署」）
# 第三波接线收口十一修（C2/C3/C4/C5/C6/C8 + D2/D3/D4/D5留痕 + E2/E4 + B5/B6 + F1/F2）
# 的落地锁。一物一锁防复活；全部负向锁带**注释行过滤**（§静态负锁教训：
# 「旧写法不得复活」的说明注释≠旧写法；JSX 还有 {/*…*/} 第三形态，滤要认三种）。
#   ① §E 交割/网关客户端：补记归因不再写虚构 "settle:"+day（无归因行走显式前缀）、
#      SettlementTrade 解码 signal_id、BrokerStatus 双腿（/health 基础 + /admin/status
#      专取，nil=没读到不许冒充）、InAuctionWindow 补第 15 判据（§96 同口径，
#      函数体 perl 抽取锤「体内确有 IsTradingDay」而非全文件计数）、order() 4xx 直败。
#   ② §F 观测/审计：资金新鲜度收口 cntime.Loc（controller.go 活代码 time.Local=0）、
#      SumFilledQty 出错留痕行在位（仍回 0，补的只是日志）、admin.go 审计行 3→8
#      （五类特权变更各有一行；09-29 §0929D1 写通道再加两行 ⇒ 本段该锁现为 10，见本段 ② §F D4 处说明）。
#   ③ §G 审批一致 + 第三态出口：Apply 失败保持 proposed 的用例在位；qmt_admin.go/
#      handlers_qmt_admin.go 两新文件、两路由注册、order-confirm 每次尝试落审计；
#      前端 API 尾部函数与 Quant「待核对委托」卡在位。
#   ④ §H UAT 自举：数据目录双口径清理（默认路径/所有权标记，防误删非属主目录）、
#      引擎指纹验证函数、端口 lsof 清场、409 显式话术。
#   ⑤ §I 前端：Signals.jsx 原生对话框活代码清零、App.jsx 死分支删除、审批后 refetch。
#   ⑥ §J 调研层：板块腿失败标记透传到类型层与 sector_agent、classifyPhase 未知态、
#      simulateCombo 循环体内确有 applyMinuteScope（awk 抽函数体，防 map 串台复活）。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 99 §0925EVE-W3 第三波接线收口：交割归因/BrokerStatus双腿/第15判据/4xx直败/时间口径/留痕/审计/审批一致/第三态面板/自举幂等/前端对话框/板块未知态（2026-09-25 owner 令「全部做完后部署」）..."
EW_ERRS=""
ew_chk() { if [ "$2" != "$3" ]; then EW_ERRS="${EW_ERRS}
  · ${1}（读到 ${2}，应为 ${3}）"; fi; }
ew_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then EW_ERRS="${EW_ERRS}
  · ${1}（读到 ${2}，应 ≥ ${3}）"; fi; }
ew_absent() { if [ "${2:-0}" -ne "0" ]; then EW_ERRS="${EW_ERRS}
  · ${1}（应彻底没有，实得 ${2} 处）"; fi; }
# 注释行过滤：Go 行注释、JS 行注释、块注释起始、JSX {/*、块注释续行 * —— 五种形态都算注释
ew_code_hits() { # $1=文件 $2=扩展正则 → 非注释命中行数
	{ grep -nE "$2" "$1" 2>/dev/null || true; } | { grep -Ev '^[0-9]+:[[:space:]]*(//|\{?/\*|\*)' || true; } | wc -l | tr -d ' '
}

# ── ① §E C5 交割归因 ──
ew_min "无归因行显式前缀常量（不再虚构 settle:+day）" "$(grep -c 'settleUnattributedPrefix' internal/trading/settlement.go || true)" "2"
ew_absent "旧虚构归因赋值 SignalID: \"settle:\"（活代码）" "$(ew_code_hits internal/trading/settlement.go 'SignalID: "settle:"')"
ew_chk "SettlementTrade 解码网关 signal_id" "$(grep -c 'json:"signal_id"' internal/trading/qmt_client.go || true)" "1"
# ── ① §E C4 BrokerStatus 双腿 ──
ew_min "BrokerStatus 走 /admin/status 专腿" "$(grep -c '/admin/status' internal/trading/qmt_client.go || true)" "1"
ew_min "读不到不冒充：AdminStatusOK 三态标记" "$(grep -c 'AdminStatusOK' internal/trading/qmt_client.go || true)" "2"
# ── ① §E C6 第 15 判据（§96 同口径；函数体抽取，不吃全文件计数） ──
EW_AUC="$(perl -0777 -ne 'my $c = () = /func InAuctionWindow\(.*?\{.*?IsTradingDay/s; print $c' internal/data/fetcher.go)"
ew_chk "InAuctionWindow 体内确有 IsTradingDay 闸（§96 漏网第 15 判据收口）" "$EW_AUC" "1"
# ── ① §E C8 4xx 直败 ──
ew_min "order() 确定性拒绝分类器（定义+调用）" "$(grep -c 'isDeterministicGatewayRejection' internal/trading/qmt_client.go || true)" "2"

# ── ② §F D2 时间口径 ──
ew_absent "controller.go 活代码 time.Local（资金新鲜度已收口 cntime.Loc）" "$(ew_code_hits internal/trading/controller.go 'time\.Local')"
# ── ② §F D3 留痕 ──
ew_chk "SumFilledQty 出错留痕行在位（0=未知口径保留）" "$(grep -c 'SumFilledQty 查询失败按 0 返回' internal/store/real_positions.go || true)" "1"
# ── ② §F D4 特权审计 ──
# 计数口径同日同步（本仓纪律，同 §88 INFO 行）：8 行＝09-25 批五类特权变更；
# 10 行＝09-29 §0929D1 稀疏 merge 写通道再加两行（config_d1_merge 变更留痕 +
# config_snapshot_failed 快照失败留痕——快照没落成也必须有痕迹，否则"改过什么"永远查不到）。
# 新增审计行必须同日同步这条数，并在 §106 逐名钉住动作名，防"少一行＝静默无痕"。
ew_chk "admin.go 审计行 8→10（09-25 五类特权变更 8 + §0929D1 写通道留痕 2）" "$(grep -c 'opslog.Audit' internal/server/admin.go || true)" "10"

# ── ③ §G C2 审批一致 ──
ew_min "Apply 失败保持 proposed 的用例在位" "$(grep -c 'TestResearchApproveApplyFailureKeepsProposed' internal/server/research_test.go || true)" "1"
# ── ③ §G C3 第三态出口 ──
ew_chk "网关管理面 Go 客户端新文件在位" "$(test -f internal/trading/qmt_admin.go && echo 1 || echo 0)" "1"
ew_chk "两 admin 端点 handler 新文件在位" "$(test -f internal/server/handlers_qmt_admin.go && echo 1 || echo 0)" "1"
ew_min "pending-review/order-confirm 路由注册" "$(grep -c 'pending-review\|order-confirm' internal/server/server.go || true)" "2"
ew_min "人工改判每次尝试落审计行" "$(grep -c 'qmt_order_confirm' internal/server/handlers_qmt_admin.go || true)" "1"
ew_min "前端 API 尾部新函数" "$(grep -c 'qmtPendingReview' web/src/api/index.js || true)" "1"
ew_min "Quant 页待核对委托卡（含四态文案）" "$(grep -c '待核对委托' web/src/pages/Quant.jsx || true)" "2"

# ── ④ §H UAT 自举幂等 + 身份 ──
ew_min "数据目录所有权标记（双口径清理防误删）" "$(grep -c 'UAT_MARKER' scripts/uat_bootstrap.sh || true)" "4"
ew_min "引擎实例指纹验证函数（定义+多次调用）" "$(grep -c 'verify_engine_fingerprint' scripts/uat_bootstrap.sh || true)" "2"
ew_min "端口清场 lsof 判据" "$(grep -c 'lsof -nP -tiTCP' scripts/uat_bootstrap.sh || true)" "1"
ew_min "409 已初始化显式话术（不再裸喂 json）" "$(grep -c '409' scripts/uat_bootstrap.sh || true)" "1"

# ── ⑤ §I 前端对话框/死分支/refetch ──
ew_absent "Signals.jsx 活代码 window.prompt/confirm/alert（WebView 静默吞，改走 TDesign）" "$(ew_code_hits web/src/pages/Signals.jsx 'window\.(prompt|confirm|alert)')"
ew_absent "App.jsx 恒不命中的 msg.signal 死分支（证据链见删除处注释）" "$(ew_code_hits web/src/App.jsx 'msg\.signal')"
ew_min "审批/驳回/灰度后统一 refetch（不就地改内存状态）" "$(grep -c 'refetchCandidatesAfterAction' web/src/pages/Research.jsx || true)" "2"

# ── ⑥ §J 板块未知态 + 分钟串台 ──
ew_min "classifyPhase 吃行情腿失败标记" "$(grep -c 'quoteLegFailed' internal/sector_agent/agent.go || true)" "2"
ew_min "SectorInfo 透传 QuoteLegFailed（数据层字段）" "$(grep -c 'QuoteLegFailed' internal/data/types.go || true)" "1"
EW_MS="$(awk '/func .*simulateCombo/{f=1} f&&/applyMinuteScope/{c++} f&&/^}$/{exit} END{print c+0}' internal/btreplay/sweep.go)"
ew_min "simulateCombo 循环体内确有 applyMinuteScope（map 串台不得复活）" "$EW_MS" "1"
ew_min "两票互不污染回归用例在位" "$(grep -c 'TestSimulateComboMinuteScopePerStock' internal/btreplay/*_test.go 2>/dev/null | awk -F: '{s+=$2} END{print s+0}')" "1"

if [ -z "$EW_ERRS" ]; then
	echo "ok - §0925EVE-W3 守卫通过（E 交割/网关 7 + F 观测审计 3 + G 审批/第三态 7 + H 自举 4 + I 前端 3 + J 调研层 4）"
else
	echo "--- FAIL: §0925EVE-W3 断言不符:${EW_ERRS}"
	exit 1
fi

# ════════════════════════════════════════════════════════════════════════════
# §100 §0926-W2（2026-09-26 批：owner 第二波裁决 ⑤⑥⑦⑧⑨⑩ + 三、的 E1/C7 两单源化）
# 一物一锁防复活，七族：
# ① A5 跌停追卖闸常开（*bool 三态，nil=开；旧 false 缺省形态不得回潜）。
# ② A1 柜台可卖量证据腿（CanUseQty *int 入账 → checkT1Sellable min(柜台,本地) 收紧；
#    golden/反射/真行采样三层契约锁 + COALESCE 覆盖腿恰 2 + 补列无 DEFAULT）。
# ③ B2 回放吃财务（finaProvider 装配 + 三个注入点 + §B2-FINA 收尾读数不静默）。
# ④ B3 三套打分口径只标注不统一（口径 A/B/C 三处标注 + 扫参哑火档标注 + 假话术墓碑）。
# ⑤ B4 幸存者偏差缺省开（PITEnabled nil=开 + UniverseAt 的 delist_date IS NULL 放行腿）。
# ⑥ B7 实盘财务公告日闸（LatestVisibleFina 唯一谓词家 + 实盘/回放两调用点）。
# ⑦ E1 盈亏单轨（算式只住 paperPnlTotals；校准 append-only 入库、清零服务端自算、
#    前端 localStorage 校准键判红）。⑧ C7 服务定义单源（部署清单/6 消费脚本/
#    verify_deploy 第 26 探针/服务名集合等值/旧 nssm 字面量负锁）。
# ⑨ 元闸（FIX_PLAN ⑨「元验收」兑现）：带 § 的防线函数必须 grep 到**非测试**调用点。
#    五例逐一过筛：PITEnabled/gateLiveStrategyLibrary/order-confirm 三条活线判红；
#    NewFailoverBoard 与 AuditRulesDiff 原为 OBS 待裁决（09-26 建闸时实测零生产调用），
#    同日下午 owner 裁决「移除/统一包装」已实施 ⇒ 两条同批转判红组（见下方 ⑨ 段说明）。
# 全部判据都是静态 grep/awk，真实行为回归在 -full 与各包用例（gate_test /
# real_positions_test / pnl_offset* _test / report_contract_test / test_report_contract.py /
# h2_positions_balance.test.jsx §E1 段）。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 100 §0926-W2 第二波裁决+A1/A5/B2/B3/B4/B7+E1 盈亏单轨+C7 服务单源+元闸（2026-09-26 owner 裁决批）..."
GW_ERRS=""
GW_OBS=""
gw_chk() { if [ "$2" != "$3" ]; then GW_ERRS="${GW_ERRS}
  · ${1}（读到 ${2}，应为 ${3}）"; fi; }
gw_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then GW_ERRS="${GW_ERRS}
  · ${1}（读到 ${2}，应 ≥ ${3}）"; fi; }
gw_absent() { if [ "${2:-0}" -ne "0" ]; then GW_ERRS="${GW_ERRS}
  · ${1}（应彻底没有，实得 ${2} 处）"; fi; }
# 非注释命中（§89/静态负锁同族坑：说明注释≠旧写法复活；set -e 下管道 grep 必带 || true）
gw_code_hits() { # $1=文件 $2=扩展正则 → 非注释命中行数
	{ grep -nE "$2" "$1" 2>/dev/null || true; } | { grep -Ev '^[0-9]+:[[:space:]]*(//|\*)' || true; } | wc -l | tr -d ' '
}
# 函数体（定义行→顶格 `}`）内按**字面子串**计数——绕开 BSD awk -v 对 \(\) 的吞转义坑
gw_awk_body() { # $1=文件 $2=sig子串 $3=body子串
	awk -v sig="$2" -v body="$3" 'index($0,sig){f=1} f && $0 ~ /^}$/ {exit} f && index($0,body){c++} END {print c+0}' "$1"
}
# 元闸取数口：全仓 Go 里非测试文件、非注释行、非 func 定义行的引用计数
gw_meta_calls() { # $1=扩展正则
	{ grep -rnE "$1" --include='*.go' cmd internal 2>/dev/null || true; } | grep -v '_test\.go:' | { grep -vE ':[0-9]+:[[:space:]]*(//|\*)' || true; } | { grep -vE ':[0-9]+:func ' || true; } | wc -l | tr -d ' '
}

# ── ① A5 跌停追卖闸常开 ──
gw_chk "A5 三态字段 *bool（nil=常开缺省）" "$(grep -c 'LimitDownBlockSell \*bool' internal/config/config.go || true)" "1"
gw_chk "A5 判定函数唯一落点" "$(grep -c 'func (r RiskGateConfig) LimitDownBlockSellEnabled() bool' internal/config/config.go || true)" "1"
gw_chk "A5 nil→true 常开腿在函数体内" "$(gw_awk_body internal/config/config.go 'LimitDownBlockSellEnabled() bool' 'return true')" "1"
gw_chk "A5 常开回归用例在位" "$(grep -c 'func TestGateLimitDownAlwaysOn' internal/risk/gate_test.go || true)" "1"

# ── ② A1 柜台可卖量证据腿 ──
gw_chk "A1 RealPosition.CanUseQty 三态字段（*int，NULL=未知）" "$(grep -c 'CanUseQty \*int' internal/store/real_positions.go || true)" "1"
gw_chk "A1 幂等补列且刻意无 DEFAULT（DEFAULT 0 会把存量行伪造成柜台真值）" "$(gw_code_hits internal/store/real_positions.go 'ALTER TABLE real_positions ADD COLUMN can_use_qty')" "1"
gw_absent "A1 负锁：补列带 DEFAULT（三态塌成 0=未知被伪造的形态）" "$(grep -c 'ADD COLUMN can_use_qty INTEGER DEFAULT' internal/store/real_positions.go || true)"
gw_chk "A1 COALESCE(新,旧) 覆盖腿恰 2（两条 upsert 路径都要接柜台值，缺一条=断一条通道）" "$(grep -c 'can_use_qty=COALESCE(excluded.can_use_qty, real_positions.can_use_qty)' internal/store/real_positions.go || true)" "2"
gw_chk "A1 柜台更小一律收紧（sellable=counter 必须落在 counter<localEst 分支体内）" "$(gw_awk_body internal/risk/gate.go 'if counter < localEst' 'sellable = counter')" "1"
gw_min "A1 交叉偏差告警腿（warn + onGate 推送）" "$(grep -c 'T+1 可卖量交叉偏差' internal/risk/gate.go || true)" "1"
gw_chk "A1 反射集 vs golden 持仓行字段锁在位" "$(grep -c 'vs golden.positions_row_fields' internal/server/report_contract_test.go || true)" "1"
gw_chk "A1 真行采样双测（样例==broker 真实产出、样例==golden 一致）" "$(grep -c 'def test_positions_sample_' qmt_gateway/tests/test_report_contract.py || true)" "2"
gw_min "A1 采样夹具在位（可卖量断腿的输入源）" "$(test -f qmt_gateway/contract/positions_sample.json && echo 1 || echo 0)" "1"

# ── ③ B2 因子回放喂财务输入 ──
gw_chk "B2 finaProvider 定义在位" "$(grep -c 'type finaProvider struct' internal/btreplay/fina_input.go || true)" "1"
gw_chk "B2 扫参注入点恰 2（主扫+复核链，B6 教训：漏一处串一台）" "$(grep -c 'o.applyFinaScope(ad' internal/btreplay/sweep.go || true)" "2"
gw_chk "B2 回放注入点恰 1" "$(grep -c 'o.applyFinaScope(ad' internal/btreplay/replay.go || true)" "1"
gw_min "B2-FINA 收尾读数（吃到多少票必须回显，不静默）" "$(grep -c 'B2-FINA' internal/btreplay/replay.go || true)" "2"

# ── ④ B3 三套打分口径：只标注不统一（owner 裁决「保留三套但各自标注清楚」） ──
gw_min "B3 口径 A 标注（ic.go 截面 z 后求和=预筛）" "$(grep -c '口径 A' internal/research/ic.go || true)" "1"
gw_min "B3 口径 B 标注（discover.go 原始加权和+复合分截面 z=证据）" "$(grep -c '口径 B' internal/research/discover.go || true)" "1"
gw_min "B3 口径 C 标注（scoring 包 时序分位×权重=下单）" "$(grep -c '口径 C' internal/research/scoring/scoring.go || true)" "1"
gw_min "B3 口径 C 标注（factor.scoreRule 实盘/回放同一 Evaluate）" "$(grep -c '口径 C' internal/strategies/factor/factor.go || true)" "1"
gw_min "B3 哑火档标注（因子分值域 [50,100] ⇒ 扫参 40/45 恒不过滤）" "$(grep -c '哑火' internal/btreplay/sweep.go || true)" "1"
gw_chk "B3 假话术墓碑在位（旧「三处同口径」声明已改判为不实并留痕）" "$(grep -c '该声明不实' internal/research/scoring/scoring.go || true)" "1"

# ── ⑤ B4 幸存者偏差缺省开（owner 裁决「开，默认打开」） ──
gw_chk "B4 PITEnabled 定义+调用两腿" "$(grep -c 'PITEnabled()' internal/btreplay/replay.go || true)" "2"
gw_chk "B4 nil=开语义唯一判定式" "$(grep -c 'return o.PointInTime == nil' internal/btreplay/replay.go || true)" "1"
gw_chk "B4 UniverseAt 的 delist_date IS NULL 放行腿（NULL 被三值逻辑逐出＝池静默缩水）" "$(grep -c "delist_date IS NULL OR delist_date = ''" internal/store/store.go || true)" "1"
gw_min "B4 降级读数不静默（poolNote 三态：显式关/起始日空/元数据零覆盖都点名）" "$(grep -c '幸存者偏差在体' internal/btreplay/replay.go || true)" "2"

# ── ⑥ B7 实盘财务公告日闸（owner 裁决「实盘财务按公告日对齐：是」） ──
gw_chk "B7 唯一谓词家 LatestVisibleFina 定义" "$(grep -c 'func LatestVisibleFina' internal/strategy_engine/fina_pit.go || true)" "1"
gw_chk "B7 实盘取数腿调用点在位（fina_cache）" "$(gw_code_hits cmd/quant/fina_cache.go 'strategy_engine\.LatestVisibleFina')" "1"
gw_chk "B7 回放取数腿调用点在位（fina_input 同谓词同口径）" "$(gw_code_hits internal/btreplay/fina_input.go 'strategy_engine\.LatestVisibleFina')" "1"
gw_chk "B7 滞后上限常量收编（实盘/回放同一 240 天）" "$(grep -c 'const FinaStaleMaxDays = 240' internal/strategy_engine/fina_pit.go || true)" "1"

# ── ⑦ E1 盈亏单轨（owner：「盈亏前端自算与后端两套账…要做」） ──
gw_chk "E1 校准端点路由注册（admin 收权）" "$(grep -c 'POST /api/holdings/pnl-offset' internal/server/server.go || true)" "1"
gw_chk "E1 唯一算式家 paperPnlTotals" "$(grep -c 'func (s \*Server) paperPnlTotals' internal/server/handlers_fix.go || true)" "1"
gw_chk "E1 清零值服务端自算（不信前端算术＝两套账的根子）" "$(gw_code_hits internal/server/handlers_fix.go 'newOff = r2\(realized \+ unrealized\)')" "1"
gw_chk "E1 偏移读失败抬 total_pnl:null（§N-5 姿势：错误不折进 0）" "$(grep -c 'pnl_offset_error' internal/server/handlers_fix.go || true)" "2"
gw_min "E1 append-only 校准表（只增不改不删，留痕可回放）" "$(grep -c 'pnl_offset_history' internal/store/pnl_offset.go || true)" "2"
gw_absent "E1 负锁：校准表出现 UPDATE/DELETE 语句（审计链不许被改写）" "$(grep -cE 'UPDATE pnl_offset_history|DELETE FROM pnl_offset_history' internal/store/pnl_offset.go || true)"
gw_chk "E1 前端正规通道函数在位" "$(grep -c 'export async function resetPaperPnlOffset' web/src/api/index.js || true)" "1"
gw_chk "E1 前端调用点（清零按钮走后端，不再写浏览器账）" "$(gw_code_hits web/src/pages/Positions.jsx 'resetPaperPnlOffset')" "1"
gw_absent "E1 负锁：前端 localStorage 盈亏校准键复活（两套账的存储形态）" "$(gw_code_hits web/src/pages/Positions.jsx 'localStorage\.(getItem|setItem)\(["'"'"']pnl')"
gw_min "E1 后端回归用例（等值链+admin-only 两条）" "$(grep -c '^func Test' internal/server/pnl_offset_e1_test.go || true)" "2"
gw_min "E1 store 回归用例（latest-wins+append-only 计数锁）" "$(grep -c '^func Test' internal/store/pnl_offset_test.go || true)" "2"
gw_min "E1 前端用例段（§E1 describe + 四用例）" "$(grep -c '§E1' web/src/__tests__/h2_positions_balance.test.jsx || true)" "8"

# ── ⑧ C7 Windows 服务拉起单源化（owner：「三套并存的单源化，要做」） ──
gw_chk "C7 单源文件在位" "$(test -f deploy/qmt-win/service_definitions.ps1 && echo 1 || echo 0)" "1"
gw_min "C7 部署清单接线（ps1_bom+scp 两腿，§ENH-5「仓库里有、现网没有」同族）" "$(grep -c 'service_definitions.ps1' scripts/deploy_guangzhou.sh || true)" "2"
# §0926E2E-W2C 同日同步：6→7——备份脚本接入单源取 ${SvcGatewayConfigFile}（密钥备份腿），
# 合法新增消费者，按「观测计数锁新增腿须同日同步」口径改判数（2026-09-27 预演读数 7）。
gw_chk "C7 消费脚本 dot-source 恰 7 个（三套并存的收编面+§0926E2E-W2C 备份腿）" "$(grep -l 'Join-Path \$PSScriptRoot "service_definitions.ps1"' deploy/qmt-win/*.ps1 | wc -l | tr -d ' ')" "7"
GW_SVCA="$(sed -n 's/^\$SvcName[A-Za-z]*[[:space:]]*=.*else { "\([^"]*\)" }$/\1/p' deploy/qmt-win/service_definitions.ps1 | sort | paste -sd, -)"
GW_SVCB="$(sed -n 's/.*foreach ($s in @("\(.*\)")).*/\1/p' scripts/verify_deploy_guangzhou.sh | tr -d '"' | tr ',' '\n' | tr -d ' ' | sort | paste -sd, -)"
gw_chk "C7 服务名集合等值：单源表 vs 部署探针（ defs=${GW_SVCA} probe=${GW_SVCB}）" "$( [ "$GW_SVCA" = "$GW_SVCB" ] && echo eq || echo ne )" "eq"
gw_chk "C7 verify_deploy 第 26 探针在位" "$(grep -c 'ops:C7 service_definitions single source in place' scripts/verify_deploy_guangzhou.sh || true)" "1"
gw_chk "C7 verify_deploy 传参腿（SSH 调用把落盘位喂给探针）" "$(grep -c 'SvcDefsPath ${QMT_WIN_DIR}/service_definitions.ps1' scripts/verify_deploy_guangzhou.sh || true)" "1"
gw_absent "C7 负锁：旧 C:\\qmt\\nssm 硬编码字面量复活（单源候选表之外只准待在注释里）" "$({ grep -n 'qmt\\nssm\\nssm.exe' deploy/qmt-win/*.ps1 2>/dev/null || true; } | grep -v '^deploy/qmt-win/service_definitions.ps1' | { grep -vE ':[0-9]+:[[:space:]]*#' || true; } | wc -l | tr -d ' ')"

# ── ⑨ 元闸：带 § 的防线函数必须有非测试调用点（FIX_PLAN ⑨ 元验收） ──
gw_min "元闸 PITEnabled（B4 时点池判定）有生产调用" "$(gw_meta_calls 'o\.PITEnabled\(\)')" "1"
gw_min "元闸 gateLiveStrategyLibrary（§95/C1 零规则闸）有生产调用" "$(gw_meta_calls 'gateLiveStrategyLibrary\(')" "1"
gw_chk "元闸 order-confirm（C3 第三态出口）路由注册在生产码" "$(gw_code_hits internal/server/server.go 'HandleFunc\("POST /api/qmt/order-confirm"')" "1"
# 2026-09-26 午后 owner 裁决到账，这两条由 OBS 观测读数转进判红组（52453ea 批只留读数，
# 同日实施批把裁决钉死）：
#   ① NewFailoverBoard 裁决「移除」——板块降级链零生产调用，函数本体连文件一起删；
#      锁＝全仓 Go 不再有任何 NewFailoverBoard 定义/引用（死代码不得复活）。
#   ② AuditRulesDiff 裁决「统一包装」——config.json 变更审计单入口；锁＝生产调用点 ≥2
#      （qmt 保存路 + 回滚路）+ qmt.go 里 opslog.Audit("config_change" 直写清零。
gw_absent "元闸 NewFailoverBoard（裁决：移除）不得复活" "$(gw_meta_calls 'NewFailoverBoard\(')"
gw_min "元闸 AuditRulesDiff（裁决：统一包装）生产调用 ≥2（qmt 保存+回滚两路）" "$(gw_meta_calls 'AuditRulesDiff\(')" "2"
gw_absent "元闸 负锁：qmt.go opslog.Audit(\"config_change\" 直写复活（绕过单入口）" "$(gw_code_hits internal/server/qmt.go 'opslog\.Audit\("config_change"')"

if [ -z "$GW_ERRS" ]; then
	echo "ok - §0926-W2 守卫通过（A5 锁 4 + A1 锁 9 + B2 锁 4 + B3 锁 6 + B4 锁 4 + B7 锁 4 + E1 锁 12 + C7 锁 7 + 元闸 6 判红组，含 09-26 午后两裁决转红）"
	if [ -n "$GW_OBS" ]; then echo "观察读数（不判红，待裁决项点名）:${GW_OBS}"; fi
else
	echo "--- FAIL: §0926-W2 断言不符:${GW_ERRS}"
	exit 1
fi

# ════════════════════════════════════════════════════════════════════════════
# §101 §0926PM（2026-09-26 下午批，owner 令「token 轮换+勘误 / 日历读数 / 二波+三季报重灌 / 门禁+部署」）
# 一物一锁，四族（②b 为 09-26 深夜收口批并入）：
# ① §FINA-Q3 三季报云端重灌通道（scripts/backfill_fina_q3_guangzhou.sh）：这条通道**会写现网
#    研究库**，安全口径必须逐条有锁而不是只活在头注释里——远端查询全走 mode=ro（缺省只读可证）、
#    只补缺失键（对财务三表零 UPDATE 零 DELETE）、双层回滚依据（SNAP_OK 表级快照 + 插入键台账，
#    台账行数等值锁）、磁盘护栏（ABORT=disk）、写后 MISSING 归零复核；再加两条运行时反证
#    （离网真跑：正常临时库必须停在 BatchMode 预探测且**任何写入侧判定行都不许出现**；空载荷
#    必须判红并点名原因，"0 行也算成功"是 §MINUTE 同款假绿）。ps1 体内掺中文会被它自己的
#    ASCII 闸整轮判红——09-26 建闸首跑实抓一次，静态锁防复发。
# ② §CAL-READOUT 日历已加载只读读数（verify_deploy 第 27 探针）：判据只认「负线晚于全部正线」
#    这一条不对称锁；凭据负锁（验证链零 /api/metrics 非注释命中，读数走磁盘两腿）；日志路径
#    必须先读 nssm 注册表键的 AppStdErr/AppStdout 再回落实测位（§N-5：解析控制台文本必踩坑）。
# ②b §A5-CURRENT 跌停追卖闸现网生效值（verify_deploy 第 28 探针，09-26 深夜收口批）：
#    常开裁决的部署前提「config.json 不得留 false」的免凭据取证腿——生效路径单点读法锁
#    （rules.qmt.risk_gate 恰 1 处）、不对称判据锁（唯一红线＝显式 false，死键/缺文件不判红）、
#    裸扫第二腿字面锁、INFO 恒回显锁、判据行中文负锁（GBK 回传课）。
# ③ §RESTIC-LOCK Mac 拉取腿陈旧锁自愈：两仓 unlock + 三败中途补一次（03:09 遗留锁拦死 07:00
#    窗的实录修法）；自愈必须非致命（每行带 || 兜底），且 copy 成败判定语义一字未动。
# 全部判据是静态 grep + 离网一次性临时库真跑（段尾统一 rm，测试数据不落工作树）。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 101 §0926PM 三季报通道+日历读数探针+跌停闸现值探针+restic 自愈（2026-09-26 下午批）..."
FQ_ERRS=""
fq_chk() { if [ "$2" != "$3" ]; then FQ_ERRS="${FQ_ERRS}
  · ${1}（读到 ${2}，应为 ${3}）"; fi; }
fq_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then FQ_ERRS="${FQ_ERRS}
  · ${1}（读到 ${2}，应 ≥ ${3}）"; fi; }
fq_absent() { if [ "${2:-0}" -ne "0" ]; then FQ_ERRS="${FQ_ERRS}
  · ${1}（应彻底没有，实得 ${2} 处）"; fi; }

FQ_SCR=scripts/backfill_fina_q3_guangzhou.sh
FQ_VD=scripts/verify_deploy_guangzhou.sh
FQ_RS=deploy/mac/restic_pull_backup.sh
FQ_SY=0; bash -n "$FQ_SCR" 2>/dev/null || FQ_SY=$?
fq_chk "§FINA-Q3 脚本语法自检 bash -n" "$FQ_SY" "0"
FQ_SY2=0; bash -n "$FQ_VD" 2>/dev/null || FQ_SY2=$?
fq_chk "verify_deploy 语法自检 bash -n（第 28 探针插入后）" "$FQ_SY2" "0"
FQ_SY3=0; bash -n "$FQ_RS" 2>/dev/null || FQ_SY3=$?
fq_chk "restic 拉取腿语法自检 bash -n" "$FQ_SY3" "0"

# ── ① §FINA-Q3 写入口径（静态） ──
fq_absent "对财务表的任何 UPDATE（本通道只补缺失键）" "$(grep -cE 'UPDATE (fina_indicator|income|cashflow)' "$FQ_SCR" || true)"
fq_absent "对财务表的任何 DELETE（回滚只准按台账另行处置）" "$(grep -cE 'DELETE FROM (fina_indicator|income|cashflow)' "$FQ_SCR" || true)"
fq_min "ro 连接腿在位（本机导出+远端复核+快照读+POST 回读，全走只读连接起步 3 条）" "$(grep -c 'mode=ro' "$FQ_SCR" || true)" "3"
fq_min "磁盘护栏判红在位（ABORT=disk）" "$(grep -c 'ABORT=disk' "$FQ_SCR" || true)" "1"
fq_min "表级快照判定行 SNAP_OK（判定+缺行判红两腿）" "$(grep -c 'SNAP_OK' "$FQ_SCR" || true)" "2"
fq_min "插入键台账按 TAG 落远端 backup_fina（第一层回滚依据）" "$(grep -c 'backup_fina/inserted-' "$FQ_SCR" || true)" "1"
fq_min "台账行数等值锁在位（行数!=inserted 即判红）" "$(grep -c '台账行数' "$FQ_SCR" || true)" "1"
fq_min "写后 MISSING 归零复核（口径⑤第二遍）" "$(grep -c 'missing=0' "$FQ_SCR" || true)" "1"
fq_min "ps1 组装后的非 ASCII 自检（§CAND-PUSH 同闸）" "$(grep -c "LC_ALL=C grep -n '\[^ -~\]'" "$FQ_SCR" || true)" "1"
fq_min "解析远端回传前去 CR（§CRLF 同课）" "$(grep -c "tr -d '\\\\r'" "$FQ_SCR" || true)" "1"
FQ_PS1BODY="$(awk '/run.ps1" <<.PSEOF\./{f=1;next} f&&/^PSEOF$/{exit} f' "$FQ_SCR")"
FQ_PS1CN="$(printf '%s' "$FQ_PS1BODY" | LC_ALL=C grep -c '[^ -~]' || true)"
fq_absent "编排 ps1 体内掺中文（撞自己的 ASCII 闸＝整轮判红，09-26 实录）" "$FQ_PS1CN"

# ── ①b §FINA-Q3 运行时离网真跑（一次性临时库，绝不连生产；段尾统一 rm）──
FQ_TMP="$(mktemp -d /tmp/verify101_XXXXXX)"
sqlite3 "$FQ_TMP/t.db" "CREATE TABLE fina_indicator (ts_code TEXT, end_date TEXT, eps DOUBLE, PRIMARY KEY(ts_code,end_date)); CREATE TABLE income (ts_code TEXT, end_date TEXT, revenue DOUBLE, PRIMARY KEY(ts_code,end_date)); INSERT INTO fina_indicator VALUES ('600000.SH','20250930',1.2),('000001.SZ','20240930',0.8); INSERT INTO income VALUES ('600000.SH','20250930',100.0);"
FQ_PVOUT="$FQ_TMP/preview.out"
FQ_PV=0
GZ_IP=203.0.113.7 LOCAL_DB="$FQ_TMP/t.db" bash "$FQ_SCR" > "$FQ_PVOUT" 2>&1 || FQ_PV=$?
fq_chk "离网真跑必须停在登录探测（非 0＝连不上生产时绝不自称成功）" "$([ "$FQ_PV" -ne 0 ] && echo nonzero || echo zero)" "nonzero"
fq_min "导表腿先于预探测完成（FINA_LOCAL 判定行回显计划数 3）" "$(grep -c 'FINA_LOCAL rows=3' "$FQ_PVOUT" || true)" "1"
FQ_WRITE="$(grep -cE 'FINA_BACKFILL_DONE|PS_DONE|SNAP_OK|ABORT=' "$FQ_PVOUT" || true)"
fq_absent "预览/失败路径出现任何写入侧判定行（写入口径只在 -Apply 远端真跑后出现）" "$FQ_WRITE"
sqlite3 "$FQ_TMP/e.db" "CREATE TABLE fina_indicator (ts_code TEXT, end_date TEXT); CREATE TABLE income (ts_code TEXT, end_date TEXT);"
FQ_EM=0
GZ_IP=203.0.113.7 LOCAL_DB="$FQ_TMP/e.db" bash "$FQ_SCR" > "$FQ_TMP/empty.out" 2>&1 || FQ_EM=$?
fq_chk "空载荷必须判红（0 行不算成功）" "$([ "$FQ_EM" -ne 0 ] && echo nonzero || echo zero)" "nonzero"
fq_min "空载荷失败原因点名（不许只留退出码）" "$(grep -c '载荷为空' "$FQ_TMP/empty.out" || true)" "1"
rm -rf "$FQ_TMP"

# ── ② §CAL-READOUT 第 27 探针 ──
fq_chk "日历探针判据行恰 1（不对称：红＝负线晚于全部正线）" "$(grep -c '\$calBad = (\$calNegTs -ne "" -and (\$calNegTs -gt \$calGoodTs))' "$FQ_VD" || true)" "1"
fq_chk "日历探针判定名恰 1（Probe 行）" "$(grep -c 'Probe "cal: trading calendar not in fail-open' "$FQ_VD" || true)" "1"
fq_min "INFO 恒回显通道（绿也要看得到读数，§SIGNAL-DIST 姿势）" "$(grep -c 'INFO|cal_readout' "$FQ_VD" || true)" "1"
fq_min "日志路径先读 nssm 注册表键 AppStdErr/AppStdout（§N-5）" "$(grep -c "@('AppStdErr', 'AppStdout')" "$FQ_VD" || true)" "1"
fq_min "注册表读不到时回落 prune_logs 实测位" "$(grep -c 'C:\\opt\\quant\\quant_stderr.log' "$FQ_VD" || true)" "1"
FQ_METRICS="$({ grep -n '/api/metrics' "$FQ_VD" || true; } | { grep -vE '^[0-9]+:[[:space:]]*#' || true; } | wc -l | tr -d ' ')"
fq_absent "凭据负锁：非注释行出现 /api/metrics（读数刻意走磁盘两腿，验证链不新增凭据）" "$FQ_METRICS"
FQ_CALCN="$({ grep -n 'calDetail\|cal_readout\|calBad' "$FQ_VD" || true; } | { grep -vE '^[0-9]+:[[:space:]]*#' || true; } | LC_ALL=C grep -c '[^ -~]' || true)"
fq_absent "中文判据负锁：cal 判据/明细行掺中文（GBK 回传课）" "$FQ_CALCN"
fq_min "verdict 四态锚之一 NOT-LOADED 在位" "$(grep -c '"NOT-LOADED"' "$FQ_VD" || true)" "1"

# ── ②b §A5-CURRENT 第 28 探针（09-26 深夜收口批：跌停追卖常开闸现网生效值补核，零凭据） ──
fq_chk "limitdown 判据赋值恰 1（唯一红线＝生效路径显式 false；死键/缺文件不判红的不对称钉死）" "$(grep -c '\$ldBad = (\$ldVal -eq "false")' "$FQ_VD" || true)" "1"
fq_chk "limitdown Probe 行恰 1" "$(grep -c 'Probe "cfg: limit-down sell-chase gate' "$FQ_VD" || true)" "1"
fq_min "limitdown INFO 恒回显通道（绿也要看得到现值）" "$(grep -c 'INFO|limitdown_readout' "$FQ_VD" || true)" "1"
fq_chk "裸扫第二腿字面在位（键位漂移检测的正则恰 1 处使用）" "$(grep -cF '"limit_down_block_sell"\s*:\s*false' "$FQ_VD" || true)" "1"
fq_chk "生效路径读法单点（rules.qmt.risk_gate 取数行恰 1，防第二处读法漂移）" "$(grep -c '$ldJson.rules.qmt.risk_gate.limit_down_block_sell' "$FQ_VD" || true)" "1"
FQ_LDCN="$({ grep -n 'ldBad\|ldDetail\|limitdown_readout\|ldVal' "$FQ_VD" || true; } | { grep -vE '^[0-9]+:[[:space:]]*#' || true; } | LC_ALL=C grep -c '[^ -~]' || true)"
fq_absent "中文判据负锁：limitdown 判据/明细行掺中文（GBK 回传课）" "$FQ_LDCN"

# ── ③ §RESTIC-LOCK 拉取腿陈旧锁自愈 ──
fq_min "unlock 点名（两仓首清+三败中途补+口径注释，≥6 处）" "$(grep -c 'unlock' "$FQ_RS" || true)" "6"
FQ_UNLOCK_RAW="$({ grep -E '^[[:space:]]*restic .*unlock' "$FQ_RS" || true; } | { grep -vc '|| ' || true; })"
fq_absent "unlock 命令行无 || 兜底（自愈必须非致命，真并发锁照样交给 copy 判红）" "$FQ_UNLOCK_RAW"
fq_chk "copy 成败判定语义一字未动（COPY_OK 判定行恰 1）" "$(grep -c '\[ "\$COPY_OK" = "1" \] || fail' "$FQ_RS" || true)" "1"

if [ -z "$FQ_ERRS" ]; then
	echo "ok - §0926PM 守卫通过（FINA-Q3 静态锁 11 + 离网反证 5 + 日历探针锁 8 + 跌停闸现值探针锁 6 + restic 自愈锁 3）"
else
	echo "--- FAIL: §0926PM 断言不符:${FQ_ERRS}"
	exit 1
fi


# ════════════════════════════════════════════════════════════════════════════
# §102 §QMT-TOKENROT-CLI（2026-09-26，owner 令「门禁和规则摸牌后，你来补上网关口令令牌」）：
# 轮换从「等 owner 手跑」收编成 Mac 侧正规通道 scripts/rotate_qmt_token_guangzhou.sh。
# 这条通道**会写现网鉴权面**（源1 网关配置文件 + 源2 服务 env 冗余腿），三条口径逐条有锁：
# ① 缺省零连接预览（离网真跑必须 exit 0 且回显 ROTATE_PLAN——连不上生产也照样出计划，
#    与 §101「连不上必停」互为镜像：预览本来就不该连）；-Bogus/双开关必须退 2；
#    离网 -DryRun/-Apply 必须非 0 停在预探测，**任何判定行都不许出现**（假绿反证）。
# ② 密钥纪律成码：驱动内必须有 40+hex 明文闸（固定串锁），且**不骑 admin 会话**
#    （admin_session_token 零引用负锁——轮换写侧全在服务器本地，压根不需要设置页凭据）；
#    判定行锚点与 ps1 侧**两侧同源**（驱动认的三行＝ps1 真写的三行，改一边即红）。
# ③ 现网脚本落点路径锁（§ENH-5 形态：仓库里有、现网也必须在那个路径有）。
# ③ 现网脚本落点路径锁（§ENH-5 形态：仓库里有、现网也必须在那个路径有）。
# ④ §0926ROT-ALIGN（2026-09-26 owner 令「我授权你来统一改」）：-Align 对齐方向（源1 现值→
#    源3 引擎 config.json/源4 桥配置，不生成新 token）——新判定名 ALIGN_READOUT/ALIGN_APPLIED
#    各恰 1、开关映射锁、驱动锚与 ps1 写行两侧同源（含"提案自证"拒判腿），离网两形态
#    （-Align / -Align -Apply）必须死在预探测且判定名一条都不许出现；判定名与轮换互不借用。
# ⑤ §0926ROT-SRC5（2026-09-26 深夜发版实录）：-Align 补源5＝auth.json 账号级快照
#    （configs[]→quant_config_json_v1→.qmt.token，GetQMTConfigFor 的账号覆盖层——09-26 实录：
#    只对齐源3 时 verify ③b 腿仍红 engineAuth 分歧＝真实下单链拿旧口令）。三态判定行
#    （absent/same/write）+ 计划/拒判/复读锚点两侧同源；写后复读 src5 残留非对齐指纹必拦停；
#    同腿修 verify 第 20 探针 env 读法（Services\<svc> 单级 → +\Parameters 两级直读，
#    §0926-ROT 同款失明——旧读法把"有键"恒记 key-absent，env 源从未参与对账）。
#    生效前提写进 ps1/驱动收尾口径：auth store 启动时载入，src5 写过必须重启 quant。
# 全部判据＝静态 grep -F + 离网一次性真跑（TEST-NET 假 IP，BatchMode 预探测必失败，
# 零凭据零写入；每跑 ≤10s 超时，整段增时约一分钟）。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 102 §QMT-TOKENROT-CLI 网关口令轮换正规通道（2026-09-26 owner 令收编）..."
RT_ERRS=""
rt_chk() { if [ "$2" != "$3" ]; then RT_ERRS="${RT_ERRS}
  · ${1}（读到 ${2}，应为 ${3}）"; fi; }
rt_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then RT_ERRS="${RT_ERRS}
  · ${1}（读到 ${2}，应 ≥ ${3}）"; fi; }
rt_absent() { if [ "${2:-0}" -ne "0" ]; then RT_ERRS="${RT_ERRS}
  · ${1}（应彻底没有，实得 ${2} 处）"; fi; }

RT_SCR=scripts/rotate_qmt_token_guangzhou.sh
RT_PS1=deploy/qmt-win/rotate_qmt_token.ps1
RT_SY=0; bash -n "$RT_SCR" 2>/dev/null || RT_SY=$?
rt_chk "驱动脚本语法自检 bash -n" "$RT_SY" "0"
# §89 自指锁（09-26 实录：`cmd; rc=$?` 裸跑在 set -euo pipefail 下让整轮 verify 无 FAIL 无 ok
# 静默中止，exit 2 连红单都不留）：七处预期失败的反证子进程必须全部写成 `|| RT_XX=$?` 收码。
# ⚠ 本段三行消息串里**绝不许出现反引号**——双引号内反引号＝命令替换，语法坏时 rt_chk 整行静默
# 不执行（09-26 复演实录：假绿形态），要引用形态一律用「」。
rt_chk "反证裸跑形态清零（「cmd; RC=$?」直连 RT_XX=$ 的写法必须不存在，§89 同族自指）" "$(grep -Ec '; RT_(PV|BG|MX|DR|AP|AD|AA)=[$]' scripts/verify_changes.sh || true)" "0"
rt_min "七处反证全部 RT_XX=0; 收码起跑（行首锚定 ≥7，PV/BG/MX/DR/AP/AD/AA 各一）" "$(grep -Ec '^RT_(PV|BG|MX|DR|AP|AD|AA)=0; GZ_IP=203' scripts/verify_changes.sh || true)" "7"
rt_min "七处收码尾巴在位（|| 直连 RT_XX= 计数 ≥7，缺一个即该处会静默杀父）" "$(grep -Ec '[|][|] RT_(PV|BG|MX|DR|AP|AD|AA)=' scripts/verify_changes.sh || true)" "7"

# ── ② 静态：缺省方向 / 明文闸 / 凭据负锁 / 两侧同源 ──
rt_chk "缺省零连接预览判定名恰 1（ROTATE_PLAN connect=0）" "$(grep -Fc 'mode=preview connect=0' "$RT_SCR" || true)" "1"
rt_chk "干跑成功判定名恰 1（ROTATE_READOUT）" "$(grep -Fc 'ROTATE_READOUT ok' "$RT_SCR" || true)" "1"
rt_chk "轮换成功判定名恰 1（ROTATE_APPLIED）" "$(grep -Fc 'ROTATE_APPLIED token_fp=' "$RT_SCR" || true)" "1"
rt_chk "40+hex 明文闸固定串在位恰 1（值不该到过打印层，出现即停手 exit 4）" "$(grep -Fc '[0-9a-f]{40,}' "$RT_SCR" || true)" "1"
rt_absent "驱动不骑 admin 会话（admin_session_token 零引用——轮换写侧走服务器本地，不新增凭据面）" "$(grep -Fc 'admin_session_token' "$RT_SCR" || true)"
rt_absent "驱动不自读网关配置文件内容（Get-Content 零命中——值只在 ps1 函数栈里过）" "$(grep -Fc 'Get-Content' "$RT_SCR" || true)"
rt_chk "现网脚本落点路径锁（deploy 清单同一落点，§ENH-5 防"仓库有现网没"）" "$(grep -Fc 'ROTATE_PS1:-C:/opt/quant/qmt-win/rotate_qmt_token.ps1' "$RT_SCR" || true)" "1"
rt_min "解析远端回传前去 CR（§CRLF 同课，≥2 处）" "$(grep -c "tr -d '\\\\r'" "$RT_SCR" || true)" "2"
# 两侧同源：驱动认的三条判定行必须真是 ps1 写出来的那三行（改 ps1 文案不同步驱动＝红）。
rt_min "ps1 侧判定行「config_file written token_fp=」在位" "$(grep -Fc 'config_file written token_fp=' "$RT_PS1" || true)" "1"
rt_min "ps1 侧判定行「read-back confirmed」在位" "$(grep -Fc 'read-back confirmed' "$RT_PS1" || true)" "1"
rt_min "ps1 侧判定行「self-check(a) gateway file source CONFIRMED」在位" "$(grep -Fc 'self-check(a) gateway file source CONFIRMED' "$RT_PS1" || true)" "1"
# [2b] 现网脚本对齐腿（2026-09-26 实录：`-s` 部署不重传 ps1，现网停在带语法坏行的旧版，
# 干跑首跑远端 ParserError 才暴露）——通道必须先比对再执行，判据三处同源锁：
rt_chk "对齐成功判定名恰 1（ROTATE_PS1_SYNCED，只在真覆盖后出现）" "$(grep -Fc 'ROTATE_PS1_SYNCED' "$RT_SCR" || true)" "1"
rt_min "远端指纹读法走 Get-FileHash（不回显全 64 位，管道内截 12 位前缀）" "$(grep -Fc 'Get-FileHash' "$RT_SCR" || true)" "1"
rt_min "覆盖前远端必落 .stale 时间戳副本（备份不成就不覆盖）" "$(grep -Fc '.stale-' "$RT_SCR" || true)" "1"
# ── §0926ROT-ALIGN（owner 令「我授权你来统一改」）：-Align 对齐方向的判定名/映射/两侧同源 ──
# 对齐＝不生成新 token，把源1 现值在服务器本地补到源3/源4。新判定名各恰 1（与轮换判定名
# 互不借用——ROTATE_APPLIED 恰 1 的旧锁同时钉住"align 分支没伪装成轮换成功"这一形态）。
rt_chk "对齐计划判定名恰 1（ALIGN_READOUT ok）" "$(grep -Fc 'ALIGN_READOUT ok' "$RT_SCR" || true)" "1"
rt_chk "对齐落地判定名恰 1（ALIGN_APPLIED token_fp=）" "$(grep -Fc 'ALIGN_APPLIED token_fp=' "$RT_SCR" || true)" "1"
rt_min "两轴合成折叠：只读向 aligndry 赋值在位（-Align 缺省必须落在只读向）" "$(grep -Fc 'MODE="aligndry"' "$RT_SCR" || true)" "1"
rt_min "两轴合成折叠：落地向 alignapply 赋值在位" "$(grep -Fc 'MODE="alignapply"' "$RT_SCR" || true)" "1"
rt_chk "落地开关映射恰 1（alignapply 只送「-Align -Apply」，禁把 -Apply 单独发给 ps1 触发轮换）" "$(grep -Fc 'FLAG="-Align -Apply"' "$RT_SCR" || true)" "1"
rt_min "ps1 侧对齐计划判定行「align src3 plan 」在位" "$(grep -Fc 'align src3 plan ' "$RT_PS1" || true)" "1"
rt_min "ps1 侧对齐计划判定行「align src4 plan 」在位" "$(grep -Fc 'align src4 plan ' "$RT_PS1" || true)" "1"
rt_min "ps1 侧对齐完成判定行「align src3 done token_fp=」在位" "$(grep -Fc 'align src3 done token_fp=' "$RT_PS1" || true)" "1"
rt_min "ps1 侧对齐完成判定行「align src4 done 」在位" "$(grep -Fc 'align src4 done ' "$RT_PS1" || true)" "1"
rt_min "驱动认的 src3 完成锚点在位（两侧同源：驱动锚＝ps1 真写）" "$(grep -Fc 'align src3 done token_fp=' "$RT_SCR" || true)" "2"
rt_min "驱动认的 src4 完成锚点在位（同上，锚点表+提取腿各一处）" "$(grep -Fc 'align src4 done ' "$RT_SCR" || true)" "2"
# ── §0926ROT-SRC5 锁组（09-26 深夜实录：账号快照是 GetQMTConfigFor 的账号覆盖层，漏了它
#    等于没对齐——计数全部先预演读数再入段，形态与 src3/src4 锁同族）──
rt_chk "ps1 侧 src5 读侧回显三形态在位（read/parse-error/no-file 各恰 1，失明必现形）" "$(grep -Fc 'src5 auth_snap' "$RT_PS1" || true)" "3"
rt_min "ps1 侧对齐计划判定行「align src5 plan 」在位" "$(grep -Fc 'align src5 plan ' "$RT_PS1" || true)" "1"
rt_chk "ps1 侧 src5 完成判定三态在位（absent/same/write 各一条 done 行，恰 3）" "$(grep -Fc 'align src5 done token_fp=' "$RT_PS1" || true)" "3"
rt_min "ps1 侧 src5 写侧拒判族在位（提案解析/字节差/条数/指纹未命中全过才落盘，≥8）" "$(grep -Fc 'align src5 refused' "$RT_PS1" || true)" "8"
rt_min "ps1 侧 src5 写后复读自证在位（parse/指纹/条数三查，≥3）" "$(grep -Fc 'align src5 read-back' "$RT_PS1" || true)" "3"
rt_min "驱动认的 src5 完成锚点在位（锚点表+提取腿各一处，两侧同源）" "$(grep -Fc 'align src5 done ' "$RT_SCR" || true)" "2"
rt_min "驱动 aligndry 计划锚含 src5（计划读数不完整即停）" "$(grep -Fc 'align src5 plan ' "$RT_SCR" || true)" "1"
rt_min "驱动写后复读 src5 腿在位（取值+判读≥4 处，残留非对齐指纹必拦停）" "$(grep -Fc 'SRC5_LINE' "$RT_SCR" || true)" "4"
rt_min "驱动干跑五源锚行在位（'[rot] src5 ' 进锚点表）" "$(grep -Fc "[rot] src5 " "$RT_SCR" || true)" "1"
rt_min "verify 第 20 探针 env 腿两级读法在位（both-paths-unreadable 自证串恰 1）" "$(grep -Fc 'both-paths-unreadable' scripts/verify_deploy_guangzhou.sh || true)" "1"
rt_min "ps1 侧对齐写前三重拒判之「提案自证」在位（指纹不命中锚点块即一个字都不写）" "$(grep -Fc '提案自证不过' "$RT_PS1" || true)" "2"
# 09-26 对齐批实锤的假绿补洞：仓内词法扫描把反斜杠当普通字符，反斜杠贴引号恰好抵平括号照样
# PASS，但 PS5.1 不认反斜杠转义——现网首跑 ParserError。形态锁：该串在 rotate ps1 出现即红。
rt_absent "rotate ps1 反斜杠贴双引号形态清零（PS 转义只认反引号，C 形写法＝引号语境翻转炸弹）" "$(grep -Fc '\""' "$RT_PS1" || true)"
# 09-26 真跑锤实的两处判据修复（两轮 -Apply 都被"写侧成功、自证假红"拦停）：
# ①NSSM 真存储走 Services\<svc>\Parameters 两级路径；②`return ,$list` 穿 @(func) 落嵌套数组，
#   调用侧必须 Flatten-EnvPairs 展平——定义 1 + 调用 3（src 读/并集/写回读）＝4 处，少一处即漏网。
RT_REG=deploy/qmt-win/register_engine_services.ps1
rt_chk "rotate ps1 Flatten-EnvPairs 在位恰 4（定义+三调用点全展平，漏一个＝§N-5 并集洗键复发）" "$(grep -Fc 'Flatten-EnvPairs' "$RT_PS1" || true)" "4"
rt_chk "rotate ps1 读法覆盖 Parameters 真存储路径恰 2（函数+诊断各一处）" "$(grep -Fc '"\Parameters"' "$RT_PS1" || true)" "2"
rt_min "register ps1 同步同款展平（定义+两调用点 ≥3，否则重跑注册会静默丢现值键）" "$(grep -Fc 'Flatten-EnvPairs' "$RT_REG" || true)" "3"
rt_min "register ps1 读法同步覆盖 Parameters 路径（≥1）" "$(grep -Fc '"\Parameters"' "$RT_REG" || true)" "1"
# ③括号/花括号平衡机器锁：09-23 写下的 186 行坏语法潜伏三天、现网首跑才 ParserError——
# 仓内没有真 PS 解析器，用反引号/引号/注释感知的词法扫描顶替，两份 ps1 必须归零且无下溢。
RT_BAL="$(python3 - "$RT_PS1" "$RT_REG" <<'PY'
import sys
ok = True
for path in sys.argv[1:]:
    src = open(path, encoding='utf-8-sig').read()
    i, n = 0, len(src)
    dp = db = 0; under = 0
    while i < n:
        c = src[i]
        if c == '#':
            j = src.find('\n', i); i = n if j < 0 else j; continue
        if c == "'":
            j = i + 1
            while j < n and src[j] != "'": j += 1
            i = j + 1; continue
        if c == '"':
            j = i + 1
            while j < n:
                if src[j] == '`': j += 2; continue
                if src[j] == '"': break
                j += 1
            i = j + 1; continue
        if c == '`': i += 2; continue
        if c == '$' and i + 1 < n and src[i+1] == '(': dp += 1; i += 2; continue
        if c == '(': dp += 1
        elif c == ')':
            dp -= 1
            if dp < 0: under = 1
        elif c == '{': db += 1
        elif c == '}':
            db -= 1
            if db < 0: under = 1
        i += 1
    if dp != 0 or db != 0 or under: ok = False
print('PASS' if ok else 'FAIL')
PY
)" 2>/dev/null || RT_BAL="FAIL"
rt_chk "两份 ps1 括号平衡词法扫描（防 09-23 潜伏坏行复发：仓内无 PS 解析器，首跑=现网炸）" "$RT_BAL" "PASS"

# ── ① 离网真跑反证（TEST-NET 假 IP，绝不触生产）──
# ⚠ §89 同族坑实录（09-26 首跑）：本脚本开头开着 `set -euo pipefail`，`cmd; rc=$?` 形态在
#    cmd 预期非 0（反证要它失败）时会先杀死父 shell——整轮 verify 无 FAIL 无 ok 静默中止。
#    正解＝`rc=0; cmd || rc=$?`（成功时 rc 保持 0，失败时收码断言）。
RT_TMP="$(mktemp -d /tmp/verify102_XXXXXX)"
RT_PV=0; GZ_IP=203.0.113.7 bash "$RT_SCR" > "$RT_TMP/plan.out" 2>&1 || RT_PV=$?
rt_chk "缺省预览离网必须 exit 0（预览态不连生产是设计，不是没测到）" "$RT_PV" "0"
rt_chk "预览回显计划判定行" "$(grep -Fc 'ROTATE_PLAN mode=preview connect=0' "$RT_TMP/plan.out" || true)" "1"
rt_absent "预览态不得出现任何连接脚印（通道可用＝连过了）" "$(grep -Fc '通道可用' "$RT_TMP/plan.out" || true)"
RT_BG=0; GZ_IP=203.0.113.7 bash "$RT_SCR" -Bogus > "$RT_TMP/bogus.out" 2>&1 || RT_BG=$?
rt_chk "未知参数必须退 2（缺省方向不靠猜）" "$RT_BG" "2"
RT_MX=0; GZ_IP=203.0.113.7 bash "$RT_SCR" -DryRun -Apply >/dev/null 2>&1 || RT_MX=$?
rt_chk "双开关互斥必须退 2（与 ps1 同规）" "$RT_MX" "2"
RT_DR=0; GZ_IP=203.0.113.7 bash "$RT_SCR" -DryRun > "$RT_TMP/dr.out" 2>&1 || RT_DR=$?
rt_chk "离网干跑必须非 0（连不上就绝不自称读数完整）" "$([ "$RT_DR" -ne 0 ] && echo nonzero || echo zero)" "nonzero"
rt_absent "离网干跑不许出现任何读数判定行" "$(grep -Fc 'ROTATE_READOUT' "$RT_TMP/dr.out" || true)"
RT_AP=0; GZ_IP=203.0.113.7 bash "$RT_SCR" -Apply > "$RT_TMP/ap.out" 2>&1 || RT_AP=$?
rt_chk "离网轮换必须非 0（预探测拦停）" "$([ "$RT_AP" -ne 0 ] && echo nonzero || echo zero)" "nonzero"
rt_absent "离网轮换不许出现写入判定行（写入口径只在远端真跑后出现）" "$(grep -Fc 'ROTATE_APPLIED' "$RT_TMP/ap.out" || true)"
rt_absent "离网任何一轮都不许出现现网 ps1 的 src 读数行（防「假 IP 连真机」串台）" "$(grep -Fc '[rot] src1' "$RT_TMP/dr.out" || true)"
rt_absent "离网干跑不许出现对齐同步判定行（预探测拦停，[2b] 不该被走到）" "$(grep -Fc 'ROTATE_PS1_SYNCED' "$RT_TMP/dr.out" || true)"
rt_absent "离网轮换不许出现对齐同步判定行（同上——没连上就一条都不许写）" "$(grep -Fc 'ROTATE_PS1_SYNCED' "$RT_TMP/ap.out" || true)"
# §0926ROT-ALIGN 离网反证：-Align 两形态同样必须死在预探测——对齐方向没有"连不上也照样绿"的余地。
RT_AD=0; GZ_IP=203.0.113.7 bash "$RT_SCR" -Align > "$RT_TMP/ad.out" 2>&1 || RT_AD=$?
rt_chk "离网 -Align 必须非 0（连不上就绝不自称计划完整）" "$([ "$RT_AD" -ne 0 ] && echo nonzero || echo zero)" "nonzero"
rt_absent "离网 -Align 不许出现 ALIGN_READOUT 判定行" "$(grep -Fc 'ALIGN_READOUT' "$RT_TMP/ad.out" || true)"
rt_absent "离网 -Align 不许出现现网脚本同步脚印" "$(grep -Fc 'ROTATE_PS1_SYNCED' "$RT_TMP/ad.out" || true)"
RT_AA=0; GZ_IP=203.0.113.7 bash "$RT_SCR" -Align -Apply > "$RT_TMP/aa.out" 2>&1 || RT_AA=$?
rt_chk "离网 -Align -Apply 必须非 0（写方向预探测拦停）" "$([ "$RT_AA" -ne 0 ] && echo nonzero || echo zero)" "nonzero"
rt_absent "离网 -Align -Apply 不许出现 ALIGN_APPLIED 写入判定行" "$(grep -Fc 'ALIGN_APPLIED' "$RT_TMP/aa.out" || true)"
rt_absent "离网 -Align -Apply 不许串到轮换判定名（两方向判定互不借用）" "$(grep -Fc 'ROTATE_APPLIED' "$RT_TMP/aa.out" || true)"
rm -rf "$RT_TMP"

if [ -z "$RT_ERRS" ]; then
	echo "ok - §QMT-TOKENROT-CLI 守卫通过（静态锁含两侧同源锚 46 + 离网反证 18；§0926ROT-SRC5 账号快照腿后共 64）"
else
	echo "--- FAIL: §102 断言不符:${RT_ERRS}"
	exit 1
fi

# ════════════════════════════════════════════════════════════════════════════
# §103 §0926E2E（2026-09-26 晚~09-27 全量审计修复批，owner 令「全部按推荐选项执行」）：
# 一~四波+矩阵补位的机器锁段。设计口径：
# ① 锁「吞错放水」家族的复发面（error 被 `_` 丢弃、伪造中性值、死开关回潮），
#    全部按运行时真实取值链选锚（记忆 [[probe-premises-from-value-chain]]）；
# ② 负向锁避说明注释误伤（记忆 [[static-negative-locks]]）：OptimizeAutoApply 的
#    删除说明注释本身含该词 → 负锁只打「字段定义形态」（行首空白+标识符），注释行
#    以 // 开头天然不中；1500 伪造锁打赋值形态 `pCount = 1500` 而非裸 1500；
# ③ 新锁全部计数先预演后入段（§GATE-COUNT-LOCK 教训，2026-09-27 预演记录：
#    gate.go 吞错形态 0/正向 1、market.go 1500 形态 0、server 非测试 _=json 0、
#    W1B 锚 config 11/qmt 2、SETUP_TOKEN 面 7 文件、截止等值串 1、expires_at 1、
#    W2C 1/W2D 2、16 回潮 0、11A 4、4B 4、3A 1、OFFLINE=2 处 1、17c 1、17A-Dash 5、
#    17B-fetcher 1、17D-Quant 2、mock 三路由 1/1/1、relax 默认 false 1、queued 直读 1、
#    apk-smoke 1/EMBED 锁 1、persist/hotSwap false 1、StartWeeklyLLMProbe 接线 1）；
# ④ 行为用例不在此段（go/pytest/vitest/Playwright 四套各自已带），本段只防静态回潮。
# ⑤ 同日两次同步实录（09-29 15:0x，全量门禁 E 轮在本段判红）：
#    「1-B W1B 锚 config.go」预演 11，现读到 13 —— 本批 ① D1 落码时在 config.go 写了两条 setter
#    注释、按同口径交叉引用了 §0926E2E-W1B 这个锚名，锚计数把交叉引用一并数进去。这是今日第三例
#    「加了一条腿/一句引用，计数锁没跟着同步」（前两例＝§88 INFO 观测行 5→6、§99 admin 审计行 8→10），
#    且本例是锁**按设计应该红**的那一类：它证明这条锚计数确实咬在文件内容上。处置＝11→13 同步并在
#    FAIL 文案点名两行来源（不回退阈值、不放宽成 ≥）。
#    同轮附带修正：e2e_eq 的判红文案把两个数打反（$2 实读被打成「期望」、$3 预演被打成「实得」），
#    本段全部等值调用按「先读数、后预演值」传参，故文案改印「实读=/应=」——读反一次就够，
#    会把下一个人往「把阈值改成实读数以外」的方向同步。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 103 §0926E2E 全量审计修复批（一~四波+矩阵补位+§0927KA 保活加固静态锁 36 条（含 §0926E2E-13b 双发守卫三枚，09-27 增））..."
E2E_ERRS=""
# e2e_eq <名> <实得> <期望>：等值锁（本段全部调用按「先读数值、后写预演值」传参）；
# e2e_ge <名> <下限> <实得>：存在性下限锁（先阈值后读数）。
# （≥ 用于"注释/锚点条数会随后续施工增加"的观测，防单向等值把后人合法加注释判红。）
# 09-29 实录修文案：旧消息把 $2 打成「期望」、$3 打成「实得」，与等值锁的传参顺序正好相反，
# 判红时读到的"期望=13 实得=11"其实是"实读 13、应为 11"——把口径写反的锁文案会让人往错误方向
# 同步计数（本仓教训：判据文案必须与传参实序同形），故等值锁改印「实读=/应=」。
e2e_eq() {
	if [ "$2" != "$3" ]; then E2E_ERRS="${E2E_ERRS}
  - $1: 实读=$2 应=$3"; fi
	return 0
}
e2e_ge() {
	if [ "$3" -lt "$2" ]; then E2E_ERRS="${E2E_ERRS}
  - $1: 下限=$2 实得=$3"; fi
	return 0
}
# ec <file> <fixed-pattern>：grep -Fc 计数（无匹配 exit 1 → || true 归 0，set -e 雷区记忆）
ec() { grep -Fc -- "$2" "$1" 2>/dev/null || true; }
ecn() { grep -Ec -- "$2" "$1" 2>/dev/null || true; }

# —— 一波「吞错放水」——
e2e_eq "1-A gate.go TodayRealizedPnl 调用点禁「, _」吞错形态（负锁）" \
	"$(grep -F 'TodayRealizedPnl(' internal/risk/gate.go | grep -c ', _' || true)" "0"
e2e_eq "1-A gate.go 承接 err 的正向调用恰 1 处" \
	"$(ec internal/risk/gate.go 'pnl, err := g.st.TodayRealizedPnl')" "1"
e2e_eq "1-B W1B 锚 config.go 恰 13（09-29 §0929CFG-D1 两条 setter 注释按同口径交叉引用该锚 ⇒ 11→13 同日同步；新增 W1B 腿须再同步本计数）" "$(ecn internal/config/config.go '0926E2E-W1B')" "13"
e2e_eq "1-B W1B 锚 qmt.go 恰 2（同上）" "$(ecn internal/server/qmt.go '0926E2E-W1B')" "2"
e2e_eq "1-C/1-D internal/server 非测试文件「_ = json.NewDecoder」清零（负锁；测试读体豁免）" \
	"$(grep -rl '_ = json.NewDecoder' internal/server --include='*.go' 2>/dev/null | grep -v _test.go | wc -l | tr -d ' ')" "0"
e2e_eq "1-4 GetIndexData 伪造 1500 涨跌家数不得回潮（负锁，按赋值形态）" \
	"$(ec internal/data/market.go 'pCount = 1500')" "0"
e2e_ge "1-4 market.go 真弃权声明锚 ≥1" "1" "$(ecn internal/data/market.go '0926E2E-14')"

# —— 二波「暴露面封堵」——
e2e_ge "2-A SETUP_TOKEN 部署面覆盖文件数 ≥7（service/广州/汉城/注册/备份/verify）" \
	"7" "$(git grep -l SETUP_TOKEN -- deploy dist-guangzhou scripts 2>/dev/null | wc -l | tr -d ' ')"
e2e_eq "2-B SSE query-token 退役截止常量恰 1（2026-10-15 CST 等值串）" \
	"$(ec internal/server/sse.go 'time.Date(2026, 10, 15, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))')" "1"
e2e_eq "2-B 登录响应下发 sse_query_token_expires_at 提示恰 1" \
	"$(ec internal/server/server.go 'sse_query_token_expires_at')" "1"
e2e_ge "2-C 网关密钥文件权限自检锚 gateway.py ≥1" "1" "$(ecn qmt_gateway/gateway.py '0926E2E-W2C')"
e2e_ge "2-D outbox 冻结删除锚 store.py ≥1" "1" "$(ecn qmt_gateway/store.py '0926E2E-W2D')"

# —— 三波「口径裁决」——
e2e_eq "16 OptimizeAutoApply 字段定义不得回潮（行首形态负锁，说明注释豁免）" \
	"$(grep -Ec '^[[:space:]]*OptimizeAutoApply' internal/config/config.go || true)" "0"
e2e_ge "11A confirmedReplay 新鲜度闸函数在位（sell.go ≥1）" "1" "$(ec internal/signalctl/sell.go 'sellConfirmedStale')"
e2e_ge "4B 六闸默认关清单在位（config.go defaultOffGateEntries ≥1）" "1" "$(ec internal/config/config.go 'defaultOffGateEntries')"
e2e_eq "3A 白名单只约束自动信号通道口径声明在位（gate.go 恰 1）" "$(ecn internal/risk/gate.go '0926E2E-3A')" "1"

# —— 四波「体验/卫生」——
e2e_eq "13 前端离线判定阈值等值锁（连续 2 次失败才翻离线，App.jsx 恰 1）" \
	"$(ec web/src/App.jsx 'const OFFLINE_AFTER_FAILS = 2')" "1"
# §0926E2E-13b（09-27 Playwright 双发事故）：StrictMode 双挂载下旧挂载的异步 checkAuth
# 结果不得再拉起轮询（active 守卫三形态各恰 1），且 w13 spec 已改按时间窗设卡。
# 预演读数（09-27 grep -Fc）：'if (ok && active) startPolling()'=1、'active = false'腿=1、
# 'WINDOW_START_MS = 55000'=1。
e2e_eq "13b 挂载轮询 active 守卫调用点恰 1（摘守卫=启动连发双轮询，§13 计数被打满）" \
	"$(ec web/src/App.jsx 'if (ok && active) startPolling()')" "1"
e2e_eq "13b 卸载清理置 active=false 恰 1（守卫的另一半，缺它=只写不用）" \
	"$(ec web/src/App.jsx 'active = false // §0926E2E-13b')" "1"
e2e_eq "13b w13 Playwright 时间窗判据在位（按请求计数会拨到 Dashboard 首发）" \
	"$(ec web/e2e/w13_offline_debounce.spec.mjs 'WINDOW_START_MS = 55000')" "1"
e2e_eq "17c e2e 凭据缺失 test.fail 快锁在位（auth.setup.mjs 恰 1）" \
	"$(ec web/e2e/auth.setup.mjs '§0926E2E-17c：E2E_USER/E2E_PASS 未注入')" "1"
e2e_ge "17a Dashboard 轮询 60s 对齐锚 ≥2（两处高频轮收编）" "2" "$(ecn web/src/pages/Dashboard.jsx '0926E2E-17A')"
e2e_ge "17b fetcher 快照 AtomicWrite 锚 ≥1" "1" "$(ecn internal/data/fetcher.go '0926E2E-17B')"
e2e_ge "17d 成员 admin 入口灰化锚 Quant.jsx ≥1" "1" "$(ecn web/src/pages/Quant.jsx '0926E2E-17d')"

# —— 矩阵补位 MX1/MX2/MX3 ——
e2e_eq "MX1 mock 派发取单腿恰 1" "$(ec cmd/qmt-mock/main.go '"/dispatch/pending"')" "1"
e2e_eq "MX1 mock 派发结算腿恰 1" "$(ec cmd/qmt-mock/main.go '"/dispatch/result"')" "1"
e2e_eq "MX1 mock 操作员注入腿恰 1" "$(ec cmd/qmt-mock/main.go '"/dispatch/enqueue"')" "1"
e2e_eq "MX1 mock 订单闸默认严格（-relax-order-check 缺省 false 恰 1）" \
	"$(ec cmd/qmt-mock/main.go 'flag.Bool("relax-order-check", false')" "1"
e2e_eq "MX1 mock /cancel 临界区直读 activeBroker 防自死锁形态恰 1（b.active() 会二次加锁）" \
	"$(ec cmd/qmt-mock/main.go 'b.activeBroker == "queued"')" "1"
e2e_eq "MX2 nightly apk-smoke job 在位恰 1" "$(ec .github/workflows/nightly-e2e.yml 'apk-smoke:')" "1"
e2e_eq "MX2 内嵌指纹==checkout SHA 等值锁在位恰 1" \
	"$(ec .github/workflows/nightly-e2e.yml 'test "$EMBED" = "$HEAD_SHA"')" "1"
e2e_eq "MX3 周度探测必须走只读腿（persist=false+hotSwap=false 等值恰 1）" \
	"$(ec internal/server/llm_weekly_probe.go 'false /*persist*/, false /*hotSwap*/')" "1"
e2e_eq "MX3 周度例行已接线（cmd/quant/main.go StartWeeklyLLMProbe 恰 1）" \
	"$(ecn cmd/quant/main.go 'StartWeeklyLLMProbe')" "1"

# —— §0927KA 保活日历加固（09-27 部署实录：现网旧版工作日近似推幻觉 target 空转过夜挡部署）——
# 预演读数（09-27 grep -Fc）：部署清单 'scripts/dataload_keepalive.py'=1、
# keepalive 源 '0927KA' 锚=10、'abort early'=1。
e2e_eq "KA 部署清单必须带 dataload_keepalive.py（漏列=§M6/§0927KA 修复永不到现网，09-26 实录本体）" \
	"$(ec scripts/deploy_guangzhou.sh 'scripts/dataload_keepalive.py')" "1"
e2e_ge "KA keepalive 源 §0927KA 加固锚（日历自愈回写+无进展早停）≥8" "8" "$(ecn scripts/dataload_keepalive.py '0927KA')"
e2e_eq "KA 无进展早停日志锚在位恰 1（幻觉 target 最多烧两轮的保险丝）" \
	"$(ec scripts/dataload_keepalive.py 'abort early')" "1"

if [ -z "$E2E_ERRS" ]; then
	echo "ok - §0926E2E 守卫通过（静态锁 36 条：吞错收口 7 + 暴露面 5 + 裁决口径 4 + 体验卫生 8 + 矩阵补位 9 + 保活加固 3）"
else
	echo "--- FAIL: §103 断言不符:${E2E_ERRS}"
	exit 1
fi

# ════════════════════════════════════════════════════════════════════════════
# §104 §0927AUDIT-D3（2026-09-28 修复批）：本地门禁与 CI 的 gofmt 口径对齐。
# 背景：CI 第一道就是 `gofmt -l internal cmd`（ci.yml:36-42），而本门禁 103 段
#   历史全文从不提 gofmt ⇒「本地全绿 / CI 红」漂移。09-27 全栈字节级 UAT 报告 D3
#   锤实现行时点：HEAD 树上 13 个文件被 gofmt 判违规（含 server.go/qmt.go 本体）。
# 本批已把 13 文件全部 gofmt -w 清零；过程中锤到一个工具族新知识：**gofmt 的 doc
#   注释规范化会把相邻两个单引号（SQL 空串字面量 `''`）改写成单个右双引号 ”**，
#   函数体内的普通行注释不受影响——doc 注释里要写 SQL 空串原文时，把字面量挪进
#   函数体注释（见 pnl_offset_test.go / universe_test.go 的留痕写法）。
# 预演读数（09-28）：清理后 `gofmt -l internal cmd` 输出 0 行（先预演后入段纪律）。
# §89 自指锁同口径：管道失败不吞退出码，`|| true` 兜住赋值。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 104 §0927AUDIT-D3 gofmt 与 CI 同段（gofmt -l internal cmd 必须为空）..."
GOFMT_DIRTY=$(gofmt -l internal cmd 2>/dev/null || true)
if [ -n "$GOFMT_DIRTY" ]; then
	echo "--- FAIL: §104 以下文件未过 gofmt（CI 第一道必红，本地绿≠真绿）："
	printf '%s\n' "$GOFMT_DIRTY" | sed 's/^/  /'
	exit 1
fi
echo "ok - §0927AUDIT-D3 守卫通过（gofmt -l 0 文件，与 ci.yml:36-42 同口径）"

# ════════════════════════════════════════════════════════════════════════════
# §105 §0927AUDIT 修复批专项静态锁（2026-09-28 凌晨批：D1/D2/D4/D5 + 独立 UAT 脚本自纠 + 门禁自修）
# 主题：把「审计报告锤实、本批落码」的六处关键口径钉成机器锁，防回潮。
# 预演读数（09-28 00:3x，先预演后入段）：费用腿公式行=1；-509 期望=1；main.go 真实
# os.Exit 调用行=0（6 处全在注释）；sleepOrStop 行=4；mainLoop: 标签=1；testTimeout:
# 60000 行=1；indep 脚本自纠锚=1；” 字符在两个受害测试文件=0/0。
# §89 纪律：计数型 grep 赋值一律 `|| true` 兜住（0 命中时 grep -c 退出码 1，pipefail 下会把
# "全绿" 变 "裸死"）；行为腿先收全文再判红，红项当场可读。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 105 §0927AUDIT D1/D2/D4/D5 修复批专项静态锁 + 独立UAT脚本自纠 + gofmt-”防回潮（2026-09-28；D4 那条在 2026-10-06 波 7 由「main.go 不许出现 os.Exit 字面量」改写成「os.Exit 的行号必须落在 main 外壳内且恰好一条」＋三枚位置反证，成因见 D4 段注释）..."

# —— 行为腿：D1 已实现盈亏扣费回归（含方向锁用例，秒级）——
RP=$(go test -count=1 ./internal/store/ -run 'TestTodayRealizedPnl' 2>&1 || true)
if printf '%s\n' "$RP" | /usr/bin/grep -qE '^(--- FAIL|FAIL)'; then
	echo "--- FAIL: §105 D1 行为腿判红（TodayRealizedPnl 扣费回归），全文如下："
	printf '%s\n' "$RP" | head -40
	exit 1
fi
printf '%s\n' "$RP" | grep -E '^ok' || { echo "--- FAIL: §105 D1 行为腿没跑到（无 ok 行＝用例可能被改名/删除）"; exit 1; }

# —— D1 公式锚：卖出腿费用必须从已实现盈亏里扣（费用不入账＝熔断闸系统性低估亏损）——
grep -q -- "- f.Fee - f.StampTax" internal/store/risk_gates.go \
	|| { echo "--- FAIL: §105 D1 费用腿公式锚丢失（TodayRealizedPnl 回到未扣费口径）"; exit 1; }
grep -q "const want = -509" internal/store/realized_pnl_fee_test.go \
	|| { echo "--- FAIL: §105 D1 含费期望值 -509 锚丢失（方向锁被放宽＝假绿温床）"; exit 1; }

# —— D4 优雅停机锚（判据形态在 2026-10-06 波 7 改写过一次，成因写在下面，别再改回去）——
#   原锁是「cmd/quant/main.go 里不得出现 os.Exit 字面量」，那是把「收尾不被跳过」错抄成「文件里
#   没这个词」：§W7-FATAL 为了消掉启动期三处 log.Fatalf（它们在 defer 链**之外**终结进程），把进程
#   体搬进 run() int，退出码必须由 main 的进程外壳交给 os.Exit——run 里 return 只回到外壳，
#   不交退出码就等于把 fail-fast 悄悄降级成「退出码 0」。于是**合法形态**里也会出现一条 os.Exit，
#   旧锁照字面判红（10-09 门禁 -full 实跑 §105 首红就是这一条）。
#   真判据是位置而不是字面量：os.Exit 只允许出现在 func main() 外壳内、且恰好一条；
#   run()（以及以后任何别的函数）体内一条都不许有——这三条合起来才等价于「旧缺陷不再复活」。
D4_READ=$(awk '/^func main\(\)/{inmain=1} /^func run\(\) int/{inmain=0; inrun=1}
	/^[ \t]*os\.Exit\(/{if(inmain)m++; else o++} END{printf "in_main=%d outside=%d", m+0, o+0}' cmd/quant/main.go)
[ "${D4_READ}" = "in_main=1 outside=0" ] \
	|| { echo "--- FAIL: §105 D4 退出姿势读数漂移（实读=${D4_READ} 应=in_main=1 outside=0：外壳那条 os.Exit 是交出退出码的唯一通路，跑到 0＝fail-fast 被吞成 0，跑到 2＝又长出第二条；outside≥1＝在 run 的 defer 链之外终结进程，正是旧缺陷本体）"; exit 1; }
echo "ok - §105 D4 退出姿势（进程体在 run、退出码由 main 外壳交 os.Exit，实读 ${D4_READ}）"

# D4 反证三枚（§111「等值锁非单向锁」同族）：只断「读数不许漂」不够，必须证明这把尺子在
# 三种坏法下各自翻转；全程在 /tmp 副本上做，仓库文件零改动。
D4W=$(mktemp -d /tmp/d4rev-XXXXXX 2>/dev/null || true)
[ -n "${D4W:-}" ] && [ -d "$D4W" ] || { echo "--- FAIL: §105 D4 反证的临时目录建不出来（建不出来就没有副本树，破坏只能落在真文件上）"; exit 1; }
d4_read() { awk '/^func main\(\)/{inmain=1} /^func run\(\) int/{inmain=0; inrun=1}
	/^[ \t]*os\.Exit\(/{if(inmain)m++; else o++} END{printf "in_main=%d outside=%d", m+0, o+0}' "$1"; }
d4_probe() { # $1=编号 $2=old 整串 $3=new 整串 $4=期望读数 $5=说明
	local id="$1" old="$2" new="$3" want="$4" why="$5" n got
	cp cmd/quant/main.go "$D4W/$id.go" || { echo "--- FAIL: §105 D4 反证 ${id} 取不到副本"; exit 1; }
	n=$(python3 - "$D4W/$id.go" "$old" "$new" <<'PYD4'
import sys
# 整串替换并打印落地数（0＝靶串没命中、>1＝破坏面比本枚射程大，两种都要红，与 §114 mutate.py 同纪律）
p, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(p, encoding="utf-8").read()
n = s.count(old)
if n:
    open(p, "w", encoding="utf-8").write(s.replace(old, new))
print(n)
PYD4
)
	[ "${n:-0}" = "1" ] || { echo "--- FAIL: §105 D4 反证 ${id} 变异落地数=${n}（应恰好 1）：${why}"; rm -rf "$D4W"; exit 1; }
	got=$(d4_read "$D4W/$id.go")
	[ "${got}" = "${want}" ] \
		|| { echo "--- FAIL: §105 D4 反证 ${id} 读数没按预期翻转（破坏后应=${want} 实得=${got}）：${why}"; rm -rf "$D4W"; exit 1; }
	echo "ok - §105 D4 反证 ${id}（${why}）：读数 in_main=1 outside=0 → ${got}"
}
d4_probe R1 $'\tlog.SetFlags(log.LstdFlags | log.Lshortfile)' \
	$'\tlog.SetFlags(log.LstdFlags | log.Lshortfile)\n\tos.Exit(1)' \
	'in_main=1 outside=1' \
	'把 os.Exit 长回进程体里（defer 链还没展开就终结进程＝旧缺陷本体），outside 必须现形'
d4_probe R2 $'\tif code := run(); code != 0 {\n\t\tos.Exit(code)\n\t}' \
	$'\tif code := run(); code != 0 {\n\t\tos.Exit(code)\n\t\tos.Exit(code)\n\t}' \
	'in_main=2 outside=0' \
	'外壳里再长出第二条退出姿势时，等值计数锁必须红（≤1 的单向锁在这里会放过）'
d4_probe R3 $'\t\tos.Exit(code)' '\t\t_ = code' \
	'in_main=0 outside=0' \
	'把外壳那条 os.Exit 摘掉＝启动期 fail-fast 的退出码被静默吞成 0，等值锁的另一半（不许为 0）在这里生效'
rm -rf "$D4W"
SLEEP_STOP=$(grep -c "sleepOrStop" cmd/quant/main.go || true)
[ "$SLEEP_STOP" = "4" ] || { echo "--- FAIL: §105 D4 sleepOrStop 锚计数漂移（got=$SLEEP_STOP 预演=4：定义+注释+两处心跳；信号可能又打不进睡眠）"; exit 1; }
grep -q "mainLoop:" cmd/quant/main.go \
	|| { echo "--- FAIL: §105 D4 mainLoop 标签丢失（退出信号无法跳出外层轮询循环）"; exit 1; }

# —— D2 启动告警锚：敞开窗口显式留痕不得拆（fail-open 语义保留，但必须让运维看得见）——
grep -q "POST /setup 处于无令牌敞开窗口" cmd/quant/main.go \
	|| { echo "--- FAIL: §105 D2 SETUP_TOKEN 敞开窗口启动告警丢失（fail-open 由设计变回设计+隐身）"; exit 1; }

# —— D5 超时收敛锚：60s 对冷 transform 是必要水位，回缩会复现「基建脆弱冒充产品缺陷」——
grep -q "testTimeout: 60000" web/vitest.config.js \
	|| { echo "--- FAIL: §105 D5 vitest testTimeout 回离 60000（冷 transform 39.6s 实录在前）"; exit 1; }

# —— 独立 UAT 脚本自纠锚：构建指纹不一致必须计 FAIL（旧 else 分支 INFO+PASS 结构上永不为红）——
grep -q "build_commit 与 git HEAD 不一致" tools/indep_byte_uat.sh \
	|| { echo "--- FAIL: §105 独立 UAT 指纹对拍又回到永不为绿即通过的假检查"; exit 1; }

# —— §51 门禁自修锚：三文件行为锁必须串行跑（并行冷 transform 挤满核心×2 轮门禁内假红）——
grep -q -- "--no-file-parallelism" scripts/verify_changes.sh \
	|| { echo "--- FAIL: §105 §51 串行调度锚丢失（并行三 jsdom 重组件文件=负载假红温床）"; exit 1; }

# —— gofmt-”防回潮负锁：doc 注释里的 SQL 空串 '' 会被 gofmt 洗成单个右双引号 ”（本批实录炸过
#    pnl_offset_test.go / universe_test.go 三处）；字面量已挪进函数体注释，两文件不得再出现 ”。
#    预演读数=0/0；等值锁而非「≤」——一旦出现即说明有人又把 SQL 原文写回 doc 注释。——
for f in internal/store/pnl_offset_test.go internal/store/universe_test.go; do
	DQ=$(grep -c '”' "$f" || true)
	[ "${DQ:-0}" = "0" ] || { echo "--- FAIL: §105 gofmt ” 腐蚀现身 ${f}（got=$DQ 预演=0——SQL 空串原文别写进 doc 注释，见 §104 说明）"; exit 1; }
done

echo "ok - §0927AUDIT 修复批守卫通过（行为腿 1 组 + 静态锁 10 道 + 负锁 2 枚）"

# ════════════════════════════════════════════════════════════════════════════
# §106 §0929 批专项静态锁（2026-09-29 盘中批：⑩ 成交额量纲 + ⑪ 运维面四条）
# 主题：本批交付的是「校验面/口径面」而不是新功能——最怕的就是**锁自己失效**：
#   ⑩ 换算只在一个调用点接上、另一个数据入口静默漏（历史缺陷：只有主链路换算）；
#   ⑪-1 五个现网执行体不入清单 ⇒ 下次改网关运维面等于改了个没人上传的文件（§P0-B/§ENH-5/§0927KA 三连判例）；
#   ⑪-2 夜间验收脚本的阈值若只写在 bash 侧、没透传进 PS/python，则 CAND_MAX_AGE_DAYS 这类
#        环境变量是惰性的 ⇒ 预览说的是一套、实跑判的是另一套（本批首版就踩了这条，见下面等值锁）；
#   ⑪-4 告警三件套（量规赋值点 / DefaultAlertRules / alert_routing 路由表）漏任一条就是死规则或裸量规；
#   ⑪-5 恢复演练的 sqlite3 能力判定回到「多处 if 静默跳过」，演练会重新变成自证。
# 预演读数（09-29 12:5x，先预演后入段，逐条实测）：main.go 换算调用点=2；dataload amount-check
# 子命令=1；quality.go AmountCaliberFactor=1；cmd/dataload 内「落库口径一致」=0；
# verify_deploy 行首 Probe=25、amount-check=3；夜间脚本 ActiveMax/TaskAgeHours/CandAgeDays 各=4、
# --active-max/--task-age-hours/--cand-age-days 各=3、mode=ro=2、PASS<7 守卫=1、字面 IPv4=0；
# 心跳三规则 alerter/routing 各=1、三个量规的字面赋值点「SetGauge("<键名>"」各=2、
# scoring_loop 接线各=1、library_watch 接线=2；
# restore_drill REQUIRE_SQLITE 默认=1、行首 if command -v sqlite3 判定=1（单点）、落档键=7。
# 锁实现说明：eq106 一律「实际读数 == 预演读数」的等值锁，不用「≥」单向阈值（本仓教训：
# 单向锁等于把口径推向任意远）；计数用 grep -c（按行计），改文案前先重跑预演再改本段数字。
# 中文引号说明：本段所有断言消息里的代码形态用「」，不放真反引号也不放 ASCII 双引号
# （§102 那次命令替换自爆的同族雷）。
# 反证记录（09-29 13:1x，/tmp/sec106_counterproof.sh 在镜像里逐条破坏，主仓零改动）：
# 28 枚破坏全部 RED-AS-EXPECTED、NOOP=0、MIRROR_LEFTOVERS=0。首轮跑出 6 条 STAYED-GREEN，
# 根因全部是「标识符锁被子串命中」——把 NormalizeTushareAmount / library_stale_days /
# TestNormalizeThenLoadThenProbe 改名成 *_v2 后，裸标识符模式照样匹配。故本段凡涉及函数名、
# 规则名、量规键名、用例名，一律换成带引号/带字段名的锚（Name: "x"、"x":）或整词锁 word106，
# 并补钉 4 条「-run 正则下改名即静默不跑」的反证用例名锁（静态锁 54→58）。
# 二次反证记录（09-29 14:0x，§69 全量门禁判红实录逼出的口径修正）：本批首版把三个量规键名立成
# const 别名再 SetGauge(别名,)，§106 的键名等值锁照样绿，而通用守卫 §DEADGAUGE 按字面量扫赋值点，
# 三条心跳当场判「有规则无赋值点、永不触发」⇒ 键名锁换成与守卫同形的「SetGauge("<键名>"」等值锁
# （各=2 行：写 0 收案 + 写真实读数），并补 3 枚别名ban负锁（静态锁 58→61、负锁 2→5 枚）。
# 教训落档：锁的形态必须与它要保护的判据同形，否则锁绿着放死规则过去。
# 同日二次收口（09-29 14:3x，全量门禁 D 轮在 §88 判红的实录）：本批给 verify_deploy 加了第 30 探针
# 的 INFO 恒回显腿，而 §88 的「INFO 观测行数」计数锁仍写死 5 ⇒ 门禁当场判红（**这是这条锁应该做的事**，
# 红项指向"新增观测腿没同步计数锁"）。现按本仓纪律同日同步：§88 计数 5→6 并把六条腿逐名枚举进 FAIL
# 文案；§106 补一枚 INFO|amount_scale_readout 在位锁（与 §101 的 cal_readout / limitdown_readout 同姿势，
# 摘掉回显腿＝量纲只在红的时候才留痕，观察面退回自证），静态锁 61→62。
# 同日三次扩段（09-29 14:4x，§99 判红后补）：全量门禁在 §99「admin.go 审计行数＝8」判红——本批 ① D1
# 稀疏 merge 写通道加了 2 行特权变更留痕（config_d1_merge / config_snapshot_failed）⇒ 计数 8→10 同日同步，
# FAIL 文案写清两行来源。另补测试资产登记锁 4 道（Playwright 三条 POS 腿 + 共用拦截 helper + 两个审计动作名），
# 静态锁 62→66；预演读数：test('POS- =3、interceptHoldingsWrite( =4、两个动作名各=1。
# §W7-D 同日同步（2026-10-09 波 7）：第 30 探针与夜间 scale 腿各扩出 **ths_daily** 一张表
# （amount-check --table ths_daily），verify_deploy 的 amount-check 计数 3→4（注释 2 + 实调用 2），
# 上面那行等值锁按新数改；两张表合并成**一腿一 PASS**，所以本段的 `^Probe `=26、§88 的 INFO 观测行=7、
# 夜间 `PASS" -lt 7` 与七腿齐备锁全部不改——改的是判据覆盖面，不是判数；判数若跟着动，
# 就等于把"新增第 8 条腿"这件没发生的事写进半态守卫。ths_daily 自身的静态锁与行为腿在 §114。
# 三次扩段反证实测（/tmp/cp106/run_cp_reg.py，镜像破坏、主仓零改动、LEFTOVERS=0）：首轮 6 枚破坏里
# R2「helper 整体改名 interceptHoldingsWrite→interceptHoldingsWriteV2」**STAYED-GREEN**——裸标识符模式
# 被改名后的子串命中，与本轮 §106 首版标识符锁同一族雷（本仓教训第 N 次复现）。故该锁换成带调用
# 形态的「interceptHoldingsWrite(」（定义行与三条调用行都含左括号，改名加后缀即 0 命中），
# 换锁后 R2 单独把 helper 改名 ⇒ 等值锁 64 判红。其余 R1/R3/R4/R5/R6 全部 RED-AS-EXPECTED，NOOP=0。
# 反证归属纪律同步落档：harness 的变异基线必须从主仓真值读，不能读镜像——首轮 R2 撞 R1 的红、
# R4 撞 R3 的红，就是这个串台把「上一条破坏的残留」冒充成「本条锁咬住了」。
# 二次反证实测（/tmp/cp106/run_counterproof.py + A' 组，镜像破坏、主仓零改动、LEFTOVERS=0）：
#   B 组（摘掉「写 0 收案」赋值点，等值锁 2→1）三枚全 RED-AS-EXPECTED；
#   A 组（键名整体改回 const 别名＋赋值点用别名）三枚先撞赋值点等值锁判红，且同镜像里量到
#     「旧键名字符串锁」的读数仍＝预演值 2＝**旧锁对这一破坏完全失明**（这条数字就是换锁形的证据）；
#   A' 组（只追加一条未使用的别名声明，赋值点字面量仍=2）⇒ 等值锁保持绿、三枚 ban 负锁
#     各自单独判红（负锁 42/43/44），证明负锁自身在咬而不是搭等值锁的红。NOOP=0。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 106 §0929 数据量纲 ⑩ + 运维面 ⑪ 专项静态锁与行为腿（2026-09-29 批）..."

CNT106=0
# has106：存在性锁（模式必须出现在文件里）。
has106() {
	CNT106=$((CNT106 + 1))
	grep -q -- "$2" "$1" || { echo "--- FAIL: §106 锁 ${CNT106}（${3}）：${1} 缺「${2}」"; exit 1; }
}
# eq106：等值锁（grep -c 实际读数必须逐字等于预演读数）。
eq106() {
	CNT106=$((CNT106 + 1))
	local got
	got=$(grep -c -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §106 等值锁 ${CNT106}（${4}）：${1} 模式「${2}」got=${got:-0} 预演=$3"; exit 1; }
}
# absent106：负锁（该形态在文件里的行数必须为 0＝回归即红）。
absent106() {
	CNT106=$((CNT106 + 1))
	local got
	got=$(grep -c -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "0" ] || { echo "--- FAIL: §106 负锁 ${CNT106}（${3}）：${1} 又出现「${2}」got=${got}"; exit 1; }
}
# word106：标识符存在锁（整词匹配，且必须恰好 1 行）。为什么不用 has106：反证实测「把函数改名成
# XXX_v2」时子串仍然命中，存在性锁会放过去（STAYED-GREEN 实录 6 条）。整词 + 计数等值才是锁。
word106() {
	CNT106=$((CNT106 + 1))
	local got
	got=$(grep -cw -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "1" ] || { echo "--- FAIL: §106 整词锁 ${CNT106}（${3}）：${1} 形态「${2}」got=${got:-0} 预演=1"; exit 1; }
}

# ── ⑩ 成交额量纲：写侧换算调用点、抽检子命令、quality 自适应、幻觉文案不得复活 ──
eq106 internal/data/amountscale.go 'func NormalizeTushareAmount(table string' 1 '⑩ 归一函数签名在位且只有一处定义'
eq106 cmd/dataload/main.go 'data\.NormalizeTushareAmount(' 2 '⑩ 写侧换算调用点数（index_daily 腿 + 通用表腿各一处，摘掉任一条＝那入口回到千元落库）'
has106 cmd/dataload/amount_check.go '"amount-check"' '⑩ 只读抽检子命令在位'
eq106 internal/store/quality.go 'd\.AmountCaliberFactor(' 1 '⑩ 筛池按量纲自适应的调用点在位（摘掉＝池子按错量纲筛）'
absent106 cmd/dataload/main.go '落库口径一致' '⑩ 旧「落库口径一致」幻觉文案不得复活（该注释断言过一件没发生的事）'
word106 cmd/dataload/amount_scale_crosscheck_test.go 'func TestNormalizeThenLoadThenProbe' '⑩ 贯通用例在位（-run 正则会「匹配不到也算 ok」，用例名必须逐名钉住）'
word106 internal/store/amount_scale_probe_test.go 'func TestProbeDailyAmountScaleMixed' '⑩ 混源判红用例在位'

# ── ⑪-1 部署清单：五个现网执行体必须逐名入清单（漏一个＝改了个没人上传的文件）──
for f in outbox_admin.py register_service.ps1 all_service_watchdog.ps1 daily_ops_check.ps1 enable_ensure.ps1; do
	has106 scripts/deploy_guangzhou.sh "$f" "⑪-1 部署清单含 ${f}"
done
eq106 scripts/verify_deploy_guangzhou.sh '第 29 探针' 5 '⑪-1 第 29 探针五处同源（头部清单/可调项说明/变量定义/param 注释/正文段）'
eq106 scripts/verify_deploy_guangzhou.sh '第 30 探针' 3 '⑩ 第 30 探针三处同源（头部清单/param 注释/正文段）'
eq106 scripts/verify_deploy_guangzhou.sh '^Probe ' 26 '⑩⑪＋§107 后 PS 侧行首探针语句数（第 31 探针快照 ACL 入列 25→26；新增探针须先重跑预演再改这里的数）'
eq106 scripts/verify_deploy_guangzhou.sh 'amount-check' 4 '⑩ 现网只读抽检调用链（注释 2 处 + 实调用 2 处：§W7-D 起 daily 腿与 ths_daily 腿各一条，2026-10-09 同步 3→4）'
# INFO 恒回显在位锁：绿也要看得到抽检读数（摘掉这条＝量纲只在红的时候才留痕，观察面退回自证）。
# 与 §101 的 INFO|cal_readout / INFO|limitdown_readout 两把同姿势；摘掉即 §88 计数锁同时判红。
has106 scripts/verify_deploy_guangzhou.sh 'INFO|amount_scale_readout' '⑩ 第 30 探针 INFO 恒回显腿在位'
has106 scripts/verify_deploy_guangzhou.sh 'daily_ops_check' '⑪-1 第 29 探针逐文件标记锁在位'

# ── ⑪-2 夜间验收：七腿齐备、只读通道、半态守卫、阈值真透传 ──
VNG=scripts/verify_nightly_guangzhou.sh
has106 "$VNG" 'Probe "svc:' '⑪-2 腿 1 服务态'
has106 "$VNG" 'Probe "hb:' '⑪-2 腿 2 调度心跳'
has106 "$VNG" 'Probe "mem:' '⑪-2 腿 3 进程内存'
has106 "$VNG" 'Probe "scale:' '⑪-2 腿 4 量纲抽检'
has106 "$VNG" 'queue:research task queue' '⑪-2 腿 5 队列'
has106 "$VNG" 'cand:nightly research produced' '⑪-2 腿 6 候选产出'
has106 "$VNG" 'fina:financial indicator table loaded' '⑪-2 腿 7 财务覆盖'
has106 "$VNG" 'mode=ro' '⑪-2 只读通道（file:…?mode=ro，演练/校验绝不写现网库）'
eq106 "$VNG" 'PASS" -lt 7' 1 '⑪-2 半态守卫（腿数不足 7 必红，绝不把「没跑出来」当成功）'
# 阈值透传三连：bash 侧变量 → PS param 声明 → python 形参。少任何一环，该环境变量就是惰性的
# （本批首版就是这样：预览里带阈值、实跑里没带，判据悄悄按默认值走）。
for th in ActiveMax TaskAgeHours CandAgeDays; do
	eq106 "$VNG" "$th" 4 "⑪-2 阈值 ${th} 四处同源（bash 变量行/PS 调用行/param 声明/兜底默认）"
done
for py in active-max task-age-hours cand-age-days; do
	CNT106=$((CNT106 + 1))
	got=$(grep -c -- "--$py" "$VNG" 2>/dev/null || true)
	[ "${got:-0}" = "3" ] || { echo "--- FAIL: §106 等值锁 ${CNT106}（⑪-2 python 形参 --${py} 三处同源：RUN_PY 预览/PS 实调用/argparse）got=${got:-0} 预演=3＝阈值没真透传"; exit 1; }
done

# ── ⑪-4 心跳三件套：量规键名 / 规则 / 路由 / 接线，四者缺一即死规则或裸量规 ──
# 全部用「Name: "x"」「"x":」这类带引号/带字段名的锚：反证实测过只写裸标识符时，
# 把规则改名成 x_v2 仍是子串命中，等值锁会放过去（同批 STAYED-GREEN 6 条的根因）。
for r in signal_zero_in_session realized_pnl_zero_with_sells library_stale_days; do
	eq106 internal/metrics/alerter.go "Name: \"$r\"" 1 "⑪-4 规则 ${r} 在 DefaultAlertRules 注册（恰一条）"
	eq106 internal/metrics/alert_routing.go "\"$r\":" 1 "⑪-4 规则 ${r} 在路由表有条目（缺＝走默认路由，owner 改路由时会被漏掉）"
done
# 量规**赋值点**用 §69 通用守卫同一字面形态（SetGauge("<键名>"）逐文件数两条：0 写 0、1 写真实读数。
# 为什么不是"键名字符串在文件里出现一次"：09-29 全量门禁 §69 判红实录——本批最初把三个键名立成
# const 别名再 SetGauge(别名,)，§106 这条键名锁照样绿（别名声明那行命中），而 §DEADGAUGE 的通用
# 守卫按字面量扫赋值点，三条心跳当场被判"有规则无赋值点、永不触发"。**锁的形态必须和它要保护的
# 判据同形**，否则锁绿着把死规则放过去。别名同时用负锁钉死，不许回潜。
eq106 internal/engine/heartbeat.go 'SetGauge("signal_zero_session_sec"' 2 '⑪-4 零信号量规赋值点两处（收案写 0 + 计时写真实值）'
eq106 internal/trading/heartbeat.go 'SetGauge("realized_pnl_zero_with_sells"' 2 '⑪-4 已实现恒 0 量规赋值点两处'
eq106 internal/server/library_staleness.go 'SetGauge("library_stale_days"' 2 '⑪-4 库陈旧量规赋值点两处'
absent106 internal/engine/heartbeat.go 'signalHeartbeatGaugeName' '⑪-4 键名 const 别名不得复活（别名＝§69 守卫失明）'
absent106 internal/trading/heartbeat.go 'realizedPnlHeartbeatGaugeName' '⑪-4 同上（已实现心跳）'
absent106 internal/server/library_staleness.go 'libraryStalenessGaugeName' '⑪-4 同上（库陈旧）'
eq106 internal/engine/scoring_loop.go 'feedSignalHeartbeatGauge' 1 '⑪-4 零信号心跳接线（周期喂数点）'
eq106 internal/engine/scoring_loop.go 'RefreshRealizedPnlHeartbeat' 1 '⑪-4 已实现心跳接线（只在实盘态喂）'
eq106 internal/server/library_watch.go 'refreshLibraryStalenessGauge' 2 '⑪-4 库陈旧接线（启动一次+每轮一次）'
# 反证用例名锁（整词等值）：行为腿的 -run 正则会「匹配不到也算 ok」，用例名必须在源文件逐名在位。
word106 internal/engine/heartbeat_test.go 'func TestFeedSignalHeartbeatZeroAndPinnedPair' '⑪-4 零信号成对反证用例在位'
word106 internal/engine/heartbeat_test.go 'func TestFeedSignalHeartbeatNonLiveDoesNotWrite' '⑪-4 非实盘不落笔（反掩蔽）用例在位'
word106 internal/trading/heartbeat_test.go 'func TestRefreshRealizedPnlHeartbeatFeedsGaugePair' '⑪-4 已实现心跳成对反证用例在位'
word106 internal/server/library_staleness_test.go 'func TestRefreshLibraryStalenessGaugeEndToEnd' '⑪-4 库陈旧端到端用例在位'
word106 internal/store/realized_pnl_heartbeat_test.go 'func TestCountSellFillsDayAndUserBoundary' '⑪-4 卖出笔数三道边界用例在位'
# 反证腿名逐条钉：-run 用「A|B|C」正则，改名后其余用例照样跑、包照样回 ok，
# 被摘掉的恰好是那条反证（本批反证实测 STAYED-GREEN 的根因）。
word106 internal/trading/heartbeat_test.go 'func TestRefreshRealizedPnlHeartbeatNonLiveDoesNotWrite' '⑪-4 实盘心跳非实盘不落笔用例在位'
word106 internal/engine/heartbeat_test.go 'func TestSignalHeartbeatRuleRegisteredAndKeyAligned' '⑪-4 零信号三件套对齐用例在位'
word106 internal/trading/heartbeat_test.go 'func TestRealizedPnlHeartbeatRuleKeyAligned' '⑪-4 已实现心跳三件套对齐用例在位'
word106 internal/server/library_staleness_test.go 'func TestLibraryStaleRuleRegisteredAndRouted' '⑪-4 库陈旧规则与路由归属用例在位'

# ── ⑪-5 恢复演练：能力判定单点 fail-closed，不得回到「多处 if 静默跳过然后全绿」──
# 负锁：现网入口只认 GZ_IP 环境变量，脚本里不许内嵌字面公网 IP（本仓纪律：部署/校验命令与
# 文档都不写字面生产 IP，避免它随聊天与终端历史外溢）。
CNT106=$((CNT106 + 1))
IPHITS=$(grep -Ec '[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}' "$VNG" || true)
[ "${IPHITS:-0}" = "0" ] || { echo "--- FAIL: §106 负锁 ${CNT106}（⑪-2 夜间验收脚本内嵌字面 IP，入口应只有 GZ_IP）got=${IPHITS}"; exit 1; }

RD=scripts/restore_drill.sh
eq106 "$RD" 'REQUIRE_SQLITE:-1' 1 '⑪-5 缺 sqlite3 默认判红（显式 REQUIRE_SQLITE=0 才允许跳过）'
eq106 "$RD" '^[[:space:]]*if command -v sqlite3 >' 1 '⑪-5 sqlite3 能力判定单点化（回到 3 处＝演练又变自证）'
has106 "$RD" '查询失败（表不存在或库不可读' '⑪-5 账本四表可读硬判红（旧版只 echo ERR 仍全绿）'
has106 "$RD" 'DRILL_RECORD' '⑪-5 演练读数留档腿（失败也留一行，定时腿才看得见红过）'

# ── 测试资产登记锁（09-29 本批自己的自动化腿入锁；④ 前端持仓写 + ① D1 写通道留痕）──
# 为什么单独钉这一组：本轮全量门禁两次判红（§88 INFO 观测行数、§99 admin 审计行数）根因是同一族
# ——「新增一条腿，计数锁没跟着同步」。而**测试腿**静默消失比观测腿更常见：改个名、注释掉、
# 被 -run 正则吃不到，用例数都会变少而没人红。所以这里钉的是"腿的数量与形状"，不是"腿绿不绿"。
# 三条 Playwright 浏览器腿按 test('POS- 前缀等值锁 3；共用拦截 helper 恰 4 行（定义 1 + 三条腿各调 1），
# 谁把某条腿改成真写现网库（去掉拦截），计数立刻掉到 3 当场判红——零写入纪律由此变成可机检的约束。
eq106 web/e2e/uat_full.spec.mjs "test('POS-" 3 '④ 持仓整表写浏览器腿三条在位（载荷腿/失败可见腿/重试去重腿）'
eq106 web/e2e/uat_full.spec.mjs 'interceptHoldingsWrite(' 4 '④ 三条腿共用零写入拦截 helper（定义 1 + 调用 3）'
# ① D1 写通道的两行审计**动作名**逐名钉住：§99 那条只数总行数（8→10），动作名被改掉＝留痕语义丢了
# 而计数照旧，事后查"谁改了配置"就查不到那本账；故按带引号的字面量各钉一枚。
eq106 internal/server/admin.go '"config_d1_merge"' 1 '① D1 稀疏 merge 变更留痕动作名在位（改名＝审计账断腿）'
eq106 internal/server/admin.go '"config_snapshot_failed"' 1 '① D1 快照失败留痕动作名在位（快照没落成也要有痕）'

echo "ok - §106 静态锁 ${CNT106} 道通过（含负锁 5 枚 + 阈值透传等值锁 6 道 + 测试资产登记锁 4 道）"

# ── 行为腿：五组用例 + 两条脚本离线自证（先收全文再判红，红项当场可读）──
leg106() { # $1=说明 $2=包 $3=-run 正则
	local out
	out=$(go test -count=1 "$2" -run "$3" 2>&1 || true)
	if printf '%s\n' "$out" | /usr/bin/grep -qE '^(--- FAIL|FAIL)'; then
		echo "--- FAIL: §106 行为腿判红（${1}），全文如下："
		printf '%s\n' "$out" | head -40
		exit 1
	fi
	printf '%s\n' "$out" | /usr/bin/grep -qE '^ok' || {
		echo "--- FAIL: §106 行为腿没跑到（$1 无 ok 行＝包编译失败或用例被删）"
		exit 1
	}
	echo "ok - §106 行为腿 $1"
}
# 三件套等值闸：路由表条目数 == DefaultAlertRules 条数（本批 15→18，§107 再 +1＝19，两侧同批动）
# §W7-COUNT-SYNC（2026-10-09 波 7）：现**实数 20**（必推 12 / 日汇总 8，两个集合名字逐一对齐）。
#   多出来的那一条是波 5 §P2-E 的 settlement_not_verified——它入表时只加了路由条目和规则注册，
#   没动这里的文案，于是「（19 条）」在本批之前一直是**过时读数**（同 §0929「加腿没同步计数」家族）。
#   取向：文案里的数字不是判据（判据是 TestRoutingCoversAllDefaultRules 里的等值比较，它按
#   len(DefaultAlertRules()) 派生，加一条规则不需要改测试），所以这里只做**说明同步**；
#   真正的锁面是 §114 里那枚「说明里的条数必须等于源码派生条数」的等值锁——下次再加规则却忘了
#   同步文案时，红的是这枚锁而不是读者的眼睛。
leg106 'metrics 路由覆盖全规则（20 条：必推 12 / 日汇总 8）' ./internal/metrics/ 'TestRoutingCoversAllDefaultRules'
leg106 'engine 零信号心跳（判据/成对反证/非实盘不落笔/接线）' ./internal/engine/ \
	'TestSignalHeartbeatAgePredicate|TestFeedSignalHeartbeatZeroAndPinnedPair|TestFeedSignalHeartbeatNonLiveDoesNotWrite|TestSignalHeartbeatRuleRegisteredAndKeyAligned|TestRefreshStalenessFeedsSignalHeartbeat'
leg106 'trading 已实现盈亏心跳（配对/孤儿卖出/非实盘不落笔/键名对齐）' ./internal/trading/ \
	'TestRealizedPnlHeartbeatMatchedTriple|TestRefreshRealizedPnlHeartbeatFeedsGaugePair|TestRefreshRealizedPnlHeartbeatNonLiveDoesNotWrite|TestRealizedPnlHeartbeatRuleKeyAligned'
leg106 'store 卖出笔数口径（日界+账号+方向三道边界/容差）' ./internal/store/ \
	'TestCountSellFillsDayAndUserBoundary|TestRealizedPnlHeartbeatToleranceBoundary'
leg106 'server 战法库陈旧（纯函数/端到端/规则与路由归属）' ./internal/server/ \
	'TestLibraryStalenessDaysPredicate|TestRefreshLibraryStalenessGaugeEndToEnd|TestLibraryStaleRuleRegisteredAndRouted'
# ⑩ 量纲：写侧归一 + 链路贯通（tushare 形态经归一后落库真值）+ 现网抽检子命令
leg106 'data 成交额量纲归一（含白名单/坏单元格不误 0）' ./internal/data/ 'TestNormalizeTushareAmount'
leg106 'dataload 量纲交叉核对（常量单源 + 归一→InsertRows→抽检读数一致）' ./cmd/dataload/ \
	'TestAmountScaleConstantsAgree|TestNormalizeThenLoadThenProbe'
leg106 'store 量纲抽检与筛池自适应' ./internal/store/ \
	'TestTushareAmountScaleMatchesStoreConst|TestProbeDailyAmountScale|TestAmountCaliberFactor|TestScreenedCodesAmountCaliber'

# 夜间脚本离线自证：语法 + -Preview 必须零 SSH（预览与实跑同一条命令串，缺一环就是假绿温床）
bash -n scripts/verify_nightly_guangzhou.sh || { echo "--- FAIL: §106 夜间验收脚本语法不过"; exit 1; }
PV=$(GZ_IP=127.0.0.1 ./scripts/verify_nightly_guangzhou.sh -Preview 2>&1 || true)
printf '%s\n' "$PV" | grep -q -- '-ActiveMax' \
	|| { echo "--- FAIL: §106 夜间验收 -Preview 未透传阈值（预览里看不到阈值＝实跑也带不上）"; exit 1; }
printf '%s\n' "$PV" | grep -q 'C:/var/lib/quant-trading-v2' \
	|| { echo "--- FAIL: §106 夜间验收 -Preview 的 -DataDir 指错（应指数据目录而非部署目录——心跳文件与库都在数据目录）"; exit 1; }
bash -n scripts/restore_drill.sh || { echo "--- FAIL: §106 恢复演练脚本语法不过"; exit 1; }
echo "ok - §106 行为腿 8 组 + 离线自证 4 条通过"

# ════════════════════════════════════════════════════════════════════════════
# §107 §0929 晚批：恢复演练**首次挂调度真跑**锤实的三条缺陷（DRILL-A/B/C）+ 两条
#     「有脚本无调度」收编（演练本体 / 夜间验收）+ 休市日增量心跳 + 快照目录权限收敛
#
# 这一段的主题不是"新功能没锁"，而是**三条写得看起来很完整的判据，一次都没真跑过**：
#   DRILL-A（读法坏）：verify_restore.sh 用 restic snapshots --json --last 1 取最新快照。
#     本机 restic 0.19.1 把 --last 判废弃、并把它后面的 "1" 当成**快照 ID 前缀**去过滤
#     ⇒ 恒返回空数组且 rc=0 ⇒ 演练永远报"仓库里没有快照"。修法是不过滤、按 time 自己排序，
#     并且**不赌 --latest**（那只在更新版本有，两个都在外部命令上赌版本＝下次升级再坏一次）。
#   DRILL-B（顺序坏）：backup_snapshot.ps1 原先把 SNAPSHOT_OK 写在 restic backup **之后**
#     ⇒ 每一份快照装的都是**上一夜**的标记 ⇒ 异地那份的自我描述恒定晚一个世代，
#     新鲜度判据在完全健康的备份链上也必然超龄（09-29 23:45 实跑读数 age=66h > 54h 就是它）。
#     同批把阈值按布局分流（目录模式 30h / restic 模式 54h，restic 多容忍一个标记世代），
#     并给 Mac 拉取腿加第二个时点 10:30（广州实际收工在 05:20~08:19 之间浮动，07:00 那次常赶上）。
#   DRILL-C（断言坏）：restore_drill.sh 要求"每个账号目录都有 paper.json"。现网实测是
#     4 个账号目录、只有主账号有 paper.json、另有两个是空目录——paper.json 是"这个账号做过
#     纸面交易"的**结果**，不是"账号存在"的凭证。重写为四条各防一件事：递归文件数>0、
#     与 SNAPSHOT_OK.accounts_files **等值**、至少一个 paper.json、其余逐账号打 INFO。
#   外加本批新装的两条定时腿（演练/夜间验收）与"死调度探测器"（record_freshness 单实现两消费者）。
#   锁面预演时又锤出一条**前提缺陷**（DRILL-D，见下面别名那组锁）：ssh -G 对未配置的别名
#   不会失败，它把别名本身当 hostname 原样回显 ⇒ "派生不出主机名就判红"恒不触发。
#
# 预演读数（09-29 23:5x ~ 09-30 00:1x，逐条实测后才入段）：
#   verify_restore.sh 执行行 --last=0、snapshots --json 取法=1、按 time 排序=2、short_snapshot_id=2；
#   backup_snapshot.ps1 SNAPSHOT_OK 落盘=2、成功标记行 284 < restic backup 行 286；
#   阈值三处：30/54 分流=1、透传给演练=1、演练默认 30=1；
#   com.quant.backup.plist Hour=2（7/10 各 1）、安装器 HOUR_LINES 锁=3；
#   restore_drill.sh accounts_files=5、PAPER_TOTAL=4、旧 MISSING 形态=0；
#   restic_pull_backup.sh record_freshness 定义=1 调用=3、三个 *_MAX_AGE_DAYS 各=1（§MAC-WATCHDOG 起多一位看门狗消费者）；
#   run_nightly_verify.sh alias_declared 定义=1、预览腿=1、实跑腿=1、两条判红文案各=1；
#   心跳四件套：规则名=1、路由=1、SetGauge 字面赋值点=2、接线=1、const 别名=0；
#   deploy_guangzhou.sh harden_snapshot_acl=2（BOM 归一 + 上传清单，无 -File 执行行）、
#   verify_deploy 第 31 探针=6、INFO|snapshot_acl_readout=1、加固器 Protect-Dir/Protect-Pass 各=2、
#   ACL_RESULT ok=true=2 / ok=false=1、[switch]$Apply=1；
#   口令脚本 PASS_VALUE=5、执行态 chmod 600=2（另有 1 处命中在说明注释里，故等值锁锚行首并配
#   umask 077=1 那枚独立锁；上一版把注释也算进"三处收口"，反证时才暴露：计数里混注释的等值锁
#   删掉真 chmod 仍能靠注释凑数，且文案谎称"含目录收口"而目录侧其实是 umask）、echo 口令值=0；
#   deploy/mac 全部 .sh/.plist 的字面 IPv4=0；plist 模板派生数=3。
# 逐枚镜像反证（09-30 00:2x~00:5x，harness=/tmp/cp107/run_counterproof.py，主仓零改动）：
#   **57 枚破坏全部 RED 且 FAIL 编号与本枚指定的锁编号逐一对上（NOT-AS-EXPECTED=0，跑完复位自检 GREEN）**
#   ——覆盖静态锁 1~55 全面（含 19 的两形态：安装器改名 / plist 指到仓里没有的被调脚本；
#   55 的两形态：glob 失效使待扫清单=1 / 调度链塞字面 IP）。反证过程又锤出三枚 harness 自身的雷，
#   记下来是因为它们与本仓的假绿形态同源，下一批写 harness 时一定会再踩：
#     ① **按文件清单复原镜像 ≠ 复位**：v1 用「reset_file 逐文件从主仓 cp 回来」清场，可改名/挪走类
#        破坏（把三个 plist 改成 *.moved、把安装器改名）根本不在清单的覆盖面上，残留一路冒充后面
#        20 枚的归属（全报成"派生正锁 18 咬住了"，实际是上一枚的残渣）。v2 改成**每枚之前整体重建
#        镜像**，并在最后跑一次复位自检——复位不自证绿，这一轮结果就作废。
#     ② **首红即退 ⇒ 破坏必须挑"本枚独有"的形态**：往 deploy 链里追加一条 `-File …harden_snapshot_acl`
#        确实该红，但它先撞在"两处同源"计数锁（40）上，归属就错了；同样把口令脚本的 chmod 从注释里
#        改红也算不到执行态那两枚上。改成**整行替换 / 锚在行首**，让前一枚保持绿。
#     ③ **改名式破坏若保留原锚前缀＝恒绿**：`dimNaMode`→`dimNaModeV2` 在子串计数的等值锁下毫无变化
#        （这一枚第一轮就是 STAYED-GREEN）；`snapshots --json`→`snapshots --json=1` 同形。判据锁的
#        锚是"连续子串"，破坏也必须打断这段连续子串才叫反证（与 §标识符锁被子串命中 同族）。
#     ④ 顺带把一枚**锁面自身**的缺陷改了：`chmod 600` 等值锁原写 3 处，第三处命中其实是文件头注释，
#        而文案谎称"含目录收口"（目录侧其实是 `umask 077`）。改为行首锚 2 处 + `umask 077` 独立一枚，
#        两段各防一件事；这属于"计数锚里混注释"的通用形态，别处新增计数锁前先确认命中行全是执行态。
#   还有两枚是**破坏方自己写错**（不是锁失明）：plist 时点块定位一开始按"文件里第一次出现
#   StartCalendarInterval"找，命中的是文件头注释 ⇒ 把 ProgramArguments 数组当成了时点数组，恒找不到
#   Hour=10 那块（反证 harness 与门禁共享同一个"注释混进锚"的雷）。
# 同步项（本批把「加一条腿必须同日同步计数锁」这条纪律又走了一遍，三处）：
#   §106 行首 Probe 25→26（第 31 探针入列）、§106 行为腿文案「路由覆盖全规则（18 条）」→19 条
#   （休市日心跳是第 19 条规则，两侧同批动）、§88 INFO 观测行 6→7（快照 ACL 读数腿）。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 107 §0929 恢复演练三条真缺陷 + 定时腿收编 + 休市日心跳 + 快照权限 静态锁与行为腿..."

CNT107=0
has107() {
	CNT107=$((CNT107 + 1))
	grep -q -- "$2" "$1" || { echo "--- FAIL: §107 锁 ${CNT107}（${3}）：${1} 缺「${2}」"; exit 1; }
}
eq107() {
	CNT107=$((CNT107 + 1))
	local got
	got=$(grep -c -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §107 等值锁 ${CNT107}（${4}）：${1} 模式「${2}」got=${got:-0} 预演=$3"; exit 1; }
}
absent107() {
	CNT107=$((CNT107 + 1))
	local got
	got=$(grep -c -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "0" ] || { echo "--- FAIL: §107 负锁 ${CNT107}（${3}）：${1} 又出现「${2}」got=${got}"; exit 1; }
}
# absent_code107：只在**执行行**上判红的负锁。为什么不能沿用 absent107：本批 --last 这一条
# 恰恰要求注释里**保留**踩坑原文（含 restic 的报错行），否则下一个人"顺手加回来"时没有证据。
# 说明注释命中负锁＝误伤（本仓教训），所以先剥掉以 # 开头的行再计数。
absent_code107() {
	CNT107=$((CNT107 + 1))
	local got
	got=$(grep -v '^[[:space:]]*#' "$1" 2>/dev/null | grep -c -- "$2" 2>/dev/null || true)
	[ "${got:-0}" = "0" ] || { echo "--- FAIL: §107 执行行负锁 ${CNT107}（${3}）：${1} 的非注释行里出现「${2}」got=${got}（注释里保留成因是允许的，执行行不行）"; exit 1; }
}
# line_before107：先后顺序锁。本批 DRILL-B 的整个成因就是"两行语句的相对顺序"，
# 用存在性锁（两段都在文件里）对此完全失明——把它们换个位置照样全绿。
line_before107() { # $1=文件 $2=应在前的模式 $3=应在后的模式 $4=说明
	CNT107=$((CNT107 + 1))
	local a b
	a=$(grep -n -- "$2" "$1" 2>/dev/null | head -1 | cut -d: -f1 || true)
	b=$(grep -n -- "$3" "$1" 2>/dev/null | head -1 | cut -d: -f1 || true)
	{ [ -n "$a" ] && [ -n "$b" ]; } || { echo "--- FAIL: §107 先后锁 ${CNT107}（${4}）：${1} 里两段的锚点没都找到 first=${a:-?} second=${b:-?}"; exit 1; }
	[ "$a" -lt "$b" ] || { echo "--- FAIL: §107 先后锁 ${CNT107}（${4}）：${1} 里「${2}」在第 ${a} 行、已不早于「${3}」第 ${b} 行（顺序一倒，每份快照装的标记就都是上一夜的）"; exit 1; }
}

VR=deploy/mac/verify_restore.sh
RD=scripts/restore_drill.sh
BS=deploy/qmt-win/backup_snapshot.ps1
PB=deploy/mac/com.quant.backup.plist
PN=deploy/mac/com.quant.nightly.plist
PD=deploy/mac/com.quant.drill.plist
RN=deploy/mac/run_nightly_verify.sh
RW=deploy/mac/run_drill_weekly.sh
RP=deploy/mac/restic_pull_backup.sh

# ── DRILL-A：restic 快照读法（不得回到在外部命令上赌版本标志）──
absent_code107 "$VR" '--last' 'DRILL-A --last/--latest 都不得出现在执行行（0.19.1 把 --last 改写成 ID 前缀过滤⇒恒空且 rc=0；--latest 未在本机验证过，取向是不赌版本）'
eq107 "$VR" 'restic -r "$REPO" snapshots --json' 1 'DRILL-A 取快照的形态：不过滤、整份 JSON 自己排（摘掉＝退回按标志过滤）'
eq107 "$VR" 'key=lambda s: s.get("time","")' 2 'DRILL-A 按 time 排序取最新（id 腿 + ts 腿各一处，少一处＝两腿读的不是同一份快照）'
eq107 "$VR" 'short_snapshot_id' 2 'DRILL-A 短 ID 优先、缺失回退完整 ID（成因注释 1 + 代码 1）'

# ── DRILL-B：标记必须在打包之前落盘 + 阈值按布局分流 + 拉取腿双时点 ──
line_before107 "$BS" '$marker | ConvertTo-Json' 'Invoke-Restic @("backup"' 'DRILL-B 成功标记写在 restic backup **之前**（这条是"快照自我描述晚一个世代"的唯一成因）'
eq107 "$BS" 'Set-Content -Path (Join-Path $SnapRoot "SNAPSHOT_OK")' 2 'DRILL-B SNAPSHOT_OK 两个落笔点（成功 ok:true / 失败 ok:false，少一个就有一侧没人写标记）'
eq107 "$VR" 'echo 30 || echo 54' 1 'DRILL-B 阈值按布局分流在位（目录模式 30h / restic 模式 54h；写死一个数＝另一模式永远错判）'
eq107 "$VR" 'SNAP_MAX_AGE_HOURS="$SNAP_MAX_AGE_HOURS"' 1 'DRILL-B 阈值真的透传给 restore_drill.sh（09-29 23:45 实测：透传缺失时演练按默认 30h 判，读数冒充 54h）'
eq107 "$RD" '${SNAP_MAX_AGE_HOURS:-30}' 1 'DRILL-B 演练侧默认值同源 30h（改一侧不改另一侧＝手工跑与定时跑两套口径）'
eq107 "$PB" '<key>Hour</key>' 2 'DRILL-B 拉取腿双时点（07:00 + 10:30；广州实际收工 05:20~08:19 浮动，单时点会整代赶不上）'
eq107 "$PB" '<integer>7</integer>' 1 'DRILL-B 第一个时点 07 在位且只一次'
eq107 "$PB" '<integer>10</integer>' 1 'DRILL-B 第二个时点 10 在位且只一次（半改模板＝只加一个 Hour 时这里掉到 0）'
eq107 deploy/mac/install_mac_backup_agent.sh 'HOUR_LINES' 3 'DRILL-B 安装器自带双时点前置锁（漏装＝plist 改坏也照样装上去）'

# ── DRILL-C：accounts 断言按"要防的失效形态"重写，旧"每账号都有 paper.json"不得复活 ──
eq107 "$RD" 'accounts_files' 5 'DRILL-C 等值腿的字段五处同源（说明/取产物数/取标记数/判红/回显/旧标记兜底）'
eq107 "$RD" 'PAPER_TOTAL' 4 'DRILL-C "至少一个 paper.json"在位数腿（累加/判红/回显 + 初值）'
has107 "$RD" '属正常形态' 'DRILL-C 无 paper.json 的账号打 INFO 而不是判红（不隐身，但也不误伤合法生产形态）'
absent107 "$RD" 'MISSING' 'DRILL-C 旧"每账号缺 paper.json 即红"形态不得复活（现网实测 4 个账号只有 1 个有，那条断言在健康备份链上永远红）'

# ── 定时腿派生锁：deploy/mac 下每个 launchd 模板都必须有同名安装器 + 仓内被调脚本 ──
# 为什么用派生而不是写死三个文件名（§BOM-REPO-DERIVE 的教训直接搬过来）：
#   上一批的仓库字节 BOM 锁就是因为"锁面=硬编码清单"，漏了 watchdog 那个文件整整一天，
#   而现网被部署链就地补好了、git status 只显示 1 字节 M 被当噪声。清单式锁对**下一个**
#   新增任务天生失明，所以这里从目录派生全集，并钉一枚"派生清单过短即红"的正锁。
CNT107=$((CNT107 + 1))
PLIST_N=$(ls -1 deploy/mac/com.quant.*.plist 2>/dev/null | wc -l | tr -d '[:space:]')
[ "${PLIST_N:-0}" -ge 3 ] || { echo "--- FAIL: §107 派生正锁 ${CNT107}（deploy/mac 下 launchd 模板派生数=${PLIST_N}，<3＝派生模式失效或模板被挪走，下面这个循环会退化成恒绿空转）"; exit 1; }
PAIR_MISS=""
for p in deploy/mac/com.quant.*.plist; do
	SHORT="$(basename "$p" .plist)"
	SHORT="${SHORT#com.quant.}"
	[ -f "deploy/mac/install_mac_${SHORT}_agent.sh" ] || PAIR_MISS="${PAIR_MISS} ${SHORT}(缺同名安装器)"
	# plist 的 ProgramArguments 指的是**稳定副本**路径，这里只取脚本名回仓核对文件在不在；
	# 副本没装是安装期的事，模板指向一个仓里都不存在的脚本才是设计期就该拦下的错。
	SCRIPT_NAME="$(sed -n '/<key>ProgramArguments<\/key>/,/<\/array>/p' "$p" | grep -o '[A-Za-z0-9_]*\.sh' | tail -1)"
	[ -n "$SCRIPT_NAME" ] || PAIR_MISS="${PAIR_MISS} ${SHORT}(ProgramArguments 里找不到 .sh)"
	[ -f "deploy/mac/${SCRIPT_NAME}" ] || PAIR_MISS="${PAIR_MISS} ${SHORT}(被调脚本 ${SCRIPT_NAME} 不在仓里)"
done
CNT107=$((CNT107 + 1))
[ -z "$PAIR_MISS" ] || { echo "--- FAIL: §107 调度件配对锁 ${CNT107}：${PAIR_MISS}"; exit 1; }

# ── 死调度探测器：一个判读函数、三个消费者（演练 + 夜间验收 + 看门狗），不得写回多份近乎一样的解析 ──
eq107 "$RP" 'record_freshness()' 1 '探测器定义唯一（两份并存的结局是只修一份，另一份继续读旧键名）'
eq107 "$RP" '^record_freshness ' 3 '消费者恰好三个（drill / nightly / watchdog；§MAC-WATCHDOG 起看门狗留档也由它反查。掉一个＝那条调度重新没人反查新鲜度；§MAC-WATCHDOG 之前是 2，涨到 3 的那一位是看门狗自己）'
eq107 "$RP" 'DRILL_MAX_AGE_DAYS' 1 '演练超龄阈值只在调用点出现一次（9 天＝周日一次 + 两天余量）'
eq107 "$RP" 'NIGHTLY_MAX_AGE_DAYS' 1 '夜间验收超龄阈值只在调用点出现一次（2 天＝天天跑 + 一天余量）'
eq107 "$RP" 'WATCHDOG_MAX_AGE_DAYS' 1 '看门狗超龄阈值只在调用点出现一次（2 天＝天天跑 + 一天余量；§MAC-WATCHDOG）'

# ── DRILL-D：别名前提（ssh -G 对未配置别名把别名本身当 hostname 回显 ⇒ "非空即通过"恒真）──
eq107 "$RN" 'alias_declared()' 1 '别名点名判据单实现（预览腿与实跑腿共用，不许一个有一个没有）'
eq107 "$RN" 'alias_declared ||' 1 '预览分支调用点名判据（安装期自检就拦下"别名压根不存在"）'
eq107 "$RN" 'if ! alias_declared' 1 '定时分支调用点名判据（不拦＝每天拉起一次然后红在连接阶段）'
eq107 "$RN" 'PREVIEW-FAIL alias-not-declared' 1 '预览分支判红文案（ASCII 锚，与 -Preview 协议同形）'
eq107 "$RN" 'FAIL reason=alias-not-declared' 1 '定时分支判红文案（START 之前的 FAIL 也必须留档，否则探测器看不到这次跑过）'
# 派生行在位 + **顺序**锁：点名判据必须在 ssh -G 派生之前。为什么不是负锁"不许有 ssh -G 派生"：
# 派生本身是对的（仓库不许出现字面 IP），坏只坏在把"非空"当前提；把这两行的顺序倒回来，
# 别名压根不存在时派生值仍是别名本身（非空），第二道判据看不出任何问题。
eq107 "$RN" 'GZ_HOST="$(ssh -G' 1 'DRILL-D 派生腿在位且只一条（多条＝有人加回落路径）'
line_before107 "$RN" 'if ! alias_declared' 'GZ_HOST="$(ssh -G' 'DRILL-D 点名判据先于派生（倒序＝自证绿复活）'

# ── 休市日增量心跳（第 19 条规则）：三件套 + 接线 + 别名 ban，形态与 §DEADGAUGE 同形 ──
eq107 internal/metrics/alerter.go 'Name: "signal_pinned_on_closed_day"' 1 '规则注册恰一条（重复登记会双推）'
eq107 internal/metrics/alert_routing.go '"signal_pinned_on_closed_day":' 1 '路由表有条目（缺＝走默认路由，owner 改路由时被漏掉；本条必须 RouteDaily，长假全程破线走必推会刷满）'
eq107 internal/engine/heartbeat.go 'SetGauge("signal_closed_day_pinned"' 2 '量规字面赋值点两处（休市日收案写 0 + 写真实读数），与 §69 通用守卫同形'
absent107 internal/engine/heartbeat.go 'closedDayPinnedGaugeName' '键名 const 别名不得复活（别名＝§69 守卫失明，09-29 14:0x 判红实录）'
eq107 internal/engine/heartbeat.go 'feedClosedDayPinnedGauge(now time.Time, tradingDay bool) {' 1 '喂数函数定义唯一（签名整串作锚：形参里有指针类型 *Engine，BRE 的 * 是量词，直接写 (e *Engine) 会恒 0 命中＝锁自己踩雷）'
eq107 internal/engine/heartbeat.go 'e.feedClosedDayPinnedGauge(now, tradingDay)' 1 '接线在位（定义没接＝量规恒 0、规则恒不触发，而 §69 只看赋值点字符串看不见这件事）'
word107() {
	CNT107=$((CNT107 + 1))
	local got
	got=$(grep -cw -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "1" ] || { echo "--- FAIL: §107 整词锁 ${CNT107}（${3}）：${1} 形态「${2}」got=${got:-0} 预演=1"; exit 1; }
}
word107 internal/engine/heartbeat_test.go 'func TestClosedDayPinnedCountPair' '休市/开市成对反证用例在位'
word107 internal/engine/heartbeat_test.go 'func TestSignalHeartbeatSaturdayIsNotAFault' '周六不该判故障的用例在位（反向误伤形态）'
word107 internal/engine/heartbeat_test.go 'func TestSignalClosedDayRuleRegisteredAndKeyAligned' '休市日心跳三件套对齐用例在位'

# ── 快照目录权限收敛 + restic 口令离线副本（⑪-3 的落码腿）──
eq107 scripts/deploy_guangzhou.sh 'harden_snapshot_acl' 2 '加固器两处同源（BOM 归一 + 上传清单）＝只下发不自动执行'
absent_code107 scripts/deploy_guangzhou.sh '\-File .*harden_snapshot_acl' '部署链不得自动执行加固器（ACL 收敛属于现网特权变更，必须 owner 当面跑预演看读数再 -Apply）'
eq107 deploy/qmt-win/harden_snapshot_acl.ps1 '\[switch\]\$Apply' 1 '缺省只读预演（不带 -Apply 一个字节不改，与 rotate_qmt_token/decommission 家族同姿势）'
eq107 deploy/qmt-win/harden_snapshot_acl.ps1 'Protect-Dir' 2 '目录收敛实现（定义 + 调用）'
eq107 deploy/qmt-win/harden_snapshot_acl.ps1 'Protect-Pass' 2 '口令文件收敛实现（定义 + 调用）'
eq107 deploy/qmt-win/harden_snapshot_acl.ps1 'ACL_RESULT|ok=false' 1 '预演读数能判红（现网实测 outside>0 时这条命令必红；只回显数不判红就是"永远绿"的观测行）'
eq107 scripts/verify_deploy_guangzhou.sh '第 31 探针' 6 '第 31 探针六处同源（头部清单/可调项/变量定义/param 注释/正文段/结果段）'
has107 scripts/verify_deploy_guangzhou.sh 'INFO|snapshot_acl_readout' 'ACL 读数恒回显腿在位（绿也要看得到现网到底放开了哪些 SID）'
eq107 scripts/backup_restic_pass_offline.sh 'PASS_VALUE' 5 '口令值只在"取一次→算指纹→落盘"链路里出现（多出的一行＝某处把它送进了输出）'
absent_code107 scripts/backup_restic_pass_offline.sh 'echo.*PASS_VALUE' '绝不回显口令明文（本仓纪律：凭据只报长度与指纹）'
eq107 scripts/backup_restic_pass_offline.sh '^chmod 600 ' 2 '离线副本两处执行态 chmod 600（临时文件 / 目标文件）。为什么锚在**行首**并写 2 而不是 3：本行上一版写的是「chmod 600」计数 3，第三处命中其实是文件头那句说明注释——计数锚里混注释＝有人删掉真 chmod 而注释还在时会误判，而注释本来就不该收进"执行处数"的等值里（本仓锁形要与判据同形）。目录侧不收口而是靠 umask 077（下面那枚锁管），所以文案不再谎称"三处 600 含目录"。'
has107 scripts/backup_restic_pass_offline.sh 'umask 077' '落盘前的 umask 收口在位（离线副本目录权限靠这一条，不靠 chmod 600）'

# ── 前端两格（因子/形态「不适用」+ 亚单位不再取整）与本批测试资产登记 ──
eq107 web/src/pages/Signals.jsx '不适用' 7 '标注文案七处同源（判定注释/前置说明/口径注释/渲染与说明文本；改名＝说明与渲染不再同源）'
eq107 web/src/pages/Signals.jsx 'dimNaMode' 2 '「两维同缺才算无四维」判定：定义 + 渲染各一处（单缺也标注＝把真四维漏键当不适用）'
eq107 web/src/__tests__/signals_dim_cells_0929.test.jsx "it('H" 6 '本批前端六条腿逐名前缀在位（H1 取整反证 / H2 判定 / H3 旧口径必红 / H4-H6 挂载态）'

# ── 新定时腿不得内嵌字面公网 IP（派生清单，与 §106 同族纪律）──
# §KUMA-SECREDTO（2026-10-07 波 3）把文件面从 `*.sh + plist` 扩到 **`*.js`**：
#   原来这组 glob 压根不含 .js，于是 kuma_seed.js 里那份字面公网 IP 与那份 32-hex ntfy 主题
#   一路躲过全部守卫——**清单式锁的射程由清单决定**，这是同族第三次（§BOM-REPO → §BOM-REPO-DERIVE → 本次）。
#   本段的 IP 扫描读数（IP_SCAN_N / IP_HITS）同时交给 §110 消费：§110 不再自己写第二遍扫描，
#   两把锁各自扫一遍的结局就是"改一处漏一处"（§0929DRILL 判据函数单实现同族）。
#   回环 127.x 从计数里剥掉：它不是"出口地址"而是本机地址，把 kuma 的 http://127.0.0.1:3001
#   当公网 IP 判红，就是把锁磨到人人绕道（§107 DRILL-C 教训：健康现网上永远红的锁＝自毁信誉）。
CNT107=$((CNT107 + 1))
IP_SCAN_N=0
IP_HITS=0
for f in deploy/mac/*.sh deploy/mac/*.js deploy/mac/com.quant.*.plist; do
	IP_SCAN_N=$((IP_SCAN_N + 1))
	# grep -oE 逐个抓四段点分十进制再滤掉 127. 前缀：用 -c 数行会把"一行两个地址"数成一个。
	IP_HITS=$((IP_HITS + $(grep -oE '[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}' "$f" 2>/dev/null | grep -vc '^127\.' || true)))
done
[ "$IP_SCAN_N" -ge 13 ] || { echo "--- FAIL: §107 派生正锁 ${CNT107}（deploy/mac 待扫文件数=${IP_SCAN_N}，<13＝派生 glob 失效，这组锁会静默空转）"; exit 1; }
[ "${IP_HITS:-0}" = "0" ] || { echo "--- FAIL: §107 负锁 ${CNT107}（Mac 侧链里内嵌字面公网 IPv4，出口应只有 ssh 别名 gz 或运行期参数）got=${IP_HITS} 扫描文件数=${IP_SCAN_N}"; exit 1; }

echo "ok - §107 静态锁 ${CNT107} 道通过（含执行行负锁 3 枚 + 键名/形态负锁 3 枚 + 先后顺序锁 1 枚 + 派生正锁 2 枚 + 调度配对派生锁 1 枚）"

# ── 行为腿（全部离线，零写入、零外呼）────────────────────────────────────────
# 为什么这一段必须有**假产物**腿：DRILL-A/B/C 三条全都是"脚本写得完整但从没真跑过"，
# 而静态锁只能证明字形在位，证明不了判据**按预期红/绿**。四条子腿各自摘掉一条硬判据必红，
# 且正腿 (a) 用的是现网实测形状（4 个账号目录、1 个有 paper.json、2 个空目录）——
# 这条子腿就是"恢复演练在健康备份链上被误判成红"那个形态的反证。
FXROOT=$(mktemp -d /tmp/sec107_drill_XXXXXX)
SNAP="$FXROOT/snap"
mkdir -p "$SNAP/state" "$SNAP/accounts/u_main" "$SNAP/accounts/u_empty"
python3 - "$SNAP/live.db" "$SNAP/trading.db" <<'PYFX'
import sqlite3, sys
# 四表各插一行：REQUIRE_NONEMPTY 默认 1，空表会被判红（那是对的），假产物就要供得上这条判据
for p in sys.argv[1:]:
    con = sqlite3.connect(p)
    for t in ("real_positions", "orders", "fills", "real_account"):
        con.execute("CREATE TABLE IF NOT EXISTS %s (id INTEGER)" % t)
        con.execute("INSERT INTO %s VALUES (1)" % t)
    con.commit()
    con.close()
PYFX
[ -f "$SNAP/live.db" ] || { echo "--- FAIL: §107 行为腿搭建失败（python3 造不出 sqlite 假库，后面的判据无从谈起）"; exit 1; }
printf '{"a":1}\n' > "$SNAP/state/auth.json"
printf '{"rules":{}}\n' > "$SNAP/state/config.json"
printf '{"x":1}\n' > "$SNAP/accounts/u_main/paper.json"
printf '{}\n' > "$SNAP/accounts/u_main/messages.json"

# 标记写入：$1=ts $2=accounts_files（ts 用**本机本地时间**，与 backup_snapshot.ps1 的 Get-Date 同口径；
# 这里要是混进 UTC 串，本地 CST+8 会把年龄多算 8 小时，超龄腿就会以错的原因判红）
fx_marker() {
	printf '{"ok":true,"ts":"%s","dbs":{"live.db":123,"trading.db":45},"accounts_files":%s}' "$1" "$2" > "$SNAP/SNAPSHOT_OK"
}
fx_run() {
	BACKUP_DIR="$SNAP" DRILL_DIR="$FXROOT/drill" ARTIFACT=guangzhou SNAP_MAX_AGE_HOURS=30 \
		DRILL_RECORD="$FXROOT/record.jsonl" /bin/bash "$RD" >"$FXROOT/out.log" 2>&1
	echo "$?"
}
TS_NOW=$(python3 -c 'import datetime;print(datetime.datetime.now().strftime("%Y-%m-%dT%H:%M:%S"))')
TS_OLD=$(python3 -c 'import datetime;print((datetime.datetime.now()-datetime.timedelta(hours=31)).strftime("%Y-%m-%dT%H:%M:%S"))')

# (a) 正腿：现网同形 + 新鲜 + 计数一致 ⇒ 必须绿
fx_marker "$TS_NOW" 2
rc=$(fx_run)
[ "$rc" = "0" ] || { echo "--- FAIL: §107 行为腿 a（现网同形的健康产物被判红 rc=${rc}，正是 DRILL-C 那个误伤形态复活了）"; tail -12 "$FXROOT/out.log"; exit 1; }
echo "ok - §107 行为腿 a（假广州产物：1 个 paper.json + 空账号目录 + 计数等值 ⇒ 演练全绿）"
# (b) 新鲜度腿：ts 超 31h > 阈值 30h ⇒ 必须红，且红在超龄这条上
fx_marker "$TS_OLD" 2
rc=$(fx_run)
[ "$rc" != "0" ] || { echo "--- FAIL: §107 行为腿 b（31h 前的标记在 30h 阈值下仍判绿＝新鲜度腿恒绿）"; exit 1; }
grep -q '广州快照过期' "$FXROOT/out.log" \
	|| { echo "--- FAIL: §107 行为腿 b（红了但不是因为超龄，说明判据被别的腿先咬住，这条反证不可信）"; tail -6 "$FXROOT/out.log"; exit 1; }
echo "ok - §107 行为腿 b（超龄标记 ⇒ 判红且红在新鲜度这条上）"
# (c) 等值腿：标记说 99、产物实际 2 ⇒ 必须红（DRILL-C 重写后的②：半途/撕裂恢复会少文件而"非空"看不见）
fx_marker "$TS_NOW" 99
rc=$(fx_run)
[ "$rc" != "0" ] || { echo "--- FAIL: §107 行为腿 c（accounts_files 与产物数不等仍放行＝等值腿是摆设）"; exit 1; }
grep -q '≠ SNAPSHOT_OK.accounts_files' "$FXROOT/out.log" \
	|| { echo "--- FAIL: §107 行为腿 c（红了但不是等值这条）"; tail -6 "$FXROOT/out.log"; exit 1; }
echo "ok - §107 行为腿 c（标记与产物账号文件数撕裂 ⇒ 判红）"
# (d) paper 在位腿：全体账号都没有 paper.json ⇒ 必须红（此时"非空"与"等值"两条都放行，红只能来自③）
rm -f "$SNAP/accounts/u_main/paper.json"
printf '{}\n' > "$SNAP/accounts/u_main/other.json"
fx_marker "$TS_NOW" 2
rc=$(fx_run)
[ "$rc" != "0" ] || { echo "--- FAIL: §107 行为腿 d（模拟盘账本整条消失仍放行＝③没接上）"; exit 1; }
grep -q '一个 paper.json 都没有' "$FXROOT/out.log" \
	|| { echo "--- FAIL: §107 行为腿 d（红了但不是 paper 在位这条）"; tail -6 "$FXROOT/out.log"; exit 1; }
echo "ok - §107 行为腿 d（paper.json 全体消失 ⇒ 判红）"
# 留档腿：四次跑（含三次红）都要各写一行——定时链反查的就是这个文件
FX_REC=$(grep -c . "$FXROOT/record.jsonl" 2>/dev/null || true)
CNT107=$((CNT107 + 1))
[ "${FX_REC:-0}" -ge 4 ] || { echo "--- FAIL: §107 留档锁 ${CNT107}（四次演练只留了 ${FX_REC:-0} 行；失败不留档＝死调度探测器看不到红过）"; exit 1; }
echo "ok - §107 留档锁（四次跑各留一行，读数=${FX_REC}）"

# (e) plist 结构腿：用 plistlib 读**运行时真值**，而不是 grep 字形（三张模板各自的
#   Label / 稳定副本路径 / 时点都在这里核；Desktop 指进 ProgramArguments 会撞上
#   launchd 的 TCC 静默失败形态，那是"每天定时、每天失败(126)"的成因，必须当场拦）。
CNT107=$((CNT107 + 1))
python3 - "$PN" "$PD" "$PB" <<'PYPL' || { echo "--- FAIL: §107 行为腿 e（三张 launchd 模板的结构判据没过，见上方逐条原因）"; exit 1; }
import plistlib, sys
bad = []
want = {
    "com.quant.nightly": ({"Hour": 9, "Minute": 20}, "run_nightly_verify.sh"),
    "com.quant.drill": ({"Weekday": 0, "Hour": 9, "Minute": 0}, "run_drill_weekly.sh"),
    "com.quant.backup": ([{"Hour": 7, "Minute": 0}, {"Hour": 10, "Minute": 30}], "restic_pull_backup.sh"),
}
for path in sys.argv[1:]:
    with open(path, "rb") as f:
        d = plistlib.load(f)
    label = d.get("Label")
    if label not in want:
        bad.append(f"{path}: Label={label} 不在预期三张之内")
        continue
    sched, script = want[label]
    args = d.get("ProgramArguments") or []
    if len(args) != 2 or args[0] != "/bin/bash" or not str(args[1]).endswith("/" + script):
        bad.append(f"{label}: ProgramArguments={args}（应为 /bin/bash + 稳定副本/{script}）")
    if "Desktop" in " ".join(str(a) for a in args):
        bad.append(f"{label}: 稳定副本指到 Desktop（launchd 读不到 TCC 保护目录，会每天静默失败 126）")
    if str(args[1]).find("/backups/quant/") < 0:
        bad.append(f"{label}: 副本不在 ~/backups/quant/ 下（定时腿必须走非保护目录）")
    got = d.get("StartCalendarInterval")
    if got != sched:
        bad.append(f"{label}: 时点={got} 预期={sched}（backup 的双时点是 DRILL-B 收口 07:00 赶不上广州收工那条）")
if bad:
    for b in bad:
        print("   " + b)
    sys.exit(1)
print("   plist 三张结构核对通过（Label/副本路径/时点均为运行时真值）")
PYPL
echo "ok - §107 行为腿 e（三张 launchd 模板结构 + 时点 + 非桌面副本）"

# (f) 镜像树两半锁：演练与夜间两条定时腿都是"薄壳 + 被调脚本"两份，只装一半就是
#   装上去一个永远找不到判据的任务（而定时触发的失败是静默的，只有日志里一行 FAIL）。
MIRROR=$(mktemp -d /tmp/sec107_mirror_XXXXXX)
mkdir -p "$MIRROR/scripts"
cp "$RW" "$MIRROR/run_drill_weekly.sh"
cp "$RN" "$MIRROR/run_nightly_verify.sh"
cp "$RD" "$MIRROR/scripts/restore_drill.sh"
mkdir -p "$MIRROR/deploy/mac" && cp "$VR" "$MIRROR/deploy/mac/verify_restore.sh"
# 演练薄壳：镜像树缺 scripts/restore_drill.sh ⇒ 必须 rc=1 且红在 halved-mirror-tree 这条上
rm -f "$MIRROR/scripts/restore_drill.sh"
HOME="$MIRROR/home" LOG_DIR="$MIRROR/log" DRILL_RECORD="$MIRROR/rec.jsonl" \
	/bin/bash "$MIRROR/run_drill_weekly.sh" >"$MIRROR/drill_half.log" 2>&1 && rc=0 || rc=$?
[ "$rc" != "0" ] || { echo "--- FAIL: §107 行为腿 f（演练镜像树少一半还判绿＝稳定副本拍平没人拦）"; exit 1; }
grep -q 'halved-mirror-tree' "$MIRROR/drill_half.log" \
	|| { echo "--- FAIL: §107 行为腿 f（红了但不是「少一半」这条，说明拦下它的是别的原因）"; tail -6 "$MIRROR/drill_half.log"; exit 1; }
echo "ok - §107 行为腿 f（演练镜像树缺一半 ⇒ 安装期/首跑即拦下）"
# 夜间薄壳：预览分支缺被调脚本 ⇒ PREVIEW-FAIL missing-target，且**不写日志与留档**
mkdir -p "$MIRROR/n2"
cp "$RN" "$MIRROR/n2/run_nightly_verify.sh"
"$MIRROR/n2/run_nightly_verify.sh" -Preview >"$MIRROR/nightly_half.log" 2>&1 && rc=0 || rc=$?
[ "$rc" != "0" ] || { echo "--- FAIL: §107 行为腿 f2（夜间薄壳找不到被调脚本还能 rc=0）"; exit 1; }
grep -q 'PREVIEW-FAIL missing-target' "$MIRROR/nightly_half.log" \
	|| { echo "--- FAIL: §107 行为腿 f2（红了但不是 missing-target 这条）"; tail -6 "$MIRROR/nightly_half.log"; exit 1; }
[ ! -f "$MIRROR/log/nightly_verify.log" ] || { echo "--- FAIL: §107 行为腿 f2（-Preview 写了日志——预览不该留下任何「今天跑过」的痕迹）"; exit 1; }
echo "ok - §107 行为腿 f2（夜间薄壳缺被调脚本 ⇒ 预览判红且不落日志）"

# (g) DRILL-D 别名前提腿：用**不存在的别名**跑预览 ⇒ 必须红在 alias-not-declared 上。
#     这条腿的存在本身就是证据：旧写法（只看 ssh -G 非空）在这个输入下会**恒绿**
#     ——ssh 把别名本身当 hostname 回显。这里不发任何网络请求（预览分支只打印命令）。
cp scripts/verify_nightly_guangzhou.sh "$MIRROR/scripts/verify_nightly_guangzhou.sh"
SSH_ALIAS=qzzz-not-a-real-alias "$MIRROR/run_nightly_verify.sh" -Preview >"$MIRROR/alias_leg.log" 2>&1 && rc=0 || rc=$?
[ "$rc" != "0" ] || { echo "--- FAIL: §107 行为腿 g（未配置的别名通过预览＝DRILL-D 那条自证绿没修掉）"; exit 1; }
grep -q 'PREVIEW-FAIL alias-not-declared' "$MIRROR/alias_leg.log" \
	|| { echo "--- FAIL: §107 行为腿 g（红了但不是别名点名这条；若是 host-derive-empty 说明 ssh 行为变了，要重读成因）"; tail -6 "$MIRROR/alias_leg.log"; exit 1; }
# 反向自证（这条就是"为什么必须点名"的数字证据）：未配置别名时 ssh -G 确实**返回非空**
ECHO_HOST="$(ssh -G qzzz-not-a-real-alias 2>/dev/null | awk '/^hostname /{print $2; exit}')"
[ -n "$ECHO_HOST" ] || { echo "--- FAIL: §107 行为腿 g 的前提说明失效（本机 ssh -G 对未配置别名不再回显主机名：说明成因已变，请重读本段并按新行为调整判据，别把这条直接删掉）"; exit 1; }
echo "ok - §107 行为腿 g（未配置别名 ⇒ 点名判据拦下，且实测 ssh -G 对假别名仍回显非空主机名＝旧写法恒绿的证据）"
rm -rf "$MIRROR" "$FXROOT"

# (h) 语法腿：本批改过的 7 个 shell 件 + 2 个新件全部过 bash -n（派生清单，不写死文件名）
CNT107=$((CNT107 + 1))
SYN_N=0
for f in deploy/mac/*.sh scripts/restore_drill.sh scripts/verify_nightly_guangzhou.sh; do
	SYN_N=$((SYN_N + 1))
	bash -n "$f" || { echo "--- FAIL: §107 语法锁 ${CNT107}（$f 语法不过，已扫 ${SYN_N} 个）"; exit 1; }
done
[ "$SYN_N" -ge 9 ] || { echo "--- FAIL: §107 派生正锁 ${CNT107}（语法腿只扫到 ${SYN_N} 个文件，glob 失效＝整段空转）"; exit 1; }
echo "ok - §107 语法锁（派生扫到 ${SYN_N} 个 shell 件，全部 bash -n 通过）"

# (i) Go 行为腿：休市日心跳纯函数 + 三件套对齐 + 接线
#     （路由表↔规则表的等值闸 §106 已经跑过一条 leg106，同一条 go test 不在这里跑第二遍——
#       门禁后半段本来就慢，重复跑只会把"哪条腿红了"变成两行同样的红）
leg107() { # $1=说明 $2=包 $3=-run 正则
	local out
	out=$(go test -count=1 "$2" -run "$3" 2>&1 || true)
	if printf '%s\n' "$out" | /usr/bin/grep -qE '^(--- FAIL|FAIL)'; then
		echo "--- FAIL: §107 行为腿判红（${1}），全文如下："
		printf '%s\n' "$out" | head -40
		exit 1
	fi
	printf '%s\n' "$out" | /usr/bin/grep -qE '^ok' || {
		echo "--- FAIL: §107 行为腿没跑到（$1 无 ok 行＝包编译失败或用例被删）"
		exit 1
	}
	echo "ok - §107 行为腿 $1"
}
leg107 'engine 休市日增量心跳（成对反证/周六不误伤/三件套对齐/接线）' ./internal/engine/ \
	'TestClosedDayPinnedCountPair|TestSignalHeartbeatSaturdayIsNotAFault|TestSignalClosedDayRuleRegisteredAndKeyAligned|TestRefreshStalenessFeedsSignalHeartbeat'
echo "ok - §107 行为腿 9 组 + 派生/留档锁通过"

# ════════════════════════════════════════════════════════════════════════════
# §108 §STRATEGY-FIX（2026-10-06 修复批 波 1）：战法日预算子闸对五个内置战法**恒不触发**
#
# owner 在设置页填了「龙头 = 20 万」，系统一分钱都不拦。三条根因串在同一条链上，只修任一条
# 都还是漏的，所以三条各配自己的锁（本段三类判据：键空间派生腿 / 执行行静态锁 / Go 行为腿）：
#   根因①（键空间分叉＝整条子闸形同虚设）：子闸取键在 gate.go 里另写了一份两行优先级
#     「StrategyID 非空用它，否则回退显示名 Strategy」，而内置五战法的 StrategyID 在生产里恒为空串
#     （combat_agent 只给战法库规则填 ID）⇒ 子闸拿到的是「龙头」；配置写入侧 server/qmt.go 的
#     knownStrategyIDSet 只收规范 ID，非白名单键直接 400 ⇒「龙头」根本存不进 StrategyAllocs。
#     两侧永不相交 ⇒ 五个内置战法的日预算一个都命中不了，只有 fac_*/pat_* 碰巧命中。
#     而被跳过的那条 o.StrategyType 恰恰就是规范键本身（engine.go 构造 OrderRequest 时已填、
#     controller.go 全量透传到 LiveOrder）。修法不是"再补一层回退"，而是**取消本地写法**：委托
#     signalctl.StrategyKeyOf——买入幂等键（engine.go）、准入判定（AdmitStrategy）、探针三处从 §C6
#     起就同源，子闸是第四把、也是唯一一把自己写的。以后优先级怎么改，四处一起漂，永不再分叉。
#   根因②（聚合谓词被柜台截断吃掉）：userOrderId 被截到 24 字符，
#     `buy:603468:dragon_return:20261005`(33) → `buy:603468:dragon_return`，尾冒号连同日期一起没了，
#     旧谓词 `LIKE '%:'||key||':%'` 要求键两侧都有冒号 ⇒ 该战法"今日已成交"恒读 0；
#     另外 LIKE 里 `_` 是单字符通配符，fac_1 能冒充 faxx1（串账方向还不一定）。
#     修法：单点 store.signalIDHasStrategySQL 用 `instr(':'||signal_id||':', ':'||?||':') > 0`
#     （先把被匹配串补上冒号边，再做整段相等，无通配语义），**两本账共用**（已成交聚合 + 战法维
#     在途冻结）。历史行不回填：编号是事实主键，判重索引与勘误台账都挂在它上面（§SIGID-TRUNC 同口径）。
#   根因③（在途冻结不进判定＝同战法连发可穿透）：旧子闸只比"已成交"，注释却写着
#     「LocalBuyFrozen 无 strategy 维度……此处作为叠加守卫足够保守」——那是幻觉注释：全局闸 2 只保
#     总量，本闸对本战法的在途一分钱都不感知，连发能一路穿透到全局闸才停（"足够保守"的前提不存在）。
#     补 store.LocalBuyFrozenByStrategy（同一把谓词）后判定式改成 已成交 + 在途 + 本次。
#     有意比全局闸 2 紧：**不做卖出回款对冲**——实盘卖出的幂等键带的是类别（止损/止盈/减仓）而不是
#     战法键，「这个战法今天回血多少」在账本上问不出来，硬凑近似是把保守换成假精确；
#     所以拦截理由明说「口径较全局预算闸偏紧」，别让它看起来像精确账。
#   根因③之二（新账的读失败分支"看着有测试其实跑不到"）：战法维两本账的 fail-closed 用关库跑不出来
#     ——checkBuyDiscipline 一进门先把全局三本账读掉，库一关就红在 "read buy fills"，子闸 2b 根本走不到。
#     故按 §0926E2E-W1A 的 realizedPnlFn 同一种缝注入读数错误，两条分支各测一次，并且断言理由点名
#     是**哪一本账**失败——红在别处也算过的话，这条用例就是空的。
#
# 键空间可达性用 python 腿**派生**判定（不写死清单，写死清单＝下一个新增战法天生在锁外，§BOM-REPO-DERIVE）：
#   K = server/qmt.go 内置白名单里 Kind:"form" 的 ID 全集（配置可写侧；fac_*/pat_* 由战法库运行时
#       注入，静态不可派生，故本腿只判内置侧 + 前缀约定由配置写入校验兜着）；
#   P = signalctl.StrategyKeyOf 函数体内所有字面 return（生产可产生侧）；
#   S = combat_agent/adapter.go 里"多个 *X.Strategy 并进同一个 case、下一行注释点名做空"的那一行（做空族分组点）；
#   判定 K ∪ S == P 且 K ∩ S == ∅，双向都有意义：
#     · P\(K∪S) 非空 ⇒ 生产会产出这个键，而配置侧填不进去 ⇒ 该战法日预算**永远配不出来**＝根因①复活；
#     · K\P 非空 ⇒ 配置能填、子闸永远产不出该键＝同一种死的另一面；
#     · K∩S 非空 ⇒ 做空族进了实盘买入白名单，那是"键空间口径要重写"的提醒而不是回归（红在文案里写明）。
#   另钉四枚前提锁：|K|>=5、|P|>=9、|S|==4、分组行必须**恰好一条**——grep/正则一失效集合就变空，
#   而"空集 ⊆ 任何集合"恒绿，整段会退化成一行都不判的自证绿。
#
# 预演读数（2026-10-06，逐条实测后才入段）：
#   派生腿：K=5（double_bump dragon dragon_return momentum n_shape）、P=9（K 五键 + 做空四键）、
#     S=4（break_down good_news_fade high_churn leader_decay）、K∪S==P（9==9）、K∩S=∅；
#   执行行静态锁：gate.go 委托式=1、`stratKey := resolveStratKeyForGate(o)`=1、判定式含在途=1、
#     两本账读数腿各=1、`return fmt.Sprintf("查询战法在途冻结: %v", fErr)`=1、
#     resolveStratKeyForGate 函数体内 `signalctl.StrategyKeyOf`=1 且 `== ""`=0、
#     `o.StrategyID != ""`=0、旧幻觉注释非注释命中=0；
#   谓词单源：real_positions.go 非注释命中 2（定义 1 + 在途账消费 1）、risk_gates.go 消费 1、
#     `instr(':' ||`=1、全仓非测试非注释 `LIKE '%:`=0；
#   配置侧：qmt.go `knownSet := s.knownStrategyIDSet()`=3（Strategies/StrategyAmounts/StrategyAllocs 三张表
#     共用同一份派生集合）、`set[k.ID] = true`=1、config.go 新口径注释「键=**规范战法 ID**」=1、
#     旧口径串「（如 "龙头"）」=0；
#   前端第三把写键的手：Quant.jsx `allocInput[v.id]`=1、`allocInput[v.name]`=0（键只能用规范 ID，
#     显示名进不了配置写入校验）；
#   在途账委托面：real_positions.go `return d.localBuyFrozen(`=2（两个公开口径都是薄壳）、
#     `func (d *DB) localBuyFrozen(userID, day, strategyKey string) (float64, error) {` 非注释命中=1。
# 逐枚反证（同批跑，harness=/tmp/cp108/run_static.py 与 run_counterproof.py，镜像=/tmp/mirror108 整体重建，主仓零改动）：
#   静态锁 24 道**逐枚**配破坏（24 发，不做"挑几枚代表性的"——没反证的那枚就是没验证过的锁），
#   行为/派生腿 10 发（K1~K6 键空间派生 + G1~G4 Go 行为）：34 发全部 RED，且 FAIL 文案里的锁编号
#   与本枚指定编号逐一对上（NOT-AS-EXPECTED=0），跑完复位自检 GREEN。
#   harness 自身也踩过一次同族坑：归属核对原写 `expect in out` 且 expect=「锁 1」，而 "锁 1" 是
#   "锁 12（" 的子串 ⇒ 12~24 里任何一枚红都会冒充锁 1 通过＝反证白跑。已改成带括号的整串「锁 N（」
#   （§标识符锁被子串命中 的第六种形态：这次命中的是反证 harness 本身，不是被测代码）。
#   反证还当场锤出**别段**一处锁形失效：§28 的 §C1 日期过滤锁锚在 `func (d *DB) LocalBuyFrozen`
#   的体内 -A6，而本批把该公开口径拆成薄壳（SQL 挪进唯一实现）⇒ 这条锁在健康代码上恒红
#   （首轮 -full 就是在 §28 停住的）。已把锚点挪到 localBuyFrozen 并把不变量原样保留，
#   §0929DRILL-C 同族：判据要按「要防的失效形态」写，不按「想象中的代码形状」写。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 108 §STRATEGY-FIX 战法日预算子闸三条根因（键空间同源 / 截断聚合 / 在途冻结）静态锁与行为腿..."

CNT108=0
# eq108 用 **grep -cF（整串）**而不是上一段沿用的 `grep -c --`（BRE）：本段锚串里有
#   `allocInput[v.id]`、`set[k.ID] = true`、`键=**规范战法 ID**` 三类，BRE 语义下
#   `[v.id]` 是字符类（匹配单个字符，整串恒 0 命中）、`**` 是量词（ BSD grep 直接报
#   "repetition-operator operand invalid"）——照抄上一段的 helper 会得到"预演读的是 -F 口径、
#   门禁跑的是 BRE 口径"的两套数，锁要么恒红要么恒绿（§标识符锁被子串命中 的同族新形态：
#   这次不是子串，是元字符把锚串本身改了语义）。反过来说，需要正则的判据一律走 code_eq108（ERE 且括号转义）。
eq108() { # $1=文件 $2=整串 $3=预演读数 $4=说明（整文件计数，含注释——用于"新口径注释必须在位"这类正面断言）
	CNT108=$((CNT108 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §108 整串等值锁 ${CNT108}（${4}）：${1} 整串「${2}」got=${got:-0} 预演=$3"; exit 1; }
}
# 执行行等值/负锁：复用 §95 的 gw_code_hits（同一个判读实现两处消费，不再抄一份第二把尺子——
# 两份并存的结局是只修一份、另一份继续按旧口径读数，本仓 §107 刚记过这一族）。
code_eq108() { # $1=文件 $2=ERE $3=期望非注释命中数 $4=说明
	local got
	got=$(gw_code_hits "$1" "$2")
	CNT108=$((CNT108 + 1))
	[ "$got" = "$3" ] || { echo "--- FAIL: §108 执行行锁 ${CNT108}（${4}）：${1} 模式「${2}」非注释命中=${got} 预演=$3"; exit 1; }
}

# ── 根因①：取键必须委托同源，本地优先级不得复活 ──
eq108 internal/risk/gate.go 'return signalctl.StrategyKeyOf(combat_agent.Signal{' 1 '取键委托式在位且唯一（子闸与幂等键/准入同一把尺子）'
code_eq108 internal/risk/gate.go 'stratKey := resolveStratKeyForGate\(o\)' 1 '子闸真的消费委托结果（定义了不接＝键恒空、hasAlloc 恒 false，只看赋值点的守卫看不见这件事）'
code_eq108 internal/risk/gate.go 'o\.StrategyID != ""' 0 '本地两行优先级（StrategyID 非空否则回退显示名）不得复活＝根因①'
code_eq108 internal/risk/gate.go '此处作为叠加守卫足够保守' 0 '幻觉注释不得留在执行行上（成因原文只准出现在注释里，见上一条注释块）'
# 函数体级判据：resolveStratKeyForGate 内除了委托调用不许有别的选择逻辑（复用 §95 的 gw_awk_body）。
BODY_KEYS=$(gw_awk_body internal/risk/gate.go 'func resolveStratKeyForGate' 'signalctl.StrategyKeyOf')
CNT108=$((CNT108 + 1))
[ "$BODY_KEYS" = "1" ] || { echo "--- FAIL: §108 函数体锁 ${CNT108}（取键函数体内委托次数应为 1，实得 ${BODY_KEYS}；0＝退回本地写法，>1＝又分叉）"; exit 1; }
BODY_FALLBACK=$(gw_awk_body internal/risk/gate.go 'func resolveStratKeyForGate' '== ""')
CNT108=$((CNT108 + 1))
[ "$BODY_FALLBACK" = "0" ] || { echo "--- FAIL: §108 函数体锁 ${CNT108}（取键函数体内出现「== \"\"」回退腿 ${BODY_FALLBACK} 处＝本地优先级回来了，这正是五个内置战法恒不命中的落点）"; exit 1; }

# ── 根因②：整段冒号谓词单源，两本账共用，旧 LIKE 口径禁止复活 ──
eq108 internal/store/real_positions.go 'func signalIDHasStrategySQL(signalIDExpr string) string {' 1 '谓词定义唯一'
code_eq108 internal/store/real_positions.go 'signalIDHasStrategySQL\(' 2 '定义 1 + 战法维在途账消费 1（出现第 3 处＝又开了一本新账，回来把口径写清楚）'
code_eq108 internal/store/risk_gates.go 'signalIDHasStrategySQL\(' 1 '已成交聚合消费同一把谓词（两本账各自实现＝两把尺子，§C6 同族）'
eq108 internal/store/real_positions.go "instr(':' ||" 1 '冒号补边整段匹配只写在单源函数里（第二处手写 instr＝单源失效）'
PRED_LIKE=$(gw_meta_calls "LIKE '%:")
CNT108=$((CNT108 + 1))
[ "$PRED_LIKE" = "0" ] || { echo "--- FAIL: §108 全仓执行行负锁 ${CNT108}（非测试 Go 代码里又出现按两侧冒号的 LIKE 匹配 ${PRED_LIKE} 处：柜台 24 字符截断行必然读 0，且 _ 通配会串账）"; exit 1; }

# ── 根因③：在途冻结进了判定式，且两条读失败分支各自 fail-closed ──
code_eq108 internal/risk/gate.go 'filledByStrat\+frozenByStrat\+amount > alloc' 1 '判定式含在途冻结项（只比已成交＝同战法连发可穿透）'
code_eq108 internal/risk/gate.go 'g\.stratFilledAmount\(today, stratKey\)' 1 '战法已成交账读数腿'
code_eq108 internal/risk/gate.go 'g\.stratFrozenAmount\(today, stratKey\)' 1 '战法在途冻结账读数腿（新账必须真被消费，定义了不接＝恒 0）'
code_eq108 internal/risk/gate.go 'return fmt\.Sprintf\("查询战法在途冻结: %v", fErr\)' 1 '在途账读失败即拒单（吞成 0＝DB 故障期间该战法不限额）'
code_eq108 internal/risk/gate.go 'stratFrozenFn func\(userID, day, strategyKey string\) \(float64, error\)' 1 '测试缝字段在位（没有它，上面那条分支在关库用例里永远走不到＝"有测试"是空的）'

# ── 配置侧与前端：键空间的另外两只手 ──
eq108 internal/server/qmt.go 'knownSet := s.knownStrategyIDSet()' 3 '白名单/每次买多少/今天最多花多少三张表共用同一份派生集合（少一处＝那张表自己定义"什么键合法"）'
eq108 internal/server/qmt.go 'set[k.ID] = true' 1 '校验集合由 knownStrategyList 派生，不另写一份清单'
eq108 internal/config/config.go '键=**规范战法 ID**' 1 'StrategyAllocs 注释按真口径写（旧注释「先按 StrategyID、回退到显示名」正是根因①的**书写来源**）'
eq108 internal/config/config.go '（如 "龙头"）' 0 '旧口径整串不得作为现口径留在文件里（成因引用带「」引号，与本串不同形）'
eq108 web/src/pages/Quant.jsx 'allocInput[v.id]' 1 '前端日预算输入框按规范 ID 建键（与白名单同一份列表）'
code_eq108 web/src/pages/Quant.jsx 'allocInput\[v\.name\]' 0 '不许拿显示名当键（后端写入校验对未知键直接 400，用户会看到"保存成功但明天又没了"）'

# ── 根因③之补充：两本在途账必须共用**一份**实现（拆成两个公开口径只是外壳）──
#   本批把 LocalBuyFrozen 拆成「全局薄壳 + 战法薄壳 + 唯一实现 localBuyFrozen」。拆开的风险不是
#   多两个函数名，而是**日后有人把 SQL 内联回某个薄壳**——那时两本账立刻分叉（改一处漏一处，
#   §C6 单源族反复踩的那条），而全局薄壳看起来仍然完全正常。
eq108 internal/store/real_positions.go 'return d.localBuyFrozen(' 2 '两个公开口径都只做委托（少一处＝有人把 SQL 内联回了薄壳，两本账开始各长各的）'
code_eq108 internal/store/real_positions.go 'func \(d \*DB\) localBuyFrozen\(userID, day, strategyKey string\) \(float64, error\) \{' 1 '唯一实现只有一份（出现第二份＝两把尺子回来了）'

# ── 键空间可达性派生腿（A1/A2：三集合各自派生 + 双向等值 + 空/歧义即红）──
python3 - internal/server/qmt.go internal/signalctl/signalctl.go internal/combat_agent/adapter.go <<'PYKEY108' || { echo "--- FAIL: §108 键空间派生腿判红（上面已打印 K/P/S 三个集合的实得元素与个数，以及破线的那一条）"; exit 1; }
# -*- coding: utf-8 -*-
# §STRATEGY-FIX 波 1：战法日预算子闸的键空间可达性（派生式，不写死清单）。
import re
import sys

Q, S, A = sys.argv[1], sys.argv[2], sys.argv[3]


def die(msg):
    print("   " + msg)
    sys.exit(1)


def read(path):
    try:
        with open(path, encoding="utf-8") as fh:
            return fh.read()
    except OSError as e:
        die("锚点文件读不到：%s（%s）——本腿的派生依赖这三个文件，路径搬了要跟着改，不许把判据放宽" % (path, e))


qmt, sig, ada = read(Q), read(S), read(A)

# K：配置写入侧允许的键 = 内置白名单里 Kind:"form" 的 ID
K = set(re.findall(r'\{ID:\s*"([a-z0-9_]+)",\s*Name:\s*"[^"]*",\s*Kind:\s*"form"\}', qmt))
# P：生产可产生的规范键 = StrategyKeyOf 函数体内所有字面 return
m = re.search(r'^func StrategyKeyOf\(sig combat_agent\.Signal\) string \{\n(.*?)^\}\n', sig, re.M | re.S)
if not m:
    die("锚点失效：%s 里没解析到 StrategyKeyOf 函数体（签名或收尾大括号形态变了 ⇒ P 会退化成空集合，"
        "而空集合 ⊆ 任何集合恒绿，本腿就白跑了）" % S)
P = set(re.findall(r'\breturn\s+"([a-z0-9_]+)"', m.group(1)))
# S：做空族分组点 = 同一 case 行并了多个 *X.Strategy、下一行注释点名做空（分组只应有一处）
lines = ada.split("\n")
groups = [ln for i, ln in enumerate(lines)
          if ln.startswith("\tcase ") and ln.count(".Strategy") >= 2
          and i + 1 < len(lines) and "做空" in lines[i + 1]]
if len(groups) != 1:
    die("锚点歧义：%s 里符合「同一 case 分组多个 *X.Strategy + 下一行注释点名做空」的行有 %d 条（预演=1）。"
        "分组点挪走了 ⇒ 派生不出做空族集合，本腿必须停下来让人重读键空间口径" % (A, len(groups)))
SHORT = set(re.findall(r'\*([a-z_]+)\.Strategy', groups[0]))

print("   K（配置可写侧，来自 %s 内置白名单 Kind=form）= %d 个：%s" % (Q, len(K), " ".join(sorted(K))))
print("   P（生产可产生侧，来自 %s StrategyKeyOf 字面 return）= %d 个：%s" % (S, len(P), " ".join(sorted(P))))
print("   S（做空族分组，来自 %s 同一 case 行）= %d 个：%s" % (A, len(SHORT), " ".join(sorted(SHORT))))

bad = []
# 空转正/过短即红：三集合各自的下限（正则一失效集合就变空，"空 ⊆ 任何"恒绿）
if len(K) < 5:
    bad.append("|K|=%d < 5：内置白名单派生断了（K 是配置可写侧全集，五个内置战法一个都不能少）" % len(K))
if len(P) < 9:
    bad.append("|P|=%d < 9：StrategyKeyOf 字面键派生断了（预演 9 = 五内置 + 四做空）" % len(P))
if len(SHORT) != 4:
    bad.append("|S|=%d ≠ 4：做空族分组行数变了（多了＝有键被当豁免项从检查里漏掉；少了＝分组锚没咬住）" % len(SHORT))

miss_prod = sorted(K - P)         # 配置能填、子闸永远产不出这个键
miss_cfg = sorted(P - K - SHORT)  # 子闸会产出、配置侧却填不进去的键（豁免集合之外）
overlap = sorted(K & SHORT)
if miss_prod:
    bad.append("K\\P 非空 %s：白名单里的键 StrategyKeyOf 产不出来 ⇒ 给该战法配的日预算永远查不到（根因①的另一面）" % miss_prod)
if miss_cfg:
    bad.append("P\\(K∪S) 非空 %s：生产会产出这些键，而配置写入侧对未知键直接 400 ⇒ 它们的日预算**永远配不出来**"
               "＝子闸对该战法恒不触发（根因①的复活形态）" % miss_cfg)
if overlap:
    bad.append("K∩S 非空 %s：做空族进了实盘买入白名单 ⇒ 要么键空间口径变了、要么豁免集合该重划，"
               "必须回来重读本段并按新口径调整（这不是回归，是提醒）" % overlap)

if bad:
    for b in bad:
        print("   " + b)
    sys.exit(1)
print("   键空间可达性等值判定通过：K∪S == P（%d == %d）、K∩S == ∅ ⇒ 子闸取键与配置写入侧同源可达"
      % (len(K | SHORT), len(P)))
PYKEY108
echo "ok - §108 键空间可达性派生腿（K∪S==P 双向等值 + 四枚前提锁）"

# ── Go 行为腿（B1~B7：拦/放成对、五个内置逐个、显示名手工单、24 字符截断、在途穿透与串账、两条 fail-closed）──
leg108() { # $1=说明 $2=包 $3=-run 正则
	local out
	out=$(go test -count=1 "$2" -run "$3" 2>&1 || true)
	if printf '%s\n' "$out" | /usr/bin/grep -qE '^(--- FAIL|FAIL)'; then
		echo "--- FAIL: §108 行为腿判红（${1}），全文如下："
		printf '%s\n' "$out" | head -40
		exit 1
	fi
	printf '%s\n' "$out" | /usr/bin/grep -qE '^ok' || {
		echo "--- FAIL: §108 行为腿没跑到（$1 无 ok 行＝包编译失败或用例被删）"
		exit 1
	}
	echo "ok - §108 行为腿 $1"
}
leg108 'risk 子闸 2b（生产键形拦/放成对、显示名手工单、截断成交计入、五内置逐个、fac/pat 互不串账、在途冻结穿透与别战法不占额）' ./internal/risk/ \
	'TestGateBuyDisciplineSubGate2b'
leg108 'risk 子闸 2b 两条读失败 fail-closed（测试缝注入，红在别处不算通过）' ./internal/risk/ \
	'TestGateBuyDisciplineSubGate2bFailClosed'
leg108 'store 战法账本三腿（聚合口径只数买入/当日/本账号、24 字符截断匹配、战法维在途冻结与全局账等值）' ./internal/store/ \
	'TestSumBuyFilledAmountByDayForStrategy|TestStrategySignalIDTruncationMatch|TestLocalBuyFrozenByStrategy'

echo "ok - §108 静态锁 ${CNT108} 道 + 键空间派生腿 + Go 行为腿 3 组通过"

# ════════════════════════════════════════════════════════════════════════════
# §SELLFILL-DECOUPLE（2026-10-06 修复批 波 2 / 审计报告 P1-B + owner 裁决 3）：
# 网关「无底仓的卖出」曾经连流水都不落——记账与成本核算被写在同一个 return 上
#
# 缺陷本体（qmt_gateway/store.py::apply_fill 卖出分支）：旧实现是
#     else:
#         if row is None:
#             return None, False        ← 这一行在 INSERT INTO fills **之前**
# 于是"查不到持仓行"的卖出成交在网关账本上**根本不存在**。这不是"成本算不出"，是"流水没落"，
# 两者被同一个 return 绑死了。触发条件不需要任何异常，三条都是日常形态：网关重启后的持仓空窗、
# 柜台侧手工卖出而本地无行、交割单补记路径。三条下游腿全指望这一行：
#   · Go /settlement 三方对账：券商有成交、本地 fills 缺行 ⇒ 资金事实悬空，而且这条**系统性**
#     缺行会把真差异淹成"每次都有一堆差"的对账噪声（对账失去判别力比报错更危险）；
#   · store.SumSellFilledAmountByDay：少算当日回款 ⇒ 预算被占满后无法释放，与 09-22
#     「卖出记成买入 → 回款 0 → 当日预算占满」错账同族后果；
#   · TodayRealizedPnl / 胜率统计：行都不在，连"该不该计这笔"都问不到。
#
# 修法＝**记账与成本核算解耦**：fills 流水无条件落（写点仍在函数尾部、单一处），持仓缺行只影响
# "本笔不动持仓账"这一件事（不建空仓行、不做幽灵成本）。返回值的 None 语义由此澄清：
# None＝"这个 code 当前无持仓"，不是"这笔没发生"。
#   成本不可知态**不写 0、也不加第二本成本账**：修复计划原文写的是"断言 fills 成本字段 IS NULL"，
#   实测锤实后发现 **fills 表根本没有成本列**（order_id/code/side/price/qty/amount/traded_at/
#   signal_id/user_id/trade_id/fee/stamp_tax）——所以 E5 按真形态改写成两条更硬的断言：
#   ① PRAGMA 里不许出现任何含 cost 的列（列不存在＝"不可知"的正确表达，将来真有人加列这条腿
#   立刻红并要求重新表态）；② 无底仓卖出不得建持仓行。把"没有成本"翻译成一个 0 值字段＝下游把
#   0 当真实成本，比缺失更坏（Go 侧 costBasisFor 无持仓且无当日买入 ⇒ 该笔 fail-open 不计入
#   TodayRealizedPnl，这条口径本批一字未动，只补了一条断言防止有人"顺手"改成按 0 成本算——
#   那会把一次数据缺口直接推成熔断信号）。
#   同族的旧口径书写也要一起改，否则会留下"注释把缺陷当设计"的第二代误导：
#   gateway.py 代码回填处（旧文"否则 apply_fill 会拿空代码查持仓、卖出被判为『无底仓 no-op』
#   而静默漏账"——现在流水已无条件落，欠的从"流水"变成"减仓"）、handler._vouch_trade_side 的
#   docstring、tests/test_file_bridge.py::_seed_600580 的建底仓理由（那条注释原本拿缺陷当夹具前提）。
#
# owner 裁决 3（两桥补偿口径对齐，选"HTTP 桥加 outbox/重推"那一支）：
#   qmt_bridge.py::_report_new_trades 旧形态是先 `self._seen_trades.add(tid)` 再 `_post`，
#   每笔 tid 一生只推一次、且"已上报"在送达前就写下 ⇒ 网关重启/网络抖动窗口里的成交在 HTTP 桥上
#   永久消失；而策略桥每轮重推全量 DEAL（柜台是权威源，漏记会自愈）。同一条成交会不会丢取决于
#   走哪条桥＝口径分裂。现在改成"status==0（未送达）⇒ 不记 tid、warning 留痕、continue 下轮重推"，
#   与策略桥同语义，并且比内存队列更硬（桥进程重启也不丢，事实来自柜台查询）。
#   4xx 属于"网关收到并明确拒了"（如 §REJECT 身份锚皆空），不算漏记面 ⇒ 照旧记账，避免每轮刷告警。
#   策略桥侧补一段**口径声明注释**（该文件强制纯 ASCII——GBK 沙箱，门禁 §C 已有非 ASCII 字节守卫，
#   所以这段注释是英文的，中文会乱码）。
#
# 判据分三类（本段三类各有存在必要，互相不能替代）：
#   ① 静态整串锁（eq109，grep -cF）：钉"写点唯一 / 消费点唯一 / 留痕在位 / 旧口径串不得回流"。
#     用 -cF 不用 BRE（§108 的教训：`[store]`、`**t` 在 BRE 下语义被改写，恒 0 命中或正则报错）。
#   ② python 结构腿（PY109）：钉**形态**而不是字符串——"无底仓分支与流水写点之间零出口"、
#     "发送 < 未送达守卫 < 记 tid"。静态串锁挡不住"把 INSERT 挪进 else 分支"这类形态破坏
#     （串还在、顺序还在、语义已经反了），这一类破坏只有按行区间取的判据能咬住。
#   ③ 行为腿：pytest 15 条（E1/E3/E4/E5/E5b + 入口腿 + HTTP 桥补偿四态）+ Go 3 条
#     （无底仓卖出的**读取侧**对照：流水可见、回款计入、成本不可知时盈亏 fail-open、重放幂等）。
#     Go 腿的存在理由：缺陷在 Python，但后果由 Go 读；两本账（网关 SQLite 与引擎 SQLite 视图）
#     必须对同一笔事实给同一个数，只补 Python 侧断言＝修了一侧、另一侧靠巧合。
#
# 预演读数（2026-10-07，逐条实测后才入段）：
#   静态：store.py `def apply_fill(self, f):`=1、fills 写点整串=1、无底仓留痕 log.warning=1、
#     docstring 新口径串=1；handler.py `pos, is_dup = self.store.apply_fill(ev)`=1（唯一消费点）；
#     gateway.py 新口径 prose=1、旧 prose 串「同样以派发项为准——否则」=0（成因引用用『』不同形）；
#     qmt_bridge.py 发送腿=1、`if status == 0:`=1、`self._seen_trades.add(tid)`=1；
#     qmt_bridge_strategy.py 口径声明注释=1、该文件非 ASCII 字节=0（沿用 §C 既有守卫，不重复实现）；
#   结构腿：apply_fill 体内（docstring 之后起算）return=2（判重@15 + 收尾@100）、无底仓分支@24、
#     留痕@68、fills 唯一写点@86 ⇒ 分支与写点之间零出口；_report_new_trades 发送@24 <
#     未送达守卫@25（continue@28）< 记 tid@29；
#   测试资产：pytest 文件 `^    def test_`=15（与实跑 collected 15 passed 等值）、
#     Go `^func TestOrphanSell`=3（实跑 ok）。
#
# 逐枚反证（同批实跑，harness＝/tmp/cp109/run_static.py；镜像整体重建、主仓零改动）：
# 实得读数＝**8 枚全部 RED 且 FAIL 归属串与本枚指定串逐字对上，NOT-AS-EXPECTED=0，复位自检 rc=0**，
# 预检（8 枚锚串在 pristine 主仓文件里全部命中）通过。每枚破坏与其咬住的锁：
#   P1 留痕之后补一条 return（＝恢复「卖不出底仓立即返回」，**静态锚全留在位**）⇒ 红在 ② 结构腿
#     「无底仓卖出分支与流水写点之间有 return」；这一枚是特意设计成"串锁看不见、只有区间判据看得见"，
#     用来证明 ② 不是 ① 的重复。
#   P2 在持仓分支里再开第二个 fills 写点（「无条件」退化成「有条件」）⇒ 红在 ① 写点唯一（文案
#     「fills 写点唯一且字段全量」），② only_one 同红。
#   P3 把 docstring 里那句旧文 `return None, False` 搬进执行体 ⇒ 红在 ② 「执行体内 return 语句 3 处」。
#   P4 HTTP 桥把 `self._seen_trades.add(tid)` 挪回 _post 之前（＝裁决 3 要对齐掉的那条旧形态）⇒ 红在
#     ② 「HTTP 桥补偿顺序错位」；静态锚还在位，**旧形态唯一可判的锁就是这一枚**。
#   P5 摘掉未送达守卫（发送状态不再参与处置）⇒ 红在 ① 「未送达守卫在位」。
#   P6 无底仓分支静默吞掉（留痕整段摘除）⇒ 红在 ① 「无底仓卖出的留痕腿在位」。
#   P7 删一条 pytest 腿（改名必须打断 test_ 前缀，否则子串命中判绿）⇒ 红在 ③ 测试资产登记「15→14」。
#   P8 给 fills 表加一列 cost_price（E5 的前提变了）⇒ 红在 ③ 「§109 pytest 行为腿判红」，
#     由行为腿逼着人重新表态，而不是让静态锁悄悄放过一个语义变化。
#   过程锤出四条 harness/锁形纪律（都写进本段，别留给下一个跑批的人）：
#     · **锁序抢归属**：结构腿原形是「先数 return 总数、后判分支到写点之间零出口」，于是 P1 被计数锁
#       先吞掉、打印成 P3 的文案——同一把尺子上的两道检查会互相抢归属，**判据顺序本身是归属的一部分**
#       ⇒ 重排成 区间判据 → 留痕位置 → 总数 → 判重位置，P1/P3 才各自红在本枚；
#     · **计数锚混 docstring**：apply_fill 的 docstring 里有"旧实现把卖不出底仓当成立即 return 的
#       理由"这句话，按整行文本数 return 会数到 3≠2（§0929DRILL「计数锚混注释」同族的新藏处——
#       这次藏的是三引号文档串，不是 # 行）⇒ 结构腿必须先跳过 docstring 再判执行体；
#     · **切片错位**：跳 docstring 时按 `len(quote)` 切起始行，会削掉缩进的前三个空格而不是引号，
#       于是"闭合"在 docstring 第一行就成立（实测 code_start 返回 2），整段文档被当执行体 ⇒
#       必须先 lstrip() 再切。两个都是"判据看着严格、其实在读说明文字"的形态；
#     · **跨行锚块禁止手敲**：harness 首版在 Python 里手写那段 4 行的 log.warning 整块，第二行少一个
#       空格（32→31 列）⇒ P1/P2/P6 三枚破坏全部施加不上，预检报 PRE-FAIL（差点被当成"锁失明"）
#       ⇒ 改成运行时从主仓 pristine 文件按边界取块（起始锚行 → 第一条以 `")` 收尾的行），并要求
#       命中数恰为 1，不唯一就停。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 109 §SELLFILL-DECOUPLE 卖出流水与成本核算解耦（含两桥上报告警口径对齐）静态锁 + 结构腿 + 行为腿..."

CNT109=0
# eq109 与 §108 的 eq108 同形（grep -cF 整串），但**有意各写一份**而不是抽公共 helper：
# 两段的锚串语言不同（一段全 Go/JSX、一段全 Python），合并只会让下一次"某一侧要放宽"时
# 两侧一起被放宽。等值语义（整串、整文件计数、红在文案里点名文件与预演读数）保持一致。
eq109() { # $1=文件 $2=整串 $3=预演读数 $4=说明
	CNT109=$((CNT109 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §109 整串等值锁 ${CNT109}（${4}）：${1} 整串「${2}」got=${got:-0} 预演=$3"; exit 1; }
}

# ── ① Python 侧：写点唯一 / 消费点唯一 / 留痕在位 / 旧口径书写不得回流 ──
eq109 qmt_gateway/store.py 'def apply_fill(self, f):' 1 'apply_fill 只有一个实现（第二份＝又开了一条不走"流水无条件落"的入账路径）'
eq109 qmt_gateway/store.py 'INSERT INTO fills(order_id, code, side, price, qty, amount, traded_at, signal_id, user_id, trade_id, fee, stamp_tax)' 1 'fills 写点唯一且字段全量（写点若复制进持仓分支，"无条件"就变成有条件）'
eq109 qmt_gateway/store.py 'log.warning("[store] §SELLFILL-DECOUPLE 卖出成交无底仓' 1 '无底仓卖出的留痕腿在位（该分支现在只做两件事：留痕 + 不动持仓）'
eq109 qmt_gateway/store.py '§SELLFILL-DECOUPLE（2026-10-06 修复批 波 2）：**fills 流水无条件落**是本函数的第一职责' 1 'docstring 按新口径写（None＝当前无持仓，不是这笔没发生）'
eq109 qmt_gateway/handler.py 'pos, is_dup = self.store.apply_fill(ev)' 1 '入库入口唯一消费点（第二条 apply_fill 调用腿＝绕开了判重与留痕口径）'
eq109 qmt_gateway/gateway.py '§SELLFILL-DECOUPLE（2026-10-06 修复批 波 2）改了这条理由' 1 '代码回填处的成因改写留在文件里（旧注释拿缺陷当设计前提，是最容易复犯的二代误导）'
eq109 qmt_gateway/gateway.py '同样以派发项为准——否则' 0 '旧口径整串不得作为现口径回流（引用旧文一律带『』引号，与本串不同形）'
# ── ① 之二：两桥上报告警口径（E6 的静态面；行为面在 pytest，顺序面在结构腿）──
eq109 qmt_gateway/qmt_bridge.py 'status, _ = self._post("/dispatch/result", {"type": "trade", **t})' 1 '成交上报取回状态码（不取回就没法判"未送达"，裁决 3 的第一前提）'
eq109 qmt_gateway/qmt_bridge.py 'if status == 0:' 1 '未送达守卫在位（0＝urllib 异常分支，与"网关明确拒绝"的 4xx 必须分开处置）'
eq109 qmt_gateway/qmt_bridge.py 'self._seen_trades.add(tid)' 1 '已上报记账点唯一（第二处＝有人又在发送前偷偷记了一笔）'
eq109 qmt_gateway/qmt_bridge_strategy.py 'SELLFILL-RETRY reporting-compensation contract' 1 '策略桥的补偿口径声明在位（该文件强制纯 ASCII，故注释为英文；见门禁 §C 非 ASCII 字节守卫）'
# ── ① 之三：测试资产登记（删腿/改名必须在这里红，而不是静默少测）──
eq109 qmt_gateway/tests/test_sell_fill_decouple.py '§SELLFILL-DECOUPLE（2026-10-06 修复批 波 2 / P1-B）' 1 'pytest 腿文件的成因头注在位（新锁与被测文件必须互指，否则下一个跑批的人找不到被测面）'
eq109 qmt_gateway/qmt_bridge.py '§SELLFILL-RETRY' 2 '本波标签同时出现在成因注释与**运行时告警文案**里（现网日志要能按这个串反查口径，只写在注释里等于线上看不见）'
eq109 internal/store/sell_fill_decouple_test.go '§SELLFILL-DECOUPLE（2026-10-06 修复批 波 2 / P1-B）Go 读取侧回归' 1 'Go 侧读取回归的成因头注在位（跨语言两本账同一事实的书面依据）'

PYTEST_LEGS=$(grep -c '^    def test_' qmt_gateway/tests/test_sell_fill_decouple.py || true)
CNT109=$((CNT109 + 1))
[ "${PYTEST_LEGS:-0}" = "15" ] || { echo "--- FAIL: §109 测试资产登记锁 ${CNT109}（pytest 腿数应为 15（E1 无底仓落库 / E3 三条幂等 / E4 两条持仓 / E5 四条不可知态 / 入口腿 / HTTP 桥补偿四态），实得 ${PYTEST_LEGS}：少一条＝有一个形态不再被覆盖）"; exit 1; }
GO_LEGS=$(grep -c '^func TestOrphanSell' internal/store/sell_fill_decouple_test.go || true)
CNT109=$((CNT109 + 1))
[ "${GO_LEGS:-0}" = "3" ] || { echo "--- FAIL: §109 测试资产登记锁 ${CNT109}（Go 读取侧腿数应为 3（流水可见 / 回款计入而盈亏 fail-open / 重放幂等），实得 ${GO_LEGS}）"; exit 1; }

# ── ② 结构腿：钉形态而不是钉字符串（把 INSERT 挪进分支、把 add 挪回 _post 之前，都在这里红）──
python3 - <<'PY109' || { echo "--- FAIL: §109 结构腿判红（上面已打印 apply_fill 与 _report_new_trades 的行区间读数，以及破线的那一条）"; exit 1; }
# -*- coding: utf-8 -*-
# §SELLFILL-DECOUPLE 结构判据：流水写点与持仓分支之间"零出口"、HTTP 桥"送达才记 tid"。
# 静态整串锁挡不住形态破坏（串还在、顺序还在、语义已反），这一类只有按行区间取的判据能咬住。
import re
import sys

STORE = "qmt_gateway/store.py"
BRIDGE = "qmt_gateway/qmt_bridge.py"


def die(msg):
    print("   " + msg)
    sys.exit(1)


def read_lines(path):
    try:
        with open(path, encoding="utf-8") as fh:
            return fh.read().split("\n")
    except OSError as e:
        die("锚点文件读不到：%s（%s）——本腿依赖这两个文件的形态，路径搬了要跟着改，不许把判据放宽" % (path, e))


def is_code(line):
    """非空且不是整行注释（本腿只在函数执行体里跑，docstring 已由 code_start 整段跳过）。"""
    s = line.strip()
    return bool(s) and not s.startswith("#")


def region(lines, sig_re, label):
    """按签名行取函数体 [start, end)：end = 同缩进或更浅缩进的下一个 def/class/装饰器。"""
    hits = [i for i, ln in enumerate(lines) if re.match(sig_re, ln)]
    if len(hits) != 1:
        die("%s 的签名锚点命中 %d 处（预演=1）：签名换了形 ⇒ 本腿会退化成扫全文，必须停下来人肉重读"
            % (label, len(hits)))
    start = hits[0]
    indent = len(lines[start]) - len(lines[start].lstrip())
    end = len(lines)
    for j in range(start + 1, len(lines)):
        ln = lines[j]
        if not ln.strip():
            continue
        cur = len(ln) - len(ln.lstrip())
        if cur <= indent and re.match(r"^\s*(def |class |@)", ln):
            end = j
            break
    return start, end


def code_start(lines, lo, hi):
    """跳过函数 docstring，返回第一条真代码行的下标。

    为什么必须有这一步（本段预演实测锤出来的两条之一）：apply_fill 的 docstring 里写着
    "旧实现把卖不出底仓当成立即 return 的理由"，按整行文本数 return 会数到 3≠2 ——
    说明注释混进计数锚＝§0929DRILL「计数锚混注释」的同族新藏处（这次藏的是三引号文档串）。
    """
    i = lo
    while i < hi and not lines[i].strip():
        i += 1
    if i < hi and re.match(r'^\s*("""|\'\'\')', lines[i]):
        quote = '"""' if lines[i].strip().startswith('"""') else "'''"
        j = i
        while j < hi:
            # 切片必须先 lstrip()：起始行是「8 空格 + """」，按 len(quote) 切会削掉三个空格、
            # 留下引号本体，于是"闭合"判定在 docstring 第一行就成立（实测 code_start 返回 2），
            # 整段文档被当成执行体——这是"判据看着严格、其实在读说明文字"的形态。
            tail = lines[j].lstrip()[len(quote):] if j == i else lines[j]
            if quote in tail:
                return j + 1
            j += 1
        return hi
    return i


def only_one(body, needle, label):
    idxs = [i for i, ln in enumerate(body) if needle in ln]
    if len(idxs) != 1:
        die("%s：函数体内找到 %d 处「%s」（预演=1）——形态变了，本腿的区间判据不再成立，"
            "必须回来重读而不是把期望数改成实得数" % (label, len(idxs), needle))
    return idxs[0]


# ── ① store.apply_fill：持仓分支与流水写点之间不许有出口 ──
lines = read_lines(STORE)
s_start, s_end = region(lines, r"^\s*def apply_fill\(self, f\):\s*$", "store.apply_fill")
full = lines[s_start:s_end]
body = full[code_start(full, 1, len(full)):]

# 判据顺序有意为之（§109 反证 P1 与 P3 的归属全靠这个顺序）：先判"分支与写点之间有没有出口"
# （P1：留痕之后补 return＝P1-B 的复活形态），再判"出口总数恰为 2"（P3：在函数体顶部塞一条假
# 出口，它不在区间内，只有计数能咬住）。两步颠倒过来的话，P1 会被计数锁先拦掉，那枚反证就变成
# "红是红了，但不是本枚的功劳"——归属核对失败（§108「每枚破坏必须本枚独有」的同族新形态：
# 这次不是锚串重叠，是**同一把尺子的两道检查重叠**）。
buy_branch = only_one(body, 'elif fill_side == "买入":', "买入分支锚")
sell_none = next((i for i in range(buy_branch + 1, len(body))
                  if is_code(body[i]) and "if row is None:" in body[i]), None)
if sell_none is None:
    die("买入分支之后找不到 `if row is None:`（无底仓卖出分支的锚点）⇒ 分支结构被改写，"
        "本腿无法定位『持仓缺行』那一支，必须回来重读键空间之外的这条记账口径")
ins = only_one(body, "INSERT INTO fills(order_id", "fills 唯一写入点")
warn = only_one(body, 'log.warning("[store] §SELLFILL-DECOUPLE 卖出成交无底仓', "无底仓留痕腿")

if not (sell_none < ins):
    die("INSERT INTO fills 落在 `if row is None:` **之前**（写点@%d vs 分支@%d）：写点被挪进/挪到"
        "持仓分支一侧＝流水不再无条件落" % (ins, sell_none))
# 出口总数恰为 2（判重早退 + 收尾返回）放在区间判据之后，见上面那段说明。
rets = [i for i, ln in enumerate(body) if is_code(ln) and re.search(r"\breturn\b", ln)]
between = [i for i in rets if sell_none < i < ins]
if between:
    die("无底仓卖出分支与流水写点之间有 return（体内第 %s 行）＝E2 破坏的形态："
        "『查不到持仓行』又被当成立即返回的理由，整笔卖出流水消失" % between)
if len(rets) != 2:
    die("store.apply_fill 执行体内 return 语句 %d 处（预演=2：判重早退 + 收尾返回）。"
        "少一处＝判重出口没了（重放会二次落库）；多一处＝无底仓卖出又长出提前 return，"
        "正是 P1-B 的复活形态（整笔流水被吞）" % len(rets))
if not (sell_none < warn < ins):
    die("无底仓分支的留痕 log.warning 不在分支与写点之间（warn@%d，分支@%d，写点@%d）："
        "该分支现在只做两件事——留痕 + 不动持仓；缺一条就说明有人把它改回早退或改成静默吞掉"
        % (warn, sell_none, ins))
dedup_ret = rets[0]
if not (dedup_ret < sell_none):
    die("判重出口不在无底仓分支之前（%d vs %d）：判重位置挪了，重放路径与本腿的区间判据都要重新读"
        % (dedup_ret, sell_none))
print("   store.apply_fill：体内 return=%d（判重@%d + 收尾@%d）、无底仓分支@%d、留痕@%d、"
      "fills 唯一写点@%d ⇒ 分支与写点之间零出口"
      % (len(rets), dedup_ret, rets[-1], sell_none, warn, ins))

# ── ② qmt_bridge._report_new_trades：送达才记 tid（顺序锁，owner 裁决 3）──
blines = read_lines(BRIDGE)
b_start, b_end = region(blines, r"^\s*def _report_new_trades\(self\):\s*$", "Bridge._report_new_trades")
bfull = blines[b_start:b_end]
bbody = bfull[code_start(bfull, 1, len(bfull)):]
post = only_one(bbody, 'status, _ = self._post("/dispatch/result", {"type": "trade", **t})', "上报发送腿")
add = only_one(bbody, "self._seen_trades.add(tid)", "已上报记账腿")
guard = only_one(bbody, "if status == 0:", "未送达守卫腿")
if not (post < guard < add):
    die("HTTP 桥补偿顺序错位：发送@%d / 未送达守卫@%d / 记 tid@%d 必须严格递增。"
        "记在发送之前＝每笔只推一次且失败即永久消失（漏记不自愈，正是裁决 3 要对齐掉的那条）"
        % (post, guard, add))
skips = [i for i, ln in enumerate(bbody) if is_code(ln) and "continue" in ln and post < i < add]
if len(skips) != 1:
    die("发送与记账之间缺少『未送达就跳过本轮』的 continue（实得 %d 处）："
        "守卫写了却不生效＝status==0 也会把 tid 记成已上报" % len(skips))
print("   qmt_bridge._report_new_trades：发送@%d < 未送达守卫@%d（continue@%d）< 记 tid@%d "
      "⇒ 未送达不记、下轮重推" % (post, guard, skips[0], add))
PY109
echo "ok - §109 结构腿（流水写点零出口 + 送达才记顺序锁）"

# ── ③ 行为腿：pytest 15 条（Python 写侧 + 入口 + HTTP 桥补偿）+ Go 3 条（读取侧对照）──
py_out=$(py_tests qmt_gateway/tests/test_sell_fill_decouple.py 2>&1 || true)
if printf '%s\n' "$py_out" | /usr/bin/grep -qE 'FAILED|ERROR|ModuleNotFound|No such file'; then
	echo "--- FAIL: §109 pytest 行为腿判红（§SELLFILL-DECOUPLE 15 条），尾部如下："
	printf '%s\n' "$py_out" | tail -30
	exit 1
fi
printf '%s\n' "$py_out" | /usr/bin/grep -qE '15 (passed|tests)' || {
	echo "--- FAIL: §109 pytest 行为腿没跑到 15 条（尾部如下；14 条＝有腿被删或改名，0 条＝导入/语法坏了）"
	printf '%s\n' "$py_out" | tail -20
	exit 1
}
echo "ok - §109 pytest 行为腿（无底仓卖出流水 / 幂等锚 / 持仓腿 / 成本不可知态 / 入口腿 / 两桥补偿四态）"

leg109() { # $1=说明 $2=包 $3=-run 正则
	local out
	out=$(go test -count=1 "$2" -run "$3" 2>&1 || true)
	if printf '%s\n' "$out" | /usr/bin/grep -qE '^(--- FAIL|FAIL)'; then
		echo "--- FAIL: §109 Go 行为腿判红（${1}），全文如下："
		printf '%s\n' "$out" | head -40
		exit 1
	fi
	printf '%s\n' "$out" | /usr/bin/grep -qE '^ok' || {
		echo "--- FAIL: §109 Go 行为腿没跑到（$1 无 ok 行＝包编译失败或用例被删）"
		exit 1
	}
	echo "ok - §109 Go 行为腿 $1"
}
leg109 'store 无底仓卖出读取侧三腿（流水经 fills_effective 可见、回款计入而盈亏按成本不可知 fail-open、trade_id 重放幂等）' ./internal/store/ \
	'TestOrphanSellJournaledAndVisible|TestOrphanSellCountsProceedsButNotPnl|TestOrphanSellReplayStaysOneRow'

echo "ok - §109 静态锁 ${CNT109} 道 + 结构腿 + 行为腿（pytest 15 / Go 3）通过"

echo "==> 110 §KA-TASKREG + §KUMA-SECREDTO 计划任务全集探针 + 监控凭据出仓：静态锁 + 派生等值 + 判读函数十九腿 + 摘锁反证十二枚 + 反证矩阵..."

# §KA-TASKREG（2026-10-07 修复批 波 3，§AUDIT_20261005 P1-D + P1-E）本段守两件事：
#   ① 「有脚本无调度」第 N 次同族的**三段闭环**：任务名单/阈值单源（service_definitions.ps1）
#      + 注册体（register_engine_services.ps1 §6b，缺省只预演）+ 部署面在位/新鲜双判
#      （verify_deploy_guangzhou.sh 第 32 探针）。三段缺一段就回到同一个形态——
#      09-16 那次 keepalive 手工建、09-26 那次备份脚本手工装、09-29 那两条"有脚本无 launchd"，
#      每一次都是判据写得很完整而没人/没钟去触发它。
#   ② 监控告警凭据出仓（ntfy 主题＝凭据，旧值已进 git 历史⇒按已泄露处理）：
#      仓库里不许再有 32-hex 主题与字面公网 IP，取值走 ntfy_topic.sh 单实现，
#      kuma_seed.js 的两个值改必传参数、缺参非零退出。
# 本段的锁形为什么是"派生"而不是"名单"：P1-E 的根因就是清单式锁的射程由清单决定——
#   §107 那把"新增腿不许内嵌字面公网 IP"的负锁从 `deploy/mac/*.sh + plist` 派生，
#   `.js` 压根不在射程里，于是 kuma_seed.js 里那条字面 IP 与那份 32-hex 主题一路躲过全部守卫。
#   同族第三次（§BOM-REPO → §BOM-REPO-DERIVE → 本次）。所以 ② 的文件面改成从目录本身派生，
#   并钉一枚"待扫文件数过少即红"的正锁；① 的任务面改成从 service_definitions.ps1 派生集合，
#   双向等值而不是写死七个名字。
# 判读函数放 bash 的行为腿（B1）是本段最有价值的一条：本机没有 PowerShell，PS 里的判据永远
#   只能靠"字符串在位"自证；把红绿判断做成纯 bash 函数后，它能被喂合成读数逐条验红/验绿，
#   还能摘锁反证。§0929DRILL 四条缺陷的共同根因正是"判据从没真跑过"。
CNT110=0
eq110() { # $1=文件 $2=整串 $3=预演读数 $4=说明
	CNT110=$((CNT110 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §110 整串等值锁 ${CNT110}（${4}）：${1} 整串「${2}」got=${got:-0} 预演=$3"; exit 1; }
}
ln110() { # $1=文件 $2=整串 → 首个命中行号（0＝没有）；顺序锁用
	local n
	n=$(grep -nF -- "$2" "$1" 2>/dev/null | head -1 | cut -d: -f1 || true)
	printf '%s' "${n:-0}"
}

# gate_clip <上限字符数> <串> —— 按**字符**截断读数回显，且结果与 LC_CTYPE 无关（§P2-M）。
#
# 为什么要有这么一枚（2026-10-07 整轮 -collect 第一轮实跑逼出来的）：
# 回显别人给的读数时必须截短，旧写法是 bash 切片 `${got:0:92}`。bash 3.2 的这个切片**跟随
# 当前字符类**：本机 `LANG=""`、`LC_CTYPE="C"` 时它按**字节**切（同一句在 UTF-8 locale 下按字符切，
# 所以单跑 §110 从来看不见这个坏法——只有整轮把日志喂给分类器时才炸）。中文是三字节，
# 按字节切就会正好劈开一个字符，日志里落下一个非法 UTF-8 字节。
# 后果不在那一行好看与否：收集模式的分类器读的正是这份日志，严格解码时**一个坏字节**把整遍
# 分类打挂，汇总于是读成 `BALANCE=MISSING:1,2,…,111`——那一轮真实情况是 109 段全绿跑完、
# 日志里没有任何 `--- FAIL`，却被报成"整轮没有结论"。显示层的截断把验证层的结论抹掉了，
# 这比任何一段的红都更坏。修法分两侧：分类器读日志改容错解码（别人的字节不该由我定质量），
# 自己写日志这一侧改**按字符**截断（不靠 locale 赌运气）；§111 的 L10/L11 两腿把这两侧各自
# 配了反证（把容错退回严格、把截断退回 `${x:0:N}`，缺陷必须在同一个输入上复现）。
gate_clip() { # $1=上限字符数 $2=串（可含中文/半截字节）
	printf '%s' "${2-}" | python3 -c 'import sys; s = sys.stdin.buffer.read().decode("utf-8", "replace"); n = int(sys.argv[1]); sys.stdout.write(s[:n])' "${1-92}"
}

# ── ① Mac 侧：主题取用单实现 + 两个消费腿 + 空主题不假称网络抖动 ──
eq110 deploy/mac/ntfy_topic.sh 'ntfy_topic_resolve() {' 1 '主题取用的唯一实现（第二处＝各脚本自己再拼一遍 security 命令，轮换时必漏一处）'
eq110 deploy/mac/ntfy_topic.sh 'ntfy_topic_report() {' 1 '只报长度与指纹前 8 位的回显器在位（不回显明文这条铁律的落点）'
eq110 deploy/mac/restic_pull_backup.sh 'NTFY_LIB=' 1 '拉取腿按**同目录**定位 lib（镜像树少拷它就必须拒跑，而不是静默不推）'
eq110 deploy/mac/restic_pull_backup.sh '. "$NTFY_LIB"' 1 '拉取腿 source 单实现'
eq110 deploy/mac/verify_restore.sh 'NTFY_LIB=' 1 '演练腿同一条定位口径'
eq110 deploy/mac/verify_restore.sh '. "$NTFY_LIB"' 1 '演练腿 source 单实现'
eq110 deploy/mac/restic_pull_backup.sh 'ALERT-NOT-SENT' 1 '空主题走"没发出去"的显式留痕（旧版打到 ntfy.sh 根路径吃 404、却写"网络？"＝把配置缺失伪装成网络抖动）'
eq110 deploy/mac/verify_restore.sh 'ALERT-NOT-SENT' 1 '演练腿同一条留痕（两个消费者的失败语义必须一致）'
eq110 deploy/mac/install_mac_backup_agent.sh '$BIN_DIR/ntfy_topic.sh' 4 '拉取腿安装器四件在位（计划回显/cp/chmod/落位自检）：lib 不落进稳定副本＝每晚 FATAL 静默'
eq110 deploy/mac/install_mac_drill_agent.sh '${DRILL_HOME}/deploy/mac/ntfy_topic.sh' 4 '演练安装器四件在位（镜像树少这一个文件，周日演练当场 FATAL）'
CNT110=$((CNT110 + 1))
KCFetch=$(grep -rc -- '-s "${NTFY_KEYCHAIN_ITEM' deploy/mac 2>/dev/null | grep -v ':0$' | wc -l | tr -d ' ' || true)  # 末尾 || true 是被 §89 门禁自锁逼出来的（09-24 实录：未加固的计数赋值零命中会静默中止整轮 verify；本枚入段后 §89 当场把它拦下，说明那条锁是活的）
[ "${KCFetch:-0}" = "1" ] || { echo "--- FAIL: §110 单实现正锁 ${CNT110}（钥匙串取主题的表达式只允许出现在 ntfy_topic.sh，实得有 ${KCFetch} 个文件在各自拼 security 命令）"; exit 1; }

# ── ② 文件面派生负锁（§107 清单式锁的替身：从目录本身派生，不再按文件类型点名）──
CNT110=$((CNT110 + 1))
MAC_FILES=$(ls deploy/mac | wc -l | tr -d ' ')
[ "${MAC_FILES:-0}" -ge 13 ] || { echo "--- FAIL: §110 派生正锁 ${CNT110}（deploy/mac 待扫文件数=${MAC_FILES}，<13＝目录读法坏了，这组凭据负锁会静默空转）"; exit 1; }
CNT110=$((CNT110 + 1))
HEX_HITS=0
for f in deploy/mac/*; do
	[ -f "$f" ] || continue
	HEX_HITS=$((HEX_HITS + $(grep -Ec '[0-9a-f]{32}' "$f" 2>/dev/null || true)))
done
[ "${HEX_HITS:-0}" = "0" ] || { echo "--- FAIL: §110 负锁 ${CNT110}（deploy/mac 里出现 32 位十六进制串 ${HEX_HITS} 处＝ntfy 主题/口令回流；主题＝凭据，已知值只准走钥匙串或参数）"; exit 1; }
# §107 的 glob 必须真的含 .js——P1-E 的直接教训：扩文件面这件事只有写在 glob 里才算数。
# 预演读数=2 而不是 1：本枚锁自身的参数里就带着这串 glob（写锁的人没法不写被锁的串），
# 于是"真代码行 + 这条锁"各命中一次。将来出现 3 处只有两种可能——有人在说明文字里抄了整串
# （刻意改锁要重跑预演）或 glob 被抄了第二份（正是本仓要根因的第二本账）。
eq110 scripts/verify_changes.sh 'for f in deploy/mac/*.sh deploy/mac/*.js deploy/mac/com.quant.*.plist; do' 2 '§107 的派生扫描 glob 含 .js（不含＝kuma_seed.js 那类文件又回到锁外；命中数含本枚锁自身）'

# ── ③ kuma_seed.js：两个值改必传参数，且**校验排在 require 之前** ──
eq110 deploy/mac/kuma_seed.js '缺少必传参数' 1 '缺参点名文案在位'
eq110 deploy/mac/kuma_seed.js 'process.exit(2)' 2 '两个校验出口各一处（缺参 / IP 形状不符）'
CNT110=$((CNT110 + 1))
V110=$(ln110 deploy/mac/kuma_seed.js '缺少必传参数')
V110B=$(ln110 deploy/mac/kuma_seed.js 'const { io } = require("socket.io-client")')
[ "$V110" -gt 0 ] && [ "$V110B" -gt 0 ] && [ "$V110" -lt "$V110B" ] || { echo "--- FAIL: §110 先后顺序锁 ${CNT110}（参数校验@${V110} 必须早于 require@${V110B}：校验排在 require 之后时，仓库目录里直接跑必撞 MODULE_NOT_FOUND，'缺参必非零退出'会被一个不相干的意外满足＝判据的失败原因不是我以为的原因）"; exit 1; }

# ── ④ 广州面：任务名单/阈值单源 + 注册体在开关之后 + 探针按派生集合走 ──
eq110 deploy/qmt-win/service_definitions.ps1 '$SvcTaskDataloadKeepAlive = "QMT-Dataload-KeepAlive"' 1 'keepalive 任务名进单源（现网名，RUNBOOK §1）'
eq110 deploy/qmt-win/service_definitions.ps1 '$SvcTaskRoster = @(' 1 '任务全集容器在位（第 32 探针按它遍历，不再各写各的名单）'
eq110 deploy/qmt-win/service_definitions.ps1 '$SvcTaskFreshRules = @(' 1 '新鲜度规则容器在位（阈值全仓唯一一份）'
eq110 deploy/qmt-win/service_definitions.ps1 '$SvcTaskInPlaceOnly = @(' 1 '"仅查在位"名单在位（ONLOGON/ONSTART 不拿上次运行时间判健康度）'
eq110 deploy/qmt-win/register_engine_services.ps1 '[switch]$RegisterKeepaliveTask' 1 '注册开关在位（§0929OPS-⑪：schtasks /Create 属现网特权变更，只上传不自动执行）'
eq110 deploy/qmt-win/register_engine_services.ps1 'SKIP-CREATE 缺省只预演' 1 '缺省态打印预演而不是动手'
eq110 deploy/qmt-win/register_engine_services.ps1 'schtasks /Create /F /SC DAILY /ST $SvcKeepaliveDailyAt' 1 '注册语句唯一且阈值/时点取单源变量（写死小时数＝与单源脱钩的第二本账）'
eq110 deploy/qmt-win/register_engine_services.ps1 'KA_TASK name=' 1 '预演读数行在位（绿也要看得到现网到底存着哪条动作行）'
CNT110=$((CNT110 + 1))
SW110=$(ln110 deploy/qmt-win/register_engine_services.ps1 'if ($RegisterKeepaliveTask) {')
CR110=$(ln110 deploy/qmt-win/register_engine_services.ps1 'schtasks /Create /F /SC DAILY /ST $SvcKeepaliveDailyAt')
[ "$SW110" -gt 0 ] && [ "$CR110" -gt 0 ] && [ "$SW110" -lt "$CR110" ] || { echo "--- FAIL: §110 先后顺序锁 ${CNT110}（开关判断@${SW110} 必须早于 /Create@${CR110}：倒序＝重跑注册脚本就顺手改了现网任务，'只上传不自动执行'这条纪律被代码自己破掉）"; exit 1; }
eq110 scripts/verify_deploy_guangzhou.sh 'ops:scheduled-task roster in place + periodic tasks fresh' 3 '第 32 探针判定名三处同源（PASS 行 / FAIL 行 / 无读数正锁行；改名＝现网明细与门禁文案脱钩）'
eq110 scripts/verify_deploy_guangzhou.sh 'judge_task_roster' 3 '判读函数三处（注释指涉 / 定义 / 调用点）——定义没被调用＝读数没人判、探针恒绿'
eq110 scripts/verify_deploy_guangzhou.sh '$SvcTaskRoster' 4 '探针遍历单源集合的四处在位（elseif 空集合判定 + foreach + 两处注释）'
# ── ④b 10-09 首拨之后的两条读法修（哨兵年代界 + 注册龄）＋ 全集补第三条任务 ──────────────
# 背景读数（写进锁面，免得下次有人把判据"简化"回去）：首拨时 quant-backup-snap 报
#   `age=235445.9h`，反算 = 1999-11-30 00:01，是任务计划程序给"从未运行"的零值哨兵；
#   旧判据只挡 Year -gt 1900，挡不住这个形态 ⇒ 哨兵被当成真运行时间，探针在**健康现网**上
#   判红（而且每个发版日都红，因为 [2e] 每次都删建重注册）。
eq110 scripts/verify_deploy_guangzhou.sh 'LastRunTime.Year -gt 2010' 1 '上次运行的年代界（2010 而非 1900：1900 与 1999 两种零值哨兵都挡在门外）'
eq110 scripts/verify_deploy_guangzhou.sh 'LastRunTime.Year -gt 1900' 0 '旧年代界不许复活（负锁：留着它＝哨兵又变成一条与故障无关的红）'
eq110 scripts/verify_deploy_guangzhou.sh '"|reg_h=" + $krRegH' 1 '注册龄进读数协议（PS 一侧算，bash 只比大小）'
eq110 scripts/verify_deploy_guangzhou.sh '"|last_run=" + $krLastRun' 1 '上次运行原始时间戳进读数协议（年代界判定要能被读数复核，而不是让人信判据）'
eq110 scripts/verify_deploy_guangzhou.sh 'if [ "$age" = "never" ]' 1 '从未运行的两态分流在位（刚注册未到触发点 vs 该跑没跑）'
eq110 scripts/verify_deploy_guangzhou.sh 'never-run-beyond-rule=${notrun}' 1 '注册龄超阈值仍未运行汇总成红（分流只放" young "那一侧，不放行未知）'
eq110 scripts/verify_deploy_guangzhou.sh '从未运行且注册龄读不出' 1 'reg_h 缺失/负数/非数字走 fail-closed 并单独点名'
eq110 scripts/verify_deploy_guangzhou.sh 'unreadable-or-never-run' 0 '旧标签（把"从未运行"与"读不出"压成一个数）不许复活——两者处置相反：一个可能是刚装好，一个是被权限/异常挡住'
# ── Enabled 读数来源（10-09 自查锤出的第 32 探针第二个缺陷，比年代界那条更隐蔽）────────────
# 缺陷本体：旧写法只从 Get-ScheduledTaskInfo 那个对象上找 Enabled，而"禁用中"这件事在
#   **任务定义**（Get-ScheduledTask 的 MSFT_ScheduledTask）上才是权威位置。那个类上如果压根
#   没有这个属性，读数就恒 "na" ⇒ bash 侧 `enabled=False` 那条判据**在现网结构性失灵**，
#   而它在门禁里的合成腿（i 腿）照样绿——"判据从没真跑过"的同族，只是这次坏在读数供给侧。
#   本机没有 PowerShell，这条只能靠**形状**判：两处都试、定义那份排在前面（TaskInfo 因权限
#   或异常失败时 Enabled 仍然读得到），并且 catch 不许把好读数一起抹成 na。
# ★ 上面这一版本身还缺一把，而且缺得没有症状：它问的那两本账（定义对象属性 / 运行信息对象属性）
#   在现网**都没命中**，所以 10-09 夜间真拨测的八条读数里 enabled 仍然全 na——这条判据在现网还是
#   没真跑过，只是门禁的 i 腿（喂 enabled=False 的合成读数）照样绿。定义文本里那个节点才是权威位置，
#   接线在下面的 ④c。
eq110 scripts/verify_deploy_guangzhou.sh 'if ($krTask -and $krTask.PSObject.Properties' 1 'Enabled 的第二来源＝任务定义对象属性（权威位置其实是定义 XML 的那个节点，见下面 ④c；只留运行信息那一份＝这条判据可能在现网恒不触发）'
eq110 scripts/verify_deploy_guangzhou.sh 'if ($krEnabled -eq "na" -and $krInfo.PSObject.Properties' 1 '第二来源只在第一来源没读到时才拨（两本账：定义给了值又让运行信息覆盖，谁赢看异常顺序）'
eq110 scripts/verify_deploy_guangzhou.sh "if (\$krInfo.PSObject.Properties['Enabled']) { \$krEnabled" 0 '旧的"只问运行信息"形态不许复活（负锁：它坏的时候没有任何症状，探针一直是绿的）'
eq110 scripts/verify_deploy_guangzhou.sh '; $krEnabled = "na" }' 0 'TaskInfo 失败不许连带抹掉已取到的 Enabled（负锁：一条腿坏不能把另一条腿的好读数丢掉）'
CNT110=$((CNT110 + 1))
EN110A=$(ln110 scripts/verify_deploy_guangzhou.sh 'if ($krTask -and $krTask.PSObject.Properties')
EN110B=$(ln110 scripts/verify_deploy_guangzhou.sh '$krInfo = Get-ScheduledTaskInfo -TaskName $krName -ErrorAction Stop')
[ "$EN110A" -gt 0 ] && [ "$EN110B" -gt 0 ] && [ "$EN110A" -lt "$EN110B" ] || { echo "--- FAIL: §110 先后顺序锁 ${CNT110}（定义腿取 Enabled@${EN110A} 必须早于 TaskInfo 那条 try@${EN110B}：排在 try 之后＝TaskInfo 一抛异常就永远走不到定义腿，读回 na，正好回到本枚要根除的那个坏形态）"; exit 1; }
# ── ④c 定义侧三把读数的来源改成**任务定义 XML**（10-10 复拨自查锤出的第 32 探针第三、第四个缺陷）──
# 这一组不是读码想到的，是 10-09 夜间那次真拨测的读数自己招出来的：八条任务回传的
#   `reg_h` 与 `enabled` **全 na**。两条独立成因：
#   其一，注册龄那一式解析的是 `.Task`——MSFT_ScheduledTask 上 `.Task` 是个 CimInstance 而不是
#     字符串，字符串化得到的是**类型名**，`[xml]` 解析它必抛 ⇒ 整块进 catch ⇒ 恒 na。恒 na 在
#     bash 侧是 fail-closed 的红，于是现网红项写成 `quant-backup-snap(age=never;reg_h=na)`——
#     **我的读法坏了，长得却像现网坏了**；照着这条红上机，会去查快照任务而不是查探针。
#   其二，Enabled 问的两本账（定义对象属性、运行信息对象属性）在现网**都没命中**，而它没问过
#     定义文本里那个 `<Settings><Enabled>` 节点 ⇒ enabled 恒 na ⇒ "周期任务被禁用判红"仍然结构性
#     失灵。④b 那一版只把"只问运行信息"改成"两处都问"，坏的时候照样没有症状：门禁的 i 腿
#     （喂 enabled=False 的合成读数）永远绿，因为它测的是判读侧，失灵发生在读数供给侧。
# 修法：`.Xml` 才是任务定义文本，schtasks /Create 一定会写 RegistrationInfo/Date 与
#   Settings/Enabled；解析一次，定义侧三把（Enabled / 注册龄 / 动作行）都从这一次调用取。
#   Enabled 走四级链：XML 按名取元素 → XML 适配式 → 定义对象属性 → 运行信息对象属性，全不中才留 na
#   （未知不判绿），并把"走了哪一条"随读数带回来（en_src / reg_src）。
# ★ 为什么不选一条路走到底：`.Xml` 这一式本身也是一个**新的赌注**——计划任务 XML 带默认命名空间，
#   适配式访问在带命名空间的文档上给不给值、`[string]` 一个 XmlElement 给的是 InnerText 还是类型名，
#   这两条在本机同样没法验。"没法验"正是上一个缺陷的出身。所以两条都走，并且**读数自报来源**
#   （先例＝§N-5 服务 env 腿的 read=registry|nssm-text）：下一次拨测要么看见值，要么看见 src=none，
#   红项自己说清"是现网坏还是我的读法坏"，不需要再拿一趟上机去猜。
# ★ 不许把 XML 的布尔文本直接 `[bool]` 化：PowerShell 里非空字符串恒真，`[bool]"false"` 是 ${true}，
#   那一式会把"已禁用"洗成"已启用"——那不是读不到，是**读反**，比 na 更坏（na 至少还会 fail-closed）。
#   所以按字面量映射成判读侧认的拼写，而且真值支与假值支**各钉一枚**：只钉真值支就等于给
#   "顺手写成 [bool]"留门，而那正是把判据反着弄坏的入口。
# ★ 10-10 晚收口：解析入口从「一行式 [xml][string]$krTask.Xml」改成带两道门闩的守卫——
#   上机直读证伪了"解析成功但节点不存在"的收窄：该机 CimInstance 没有 .Xml 属性，而 [xml]"" 在
#   PS5.1 不抛、产出无根元素的空 XmlDocument 且对象为真值 ⇒ xml_from 自报 task-xml、取元素腿全空、
#   外部通道被挡，reg_src=none 是假阴性（读法供给侧第四犯）。空串不当解析输入 + 无根不当成功，
#   这两道门闩各自钉一枚，且第一通道失败必须让位给外部通道（顺序锁已有）。
eq110 scripts/verify_deploy_guangzhou.sh '$krXml = [xml]$krTaskXmlTxt' 1 '定义侧三把的唯一解析入口＝任务定义文本（一次调用、三把各自取；输入先过长度下限，与外部通道同一道门槛）'
eq110 scripts/verify_deploy_guangzhou.sh 'if (-not $krXml.DocumentElement) { $krXml = $null; $krXmlErr = "no-root-element" }' 1 '解析产物必须有根元素才算第一通道成功（[xml]"" 不抛＝空文档冒充成功的那个假阴性入口，就是这一枚钉死的）'
eq110 scripts/verify_deploy_guangzhou.sh 'task-xml-no-prop' 1 '属性不存在要自证（"属性根本不在"与"属性在但空"是两种成因，xml_err 分得开；空文档冒充成功从此有名字可指认）'
eq110 scripts/verify_deploy_guangzhou.sh '$krXml.GetElementsByTagName("Enabled")' 1 'Enabled 的第一条取元素腿＝按局部名，与默认命名空间无关（这条排在适配式之前：它不依赖"PS 的属性适配器怎么处理命名空间"这件事）'
eq110 scripts/verify_deploy_guangzhou.sh '$krXml.GetElementsByTagName("Date")' 1 '注册龄的第一条取元素腿＝按局部名（同上；这条式子出现在注释里不算，锚带赋值左值才只钉代码形状）'
eq110 scripts/verify_deploy_guangzhou.sh '$krEnRaw = $krXml.Task.Settings.Enabled' 1 'Enabled 的第二条腿＝适配式（两条腿不是冗余：任何一条单独不通时，另一条把值读回来，而"都不通"会自报 none）'
eq110 scripts/verify_deploy_guangzhou.sh '$krRegRaw = $krXml.Task.RegistrationInfo.Date' 1 '注册龄的第二条腿＝适配式（同上）'
eq110 scripts/verify_deploy_guangzhou.sh 'if (-not $krEnNode) {' 1 'Enabled 的退路门闩在位（缺它＝第一条腿失败就直接掉到对象属性，而那两本账在现网已被实测证明读不到）'
eq110 scripts/verify_deploy_guangzhou.sh 'if (-not $krRegNode) {' 1 '注册龄的退路门闩在位（同上）'
eq110 scripts/verify_deploy_guangzhou.sh '-is [System.Xml.XmlElement]' 2 '元素对象显式取 InnerText 的两处（Enabled/Date 各一条适配腿）。为什么不写 `[string]${节}点`：对 XmlElement 做字符串化拿什么形状本机同样没验——把"赌属性形状"换成"赌转换形状"不算修好'
eq110 scripts/verify_deploy_guangzhou.sh '$krRegDt.Year -gt 2010' 1 '注册时刻的年代界（与"上次运行"那把同源 2010）。这里防的不是现网坏，是**我自己按名取错元素**：任务 XML 里若还有别的 Date 元素，过老的日期就是那种错读的形状，取错当读不出，而不是拿假龄去比阈值'
eq110 scripts/verify_deploy_guangzhou.sh '"|en_src=" + $krEnSrc' 1 '读法来源进读数协议（供给侧失灵的症状是"恒 na 而判据照常绿"，光有值没有来源时红了还得人上机猜）'
eq110 scripts/verify_deploy_guangzhou.sh '"|reg_src=" + $krRegSrc' 1 '注册龄的读法来源同样进协议（两条腿共用一个自证口径，红了直接看是哪条式子没通）'
eq110 scripts/verify_deploy_guangzhou.sh 'if ($krXml) {' 3 '三条定义侧腿的门闩都是 XML 文档而不是原始对象（三处＝Enabled 腿 + 注册龄腿 + 第二条通道成功后的来源回填；门闩写回原始对象＝把本来就没解析出来的文档当成「读到了」，10-10 二拨那串 na 就是这么来的）'
eq110 scripts/verify_deploy_guangzhou.sh '$krEnNode -eq "true"' 1 'Enabled 字面量映射·真值支'
eq110 scripts/verify_deploy_guangzhou.sh '$krEnNode -eq "false"' 1 'Enabled 字面量映射·假值支（只钉上一枚＝这条判据可以被反着弄坏而不红）'
eq110 scripts/verify_deploy_guangzhou.sh '([xml][string]$krTask.Task)' 0 '坏形态不许复活（负锁：解析 `.Task` 必抛→注册龄恒 na→发版日凭空一条与故障无关的红）'
eq110 scripts/verify_deploy_guangzhou.sh '[bool]$krEnNode' 0 '不许把 XML 布尔文本直接布尔化（负锁：非空字符串恒真，禁用会被读成启用＝读反而不是读不到）'
# 跨语言拼写等值（PS 侧出什么 ↔ bash 侧比什么）。字面量**从判读侧那条比较式派生**，再要求 PS 侧
#   恰好有一处映射成它：写死 "False" 的话，两边同时改名会一路绿着把禁用判定弄死；而这条锁的形状
#   由判读侧决定——判据真正生效的地方是 bash 的那个字面量，供给侧必须跟着它。
CNT110=$((CNT110 + 1))
ENFALSE110=$(grep -oE '\[ "\$en" = "[^"]*" \]' scripts/verify_deploy_guangzhou.sh | head -1 | sed -n 's/.*= "\([^"]*\)".*/\1/p' || true)
[ -n "$ENFALSE110" ] || { echo "--- FAIL: §110 跨语言等值正锁 ${CNT110}（从判读侧抽不出被比较的 Enabled 字面量＝那条比较式换了形状，这枚锁失去守护对象；空抽取的锁比没有锁更坏，它让人以为验过）"; exit 1; }
ENPS110=$(grep -cF -- "\$krEnabled = \"${ENFALSE110}\"" scripts/verify_deploy_guangzhou.sh || true)
# 期望 2 而不是 1（10-10 三批把 State 末级接进同一条映射之后抬的）：产出这个字面量的两支＝
#   XML 文本假值支 + State=Disabled 末级支。两支**必须映射到判读侧认的同一个拼写**，
#   所以这枚锁从"恰好一处"改成"恰好两处"而不是改成下限——第三支冒出来同样要问一句"它是谁"。
[ "${ENPS110:-0}" = "2" ] || { echo "--- FAIL: §110 跨语言等值锁 ${CNT110}（PS 侧把 Enabled 映射成判读侧认的那个字面量的语句实得 ${ENPS110:-0} 处、应为 2＝XML 文本支 + State 末级支；一边拼写脱钩＝读数里明明有禁用值而判据永不命中，正是 ④c 那条失灵的同族）"; exit 1; }
# ④d 10-10 二拨读数逼出的第三批读法（同一次拨测：`.Xml` 仍没给出值，而 `action=` 有值 ⇒
#   `$krTask` 非空是被读数证明的，失灵点只能在解析这一步或"节点本来就不存在"）。
# 这一段的全部内容是**把两种相反成因分开回传**，外加一条已证明活着的外部通道兜底：
#   krObj（定义对象拿到没）/ krXmlFrom（那份 XML 从哪条通道来的）/ krXmlErr（解析器抱怨了什么），
#   以及 Enabled 的末级来源 State（它回答的是判据真正要问的那件事：这任务现在会不会自己跑）。
# 取向上的自我约束：**不再猜第三种属性名**。上一次失灵（`.Task`）就是因为拿"我确定它是 XML"当判据；
#   这一次先把"我到底走到哪一步"变成读数，再决定换什么。兜底通道选 schtasks /Query /TN <name> /XML
#   是因为这条命令行在同一个探针里已经被证明活着（present= 就靠它的退出码 0），
#   而它的输出是微软对外的文档格式（XML），比 cmdlet 对象的属性形状稳定。
CNT110=$((CNT110 + 1))
eq110 scripts/verify_deploy_guangzhou.sh 'if ($krTask) { $krObj = "ok" }' 1 '定义对象自证：非空才写 ok（动作行也从同一个对象读，所以这一把不是多余的重复）'
eq110 scripts/verify_deploy_guangzhou.sh '$krSbx = @(schtasks /Query /TN $krName /XML 2>$null)' 1 '第二条通道在位（外部通道兜底，不是又一个属性名）'
eq110 scripts/verify_deploy_guangzhou.sh '$krXml = [xml]$krSbTxt' 1 '第二条通道的解析支在位'
eq110 scripts/verify_deploy_guangzhou.sh 'if ($krXml -and $krXmlFrom -eq "none") { $krXmlFrom = "task-xml" }' 1 '通道来源回填：第一条通道成功时也要自证来源，否则读数只能说"有 XML"不能说"从哪来"'
eq110 scripts/verify_deploy_guangzhou.sh '$krXmlErr = $_.Exception.Message' 2 '两条通道的解析器原话各带一次（拿到的是抱怨文本而不是猜）'
eq110 scripts/verify_deploy_guangzhou.sh 'if ($krXmlErrRaw -and -not $krXmlErr) { $krXmlErr = "non-ascii" }' 1 'zh-CN 的异常文本剥完非 ASCII 会变空串：空串与"没有异常"必须还能分开'
eq110 scripts/verify_deploy_guangzhou.sh '$krSt = [string]$krTask.State' 1 '末级来源取 State（只取字符串，不做布尔化、不做 -match 通配）'
eq110 scripts/verify_deploy_guangzhou.sh '$krEnSrc = "defs-state"' 2 'State 两支各标一次来源（Disabled→False / Ready|Running→True），锚在赋值式上以免背进注释'
eq110 scripts/verify_deploy_guangzhou.sh '"|obj=" + $krObj' 1 '三把观测键之一进协议（位置在 action= 之前）'
eq110 scripts/verify_deploy_guangzhou.sh '"|xml_from=" + $krXmlFrom' 1 '同上：通道来源'
eq110 scripts/verify_deploy_guangzhou.sh '"|xml_err=" + $krXmlErr' 1 '同上：解析器原话'
# 竖线中和（10-10 三批）：`xml_err` 是**排在行中段**的自由文本（action 之前），而 bash 按"最后一处
#   |键名="抽值——异常原文会把整段输入带回消息里，一旦其中出现 `|enabled=` 这种形状，判读键就被抽成
#   那段文本＝读数自己把自己的判据弄反（本段根除的那一族换了个成因：值撑破协议，而不是通道不通）。
#   锚只钉那条 -replace 本体（说明注释里不出现这一整串，免得又踩「计数锚混注释」第三次）。
eq110 scripts/verify_deploy_guangzhou.sh "-replace '\|', '+'" 1 '行中段自由文本先中和竖线（判读键不被异常文本抽走）'
# 未知状态不许映射（末级只认三个已知值；别的状态留 na＝读不出，而不是拿没见过的值去把禁用判反）。
eq110 scripts/verify_deploy_guangzhou.sh '$krEnabled = [string]$krSt' 0 '不许把 State 整串直塞进 enabled（判读侧认的字面量只有 True/False）'
# 先后顺序（本段第 6 枚）：外部通道兜底必须**先于**两个 XML 消费者，否则消费者拿到的是 null 文档、
#   兜底白做——而读数看起来与"通道都没通"一模一样（src 仍是 none），又一次恒绿装饰。
CNT110=$((CNT110 + 1))
SBX110A=$(ln110 scripts/verify_deploy_guangzhou.sh '$krXml = [xml]$krSbTxt')
SBX110B=$(ln110 scripts/verify_deploy_guangzhou.sh '$krXml.GetElementsByTagName("Enabled")')
[ "$SBX110A" -gt 0 ] && [ "$SBX110B" -gt 0 ] && [ "$SBX110A" -lt "$SBX110B" ] || { echo "--- FAIL: §110 先后顺序锁 ${CNT110}（外部通道解析@${SBX110A} 必须早于 XML 消费者@${SBX110B}：兜底排在消费之后就是白兜，而 src 仍写 none，两种失灵在读数上不可分）"; exit 1; }
# 先后顺序（本段第 7 枚）：State 末级必须排在**直接来源之后**（这一枚钉的是语义次序不是语法次序）。
#   为什么值得钉：State 是**派生态**，XML 的 <Enabled> 与对象属性才是被读的那一位。把派生态提到前面，
#   判据就会在"标志位明明写着 false"时先撞上派生态并把它报成 True——读反，而且绿（§派生状态不是来源）。
CNT110=$((CNT110 + 1))
ST110A=$(ln110 scripts/verify_deploy_guangzhou.sh '$krEnSrc = "info-prop"')
ST110B=$(ln110 scripts/verify_deploy_guangzhou.sh '$krSt = [string]$krTask.State')
[ "$ST110A" -gt 0 ] && [ "$ST110B" -gt 0 ] && [ "$ST110A" -lt "$ST110B" ] || { echo "--- FAIL: §110 先后顺序锁 ${CNT110}（运行信息那条直接来源腿@${ST110A} 必须早于 State 末级@${ST110B}：派生态排到来源之前＝标志位明明 false 也会被状态报成 True，是读反不是读不到）"; exit 1; }
# 先后顺序（本段第 5 枚）：XML 解析腿必须早于它的两个消费者。为什么值得单独钉——这两条腿各自带
#   XML 门闩，把解析那一行往下挪到任何一个消费者之后，定义侧读数会**一起**静默变 na：不报语法错、
#   也不在别处红，现网看到的还是那串"八条全 na"（④c 的成因之一就是这样的一次挪位级别错误）。
CNT110=$((CNT110 + 1))
XML110A=$(ln110 scripts/verify_deploy_guangzhou.sh '$krXml = [xml]$krTaskXmlTxt')
XML110B=$(ln110 scripts/verify_deploy_guangzhou.sh '$krXml.GetElementsByTagName("Enabled")')
XML110C=$(ln110 scripts/verify_deploy_guangzhou.sh '$krXml.GetElementsByTagName("Date")')
[ "$XML110A" -gt 0 ] && [ "$XML110B" -gt 0 ] && [ "$XML110C" -gt 0 ] && [ "$XML110A" -lt "$XML110B" ] && [ "$XML110A" -lt "$XML110C" ] || { echo "--- FAIL: §110 先后顺序锁 ${CNT110}（XML 解析@${XML110A} 必须早于 Enabled 取元素腿@${XML110B} 与注册时刻取元素腿@${XML110C}：解析排在消费者之后时两把读数一起静默变 na，没有语法错也没有别处会红）"; exit 1; }
eq110 scripts/verify_deploy_guangzhou.sh 'if [ "$name" = "__live_names__" ]' 1 '观测行在 bash 侧有专用分支（不落到在位判定，否则凭空多一条 absent 红）'
# 观测行的两个出口各自钉一次，而不是钉「TASK|__live_names__ 出现 2 次」：
# 实际整串命中是 3 次——协议注释块里也写了这个字面量。计数锚一旦把注释行算进预演数，
# 注释就背上了判据的重量（改一句说明就红），而反向更坏：删掉两个 Write-Output 出口、
# 注释还在 ⇒ 计数从 3 掉到 1 也是红，但红的原因要人来分辨。取向是**锚只钉代码形状**：
# 两个出口各带自己独有的下游文本，注释怎么写都不参与计数（§110 计数锚不混注释的同一条纪律）。
eq110 scripts/verify_deploy_guangzhou.sh 'Write-Output ("TASK|__live_names__|present="' 1 '观测行的正常回传出口（唯一一处：整个清单只由这一条语句带上任务名串）'
eq110 scripts/verify_deploy_guangzhou.sh 'TASK|__live_names__|present=0|rule=none|age_h=na|state=live-names-unreadable' 1 '读不到也照样回一条（否定支：现网同族清单读不出＝未知，不能让 absent 行失去对照）'
CNT110=$((CNT110 + 1))
LN110A=$(ln110 scripts/verify_deploy_guangzhou.sh 'Write-Output ("TASK|__live_names__|present=" + $krMine.Count')
LN110B=$(ln110 scripts/verify_deploy_guangzhou.sh 'foreach ($krName in @($SvcTaskRoster)) {')
[ "$LN110A" -gt 0 ] && [ "$LN110B" -gt 0 ] && [ "$LN110A" -lt "$LN110B" ] || { echo "--- FAIL: §110 先后顺序锁 ${CNT110}（现网同族清单@${LN110A} 必须早于任务遍历@${LN110B}：判读是单遍流式，清单晚到 ⇒ absent 行引用它时还是空串，「没装 vs 改了名」这个区分当场失效）"; exit 1; }
# 全集补第三条任务（QMT-Ensure-Restore-0840）的三处成对：声明 / 进名单 / 定阈值，缺一即红。
# 为什么用"同一个变量名出现三次"而不是三条各钉一次存在：本段的立身之事就是**名单与阈值必须成对**
# （§KA-TASKREG 的根因正是脚本在清单里、任务不在名单里），把三处压成一枚计数锁，
# 将来补名单漏阈值就直接红，而不是等 F2 的集合等值腿去算差集（两道都在，这道更早、更便宜）。
eq110 deploy/qmt-win/service_definitions.ps1 '$SvcTaskEnsureRestore' 3 '新任务在单源出现三处（声明 + roster + 新鲜度规则）；两处＝补了名单没定阈值或反之'
eq110 deploy/qmt-win/service_definitions.ps1 'MaxAgeHours = 96' 1 '工作日 08:40 任务的阈值是 96h（周末合法间隔 72h + 一天），不统一抄每日任务的 30'
eq110 deploy/qmt-win/service_definitions.ps1 'MaxAgeHours = 30' 3 '每日任务阈值 30h 恰三处（快照/日志清理/盘后保活）；加第四条每日任务要连同本枚预演数一起改，别把锁改松'
CNT110=$((CNT110 + 1))
sed -n '/^judge_task_roster()/,/^}/p' scripts/verify_deploy_guangzhou.sh > /tmp/w3_jtr_region_110.sh
AWKRULE=$(grep -cF -- '$age > $rule' /tmp/w3_jtr_region_110.sh || true)
HARDAGE=$(grep -cE '\$age > [0-9]' /tmp/w3_jtr_region_110.sh || true)
[ "${AWKRULE:-0}" = "1" ] || { echo "--- FAIL: §110 阈值同源锁 ${CNT110}（判读区里那条 awk 年龄比较不再和读数带回的阈值比，实得 ${AWKRULE}＝0 或 >1；阈值必须是 $SvcTaskFreshRules 里那份，bash 侧不许存常量副本）。顺带一条自伤记录：这行原本把整串「dollar 变量名」抄进 FAIL 文案，全角括号紧跟变量名会被本仓 §23 的变量吞噬守卫判红——文案里引用代码要写成不邻接的说明，别反过来让守卫红在自己的话术上"; exit 1; }
[ "${HARDAGE:-0}" = "0" ] || { echo "--- FAIL: §110 阈值同源负锁 ${CNT110}（判读区里出现写死小时数 ${HARDAGE} 处＝阈值第二本账，改单源不会带动探针，反过来探针会长期用旧阈值判红）"; exit 1; }
# 时钟归属（10-09）：注册龄/上次运行的**日期减法只在 PS 一侧做**，bash 只比大小。
#   为什么钉这一条：判读区一旦有人写 `date -j`（BSD）或 `date -d`（GNU），本机跑得通、
#   换到另一套 date 语法就静默读出空值，而空值在这段里会被当成"注册龄读不出"判红——
#   红的原因不是现网坏，是我的判据换了台机器就坏。位置排在 /tmp/w3_jtr_region_110.sh
#   生成之后（它读的就是那份抽出来的判读区）。
CNT110=$((CNT110 + 1))
BASHDATE110=$(grep -cE 'date -[jd]' /tmp/w3_jtr_region_110.sh 2>/dev/null || true)
[ "${BASHDATE110:-0}" = "0" ] || { echo "--- FAIL: §110 时钟归属负锁 ${CNT110}（判读区里出现 date 命令 ${BASHDATE110} 处＝把日期算术搬回本机；BSD date -j 与 GNU date -d 两套语法，且现网时钟在那台机器上，这里算出来的龄没有意义）"; exit 1; }
# 读法来源两把**只观测、不判读**（10-10 补 en_src/reg_src 时同时立下的边界）：判读区里 $ens/$rgs
#   只许出现在 INFO 回显里。为什么这一条要钉而不是写在注释里：一旦有人拿"来源＝xml-byname"当健康度，
#   "我的式子换了条路"就会被读成现网变化，而现网的禁用/从未运行仍然没人判——供给侧失灵只是换了个方向重演。
#   同一枚旁边配一道**自检正锁**（带 src 的 INFO 行数下限）：负锁读的正是这份抽取，抽取空了它也会绿，
#   空扫描的锁比没有锁更坏（§70 空清单正锁、删行锚自检同一族）。
#   写法上两处避开本机两个坑：`\b` 是 GNU 扩展，BSD grep 不认，改用"后面不许再跟字母数字下划线"；
#   计数管道在 set -e 下必须带 `|| true`，否则命中 0 时整段被当成命令失败而不是锁绿（§89）。
CNT110=$((CNT110 + 1))
SRCJUDGE110=$(grep -E '\$\{?(ens|rgs|obj|xf|xe)([^[:alnum:]_]|$)' /tmp/w3_jtr_region_110.sh 2>/dev/null | grep -v 'echo "INFO' | wc -l | tr -d ' ' || true)
[ "${SRCJUDGE110:-0}" = "0" ] || { echo "--- FAIL: §110 观测不判读负锁 ${CNT110}（判读区里拿读法来源做判断的行 ${SRCJUDGE110} 处：src 两把只进 INFO，红绿必须由 present/rule/age_h/reg_h/enabled 这些现网量决定）"; exit 1; }
CNT110=$((CNT110 + 1))
SRCINFO110=$(grep -cE 'echo "INFO.*en_src=\$\{ens' /tmp/w3_jtr_region_110.sh 2>/dev/null || true)
[ "${SRCINFO110:-0}" -ge 5 ] || { echo "--- FAIL: §110 观测腿自检 ${CNT110}（判读区里带 en_src 的 INFO 回显实得 ${SRCINFO110:-0} 行，<5＝上一枚负锁读的那份抽取自己空了；它绿着的时候分不清是「没有旁路」还是「压根没抽到东西」）"; exit 1; }
CNT110=$((CNT110 + 1))
TASKNAME_IN_PROBE=$(grep -vE '^[[:space:]]*#' scripts/verify_deploy_guangzhou.sh | grep -cF 'QMT-Dataload-KeepAlive' || true)
[ "${TASKNAME_IN_PROBE:-0}" = "0" ] || { echo "--- FAIL: §110 派生正锁 ${CNT110}（探针代码行里出现字面任务名 ${TASKNAME_IN_PROBE} 处＝名单被抄了第二份，新增任务会躲过探针——本批要根除的正是清单式锁）"; exit 1; }
# 出门文本里的"顺序锁几枚"**派生自本段自己的 FAIL 文案**，不再手写：
#   那一行原本写着"顺序锁 2"，而本段的顺序锁一枚接一枚往上添（参数校验先于 require、注册开关先于
#   /Create、清单先于遍历、定义腿先于 TaskInfo try、解析先于消费者……），那句手写分项每添一枚就
#   过一次时——而**假话留在绿色输出里，没有人会去查**。分项数因此不写，改由计数派生。
#   尺子是派生 + 下限：枚数从文案计数（每枚顺序锁的 FAIL 行都以「FAIL 前缀 + 这一类锁的名字」开头），
#   读到 0 或比现值下限还低＝计数模式自己失效（§70 空清单正锁同族），这时宁可可疑不可沉默。
#   ★ 两处不诚实的形态在这一枚上都会犯，所以按形态拆干净：
#   其一，计数串**不能混注释**——本段上面那段说明里也写了这类锁的名字，只按锁名计数会多算注释行，
#     于是绿色输出报"顺序锁 6 枚"而读者只数得出 4 枚（本枚最初就是这个样子）；
#     所以计数串带上 `--- FAIL: ` 段，注释行里没有它。
#   其二，计数串**不能把自己算进去**——grep 的模式在文件里是明文，一旦整串出现，这一行就成了第 N+1 枚；
#     所以把串拆成"字面量前段 + 拼接后段"，文件里任何一行都不含完整串。
CNT110=$((CNT110 + 1))
ORDP110='--- FAIL: §110 先后顺序'
ORD110=$(grep -c -- "${ORDP110}锁" scripts/verify_changes.sh || true)
[ "${ORD110:-0}" -ge 7 ] || { echo "--- FAIL: §110 分类计数正锁 ${CNT110}（派生出的顺序锁枚数=${ORD110:-0}，<7＝计数模式或 FAIL 文案前缀被人改掉，本段出门文本会开始说谎；下限历次抬升＝第五枚「解析先于消费者」、第六枚「外部通道兜底先于 XML 消费者」、第七枚「派生态 State 晚于直接来源」，将来添锁要连同下限一起抬，别把锁改松）"; exit 1; }
echo "ok - §110 静态锁 ${CNT110} 道通过（其中先后顺序锁 ${ORD110} 枚＝从本段 FAIL 文案派生计数；分项见 ①②③④ 各组标题，不再手写分项数——手写分项每加一枚锁就过一次时）"

# ──────────────────────────────────────────────────────────────────────────────
# F2 派生集合双向等值腿（"任务名单只有一份"这条主张用机器验，而不是用注释主张）
# ──────────────────────────────────────────────────────────────────────────────
# 为什么单列一条 python 腿而不是再加几枚 eq110：eq 锁只能钉"某串出现 N 次"，钉不住**集合关系**——
#   roster 与 rules∪inplace 是否等值、rules 与 inplace 是否互斥、注册脚本的回退字面量与单源是否同值。
#   把这些关系写成硬编码清单就又回到 P1-E 的根因（清单式锁的射程由清单决定）。
#   这里从两个文件各自解析出集合再比，并把读到的东西打印出来，红了能直接看是哪条关系破的。
# F2/F3 共用一棵临时树：F3 的镜像反证必须跑**同一份** python 判据（复制第二实现＝两条腿各判各的，
# 正是本批要根除的"名单抄第二份"形态），所以先把判据落成文件，正腿与反腿只差 argv 指向主仓还是镜像。
CNT110=$((CNT110 + 1))
W3F="$(mktemp -d 2>/dev/null || true)"
[ -n "$W3F" ] && [ -d "$W3F" ] || { echo "--- FAIL: §110 F2 连临时目录都建不出来（下面的镜像反证全部无从谈起）"; exit 1; }
mkdir -p "$W3F/mir"
cat > "$W3F/f2.py" <<'PY110F2'
import re, sys

defs_path, reg_path = sys.argv[1], sys.argv[2]
defs = open(defs_path, encoding="utf-8-sig").read()
reg = open(reg_path, encoding="utf-8").read()


def block(text, head):
    """取 `@(` 容器里的内容；容器以单独一行的 `)` 收尾（写法变了就解析不到，宁可判红）。"""
    m = re.search(re.escape(head) + r"\s*=\s*@\((.*?)\n\s*\)", text, re.S)
    if not m:
        raise SystemExit("F2-FAIL: 容器 %s 解析不到（写法变了＝这条腿瞎了）" % head)
    return m.group(1)


roster = re.findall(r"\$SvcTask\w+", block(defs, "$SvcTaskRoster"))
rules_blk = block(defs, "$SvcTaskFreshRules")
pairs = re.findall(r"Name\s*=\s*(\$SvcTask\w+)\s*;?\s*MaxAgeHours\s*=\s*([0-9]+)", rules_blk)
inplace = re.findall(r"\$SvcTask\w+", block(defs, "$SvcTaskInPlaceOnly"))
ps_rows = len([l for l in rules_blk.splitlines() if l.strip().startswith("[pscustomobject")])

print("F2 读数: roster=%d rules=%d(inplace=%d)" % (len(roster), ps_rows, len(inplace)))
print("F2 名单: roster=%s" % ",".join(roster))
print("F2 名单: inplace=%s" % ",".join(inplace))

if ps_rows != len(pairs):
    raise SystemExit("F2-FAIL: $SvcTaskFreshRules 的 pscustomobject 行数与 Name/MaxAgeHours 配对数不等"
                     "（%d vs %d）＝有行的阈值没被读到，探针会拿空阈值判健康" % (ps_rows, len(pairs)))
if len(roster) != len(set(roster)):
    raise SystemExit("F2-FAIL: $SvcTaskRoster 有重复项：%s" % roster)
rules_names = [n for n, _ in pairs]
if set(roster) != set(rules_names) | set(inplace):
    raise SystemExit("F2-FAIL: 集合关系破了——roster 比 rules∪inplace 多出=%s 少出=%s"
                     "（新加任务却没定判法＝探针少查一个，正是本批要根除的形态）"
                     % (sorted(set(roster) - (set(rules_names) | set(inplace))),
                        sorted((set(rules_names) | set(inplace)) - set(roster))))
both = sorted(set(rules_names) & set(inplace))
if both:
    raise SystemExit("F2-FAIL: 同一任务既在新鲜度表又在仅查在位表：%s（两本账必有一本先过时）" % both)
for name, hours in pairs:
    if int(hours) <= 0:
        raise SystemExit("F2-FAIL: %s 的 MaxAgeHours=%s（<=0＝任何读数都判红，健康现网永远红）" % (name, hours))
ka = "$SvcTaskDataloadKeepAlive"
if ka not in roster:
    raise SystemExit("F2-FAIL: roster 里没有 %s（第 32 探针不查它＝回到『有脚本无调度还没人判红』）" % ka)
if ka not in rules_names:
    raise SystemExit("F2-FAIL: %s 不在新鲜度表里（每日 17:10 的任务停更三天也不会红）" % ka)

# 注册脚本的**回退字面量**必须与单源同值：回退块是 dot-source 失败时的备胎，写歪一次
# ＝用备用路径建出来的任务名与探针查的名字对不上（两边各自都"绿"）。
# 不按行首锚：回退块里一行放多个赋值（`$a = "x"; $b = "y"`），行首锚会漏掉后半串——
# 漏掉的正是探针要查的名字，于是"回退与单源脱钩"这条锁对它们结构性失明（同族：锚点太窄＝假绿）。
raw = re.findall(r'(\$Svc(?:Task|Keepalive|Name)\w+)\s*=\s*"([^"]*)"', reg)
fb = {}
for k, v in raw:
    if k in fb and fb[k] != v:
        raise SystemExit("F2-FAIL: 注册脚本里 %s 被赋成两个不同的字面量（%s / %s）——同名两值，谁覆盖谁看执行顺序" % (k, fb[k], v))
    fb[k] = v
if len(fb) < 10:
    raise SystemExit("F2-FAIL: 注册脚本回退字面量解析到 %d 个（<10＝回退块写法变了或被漏掉一批，等值锁形同虚设）" % len(fb))
print("F2 回退: %s" % ",".join(sorted(fb)))
for var, lit in sorted(fb.items()):
    m = re.search(r'(?m)^\s*%s\s*=\s*(.*)$' % re.escape(var), defs)
    if not m:
        raise SystemExit("F2-FAIL: 注册脚本回退了单源里根本没有的量 %s（备胎自创名字＝第二本账的起点）" % var)
    rhs = m.group(1)
    sm = re.match(r'^\s*if\s*\(.*\)\s*\{.*?\}\s*else\s*\{\s*"([^"]*)"\s*\}', rhs)
    dm = re.search(r'"([^"]*)"', rhs)
    want = sm.group(1) if sm else (dm.group(1) if dm else None)
    if want is None:
        raise SystemExit("F2-FAIL: 单源里 %s 的字面量解析不到（rhs=%s）" % (var, rhs[:60]))
    if want != lit:
        raise SystemExit("F2-FAIL: %s 回退字面量与单源不同值（register=「%s」 defs=「%s」）" % (var, lit, want))
print("F2 ok - roster=%d = rules(%d)+inplace(%d) 且互斥；回退字面量 %d 个逐项与单源等值"
      % (len(roster), len(rules_names), len(inplace), len(fb)))
PY110F2
python3 "$W3F/f2.py" deploy/qmt-win/service_definitions.ps1 deploy/qmt-win/register_engine_services.ps1 \
	|| { echo "--- FAIL: §110 F2 派生集合等值腿判红（上面已打印四个集合的实得元素，先看哪一条关系破了）"; exit 1; }
echo "ok - §110 F2 派生集合双向等值腿通过（roster/rules/inplace 三容器 + 注册回退字面量）"

# ──────────────────────────────────────────────────────────────────────────────
# F3 双向镜像反证（FIX_PLAN §5.5 F3：删注册体一条 ⇒ 红，删覆盖侧一条 ⇒ 红，两个方向都要验）
# ──────────────────────────────────────────────────────────────────────────────
# 探针侧的"名单抄第二份"不是靠 python 集合腿证的（第 32 探针按单源集合遍历，压根没有 per-task 条目可删），
# 它靠的是同段那两枚派生锁：单源引用计数等值 + 探针代码行零字面任务名。这两枚也各有镜像反证（c/d 两条腿）。
# ★ 每枚破坏都从主仓**整体重建**镜像目录再改（纪律：复位≠按文件清单复原——上一枚改剩的文件会冒充本枚的归属），
#   且破坏串是整串替换、不留原锚前缀（改名式破坏 `...KeepAlive` → `...KeepAliveX`，子串命中会让反证恒绿）。
f3_rebuild() { # 重建一份"与主仓逐字节相同"的镜像（两个 ps1 + 部署探针），返回其目录
	local d="$W3F/mir/$1"
	rm -rf "$d"
	mkdir -p "$d"
	cp deploy/qmt-win/service_definitions.ps1 "$d/service_definitions.ps1"
	cp deploy/qmt-win/register_engine_services.ps1 "$d/register_engine_services.ps1"
	cp scripts/verify_deploy_guangzhou.sh "$d/verify_deploy_guangzhou.sh"
	echo "$d"
}

f3_sub() { # $1=文件 $2=原串（必须恰好命中一次）$3=替换串；命中数≠1 直接把这条反证判红
	local n
	n=$(python3 -c 'import sys
p, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
t = open(p, encoding="utf-8-sig").read()
c = t.count(old)
if c != 1:
    sys.stderr.write("HIT=%d\n" % c)
    raise SystemExit(1)
open(p, "w", encoding="utf-8").write(t.replace(old, new))' "$1" "$2" "$3" 2>&1) || {
		echo "--- FAIL: §110 F3 破坏点没落在预期的唯一位置（原串命中数≠1：${n}）——反证若改不到东西，『必红』就成了自证"; exit 1; }
}

f3_drop_line() { # $1=文件 $2=扩展正则（必须恰好命中一行）；删掉该行
	local n
	n=$(python3 -c 'import sys, re
p, pat = sys.argv[1], sys.argv[2]
lines = open(p, encoding="utf-8-sig").read().splitlines(True)
hit = [i for i, l in enumerate(lines) if re.search(pat, l)]
if len(hit) != 1:
    sys.stderr.write("HIT=%d\n" % len(hit))
    raise SystemExit(1)
del lines[hit[0]]
open(p, "w", encoding="utf-8").write("".join(lines))' "$1" "$2" 2>&1) || {
		echo "--- FAIL: §110 F3 删行锚没命中唯一行（${n}）——锚写歪的反证等于没反证"; exit 1; }
}

f3_expect_f2_red() { # $1=标签 $2=期望的 F2-FAIL 点名片段 $3=defs $4=reg
	local out rcF3
	CNT110=$((CNT110 + 1))
	rcF3=0
	out="$(python3 "$W3F/f2.py" "$3" "$4" 2>&1)" || rcF3=$?
	[ "$rcF3" != "0" ] || { echo "--- FAIL: §110 F3 ${1}：镜像破坏后 F2 判据仍然 0 退出（这条等值锁是恒绿的装饰）"; exit 1; }
	printf '%s' "$out" | grep -qF 'F2-FAIL' || { echo "--- FAIL: §110 F3 ${1}：非零退出但不是判据自己点名（说明镜像坏了而不是锁红了）：$(printf '%s' "$out" | tail -2)"; exit 1; }
	printf '%s' "$out" | grep -qF -- "$2" || { echo "--- FAIL: §110 F3 ${1}：红的归属不对（期望点名「${2}」，实得尾部：$(printf '%s' "$out" | tail -1)）——本枚破坏必须只有这一枚能造成这个红"; exit 1; }
	echo "ok - §110 F3 ${1} => $(gate_clip 96 "$(printf '%s' "$out" | grep -F 'F2-FAIL' | head -1)")"
}

# ── F3 删行锚自检（10-09 本段自己踩出来的那一条，所以钉在本段正文里）────────────────
# 形状要求：**按数组元素删行**的锚必须对尾逗号容错（写 `,?`）。元素的行文本由它在第几位决定
#   （末项没逗号、中间项有），锚按某一种写法刻死，下一次往名单增删项就会把反证腿自己弄瞎——
#   本批往 roster 末尾追加 $SvcTaskEnsureRestore 时，KeepAlive 那行从末项变中间项，旧锚命中数
#   从 1 掉到 0，红的是反证不是判据；而这种红最容易被当成"环境坏了"绕过去，正是判据失明的开始。
# 两个方向都要钉：待扫调用数≥3（扫描表达式自己写坏＝这枚锁退化成恒绿空循环，§70 空清单同族）、
#   不合格锚数=0。
CNT110=$((CNT110 + 1))
ANCHOR_SCAN=$(python3 - <<'PY110ANCH' || true
import re
t = open("scripts/verify_changes.sh", encoding="utf-8").read()
calls = re.findall(r"""f3_drop_line "\$D110/service_definitions\.ps1" '([^']+)'""", t)
roster_style = [p for p in calls if p.startswith(r"^\s*\$SvcTask")]
bad = [p for p in roster_style if ",?" not in p]
print("%d %d %d %s" % (len(calls), len(roster_style), len(bad), "|".join(bad)))
PY110ANCH
)
read -r ASCALLS ASROSTER ASBAD ASWHAT <<<"${ANCHOR_SCAN:-0 0 0 }"
[ "${ASCALLS:-0}" -ge 3 ] || { echo "--- FAIL: §110 删行锚自检 ${CNT110}（扫到的 f3_drop_line 调用数=${ASCALLS:-0}，<3＝扫描表达式自己写坏了，这枚正在空转；空扫描的锁比没有锁更坏，它让人以为验过）"; exit 1; }
[ "${ASROSTER:-0}" -ge 2 ] || { echo "--- FAIL: §110 删行锚自检 ${CNT110}（按数组元素删行的锚只剩 ${ASROSTER:-0} 处，<2＝a/a2 两条反证有一条被改成了别的形状，本枚自检失去守护对象）"; exit 1; }
[ "${ASBAD:-0}" = "0" ] || { echo "--- FAIL: §110 删行锚自检 ${CNT110}（下列按元素删行的锚没对尾逗号容错：${ASWHAT}＝名单一增删项，反证腿会报「锚没命中」而不是报锁红，红错归属）"; exit 1; }
echo "ok - §110 删行锚自检通过（f3_drop_line 调用 ${ASCALLS} 处，其中按元素删行 ${ASROSTER} 处全部带 ,?）"

# a) 删 roster 里的 keepalive（＝"注册体/名单只有一份"里那份名单少一项）⇒ 集合关系破 + 少出点名
# ★ 删行锚必须对**尾逗号**容错（10-09 实录：本批往 roster 末尾追加 $SvcTaskEnsureRestore 时，
#   KeepAlive 那行从"末项无逗号"变成"中间项带逗号"，旧锚 `...KeepAlive\s*$` 命中数掉到 0，
#   反证腿当场以"锚没命中唯一行"判红）。这不是巧合而是这类锚的固有形状：数组元素的行文本
#   由**它在第几位**决定，锚按某一位的写法写死，下一次增删元素就会把反证自己弄瞎。
#   容错写法 `,?` 让"删掉这一项"这件事与位置无关。
D110="$(f3_rebuild a)"
f3_drop_line "$D110/service_definitions.ps1" '^\s*\$SvcTaskDataloadKeepAlive\s*,?\s*$'
f3_expect_f2_red 'a 删 roster 一项 ⇒ 集合等值必红' "少出=['\$SvcTaskDataloadKeepAlive']" \
	"$D110/service_definitions.ps1" "$D110/register_engine_services.ps1"

# a2) 删 roster 里**本批新加**的那条（QMT-Ensure-Restore-0840）⇒ 同一枚集合锁、点名新元素。
# 为什么既有 a 还要 a2：a 腿验的是 keepalive 那条**既有**关系；本批往里加了一个新元素，
# 不加自己的反证，"集合等值"对新元素就只是"算过了"而不是"验过了"——F2 绿可能因为两边都齐，
# 也可能因为判据压根没看见它（同族：清单式锁对下一个新增项天生失明，§BOM-REPO-DERIVE 那一课）。
D110="$(f3_rebuild a2)"
f3_drop_line "$D110/service_definitions.ps1" '^\s*\$SvcTaskEnsureRestore\s*,?\s*$'
f3_expect_f2_red 'a2 删新任务 roster 一项 ⇒ 集合等值必红并点名它' "少出=['\$SvcTaskEnsureRestore']" \
	"$D110/service_definitions.ps1" "$D110/register_engine_services.ps1"

# b) 删新鲜度规则里那一行（＝任务在名单里却没人规定它该多久跑一次）⇒ 同一枚锁、多出点名
D110="$(f3_rebuild b)"
f3_drop_line "$D110/service_definitions.ps1" 'Name = \$SvcTaskDataloadKeepAlive'
f3_expect_f2_red 'b 删新鲜度规则一项 ⇒ 集合等值必红' "多出=['\$SvcTaskDataloadKeepAlive']" \
	"$D110/service_definitions.ps1" "$D110/register_engine_services.ps1"

# c) 改注册脚本的回退字面量（＝dot-source 失败时备胎自创名字，探针查的名字与建出来的对不上）
D110="$(f3_rebuild c)"
f3_sub "$D110/register_engine_services.ps1" '$SvcTaskDataloadKeepAlive = "QMT-Dataload-KeepAlive"' \
	'$SvcTaskDataloadKeepAlive = "QMT-Dataload-KeepAliveX"'
f3_expect_f2_red 'c 回退字面量与单源不同值 ⇒ 等值必红' '回退字面量与单源不同值' \
	"$D110/service_definitions.ps1" "$D110/register_engine_services.ps1"

# d) 探针侧两枚派生锁各自反证：单源引用少一处 / 代码行里出现字面任务名（两枚破坏各建一份镜像，
#    绝不共享锚——共享锚会让"是哪枚锁红"串味，纪律 4）
CNT110=$((CNT110 + 1))
D110="$(f3_rebuild d)"
f3_sub "$D110/verify_deploy_guangzhou.sh" 'foreach ($krName in @($SvcTaskRoster)) {' \
	'foreach ($krName in @($rosterCopiedIntoProbe)) {'
ROSTER_REF_MIRROR=$(grep -cF '$SvcTaskRoster' "$D110/verify_deploy_guangzhou.sh" || true)
[ "${ROSTER_REF_MIRROR:-0}" != "4" ] || { echo "--- FAIL: §110 F3 d：探针不再遍历单源集合后，引用计数仍是 4（那枚等值锁看不见这件事发生了）"; exit 1; }
echo "ok - §110 F3 d 探针摘掉单源遍历 ⇒ 引用计数等值锁脱离（镜像实得 ${ROSTER_REF_MIRROR}，主仓应 4）"
CNT110=$((CNT110 + 1))
D110="$(f3_rebuild e)"
f3_sub "$D110/verify_deploy_guangzhou.sh" 'foreach ($krName in @($SvcTaskRoster)) {' \
	'foreach ($krName in @($SvcTaskRoster)) {
    $expectedTaskName = "QMT-Dataload-KeepAlive"'
NAME_IN_MIRROR=$(grep -vE '^[[:space:]]*#' "$D110/verify_deploy_guangzhou.sh" | grep -cF 'QMT-Dataload-KeepAlive' || true)
[ "${NAME_IN_MIRROR:-0}" != "0" ] || { echo "--- FAIL: §110 F3 e：名单被抄了第二份，零字面任务名负锁却仍读 0（恒绿装饰）"; exit 1; }
echo "ok - §110 F3 e 抄第二份名单 ⇒ 零字面任务名负锁脱离（镜像实得 ${NAME_IN_MIRROR}，主仓应 0）"
# f) 把定义侧的解析请回那个坏形态（`.Task` 而不是真正的定义文本）⇒ ④c 的正锁与负锁必须**一起**脱离。
#    为什么这段的形状锁也要镜像反证：本机没有 PowerShell，形状之外没有别的证明手段，而"把形状
#    改坏之后它会红"是形状锁唯一能被本机验的事——否则它们与"永远绿的装饰"不可区分（纪律 2）。
CNT110=$((CNT110 + 1))
D110="$(f3_rebuild f)"
f3_sub "$D110/verify_deploy_guangzhou.sh" '$krXml = [xml]$krTaskXmlTxt' '$krXml = ([xml][string]$krTask.Task).Task'
XMLPOS_F=$(grep -cF -- '$krXml = [xml]$krTaskXmlTxt' "$D110/verify_deploy_guangzhou.sh" || true)
XMLNEG_F=$(grep -cF -- '([xml][string]$krTask.Task)' "$D110/verify_deploy_guangzhou.sh" || true)
[ "${XMLPOS_F:-0}" = "0" ] || { echo "--- FAIL: §110 F3 f：定义侧解析已退回坏形态，正锁「XML 解析入口」却仍读到 ${XMLPOS_F}（那枚锁看不见这件事发生＝恒绿装饰）"; exit 1; }
[ "${XMLNEG_F:-0}" -ge 1 ] || { echo "--- FAIL: §110 F3 f：镜像里已写入被根除的读法，复活负锁却仍读 ${XMLNEG_F}（挡不住复活）"; exit 1; }
echo "ok - §110 F3 f 解析请回 .Task 那一式 ⇒ 正锁脱离（读到 ${XMLPOS_F}，主仓应 1）+ 复活负锁转红（读到 ${XMLNEG_F}，主仓应 0）"
# g) 把 Enabled 的假值支换成"直接布尔化"那一式 ⇒ 三枚同时脱离：假值支正锁、跨语言等值锁
#    （PS 侧不再产出判读侧认的那个字面量）、布尔化负锁。这一枚专门盯着**读反**这个形态：
#    它不会让读数变 na（fail-closed 接不到它），只会让"已禁用"变成"已启用"——判据照常绿，
#    现网那条被 /disable 住的守护腿就此无人认领。
CNT110=$((CNT110 + 1))
D110="$(f3_rebuild g)"
f3_sub "$D110/verify_deploy_guangzhou.sh" 'elseif ($krEnNode -eq "false") { $krEnabled = "False" }' \
	'elseif ([bool]$krEnNode) { $krEnabled = "True" }'
ENFALSEG=$(grep -cF -- '$krEnNode -eq "false"' "$D110/verify_deploy_guangzhou.sh" || true)
ENPS_G=$(grep -cF -- "\$krEnabled = \"${ENFALSE110}\"" "$D110/verify_deploy_guangzhou.sh" || true)
BOOLG=$(grep -cF -- '[bool]$krEnNode' "$D110/verify_deploy_guangzhou.sh" || true)
[ "${ENFALSEG:-0}" = "0" ] || { echo "--- FAIL: §110 F3 g：假值支已被换掉，字面量映射正锁却仍读到 ${ENFALSEG}（本枚独有性不成立）"; exit 1; }
# 主仓这里是 2 处（XML 文本支 + State 末级支），镜像弄坏 XML 文本支之后应当只剩 1 处＝State 那一支。
# 期望值跟主仓那枚等值锁同源（见 ④d 上面「应为 2」那行）：这枚反证断的是"XML 文本支脱离"，
# 不是断"映射整体消失"——把它写成 0 会在补第二支的那天自己变红（计数锚随实现漂移同族）。
[ "${ENPS_G:-0}" = "1" ] || { echo "--- FAIL: §110 F3 g：PS 侧已不再映射判读侧认的字面量（XML 文本支），跨语言等值锁却仍读到 ${ENPS_G}（应 1＝只剩 State 支；读到 0 说明 State 支也被这枚破坏顺带弄掉了，本枚独有性不成立）"; exit 1; }
[ "${BOOLG:-0}" -ge 1 ] || { echo "--- FAIL: §110 F3 g：镜像里已写入布尔化那一式，负锁却仍读 ${BOOLG}（读反形态进得来）"; exit 1; }
echo "ok - §110 F3 g 假值支请回布尔化 ⇒ 映射正锁脱离、跨语言等值锁从 2 掉到 ${ENPS_G}、布尔化负锁转红（${ENFALSEG}/${ENPS_G}/${BOOLG}）"
# h) 「读法来源只观测、不判读」那枚负锁的反证：在镜像里真拿 $ens 做一次判读分支，再用**同一把尺子**
#    （同一条正则＋同一条剥 INFO 的管道）去量镜像抽出的判读区。为什么这枚反证必须做在尺子上而不是
#    做在字符串上：那枚负锁读的是一份**抽取出来的临时区**，抽取失败（函数改名、sed 锚点脱钩）时它也读 0，
#    而且读 0 的样子和"确实没有旁路"一模一样——这正是本段最恨的那种恒绿装饰（§70 空清单、删行锚自检同族）。
CNT110=$((CNT110 + 1))
D110="$(f3_rebuild h)"
f3_sub "$D110/verify_deploy_guangzhou.sh" 'act="${line#*|action=}"' \
	'act="${line#*|action=}"
			if [ "$ens" = "none" ]; then norule="${norule} src-probe"; fi'
sed -n '/^judge_task_roster()/,/^}/p' "$D110/verify_deploy_guangzhou.sh" > "$W3F/region_h.sh"
[ -s "$W3F/region_h.sh" ] || { echo "--- FAIL: §110 F3 h：镜像里那份判读区抽不出来（抽取锚与文件脱钩，本枚反证无从谈起）"; exit 1; }
SRCJUDGE_H=$(grep -E '\$\{?(ens|rgs)([^[:alnum:]_]|$)' "$W3F/region_h.sh" 2>/dev/null | grep -v 'echo "INFO' | wc -l | tr -d ' ' || true)
[ "${SRCJUDGE_H:-0}" -ge 1 ] || { echo "--- FAIL: §110 F3 h：镜像里已经把读法来源拿去做判读，同一把尺子却读到 ${SRCJUDGE_H}（＝主仓那枚负锁的读数不来自判读区，它绿是蒙的）"; exit 1; }
echo "ok - §110 F3 h 拿 src 当判据 ⇒ 同一把尺子在镜像上读出台阶（镜像实得 ${SRCJUDGE_H}，主仓应 0）"
# i) 把第二条通道整块弄没（兜底解析式改成一个不可能命中的赋值）⇒ ④d 那枚「第二条通道的解析支在位」
#    正锁必须脱离，而**新加的 obj/xml_from/xml_err 三把观测键仍在**（这一枚断的是"兜底没了"，
#    不是"自证没了"；两件事分开，红的编号才认得出是哪一种删失）。
#    为什么这枚反证值得做：二拨的全部教训就是"通道没通"与"节点本来不存在"在读数上长一样，
#    验兜底的锁如果看不见兜底被删，那它就是在给"下一次又要靠猜"背书。
CNT110=$((CNT110 + 1))
D110="$(f3_rebuild i)"
f3_sub "$D110/verify_deploy_guangzhou.sh" '$krXml = [xml]$krSbTxt' '$krXml = $null'
SBXI=$(grep -cF -- '$krXml = [xml]$krSbTxt' "$D110/verify_deploy_guangzhou.sh" || true)
OBSI=$(grep -cF -- '"|xml_from=" + $krXmlFrom' "$D110/verify_deploy_guangzhou.sh" || true)
[ "${SBXI:-0}" = "0" ] || { echo "--- FAIL: §110 F3 i：外部通道解析支已被弄没，正锁却仍读到 ${SBXI}（看不见兜底被删＝恒绿装饰）"; exit 1; }
[ "${OBSI:-0}" = "1" ] || { echo "--- FAIL: §110 F3 i：这枚只该弄没兜底，观测键 xml_from 却读 ${OBSI}（本枚独有性不成立，红项编号会骗人）"; exit 1; }
echo "ok - §110 F3 i 删掉外部通道兜底 ⇒ 通道正锁脱离（${SBXI}，主仓应 1）、观测键仍在（${OBSI}）"
# j) 把 State 那一步**提到直接来源之前**（镜像里在字面量映射那一支之前先读一次 State，用的是同一个
#    字面量，所以顺序锁那把尺子的"首个命中"会跟着上移）⇒ 第 7 枚顺序锁必须判红。
#    这一枚专门断"派生态排到来源前面"这个形态：语法完全合法、读数不会变 na，
#    只会把"标志位明明写着 false"的任务报成 True＝**读反而绿**（§派生状态不是来源）。
CNT110=$((CNT110 + 1))
D110="$(f3_rebuild j)"
f3_sub "$D110/verify_deploy_guangzhou.sh" 'if ($krEnNode -eq "true") { $krEnabled = "True" }' \
	'if ([string]$krTask.State) { $krSt = [string]$krTask.State }
            if ($krEnNode -eq "true") { $krEnabled = "True" }'
STJA=$(grep -nF -- '$krEnSrc = "info-prop"' "$D110/verify_deploy_guangzhou.sh" | head -1 | cut -d: -f1 || true)
STJB=$(grep -nF -- '$krSt = [string]$krTask.State' "$D110/verify_deploy_guangzhou.sh" | head -1 | cut -d: -f1 || true)
[ "${STJA:-0}" -gt 0 ] && [ "${STJB:-0}" -gt 0 ] || { echo "--- FAIL: §110 F3 j：镜像里两个锚抽不出行号（${STJA}/${STJB}），这枚反证无从谈起"; exit 1; }
[ "${STJB:-0}" -lt "${STJA:-0}" ] || { echo "--- FAIL: §110 F3 j：镜像里 State 已挪到直接来源之前，第 7 枚顺序锁的两把尺子却仍读成「来源在前」（${STJB} vs ${STJA}）＝那枚锁看不见挪位"; exit 1; }
echo "ok - §110 F3 j 派生态挪到来源之前 ⇒ 第 7 枚顺序锁的尺子在镜像上读到 State@${STJB} 早于 info-prop@${STJA}（主仓应相反）"
# k) 把竖线中和那一步**改成不做**（保留非 ASCII 剥离，因为那一步与本枚无关）⇒ ④d 新钉的那枚
#    「行中段自由文本先中和竖线」正锁必须脱离，而三条观测键进协议的读数一根不许动。
#    这一枚断的是"值撑破协议"这一族：它不改变任何判读分支的语法，只在异常文本真的带出 `|` 那天
#    让判读键被抽错——所以正锁必须能在破坏后立刻看见，而不是等现网拨出一串说不清的读数。
CNT110=$((CNT110 + 1))
D110="$(f3_rebuild k)"
f3_sub "$D110/verify_deploy_guangzhou.sh" '$krXmlErr = ($krXmlErr -replace '"'"'\|'"'"', '"'"'+'"'"')' '$krXmlErr = $krXmlErr'
PIPEK=$(grep -cF -- "-replace '\|', '+'" "$D110/verify_deploy_guangzhou.sh" || true)
OBJK=$(grep -cF -- '"|obj=" + $krObj' "$D110/verify_deploy_guangzhou.sh" || true)
[ "${PIPEK:-0}" = "0" ] || { echo "--- FAIL: §110 F3 k：竖线中和已被弄没，正锁却仍读到 ${PIPEK}（看不见这一步被删＝那枚锁是恒绿装饰）"; exit 1; }
[ "${OBJK:-0}" = "1" ] || { echo "--- FAIL: §110 F3 k：这枚只该弄没中和那一步，观测键 obj 却读 ${OBJK}（本枚独有性不成立，红项编号会骗人）"; exit 1; }
echo "ok - §110 F3 k 删掉竖线中和 ⇒ 该枚正锁脱离（${PIPEK}，主仓应 1）、观测键仍在（${OBJK}）"
# 复位自检：镜像全量重建后必须回到与主仓同一读数（跑完整轮反证后主仓文件本就不该被动过）
CNT110=$((CNT110 + 1))
python3 "$W3F/f2.py" deploy/qmt-win/service_definitions.ps1 deploy/qmt-win/register_engine_services.ps1 >/dev/null \
	|| { echo "--- FAIL: §110 F3 复位自检红（反证跑完主仓判据不再通过＝镜像改动串回了主仓，纪律 3 那一条）"; exit 1; }
if git status --porcelain -- deploy/qmt-win scripts/verify_deploy_guangzhou.sh | grep -qE '^\?\?'; then
	echo "--- FAIL: §110 F3 反证在主仓目录留了新文件（镜像必须只长在临时树里）"; exit 1
fi
echo "ok - §110 F3 双向镜像反证通过（roster/规则/回退字面量三枚走同一份 python 判据，探针侧各枚走同一份锁本体；枚数不在此手写，红项的编号才是账）"

# ──────────────────────────────────────────────────────────────────────────────
# F1 判读函数行为腿：十九合成读数 + 十二枚摘锁反证
#   （§KA-TASKREG 把红绿判断从 PowerShell 搬到 bash 的全部理由，就是这一段能在本机真跑）
# ──────────────────────────────────────────────────────────────────────────────
CNT110=$((CNT110 + 1))
W3T="$(mktemp -d 2>/dev/null || true)"
[ -n "$W3T" ] && [ -d "$W3T" ] || { echo "--- FAIL: §110 F1 连临时目录都建不出来（后面的合成腿全部无从谈起）"; exit 1; }
mkdir -p "$W3T/bin"
sed -n '/^judge_task_roster()/,/^}/p' scripts/verify_deploy_guangzhou.sh > "$W3T/fn.sh"
[ -s "$W3T/fn.sh" ] || { echo "--- FAIL: §110 F1 搭建失败（judge_task_roster 抽不出来，改名/挪位＝这条腿在验一个不存在的东西）"; exit 1; }
grep -q '^judge_task_roster()' "$W3T/fn.sh" || { echo "--- FAIL: §110 F1 抽出区间首锚不是函数声明（sed 范围切歪了）"; exit 1; }
[ "$(tail -n 1 "$W3T/fn.sh")" = "}" ] || { echo "--- FAIL: §110 F1 抽出区间末行不是 }（多切/少切）"; exit 1; }
cat > "$W3T/run.sh" <<'RUN110'
#!/bin/bash
# 把合成读数喂给判读函数，只取它的判决行（PASS|/FAIL|）。$1=函数文件 $2=读数（可多行）
set -uo pipefail
# shellcheck source=/dev/null
. "$1"
printf '%s\n' "$2" | judge_task_roster | grep -E '^(PASS|FAIL)\|' | head -1
RUN110
chmod +x "$W3T/run.sh"
# 第二个跑法：整段输出都留下（INFO 也是判据的一部分，观测行的价值只能从 INFO 里读）。
# 为什么不给 run.sh 加参数：run.sh 只回判决行，那一行的形状被 §111 钉死了（gate_clip 那枚锁
# 认的就是 leg110 的 echo），加参数等于动那条锁的锚——老跑法一个字不动，新跑法另起一份。
cat > "$W3T/runall.sh" <<'RUN110ALL'
#!/bin/bash
# 把合成读数喂给判读函数，INFO 与判决一起回（$1=函数文件 $2=读数，可多行）
set -uo pipefail
# shellcheck source=/dev/null
. "$1"
printf '%s\n' "$2" | judge_task_roster
RUN110ALL
chmod +x "$W3T/runall.sh"

leg110() { # $1=用例名 $2=期望 PASS|FAIL $3=FAIL 明细必须点名串 $4=读数
	local got
	CNT110=$((CNT110 + 1))
	got="$(bash "$W3T/run.sh" "$W3T/fn.sh" "$4" || true)"
	[ "$(printf '%s\n' "$got" | grep -cE '^(PASS|FAIL)\|' || true)" = "1" ] || {
		echo "--- FAIL: §110 F1 ${1}：判决行数≠1（一次判读必须给出恰好一条结论；两条＝两本账，零条＝没人判）"; exit 1; }
	[ "${got%%|*}" = "$2" ] || {
		echo "--- FAIL: §110 F1 ${1}：期望 $2 实得 ${got%%|*} :: ${got}"; exit 1; }
	if [ "$2" = "FAIL" ] && [ -n "$3" ] && ! printf '%s' "$got" | grep -qF -- "$3"; then
		echo "--- FAIL: §110 F1 ${1}：判红了却没点名 '$3'（红必须说清是哪个任务、因为什么，否则现网还得人工拆读数）"; exit 1;
	fi
	echo "ok - §110 F1 ${1} => $(gate_clip 92 "$got")"
}

legnote110() { # $1=用例名 $2=期望 PASS|FAIL $3=判决必须点名串 $4=整段输出必须包含串 $5=读数
	local all V
	CNT110=$((CNT110 + 1))
	all="$(bash "$W3T/runall.sh" "$W3T/fn.sh" "$5" || true)"
	V="$(printf '%s\n' "$all" | grep -E '^(PASS|FAIL)\|' | head -1 || true)"
	[ "$(printf '%s\n' "$all" | grep -cE '^(PASS|FAIL)\|' || true)" = "1" ] || {
		echo "--- FAIL: §110 F1 ${1}：判决行数≠1（观测腿同样只能给一条结论；两条＝两本账，零条＝判读函数没走到汇总）"; exit 1; }
	[ "${V%%|*}" = "$2" ] || { echo "--- FAIL: §110 F1 ${1}：期望 $2 实得 ${V}"; exit 1; }
	if [ "$2" = "FAIL" ] && [ -n "$3" ] && ! printf '%s' "$V" | grep -qF -- "$3"; then
		echo "--- FAIL: §110 F1 ${1}：判红了却没点名 '$3'"; exit 1;
	fi
	# 第四臂是本腿存在的理由：判决对了不算完，**读数必须真的打印给看的人**
	# （观测行只进内部变量、INFO 行被吞掉＝"加了仪表却没接线"，10-09 复盘的那条形态）。
	printf '%s\n' "$all" | grep -qF -- "$4" || {
		echo "--- FAIL: §110 F1 ${1}：整段输出里没有「${4}」（判决对但读数没落地＝现网仍然查不到那一手信息）"; exit 1; }
	echo "ok - §110 F1 ${1} => $(gate_clip 92 "$V")"
}

KA110_OK='TASK|QMT-Gateway-Ensure|present=1|rule=2|age_h=0.3|state=present+lastrun|enabled=True|action=wscript.exe //B C:\opt\quant\qmt-win\run_qmt_ensure.vbs'
leg110 'a 周期任务在位且新鲜判绿' PASS "" "$KA110_OK"
leg110 'b 超龄判红并点名 stale' FAIL "stale=" 'TASK|quant-backup-snap|present=1|rule=30|age_h=72.5|state=present+lastrun|enabled=True|action=powershell -File backup_snapshot.ps1'
leg110 'c keepalive 不在位判红并点名 absent' FAIL "absent=" 'TASK|QMT-Dataload-KeepAlive|present=0|rule=30|age_h=na|state=absent|enabled=na|action='
leg110 'c2 缺席且时间字段新鲜：仍报 absent 不报 stale（归属优先级＝缺席比新鲜度更根本）' FAIL "absent=" 'TASK|QMT-Dataload-KeepAlive|present=0|rule=30|age_h=1.0|state=present+lastrun|enabled=True|action=x'
leg110 'd 新任务没定阈值判红（不许默认放行）' FAIL "no-freshness-rule=" 'TASK|Quant-New-Thing|present=1|rule=none|age_h=1.0|state=present+lastrun|enabled=True|action=x'
leg110 'e 上次运行时间读不出判红（本机验不了 PS 权限，取向是 fail-closed：读不到就红）' FAIL "last-run-unreadable=" 'TASK|quant-all-wd|present=1|rule=2|age_h=na|state=info-unreadable|enabled=na|action='
leg110 'f ONLOGON 仅查在位：没有"上次运行时间"这个概念也不算病' PASS "" 'TASK|QMT-Gateway-Logon|present=1|rule=inplace|age_h=na|state=present+lastrun|enabled=True|action=powershell -File start_gateway.ps1'
leg110 'g 整段零读数判红（判据失明≠全部健康）' FAIL "no-task-readings" ""
leg110 'h 负 age 单独点名 clock-skew（不许与"读不出"混成一个标签）' FAIL "clock-skew" 'TASK|Quant-Log-Prune|present=1|rule=30|age_h=-40.2|state=present+lastrun|enabled=True|action=x'
leg110 'i 周期任务被禁用判红（现网 /disable 是临时的，长期停着必须吵）' FAIL "disabled=" 'TASK|QMT-Ensure-Running|present=1|rule=4|age_h=0.5|state=present+lastrun|enabled=False|action=x'
leg110 'j 动作行含竖线不劈字段' PASS "" 'TASK|QMT-Dataload-KeepAlive|present=1|rule=30|age_h=3.5|state=present+lastrun|enabled=True|action=cmd /c a.py || cmd /c b.py'
leg110 'k 单源 dot-source 失败走 defs-unreadable 且判红' FAIL "no-freshness-rule=" 'TASK|__service_definitions__|present=0|rule=none|age_h=na|state=defs-unreadable|enabled=na|action='
leg110 'l 多任务里一枚超龄即整探针红（其余绿不掩盖它）' FAIL "stale=" "$(printf '%s\n%s' "$KA110_OK" 'TASK|quant-backup-snap|present=1|rule=30|age_h=99.9|state=present+lastrun|enabled=True|action=y')"

# ── m…s：10-09 首拨之后补的七条（从未运行两态 + 注册龄 fail-closed + 观测行 + 旧格式兼容）──
# 这批读数是**照首拨现网真实形态写的**，不是编的：quant-backup-snap 每次发版被 [2e] 删建重注册，
# 上次运行读成 never 而触发点是次日 04:00 —— 旧判据把这种正常形态读成红（先哨兵值 235445.9h，
# 挡住后又变成"从未运行"），m 腿就是那条假红的复现；n/p 腿证明"装了很久却一次没跑"仍然必须红，
# 否则挡住假红的同时把真红也一起放掉了（等值锁非单向锁：只往绿的方向修＝把判据修没了）。
leg110 'm 从未运行但注册龄还在阈值内＝刚装上来，不判红（发版日假红的复现腿）' PASS "" 'TASK|quant-backup-snap|present=1|rule=30|age_h=never|state=never-run|enabled=True|reg_h=5.0|last_run=na|action=powershell -File backup_snapshot.ps1'
leg110 'n 从未运行且注册龄超阈值＝该跑没跑，判红并点名 never-run-beyond-rule' FAIL "never-run-beyond-rule=" 'TASK|QMT-Dataload-KeepAlive|present=1|rule=30|age_h=never|state=never-run|enabled=True|reg_h=120.0|last_run=na|action=cmd /c python x.py'
leg110 'o 从未运行且注册龄读不出（没有 reg_h 这个键）＝fail-closed 判红，不许当"新装的"' FAIL "reg_h=missing" 'TASK|Quant-Log-Prune|present=1|rule=30|age_h=never|state=never-run|enabled=True|action=powershell -File prune_logs.ps1'
leg110 'p 注册龄是负数＝现网时钟/注册节点异常，单独点名而不是洗成绿' FAIL "reg_h=-3.2" 'TASK|QMT-Gateway-Ensure|present=1|rule=2|age_h=never|state=never-run|enabled=True|reg_h=-3.2|last_run=na|action=x'
# q/s 走 legnote110：观测行的价值全在 INFO 里，判决行看不出来（只断 PASS 会放过"分支在但清单没打印"）。
legnote110 'q 只有观测行时判绿，且现网同族清单真的打印出来' PASS "" '现网同族任务 3 条' 'TASK|__live_names__|present=3|rule=none|age_h=na|state=live-names|enabled=na|action=QMT-Ensure-Gateway,Quant-Log-Prune,QMT-Qmtctl-Ensure'
leg110 'r 旧格式读数（没有 reg_h/last_run 两个新键）+ 数字年龄照旧判绿＝协议向后兼容' PASS "" 'TASK|quant-backup-snap|present=1|rule=30|age_h=12.0|state=present+lastrun|enabled=True|action=powershell -File backup_snapshot.ps1'
# s 是本批加观测行要回答的那个问题：present=0 到底是"根本没装"还是"装了但改了名"。
# 前者要注册，后者要先查是谁改的名——处置不同，所以缺席行必须把同族清单带在身边。
legnote110 's 缺席任务把同族清单一起报出来（absent 的处置由清单决定）' FAIL "absent=" 'live_names=QMT-Ensure-Gateway,quant-all-watchdog-old' "$(printf '%s\n%s' 'TASK|__live_names__|present=2|rule=none|age_h=na|state=live-names|enabled=na|action=QMT-Ensure-Gateway,quant-all-watchdog-old' 'TASK|quant-all-wd|present=0|rule=inplace|age_h=na|state=absent|enabled=na|action=')"

mut110() { # 摘锁反证：$1=标签 $2=原串（必须恰好出现一次）$3=替换串 $4=读数 $5=摘锁后期望判决
	local got
	CNT110=$((CNT110 + 1))
	python3 - "$W3T/fn.sh" "$W3T/fn_mut.sh" "$2" "$3" <<'PY110MUT' || { echo "--- FAIL: §110 反证 ${1}：变异没落地（原串出现次数≠1，破坏打偏或已被改写）"; exit 1; }
import sys
src, out, old, new = sys.argv[1:5]
t = open(src, encoding="utf-8").read()
n = t.count(old)
if n != 1:
    sys.stderr.write("MUT-NOT-LANDED: 原串在判读函数里出现 %d 次（要求恰好 1 次）\n" % n)
    sys.exit(3)
open(out, "w", encoding="utf-8").write(t.replace(old, new))
print("MUT_APPLIED")
PY110MUT
	got="$(bash "$W3T/run.sh" "$W3T/fn_mut.sh" "$4" || true)"
	[ "${got%%|*}" = "$5" ] || {
		echo "--- FAIL: §110 反证 ${1}：摘锁后期望 $5 实得 ${got%%|*}（${got}）——说明该判据本来就没起作用，正向腿是蒙对的"; exit 1; }
	echo "ok - §110 反证 ${1}（摘锁 => ${5}）"
}
# M1–M7：每条摘掉一处判据，对应合成读数必须翻转——这证明那条判决是这条判据给的。
mut110 'M1 摘"零读数判红"正锁 ⇒ g 转绿' '[ "$n" -eq 0 ]' '[ "$n" -eq 99999 ]' "" PASS
mut110 'M2 摘超龄比较 ⇒ b 转绿' 'exit !($age > $rule)' 'exit 1' 'TASK|quant-backup-snap|present=1|rule=30|age_h=72.5|state=present+lastrun|enabled=True|action=powershell -File backup_snapshot.ps1' PASS
# M3 的读数刻意选"缺席但时间字段新鲜"那种（age=1.0h、rule=30）：如果用 c 那条（age=na）做反证，
# 摘掉在位判定后它会被下游"读不出上次运行"接住、照样红，于是这条反证报的是"锁没起作用"，
# 而真相是"红有两条独立来源"——反证读数必须只让被摘的那一条判据负责，否则测的是归属而不是效果。
mut110 'M3 摘在位判定 ⇒ 缺席且时间新鲜的任务转绿（本探针的立身之本）' '[ "$pres" != "1" ]' '[ "1" != "1" ]' 'TASK|QMT-Dataload-KeepAlive|present=0|rule=30|age_h=1.0|state=present+lastrun|enabled=True|action=x' PASS
mut110 'M4 摘"没定阈值判红" ⇒ d 转绿（漏定阈值将静默通过）' '[ -n "$norule" ] && bad="${bad} no-freshness-rule=${norule}"' '[ -n "" ] && bad="$bad"' 'TASK|Quant-New-Thing|present=1|rule=none|age_h=1.0|state=present+lastrun|enabled=True|action=x' PASS
mut110 'M5 摘"读不出上次运行判红" ⇒ e 转绿（fail-closed 退成 fail-open）' '[ -n "$unread" ] && bad="${bad} last-run-unreadable=${unread}"' '[ -n "" ] && bad="$bad"' 'TASK|quant-all-wd|present=1|rule=2|age_h=na|state=info-unreadable|enabled=na|action=' PASS
mut110 'M6 摘禁用判定 ⇒ i 转绿' '[ -n "$disabled" ] && bad="${bad} disabled=${disabled}"' '[ -n "" ] && bad="$bad"' 'TASK|QMT-Ensure-Running|present=1|rule=4|age_h=0.5|state=present+lastrun|enabled=False|action=x' PASS
# M7 方向相反，也最容易写错：摘掉"仅查在位"的提前放行，f 必须**转红**。
# 它证的是 §107 DRILL-C 那一课——ONLOGON/ONSTART 任务的上次运行时间不由时钟决定，
# 拿它当健康度判据会在健康现网上永远红，而"永远红的锁"教出来的是所有人忽略红。
mut110 'M7 摘 rule=inplace 提前放行 ⇒ f 转红（这条保护是有意加的，不是漏判）' '[ "$rule" = "inplace" ]' '[ "$rule" = "zz-never-match" ]' 'TASK|QMT-Gateway-Logon|present=1|rule=inplace|age_h=na|state=present+lastrun|enabled=True|action=powershell -File start_gateway.ps1' FAIL
# ── M8…M12：10-09 新加的三条判读（从未运行分流 / 注册龄比较 / 注册龄未知 fail-closed / 观测行分支）各配一枚 ──
# 每枚用**自己独有**的读数（纪律 4：段内首红即退，归属不能串）：
#   M8 摘分流 ⇒ m 转红（证明"挡住假红"这件事是分流给的，不是年代界自己做到——年代界在 PS 一侧，本机跑不到）
#   M9 摘注册龄比较 ⇒ n 转绿（证明"装了很久没跑"这条真红靠的是那次比较）
#   M10 摘 fail-closed 收集 ⇒ o 转绿（证明未知值不会自己变红；这一枚是本段最容易被"简化"掉的一条）
#   M11 摘 notrun 汇总 ⇒ p 转绿（收集到了却没进 bad＝白收集，同 §107"判据算了但没人判"）
#   M12 摘观测行分支 ⇒ q 转红（观测行落到在位判定，凭空多一条 absent 红——那枚专用分支是有承重作用的）
mut110 'M8 摘"从未运行"分流 ⇒ m 转红（分流不可达时 never 被读不出接住，假红复活）' '[ "$age" = "never" ]' '[ "$age" = "zz-never-match" ]' 'TASK|quant-backup-snap|present=1|rule=30|age_h=never|state=never-run|enabled=True|reg_h=5.0|last_run=na|action=powershell -File backup_snapshot.ps1' FAIL
mut110 'M9 摘注册龄与阈值的比较 ⇒ n 转绿（装了 120h 一次没跑会被无声放行）' 'exit !($reg > $rule)' 'exit 1' 'TASK|QMT-Dataload-KeepAlive|present=1|rule=30|age_h=never|state=never-run|enabled=True|reg_h=120.0|last_run=na|action=cmd /c python x.py' PASS
mut110 'M10 摘注册龄未知的 fail-closed 收集 ⇒ o 转绿（"没有这个键"从红变成绿＝反向失效）' 'notrun="${notrun} ${name}(age=never;reg_h=${reg:-missing})"' 'notrun="${notrun}"' 'TASK|Quant-Log-Prune|present=1|rule=30|age_h=never|state=never-run|enabled=True|action=powershell -File prune_logs.ps1' PASS
mut110 'M11 摘 never-run 汇总进 bad ⇒ p 转绿（收集了却不点名＝判据算了没人判）' '[ -n "$notrun" ] && bad="${bad} never-run-beyond-rule=${notrun}"' '[ -n "" ] && bad="$bad"' 'TASK|QMT-Gateway-Ensure|present=1|rule=2|age_h=never|state=never-run|enabled=True|reg_h=-3.2|last_run=na|action=x' PASS
mut110 'M12 摘观测行专用分支 ⇒ q 转红（清单行被当成任务在位判定，凭空多一条 absent）' '[ "$name" = "__live_names__" ]' '[ "$name" = "zz-live-names" ]' 'TASK|__live_names__|present=3|rule=none|age_h=na|state=live-names|enabled=na|action=QMT-Ensure-Gateway,Quant-Log-Prune,QMT-Qmtctl-Ensure' FAIL
rm -f "$W3T/fn_mut.sh"

# ──────────────────────────────────────────────────────────────────────────────
# F3 Mac 侧行为腿：单实现三态 / 空主题不请求 / 缺 lib 拒跑 / 安装器零改动 / 迁移器不回 argv
# ──────────────────────────────────────────────────────────────────────────────
# fake security 桩 + 假钥匙串状态文件：这三条腿全程只碰 ${W3T}，**本机真实钥匙串零接触**
# （写钥匙串属本地凭据变更，不在本批自动执行面内；桩把收到的命令行整串记进 SEC_LOG，
#   供"值不许进 argv"这条断言读——argv 同机任何进程 ps 可见，等于把凭据从 git 历史搬到运行期明文）。
cat > "$W3T/bin/security" <<'SHIM110'
#!/bin/bash
# 假钥匙串桩：状态文件 $FAKE_KC 的**存在**＝条目在位、**内容**＝口令值（允许为空）。
# ★ 这一版按 2026-10-10 的真机读数重写，不是按我以为的语义写：旧桩把「-w 结尾不带值」实现成
#   "从 stdin 读口令"，而真机那条 prompt 的是**终端**——管道里喂进去的值整串丢掉、条目照样建出来、
#   口令字段为空、rc 还是 0。桩照着假语义实现 ⇒ 这条坏通道在"门禁 113 段全绿"里活了一整轮，
#   直到 owner 授权真拨才被迁移器自己的读回兜底拦下（got=empty）。§MAC-DRIFT 是同一课的第二次：
#   绿的是桩，不是真实现。下面每条都在注释里标了它由哪次实测背书。
# 脚印分两条通道记（"值不许进 argv"这条锁要有可判的对象）：
#   CALL: …      ＝进程 argv（同机任何进程 ps 都读得到＝泄露面）
#   STDIN-CMD: … ＝ security -i 从 stdin 收到的那行命令行（本仓采用的通道，值在这里不算泄露）
mode=""
for a in "$@"; do
	case "$a" in
		-i | --interactive) mode=inter ;;
		find-generic-password) mode=find ;;
		add-generic-password) mode=add ;;
	esac
done
[ -n "${SEC_LOG:-}" ] && printf 'CALL: %s\n' "$*" >> "$SEC_LOG"
# 实测①：find 不带 -w 只看条目在位性；带 -w 时**空口令条目照样退 0、stdout 为空**
#   （旧桩写成"无值退 1"，那是我以为的；真机上 quant-ntfy-topic 那条空残骸退的是 0）
if [ "$mode" = "find" ]; then
	if [ -z "${FAKE_KC:-}" ] || [ ! -e "$FAKE_KC" ]; then exit 1; fi
	wanted=0
	for a in "$@"; do
		if [ "$a" = "-w" ]; then wanted=1; fi
	done
	if [ "$wanted" = "1" ]; then cat "$FAKE_KC"; fi
	exit 0
fi
# kc_write：唯一落盘入口。SHIM_WRITE_EMPTY=1 让"写入侧 rc=0 但值没进去"这一形态可被门禁复现
#   （就是这次真拨撞到的那台机的行为），F3-5b 靠它把"写后读回"从一句主张变成一条判据。
kc_write() {
	if [ "${SHIM_WRITE_EMPTY:-0}" = "1" ]; then
		: > "${FAKE_KC:-/dev/null}"
	else
		printf '%s' "$1" > "${FAKE_KC:-/dev/null}"
	fi
}
# refuse_dup <命令行>：条目已在位、而这次没给 -U ＝ 真机 already exists（实测③）。退 0＝这次该拒。
refuse_dup() {
	[ -e "${FAKE_KC:-/nonexistent}" ] || return 1
	if printf '%s' "$1" | grep -qE -- '(^|[[:space:]])-U([[:space:]]|$)'; then return 1; fi
	echo "add-generic-password: The specified item already exists in the keychain." >&2
	return 0
}
if [ "$mode" = "inter" ]; then
	line="$(head -n 1 | tr -d '\r\n' || true)"
	[ -n "${SEC_LOG:-}" ] && printf 'STDIN-CMD: %s\n' "$line" >> "$SEC_LOG"
	case "$line" in
		*add-generic-password*)
			if refuse_dup "$line"; then exit 1; fi
			# 实测⑤：交互行按双引号取值，值里的空格与 # 原样进条目（探针值 'a b c!d'、'a#b c' 各真拨一次）
			val=""
			if printf '%s' "$line" | grep -qE -- '-w +"[^"]*"$'; then
				val="$(printf '%s' "$line" | sed -E 's/.*-w +"([^"]*)"/\1/')"
			fi
			kc_write "$val"
			exit 0
			;;
		*find-generic-password*)
			if [ -e "${FAKE_KC:-/nonexistent}" ]; then exit 0; fi
			exit 1
			;;
	esac
	# 实测②：交互模式把内部命令的退出码透传回来（unknown command 退 1、坏参数退 2）
	#   ⇒ "security 没报错"这句话从此有意义；但正文仍不把读回当摆设（见 F3-5b）。
	echo "security: unknown command in interactive mode" >&2
	exit 1
fi
if [ "$mode" = "add" ]; then
	if refuse_dup "$*"; then exit 1; fi
	prev=""
	for a in "$@"; do
		if [ "$prev" = "-w" ] && [ -n "$a" ]; then kc_write "$a"; exit 0; fi
		prev="$a"
	done
	# 实测④：`-w` 在 argv 里是最后一个选项、后面没值 ⇒ 真机去终端要口令，管道里的值它不看，
	#   结果＝条目建出来、口令为空、rc=0。旧桩在这里读 stdin，正是把假语义写进了判据面。
	kc_write ""
fi
exit 0
SHIM110
chmod +x "$W3T/bin/security"
# 桩自己也要 bash -n：heredoc 里的内容对**外层脚本**的 bash -n 是不可见的（它是数据不是代码），
# 桩一有语法错就会以"取不到主题"的形态把三条腿判红——那正好是"判据的失败原因不是我以为的原因"
# 那一族（本批在 kuma_seed.js 上刚踩过一次：MODULE_NOT_FOUND 冒充了 SEED_FAIL）。
bash -n "$W3T/bin/security" || { echo "--- FAIL: §110 F3 桩脚本本身语法不过（先修桩，红的是桩不是判据）"; exit 1; }
TOK110='gatetok-preview-7f3'   # 刻意不是 32-hex：本仓有"仓库内不许出现 32 位十六进制"负锁，桩值不能自己踩线
KC110="$W3T/fake_kc"
: > "$KC110"

# F3-1 主题单实现三态（env 优先 / 钥匙串兜底 / 皆则 rc=1 且 stdout 空）+ report 不回显明文
CNT110=$((CNT110 + 1))
out110="$(PATH="$W3T/bin:$PATH" NTFY_TOPIC="$TOK110" bash -c '. deploy/mac/ntfy_topic.sh; ntfy_topic_resolve' || true)"
[ "$out110" = "$TOK110" ] || { echo "--- FAIL: §110 F3-1 env 腿取不到主题（got='${out110}'）——resolve 的优先级是 env 先，不认 env 就是安装器白给"; exit 1; }
out110="$(PATH="$W3T/bin:$PATH" NTFY_TOPIC="$TOK110" bash -c '. deploy/mac/ntfy_topic.sh; ntfy_topic_report leg1' || true)"
printf '%s' "$out110" | grep -qF "topic_len=${#TOK110}" || { echo "--- FAIL: §110 F3-1 report 没报长度（got='${out110}'）——排查时无法判断两处是不是同一份值"; exit 1; }
if printf '%s' "$out110" | grep -qF -- "$TOK110"; then echo "--- FAIL: §110 F3-1 report 把主题明文打出来了（'只报长度与指纹'这条铁律的落点就是 report，破了它全废）"; exit 1; fi
printf '%s' "$TOK110" > "$KC110"
out110="$(PATH="$W3T/bin:$PATH" FAKE_KC="$KC110" bash -c 'unset NTFY_TOPIC; . deploy/mac/ntfy_topic.sh; ntfy_topic_resolve' || true)"
[ "$out110" = "$TOK110" ] || { echo "--- FAIL: §110 F3-1 钥匙串腿取不到主题（got='${out110}'）——launchd 常驻形态没有 env，正式来源就是钥匙串"; exit 1; }
: > "$KC110"
out110="$(PATH="$W3T/bin:$PATH" FAKE_KC="$KC110" bash -c 'unset NTFY_TOPIC; . deploy/mac/ntfy_topic.sh; ntfy_topic_resolve' || true)"
[ -z "$out110" ] || { echo "--- FAIL: §110 F3-1 无主题时 resolve 应 stdout 空（got='${out110}'）"; exit 1; }
PATH="$W3T/bin:$PATH" FAKE_KC="$KC110" bash -c 'unset NTFY_TOPIC; . deploy/mac/ntfy_topic.sh; ntfy_topic_resolve >/dev/null' && { echo "--- FAIL: §110 F3-1 无主题时 resolve 必须非零退出（调用方靠 rc 决定走 ALERT-NOT-SENT）"; exit 1; }
out110="$(PATH="$W3T/bin:$PATH" FAKE_KC="$KC110" bash -c 'unset NTFY_TOPIC; . deploy/mac/ntfy_topic.sh; ntfy_topic_report leg2 || true' || true)"
printf '%s' "$out110" | grep -qF 'topic=ABSENT' || { echo "--- FAIL: §110 F3-1 report 缺 ABSENT 文案（got='${out110}'）——「没配」必须与「配了但值为空」可区分"; exit 1; }
echo "ok - §110 F3-1 主题单实现三态（env/钥匙串/皆无）+ report 不回显明文"

# F3-2 空主题不请求 ntfy：alert() 走 ALERT-NOT-SENT，一次 curl 都不发；有主题则必须真发
CNT110=$((CNT110 + 1))
sed -n '/^alert() {/,/^}/p' deploy/mac/restic_pull_backup.sh > "$W3T/alert.sh"
grep -q '^alert() {' "$W3T/alert.sh" || { echo "--- FAIL: §110 F3-2 alert() 抽不出来（sed 锚点与文件脱钩＝这条腿在验空气）"; exit 1; }
cat > "$W3T/alert_run.sh" <<'AR110'
#!/bin/bash
# 在隔离环境里跑拉取腿的 alert()：log 走 stdout、curl 走**标记文件**，$1=alert 函数文件
# $2=主题 $3=标题 $4=正文 $5=标记文件路径。
# 为什么 curl 桩不能 echo 到 stdout：真 alert() 里那句是 `curl ... >/dev/null 2>&1`，
# 用 stdout 打点会被它自己的重定向吞掉 ⇒ 出现"有主题时没调 curl"的假红（本批实测踩过：
# 判据的红必须来自被测代码，不能来自观测面选错）。
set -uo pipefail
log() { echo "LOG:$*"; }
# 桩里不能直接写 "$5"：函数被调用时 $n 会被换成**函数自己的**参数（curl 的第 5 个参数是 URL，
# 不是标记文件路径）——先在外层把路径落成全局量，桩体只读全局量。
CURL_MARK="$5"
# 记账用追加不覆盖（重试腿要数"到底拨了几次"）；CURL_FAIL=1 ⇒ 恒败（重试腿的恒败夹具）。
curl() { printf '%s\n' "$*" >> "$CURL_MARK"; if [ -n "${CURL_FAIL:-}" ]; then return 7; fi; return 0; }
# shellcheck source=/dev/null
. "$1"
# 重试上界/间隔的 env 缺省定义在脚本顶部、不在 alert() 体内（sed 只抽函数＝抽不到）；
# 夹具必须自己补上同款缺省，否则 set -u 下 alert 一进重试循环就 unbound 中止＝假红。
NTFY_ALERT_ATTEMPTS="${NTFY_ALERT_ATTEMPTS:-3}"
NTFY_ALERT_BACKOFF_S="${NTFY_ALERT_BACKOFF_S:-5}"
NTFY_URL="https://ntfy.invalid"
NTFY_TOPIC="$2"
alert "$3" "$4" high
AR110
rm -f "$W3T/curl.mark"
out110="$(bash "$W3T/alert_run.sh" "$W3T/alert.sh" "" "quant 备份失败" "restic copy 失败" "$W3T/curl.mark" 2>&1 || true)"
[ "$(printf '%s\n' "$out110" | grep -c 'ALERT-NOT-SENT' || true)" = "1" ] || { echo "--- FAIL: §110 F3-2 空主题没有恰好一行 ALERT-NOT-SENT（got 见下）：$(printf '%s' "$out110" | head -3)"; exit 1; }
[ -e "$W3T/curl.mark" ] && { echo "--- FAIL: §110 F3-2 空主题仍然请求了 ntfy（打到根路径吃 404，再把原因写成「网络？」＝把配置缺失伪装成网络抖动）"; exit 1; }
printf '%s' "$out110" | grep -qF '网络？' && { echo "--- FAIL: §110 F3-2 空主题分支出现「网络？」文案（没发请求就不该甩锅网络）"; exit 1; }
printf '%s' "$out110" | grep -qF 'restic copy 失败' || { echo "--- FAIL: §110 F3-2 空主题没把正文整条落日志（发不出去时日志是唯一证据面）"; exit 1; }
rm -f "$W3T/curl.mark"
out110="$(bash "$W3T/alert_run.sh" "$W3T/alert.sh" "$TOK110" t b "$W3T/curl.mark" 2>&1 || true)"
[ -e "$W3T/curl.mark" ] || { echo "--- FAIL: §110 F3-2 有主题时没调 curl＝整条 alert 是死支，上一条空主题分支的绿一并作废"; exit 1; }
grep -qF "https://ntfy.invalid/$TOK110" "$W3T/curl.mark" || { echo "--- FAIL: §110 F3-2 curl 的 URL 里没带主题（发不到那个主题＝没有告警）：$(tail -1 "$W3T/curl.mark")"; exit 1; }
if printf '%s' "$out110" | grep -qF 'ALERT-NOT-SENT'; then echo "--- FAIL: §110 F3-2 有主题却走了 ALERT-NOT-SENT（两个分支的判据串了）"; exit 1; fi
echo "ok - §110 F3-2 空主题不请求（恰好一行留痕）+ 有主题真走到 curl 且 URL 带主题"
# F3-2b 发送重试：桩 curl 恒败 ⇒ 恰 ATTEMPTS 次调用、失败文案带次数（与看门狗 WD8 同判据的第二实现面）。
CNT110=$((CNT110 + 1))
rm -f "$W3T/curl.mark"
out110="$(CURL_FAIL=1 NTFY_ALERT_ATTEMPTS=3 NTFY_ALERT_BACKOFF_S=0 bash "$W3T/alert_run.sh" "$W3T/alert.sh" "$TOK110" t b "$W3T/curl.mark" 2>&1 || true)"
f32b_n="$(grep -c . "$W3T/curl.mark" || true)"
[ "${f32b_n:-0}" = "3" ] \
	|| { echo "--- FAIL: §110 F3-2b 恒败时 curl 调用数=${f32b_n:-0}（应恰 3＝重试没在跑或上界没接 env）"; exit 1; }
printf '%s' "$out110" | grep -qF '已试 3 次仍失败' \
	|| { echo "--- FAIL: §110 F3-2b 失败文案没带次数：$(printf '%s' "$out110" | tail -1)"; exit 1; }
# F3-2b 摘锁：把上界钉成 1 ⇒ 同夹具只拨一次＝「恰 3 次」断言失真（重试循环承重）。
cp "$W3T/alert.sh" "$W3T/alert_mut.sh"
f3_sub "$W3T/alert_mut.sh" 'while [ "${attempt}" -le "${NTFY_ALERT_ATTEMPTS}" ]; do' 'while [ "${attempt}" -le 1 ]; do'
rm -f "$W3T/curl.mark"
out110="$(CURL_FAIL=1 NTFY_ALERT_ATTEMPTS=3 NTFY_ALERT_BACKOFF_S=0 bash "$W3T/alert_run.sh" "$W3T/alert_mut.sh" "$TOK110" t b "$W3T/curl.mark" 2>&1 || true)"
f32m_n="$(grep -c . "$W3T/curl.mark" || true)"
[ "${f32m_n:-0}" = "1" ] \
	|| { echo "--- FAIL: §110 F3-2b 摘锁后 curl 调用数=${f32m_n:-0}（应恰 1＝上界被钉死；读数不对＝破坏落错了地方）"; exit 1; }
echo "ok - §110 F3-2b 恒败 ⇒ 重试恰 3 次且文案带次数；摘锁钉死上界 ⇒ 只拨一次（重试循环承重）"

# F3-3 缺 ntfy_topic.sh 必须当场拒跑（镜像树少拷它＝拉取腿 FATAL，而不是静默不推）
CNT110=$((CNT110 + 1))
mkdir -p "$W3T/binonly" "$W3T/home1"
cp deploy/mac/restic_pull_backup.sh "$W3T/binonly/"
rc110=0
HOME="$W3T/home1" PATH="$W3T/bin:$PATH" bash "$W3T/binonly/restic_pull_backup.sh" >"$W3T/o1" 2>"$W3T/e1" || rc110=$?
[ "$rc110" = "1" ] || { echo "--- FAIL: §110 F3-3 缺 lib 时退出码=${rc110}（应为 1；launchd 只看退出码，退 0＝「每天定时、每天没告警」）"; exit 1; }
grep -qF 'FATAL' "$W3T/e1" || { echo "--- FAIL: §110 F3-3 缺 lib 时没打 FATAL（stderr：$(gate_clip 160 "$(head -1 "$W3T/e1")")）"; exit 1; }
[ -z "$(ls -A "$W3T/home1" 2>/dev/null || true)" ] || { echo "--- FAIL: §110 F3-3 缺 lib 的 FATAL 之前已动过 HOME（日志目录被建出来＝先落盘后校验，顺序错）"; exit 1; }
echo "ok - §110 F3-3 缺 ntfy_topic.sh 当场 FATAL rc=1 且 HOME 一个字节没动"

# F3-4 三个 Mac 安装器缺省只预览：跑真仓库文件、HOME 指空目录，退出码 0 且该目录保持为空
#   （"现网特权变更只上传不自动执行"在 Mac 侧的同一条款：门禁要证明不带 -Apply 确实没副作用，
#     而不是读一句注释相信它——§0929DRILL 的反证纪律同样适用于"零改动"这种主张。）
CNT110=$((CNT110 + 1))
for inst in install_mac_backup_agent install_mac_drill_agent install_mac_nightly_agent; do
	CNT110=$((CNT110 + 1))
	d="$W3T/home_${inst}"
	mkdir -p "$d"
	HOME="$d" PATH="$W3T/bin:$PATH" bash "deploy/mac/${inst}.sh" >"$W3T/o_${inst}" 2>&1 || {
		echo "--- FAIL: §110 F3-4 ${inst}.sh 无 -Apply 却非零退出（预览模式必须跑完并打印将做什么）：$(tail -3 "$W3T/o_${inst}")"; exit 1; }
	grep -qF '预览模式：本机一个字节没动' "$W3T/o_${inst}" || { echo "--- FAIL: §110 F3-4 ${inst}.sh 缺省态不再打印预览早退文案（＝它可能已经在动手）"; exit 1; }
	[ -z "$(ls -A "$d" 2>/dev/null || true)" ] || { echo "--- FAIL: §110 F3-4 ${inst}.sh 预览模式在 HOME 里留了东西：$(ls -A "$d" | head -3)"; exit 1; }
done
echo "ok - §110 F3-4 三个安装器缺省预览零改动（backup/drill/nightly）"

# F3-5 迁移器：预览零写 + 写入只走 security -i 的 stdin 命令行（argv 脚印干净）+ 写后读回是**活的**
#   ★ 下面 F3-5b/5c 两枚反证不是设计出来的，是 2026-10-10 owner 授权真拨时**撞出来的**：
#     当时待写入 len=32、写后读回 got=empty、而 security 的 rc 是 0——旧写法（-w 结尾不带值指望它
#     读管道）在真机上存的是空口令条目。桩当时按我以为的语义实现，所以这一路在 113 段全绿里过得去；
#     拦下来的是迁移器自己的读回兜底。本组把"兜底是活的"从注释里的主张改成判据。
CNT110=$((CNT110 + 1))
: > "$W3T/sec.log"
rm -f "$KC110"   # 缺省态＝钥匙串里压根没有这一条（真机首次安装就是这个形态；残骸态见 F3-5c）
out110="$(NTFY_INPUT_TOKEN="$TOK110" PATH="$W3T/bin:$PATH" SEC_LOG="$W3T/sec.log" FAKE_KC="$KC110" \
	bash -c 'unset NTFY_TOPIC; printf "%s" "$NTFY_INPUT_TOKEN" | ./deploy/mac/migrate_ntfy_topic_to_keychain.sh --from-stdin' 2>&1 || true)"
printf '%s' "$out110" | grep -qF 'MIGRATE_NTFY_PLAN' || { echo "--- FAIL: §110 F3-5 缺省态没进预览分支：$(printf '%s' "$out110" | tail -3)"; exit 1; }
grep -qF 'add-generic-password' "$W3T/sec.log" && { echo "--- FAIL: §110 F3-5 预览模式真调了写钥匙串（桩里留了脚印）"; exit 1; }
# 副作用锁改成 -e 而不是 -s：桩的条目**在位性**本身就是状态（建出一条空口令条目在真机上正是这次事故的形态，
# 按"内容非空才算写过"会把它当成没动过）。
[ ! -e "$KC110" ] || { echo "--- FAIL: §110 F3-5 预览模式把假钥匙串条目建出来了（哪怕内容是空的）"; exit 1; }
out110="$(NTFY_INPUT_TOKEN="$TOK110" PATH="$W3T/bin:$PATH" SEC_LOG="$W3T/sec.log" FAKE_KC="$KC110" \
	bash -c 'unset NTFY_TOPIC; printf "%s" "$NTFY_INPUT_TOKEN" | ./deploy/mac/migrate_ntfy_topic_to_keychain.sh --from-stdin -Apply' 2>&1 || true)"
printf '%s' "$out110" | grep -qF 'MIGRATE_NTFY_DONE' || { echo "--- FAIL: §110 F3-5 -Apply 没走完（写后读回比指纹这条兜底没过）：$(printf '%s' "$out110" | tail -3)"; exit 1; }
[ "$(cat "$KC110")" = "$TOK110" ] || { echo "--- FAIL: §110 F3-5 桩里存下的值与输入不符（got='$(cat "$KC110")'）"; exit 1; }
# 通道锁①（正向）：写入确实经 security -i 的 stdin 命令行到达桩
grep -qF 'STDIN-CMD: add-generic-password' "$W3T/sec.log" || { echo "--- FAIL: §110 F3-5 -Apply 没走 security -i 通道（桩里连 STDIN-CMD 脚印都没有＝那条写入口径压根没被拨，DONE 是从别处来的）"; exit 1; }
# 通道锁②（正向）：值就在那行命令行里（没有这条，下面的 argv 负锁会因为"两条通道都没写过"而假绿）
if ! grep '^STDIN-CMD: ' "$W3T/sec.log" | grep -qF -- "$TOK110"; then echo "--- FAIL: §110 F3-5 STDIN-CMD 行里没有主题值（值既没走 stdin 也没走 argv，那条目里的值是从哪来的？）"; exit 1; fi
# 通道锁③（负向）：argv 侧一个字都不带值（CALL: 行＝同机任何进程 ps 可见的面，把凭据从 git 历史搬到运行期明文就是它）
if grep '^CALL: ' "$W3T/sec.log" | grep -qF -- "$TOK110"; then echo "--- FAIL: §110 F3-5 主题值出现在 security 的 argv 脚印里（只准 STDIN-CMD: 行带值，CALL: 行必须干净）"; exit 1; fi
# 通道锁④（负向）：条目不在位时那行命令行不许带 -U（带了＝把"新建"和"覆盖已在位的另一份值"并成一条路，
#   绕过 -Force 那道"改道是有意的吗"确认——F3-5c 反过来要求残骸态必须带 -U，两枚一起才钉住分支选择）
if grep '^STDIN-CMD: ' "$W3T/sec.log" | grep -qE -- '(^|[[:space:]])-U([[:space:]]|$)'; then echo "--- FAIL: §110 F3-5 条目不在位却带 -U 写（分支选择退化成无条件覆盖，-Force 白给）"; exit 1; fi
printf '%s' "$out110" | grep -qE '[0-9a-f]{32}' && { echo "--- FAIL: §110 F3-5 迁移器输出里出现 32-hex（只准报长度与指纹前 8 位）"; exit 1; }
echo "ok - §110 F3-5 迁移器预览零写 + 值只走 security -i 的 stdin（argv 干净、分支按在位性选）+ 写后读回一致"

# F3-5b（反证一枚）桩复现"写入 rc=0 但值没进去"⇒ 迁移器必须退非零、点名读回不一致、且不再喊 DONE
CNT110=$((CNT110 + 1))
: > "$W3T/sec.log"
rm -f "$KC110"
rc110=0
out110="$(NTFY_INPUT_TOKEN="$TOK110" PATH="$W3T/bin:$PATH" SEC_LOG="$W3T/sec.log" FAKE_KC="$KC110" SHIM_WRITE_EMPTY=1 \
	bash -c 'unset NTFY_TOPIC; printf "%s" "$NTFY_INPUT_TOKEN" | ./deploy/mac/migrate_ntfy_topic_to_keychain.sh --from-stdin -Apply' 2>&1)" || rc110=$?
[ "$rc110" != "0" ] || { echo "--- FAIL: §110 F3-5b 桩把值丢掉（写入侧 rc 仍是 0）时迁移器居然退 0＝2026-10-10 那次事故被原样重演，而它正是靠这条退非零才没让人继续装代理"; exit 1; }
printf '%s' "$out110" | grep -qF '写入后读回指纹不一致' || { echo "--- FAIL: §110 F3-5b 退非零却没点名「读回不一致」（got 侧是 empty 还是错值，排查时是两种处置）：$(printf '%s' "$out110" | tail -2)"; exit 1; }
printf '%s' "$out110" | grep -qF 'got=empty 的已知成因' || { echo "--- FAIL: §110 F3-5b 空值形态没给出残骸处置（谁在钥匙串里留了条空口令条目、下一步该删哪条，读数要说全）"; exit 1; }
printf '%s' "$out110" | grep -qF 'MIGRATE_NTFY_DONE' && { echo "--- FAIL: §110 F3-5b 一边退非零一边还喊 DONE（调用方按输出尾巴读结果会当成成功＝降级报成功那一族）"; exit 1; }
echo "ok - §110 F3-5b 桩复现「值没进去」时迁移器退非零＋点名读回不一致＋无 DONE（读回兜底是活的，不是注释）"

# F3-5c（反证一枚＋分支锁）条目在位但口令为空（真机残骸态）⇒ 必须点名该形态、带 -U 覆写、最终写成功
CNT110=$((CNT110 + 1))
: > "$W3T/sec.log"
: > "$KC110"   # 空文件＝条目在位、口令字段为空（真机 quant-ntfy-topic 在这次修复前就是这个状态）
out110="$(NTFY_INPUT_TOKEN="$TOK110" PATH="$W3T/bin:$PATH" SEC_LOG="$W3T/sec.log" FAKE_KC="$KC110" \
	bash -c 'unset NTFY_TOPIC; printf "%s" "$NTFY_INPUT_TOKEN" | ./deploy/mac/migrate_ntfy_topic_to_keychain.sh --from-stdin -Apply' 2>&1 || true)"
printf '%s' "$out110" | grep -qF '条目在位但口令为空' || { echo "--- FAIL: §110 F3-5c 残骸态没被点名（按「值非空＝存在」选分支会给这种条目走不带 -U 的 add，真机退 45 already exists＝报「写入失败」却看不出是残骸挡路）"; exit 1; }
if ! grep '^STDIN-CMD: ' "$W3T/sec.log" | grep -qE -- '(^|[[:space:]])-U([[:space:]]|$)'; then echo "--- FAIL: §110 F3-5c 条目已在位却没带 -U（与 F3-5 通道锁④配对：在位必带、不在位必不带，两枚一起才钉住分支）"; exit 1; fi
[ "$(cat "$KC110")" = "$TOK110" ] || { echo "--- FAIL: §110 F3-5c 残骸没被覆写掉（got='$(cat "$KC110")'）"; exit 1; }
printf '%s' "$out110" | grep -qF 'MIGRATE_NTFY_DONE' || { echo "--- FAIL: §110 F3-5c 残骸态覆写后没走完：$(printf '%s' "$out110" | tail -2)"; exit 1; }
echo "ok - §110 F3-5c 空口令残骸条目被点名并以 -U 覆写（分支按条目在位性选，不按值非空选）"

# F3-6（＝FIX_PLAN §5.5 的 F5）kuma_seed.js 必传参数**行为腿**：断言退出码，不断言播种结果。
#
# 为什么这一段是本轮补的而不是波 3 一开始就写：③ 那组静态锁只钉了"校验文案在位 + process.exit(2) 两处
# + 校验排在 require 之前"，**形状对但不一定走得到**（§P1-A 同族：锁了形状没锁可达性）。
# 文件头注释自己已经写着"门禁 §110 的 F5 腿"，注释先于实现落纸＝注释在替代码撒谎（§0929 ⑩ 那条
# 「铲掉落库口径一致的幻觉注释」是同一件事）。
# ★ 四条腿都不执行播种体：合法参数那条**要求**卡在模块解析上（仓库根解析不到 socket.io-client），
#   于是"校验块已过"与"绝不连 kuma、绝不动钥匙串"同时成立；哪天根目录能解析到该模块，这条会点名红，
#   处置是显式换成隔离环境跑，而不是把断言改成"能连上就算绿"。
CNT110=$((CNT110 + 1))
mkdir -p "$W3T/empty_node_modules"
rc110=0
out110="$(node deploy/mac/kuma_seed.js 2>&1)" || rc110=$?
[ "$rc110" != "0" ] || { echo "--- FAIL: §110 F3-6 不传任何参数居然 0 退出（缺省值出仓后，缺参必须吵）"; exit 1; }
[ "$rc110" = "2" ] || { echo "--- FAIL: §110 F3-6 缺参退出码=${rc110}（应为 2＝校验块的专用码；换成 1 就是 MODULE_NOT_FOUND 那类意外冒充成功）"; exit 1; }
printf '%s' "$out110" | grep -qF '缺少必传参数' || { echo "--- FAIL: §110 F3-6 缺参没点名（退出非零但没有可读成因＝现网只能靠猜）：$(printf '%s' "$out110" | head -2)"; exit 1; }
printf '%s' "$out110" | grep -qF 'ntfy_topic(argv[2])' || { echo "--- FAIL: §110 F3-6 缺参文案没点名 argv[2]（只报『缺参数』不报缺哪个）"; exit 1; }
printf '%s' "$out110" | grep -qE '[0-9a-f]{32}' && { echo "--- FAIL: §110 F3-6 缺参输出里出现 32-hex（校验块不该把任何凭据形状的值打出来）"; exit 1; }
rc110=0
out110="$(node deploy/mac/kuma_seed.js "$TOK110" 2>&1)" || rc110=$?
[ "$rc110" = "2" ] || { echo "--- FAIL: §110 F3-6 只缺第二个参数时退出码=${rc110}（应为 2）"; exit 1; }
printf '%s' "$out110" | grep -qF 'gz_public_ip(argv[3])' || { echo "--- FAIL: §110 F3-6 只缺 argv[3] 却没点名它（缺参点名不精确＝两个缺参用例读起来一模一样）"; exit 1; }
printf '%s' "$out110" | grep -qF 'ntfy_topic(argv[2])' && { echo "--- FAIL: §110 F3-6 已给主题却仍报缺 argv[2]（校验条件写反）"; exit 1; }
printf '%s' "$out110" | grep -qF -- "$TOK110" && { echo "--- FAIL: §110 F3-6 缺参文案把主题值回显了（主题＝凭据，只准报参数名）"; exit 1; }
rc110=0
out110="$(node deploy/mac/kuma_seed.js "$TOK110" "1.2.3" 2>&1)" || rc110=$?
[ "$rc110" = "2" ] || { echo "--- FAIL: §110 F3-6 IP 形状不符时退出码=${rc110}（应为 2＝校验块的第二个出口）"; exit 1; }
printf '%s' "$out110" | grep -qF '不是四段点分十进制' || { echo "--- FAIL: §110 F3-6 IP 形状校验出口没走到（两个出口只验了一个＝另一半可以随便写坏）：$(printf '%s' "$out110" | head -2)"; exit 1; }
printf '%s' "$out110" | grep -qF '1.2.3' && { echo "--- FAIL: §110 F3-6 形状不符时回显了传入值（文案自己写着『不回显值』）"; exit 1; }
rc110=0
out110="$(NODE_PATH="$W3T/empty_node_modules" node deploy/mac/kuma_seed.js "$TOK110" 127.0.0.1 2>&1)" || rc110=$?
[ "$rc110" != "0" ] || { echo "--- FAIL: §110 F3-6 合法参数居然跑完并 0 退出（门禁里绝不该真播种监控项，这条红的下一步是查它连了谁）"; exit 1; }
printf '%s' "$out110" | grep -qF 'Cannot find module' \
	|| { echo "--- FAIL: §110 F3-6 合法参数没卡在模块解析上（实得尾部：$(printf '%s' "$out110" | tail -2)）——要么校验块之后的代码结构变了，要么根目录忽然能解析到 socket.io-client（那就必须在隔离环境跑，不能改断言成『连得上就算绿』）"; exit 1; }
printf '%s' "$out110" | grep -qF '缺少必传参数' && { echo "--- FAIL: §110 F3-6 参数齐全仍走缺参分支（校验条件写坏，现网会『配了值却说没配』）"; exit 1; }
printf '%s' "$out110" | grep -qF -- "$TOK110" && { echo "--- FAIL: §110 F3-6 运行输出里回显了主题明文"; exit 1; }
echo "ok - §110 F3-6 kuma 必传参数四腿（缺一个也要点名、形状出口在位、合法参数卡在依赖解析上且零回显）"

# ════════════════════════════════════════════════════════════════════════════
# ⑤ §MACD（2026-10-10 收口批）：Mac 拉取腿的唤醒窗口读法 + 稳定副本漂移探测器
#
# 两条读数都是今天实锚的，不是设想出来的靶子：
#   1) 现网侧第 32 探针三拨后的复拨（07:17）读出 `quant-backup-snap present=1 rule=30h
#      age=3.3h enabled=True lastrun=2026-10-10 04:00:00`，同一条标记手工 `ssh gz type ...SNAPSHOT_OK`
#      一次即读到 `ok:true ts=2026-10-10T05:24:47 integrity=ok accounts_files=30`
#      ⇒ 快照**跑了**；而 Mac 侧拉取腿 07:10:24 报的是「读不到广州 SNAPSHOT_OK（快照任务可能没跑）」，
#      把"本机电不到"写成了"那台机没跑"＝归因方向反了一整台机器（§0929DRILL-A 把读法失败写成
#      现网事实，同族）。日志里四例同形（10-02 10:32、10-05 10:43、10-09 10:44 挂 65 分钟后失败、
#      10-10 07:10 一秒即败），全部落在 launchd 唤醒后的第一个动作，且每例后面紧跟
#      「ntfy 告警发送失败（网络？）」⇒ 同一个断网窗口既打断读取也打断报警，
#      "今晚没备份"与"今晚没人收到通知"在现象上完全一样。修法：读取带重试（copy 那腿本来就有
#      8 次重试，读取没道理一次判死）＋成因带回日志＋半死 TCP 有封顶（ServerAlive 15s×4）。
#   2) 副本面：10-07 波 3 的告警硬化与 10-09 §W7-D 的两表量纲抽检**从没在调度里跑过**——
#      launchd 执行的稳定副本 mtime 全停在 09-30（本次实测 11 对里 3 diff + 2 missing-mirror）。
#      门禁 113 段全绿、Playwright 全绿，全部绿在仓库那份文件上；这一族连"下一次部署会带上"都没有
#      （广州侧至少还有 -s 发版推平脚本面）。⇒ 先做探测器（清单从三个安装器的 cp 行**派生**，
#      不写第二份文件清单），再给探测器自己配反证；探测器**只读不写**，修法仍是 install_*_agent.sh -Apply。
# 判据面纪律：FD5 对真仓库只回显"读得出多少对"，**不拿漂移判红**——现在真就是漂移态（owner 未当面
#   -Apply），把它做成红会让门禁在一个健康现网上永远红（§107 DRILL-C 那一课）。
# ════════════════════════════════════════════════════════════════════════════
RPP110=deploy/mac/restic_pull_backup.sh
CHK110=deploy/mac/check_mac_agent_drift.sh
eq110 "$RPP110" 'MARK_MAX_TRIES="${MARK_MAX_TRIES:-3}"' 1 '重试次数由变量给（写死 1 2 3 而文案报 env 值＝阈值惰性，本仓锤过的"预览一套实跑另一套"）'
eq110 "$RPP110" 'for mk_try in $(seq 1 "$MARK_MAX_TRIES")' 1 '循环上界由同一个变量派生（与上一条同源，缺一枚就是文案与实跑分家）'
eq110 "$RPP110" '-o ConnectTimeout=20 -o ServerAliveInterval=15' 1 '半死 TCP 有封顶（10-09 那例挂了 65 分钟才失败：ConnectTimeout 只管连上之前，连上之后没人管）。锚点带 ConnectTimeout 是因为 ServerAlive 那一双参数在 copy 的 sftp.args 里也有一份（整串只写 ServerAlive 会把两处算一起＝计数锚混消费者，本仓踩过两次）'
eq110 "$RPP110" '2>&1)" && mk_rc=0 || mk_rc=$?' 1 'stdout 与 stderr 同管道取回且退出码显式收（旧写法 2>/dev/null 把成因整条吞了）'
eq110 "$RPP110" 'c1-200' 1 '成因回显限长（行长是这条链的隐形约束，超了会在外层日志里劈行）。锚点只钉「c1-200」这一段而不钉完整截断命令：§111 有一条负锁按代码行扫「门禁正文里不许出现按字节截断」，写全串就撞上那条自己的锁（今天实踩，详见 §111 ②c 的 ★ 段——那一段是本把尺子的主人，成因写在那里）。窄锚仍然承重：改成 1-100 或换别的截断写法这串就找不到，红照样出。被扫的那份文件是 Mac 侧日志、不是分类器输入，所以限长留在拉取腿里是安全的，禁的只是门禁自己的输出面'
eq110 "$RPP110" 'sleep "${MARK_RETRY_SLEEP_SEC:-45}"' 1 '重试间隔走 env（门禁行为腿取 0 快拨，不为此拆第二条代码路径）'
eq110 "$RPP110" '本机这头没连上' 1 '失败文案只声明本机这头的事实，并把现网正规读法指出来（不替那台机下结论）'
# 发送重试四把（与看门狗同构；拉取腿的告警恰好都落在 launchd 唤醒后的断网窗口里，§4.1b.19 ① 同形四例）：
eq110 "$RPP110" 'NTFY_ALERT_ATTEMPTS="${NTFY_ALERT_ATTEMPTS:-3}"' 1 '重试次数走 env 且缺省 3'
eq110 "$RPP110" 'NTFY_ALERT_BACKOFF_S="${NTFY_ALERT_BACKOFF_S:-5}"' 1 '重试间隔走 env（F3-2 夹具取 0 快拨）'
eq110 "$RPP110" 'while [ "${attempt}" -le "${NTFY_ALERT_ATTEMPTS}" ]; do' 1 '重试循环上界与次数同一来源'
eq110 "$RPP110" '已试 ${NTFY_ALERT_ATTEMPTS} 次仍失败' 1 '失败文案报的就是循环上界那个变量'
neg110() { # $1=文件 $2=整串 $3=说明 → 代码行里彻底没有（本组只用于旧归因文案下线）
	# 为什么先剥整行注释再数：旧文案在这一版里**故意留在注释里当证据**（§W7-PROBE32 那一课——
	#   铲掉注释等于铲掉这次反证，将来没人知道曾经错过）。而负锁要拦的是"这句还会不会被用户读到"，
	#   只有代码行算数。计数锚混消费者本仓踩过三次，这次是第四种形态：负锁把说明文字算成回流。
	# 局限写在明处：行尾注释（代码后跟 # 的那一种）剥不掉，会误报回流；本组锚点选的是 fail 文案整串，
	#   真实文件里它只可能出现在代码行，所以这个局限今天不咬人——哪天红了先查是不是有人把它写进了注释。
	CNT110=$((CNT110 + 1))
	local got
	got=$(sed 's/^[[:space:]]*#.*$//' "$1" | grep -cF -- "$2" 2>/dev/null || true)
	[ "${got:-0}" = "0" ] || { echo "--- FAIL: §110 负锁 ${CNT110}（${3}）：${1} 的代码行又出现「${2}」got=${got}"; exit 1; }
}
neg110 "$RPP110" '快照任务可能没跑' '把"读不到"冒充成"它没跑"的旧文案不许回流（这一句的方向性错误会让人去查错的那台机器）'
# 同一串再做一枚**整文件**正锁：注释里那一条必须还在（它记的是"这条红曾经归因反了一整台机器"）。
# 两枚合起来才是完整形状：代码行 0 处＝用户读不到，全文件 1 处＝成因证据没被铲掉。
# 只留负锁的后果是"为了让注释不被算进来而删注释"＝把反证删了来让锁绿，本仓 §W7-PROBE32 已经为同款付过一次账。
eq110 "$RPP110" '快照任务可能没跑' 1 '旧归因文案在注释里保留一处（负锁刚判过代码行为 0；这里要的是它作为证据还在，不是它能被读到）'
CNT110=$((CNT110 + 1))
[ -x "$CHK110" ] || { echo "--- FAIL: §110 在位锁 ${CNT110}（${CHK110} 缺执行位：launchd/外层脚本按命令用它，非零退出码就是它的判决面）"; exit 1; }
eq110 "$CHK110" 'install_mac_*_agent.sh' 1 '安装器清单按文件名形状派生（§MAC-WATCHDOG 起四个；首版写死三个名字，第四个安装器上场时它的 cp 行就不在检查面内＝清单式锁失明同族，本枚锁防它回来）'
eq110 "$CHK110" 'if n_pairs < 6:' 1 '派生数下限（低于下限退 2＝探测器自己坏了不许冒充"没有漂移"）'
eq110 "$CHK110" 'DRIFT-SUMMARY|pairs=%d same=%d' 1 '汇总行形状（外层只看这一行就能分流：读得出几对、几对不一致）'
eq110 "$CHK110" 'DRIFT-UNRESOLVED|' 1 '展不开的 cp 行必须点名（静默丢弃＝把覆盖缺口伪装成通过）'

# FD1–FD4 副本漂移探测器的四态行为腿（全在 /tmp 夹具树里，仓库与真实副本一个字节都不动）
DR110="$W3T/drift"
mkdir -p "$DR110/repo/deploy/mac" "$DR110/home/backups/quant/bin"
# 夹具的安装器名单跟着真实形态走（§MAC-WATCHDOG 起四个；探测器按 glob 派生，多一个空文件不多一对）
for ins in install_mac_backup_agent.sh install_mac_nightly_agent.sh install_mac_drill_agent.sh install_mac_watchdog_agent.sh; do
	: > "$DR110/repo/deploy/mac/$ins"
done
# 夹具安装器：六个 cp 对（变量两跳 A="$B/x" 的写法也要展得开，这正是真实安装器的形态）
cat > "$DR110/repo/deploy/mac/install_mac_backup_agent.sh" <<'FIX110'
#!/bin/bash
BIN_DIR="$HOME/backups/quant/bin"
REPO_MAC_DIR="$REPO_ROOT/deploy/mac"
cp "$REPO_MAC_DIR/one.sh" "$BIN_DIR/one.sh"
cp "$REPO_MAC_DIR/two.sh" "$BIN_DIR/two.sh"
cp "$REPO_MAC_DIR/three.sh" "$BIN_DIR/three.sh"
cp "$REPO_MAC_DIR/four.sh" "$BIN_DIR/four.sh"
cp "$REPO_MAC_DIR/five.sh" "$BIN_DIR/five.sh"
cp "${REPO_MAC_DIR}/six.sh" "${BIN_DIR}/six.sh"
FIX110
for n in one two three four five six; do
	echo "print $n" > "$DR110/repo/deploy/mac/$n.sh"
	cp "$DR110/repo/deploy/mac/$n.sh" "$DR110/home/backups/quant/bin/$n.sh"
done
CNT110=$((CNT110 + 1))
HOME="$DR110/home" REPO_ROOT="$DR110/repo" bash "$CHK110" >"$DR110/fd1.log" 2>&1 && fd1_rc=0 || fd1_rc=$?
[ "$fd1_rc" = "0" ] || { echo "--- FAIL: §110 FD1 全一致的夹具被判红（rc=${fd1_rc}）：$(tail -3 "$DR110/fd1.log")"; exit 1; }
grep -qF 'DRIFT-SUMMARY|pairs=6 same=6 diff=0' "$DR110/fd1.log" \
	|| { echo "--- FAIL: §110 FD1 派生清单不是六对全绿（探测器没按安装器的 cp 行配对＝它报出来的那个 green 没有内容）：$(tail -2 "$DR110/fd1.log")"; exit 1; }
echo "ok - §110 FD1 夹具六对全一致 ⇒ rc=0（派生清单真的按 cp 行配对）"
# FD2 改一份副本 ⇒ 必须红且点名是哪一份
echo "tampered" >> "$DR110/home/backups/quant/bin/three.sh"
CNT110=$((CNT110 + 1))
HOME="$DR110/home" REPO_ROOT="$DR110/repo" bash "$CHK110" >"$DR110/fd2.log" 2>&1 && fd2_rc=0 || fd2_rc=$?
[ "$fd2_rc" = "1" ] || { echo "--- FAIL: §110 FD2 副本被改过仍 rc=${fd2_rc}（应为 1：源改完没重装这件事现在没人说）"; exit 1; }
grep -qF 'state=diff' "$DR110/fd2.log" || { echo "--- FAIL: §110 FD2 红了但没有 diff 态读数"; exit 1; }
grep -qF 'bin/three.sh' "$DR110/fd2.log" || { echo "--- FAIL: §110 FD2 没点名是哪一份副本漂了（只报「有漂移」的锁没法执行修法）"; exit 1; }
grep -qF 'DRIFT-SUMMARY|pairs=6 same=5 diff=1' "$DR110/fd2.log" \
	|| { echo "--- FAIL: §110 FD2 汇总分项与点名行不自洽：$(tail -2 "$DR110/fd2.log")"; exit 1; }
echo "ok - §110 FD2 一份副本字节不同 ⇒ rc=1 且点名该文件、分项自洽（same=5 diff=1）"
# FD3 铲掉一份副本 ⇒ missing-mirror（"副本不存在"与"副本不一致"是两种修法，不许共用一个态）
rm -f "$DR110/home/backups/quant/bin/six.sh"
CNT110=$((CNT110 + 1))
HOME="$DR110/home" REPO_ROOT="$DR110/repo" bash "$CHK110" -Json >"$DR110/fd3.log" 2>&1 && fd3_rc=0 || fd3_rc=$?
[ "$fd3_rc" = "1" ] || { echo "--- FAIL: §110 FD3 副本被铲仍 rc=${fd3_rc}"; exit 1; }
grep -qF 'missing_mirror=1' "$DR110/fd3.log" || { echo "--- FAIL: §110 FD3 没读成 missing-mirror 态：$(tail -2 "$DR110/fd3.log")"; exit 1; }
echo "ok - §110 FD3 副本缺失 ⇒ 独立态 missing_mirror=1（-Json 只出汇总也带着分项）"
# FD4 派生本身坏掉（安装器在但一对都展不开）⇒ 必须 rc=2，不许退成"没有漂移"的绿
mkdir -p "$DR110/empty_repo/deploy/mac"
for ins in install_mac_backup_agent.sh install_mac_nightly_agent.sh install_mac_drill_agent.sh install_mac_watchdog_agent.sh; do
	: > "$DR110/empty_repo/deploy/mac/$ins"
done
CNT110=$((CNT110 + 1))
HOME="$DR110/home" REPO_ROOT="$DR110/empty_repo" bash "$CHK110" -Json >"$DR110/fd4.log" 2>&1 && fd4_rc=0 || fd4_rc=$?
[ "$fd4_rc" = "2" ] || { echo "--- FAIL: §110 FD4 派生数为 0 时 rc=${fd4_rc}（应为 2：清单读不到与没有漂移共用一个绿＝本仓最贵的错法）"; exit 1; }
echo "ok - §110 FD4 派生低于下限 ⇒ rc=2（探测器自己坏了不报"一切正常"）"
# FD5 真仓库面：只锁"读得出形状"，不拿现况判红（当前 owner 未 -Apply＝真有漂移，那是台账不是门禁红）
CNT110=$((CNT110 + 1))
bash "$CHK110" -Json >"$DR110/fd5.log" 2>&1 && fd5_rc=0 || fd5_rc=$?
FD5_PAIRS=$(grep -c '^DRIFT-SUMMARY|pairs=' "$DR110/fd5.log" || true)
[ "${FD5_PAIRS:-0}" = "1" ] || { echo "--- FAIL: §110 FD5 在真仓库上读不出汇总行（探测器对本仓失效，前面四态就都是夹具里的绿）"; exit 1; }
FD5_N=$(sed -n 's/.*DRIFT-SUMMARY|pairs=\([0-9]*\).*/\1/p' "$DR110/fd5.log" | head -1)
[ "${FD5_N:-0}" -ge 6 ] || { echo "--- FAIL: §110 FD5 真仓库只派生出 ${FD5_N:-0} 对（<6＝安装器写法变了或 glob 空转）"; exit 1; }
echo "ok - §110 FD5 真仓库读数面（派生 ${FD5_N} 对，现况 rc=${fd5_rc} 只回显不判红——副本待 owner 当面 -Apply，见 RUNBOOK §4.1b.19）"
# FD6 摘锁反证：把探测器的出口判决改成恒 0，FD2 那份漂了的夹具必须"变绿"
#   （证明 FD2 的红真的来自这条判决，而不是夹具恰好让别处先退非零）
DR110_MIR="$DR110/mut"
mkdir -p "$DR110_MIR"
cp "$CHK110" "$DR110_MIR/check_mac_agent_drift.sh"
f3_sub "$DR110_MIR/check_mac_agent_drift.sh" 'sys.exit(1 if (bad or unresolved) else 0)' 'sys.exit(0)'
CNT110=$((CNT110 + 1))
HOME="$DR110/home" REPO_ROOT="$DR110/repo" bash "$DR110_MIR/check_mac_agent_drift.sh" -Json >"$DR110/fd6.log" 2>&1 && fd6_rc=0 || fd6_rc=$?
[ "$fd6_rc" = "0" ] || { echo "--- FAIL: §110 FD6 摘掉判决后仍 rc=${fd6_rc}（该枚反证没落到点上：FD2 的红其实来自别处，要重读成因）"; exit 1; }
grep -qF 'diff=1' "$DR110/fd6.log" || { echo "--- FAIL: §110 FD6 摘锁夹具里读数不再是 diff=1（破坏改变了配对本身，反证与正锁测的不是同一件事）"; exit 1; }
echo "ok - §110 FD6 摘掉出口判决 ⇒ 同一份漂移夹具退成 rc=0 而读数仍是 diff=1（FD2 的红归因到这条判决）"
# FD7 拉取腿重试这条腿的**行为**证据：桩 ssh 前两次失败、第三次成功，间隔取 0 跑完
#   （静态锁只能证明"代码长这样"，证明不了"抖一次就不再断整夜"——本批要的是后者）
STUB110="$W3T/stub7"
mkdir -p "$STUB110/bin" "$STUB110/run" "$STUB110/home"
cp "$RPP110" "$STUB110/run/restic_pull_backup.sh"
cp deploy/mac/ntfy_topic.sh "$STUB110/run/ntfy_topic.sh"
cat > "$STUB110/bin/ssh" <<'SH7'
#!/bin/bash
# 桩 ssh：第 1、2 次拨号失败（模拟唤醒窗口的电不到），第 3 次成功回一份合法标记。
# 状态计数落文件而不是变量：拉取腿每一轮重试都是**同进程内的新 ssh 调用**，
# 变量活不过一次调用（这条桩要是把计数写错，D7 就会变成"恒成功"的假绿）。
CNT_FILE="${FAKE_SSH_CNT:-/tmp/fake_ssh_cnt}"
n=0
[ -f "$CNT_FILE" ] && n="$(cat "$CNT_FILE")"
n=$((n + 1))
printf '%s' "$n" > "$CNT_FILE"
if [ "$n" -lt 3 ]; then
  echo "** WARNING: connection is not using a post-quantum key exchange algorithm." >&2
  echo "ssh: connect to host gz port 22: No route to host" >&2
  exit 255
fi
printf '%s\n' "{\"ok\":true,\"ts\":\"$(date '+%Y-%m-%dT%H:%M:%S')\",\"db_bytes\":1,\"dbs\":{\"live.db\":1,\"trading.db\":1},\"integrity\":\"ok\",\"accounts_files\":1}"
exit 0
SH7
cat > "$STUB110/bin/restic" <<'SH7R'
#!/bin/bash
# 桩 restic：所有子命令一律成功（这一腿测的是标记读取的重试，不是 restic 的行为）。
echo "stub-snapshot-id"
exit 0
SH7R
cat > "$STUB110/bin/security" <<'SH7S'
#!/bin/bash
# 桩 security：钥匙串只给 restic 仓库密码；**故意不给 ntfy 主题**——
# 于是本次运行同时走 10-07 的"无主题不请求、正文落日志"分支，一次跑验两件事。
if [ "${1:-}" = "find-generic-password" ]; then
  case "$*" in
    *quant-restic-repo-pass*) echo "stub-repo-pass" ; exit 0 ;;
  esac
  exit 44
fi
exit 0
SH7S
chmod +x "$STUB110/bin/ssh" "$STUB110/bin/restic" "$STUB110/bin/security"
bash -n "$STUB110/bin/ssh" || { echo "--- FAIL: §110 FD7 桩脚本语法不过（先修桩，红的是桩不是判据）"; exit 1; }
CNT110=$((CNT110 + 1))
rm -f "$STUB110/run/cnt"
HOME="$STUB110/home" PATH="$STUB110/bin:$PATH" FAKE_SSH_CNT="$STUB110/run/cnt" \
	NTFY_TOPIC="" MARK_RETRY_SLEEP_SEC=0 MARK_MAX_TRIES=3 \
	bash "$STUB110/run/restic_pull_backup.sh" >"$STUB110/fd7.log" 2>&1 && fd7_rc=0 || fd7_rc=$?
grep -qF '=== 备份成功 ===' "$STUB110/fd7.log" \
	|| { echo "--- FAIL: §110 FD7 抖动两次后仍没跑到成功行（rc=${fd7_rc}）：$(tail -4 "$STUB110/fd7.log")"; exit 1; }
FAIL_LINES=$(grep -c '次读取失败 rc=' "$STUB110/fd7.log" || true)
[ "${FAIL_LINES:-0}" = "2" ] || { echo "--- FAIL: §110 FD7 失败留痕不是恰好两行（实得 ${FAIL_LINES}）：重试循环与 env 次数不同源，或成因没回显"; exit 1; }
grep -qF 'No route to host' "$STUB110/fd7.log" \
	|| { echo "--- FAIL: §110 FD7 日志里没有 ssh 侧成因（旧写法 2>/dev/null 就是这样把「快照任务可能没跑」当成事实的）"; exit 1; }
grep -qF '**' "$STUB110/fd7.log" && { echo "--- FAIL: §110 FD7 后量子提示行混进了成因回显（那不是成因，留着会让人每次去查一句无关的告警）"; exit 1; }
grep -qF 'ALERT-NOT-SENT' "$STUB110/fd7.log" || { echo "--- FAIL: §110 FD7 无主题时没落 ALERT-NOT-SENT（10-07 那批的显式留痕被这次改动带丢了）"; exit 1; }
echo "ok - §110 FD7 桩 ssh 两败一成 ⇒ 拉取腿仍跑到成功行、两行成因留痕（含 No route to host、剥掉 ** 提示行）、无主题走 ALERT-NOT-SENT"
# FD8 反证：把循环上界钉死成 1（＝旧的一次判死），同一夹具必须失败
#   （证明 FD7 的绿来自"真的重试了"，而不是桩本身恰好一直成功）
mkdir -p "$STUB110/once"
cp "$RPP110" "$STUB110/once/restic_pull_backup.sh"
cp deploy/mac/ntfy_topic.sh "$STUB110/once/ntfy_topic.sh"
f3_sub "$STUB110/once/restic_pull_backup.sh" 'for mk_try in $(seq 1 "$MARK_MAX_TRIES")' 'for mk_try in $(seq 1 1)'
CNT110=$((CNT110 + 1))
rm -f "$STUB110/run/cnt8"
HOME="$STUB110/home" PATH="$STUB110/bin:$PATH" FAKE_SSH_CNT="$STUB110/run/cnt8" \
	NTFY_TOPIC="" MARK_RETRY_SLEEP_SEC=0 MARK_MAX_TRIES=3 \
	bash "$STUB110/once/restic_pull_backup.sh" >"$STUB110/fd8.log" 2>&1 && fd8_rc=0 || fd8_rc=$?
[ "$fd8_rc" != "0" ] || { echo "--- FAIL: §110 FD8 循环被钉死成一次仍判绿（FD7 的绿就不是重试给的，是桩给的）"; exit 1; }
grep -qF '本机这头没连上' "$STUB110/fd8.log" \
	|| { echo "--- FAIL: §110 FD8 失败文案没走新归因（说明改的是别处，或两枚反证落到同一个点上）：$(tail -3 "$STUB110/fd8.log")"; exit 1; }
echo "ok - §110 FD8 上界钉死成 1 ⇒ 同一夹具必败且红在新文案上（重试是这条腿的承重墙，不是装饰）"
# ════════════════════════════════════════════════════════════════════════════
# ⑥ 组：Mac 日频看门狗（§MAC-WATCHDOG，2026-10-10）。
# 背景：①拉取腿自己没有看门狗——record_freshness 那台死调度探测器挂在拉取腿上、反查的是演练与
#   夜间验收，"拉取腿多久没跑成"没有读数（09-16~09-25 断更十天靠人翻日志才发现）；
#   ②漂移探测器此前只有人坐在前面才跑（前提是推平到 same=11，10-10 当日已达成）。
#   ⇒ 新增第四条调度 com.quant.watchdog（每日 11:00，排在所有被看对象窗口之后），
#   两腿互看：看门狗反查拉取腿心跳，拉取腿用同一个 record_freshness 反查看门狗留档（消费者 2→3）。
# 行为腿 WD1–WD7 的读数形状**先在 /tmp 夹具真拨过九态**（ok/stale/no-anchor/no-log/unparsable/
#   drift/probe-broken/repo-root-absent/no-topic）再写进断言——本日两次实踩（BSD sed 两层捕获组
#   括号不配对 ⇒ 整腿 unparsable；set -u 下汇总行变量名笔误 ⇒ 九态全退 1 且留档空）都是真拨先抓住的，
#   夹具必须断「overall 行在位 + 留档非空」，光看退出码会把尾巴上的笔误读成"有事"。
# ════════════════════════════════════════════════════════════════════════════
WD110=deploy/mac/check_mac_ops_watchdog.sh
WDP110=deploy/mac/com.quant.watchdog.plist
WDI110=deploy/mac/install_mac_watchdog_agent.sh
CNT110=$((CNT110 + 1))
[ -x "$WD110" ] && [ -x "$WDI110" ] || { echo "--- FAIL: §110 WD 在位锁 ${CNT110}（看门狗/安装器缺执行位：launchd 与外层按命令用它）"; exit 1; }
eq110 "$WD110" 'PULL_SUCCESS_ANCHOR="${PULL_SUCCESS_ANCHOR:-=== 备份成功 ===}"' 1 '成功锚串可 env 且缺省与拉取腿那句同一条来源（两处各写一份锚＝改文案的那批不会改判据）'
eq110 "$RPP110" 'log "=== 备份成功 ==="' 1 '拉取腿的成功锚恰一条（看门狗按它读心跳；多一条＝夹带的成功假象会喂假心跳）'
eq110 "$WD110" 'PULL_MAX_AGE_HOURS="${PULL_MAX_AGE_HOURS:-30}"' 1 '心跳阈值单源（30h＝拉取腿每天两窗，整一天皆没跑成才吵；抄别的阈值＝对不上真实触发日历）'
eq110 "$WD110" 'threshold_h=${PULL_MAX_AGE_HOURS}' 1 '读数文案与判据同源（文案报 env 值而判据写死＝阈值惰性，本仓锤过的形状）'
eq110 "$WD110" 'ntfy_topic_resolve' 1 '告警主题只走单实现（本地再拼一遍 security 命令＝三份并存的必然结局是改一处漏两处）'
eq110 "$WD110" 'ALERT-NOT-SENT reason=no-topic' 1 '无主题不发请求、整条正文落日志（打到根路径回 404 再报"网络？"＝把配置缺失伪装成网络抖动）'
eq110 "$WD110" 'record_line "$res" "$rc"' 1 '每跑必留档（留档是这条调度唯一"真跑过"的证据面；不写＝拉取腿的反查只会看到断更）'
eq110 "$WD110" 'WATCHDOG|overall=$res rc=$rc' 1 '汇总行带退出码（launchctl LastExitStatus 与日志两条读数面必须说的是同一件事）'
eq110 "$WD110" 'DRIFT_SCRIPT:-$SELF_DIR/check_mac_agent_drift.sh' 1 '漂移判决走单实现（重写第二份字节比较＝只修一份、另一份安静地不再判红）'
eq110 "$WD110" 'QUANT_REPO_ROOT="${QUANT_REPO_ROOT:-$HOME_DIR/Desktop/quant-trading-v2}"' 1 '仓库根单点缺省（副本目录里反推出来的是镜像根＝一片 missing-repo 假红，必须显式给真值且可 env 覆盖）'
eq110 "$WDP110" '<string>com.quant.watchdog</string>' 1 'plist Label 恰一条（launchctl 按它点名；重复＝两份任务抢同一个稳定副本）'
eq110 "$WDP110" 'backups/quant/watchdog/check_mac_ops_watchdog.sh' 1 'ProgramArguments 指稳定副本（指回 Desktop 仓库＝TCC 每天静默失败 126，本仓已付过一次账）'
eq110 "$WDI110" 'grep -q "${WD_HOME}/check_mac_ops_watchdog.sh"' 1 '装机期自检 plist 模板与副本同源（模板没跟着改就拒绝安装旧版本）'
eq110 "$WDI110" 'migrate_ntfy_topic_to_keychain.sh' 1 '缺主题时只指迁移器（提示式 -w 写法教出来的空口令条目 10-10 真踩过，安装器不得再教）'
eq110 "$WDI110" 'launchctl bootstrap' 1 '重载走正规通道（只 cp 不 bootstrap＝装了没生效，正是本批 ② 要消灭的形态）'
# 发送重试四把（2026-10-10 看门狗首跑实录：ntfy.sh 间歇重置，一次判死＝断网窗口告警整批丢）：
eq110 "$WD110" 'NTFY_ALERT_ATTEMPTS="${NTFY_ALERT_ATTEMPTS:-3}"' 1 '重试次数走 env 且缺省 3（写死循环上界＝夹具/文案与实跑分家）'
eq110 "$WD110" 'NTFY_ALERT_BACKOFF_S="${NTFY_ALERT_BACKOFF_S:-5}"' 1 '重试间隔走 env（门禁夹具取 0 快拨，不为此拆第二条代码路径）'
eq110 "$WD110" 'while [ "${attempt}" -le "${NTFY_ALERT_ATTEMPTS}" ]; do' 1 '重试循环上界与次数同一来源（上界另写一个数＝循环次数与文案报的次数脱钩）'
eq110 "$WD110" '已试 ${NTFY_ALERT_ATTEMPTS} 次仍失败' 1 '失败文案报的就是循环上界那个变量（派生读数，不是第二个写死的 3）'
# 顺序锁：主题库必须先于两条腿被 source——腿里的 alert() 用到 NTFY_TOPIC，source 挪到腿后＝
# 每次都走"无主题"分支、告警全部哑掉而两条腿的读数照绿（"判据从没真跑过"的供给侧版本）。
WD_LIB_N=$(grep -n '\. "$NTFY_LIB"' "$WD110" | head -1 | cut -d: -f1 || true)
WD_PULL_N=$(grep -n 'WATCHDOG|pull verdict=' "$WD110" | head -1 | cut -d: -f1 || true)
CNT110=$((CNT110 + 1))
[ -n "$WD_LIB_N" ] && [ -n "$WD_PULL_N" ] && [ "$WD_LIB_N" -lt "$WD_PULL_N" ] \
	|| { echo "--- FAIL: §110 WD 顺序锁 ${CNT110}（ntfy 库 source 行=${WD_LIB_N:-无} 必须先于拉取腿读数行=${WD_PULL_N:-无}：倒序＝告警通道在腿运行时还没接上）"; exit 1; }
# ── WD1–WD7 行为腿：/tmp 夹具树，仓库与真实副本一个字节不动 ──
WDF="$W3T/wd"
mkdir -p "$WDF/home/backups/quant" "$WDF/bin" "$WDF/run" "$WDF/fakerepo" "$WDF/logdir"
cp "$WD110" "$WDF/run/check_mac_ops_watchdog.sh"
cp deploy/mac/ntfy_topic.sh "$WDF/run/ntfy_topic.sh"
cat > "$WDF/run/check_mac_agent_drift.sh" <<'SHWD'
#!/bin/bash
# 桩探测器：退出码由 DRIFT_RC 给，汇总行形状与真件一致（看门狗只消费这一行的"有没有"）。
echo "DRIFT-SUMMARY|pairs=99 same=99 diff=0 missing_mirror=0 missing_repo=0 unresolved=0 repo_root=stub"
exit "${DRIFT_RC:-0}"
SHWD
cat > "$WDF/bin/curl" <<'SHWDC'
#!/bin/bash
# 桩 curl：把命令行记进文件（"告警真发了"与"只落日志"就差这一笔），永不真的外呼。
# CURL_FAIL=1 ⇒ 恒败（WD8 重试腿用它数"到底拨了几次"）。
printf 'CURL %s\n' "$*" >> "${CURL_LOG:?CURL_LOG 未设}"
if [ -n "${CURL_FAIL:-}" ]; then exit 7; fi
exit 0
SHWDC
cat > "$WDF/bin/security" <<'SHWDS'
#!/bin/bash
# 桩 security：一律"取不到条目"。不放它，无主题那一例会去读**真钥匙串**——
# 既把真主题送进夹具日志，又让 ALERT-NOT-SENT 那条永远测不到（夹具自己制造被测形态）。
exit 1
SHWDS
chmod +x "$WDF/run/check_mac_ops_watchdog.sh" "$WDF/run/ntfy_topic.sh" "$WDF/run/check_mac_agent_drift.sh" \
	"$WDF/bin/curl" "$WDF/bin/security"
wd_run() { # $1=case 名 $2=PULL_LOG 路径 $3=额外 env（K=V 空格分隔，可为空）
	local name="$1" pull="$2" extra="${3:-}" out rc
	rm -f "$WDF/curl.log" "$WDF/home/backups/quant/watchdog_record.jsonl" "$WDF/home/backups/quant/watchdog.log"
	# shellcheck disable=SC2086
	out="$(env HOME="$WDF/home" LOG_DIR="$WDF/home/backups/quant" QUANT_REPO_ROOT="$WDF/fakerepo" \
		NTFY_TOPIC="topicstubvalue" CURL_LOG="$WDF/curl.log" PATH="$WDF/bin:/usr/bin:/bin" \
		PULL_LOG="$pull" DRIFT_RC="${WD_DRIFT_RC:-0}" $extra \
		/bin/bash "$WDF/run/check_mac_ops_watchdog.sh" 2>&1)" && rc=0 || rc=$?
	# rc 必须由 wd_run 自己落盘：调用方 `wd_run …; echo "$?"` 记到的是本函数的返回值（最后一条 printf），
	# 恒 0 ⇒ 期望退 1/2 的腿会全部假红（10-10 夹具首拨真踩过这个形状）。
	printf '%s\n' "$rc" > "$WDF/$name.rc"
	printf '%s\n' "$out" > "$WDF/$name.out"
}
wd_assert() { # $1=case 名 $2=期望 rc $3=overall 必含串 $4=额外断言命令（可为空）
	local name="$1" want_rc="$2" overall="$3" extra="${4:-}" got_rc
	# 退出码只认 wd_run 落盘的 .rc（.out 是合并了 stderr 的输出，退出码不在其中）
	got_rc="$(cat "$WDF/$name.rc")"
	[ "$got_rc" = "$want_rc" ] || { echo "--- FAIL: §110 WD $name 退 ${got_rc}（应 ${want_rc}）：$(grep -a 'WATCHDOG|' "$WDF/$name.out" | tail -2)"; exit 1; }
	grep -aF -- "$overall" "$WDF/$name.out" >/dev/null \
		|| { echo "--- FAIL: §110 WD $name 汇总行没带 ${overall}：$(grep -a 'WATCHDOG|' "$WDF/$name.out" | tail -2)"; exit 1; }
	[ -z "$extra" ] || eval "$extra" || { echo "--- FAIL: §110 WD $name 附加断言失败（${extra}）"; exit 1; }
	CNT110=$((CNT110 + 1))
}
# WD1 ok 态：新鲜锚 + 探测器 rc0 ⇒ 退 0、overall=ok、留档恰一行 result=ok、零外呼
printf '%s\n' "$(date '+%Y-%m-%d %H:%M:%S') === 备份成功 ===" > "$WDF/logdir/ok.log"
wd_run ok "$WDF/logdir/ok.log"
wd_assert ok 0 'overall=ok rc=0' '[ "$(grep -c . "$WDF/home/backups/quant/watchdog_record.jsonl")" = "1" ] && grep -qF "\"result\":\"ok\"" "$WDF/home/backups/quant/watchdog_record.jsonl" && [ ! -s "$WDF/curl.log" ]'
echo "ok - §110 WD1 心跳在阈内+零漂移 ⇒ 退 0、留档一行 result=ok、零外呼（这条腿的绿有留档背书）"
# WD2 stale：80h 前的锚 ⇒ 退 1、文案点名阈值、curl 恰一次（告警真发出去，不是只落日志）
printf '%s\n' "$(date -v-80H '+%Y-%m-%d %H:%M:%S') === 备份成功 ===" > "$WDF/logdir/stale.log"
wd_run stale "$WDF/logdir/stale.log"
wd_assert stale 1 'pull=stale' 'grep -qF "threshold_h=30" "$WDF/stale.out" && [ "$(grep -c . "$WDF/curl.log")" = "1" ]'
echo "ok - §110 WD2 心跳超龄 ⇒ 退 1、文案带阈值、ntfy 真发一次（读数与推送两条出口都活着）"
# WD3 两种相反成因分开：没有成功锚（跑了但每次都失败）与日志不存在（从没被拉起）不许共用一个态
printf '%s\n' '2026-10-10 07:10:02 ERROR: 读不到广州 SNAPSHOT_OK' > "$WDF/logdir/noanchor.log"
wd_run noanchor "$WDF/logdir/noanchor.log"
wd_assert noanchor 1 'pull=no-anchor'
wd_run nolog "$WDF/logdir/absent.log"
wd_assert nolog 2 'pull=no-log'
echo "ok - §110 WD3 no-anchor（退 1＝要人看失败原因）与 no-log（退 2＝要人查 launchd）两种成因两个态"
# WD4 读不出时间戳 ⇒ 退 2（探测器判据失效必须显式点名，不许冒充"没问题"或"超龄"）
printf '%s\n' 'no timestamp here === 备份成功 ===' > "$WDF/logdir/unparsable.log"
wd_run unparsable "$WDF/logdir/unparsable.log"
wd_assert unparsable 2 'pull=unparsable'
echo "ok - §110 WD4 锚行取不到时间戳 ⇒ 退 2 且点名 unparsable（读不出≠超龄≠没问题）"
# WD5 漂移三态：rc1=有漂移（退 1）、rc2=探测器自己坏（退 2）、仓库根不存在（退 2，绝不报"没有漂移"）
WD_DRIFT_RC=1 wd_run drift "$WDF/logdir/ok.log"
wd_assert drift 1 'drift=drift'
WD_DRIFT_RC=2 wd_run probebroken "$WDF/logdir/ok.log"
wd_assert probebroken 2 'drift=probe-broken'
wd_run rootabsent "$WDF/logdir/ok.log" 'QUANT_REPO_ROOT=/tmp/wd_no_such_repo_110'
wd_assert rootabsent 2 'drift=repo-root-absent'
echo "ok - §110 WD5 漂移 rc1/rc2 与仓库根缺失三态分开定级（读不出绝不冒充没有漂移）"
# WD6 无主题：stale 夹具 ⇒ ALERT-NOT-SENT 落日志、零外呼、腿级判决不受影响（退 1）
wd_run notopic "$WDF/logdir/stale.log" 'NTFY_TOPIC='
wd_assert notopic 1 'overall=alert rc=1' 'grep -qF "ALERT-NOT-SENT reason=no-topic" "$WDF/home/backups/quant/watchdog.log" && [ ! -s "$WDF/curl.log" ]'
echo "ok - §110 WD6 无主题 ⇒ 显式留痕不外呼、腿级判决照常（降级不得把有事读成没事）"
# WD7 摘锁反证两枚（各断本枚独有读数）：
#   a) 摘掉 record_line 调用 ⇒ WD1 的"留档非空"断言必须失真（留档空）＝留档断言承重；
#   b) 把 exit "$rc" 钉成 0 ⇒ WD2 的退出码断言必须失真（退 0）＝三态语义承重。
mkdir -p "$WDF/mut"
cp "$WD110" "$WDF/mut/check_mac_ops_watchdog.sh"
cp deploy/mac/ntfy_topic.sh "$WDF/mut/ntfy_topic.sh"
cp "$WDF/run/check_mac_agent_drift.sh" "$WDF/mut/check_mac_agent_drift.sh"
# WD7a 不走 wd_run（要盯原样输出），所以共享的留档文件得自己清：不清则上一腿留下的行
# 会把「摘掉 record_line 后留档必须空」这条反证读成"仍在写"＝反证自己被旧数据弄瞎。
rm -f "$WDF/home/backups/quant/watchdog_record.jsonl"
f3_sub "$WDF/mut/check_mac_ops_watchdog.sh" 'record_line "$res" "$rc" "$DETAIL"' ': # record_line 摘除'
CNT110=$((CNT110 + 1))
out110="$(env HOME="$WDF/home" LOG_DIR="$WDF/home/backups/quant" QUANT_REPO_ROOT="$WDF/fakerepo" \
	NTFY_TOPIC="topicstubvalue" CURL_LOG="$WDF/curl.log" PATH="$WDF/bin:/usr/bin:/bin" \
	PULL_LOG="$WDF/logdir/ok.log" DRIFT_RC=0 \
	/bin/bash "$WDF/mut/check_mac_ops_watchdog.sh" 2>&1)" || wd7_rc=$?
printf '%s\n' "$out110" | grep -qF 'overall=ok rc=0' \
	|| { echo "--- FAIL: §110 WD7a 摘留档后连 overall 都没了（破坏改变了别的东西，反证没落在点上）"; exit 1; }
[ ! -s "$WDF/home/backups/quant/watchdog_record.jsonl" ] \
	|| { echo "--- FAIL: §110 WD7a 摘掉 record_line 后留档仍非空（WD1 的留档断言不承重＝锁是装饰）"; exit 1; }
cp "$WD110" "$WDF/mut/check_mac_ops_watchdog.sh"
f3_sub "$WDF/mut/check_mac_ops_watchdog.sh" 'exit "$rc"' 'exit 0'
# rc 收码必须在父壳里做：`out="$(… && v=0 || v=$?)"` 的赋值落在替换子壳里，
# set -u 下父壳读它就是 unbound（WD7 首拨真撞过）。
wd7b_rc=0
CNT110=$((CNT110 + 1))
out110="$(env HOME="$WDF/home" LOG_DIR="$WDF/home/backups/quant" QUANT_REPO_ROOT="$WDF/fakerepo" \
	NTFY_TOPIC="topicstubvalue" CURL_LOG="$WDF/curl.log" PATH="$WDF/bin:/usr/bin:/bin" \
	PULL_LOG="$WDF/logdir/stale.log" DRIFT_RC=0 \
	/bin/bash "$WDF/mut/check_mac_ops_watchdog.sh" 2>&1)" || wd7b_rc=$?
[ "$wd7b_rc" = "0" ] \
	|| { echo "--- FAIL: §110 WD7b 钉死 exit 0 后仍退非零（破坏没落到退出码上，反证测的不是它声称的）"; exit 1; }
printf '%s\n' "$out110" | grep -qF 'overall=alert rc=1' \
	|| { echo "--- FAIL: §110 WD7b 钉死 exit 0 后汇总行也变了（破坏改变了读数面，两枚反证纠缠）"; exit 1; }
echo "ok - §110 WD7 摘 record_line ⇒ 留档空而 overall 照旧；钉死 exit ⇒ 退 0 而 overall 照旧（两枚反证各落各的点）"
# WD8 发送重试：桩 curl 恒败 ⇒ 恰 ATTEMPTS 次外呼、失败文案带次数、腿级判决照常（退 1 不因发送失败升级＝重试只救通道不改判决）。
wd_run sendfail "$WDF/logdir/stale.log" 'CURL_FAIL=1 NTFY_ALERT_ATTEMPTS=3 NTFY_ALERT_BACKOFF_S=0'
wd_assert sendfail 1 'pull=stale' '[ "$(grep -c . "$WDF/curl.log" || true)" = "3" ] && grep -qF "已试 3 次仍失败" "$WDF/home/backups/quant/watchdog.log"'
echo "ok - §110 WD8 桩 curl 恒败 ⇒ 重试恰 3 次外呼、失败文案带次数、腿级判决照常"
# WD8 摘锁：把重试上界钉成 1 ⇒ 同一夹具只拨一次＝WD8 的「恰 3 次」断言失真＝重试循环承重。
cp "$WD110" "$WDF/mut/check_mac_ops_watchdog.sh"
f3_sub "$WDF/mut/check_mac_ops_watchdog.sh" 'while [ "${attempt}" -le "${NTFY_ALERT_ATTEMPTS}" ]; do' 'while [ "${attempt}" -le 1 ]; do'
CNT110=$((CNT110 + 1))
wd8m_rc=0
out110="$(env HOME="$WDF/home" LOG_DIR="$WDF/home/backups/quant" QUANT_REPO_ROOT="$WDF/fakerepo" \
	NTFY_TOPIC="topicstubvalue" CURL_LOG="$WDF/curl.log.wd8m" PATH="$WDF/bin:/usr/bin:/bin" \
	CURL_FAIL=1 NTFY_ALERT_ATTEMPTS=3 NTFY_ALERT_BACKOFF_S=0 \
	PULL_LOG="$WDF/logdir/stale.log" DRIFT_RC=0 \
	/bin/bash "$WDF/mut/check_mac_ops_watchdog.sh" 2>&1)" || wd8m_rc=$?
wd8m_n="$(grep -c . "$WDF/curl.log.wd8m" || true)"
[ "${wd8m_n:-0}" = "1" ] \
	|| { echo "--- FAIL: §110 WD8 摘锁后外呼数=${wd8m_n:-0}（应恰 1＝上界被钉死；读数不对＝破坏落错了地方）"; exit 1; }
rm -f "$WDF/curl.log.wd8m"
echo "ok - §110 WD8 摘锁 钉死上界=1 ⇒ 同夹具只拨一次（重试循环承重）"
rm -rf "$W3T" "$W3F"
# 段尾总结把 CNT110 打出来：门禁段数与判定点数都是**要对外报的数**，
# 让日志自己带读数，比事后靠记忆写"约 60 道"诚实（§GATE-COUNT-LOCK 同一诉求）。
# 字面公网 IP 这一条**不自己再扫一遍**：读 §107 那条派生扫描的读数（IP_HITS / IP_SCAN_N）。
# 为什么接而不是重写：两把锁各写一遍同一判据，将来只会有一被改、另一继续用旧口径——
# 而"旧口径还绿"比"红着"更危险（§0929DRILL 的 record_freshness 单实现同族）。
# §P2-L（2026-10-07 波 4）交接断言：位置从段中挪到段尾，判据从「判红」改成走 gate_need。
# 为什么挪位置：本段四十多道判定点里只有这一条依赖 §107。收集模式下若在段中就 exit，
# 后面的锁一条都不跑——那正好把"前段坏了、后段还能不能查出别的"这件事一起抹掉。
# 为什么改走 gate_need：§107 判红时 IP_HITS 未定义，这条断言的成因是"§107 没跑完"，
# 不是"§110 坏了"。把它记成 SKIP-BY-DEPENDENCY，汇总里 FAIL 与 SKIP 两个数才各说各的事
# （H3 钉的就是这个形状：依赖腿不许冒充 PASS）。
CNT110=$((CNT110 + 1))
IP110_HAS=0
if [ -n "${IP_HITS+set}" ]; then IP110_HAS=1; fi
gate_need 110 "$IP110_HAS" '§107 的派生 IP 扫描没在本段之前跑完（IP_HITS 未定义＝「Mac 侧不许内嵌字面公网 IP」退化成一句从没执行过的承诺）'
[ "${IP_HITS:-1}" = "0" ] || { echo "--- FAIL: §110 负锁 ${CNT110}（沿用 §107 扫描读数：deploy/mac 里出现字面公网 IPv4 ${IP_HITS} 处，出口只应是 ssh 别名 gz 或运行期参数）"; exit 1; }
CNT110=$((CNT110 + 1))
[ "${IP_SCAN_N:-0}" -ge 13 ] || { echo "--- FAIL: §110 交接正锁 ${CNT110}（§107 的待扫文件数=${IP_SCAN_N}，<13＝那条 glob 已被人改窄，本段继承的是空扫描）"; exit 1; }
echo "ok - §110 交接锁：沿用 §107 派生扫描读数（待扫文件 ${IP_SCAN_N} 个 / 字面公网 IP ${IP_HITS} 处）"
echo "ok - §110 全段通过：静态锁 + F2 派生集合等值 + F3 双向镜像反证 + F1 判读十九腿与摘锁反证十二枚 + F3 Mac 侧五组与 kuma 必传腿 + ⑤ Mac 拉取腿重试与副本漂移探测（FD1–FD8，其中 FD5 只回显真仓库现况不判红），累计判定点 ${CNT110}"

echo "==> 111 §P2-L/§P2-K/§P2-M 门禁体系自身（波 4）：收集模式驱动 + 装配器 + 编码口径 + DNS 注入点——静态锁、镜像行为腿与五枚「本批真踩过的坑」的反证..."

# 本段守的是**验证机制自己**，不是业务代码。三件事各自的历史成因：
#
#  ① P2-L 收集模式（owner 裁决 ①：默认首红即退语义不变，另加 `-collect`）：
#     10-06 实录里 §104 gofmt 判红把 §105–107 整段带走，"改了 Go 文件、专项锁却从没验过"这件事
#     正是被机制本身埋掉的。但收集模式的新风险是**它自己会撒谎**：装配坏了一段都没跑，
#     汇总若只数「有没有 FAIL」，就会把「没验过」读成「验过了」——比首红即退更危险。
#     所以下面几枚腿全部围绕「读数归属」而不是「跑通了没」：EMPTY（L8）/ SILENT（L1）/ SKIP（L1·L5）/
#     收尾歧义（L6）/ 工作树漂移（L7）五种形状各自必红，外加「四桶之和 == 派生总段数」的等值锁（H2）
#     与「默认模式仍首红即退」的语义锁（L2）。
#  ② P2-K DNS 注入点：矩阵的一条正例真拨 api.siliconflow.cn，断网判红、一分钟后单跑 PASS 17.99s。
#     修法留了一个**默认值就是 net.LookupIP** 的注入点，测试装确定性答案。于是"生产路径没变"
#     这件事必须锁死：非测试代码里 DNS 解析调用恰好一处（就是注入点默认值），否则「改回直调」是静默的。
#  ③ P1-C 自证清单（G3）：069c380 的提交说明只写了「go build + 四包 PASS」，缺整轮门禁读数，
#     于是「没跑全量门禁」与「跑了但没写」在事后看不出区别。首犯 WARN、连续两批缺即红。
#     同一条 P1-C 还留了个更阴的口子：§104 的 `gofmt -l internal cmd 2>/dev/null || true` 在 gofmt
#     根本不在 PATH 的机器上会给出**空串**，那枚「必须为空」的等值锁于是恒绿。所以计划里的 G2
#     不落在 §104 段内（那里只有判据自己），而是作为对照腿落在本段（下面 ⑥ 组）：镜像里写歪一个
#     文件必须被抓到、gofmt -w 复位之后必须变空——两个方向都测，才叫对照。
#
#  ④ 计划里的 I3「全仓 _test.go 真实公网域名命中=0」**没有按原样落码**，理由按本仓纪律必须落纸：
#     判据的射程在派生阶段就不是"测试代码"——工具缓存目录 .qoder/ 下躺着 984 个 _test.go（不在版本控制里，
#     却会被 `**/_test.go` 的 glob 一并扫进），而仓库正文里的 api.siliconflow.cn、eastmoney、10jqka、ntfy.sh
#     这些域名字面量是**数据**（配置默认值、告警主题、文档），不是出呼点。本轮实测的分面是：受版本控制的
#     418 个 `_test.go` 里有 200 处域名字面量命中（剔掉 example.com 这类夹具域名仍剩 171 处，铺在 33 个文件上），
#     `.qoder/` 工具缓存里另有 984 个同名文件。照原文写死"命中=0"会把这一百多处数据命中一起判红，
#     收场的写法只能是"再加一份域名白名单"——白名单就是第二本账（§BOM-REPO-DERIVE 的教训：
#     清单式锁对下一个新增项天生失明）。于是换成锁**能不能出呼**而不是锁**字符串在不在**：
#     注入点默认值等值锁（下面 ④ 那三枚）+ 黑洞代理行为腿 I1 + 退回直调必红 I2。
#     I1 把 HTTP_PROXY/HTTPS_PROXY 指到 127.0.0.1:1 再跑整包，只要还有一条腿真出呼、真解析，它就红。
#  ⑤ P2-M 分类器读日志的编码口径（本批第二轮实跑逼出来的，见下面 ②b/②c 两组锁与 L10/L11/R4 三条腿）：
#     `-collect` 第一轮（10-07 04:39）109 段全绿、日志里零条 `--- FAIL`，汇总却报 `BALANCE=MISSING:1,2,…,111`。
#     根因是 §110 的一条中文读数被 `LC_ALL=C` 下的 bash 切片 `${got:0:92}` 按**字节**劈开，日志里落下
#     一个孤立的 0xe6，分类器严格解码当场抛异常。显示层的一次截断把验证层的整轮结论抹掉了——
#     这是收集模式自己的一种撒谎形态，所以它必须和 EMPTY/SILENT 那四种同批立起来。
#
# 反证为什么只做五枚（R1/R2/R3/R4/I2）而不是把每枚静态锁各破一次：这五枚是**本批开发过程中真的坏过、
# 并且真的被夹具抓到**的形态（前言函数被排除出收集面、SKIP 桶按逗号切、主动跳过的段被归进 SILENT、
# 分类器读日志退回严格解码、调用点退回裸 DNS）。把它们写成「退回旧实现必须在同一个夹具上复现缺陷」的腿，比再补十枚同族文案锁有用：
# 前者证明判据有牙，后者只证明字符串在位（§P1-A 教训：锁了形状没锁可达性）。
CNT111=0
GS111=scripts/verify_changes.sh
GPY111=scripts/gate_sections.py
SRV111=internal/server/server.go
RPY111="$PWD/$GPY111"
W111="$(mktemp -d /tmp/p2l-XXXXXX 2>/dev/null || true)"
[ -n "$W111" ] || { echo "--- FAIL: §111 建不出镜像目录，收集模式的十条镜像行为腿（L1/L2/L4–L11，L3 的三枚等值锁在 L1 里以 H1/H2/H3 落地）全部无法跑（宁可红，不许跳）"; exit 1; }
# 等值锁的读数面是「§111 之前的门禁正文」，不是整份脚本。为什么必须切：本段的一百来道锁
# 逐个把被判的字符串抄在自己的参数里（写锁的人没法不抄），拿整份文件去数「恰好一处」，
# 数到的是"定义一次 + 本段抄了 N 次"，预演读数被迫写成 6、12 这种数——那就不再是"只有一处"，
# 而是"没人再抄第二遍"，锁的含义被自己的文案稀释。切开后：定义面（真代码）照常判，
# 本段自己的引用不参与，负锁也不必再给"注释里提到"留豁免。
HDR111_LN=$(grep -nF 'echo "==> 111 ' "$GS111" 2>/dev/null | head -1 | cut -d: -f1 || true)
case "$HDR111_LN" in
''|*[!0-9]*) echo "--- FAIL: §111 读数面锁：在门禁里找不到本段的段头（或段头写法被改），切片没有右边界——宁可红，不许拿整份文件凑数"; exit 1 ;;
esac
GB111="$W111/gate_before_111.txt"
sed -n "1,$((HDR111_LN - 1))p" "$GS111" > "$GB111"

eq111() { # $1=文件 $2=整串 $3=预演读数 $4=说明
	CNT111=$((CNT111 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §111 整串等值锁 ${CNT111}（${4}）：${1} 整串「${2}」got=${got:-0} 预演=$3"; exit 1; }
}
neg111() { # $1=文件 $2=整串 $3=说明 → 应彻底没有
	CNT111=$((CNT111 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "0" ] || { echo "--- FAIL: §111 负锁 ${CNT111}（${3}）：${1} 又出现「${2}」got=${got}"; exit 1; }
}
min111() { # $1=说明 $2=实得 $3=应≥ —— 派生面过窄即红（空转正锁家族）
	CNT111=$((CNT111 + 1))
	[ "${2:-0}" -ge "$3" ] || { echo "--- FAIL: §111 派生正锁 ${CNT111}（${1}）：实得=${2:-0} 应≥${3}"; exit 1; }
}
ln111() { # $1=文件 $2=整串 → 首个命中行号（0＝没有）；顺序锁用
	local n
	n=$(grep -nF -- "$2" "$1" 2>/dev/null | head -1 | cut -d: -f1 || true)
	printf '%s' "${n:-0}"
}
kv111() { # $1=键 $2=整段输出 → 驱动回显的是 KEY=VALUE 形态（不是制表列），这里按 = 取
	printf '%s\n' "$2" | sed -n "s/^$1=//p" | head -1
}
code_hits111() { # $1=文件 $2=扩展正则 → 非注释命中行数（与 §110 的网关命中计数同一把尺子）
	{ grep -nE "$2" "$1" 2>/dev/null || true; } | { grep -Ev '^[0-9]+:[[:space:]]*(//|\*)' || true; } | wc -l | tr -d ' '
}

# ── ① 驱动块与装配器的形状（静态锁）──
eq111 "$GB111" '# >>> GATE-COLLECT-DRIVER' 1 '驱动块起始标记恰好一处（两处＝前言里抄了第二份驱动，两遍装配互踩）'
eq111 "$GB111" '# <<< GATE-COLLECT-DRIVER' 1 '驱动块结束标记恰好一处（与上枚成对；gate_sections.py 的 check 靠这对标记才放行前言里的语句）'
eq111 "$GPY111" 'DRIVER_BEGIN = "# >>> GATE-COLLECT-DRIVER"' 1 '装配器认识的起始标记与门禁写的字面一致（不同步＝check 要么把驱动语句当副作用判红，要么整段失去保护）'
eq111 "$GPY111" 'DRIVER_END = "# <<< GATE-COLLECT-DRIVER"' 1 '装配器认识的结束标记同上'
eq111 "$GB111" 'GATE_SELF_RAW="$PWD/$0"' 1 '相对形式的 $0 按调用时的 PWD 定死（case 的 `*)` 分支；缺了它＝cd 之后基准漂移、装配器路径算到上一层兄弟目录，L9 行为腿演示的就是这个坏法）'
eq111 "$GB111" 'GATE_SELF_RAW="$0" ;;' 1 '绝对形式的分支照旧直取 ${0}（两枚分开数：两个分支本就是两种形状，合成一个计数会把「少了一个分支」读成「多了一处引用」）'
CNT111=$((CNT111 + 1))
L_DRV=$(ln111 "$GB111" '# >>> GATE-COLLECT-DRIVER')
L_CD111=$(ln111 "$GB111" 'cd "$(dirname "$0")/.."')
if [ "$L_DRV" -le 0 ] || [ "$L_CD111" -le 0 ] || [ "$L_DRV" -ge "$L_CD111" ]; then
	echo "--- FAIL: §111 顺序锁 ${CNT111}（驱动块必须待在前言 cd **之前**：实得 L_DRV=$L_DRV L_CD=${L_CD111}）"
	echo "    cd 之后 \$0 的相对基准就变了，装配器路径会算到上一层兄弟目录——L9 行为腿演示的就是这个坏法"
	exit 1
fi
eq111 "$GB111" 'if [ "$_gate_arg" = "-collect" ]; then GATE_ARG_COLLECT=1; fi' 1 '参数扫描写成 if（`[ x ] && y` 在条件为假时整条语句返回 1，set -e 下前言当场裸死＝零读数，正是 §89 自指锁那一族）'
neg111 "$GB111" '[ "$_gate_arg" = "-collect" ] &&' '上一条的旧写法不许复活'
eq111 "$GB111" 'GATE_COLLECT_INNER=1 bash "$work/inner_$idx.sh" $GATE_ARG_INNER' 1 '内层脚本必须带 GATE_COLLECT_INNER 起（gate_need 的 SKIP 分支与递归防护都读这把键）'
eq111 "$GB111" 'echo "GATE-SKIP ${sid} · ${reason}"' 1 'gate_need 的 SKIP 标记格式与装配器 SKIP_RE 严格对齐（对不上＝跳过被读成 SILENT，成因归属就错了）'
eq111 "$GB111" '本遍记 SKIP（成因由段自己声明，原文照抄）' 1 '驱动把内层日志里的 GATE-SKIP 原文回显到 stdout（只留在 log 文件里的话，现网拿到的汇总就只剩「少了一段」，看不出那段自己写了什么；§111 L1 断言的正是这份可见性）'
eq111 "$GPY111" 'SKIP_RE = re.compile(r'"'"'^GATE-SKIP (\d+) · (.*)$'"'"')' 1 '解析侧的字面量与上枚成对'
eq111 "$GB111" 'GATE_COLLECT_MAX_PASSES="${GATE_COLLECT_MAX_PASSES:-60}"' 1 '遍数上限的赋值恰好一处（每遍是「从红段跑到文件尾」，红得越早后面越贵；越限时未读到的段由 MISSING 面点名，不当绿）'
eq111 "$GB111" 'echo "WORK=${work}"' 1 '收尾三个读数各占一行、行首即键名（镜像腿就是这样取 WORK 去复跑 summary 的；挤成一行时只有第一个键能被 `^KEY=` 取到，其余两个在消费方读成空串＝反证空过）'

# ── ② 装配器：六个子命令在位 + 本批真坏过的两处判据 ──
eq111 "$GPY111" 'def cmd_sections(gate):' 1 '子命令 sections（派生段清单＝一切等值锁的分母）'
eq111 "$GPY111" 'def cmd_helpers(gate):' 1 '子命令 helpers（跨段 helper 依赖派生）'
eq111 "$GPY111" 'def cmd_emit(gate, start_id, out_path, root):' 1 '子命令 emit（尾体装配）'
eq111 "$GPY111" 'def cmd_classify(gate, log_path, rc):' 1 '子命令 classify（单遍四桶归属）'
eq111 "$GPY111" 'def cmd_summary(gate, work_dir):' 1 '子命令 summary（跨遍汇总 + 等值锁）'
eq111 "$GPY111" 'def cmd_check(gate):' 1 '子命令 check（前言副作用自检＝emit 的正确性前提）'
eq111 "$GPY111" 'if mask[i] or i < prologue_end:' 0 '前言函数不许被排除出收集面（R1 反证腿就是把这行写回来，看搬运腿会不会立刻归零）'
eq111 "$GPY111" 'elif started[-1] in skipped:' 1 '主动跳过的段归 SKIP 桶、不折算 SILENT（R3 反证腿破的就是这一条）'
eq111 "$GPY111" 'for key in ("PASS", "FAIL", "SILENT"):' 1 'PASS/FAIL/SILENT 按逗号切、SKIP 单独按分号切（R2 反证腿破的就是这一条）'
neg111 "$GPY111" 'for key in buckets:' '四桶同解的旧写法（SKIP 值是「id·原因」，逗号切不动 ⇒ 汇总凭空少一段）'
eq111 "$GPY111" 'print("PASS_IDS\t%s"' 1 'PASS 段号也要点名：只给四个计数时，"同一段既在 PASS 又在 SKIP"这类重复看不出来'

# ── ②b §P2-M：分类器读日志的编码口径（整轮 -collect 第一轮实跑逼出来的那条）──
# 04:39 那轮的真实形态：109 段全绿跑完、日志里一条 `--- FAIL` 都没有，却因为**一个字节**
# （§110 的一条中文读数被 LC_CTYPE=C 的 bash 切片按字节劈开，见下面 ⑤ 组）解码失败，
# 分类器非零退出 ⇒ 汇总读成 `BALANCE=MISSING:1,2,…,111`。"全都验过"被报成"谁都没验"，
# 与本枚改造要消灭的形态同形、方向相反 ⇒ 这一组锁守的是收集模式自己的结论面。
eq111 "$GPY111" 'def read_lines(path, errors=None):' 1 '读文本的默认口径＝严格（errors=None），容错必须由调用点显式声明——不显式就等于偷偷全放开'
neg111 "$GPY111" 'def read_lines(path):' '旧签名（没有 errors 形参＝日志与源码一个口径，正是打挂整轮那份）不许复活'
eq111 "$GPY111" 'with open(path, encoding="utf-8", errors=errors) as fh:' 1 'errors 真的透传进了 open（只在 docstring 里讲道理＝零判据；这一串只出现在唯一的读文件处）'
eq111 "$GPY111" 'log = read_lines(log_path, errors="replace")' 1 '只有**段日志**这一入口容错：日志是别人写的字节（go test/pytest/ssh 回来的 PS 读数），一个坏字节不该把整遍结论抹掉'
eq111 "$GPY111" 'lines = read_lines(gate)' 6 '其余六个入口（sections/helpers/emit/classify/summary/check）读**门禁源码**一律严格：源文件里有非法字节＝解析器读的不是人们以为在看的那份脚本，派生读数全部作废比假装能读更安全'

# ── ②c §P2-M：门禁自己的日志面不许按字节截断 ──
# 三把尺子都是「代码行」锚（`^[[:space:]]*[^#]`）而不是全文计数：本段上面那份说明注释里
# 抄了旧写法 `${got:0:92}` 作为成因描述，按全文计数就会把这枚锁自己判红（§0929DRILL 同族：
# 负向 grep 误伤说明注释）。要禁的是**执行态**，不是提它。
# ★ 同一族的第四种自伤形态（2026-10-10 实踩）：**别的段的锚点**把被禁字面量原样抄进门禁正文——
#   §110 给 Mac 拉取腿钉了一枚 `'cut -c1-200'` 整串等值锁，锁自己绿不了，红报在本段名下。
#   这把尺子扫的是「§111 之前的门禁正文」（GB111 按行切到本段段头），所以它拦得住 §110 的锚点行。
#   修法选了**窄锚**（同一行改钉 `c1-200`：仍然承重，改成 1-100 或换截断方式照样红）而不是给本段开豁免口：
#   豁免口一旦存在，"这条截断其实在执行态"就无处可查了。
eq111 "$GB111" 'gate_clip() {' 1 '按字符截断的单实现（本段所有读数回显共用这一份；再写一份切片就是第二本账）'
eq111 "$GB111" 'echo "ok - §110 F1 ${1} => $(gate_clip 92 "$got")"' 1 'F1 判决腿的读数回显走 gate_clip（就是 04:39 那轮劈坏一个汉字的那一行）'
eq111 "$GB111" 'echo "ok - §110 F1 ${1} => $(gate_clip 92 "$V")"' 1 'F1 观测腿（10-09 加的 legnote110）同样走 gate_clip——新加的回显口子和老的一样会把中文读数截成半个字，不能只给老的那条上锁'
CNT111=$((CNT111 + 1))
for _p in '^[[:space:]]*[^#].*head -c ' '^[[:space:]]*[^#].*cut -c' '^[[:space:]]*[^#].*\$\{[A-Za-z_][A-Za-z0-9_]*:0:[0-9]+\}'; do
	_n=$(code_hits111 "$GB111" "$_p")
	if [ "${_n:-0}" != "0" ]; then
		echo "--- FAIL: §111 负锁 ${CNT111}（门禁正文里又出现按字节截断「${_p}」，实得 ${_n} 处）"
		echo "    按字节截断会在三字节汉字中间落一个非法 UTF-8 字节，而这份输出正是收集模式分类器的输入；上面 ②b 的容错只兜底，不授权这里继续写坏字节"
		exit 1
	fi
done

# ── ③ 跨段产物：语义依赖走 gate_need，纯路径变量各段自带 ──
eq111 "$GB111" 'dg=scripts/deploy_guangzhou.sh' 2 '§20 与 §23 各定义一次（路径变量没有语义依赖，不值得换来一个 SKIP；收集模式下读到空串会让假红挂在 §23 名下）'
eq111 "$GB111" 'MK_LOAD=cmd/dataload/minute_sync.go' 2 '§91 与 §92 各定义一次（同上）'
eq111 "$GB111" 'gate_need 110 "$IP110_HAS"' 1 '§110 沿用 §107 扫描读数这件事做成显式依赖声明（不声明＝前段红时本段读空值判红，归属说的是假话）'
CNT111=$((CNT111 + 1))
L_S110=$(ln111 "$GB111" 'echo "==> 110')
L_NEED110=$(ln111 "$GB111" 'gate_need 110 "$IP110_HAS"')
L_END110=$(ln111 "$GB111" 'echo "ok - §110 全段通过')
if [ "$L_S110" -le 0 ] || [ "$L_NEED110" -le 0 ] || [ "$L_END110" -le 0 ] || [ "$L_NEED110" -le "$L_S110" ] || [ "$L_NEED110" -ge "$L_END110" ]; then
	echo "--- FAIL: §111 顺序锁 ${CNT111}（gate_need 110 必须落在 §110 段内且贴近段尾：段头=$L_S110 依赖=$L_NEED110 段尾=${L_END110}）"
	echo "    放在段中＝前段一红，本段后面四十来道判定一条都不跑，收集模式反而比默认模式少给读数"
	exit 1
fi

# ── ④ P2-K：解析入口只有一处，且默认值就是真实 DNS ──
eq111 "$SRV111" 'var llmURLResolver = net.LookupIP' 1 '注入点的默认值＝真实解析（"生产语义一字未变"这条的落点）'
eq111 "$SRV111" 'ips, err := llmURLResolver(host)' 1 '闸口走注入点（改回裸 net.LookupIP 时这条与下面的等值计数一起红）'
eq111 "$SRV111" 'if len(ips) == 0 {' 1 '零地址也拒的 fail-closed 守卫（旧实现里「解析成功但零条」等于放行）'
CNT111=$((CNT111 + 1))
LOOKUP_CODE=$(code_hits111 "$SRV111" 'net\.LookupIP')
if [ "${LOOKUP_CODE:-0}" != "1" ]; then
	echo "--- FAIL: §111 等值锁 ${CNT111}（server.go 非注释行里 net.LookupIP 应恰好 1 处＝注入点默认值；实得 ${LOOKUP_CODE}）"
	echo "    第二处直调就是「矩阵又回到测这台机器能不能解析」，10-05 那条断网假红会原样复活"
	exit 1
fi
CNT111=$((CNT111 + 1))
SEAM_TEST_N=$(ls internal/server/llm_dns_seam_test.go 2>/dev/null | wc -l | tr -d ' ')
if [ "${SEAM_TEST_N:-0}" != "1" ]; then
	echo "--- FAIL: §111 文件在位锁 ${CNT111}（llm_dns_seam_test.go 不在位＝SSRF 的三条分支重新变成「只有真 DNS 才走得到」的死支）"
	exit 1
fi

# ── 镜像夹具：抽真驱动块 + 合成段体，跑六种失效形态 ──
p2l_body() { # $1=变体 $2=输出文件
	case "$1" in
	two_red) cat > "$2" <<'P2LB_ONE'
echo "==> 1 生产者段..."
PRODUCER_VAR=set-by-section-1
echo "ok - fixture s1"

echo "==> 2 判红段..."
echo "--- FAIL: fixture s2 deliberate red"
exit 1

echo "==> 3 绿段..."
echo "ok - fixture s3"

echo "==> 4 依赖段（消费 §1 写的 PRODUCER_VAR）..."
P4=0
if [ -n "${PRODUCER_VAR+set}" ]; then P4=1; fi
gate_need 4 "$P4" 'fixture：PRODUCER_VAR 由 §1 写入，未定义＝生产者没跑完'
echo "ok - fixture s4"

echo "==> 5 heredoc 诱饵 + 判红段..."
cat > "$PWD/.decoy.out" <<'P2LDECOY'
echo "==> 999 这一段在 heredoc 正文里，解析器不许把它当成真段头"
exit 1
P2LDECOY
echo "--- FAIL: fixture s5 deliberate red"
exit 1

echo "==> 6 静默退出段（既不判红、也不往下走、日志里也没有收尾标记）..."
echo "ok - fixture s6 partial"
exit 0

echo "==> 7 末段..."
echo "ok - fixture s7"

echo ""
echo "==> 全部通过"
P2LB_ONE
;;
all_green) cat > "$2" <<'P2LB_TWO'
echo "==> 1 绿段..."
G1=1
echo "ok - fixture g1"

echo "==> 2 绿段..."
echo "ok - fixture g2"

echo "==> 3 依赖段（生产者就是 §1，同一遍里跑到）..."
P3=0
if [ -n "${G1+set}" ]; then P3=1; fi
gate_need 3 "$P3" 'fixture：G1 由 §1 写入'
echo "ok - fixture g3"

echo "==> 4 末段..."
echo "ok - fixture g4"

echo ""
echo "==> 全部通过"
P2LB_TWO
;;
skip_only) cat > "$2" <<'P2LB_THREE'
echo "==> 1 绿段（不写任何产物）..."
echo "ok - fixture k1"

echo "==> 2 依赖段（生产者根本不存在，可前面一条红都没有）..."
P2=0
if [ -n "${NEVER_SET_VAR+set}" ]; then P2=1; fi
gate_need 2 "$P2" 'fixture：依赖声明本身写错了（生产者不存在）'
echo "ok - fixture k2"

echo "==> 3 末段..."
echo "ok - fixture k3"

echo ""
echo "==> 全部通过"
P2LB_THREE
;;
ambiguous) cat > "$2" <<'P2LB_FOUR'
echo "==> 1 绿段却打印判红文案..."
echo "--- FAIL: fixture prints a FAIL line and still exits 0"
echo "ok - fixture a1"

echo "==> 2 末段..."
echo "ok - fixture a2"

echo ""
echo "==> 全部通过"
P2LB_FOUR
;;
drift) cat > "$2" <<'P2LB_FIVE'
echo "==> 1 把工作树改脏的段..."
touch "$PWD/p2l_drift_file"
echo "ok - fixture d1"

echo "==> 2 末段..."
echo "ok - fixture d2"

echo ""
echo "==> 全部通过"
P2LB_FIVE
;;
order_bad) cat > "$2" <<'P2LB_SIX'
echo "==> 1 任意段..."
echo "ok - fixture o1"

echo ""
echo "==> 全部通过"
P2LB_SIX
;;
esac
}

p2l_make() { # $1=场景名 $2=段体文件 $3=非空则用「坏装配器」替身
	local d="$W111/$1"
	mkdir -p "$d/scripts" "$d/gate"
	if [ -n "${3:-}" ]; then
		cat > "$d/scripts/gate_sections.py" <<'P2LFAKE'
# §P2-L 反证夹具：只替 emit 写一个「没有段标记」的内层脚本，其余子命令原样转给真装配器。
# 验的是驱动对「装配坏了」的反应。真实坏法可能是 python 抛异常，也可能是更阴的
# "正常退出、却产出空脚本"——后者只有这种替身造得出来。
import os
import runpy
import sys

REAL = os.environ["P2L_REAL_PY"]
a = sys.argv[1:]
if a and a[0] == "emit":
    with open(a[2], "w", encoding="utf-8") as fh:
        fh.write('#!/usr/bin/env bash\nset -euo pipefail\ncd "%s"\necho "assembler produced no section markers"\n' % a[3])
    os.chmod(a[2], 0o755)
    sys.exit(0)
sys.argv = ["gate_sections.py"] + a
runpy.run_path(REAL, run_name="__main__")
P2LFAKE
	else
		ln -sf "$RPY111" "$d/scripts/gate_sections.py"
	fi
	printf '%s\n' '.decoy.out' > "$d/.gitignore"
	git init -q "$d" 2>/dev/null || true
	{
		printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail'
		if [ "$1" = "order_bad" ]; then
			printf '%s\n' 'cd "$(dirname "$0")/.."'
			sed -n '/# >>> GATE-COLLECT-DRIVER/,/# <<< GATE-COLLECT-DRIVER/p' "$GB111"
		else
			sed -n '/# >>> GATE-COLLECT-DRIVER/,/# <<< GATE-COLLECT-DRIVER/p' "$GB111"
			printf '%s\n' 'cd "$(dirname "$0")/.."'
		fi
		cat "$2"
	} > "$d/gate/fixture.sh"
	chmod +x "$d/gate/fixture.sh"
	printf '%s' "$d"
}

p2l_scene() { # $1=变体名 → 打印场景目录
	p2l_body "$1" "$W111/body_$1.txt"
	p2l_make "$1" "$W111/body_$1.txt"
}

OUT111=""
RC111=0
p2l_run() { # $1=场景目录 $2=附加环境变量串（可空）→ 置全局 OUT111 / RC111
	local d="$1" extra="${2:-}" rc=0
	RC111=0
	if [ -n "$extra" ]; then
		OUT111="$(cd "$d/gate" && env $extra bash fixture.sh -collect 2>&1)" || rc=$?
	else
		OUT111="$(cd "$d/gate" && bash fixture.sh -collect 2>&1)" || rc=$?
	fi
	RC111=$rc
}

# · L1（H1+H2+H3 一夹具四读）：双红 + heredoc 诱饵 + 依赖 + 静默退出
CNT111=$((CNT111 + 1))
D111="$(p2l_scene two_red)"
# R2/R3 两枚反证直接调装配器的 summary/classify：它们的输入是**镜像自己的**段清单（7 段），
# 不是真门禁的 109 段——拿真门禁去分桶，镜像的 7 段会剩 102 段 MISSING，读数与反证目标无关。
FIXGATE111="$D111/gate/fixture.sh"
p2l_run "$D111"
WORK2RED=$(kv111 WORK "$OUT111")
if [ "$RC111" = "0" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（两处刻意判红的镜像在 -collect 下整体 0 退出＝收集模式把红洗成了绿）"
	exit 1
fi
FIDs=$(kv111 FAIL_IDS "$OUT111")
PIDs=$(kv111 PASS_IDS "$OUT111")
SIDs=$(kv111 SKIP_IDS "$OUT111")
ZIDs=$(kv111 SILENT_IDS "$OUT111")
TOT=$(kv111 TOTAL_SECTIONS "$OUT111")
if [ "$FIDs" != "2,5" ]; then
	echo "--- FAIL: §111 行为腿 H1 ${CNT111}（FAIL_IDS 应同时含两处红＝2,5，实得「${FIDs}」；只报第一处就是被改造掉的旧机制复活）"
	exit 1
fi
if [ "$TOT" != "7" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（派生总段数应 7，实得 ${TOT}＝heredoc 正文里那行假段头（==> 999）被当成了真段，或段头正则读不到真段）"
	exit 1
fi
CNT111=$((CNT111 + 1))
PSUM=$(kv111 PASS_SECTIONS "$OUT111")
FSUM=$(kv111 FAIL_SECTIONS "$OUT111")
SSUM=$(kv111 SKIP_SECTIONS "$OUT111")
ZSUM=$(kv111 SILENT_SECTIONS "$OUT111")
BAL=$(kv111 BALANCE "$OUT111")
if [ "$((PSUM + FSUM + SSUM + ZSUM))" != "$TOT" ] || [ "$BAL" != "OK" ]; then
	echo "--- FAIL: §111 等值锁 H2 ${CNT111}（四桶 $PSUM+$FSUM+$SSUM+$ZSUM=$((PSUM + FSUM + SSUM + ZSUM)) 必须等于派生总段数 ${TOT}，BALANCE=${BAL}）"
	exit 1
fi
if [ "$SIDs" != "4" ]; then
	echo "--- FAIL: §111 行为腿 H3 ${CNT111}（§4 依赖 §1 的产物、本遍没跑到 ⇒ 必须记 SKIP，实得 SKIP_IDS=「${SIDs}」）"
	exit 1
fi
case ",$PIDs," in
*,4,*) echo "--- FAIL: §111 行为腿 H3 ${CNT111}（§4 同时出现在 PASS_IDS（${PIDs}）＝依赖没满足的段被算成通过，正是收集模式最该防的形状）"; exit 1 ;;
esac
case ",$ZIDs," in
*,4,*) echo "--- FAIL: §111 行为腿 ${CNT111}（§4 被记成 SILENT（${ZIDs}）＝SKIP_RE 与 gate_need 的标记格式对不上；跳过与静默是两种成因、两种修法，混桶会让人去改错的那一处）"; exit 1 ;;
esac
if [ "$ZIDs" != "6" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（§6 静默退出应记 SILENT，实得「${ZIDs}」＝「没验过」被折进了 PASS 或 SKIP）"
	exit 1
fi
printf '%s' "$OUT111" | grep -qF 'GATE-SKIP 4 · fixture' || { echo "--- FAIL: §111 行为腿 ${CNT111}（日志里没有 GATE-SKIP 标记原文＝SKIP 是从别处推出来的，不是段自己声明的）"; exit 1; }
printf '%s' "$OUT111" | grep -qF 'SKIP_REASON=4·fixture' || { echo "--- FAIL: §111 行为腿 ${CNT111}（汇总没回显 SKIP 成因：现网只看得到「少了一段」，看不出为什么少）"; exit 1; }
echo "ok - §111 L1（H1 双红同报 $FIDs · H2 四桶 $PSUM/$FSUM/$SSUM/$ZSUM 对 $TOT 闭合 · H3 依赖腿记 SKIP 不进 PASS · 静默退出记 SILENT）"

# · L2（owner 裁决 ①）：默认模式同夹具只报第一处就退
CNT111=$((CNT111 + 1))
rc111=0
DEF111="$(cd "$D111/gate" && bash fixture.sh 2>&1)" || rc111=$?
if [ "$rc111" = "0" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（不带 -collect 时镜像第一处红居然继续往下跑＝改动落到了公共前言，默认语义被动过）"
	exit 1
fi
printf '%s' "$DEF111" | grep -qF 'fixture s2 deliberate red' || { echo "--- FAIL: §111 行为腿 ${CNT111}（默认模式没报第一处红，报的是别的东西：$(printf '%s' "$DEF111" | tail -1)）"; exit 1; }
if printf '%s' "$DEF111" | grep -qF 'fixture s5'; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（默认模式跑到了第二处红＝首红即退被改掉，全仓所有 `verify_changes.sh` 调用点的行为跟着一起变）"
	exit 1
fi
echo "ok - §111 L2（默认模式 rc=${rc111}，只报 §2；§5 只有 -collect 才捞得出来）"

# · L4：全绿镜像 ⇒ 整体 0 退出、四桶全在 PASS、收尾歧义不触发
CNT111=$((CNT111 + 1))
D411="$(p2l_scene all_green)"
p2l_run "$D411"
if [ "$RC111" != "0" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（全绿镜像非 0 退出：$(printf '%s' "$OUT111" | tail -2 | tr '\n' ' ')）——收集模式自己红，谁还敢信它给的绿"
	exit 1
fi
if [ "$(kv111 FAIL_SECTIONS "$OUT111")" != "0" ] || [ "$(kv111 PASS_SECTIONS "$OUT111")" != "$(kv111 TOTAL_SECTIONS "$OUT111")" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（全绿时 FAIL 必须 0、PASS 必须等于总段数；实得 FAIL=$(kv111 FAIL_SECTIONS "$OUT111") PASS=$(kv111 PASS_SECTIONS "$OUT111") TOTAL=$(kv111 TOTAL_SECTIONS "$OUT111")）"
	exit 1
fi
echo "ok - §111 L4（全绿 $(kv111 PASS_SECTIONS "$OUT111")/$(kv111 TOTAL_SECTIONS "$OUT111") 段、rc=0）"

# · L5：只有 SKIP、一条红都没有 ⇒ 必须判红（否则「什么都没验」可以穿着绿衣服出场）
CNT111=$((CNT111 + 1))
D511="$(p2l_scene skip_only)"
p2l_run "$D511"
if [ "$RC111" = "0" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（有段被 SKIP 而 FAIL/SILENT 全 0，整体却 0 退出）"
	exit 1
fi
printf '%s' "$OUT111" | grep -qF '有段被记 SKIP 却没有任何 FAIL/SILENT' || { echo "--- FAIL: §111 行为腿 ${CNT111}（非零退出不是这条原因拦的，归属必须写清楚：$(printf '%s' "$OUT111" | tail -2 | tr '\n' ' ')）"; exit 1; }
echo "ok - §111 L5（SKIP-without-FAIL 判红，成因文案在位）"

# · L6：绿段里打印 `--- FAIL` 却 0 退出 ⇒ 收尾歧义判红
CNT111=$((CNT111 + 1))
D611="$(p2l_scene ambiguous)"
p2l_run "$D611"
if [ "$RC111" = "0" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（四桶全绿却印着 --- FAIL 文案，收集模式没判歧义＝某段的退出码不代表它的判据）"
	exit 1
fi
printf '%s' "$OUT111" | grep -qF '收尾歧义' || { echo "--- FAIL: §111 行为腿 ${CNT111}（非零退出没走歧义分支：$(printf '%s' "$OUT111" | tail -2 | tr '\n' ' ')）"; exit 1; }
echo "ok - §111 L6（收尾歧义判红）"

# · L7：某遍把工作树改脏 ⇒ 判红（后面的段读的是脏树，读数归属不再可信）
CNT111=$((CNT111 + 1))
D711="$(p2l_scene drift)"
p2l_run "$D711"
if [ "$RC111" = "0" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（镜像段往仓根写了文件，工作树漂移却没判红＝收集模式继续跑后面的段时读的是被改过的树）"
	exit 1
fi
printf '%s' "$OUT111" | grep -qF '把仓库工作树改动' || { echo "--- FAIL: §111 行为腿 ${CNT111}（非零退出不是漂移分支判的：$(printf '%s' "$OUT111" | tail -2 | tr '\n' ' ')）"; exit 1; }
echo "ok - §111 L7（工作树漂移判红）"

# · L8：装配坏了（emit 产出的脚本没有段标记）⇒ EMPTY 判红，绝不当"跑过了、全绿"
CNT111=$((CNT111 + 1))
p2l_body all_green "$W111/body_empty.txt"
D811="$(p2l_make empty_asm "$W111/body_empty.txt" fake)"
rc111=0
OUT111="$(cd "$D811/gate" && P2L_REAL_PY="$RPY111" bash fixture.sh -collect 2>&1)" || rc111=$?
if [ "$rc111" = "0" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（内层脚本一个段标记都没有却整体 0 退出——本枚改造要消灭的头号形态复辟）"
	exit 1
fi
printf '%s' "$OUT111" | grep -qF '一个段标记都没有' || { echo "--- FAIL: §111 行为腿 ${CNT111}（非零退出没走 EMPTY 分支：$(printf '%s' "$OUT111" | tail -2 | tr '\n' ' ')）"; exit 1; }
echo "ok - §111 L8（EMPTY 判红）"

# · L9：驱动块放在 cd 之后（本批真踩过的那次）⇒ 装配器路径算错、当场判红
CNT111=$((CNT111 + 1))
D911="$(p2l_scene order_bad)"
p2l_run "$D911"
if [ "$RC111" = "0" ]; then
	echo "--- FAIL: §111 行为腿 ${CNT111}（驱动块在 cd 之后还能正常工作＝上面那枚顺序锁成了摆设）"
	exit 1
fi
printf '%s' "$OUT111" | grep -qF '找不到装配器' || { echo "--- FAIL: §111 行为腿 ${CNT111}（非零退出没走「装配器不在位」分支，顺序坏法的读数不可归属）"; exit 1; }
echo "ok - §111 L9（顺序坏法当场现形）"

# ── R1/R2/R3：把本批真改坏过的三处退回旧实现，缺陷必须在同一个夹具上复现 ──
CNT111=$((CNT111 + 1))
IN111="$W111/inner_104.sh"
prelude111() { # $1=内层脚本 $2=段号 → 只回显"前置搬运区"（尾体第一行段标记之前）
	local f="$1" sid="$2" cut_ln
	cut_ln=$(grep -nF "echo \"GATE-SECTION-START ${sid}\"" "$f" 2>/dev/null | head -1 | cut -d: -f1 || true)
	case "$cut_ln" in
	''|*[!0-9]*) cat "$f" ;;
	*) sed -n "1,$((cut_ln - 1))p" "$f" ;;
	esac
}
# 为什么只量前置区：内层脚本的尾体是"从 104 段起到文件尾"，本段（111）自己的锁文案就在尾体里，
# 而文案里正抄着 `py_tests() {` 这三串——拿整份内层脚本数"恰好一处"，数到的是"搬运一次 + 本段抄两处"，
# 与 §111 开头的切片同一个道理（切片在 GB111，这里在 prelude）。判据要量的是**前置区搬没搬**，别的都不算。
EMIT104_RC=0
python3 "$GPY111" emit "$GS111" 104 "$IN111" "$PWD" 2>"$W111/emit_104.err" || EMIT104_RC=$?
if [ "$EMIT104_RC" != "0" ] || [ ! -s "$IN111" ]; then
	echo "--- FAIL: §111 装配搬运腿 ${CNT111}（emit 104 没产出内层脚本：rc=${EMIT104_RC}、产物非空=$([ -s "$IN111" ] && echo yes || echo no)、装配器首行=$(head -1 "$W111/emit_104.err" 2>/dev/null)）"
	echo "    这一腿的输入全部来自 emit 的产物；产物没有就直接判红，而不是拿着空文件去比计数——空文件会报成「函数没搬」，归属说假话（本批 04:1x 实录：删掉搬运注释时把 emit 调用一起删了，整腿只剩 cat: No such file 的裸错，集齐两轮才定位到）"
	exit 1
fi
PRE104="$W111/prelude_104.txt"
prelude111 "$IN111" 104 > "$PRE104"
N_PYTEST=$(grep -cF 'py_tests() {' "$PRE104" 2>/dev/null || true)
N_NEED=$(grep -cF 'gate_need() {' "$PRE104" 2>/dev/null || true)
N_GW=$(grep -cF 'gw_code_hits() {' "$PRE104" 2>/dev/null || true)
if [ "${N_PYTEST:-0}" != "1" ] || [ "${N_NEED:-0}" != "1" ] || [ "${N_GW:-0}" != "1" ]; then
	echo "--- FAIL: §111 装配搬运腿 ${CNT111}（§104 内层脚本的**前置搬运区**里 py_tests=${N_PYTEST} gate_need=${N_NEED} gw_code_hits=${N_GW}，应各 1）"
	echo "    少搬一个函数＝那段在收集模式里报 command not found，读数写着「§N 缺陷」，而真正坏的是装配器"
	exit 1
fi
python3 - "$GPY111" "$W111/gs_r1.py" <<'P2LR1'
import pathlib
import sys

src = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
old = "        if mask[i]:\n            continue\n        fm = FUNC_RE.match(line)"
new = "        if mask[i] or i < prologue_end:\n            continue\n        fm = FUNC_RE.match(line)"
if src.count(old) != 1:
    raise SystemExit("R1 反证：目标锚不唯一（%d 处）——这枚反证作废而不是勉强通过" % src.count(old))
pathlib.Path(sys.argv[2]).write_text(src.replace(old, new), encoding="utf-8")
P2LR1
EMITR1_RC=0
python3 "$W111/gs_r1.py" emit "$GS111" 104 "$W111/inner_r1.sh" "$PWD" 2>"$W111/emit_r1.err" || EMITR1_RC=$?
if [ "$EMITR1_RC" != "0" ] || [ ! -s "$W111/inner_r1.sh" ]; then
	echo "--- FAIL: §111 反证 R1 ${CNT111}（旧装配器连内层脚本都没产出（rc=${EMITR1_RC}、首行=$(head -1 "$W111/emit_r1.err" 2>/dev/null)））"
	echo "    R1 的正判据是「旧装配器搬 0 处」，而「文件根本不存在」也会读到 0——不先断产物在位，这枚反证会在装配器彻底坏掉时**假绿**"
	exit 1
fi
PRE_R1="$W111/prelude_r1.txt"
prelude111 "$W111/inner_r1.sh" 104 > "$PRE_R1"
BAD_PYTEST=$(grep -cF 'py_tests() {' "$PRE_R1" 2>/dev/null || true)
if [ "${BAD_PYTEST:-0}" != "0" ]; then
	echo "--- FAIL: §111 反证 R1 ${CNT111}（退回「前言函数排除」后内层脚本仍有 py_tests ${BAD_PYTEST} 处＝上面那枚搬运腿压根没在测装配器，只测了个字符串）"
	exit 1
fi
echo "ok - §111 R1（现实现搬 1 处 / 退回旧实现搬 0 处：这枚锁真的在管「前言函数搬不搬」）"

CNT111=$((CNT111 + 1))
if [ -z "$WORK2RED" ]; then
	echo "--- FAIL: §111 反证 R2 ${CNT111}（拿不到 two_red 的工作目录，R2/R3 只能空过——空过的反证等于没有反证）"
	exit 1
fi
python3 - "$GPY111" "$W111/gs_r2.py" <<'P2LR2'
import pathlib
import sys

src = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
# 退回本批第一轮实跑的那个形状：四桶同用逗号切，且分号循环只填 skip_reason、不填桶。
pairs = [
    ('        for key in ("PASS", "FAIL", "SILENT"):', '        for key in buckets:'),
    ('                    buckets["SKIP"].append(int(sid))\n', ''),
    ('            elif chunk.strip().isdigit():\n                buckets["SKIP"].append(int(chunk))\n', ''),
]
out = src
for old, new in pairs:
    if out.count(old) != 1:
        raise SystemExit("R2 反证：目标锚不唯一（%r 出现 %d 次）" % (old, out.count(old)))
    out = out.replace(old, new)
pathlib.Path(sys.argv[2]).write_text(out, encoding="utf-8")
P2LR2
BAD_SUM="$W111/summary_r2.txt"
python3 "$W111/gs_r2.py" summary "$FIXGATE111" "$WORK2RED" > "$BAD_SUM" 2>/dev/null || true
BAD_SKIP=$(sed -n 's/^SKIP_SECTIONS\t//p' "$BAD_SUM")
BAD_BAL=$(sed -n 's/^BALANCE\t//p' "$BAD_SUM")
if [ "${BAD_SKIP:-1}" != "0" ]; then
	echo "--- FAIL: §111 反证 R2 ${CNT111}（旧写法下 SKIP_SECTIONS 仍然是 ${BAD_SKIP}＝这条桶压根没被「逗号切」影响，正向腿是蒙对的）"
	exit 1
fi
case "$BAD_BAL" in
MISSING:4*) ;;
*) echo "--- FAIL: §111 反证 R2 ${CNT111}（旧写法应把 §4 凭空丢掉并报 MISSING:4，实得 BALANCE=${BAD_BAL}）"; exit 1 ;;
esac
CNT111=$((CNT111 + 1))
GOOD_RUN="$W111/good_summary.txt"
python3 "$GPY111" summary "$FIXGATE111" "$WORK2RED" > "$GOOD_RUN" 2>/dev/null || true
GOOD_SKIP=$(sed -n 's/^SKIP_SECTIONS\t//p' "$GOOD_RUN")
GOOD_BAL=$(sed -n 's/^BALANCE\t//p' "$GOOD_RUN")
if [ "${GOOD_SKIP:-0}" != "1" ] || [ "$GOOD_BAL" != "OK" ]; then
	echo "--- FAIL: §111 汇总腿 ${CNT111}（真装配器在 two_red 上报 SKIP=$GOOD_SKIP BALANCE=${GOOD_BAL}，应为 1/OK）"
	exit 1
fi
echo "ok - §111 R2（旧写法 SKIP=0 且 BALANCE=${BAD_BAL}；现实现 SKIP=1 且闭合）"

CNT111=$((CNT111 + 1))
python3 - "$GPY111" "$W111/gs_r3.py" <<'P2LR3'
import pathlib
import sys

src = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
old = "        elif started[-1] in skipped:\n"
if src.count(old) != 1:
    raise SystemExit("R3 反证：目标锚不唯一（%d 处）" % src.count(old))
head, rest = src.split(old, 1)
tail = rest.split("        elif not done:", 1)
if len(tail) != 2:
    raise SystemExit("R3 反证：找不到紧随其后的 `elif not done:`，退回旧实现的形状不确定")
pathlib.Path(sys.argv[2]).write_text(head + "        elif not done:" + tail[1], encoding="utf-8")
P2LR3
LOG2="$WORK2RED/log_2.txt"
if [ ! -f "$LOG2" ]; then
	echo "--- FAIL: §111 反证 R3 ${CNT111}（two_red 第 2 遍日志不在位，R3 没有输入）"
	exit 1
fi
BAD_CLS=$(python3 "$W111/gs_r3.py" classify "$FIXGATE111" "$LOG2" 0 2>/dev/null | sed -n 's/^SILENT\t//p')
GOOD_CLS=$(python3 "$GPY111" classify "$FIXGATE111" "$LOG2" 0 2>/dev/null | sed -n 's/^SILENT\t//p')
if [ "${BAD_CLS:-}" != "4" ]; then
	echo "--- FAIL: §111 反证 R3 ${CNT111}（去掉 skip 归属分支后 §4 没被判成 SILENT，实得「${BAD_CLS}」＝那条分支本来就没起作用，正向腿是蒙的）"
	exit 1
fi
if [ "${GOOD_CLS:-x}" = "4" ]; then
	echo "--- FAIL: §111 分类腿 ${CNT111}（现实现仍把主动跳过的段记成 SILENT：跳过与静默两种成因混在一桶，修法会跟着错）"
	exit 1
fi
echo "ok - §111 R3（旧实现把跳过的段读成 SILENT；现实现读成 SKIP）"

# ── ⑤ §P2-M 的两条行为腿 + 一枚反证：分类器读日志的编码口径、gate_clip 的按字符截断 ──
#
# 这一组是本轮自己踩出来的：整轮 `-full -collect` 第一轮（04:39）跑完 109 段全绿、日志里一条
# `--- FAIL` 都没有，却因为 §110 回显里一个被劈开的汉字（position 162180 的 0xe6）把分类器打挂，
# 汇总于是报 `BALANCE=MISSING:1,2,…,111`。所以这两条腿断言的不是"段会不会红"，而是
# **"结论会不会因为一个字节的编码质量而整批消失"**——收集模式最怕的就是这个。
#
# L10 的输入是**合成**的而不是那份 /tmp 日志：现网复跑不能依赖上一轮留下的临时目录，
# 半个多字节字符直接按字节写出来（`\xe6` 孤尾＝真实现落下的形状），这样这条腿在任何机器、
# 任何 locale 下都拿得到同一个输入。
CNT111=$((CNT111 + 1))
BADLOG="$W111/p2m_bad.log"
python3 - "$BADLOG" <<'P2MB' || { echo "--- FAIL: §111 行为腿 L10 ${CNT111}（合成不了含半截多字节字符的日志＝L10 没有输入，空过的行为腿等于没有行为腿）"; exit 1; }
import sys
# 三条段标记 + 收尾标记，其中第二条正文结尾故意留一个孤立的 0xe6（三字节汉字的第一个字节）。
body = (
    b"GATE-SECTION-START 1\n"
    b"ok - fixture m1\n"
    b"GATE-SECTION-START 2\n"
    b"ok - \xe4\xb8\xad\xe6\x96\x87\xe8\xaf\xbb\xe6\x95\xb0\xe5\x8d\n"
    b"GATE-SECTION-START 3\n"
    b"ok - fixture m3\n"
    b"\n"
    b"==> \xe5\x85\xa8\xe9\x83\xa8\xe9\x80\x9a\xe8\xbf\x87\n"
)
open(sys.argv[1], "wb").write(body)
try:
    body.decode("utf-8")
    raise SystemExit("输入自己不非法＝这枚反证从一开始就没有被测对象")
except UnicodeDecodeError:
    pass
P2MB
rc111=0
L10OUT="$(python3 "$GPY111" classify "$GS111" "$BADLOG" 0 2>&1)" || rc111=$?
if [ "$rc111" != "0" ]; then
	echo "--- FAIL: §111 行为腿 L10 ${CNT111}（分类器被一个坏字节打挂＝收集模式把自己的结论弄丢了，04:39 那轮就是这个读数）：$(printf '%s' "$L10OUT" | tail -2 | tr '\n' ' ')"
	exit 1
fi
L10STARTED=$(printf '%s\n' "$L10OUT" | sed -n 's/^STARTED\t//p')
L10PASS=$(printf '%s\n' "$L10OUT" | sed -n 's/^PASS\t//p')
L10EMPTY=$(printf '%s\n' "$L10OUT" | sed -n 's/^EMPTY\t//p')
L10DONE=$(printf '%s\n' "$L10OUT" | sed -n 's/^DONE\t//p')
if [ "$L10STARTED" != "1,2,3" ] || [ "$L10PASS" != "1,2,3" ]; then
	echo "--- FAIL: §111 行为腿 L10 ${CNT111}（坏字节改变了段归属：STARTED=「${L10STARTED}」PASS=「${L10PASS}」，应为 1,2,3／1,2,3——容错解码只许换掉那一个字符，不许动判据依赖的 ASCII 结构）"
	exit 1
fi
[ "$L10EMPTY" = "0" ] || { echo "--- FAIL: §111 行为腿 L10 ${CNT111}（EMPTY=${L10EMPTY}：段标记明明在位却被读成「一个段都没有」，汇总会走 MISSING 面把整轮抹掉）"; exit 1; }
[ "$L10DONE" = "1" ] || { echo "--- FAIL: §111 行为腿 L10 ${CNT111}（收尾标记读不到（DONE=${L10DONE}）：半截中文所在的行之后就没有结论了，本遍会被判 SILENT）"; exit 1; }
echo "ok - §111 L10（含孤立 0xe6 的日志照样分出三桶 STARTED/PASS=1,2,3、EMPTY=0、DONE=1：一个坏字节不再能抹掉整轮结论）"

# R4：把容错那行退回严格——同一个输入必须复现"整遍报废"，否则上面 L10 是蒙绿的。
CNT111=$((CNT111 + 1))
python3 - "$GPY111" "$W111/gs_r4.py" <<'P2MR4'
import pathlib
import sys

src = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
old = 'log = read_lines(log_path, errors="replace")'
new = "log = read_lines(log_path)"
if src.count(old) != 1:
    raise SystemExit("R4 反证：目标锚不唯一（%d 处）——这枚反证作废而不是勉强通过" % src.count(old))
pathlib.Path(sys.argv[2]).write_text(src.replace(old, new), encoding="utf-8")
P2MR4
rc111=0
R4OUT="$(python3 "$W111/gs_r4.py" classify "$GS111" "$BADLOG" 0 2>&1)" || rc111=$?
if [ "$rc111" = "0" ]; then
	echo "--- FAIL: §111 反证 R4 ${CNT111}（退回严格解码后分类器仍然 0 退出＝L10 那行容错根本不是必需的，这组锁只剩形状）"
	exit 1
fi
printf '%s' "$R4OUT" | grep -qF 'UnicodeDecodeError' || { echo "--- FAIL: §111 反证 R4 ${CNT111}（非零退出却没死在解码上，说明坏法是别的东西，L10 与 R4 不是同一条因果链：$(printf '%s' "$R4OUT" | tail -2 | tr '\n' ' ')）"; exit 1; }
if printf '%s' "$R4OUT" | grep -qF 'STARTED'; then
	echo "--- FAIL: §111 反证 R4 ${CNT111}（严格解码下仍然给出了 STARTED＝容错与严格在分类器里根本没有区别）"
	exit 1
fi
echo "ok - §111 R4（同一份输入退回严格解码：UnicodeDecodeError、零键值输出＝04:39 那轮「109 段全 MISSING」的真实成因被复现）"

# L11：gate_clip 必须在 LC_ALL=C 下仍按**字符**截断。
# 抽的是仓库里那一份定义而不是在测试里重写一遍：重写就变成"测我自己抄的那份"，
# 与 §110 真正回显时用的实现脱钩（同族：判据按运行时真实取值链，不按注释里的描述）。
CNT111=$((CNT111 + 1))
GC_DEF_LN=$(ln111 "$GS111" 'gate_clip() {')
if [ "$GC_DEF_LN" -le 0 ]; then
	echo "--- FAIL: §111 行为腿 L11 ${CNT111}（门禁里找不到 gate_clip 的定义行＝②c 那枚单实现锁与这条腿量的不是同一个东西）"
	exit 1
fi
awk -v n="$GC_DEF_LN" 'NR>=n{print; if ($0=="}") exit}' "$GS111" > "$W111/gc.sh"
grep -qF 'gate_clip() {' "$W111/gc.sh" || { echo "--- FAIL: §111 行为腿 L11 ${CNT111}（抽函数体抽出来是空的：拿空文件 source 之后再跑这条腿，恒绿）"; exit 1; }
[ "$(tail -1 "$W111/gc.sh")" = "}" ] || { echo "--- FAIL: §111 行为腿 L11 ${CNT111}（抽到的不是完整函数（末行不是顶格 `}`）：截半的函数体 source 会炸，读数归属就不在这条腿上了）"; exit 1; }
# 就是 04:39 那轮被劈坏的那条读数原文（§110 leg110 的 g 腿）。
GOT110='FAIL|ops:scheduled-task roster in place + periodic tasks fresh|no-task-readings（PS 侧一段都没回传，判据失明而不是"全部健康"）'
LC_ALL=C bash -c 'source "$0"; gate_clip 92 "$1"' "$W111/gc.sh" "$GOT110" > "$W111/clip_new.out"
LC_ALL=C bash -c 'printf %s "${1:0:92}"' x "$GOT110" > "$W111/clip_old.out"
rc111=0
CLIP_READ="$(python3 - "$W111/clip_new.out" "$W111/clip_old.out" <<'P2MC'
import sys

new = open(sys.argv[1], "rb").read()
old = open(sys.argv[2], "rb").read()
try:
    t = new.decode("utf-8")
except UnicodeDecodeError as exc:
    raise SystemExit("BADNEW 现写法自己就落下非法字节（%s）" % exc)
if len(t) != 92:
    raise SystemExit("BADLEN 现写法截出 %d 个字符，应 92（上限写坏了＝同一把尺子量不出同一件事）" % len(t))
if len(new) <= 92:
    raise SystemExit("BADBYTES 现写法输出只有 %d 字节＝它还是在按字节切，中文全被当成一字节" % len(new))
try:
    old.decode("utf-8")
except UnicodeDecodeError:
    sys.exit(0)
raise SystemExit("BADOLD 旧写法（LC_ALL=C 下的 ${x:0:92}）这次没劈坏字符＝反证失去被测对象，"
                 "这条腿的绿不能算数（换输入或换 locale 都会造成这种假象，所以 L11 强制 LC_ALL=C 跑）")
P2MC
)" || rc111=$?
if [ "$rc111" != "0" ]; then
	echo "--- FAIL: §111 行为腿 L11 ${CNT111}（截断口径不符：${CLIP_READ}）"
	exit 1
fi
echo "ok - §111 L11（LC_ALL=C 下 gate_clip 输出 $(wc -c < "$W111/clip_new.out" | tr -d ' ') 字节／恰好 92 字符且严格可解码；同一输入旧写法落下非法字节——04:39 那个字节正是从这一行进到分类器的）"

# ── P2-K 的 I1/I2：断网形态与"改回直调必红" ──
CNT111=$((CNT111 + 1))
rc111=0
I111="$(HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 NO_PROXY= go test -count=1 -v ./internal/server/ -run 'TestLLMURLResolverSeamIsHermetic|TestValidatePublicURL|TestSetLLMConfigRejectsReservedAddressThroughHTTP|TestSetLLMConfigResolvesUnresolvableHostThroughHTTP' 2>&1)" || rc111=$?
if [ "$rc111" != "0" ]; then
	echo "--- FAIL: §111 行为腿 I1 ${CNT111}（黑洞代理下解析腿判红＝那条腿还在真出呼，红来自环境而不是被测代码；10-05 实录「断网判红、单跑 PASS 17.99s」就是这一形态）：$(printf '%s' "$I111" | tail -3 | tr '\n' ' ')"
	exit 1
fi
printf '%s' "$I111" | grep -qE '^ok' || { echo "--- FAIL: §111 行为腿 I1 ${CNT111}（0 退出却没有 ok 行——十有八九是被测用例一条都没匹配上，判据空转）"; exit 1; }
# 数**顶格** `--- PASS`（顶层用例）而不是任意 PASS 行：go test 不带 -v 时，`-run` 的正则哪怕一条用例
# 都没匹配上，也照样打 `ok  ...  [no tests to run]` 并以 0 退出——只看 `^ok` 的判据会把它读成"全绿"，
# 于是"注入点被删、用例集体改名"这种坏法在这条腿上表现为一枚漂亮的绿（0929 那批"判据空转"同族）。
I1PASS=$(printf '%s' "$I111" | grep -cE '^--- PASS' || true)
min111 '黑洞代理下真跑到的顶层用例数（应≥6：注入点在位性/预留地址归属/解析成功与失败四例/两条 HTTP 腿）' "$I1PASS" "6"
echo "ok - §111 I1（黑洞代理下解析腿全绿：顶层用例 ${I1PASS} 条真跑到）"

CNT111=$((CNT111 + 1))
mkdir -p "$W111/i2"
sed -e 's/ips, err := llmURLResolver(host)/ips, err := net.LookupIP(host)/' "$SRV111" > "$W111/i2/server.go"
BAD_CALL=$(grep -cF 'llmURLResolver(host)' "$W111/i2/server.go" 2>/dev/null || true)
GOOD_CALL=$(grep -cF 'llmURLResolver(host)' "$SRV111" 2>/dev/null || true)
if [ "${BAD_CALL:-1}" != "0" ] || [ "${GOOD_CALL:-0}" != "1" ]; then
	echo "--- FAIL: §111 反证 I2 ${CNT111}（镜像里 llmURLResolver(host)=${BAD_CALL}（应 0）、真文件=${GOOD_CALL}（应 1）：这一对读数是「改回直调必红」的唯一证据，任一不符都说明锁只剩形状）"
	exit 1
fi
BAD_LOOK=$(code_hits111 "$W111/i2/server.go" 'net\.LookupIP')
if [ "${BAD_LOOK:-0}" != "2" ]; then
	echo "--- FAIL: §111 反证 I2 ${CNT111}（退回直调后非注释行 net.LookupIP 应为 2 处＝注入点默认值 + 被改坏的调用点，实得 ${BAD_LOOK}：等值锁与这枚反证用的不是同一把尺子）"
	exit 1
fi
echo "ok - §111 I2（真文件 1 处注入调用 / 镜像退回直调 0 处注入调用且 DNS 直调 2 处：上面那枚等值锁的坏法可复现）"

# ── ⑥ §P1-C 的 G2：gofmt 锁的正向对照（证明 §104 那条「输出为空」是判据，不是命令坏了）──
#
# 为什么这条腿值得立：§104 的写法是 `GOFMT_DIRTY=$(gofmt -l internal cmd 2>/dev/null || true)`。
# 命令不在 PATH、go 环境坏了、或者哪天把目录名写成一个不存在的目录，它都会**安静地给出空串**，
# 于是「必须为空」那枚等值锁退化成恒绿装饰——而 10-05 那条「HEAD 自带未格式化文件入库」的缺陷
# 恰恰是靠它拦的。单向的「为空」必须配一个「写歪了一定抓得到」的对照（等值锁非单向锁）。
CNT111=$((CNT111 + 1))
command -v gofmt >/dev/null 2>&1 || { echo "--- FAIL: §111 行为腿 G2 ${CNT111}（PATH 里没有 gofmt：§104 在这台机器上永远绿，而它的绿什么都不证明）"; exit 1; }
mkdir -p "$W111/g2"
printf 'package p\n\nfunc F() int {\n        return 1\n}\n' > "$W111/g2/g2.go"
gofmt -l "$W111/g2" > "$W111/g2_dirty.txt" 2>/dev/null || true
grep -qF 'g2.go' "$W111/g2_dirty.txt" || {
	echo "--- FAIL: §111 行为腿 G2 ${CNT111}（镜像里故意用空格缩进的 Go 文件没被 gofmt -l 点名：§104 那枚「输出必须为空」失去对照，它的绿不再等于「格式干净」）"
	echo "    实得：$(tr '\n' ' ' < "$W111/g2_dirty.txt")"
	exit 1
}
# 复位后再测一次同一目录必须变空：只看「抓到过一次」不够——残留或文件名巧合都能造出那一次抓到。
gofmt -w "$W111/g2/g2.go" 2>/dev/null || true
G2_CLEAN=$(gofmt -l "$W111/g2" 2>/dev/null || true)
[ -z "$G2_CLEAN" ] || { echo "--- FAIL: §111 行为腿 G2 ${CNT111}（gofmt -w 之后镜像仍有未格式化文件「${G2_CLEAN}」＝这枚对照不可复现，正向那次的归属就不可信）"; exit 1; }
echo "ok - §111 G2（写歪必被抓、复位后必空：§104 的空输出是判据而不是命令缺失的副产品）"

# ── G3：批验证口令的自证清单（判据从 git log 派生，不写死提交号）──
CNT111=$((CNT111 + 1))
MSG111=$(git log -1 --format=%B 2>/dev/null || true)
PREV111=$(git log -1 --skip=1 --format=%B 2>/dev/null || true)
HAS_HEAD=0
if printf '%s' "$MSG111" | grep -qE '(GATE|VERIFY)_EXIT=[0-9]+'; then HAS_HEAD=1; fi
HAS_PREV=0
if printf '%s' "$PREV111" | grep -qE '(GATE|VERIFY)_EXIT=[0-9]+'; then HAS_PREV=1; fi
if [ "$HAS_HEAD" = "0" ] && [ "$HAS_PREV" = "0" ]; then
	echo "--- FAIL: §111 自证清单锁 ${CNT111}（连续两批提交说明都没有整轮门禁读数 GATE_EXIT=/VERIFY_EXIT= ⇒「没跑全量门禁」和「跑了但没写」在事后看不出区别，069c380 那次就是这么把 §105–107 埋掉的）"
	exit 1
elif [ "$HAS_HEAD" = "0" ]; then
	echo "观察读数（不判红，G3 首犯只提醒）：HEAD 提交说明缺整轮门禁读数（GATE_EXIT=/VERIFY_EXIT=），上一批有 ⇒ 连续两批缺即红"
fi
echo "ok - §111 G3（HEAD 有读数=$HAS_HEAD / 上一批有读数=${HAS_PREV}）"

# ── 派生面正锁：装配器读得出段清单与跨段依赖（它自己的两个分母）──
CNT111=$((CNT111 + 1))
SEC_N=$(python3 "$GPY111" sections "$GS111" 2>/dev/null | awk -F'\t' '$1=="TOTAL"{print $2}')
HELP_N=$(python3 "$GPY111" helpers "$GS111" 2>/dev/null | sed -n 's/^HELPDERIVED\t//p')
CHK_OUT=$(python3 "$GPY111" check "$GS111" 2>&1 || true)
min111 '门禁派生段数应 ≥108（收集模式的分母；解析器与脚本格式脱节时会读出很小的数甚至 0，那比红更危险——它把「没跑到」报成「跑完了」）' "$SEC_N" "108"
min111 '跨段 helper 依赖应派生出 ≥3 条（gw_* 三件＋§110 那枚按字符截断的 helper——它在 111 段里只以"被锁判的字符串"形态出现，装配器按词命中就把它搬进前置区，多搬一枚纯函数无害，少搬才是事）：派生为 0＝搬运腿空转，内层脚本会安静地少搬函数' "$HELP_N" "3"
printf '%s' "$CHK_OUT" | grep -qE 'CHECK[[:space:]]+OK' || { echo "--- FAIL: §111 自检腿 ${CNT111}（gate_sections.py check 没报 OK：$(printf '%s' "$CHK_OUT" | tail -2 | tr '\n' ' ')）——前言里出现了非函数副作用语句，内层脚本会少搬那一句）"; exit 1; }
echo "ok - §111 派生面（段数=$SEC_N 跨段 helper=$HELP_N check=OK）"

rm -rf "$W111"
echo "ok - §111 全段通过：收集模式静态锁 + 十条镜像行为腿（L1 内含 H1/H2/H3 三枚等值锁，另含 SKIP/SILENT 归属；L2 默认模式语义不变；L4 全绿闭合；L5 SKIP-without-FAIL；L6 收尾歧义；L7 工作树漂移；L8 EMPTY；L9 搬运顺序；L10 日志编码容错；L11 按字符截断）+ 五枚退回旧实现的反证（R1/R2/R3/R4/I2）+ P2-K 注入点等值锁与黑洞代理腿 I1 + G2 gofmt 对照 + G3 自证清单 + 派生面正锁，累计判定点 ${CNT111}"
echo ""
echo "==> 112 §P2-E/§P2-F/§P2-G 对账三态 + 降级来源标记 + 文件桥写盘顺序（2026-10-06 修复批 波 5）：跨语言线格式等值、四处同源、窗口顺序锁、派生测试资产与十枚镜像反证（枚数由源码派生）..."

# 本段守波 5 三条缺陷的**全部落点**（10-05 全量评价 P2-E/F/G → 10-06 按 docs/FIX_PLAN_20261006.md 落码）。
#
#  ① §P2-F 降级链把断源日写成平盘。这条链四层，缺任一层「修好了」就是假话：
#       sidecar 线格式（cmd/pydata/server.py：缺测列输出 _NA、行尾带 source 列）
#         → Go 取数三态（internal/data：MissingCell/FOk/SOk，空串与 NA 都不再被静默折成 0）
#         → 落库形状（cmd/dataload/baostock.go：numOrNil 写 NULL、daily.source 盖章、
#            tradestatus 只在**明确读到 0** 时当停牌）
#         → 读侧判据（internal/store/daily_source.go 单源片段，统计点共用）。
#     所以本段一半的锁是**等值/同源**锁而不是「字符串在不在」锁：跨语言两端（Python 的
#     _STOCK_FIELDS+_SOURCE_COL 对 Go 夹具的 klineHeader；Python 的 _NA 对 Go 的 MissingCell）
#     各写一份时改一侧必红——这正是 09-25 那批「同一个判据在两个读数点各写一遍公式」的教训
#     （§0927AUDIT-D1）。两处取向按推荐项落码并在此留痕（owner 未逐条裁决，事后可否决）：
#       · `source IS NULL` 判为**世代未知**而非降级：宁可继续参与统计，也不把本列落地前的
#         历史数据凭空删掉（行为腿 B4/S2 锁住这条取向）；
#       · ST 态未知的降级行**照写** ±10% 停板价：缺护栏比偏松更糟（S3 的反向读数锁住）。
#     `DailySourceDegradedOnly` 目前只有测试消费者（生产三个统计点全用 NotDegraded）。留着不是
#     死码：写侧标记口径必须由读侧单点定义，删掉它，下一个「数降级行数」的统计点就会自己
#     写一遍 LIKE（那是本枚要防的原形）。
#  ② §P2-E 交割日静默跳过记「已对账」。旧形态是「网关未连接／执行器不支持」两条跳过腿都返回
#     nil,error=nil 并推进 lastSettleDay，再写 settlement_diff_count=0 冒充「对完无差异」
#     ⇒ 三方对账这条防线可以在网关断线的一整天里全程没跑，而看板与告警全部显示正常。
#     现改为三态 SettleOutcome + 唯一映射点 gaugeValue()（0 未跑/1 已验证/2 未连接/3 不支持）
#     + 规则 settlement_not_verified（ge 2 → p2）+ RouteDaily，§DEADGAUGE 三件套同形：接线锁
#     （defer 那一句恰好一处）、键名 const 别名 ban 负锁（别名＝通用守卫失明，09-29 14:0x 判红实录）、
#     赋值点等值。owner 裁决点「未连接算失败还是不适用」按**推荐项**落码＝算失败向（不推进
#     lastSettleDay、走 §D4 十分钟节流重试）；要改口径只需动 gaugeValue 与两条行为腿，判据面
#     （ge 2）不用改。
#  ③ §P2-G 文件桥先翻状态后写文件。旧顺序下「写 bridge_cmd.json 失败」是**确定性丢单**：行已经
#     是 inflight、桥永远看不到这张单、要等 §M16 收割线（默认 1800s）判废且不回 pending，
#     现网只留一行 log.exception。现翻转成 peek（只读）→ 写盘成功 → mark_inflight，写失败保持
#     pending 并把三个计数接进 /admin/status。这里最有价值的一枚是**窗口顺序锁**：三句代码的
#     相对顺序就是这条缺陷的本体，而「三句都在文件里」的字符串锁对顺序颠倒完全无感（回到旧
#     顺序照样三句齐全＝恒绿假锁）。
#
#  ④ 反证为什么一枚只破一个点、且每枚先要求「变异落地数恰好 1」：
#     D1/D2 两枚都撞在同一个 pytest 用例（K4）上，所以把那两条断言的文案分别打上 K4a/K4b；
#     D4/D5/D6 三枚都撞在同一个 Go 用例（TestDegradedRowLandsWithSourceAndNulls）上，所以
#     daily 根数那一句从 `!= 3` 拆成 `<3 ⇒ K1a` 与 `>3 ⇒ K1c` 两条单一成因断言、is_st 那条
#     单独成句。合并写法的后果是一串反证只证出「有东西红了」，看不出「这枚破坏被哪把尺子拦住」
#     ——段内首红即退 ⇒ 每枚破坏必须本枚独有。
#     两条本批真踩过的 harness 坑写在这里防复发：
#       · 镜像反证首轮把 Go raw-string 的反引号一起吃掉 ⇒ 包编译失败、`grep '^--- FAIL:'` 自然
#         零命中，于是报「这枚破坏没有让任何东西变红」。那是 harness 坏了，不是锁没牙 ⇒
#         下面每条反证先断言**变异后能编译**，[build failed]/cannot find package 一律按 §112 红；
#       · `"1"` 这个字面量在 server.py 里**合法存在**（交易日历腿的 is_open），所以「降级腿不许
#         硬编码 1」只能按函数窗口扫（K4 的实现形状）。全文件级同款负锁会在健康代码上恒红，
#         本段刻意不写那种形状；baostock.go 的 `"pct_chg": r.F(` 同理（指数腿是合法的），
#         所以负锁走的是 bsLoadStockTables 的**窗口**而不是整份文件。
CNT112=0
REPO112="$PWD"
SIDE112=cmd/pydata/server.py
PYT112=cmd/pydata/tests/test_degraded_source.py
BSD112=cmd/dataload/baostock.go
GLD112=cmd/dataload/degraded_source_load_test.go
DSC112=internal/store/daily_source.go
STO112=internal/store/store.go
TUS112=internal/data/tushare.go
SET112=internal/trading/settlement.go
ALR112=internal/metrics/alerter.go
ARO112=internal/metrics/alert_routing.go
RBK112=cmd/research/risk_backfill.go
GWI112=qmt_gateway/gateway.py
GWS112=qmt_gateway/store.py
GWT112=qmt_gateway/tests/test_file_bridge_push_order.py
W112="$(mktemp -d /tmp/p2wave5-XXXXXX 2>/dev/null || true)"
[ -n "$W112" ] || { echo "--- FAIL: §112 建不出镜像目录，跨语言等值腿与全部镜像反证无法跑（宁可红，不许跳）"; exit 1; }

eq112() { # $1=文件 $2=整串 $3=预演读数 $4=说明
	CNT112=$((CNT112 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §112 整串等值锁 ${CNT112}（${4}）：${1} 整串「${2}」got=${got:-0} 预演=$3"; exit 1; }
}
neg112() { # $1=文件 $2=整串 $3=说明 → 彻底没有
	CNT112=$((CNT112 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "0" ] || { echo "--- FAIL: §112 负锁 ${CNT112}（${3}）：${1} 又出现「${2}」got=${got}"; exit 1; }
}
min112() { # $1=说明 $2=实得 $3=应≥ —— 派生面过窄即红（空转正锁家族）
	CNT112=$((CNT112 + 1))
	[ "${2:-0}" -ge "$3" ] || { echo "--- FAIL: §112 派生正锁 ${CNT112}（${1}）：实得=${2:-0} 应≥${3}"; exit 1; }
}
code_hits112() { # $1=文件 $2=整串 → 非注释行命中数（Go 的 //、Python 的 #、docstring 续行的 * 都算注释）
	local n
	n=$({ grep -nF -- "$2" "$1" 2>/dev/null || true; } | { grep -Ev '^[0-9]+:[[:space:]]*(//|#|\*|"""|--)' || true; } | wc -l | tr -d ' ')
	printf '%s' "${n:-0}"
}
win112() { # $1=文件 $2=起点整串 $3=终点正则 → 打印窗口正文（含起点行，不含终点行）
	awk -v s="$2" -v e="$3" '
		!f && index($0, s) { f = 1; print; next }
		f && $0 ~ e { exit }
		f { print }
	' "$1" 2>/dev/null || true
}
winc112() { # Go 窗口内**代码行**命中行数：$1=文件 $2=起点 $3=终点 $4=整串（needle 必传，漏传会数到整窗＝恒红）
	local n
	n=$(win112 "$1" "$2" "$3" | { grep -Ev '^[[:space:]]*(//|\*|/\*)' || true; } | grep -cF -- "$4" 2>/dev/null || true)
	printf '%s' "${n:-0}"
}
winn112() { # Go 窗口内首个命中的行序（0＝没有），同样剔整行注释：顺序锁用
	local n
	n=$(win112 "$1" "$2" "$3" | { grep -Ev '^[[:space:]]*(//|\*|/\*)' || true; } | grep -nF -- "$4" 2>/dev/null | head -1 | cut -d: -f1 || true)
	printf '%s' "${n:-0}"
}
# pyseg112/pyc112/pyn112：Python 侧的窗口一律走 **ast 定位的函数段**并整块剥 docstring。
# 本批真踩过的形态：dispatch_pending_peek 的 docstring 里写着「与 dispatch_pending 的唯一区别」
# 「写文件失败等于丢单」这类说明——按文本行切窗口时，负锁会把说明文字当成代码命中（恒红），
# 顺序锁也可能先撞上 docstring 里那一句（取到错误行序＝判据错位）。ast 取段才是运行时真形。
pyseg112() { # $1=py 文件 $2=函数/方法名 → 打印该函数源码段；同名函数不恰好 1 个 ⇒ 退出码 3
	python3 - "$1" "$2" <<'PYSEG112'
import ast, sys
path, name = sys.argv[1], sys.argv[2]
src = open(path, encoding="utf-8").read()
lines = src.splitlines()
hits = [n for n in ast.walk(ast.parse(src))
        if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and n.name == name]
if len(hits) != 1:
    sys.stderr.write("SEGMENTS=%d\n" % len(hits))
    sys.exit(3)
node = hits[0]
end = node.end_lineno or len(lines)
seg = lines[node.lineno - 1:end]
b = node.body
if b and isinstance(b[0], ast.Expr) and isinstance(b[0].value, ast.Constant) and isinstance(b[0].value.value, str):
    seg = lines[node.lineno - 1:b[0].lineno - 1] + lines[b[0].end_lineno:end]
sys.stdout.write("\n".join(seg) + "\n")
PYSEG112
}
pyc112() { # 函数段内命中行数：$1=文件 $2=函数名 $3=整串；段取不到 ⇒ 打印 SEG_ERR（让等值锁与负锁一起红，不静默放行）
	local seg n
	seg=$(pyseg112 "$1" "$2" 2>/dev/null) || { printf 'SEG_ERR'; return; }
	n=$(printf '%s\n' "$seg" | grep -cF -- "$3" 2>/dev/null || true)
	printf '%s' "${n:-0}"
}
pyn112() { # 函数段内首个命中序号（0＝没有，-1＝函数段派生失败）：窗口顺序锁用
	local seg n
	seg=$(pyseg112 "$1" "$2" 2>/dev/null) || { printf -- '-1'; return; }
	n=$(printf '%s\n' "$seg" | grep -nF -- "$3" 2>/dev/null | head -1 | cut -d: -f1 || true)
	printf '%s' "${n:-0}"
}
PREV112=0
# 十组的读数先归零（set -u 下未赋值直接参与算术会中止脚本，报错位置离成因很远；
# 而「某一组一个判定点都没有」这件事由收尾那枚分组在位锁正面拦住，不靠 unbound 侥幸）。
G1_N=0; G2_N=0; G3_N=0; G4_N=0; G5_N=0; G6_N=0; G7_N=0; G8_N=0; G9_N=0; G10_N=0
SNAP112() { # $1=组号 → 记下本组新增判定点数（收尾 ok 行里的「① 组 N 道」由这里派生，不是手写清单）
	# 手写分组数字的下场和「INFO 恰 N 行」那族一样：加一道锁就过期，而过期的读数就是把覆盖面报错的源头。
	printf -v "G$1_N" '%s' "$((CNT112 - PREV112))"
	PREV112=$CNT112
}
kv112() { # $1=键 $2=多行 KEY=VALUE → 取值
	printf '%s\n' "$2" | sed -n "s/^$1=//p" | head -1
}
scan_go112() { # $1=整串 → cmd/internal 里非测试、非注释、排除工具缓存的命中行
	# BSD grep 在 `--` 之后**不做选项置换**：放在路径后的 --include 被当成文件名（实测只报一行
	# "No such file or directory" 就静默不过滤），锁面于是扫到 .py/.pyc/说明注释——负锁恒红、
	# 派生面虚高。选项一律提到操作数之前，并额外按 .go: 兜一层。
	{ grep -rnF --include='*.go' -- "$1" cmd internal 2>/dev/null || true; } \
		| { grep '\.go:' || true; } \
		| { grep -v '/\.qoder/' || true; } \
		| { grep -v '_test\.go:' || true; } \
		| { grep -Ev ':[0-9]+:[[:space:]]*(//|\*)' || true; }
}

# ── ① sidecar 线格式常量族（写侧与读侧共用的字面量，各恰一处）──
eq112 "$SIDE112" '_NA = "NA"' 1 '缺测标记的定义恰好一处（两条降级腿与 Go 侧都认这一个值）'
eq112 "$SIDE112" '_SOURCE_COL = "source"' 1 '来源列列名定义恰好一处（与 Go 侧 daily.source 列名同一个字符串）'
eq112 "$SIDE112" '_SRC_BAOSTOCK = "baostock"' 1 '主链路标记'
eq112 "$SIDE112" '_SRC_SINA = "sina_degraded"' 1 '新浪降级腿标记（后缀 degraded 是读侧 LIKE 的前提）'
eq112 "$SIDE112" '_SRC_EASTMONEY = "eastmoney_degraded"' 1 '东财降级腿标记'
eq112 "$SIDE112" 'def _fields_with_source():' 1 '表头单实现（两处出口 r_kline/_stock_kline 共用；写第二份就是第二本账）'
eq112 "$SIDE112" '_STOCK_FIELDS.split(",") + [_SOURCE_COL]' 1 '表头=股票字段+source 的唯一构造式'
CNT112=$((CNT112 + 1))
LEG_N=$(grep -cE '^def _ak_[A-Za-z0-9_]*_daily\(' "$SIDE112" 2>/dev/null || true)
min112 '兜底日线腿由源码派生（形如 _ak_xxx_daily），新增第三条腿自动进锁；派生 <2 即红＝正则失效或全被改名' "$LEG_N" 2
# K4 射程与门禁派生射程的**集合等值**（不比字符串写法：有人把正则换成非捕获组、或在中间加个空格，
# 整串等值锁会假红；真正要防的是「两条腿各自扫出不同的函数集合」——那才是 K4 与门禁分家）。
LEG_NAMES112=$(grep -oE '^def _ak_[A-Za-z0-9_]*_daily\(' "$SIDE112" 2>/dev/null | sed -E 's/^def //; s/\($//' | LC_ALL=C sort -u | paste -sd, -) || true
K4_SET=$(python3 - "$SIDE112" "$PYT112" <<'PY112K4'
import ast, re, sys
side, tpath = sys.argv[1], sys.argv[2]
pat = None
for n in ast.parse(open(tpath, encoding="utf-8").read()).body:
    if isinstance(n, ast.Assign) and len(n.targets) == 1 and getattr(n.targets[0], "id", "") == "_DEGRADED_DAILY_DEF":
        v = n.value
        if isinstance(v, ast.Call) and v.args and isinstance(v.args[0], ast.Constant):
            pat = v.args[0].value
if not isinstance(pat, str) or not pat:
    print("K4=NO_PATTERN")
    sys.exit(0)
txt = open(side, encoding="utf-8").read()
try:
    ms = re.findall(pat, txt, re.M)
except re.error as exc:
    print("K4=BAD_REGEX %s" % exc)
    sys.exit(0)
names = set()
for m in ms:
    if not isinstance(m, str):
        m = m[0] if m else ""
    m = re.sub(r'^def ', '', m)
    names.add(m.rstrip("( "))
print("K4=%s" % ",".join(sorted(n for n in names if n)))
PY112K4
) || { echo "--- FAIL: §112 K4 射程派生腿执行失败（$PYT112 语法坏了？）"; exit 1; }
K4_NAMES=$(printf '%s' "$K4_SET" | sed -n 's/^K4=//p')
CNT112=$((CNT112 + 1))
case "$K4_SET" in
K4=NO_PATTERN|K4=BAD_REGEX*|K4=)
	echo "--- FAIL: §112 K4 射程派生锁 ${CNT112}（从 $PYT112 派生不到可用的 _DEGRADED_DAILY_DEF 正则：${K4_SET}）"
	echo "    派生不到时那条腿就退化成「扫 0 个函数也算通过」的空转锁——负锁最常见的失效形态是恒真，不是报错"
	exit 1
	;;
esac
CNT112=$((CNT112 + 1))
if [ "$K4_NAMES" != "$LEG_NAMES112" ]; then
	echo "--- FAIL: §112 K4 射程集合等值锁 ${CNT112}（门禁派生的兜底腿与 pytest K4 扫到的不是同一批）"
	printf '    门禁=%s\n' "$LEG_NAMES112"
	printf '    K4  =%s\n' "${K4_NAMES}"
	echo "    分家的后果：新增第三条兜底腿时门禁数到了、K4 没扫到 ⇒ 那条腿的 tradestatus 硬编码/来源标记写错都不会红"
	exit 1
fi
echo "ok - §112 K4 射程与门禁派生同集合：${LEG_NAMES112}"
eq112 "$PYT112" 'def _src_constant_values(src):' 1 'K4b 的来源常量表单实现（把「用了哪个标记」换算成「那个标记到底是不是降级名」）'

SNAP112 1
# ── ② 跨语言等值（Python 侧构造 vs Go 侧夹具/常量；一份改动必须两边一起红）──
XLANG=$(python3 - "$SIDE112" "$GLD112" "$TUS112" <<'PY112X'
import ast, re, sys
side, gotest, tus = sys.argv[1], sys.argv[2], sys.argv[3]
vals = {}
for n in ast.parse(open(side, encoding="utf-8").read()).body:
    if isinstance(n, ast.Assign) and len(n.targets) == 1 and isinstance(n.targets[0], ast.Name):
        try:
            vals[n.targets[0].id] = ast.literal_eval(n.value)
        except Exception:
            pass
hdr = ""
if isinstance(vals.get("_STOCK_FIELDS"), str) and isinstance(vals.get("_SOURCE_COL"), str):
    hdr = vals["_STOCK_FIELDS"] + "," + vals["_SOURCE_COL"]
go = open(gotest, encoding="utf-8").read()
gols = go.splitlines()
go_hdr = ""
for i, ln in enumerate(gols):
    if ln.startswith("const klineHeader ="):
        j = i
        # 逐行吃到「上一行行尾是 +」为止：Go 里这个 const 是两段字符串拼接，只取第一段会拿到半截表头
        while gols[j].split("//")[0].rstrip().endswith("+") and j + 1 < len(gols):
            j += 1
        text = "\n".join(x.split("//")[0] for x in gols[i:j + 1])
        go_hdr = "".join(re.findall(r'"([^"]*)"', text))
        break
mc = re.search(r'const MissingCell = "([^"]*)"', open(tus, encoding="utf-8").read())
print("SIDE_HDR=%s" % hdr)
print("GO_HDR=%s" % go_hdr)
print("SIDE_NA=%s" % (vals.get("_NA") if isinstance(vals.get("_NA"), str) else ""))
print("GO_MISSING=%s" % (mc.group(1) if mc else ""))
PY112X
) || { echo "--- FAIL: §112 跨语言读数提取失败（server.py/夹具语法坏了，等值锁无从进行）"; exit 1; }
SIDE_HDR=$(kv112 SIDE_HDR "$XLANG")
GO_HDR=$(kv112 GO_HDR "$XLANG")
SIDE_NA=$(kv112 SIDE_NA "$XLANG")
GO_MISSING=$(kv112 GO_MISSING "$XLANG")
# 先钉「两个读数都非空」：空串等于空串是恒真，那正是等值锁最阴的失效形态（提取腿坏了 ⇒ 锁装作在拦东西）。
CNT112=$((CNT112 + 1))
if [ -z "$SIDE_HDR" ] || [ -z "$GO_HDR" ]; then
	echo "--- FAIL: §112 提取正锁 ${CNT112}（表头读数有一侧为空：sidecar=${#SIDE_HDR} 字符、Go 夹具=${#GO_HDR} 字符）"
	echo "     ast/正则没抓到东西＝构造式或 const 写法变了。此时若直接比「相等」会两边都空而恒真，锁就退化成恒绿"
	exit 1
fi
CNT112=$((CNT112 + 1))
if [ "$SIDE_HDR" != "$GO_HDR" ]; then
	echo "--- FAIL: §112 跨语言表头等值锁 ${CNT112}（sidecar 的 _STOCK_FIELDS+_SOURCE_COL 与 Go 夹具 klineHeader 不再是同一份列序）"
	printf '    sidecar=%s\n' "$SIDE_HDR"
	printf '    go夹具  =%s\n' "$GO_HDR"
	echo "    列序或列名任一侧改动都会让另一侧的夹具静默错位：Go 按表头名取值，缺列就按主链路盖章（dailySourceOf 的缺省分支）"
	exit 1
fi
CNT112=$((CNT112 + 1))
HDR_COLS=$(printf '%s' "$SIDE_HDR" | tr ',' '\n' | grep -c . || true)
min112 '表头列数（含 source）应 ≥19：列数掉下来说明有人在某一侧删了列而另一侧的夹具跟着改了' "$HDR_COLS" 19
CNT112=$((CNT112 + 1))
if [ -z "$SIDE_NA" ] || [ -z "$GO_MISSING" ] || [ "$SIDE_NA" != "$GO_MISSING" ]; then
	echo "--- FAIL: §112 跨语言缺测标记等值锁 ${CNT112}（sidecar 输出 _NA=${SIDE_NA}，Go 认 MissingCell=${GO_MISSING}）"
	echo "    两个字符串不一致时 FOk() 永远认不到缺测标记 ⇒ 降级列又回到「静默折成 0」的旧形态，而且全线绿"
	exit 1
fi
echo "ok - §112 跨语言读数（表头 ${HDR_COLS} 列、缺测标记 ${SIDE_NA}）"

SNAP112 2
# ── ③ Go 取数三态 + 落库姿势 ──
eq112 "$TUS112" 'const MissingCell = "NA"' 1 'Go 侧缺测标记定义恰好一处'
CNT112=$((CNT112 + 1))
MC_HITS=$(code_hits112 "$TUS112" 'MissingCell)')
min112 'FOk/SOk/F 三个取值口都认这个标记（应 ≥3；只认一处＝另一条腿继续折 0）' "$MC_HITS" 3
eq112 "$BSD112" 'if ts, ok := r.FOk("tradestatus"); ok && int(ts) == 0 {' 1 '停牌判据＝「明确读到 0」，缺测不因它跳行'
neg112 "$BSD112" 'if int(r.F("tradestatus")) == 0 {' '旧写法（缺测也当停牌）不许复活；只在说明注释里提它，所以这条走整串 grep 的是**代码行**形状（带 if 前缀），注释里没有这一串'
CNT112=$((CNT112 + 1))
BSWIN_S='func bsLoadStockTables'
BSWIN_E='^func '
HARD_F=$(winc112 "$BSD112" "$BSWIN_S" "$BSWIN_E" '"pct_chg": r.F(')
if [ "${HARD_F:-0}" != "0" ]; then
	echo "--- FAIL: §112 窗口负锁 ${CNT112}（bsLoadStockTables 里又出现「pct_chg 走 F()」=${HARD_F}）"
	echo "    F() 把缺测折成 0，而 0 在涨跌语义里是「平盘」这一真实读数——这正是 10-05 审计 P2-F 的落点。"
	echo "    锁只扫这个函数窗口：同文件 bsLoadIndex 里的 r.F(\"pctchg\") 是**合法**的（指数腿没有降级链）"
	exit 1
fi
CNT112=$((CNT112 + 1))
HALT_OLD=$(winc112 "$BSD112" "$BSWIN_S" "$BSWIN_E" 'int(r.F("tradestatus"))')
if [ "${HALT_OLD:-0}" != "0" ]; then
	echo "--- FAIL: §112 窗口负锁 ${CNT112}（装载窗口里又出现 tradestatus 的 F() 读法=${HALT_OLD}＝缺测被当 0＝降级日整行不落库）"; exit 1
fi
eq112 "$BSD112" '"source": dailySourceOf(r),' 1 '来源列的唯一写点（每行必盖章，读侧才有筛除依据）'
eq112 "$BSD112" 'func numOrNil(v float64, ok bool) any {' 1 '「两值取值 → 可空落库」的唯一姿势实现'
CNT112=$((CNT112 + 1))
NIL_CALLS=$(code_hits112 "$BSD112" 'numOrNil(')
CNT112=$((CNT112 + 1))
if [ "${NIL_CALLS:-0}" -lt 8 ]; then
	echo "--- FAIL: §112 派生正锁 ${CNT112}（numOrNil 命中（含定义）应 ≥8＝定义 1 + 涨跌两列 2 + 估值四列 4 + is_st 1，实得 ${NIL_CALLS}）"
	echo "    少一处＝有一列回到了「缺测落成 0」的老路；多出来的是新列，按同口径要求走 numOrNil"
	exit 1
fi
echo "ok - §112 numOrNil 命中（定义+调用）= ${NIL_CALLS}"

SNAP112 3
# ── ④ 读侧判据单源 + schema 三处同源 ──
eq112 "$DSC112" 'const DailySourceLikeDegraded = "%degraded%"' 1 'LIKE 模式常量恰好一处（后缀匹配：第三条兜底腿沿用 *_degraded 即自动进锁）'
CNT112=$((CNT112 + 1))
RAW_LIKE=$(scan_go112 "LIKE '%degraded%'" | wc -l | tr -d ' ')
if [ "${RAW_LIKE:-0}" != "0" ]; then
	echo "--- FAIL: §112 判据单源负锁 ${CNT112}（cmd/internal 的非测试代码里出现手写的降级 LIKE=${RAW_LIKE} 处）"
	printf '%s\n' "$(scan_go112 "LIKE '%degraded%'" | head -5)"
	echo "    每多一个统计点自己写一遍 LIKE，就多一本账：改一侧漏一侧（§0927AUDIT-D1 同族）。判据只能来自 daily_source.go"
	exit 1
fi
CNT112=$((CNT112 + 1))
READER_N=$(scan_go112 'DailySourceNotDegraded(' | wc -l | tr -d ' ')
min112 '共用判据的生产调用点（板块聚合两处 + 广度回填一处）应 ≥3；读成 0＝有人把片段又内联回去了' "$READER_N" 3
echo "   观测（判据生产调用点）: $(scan_go112 'DailySourceNotDegraded(' | cut -d: -f1 | LC_ALL=C sort -u | tr '\n' ' ')"
eq112 "$STO112" 'ALTER TABLE daily ADD COLUMN source TEXT' 1 '已建库的增量迁移恰好一处（新库靠 CREATE 带上，旧库靠这条补列）'
eq112 "$STO112" '{"daily", "source", ' 1 '迁移表名与列名逐字为 daily/source（写成 daily_basic 就永远补不到这一列）'
eq112 "$STO112" 'source TEXT,' 1 '建表语句里的 source 列恰好一处'
eq112 "$STO112" '"amount", "source"}' 1 'TableColumns(daily) 尾列为 source 且只此一处'
CNT112=$((CNT112 + 1))
IDX_SRC=$(winc112 "$STO112" 'case "index_daily":' '^[[:space:]]*case "adj_factor":' 'source')
if [ "${IDX_SRC:-0}" != "0" ]; then
	echo "--- FAIL: §112 窗口负锁 ${CNT112}（index_daily 的列清单里出现 source=${IDX_SRC}）"
	echo "    指数链路没有 akshare 兜底腿，多一个不存在的列会被 validateInsertSurface 判红；与 daily 共用 case 是这次改动最容易被顺手做错的地方"; exit 1
fi
CNT112=$((CNT112 + 1))
LIE_SYMS=$( { grep -rnF --include='*.go' -e 'IsDegDailyRow' -e 'DegradedDailyRowSQL' cmd internal 2>/dev/null || true; } | { grep -v '/\.qoder/' || true; } | { grep '\.go:' || true; } | wc -l | tr -d ' ')
if [ "${LIE_SYMS:-0}" != "0" ]; then
	echo "--- FAIL: §112 注释诚实锁 ${CNT112}（Go 里又引用了不存在的判据符号 IsDegDailyRow/DegradedDailyRowSQL=${LIE_SYMS}）"
	echo "    本批真的踩过：注释指向早已改名掉的符号，下一个人照着注释去找判据，找到的是空气（判据单源＝daily_source.go）"
	exit 1
fi

SNAP112 4
# ── ⑤ §P2-E 三态：接线锁 + 别名 ban 负锁 + 赋值点等值（§DEADGAUGE 三件套同形）──
eq112 "$SET112" 'defer func() { metrics.SetGauge("settlement_state", outcome.gaugeValue()) }()' 1 '量规接线恰好一处（defer 单点写，函数内任何 return 分支都覆盖；定义没接＝量规恒 0、规则恒不触发）'
CNT112=$((CNT112 + 1))
GAUGE_WRITES=$(code_hits112 "$SET112" 'SetGauge("settlement_state"')
if [ "${GAUGE_WRITES:-0}" != "1" ]; then
	echo "--- FAIL: §112 赋值点等值锁 ${CNT112}（settlement.go 非注释行里写这一量规应恰好 1 处＝defer 那一句，实得 ${GAUGE_WRITES}）"
	echo "    第二处赋值＝三态之外又有人直接改读数，gaugeValue() 这个唯一映射点当场失去意义"; exit 1
fi
CNT112=$((CNT112 + 1))
ALIAS_N=$( { grep -rnE --include='*.go' 'settle(State)?Gauge' cmd internal 2>/dev/null || true; } | { grep -v '/\.qoder/' || true; } | { grep '\.go:' || true; } | { grep -v '_test\.go:' || true; } | { grep -Ev ':[0-9]+:[[:space:]]*(//|\*)' || true; } | wc -l | tr -d ' ')
if [ "${ALIAS_N:-0}" != "0" ]; then
	echo "--- FAIL: §112 键名别名 ban 负锁 ${CNT112}（非测试代码里出现量规键名 const 别名=${ALIAS_N}）"
	echo "    别名会让「SetGauge(\"<键名>\" 赋值点数」这类通用守卫失明（09-29 14:0x 判红实录）；测试文件里允许自定别名，生产不许"
	exit 1
fi
eq112 "$ALR112" '{Name: "settlement_not_verified", Metric: "settlement_state", Op: "ge", Threshold: 2' 1 '规则注册恰好一条，且阈值 2 与 gaugeValue 的两个「未验证」读数同源'
eq112 "$ARO112" '"settlement_not_verified": RouteDaily' 1 '路由条目恰好一条（缺路由条目会走默认路由被漏掉，长假全程破线会刷满）'
eq112 "$SET112" 'SettleOutcomeUnknown SettleOutcome = iota' 1 '零值占位＝「没有结论」，任何判据都必须显式覆盖它'
eq112 "$SET112" 'func (o SettleOutcome) Verified() bool { return o == SettleOutcomeVerified }' 1 'Verified 必须是「等已验证」而非「不等跳过」（写成不等就把 Unknown 当已验证）'
neg112 "$SET112" 'return o != SettleOutcome' '上一条的旧形状不许复活'
CNT112=$((CNT112 + 1))
GV_CASES=$(winc112 "$SET112" 'func (o SettleOutcome) gaugeValue() int64 {' '^[[:space:]]*}$' 'return ')
GV_TWO=$(winc112 "$SET112" 'func (o SettleOutcome) gaugeValue() int64 {' '^[[:space:]]*}$' 'return 2')
GV_THREE=$(winc112 "$SET112" 'func (o SettleOutcome) gaugeValue() int64 {' '^[[:space:]]*}$' 'return 3')
if [ "${GV_CASES:-0}" != "4" ] || [ "${GV_TWO:-0}" != "1" ] || [ "${GV_THREE:-0}" != "1" ]; then
	echo "--- FAIL: §112 映射表等值锁 ${CNT112}（gaugeValue 应恰有 4 个 return、return 2 与 return 3 各恰 1：实得 分支=${GV_CASES} 二=${GV_TWO} 三=${GV_THREE}）"
	echo "    少一个态＝有一态落进 default 的 0（与「本轮没跑」同形＝静默失效）；改 2/3 任一个都要同时改规则的 Threshold，这是同一本账"
	exit 1
fi

SNAP112 5
# ── ⑤b 三态与「差异读数归零」这本账（§DEADGAUGE 负锁③ 的同源要求，本批差点被当成分叉修掉）
#     §P2-E 落码时我把跳过分支里的 `SetGauge("settlement_diff_count", 0)` 一并删了，理由是
#     「不许用 0 冒充对完无差异」——方向错了：冒充成功的是 **lastSettleDay/「已对账」账面**，
#     不是差异读数。删掉归零反而把 §DEADGAUGE（09-23 傍晚批）负锁③ 守了两周的东西拆了：
#     跳过日"什么都不写"＝上一轮真比对出的差异条数留在量规上，规则 settlement_diff（gt 0、p1）
#     会在断线日拿昨天的差异天天刷屏。现按**两键合起来才可分辨**落：state 说"验没验"、
#     diff 说"此刻可数的差异"。整轮 -collect 首轮就是靠 §69 那条老负锁把这次拆锁抓出来的。
CNT112=$((CNT112 + 1))
DIFF_ZERO=$(code_hits112 "$SET112" 'SetGauge("settlement_diff_count", 0)')
if [ "${DIFF_ZERO:-0}" != "2" ]; then
	echo "--- FAIL: §112 跳过腿差异归零等值锁 ${CNT112}（settlement.go 的跳过分支应恰有两处把差异读数归零＝「不支持」与「未连接」各一处，实得 ${DIFF_ZERO}）"
	echo "    少一处＝那条跳过腿保留 §DEADGAUGE 的残值形态（断线日拿昨天的差异刷 p1）；多一处＝有人在真比对路径上又归零一次，把真差异抹平"
	exit 1
fi
CNT112=$((CNT112 + 1))
DIFF_REAL=$(code_hits112 "$SET112" 'SetGauge("settlement_diff_count", int64(')
if [ "${DIFF_REAL:-0}" != "1" ]; then
	echo "--- FAIL: §112 真比对差异写点等值锁 ${CNT112}（真比对那处差异读数写入应恰 1 处，实得 ${DIFF_REAL}）"
	echo "    与上一条合起来才是本波的口径：差异键只有一个真值写点 + 两条跳过腿归零，state 键只有一个 defer 写点"
	exit 1
fi

# ── ⑥ §P2-G：窗口顺序锁（缺陷本体就是顺序）+ 失败可见化接线 ──
PUSH_FN='_file_bridge_push_pending'
REC_FN='_record_file_bridge_push_failure'
PEEK_FN='dispatch_pending_peek'
L_PEEK=$(pyn112 "$GWI112" "$PUSH_FN" 'dispatch_pending_peek(limit=50)')
L_WRITE=$(pyn112 "$GWI112" "$PUSH_FN" 'os.replace(tmp, cmd_path)')
L_MARK=$(pyn112 "$GWI112" "$PUSH_FN" 'dispatch_mark_inflight([')
CNT112=$((CNT112 + 1))
# 先验三个行号都是**正整数**：pyseg 取不到函数段时 pyn112 返回 -1、pyc 类返回 SEG_ERR，
# `[ SEG_ERR -le 0 ]` 在 set -e 下只是让这一条 || 短路为假（整个条件于是可能全假＝顺序锁装绿）。
case "${L_PEEK}${L_WRITE}${L_MARK}" in
*[^0-9]*) NUM_OK112=0 ;;
*) NUM_OK112=1 ;;
esac
if [ "$NUM_OK112" != "1" ] || [ "$L_PEEK" -le 0 ] || [ "$L_WRITE" -le 0 ] || [ "$L_MARK" -le 0 ] || [ "$L_PEEK" -ge "$L_WRITE" ] || [ "$L_WRITE" -ge "$L_MARK" ]; then
	echo "--- FAIL: §112 顺序锁 ${CNT112}（_file_bridge_push_pending 函数段内必须 peek < 写盘 < mark_inflight：实得 peek=${L_PEEK} 写盘=${L_WRITE} mark=${L_MARK}、三个行号须全为正整数（NUM=${NUM_OK112}）；-1/SEG_ERR＝函数段派生失败，等同判据没落地）"
	echo "    倒序＝回到「先翻 inflight 再写文件」：写盘失败时行已经是 inflight、桥看不到，等收割线判废且不回 pending ⇒ 确定性丢单"
	exit 1
fi
CNT112=$((CNT112 + 1))
OLD_TAKE=$(pyc112 "$GWI112" "$PUSH_FN" 'self.store.dispatch_pending(limit=50)')
if [ "${OLD_TAKE:-0}" != "0" ]; then
	echo "--- FAIL: §112 窗口负锁 ${CNT112}（file 桥里又用回取单即翻状态的 dispatch_pending=${OLD_TAKE}）"; exit 1
fi
eq112 "$GWI112" 'pending = self.store.dispatch_pending_peek(limit=50)' 1 'peek 取单点恰好一处'
eq112 "$GWI112" 'moved = self.store.dispatch_mark_inflight([p.get("seq") for p in pending])' 1 'mark 落单点恰好一处（按 seq 翻，且只翻仍 pending 的行）'
eq112 "$GWI112" 'self._record_file_bridge_push_failure(len(pending), cmd_path)' 1 '写失败计数接线恰好一处（旧形态只 log.exception，看不出是抖动还是系统性坏了）'
eq112 "$GWI112" 'def _record_file_bridge_push_failure(self, held, cmd_path):' 1 '计数实现单点'
CNT112=$((CNT112 + 1))
RAISES=$(pyc112 "$GWI112" "$REC_FN" 'raise ')
if [ "${RAISES:-0}" != "0" ]; then
	echo "--- FAIL: §112 窗口负锁 ${CNT112}（计数函数里出现 raise=${RAISES}）"
	echo "    这个函数跑在 0.5s 轮询线程里，抛出去等于把命令下发线程打死，比「这一轮没写成」严重得多"; exit 1
fi
CNT112=$((CNT112 + 1))
RESET_OK=$(pyc112 "$GWI112" "$PUSH_FN" 'self._file_bridge_push_fail_streak = 0')
if [ "${RESET_OK:-0}" != "1" ]; then
	echo "--- FAIL: §112 成功腿归零等值锁 ${CNT112}（push 窗口内 streak 归零应恰 1 处，实得 ${RESET_OK}）"
	echo "    不归零则观察位永远 >0（分不清「此刻还在坏」与「历史上坏过」）；归两处＝有人在别的分支提前抹平计数"; exit 1
fi
eq112 "$GWI112" 'payload["file_bridge_push"] = {' 1 '/admin/status 观察位恰好一个键（读侧只有一个消费者）'
eq112 "$GWI112" '"fail_total": int(self._file_bridge_push_fail_total),' 1 '累计计数透出'
eq112 "$GWI112" '"fail_streak": int(self._file_bridge_push_fail_streak),' 1 '连续计数透出'
eq112 "$GWI112" '"fail_held": int(self._file_bridge_push_fail_held),' 1 '被按住的单数透出'
eq112 "$GWS112" 'def dispatch_pending_peek(self, limit=50):' 1 '两段式第一腿（只读）定义恰好一处'
eq112 "$GWS112" 'def dispatch_mark_inflight(self, seqs):' 1 '两段式第二腿定义恰好一处'
eq112 "$GWS112" 'def dispatch_pending(self, limit=50):' 1 '旧函数仍在位（HTTP /dispatch/pending 是「取走即交付」，那条通道用得对；本波只把它从 file 桥换掉，没删原处能力）'
CNT112=$((CNT112 + 1))
PEEK_WRITE=$(pyc112 "$GWS112" "$PEEK_FN" 'UPDATE dispatch')
if [ "${PEEK_WRITE:-0}" != "0" ]; then
	echo "--- FAIL: §112 只读性负锁 ${CNT112}（peek 函数里出现 UPDATE dispatch=${PEEK_WRITE}）—— peek 一写状态，「写失败保持 pending」这条修法当场作废"; exit 1
fi
eq112 "$GWS112" "WHERE seq = ? AND status = 'pending'" 1 'mark 只翻仍 pending 的行（并发下不把别人的 inflight 拖回来）'

SNAP112 6
# ── ⑦ 测试资产派生登记（分母来自源码，不写死清单）──
W5GO=$(grep -rl --include='*_test.go' '修复批 波 5' cmd internal 2>/dev/null | { grep '\.go$' || true; } | { grep -v '/\.qoder/' || true; } | LC_ALL=C sort || true)
W5GO_N=$(printf '%s\n' "$W5GO" | grep -c . || true)
min112 '波 5 标记的 Go 测试文件数（派生面；新增测试文件请连同本段的登记说明一起改）' "$W5GO_N" 7
W5PY=$( { grep -rl --include='test_*.py' '修复批 波 5' cmd/pydata qmt_gateway 2>/dev/null || true; } | { grep '\.py$' || true; } | { grep -v __pycache__ || true; } | { grep -v '/\.qoder/' || true; } | LC_ALL=C sort || true)
W5PY_N=$(printf '%s\n' "$W5PY" | grep -c . || true)
min112 '波 5 标记的 pytest 文件数（线格式 K 系列 + 文件桥顺序 L 系列）' "$W5PY_N" 2
PAIRS112="$W112/w5_pairs.tsv"
: > "$PAIRS112"
for _f in $W5GO; do
	for _n in $(grep -oE '^func Test[A-Za-z0-9_]+' "$_f" 2>/dev/null | sed 's/^func //' || true); do
		printf '%s\t%s\n' "$(dirname "$_f")" "$_n" >> "$PAIRS112"
	done
done
CNT112=$((CNT112 + 1))
NAME_N=$(grep -c . "$PAIRS112" || true)
min112 '派生出的波 5 Go 用例总数（少一条＝有测试被删/改名/标记丢失；改名必须打断 Test 前缀才算真删）' "$NAME_N" 21
# 包数单独派生（不抬判定点，只是收尾读数的口径）：一个包可以有多个测试文件，
# 把「文件数」当「包数」印出来就是本段自己的「合称读数≠拆分数」形态，宁可分开数。
W5PKG_N=$(cut -f1 "$PAIRS112" 2>/dev/null | LC_ALL=C sort -u | grep -c . || true)
echo "   观测（波 5 资产）: go 文件 ${W5GO_N} 个/包 ${W5PKG_N} 个/用例 ${NAME_N} 条，pytest 文件 ${W5PY_N} 个"

SNAP112 7
# ── ⑧ 行为腿：逐包按派生用例名跑，PASS 数与派生数等值（防空转正＝-run 落空也报 ok）──
go_leg112() { # $1=包目录 $2=用例名竖线串 $3=应跑条数
	local out pass fail
	CNT112=$((CNT112 + 1))
	out=$(go test -count=1 -v -run "^($2)$" "./$1/" 2>&1 || true)
	if printf '%s\n' "$out" | grep -qE '\[build failed\]|build failed|cannot find package|^# '; then
		echo "--- FAIL: §112 Go 行为腿 ${CNT112}（包 $1 编译不过，下面的 PASS/FAIL 计数全部无意义）："
		printf '%s\n' "$out" | head -20
		exit 1
	fi
	fail=$(printf '%s\n' "$out" | grep -c '^--- FAIL' || true)
	pass=$(printf '%s\n' "$out" | grep -c '^--- PASS' || true)
	if [ "${fail:-0}" != "0" ]; then
		echo "--- FAIL: §112 Go 行为腿判红（$1 的波 5 用例，红 ${fail} 条），明细："
		printf '%s\n' "$out" | grep -E '^--- FAIL|^ +.*_test\.go:[0-9]+:' | head -20
		exit 1
	fi
	if [ "${pass:-0}" != "$3" ]; then
		echo "--- FAIL: §112 Go 行为腿计数等值 ${CNT112}（$1 应跑 $3 条、实跑 ${pass:-0} 条）"
		echo "    少了就是有用例没被 -run 命中（改名/标记丢失/被 build tag 挡掉）——「ok 但一条没跑」是本仓最常见的假绿形态"
		exit 1
	fi
	echo "ok - §112 Go 行为腿 ${1}：${pass}/$3 条通过"
}
for _p in $(cut -f1 "$PAIRS112" | LC_ALL=C sort -u); do
	_names=$(awk -F'\t' -v d="$_p" '$1==d{print $2}' "$PAIRS112" | LC_ALL=C sort | paste -sd'|' -)
	_want=$(awk -F'\t' -v d="$_p" '$1==d' "$PAIRS112" | grep -c . || true)
	go_leg112 "$_p" "$_names" "$_want"
done

py_leg112() { # $1=pytest 文件 $2=最少必须真跑过的条数（依赖无关的静态腿；防空转正）
	local target="$1" floor="$2" out want passed skipped decided ran
	CNT112=$((CNT112 + 1))
	want=$(grep -c '^    def test_' "$target" 2>/dev/null || true)
	[ "${want:-0}" -ge 1 ] || { echo "--- FAIL: §112 pytest 资产派生 ${CNT112}（$target 一条 test 方法都没派生到＝文件写法变了）"; exit 1; }
	out=$(py_tests "$target" 2>&1 || true)
	if printf '%s\n' "$out" | grep -qE 'FAILED|ERROR|ModuleNotFoundError|No such file or directory'; then
		echo "--- FAIL: §112 pytest 行为腿判红（${target}，派生 ${want} 条），尾部如下："
		printf '%s\n' "$out" | tail -25
		exit 1
	fi
	# 汇总行计数走 python 而不是 sed：pytest 在小文件上的汇总行**就是顶格**（`7 passed in 0.52s`），
	# 第一版写成 `.*[ ,]\([0-9]*\) passed`——行首那一种永远匹配不到，于是明明 7 条全过的腿被当成
	# 「unittest 退回形态」再解析一遍、读出 Ran=0 判红（本批 §112 首跑实录）。锚要按运行时真形定。
	PYSUM=$(printf '%s\n' "$out" | python3 -c 'import re,sys
t = sys.stdin.read()
m = re.search(r"(\d+) passed", t)
sk = re.search(r"(\d+) skipped", t)
print("P=%s" % (m.group(1) if m else ""))
print("S=%s" % (sk.group(1) if sk else ""))' 2>/dev/null || true)
	passed=$(printf '%s' "$PYSUM" | sed -n 's/^P=//p' | head -1)
	skipped=$(printf '%s' "$PYSUM" | sed -n 's/^S=//p' | head -1)
	if [ -z "$passed" ]; then
		# unittest 退回形态（本机没装 pytest 时 py_tests 走这条）：Ran N tests + 逐行 ... ok / ... skipped
		ran=$(printf '%s\n' "$out" | sed -n 's/^Ran \([0-9][0-9]*\) tests.*/\1/p' | head -1)
		passed=$(printf '%s\n' "$out" | grep -c '\.\.\. ok' || true)
		skipped=$(printf '%s\n' "$out" | grep -c '\.\.\. skipped' || true)
		[ "${ran:-0}" = "$want" ] || { echo "--- FAIL: §112 pytest 行为腿（unittest 形态）${CNT112}：$target 应跑 $want 条、实跑 Ran=${ran:-0}"; exit 1; }
	fi
	decided=$(( ${passed:-0} + ${skipped:-0} ))
	if [ "$decided" != "$want" ]; then
		echo "--- FAIL: §112 pytest 行为腿计数等值 ${CNT112}（$target 派生 $want 条、有结论 ${decided} 条＝通过 ${passed} + 跳过 ${skipped:-0}）"
		echo "    对不上＝有方法没被收集（改名/语法坏/类没继承 TestCase），收集不全的绿不算绿"
		exit 1
	fi
	if [ "${passed:-0}" -lt "$floor" ]; then
		echo "--- FAIL: §112 pytest 防空转正锁 ${CNT112}（$target 真跑过的条数 ${passed:-0} < 应 ≥${floor}）"
		echo "    K4/K4b 那类**只读源码**的静态腿不依赖第三方库，被整文件 skip＝本批真踩过的形状（dirname 少剥一层 ⇒ 全部 skip、门禁一条不红）"
		exit 1
	fi
	echo "ok - §112 pytest 行为腿 ${target}：${passed} 通过 / ${skipped:-0} 跳过（派生 ${want}）"
}
for _f in $W5PY; do
	case "$_f" in
	"$PYT112") py_leg112 "$_f" 1 ;;   # 线格式腿需要 pandas/baostock，只有 K4 静态腿必须真跑过
	*) py_leg112 "$_f" 5 ;;            # 文件桥顺序腿零第三方依赖，五条全须真跑
	esac
done

SNAP112 8
# ── ⑨ 镜像反证 D1–D10（/tmp 副本树，源文件零改动；先自证镜像基线全绿）──
# 反证枚数由源码派生，不写死在收尾文案里：本批加 D9、再加 D10 的两次都证明「枚数」是最容易和
# 散文走散的量（收尾写九枚、实际跑十枚，而每枚自己还是红的——没人看得出现在数到第几枚）。
# 尺子取 `^dys112 D` 的调用行数（函数定义行 `dys112() {` 不在射程），并钉一枚 ≥10 的正锁：
# 派生为空/过短＝模式失效（改名、换写法），整组反证会退化成恒绿的空循环。
DYS112_N=$(grep -c '^dys112 D' scripts/verify_changes.sh || true)
CNT112=$((CNT112 + 1))
[ "${DYS112_N:-0}" -ge 10 ] \
	|| { echo "--- FAIL: §112 反证枚数派生异常（读到 ${DYS112_N}，应 ≥10＝点名模式失效或反证被删，收尾分组读数不可信）"; exit 1; }
MIR112="$W112/mirror"
mkdir -p "$MIR112"
cp go.mod go.sum "$MIR112"/ 2>/dev/null || { echo "--- FAIL: §112 镜像缺 go.mod/go.sum，反证跑不了"; exit 1; }
cp -R cmd internal tools "$MIR112"/ 2>/dev/null || { echo "--- FAIL: §112 镜像复制 cmd/internal/tools 失败"; exit 1; }
cp -R qmt_gateway "$MIR112"/ 2>/dev/null || { echo "--- FAIL: §112 镜像复制 qmt_gateway 失败"; exit 1; }
find "$MIR112" -name '__pycache__' -type d -prune -exec rm -rf {} + 2>/dev/null || true
CNT112=$((CNT112 + 1))
if ! ( cd "$MIR112" && go build ./... ) > "$W112/build.log" 2>&1; then
	echo "--- FAIL: §112 镜像基线编译不过（反证的每一次红都必须来自被破坏的那一句，不能来自镜像自己坏了），尾部："
	tail -20 "$W112/build.log"
	exit 1
fi
BASE112_RUN='TestDailySourcePartitionOnRealTable|TestDegradedRowLandsWithSourceAndNulls|TestSettleDaySkipBranchesZeroDiffButNotVerified'
# 点名自检（与 §69 那枚同源教训，2026-10-08 波 5 实锤）：`go test -run '^(不存在的名)$'` 打的是
# `ok … [no tests to run]`，而下面那条判据按 `^ok` 数三包等值 3 —— 用例一改名，"镜像基线自证"就
# 退化成"三个空转的包"，反证的前置（镜像可信、红只来自被破坏那一句）从此是假的。
NBASE112=$(printf '%s\n' "$BASE112_RUN" | tr '|' '\n' | grep -c . || true)
CNT112=$((CNT112 + 1))
[ "${NBASE112:-0}" = "3" ] || { echo "--- FAIL: §112 基线用例清单派生为空/过短（读到 ${NBASE112}，应为 3＝点名自检自己失明）"; exit 1; }
BASE_MISS112=""
for _t in $(printf '%s\n' "$BASE112_RUN" | tr '|' '\n'); do
	find internal cmd -name '*_test.go' -exec grep -lE "^func ${_t}\(" {} + 2>/dev/null | grep -q . || BASE_MISS112="${BASE_MISS112} ${_t}"
done
CNT112=$((CNT112 + 1))
[ -z "$BASE_MISS112" ] || { echo "--- FAIL: §112 基线用例点名失效（用例在仓内不存在，go test 会打 ok [no tests to run] 骗过基线自证）：${BASE_MISS112}"; exit 1; }
BASE112=$( cd "$MIR112" && go test -count=1 -v -run "^(${BASE112_RUN})\$" ./internal/store/ ./cmd/dataload/ ./internal/trading/ 2>&1 || true )
CNT112=$((CNT112 + 1))
BASE_OK=$(printf '%s\n' "$BASE112" | grep -c '^ok' || true)
if [ "${BASE_OK:-0}" != "3" ] || printf '%s\n' "$BASE112" | grep -qE '^--- FAIL|^FAIL'; then
	echo "--- FAIL: §112 镜像基线不绿（三包应各有 1 条 ok，实得 ${BASE_OK:-0}）——镜像不可信时，任何「破坏后变红」都不构成证据"
	printf '%s\n' "$BASE112" | tail -20
	exit 1
fi
BASEPY112=$( cd "$MIR112" && py_tests "$PYT112" 2>&1 || true; printf 'SEP\n'; py_tests "$GWT112" 2>&1 || true )
if printf '%s\n' "$BASEPY112" | grep -qE 'FAILED|ERROR'; then
	echo "--- FAIL: §112 镜像 pytest 基线判红（两条 pytest 腿在干净镜像里就该绿），尾部："; printf '%s\n' "$BASEPY112" | tail -20; exit 1
fi
echo "ok - §112 镜像基线自证（三 Go 包 ok + 两条 pytest 无红）"

dys112() { # $1=编号 $2=镜像相对文件 $3=python 变异片段（必须打印落地次数） $4=go|py $5=目标 $6=-run 名(py 传 -) $7=红文案必含串
	local id="$1" rel="$2" mut="$3" kind="$4" target="$5" rx="$6" token="$7" applied out
	CNT112=$((CNT112 + 1))
	cp -f "$REPO112/$rel" "$MIR112/$rel" || { echo "--- FAIL: §112 反证 ${id} 无法从主仓复位镜像文件 $rel"; exit 1; }
	applied=$( cd "$MIR112" && python3 -c "$mut" 2>&1 | tail -1 ) || { echo "--- FAIL: §112 反证 ${id} 变异脚本执行失败：${applied}"; exit 1; }
	if [ "$applied" != "1" ]; then
		echo "--- FAIL: §112 反证 ${id} 变异落地数=${applied}，期望恰好 1（0＝镜像里没找到目标串，这枚反证等于没跑；>1＝命中面比预期宽，红了也不知道红在哪）"
		exit 1
	fi
	if [ "$kind" = go ]; then
		out=$( cd "$MIR112" && go test -count=1 -v -run "^(${rx})\$" "$target" 2>&1 || true )
		if printf '%s\n' "$out" | grep -qE '\[build failed\]|build failed|cannot find package|^# '; then
			echo "--- FAIL: §112 反证 ${id} 变异后包编译不过（harness 坏了不是锁有牙），读数如下："
			printf '%s\n' "$out" | head -20
			exit 1
		fi
	else
		out=$( cd "$MIR112" && py_tests "$target" 2>&1 || true )
		if printf '%s\n' "$out" | grep -qE 'ModuleNotFoundError|ImportError|SyntaxError'; then
			echo "--- FAIL: §112 反证 ${id} 变异后 import/语法坏（harness 坏了），尾部："; printf '%s\n' "$out" | tail -15; exit 1
		fi
	fi
	if ! printf '%s\n' "$out" | grep -qE '^(--- FAIL|FAILED|FAIL: )|^FAIL'; then
		echo "--- FAIL: §112 反证 ${id} 没有让被测腿变红（${target}）——这条锁恒绿，是假锁；尾部："
		printf '%s\n' "$out" | tail -15
		exit 1
	fi
	if ! printf '%s\n' "$out" | grep -qF -- "$token"; then
		echo "--- FAIL: §112 反证 ${id} 红了，但红文案里找不到本枚指定的归属串「${token}」（＝红在别处，成因归属不成立）"
		printf '%s\n' "$out" | grep -E 'FAIL|Error|assert' | head -10
		exit 1
	fi
	cp -f "$REPO112/$rel" "$MIR112/$rel" || { echo "--- FAIL: §112 反证 ${id} 复位失败（${rel}）"; exit 1; }
	if [ "$kind" = go ]; then
		out=$( cd "$MIR112" && go test -count=1 -run "^(${rx})\$" "$target" 2>&1 || true )
	else
		out=$( cd "$MIR112" && py_tests "$target" 2>&1 || true )
	fi
	if printf '%s\n' "$out" | grep -qE '^(--- FAIL|FAILED|FAIL: )|^FAIL'; then
		echo "--- FAIL: §112 反证 ${id} 复位后仍红＝镜像被别处污染（或复位不是真复位），后续反证的读数全部作废"
		exit 1
	fi
	echo "ok - §112 反证 ${id}：破坏 $rel → $target 红（归属串 ${token}）且复位复绿"
}

# D1 sidecar 降级腿把 tradestatus 硬编码回 "1"（旧缺陷本体）→ K4a 必红
dys112 D1 "$SIDE112" 'p="cmd/pydata/server.py"
s=open(p,encoding="utf-8").read()
a="            _NA, _NA, _NA, _NA, _NA, _NA, _NA,"
b="            \"1\", _NA, _NA, _NA, _NA, _NA, _NA, _NA,"
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' py "$PYT112" - 'K4a'
# D2 降级腿行尾标记换成主链路标记（线格式照旧、Go 照落库，唯一痕迹是常量的值）→ K4b 必红
dys112 D2 "$SIDE112" 'p="cmd/pydata/server.py"
s=open(p,encoding="utf-8").read()
a="            _SRC_SINA,"
b="            _SRC_BAOSTOCK,"
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' py "$PYT112" - 'K4b'
# D3 读侧 LIKE 从后缀匹配改成枚举 → 假想的第三条兜底腿漏网
dys112 D3 "$DSC112" 'p="internal/store/daily_source.go"
s=open(p,encoding="utf-8").read()
a="const DailySourceLikeDegraded = \"%degraded%\""
b="const DailySourceLikeDegraded = \"%sina_degraded%\""
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' go ./internal/store/ TestDailySourcePartitionOnRealTable 'TestDailySourcePartitionOnRealTable'
# D4 tradestatus 回到「缺测也当停牌」→ 降级行整行不落库（K1a：根数少于 3）
dys112 D4 "$BSD112" 'p="cmd/dataload/baostock.go"
s=open(p,encoding="utf-8").read()
a="\t\tif ts, ok := r.FOk(\"tradestatus\"); ok && int(ts) == 0 {"
b="\t\tif int(r.F(\"tradestatus\")) == 0 {"
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' go ./cmd/dataload/ TestDegradedRowLandsWithSourceAndNulls 'K1a'
# D5 停牌判据整个摘掉 → 停牌行被写进库（K1c：根数多于 3）
dys112 D5 "$BSD112" 'p="cmd/dataload/baostock.go"
s=open(p,encoding="utf-8").read()
a="if ts, ok := r.FOk(\"tradestatus\"); ok && int(ts) == 0 {"
b="if ts, ok := r.FOk(\"tradestatus\"); false && ok && int(ts) == 0 {"
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' go ./cmd/dataload/ TestDegradedRowLandsWithSourceAndNulls 'K1c'
# D6 is_st 退回 F()（缺测落成 0＝「确定不是 ST」这个真实读数）
dys112 D6 "$BSD112" 'p="cmd/dataload/baostock.go"
s=open(p,encoding="utf-8").read()
a="\t\t\t\"is_st\": numOrNil(r.FOk(\"isst\")),"
b="\t\t\t\"is_st\": r.F(\"isst\"),"
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' go ./cmd/dataload/ TestDegradedRowLandsWithSourceAndNulls 'daily_basic.is_st'
# D7 未连接态的量规读数改回 0（与「本轮没跑」同形 → 规则 ge 2 永不触发，静默失效复活）
dys112 D7 "$SET112" 'p="internal/trading/settlement.go"
s=open(p,encoding="utf-8").read()
a="\tcase SettleOutcomeSkippedNotConnected:\n\t\treturn 2"
b="\tcase SettleOutcomeSkippedNotConnected:\n\t\treturn 0"
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' go ./internal/trading/ TestSettleDaySkipBranchesZeroDiffButNotVerified 'settlement_state 应=2'
# D8 file 桥用回「取单即翻 inflight」→ 写盘失败的那轮单安静消失（L1 必红）
dys112 D8 "$GWI112" 'p="qmt_gateway/gateway.py"
s=open(p,encoding="utf-8").read()
a="pending = self.store.dispatch_pending_peek(limit=50)"
b="pending = self.store.dispatch_pending(limit=50)"
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' py "$GWT112" - 'test_L1_write_failure_keeps_pending_and_counts'

# D9 跳过腿被写回「当日已对账」（计划里的 J2 反证：把 skipped 记成 booked）⇒ J1 必红
#    D7 验的是「三态→量规」那本账，这一枚验的是「三态→账面」那本账：同一个跳过态在两条腿上
#    各有各的落点，只反证量规那条时，有人在账面那侧写回 `lastSettleDay = day` 照样全线绿。
dys112 D9 "$SET112" 'p="internal/trading/settlement.go"
s=open(p,encoding="utf-8").read()
a="\tif !outcome.Verified() {"
b="\tif false && !outcome.Verified() {"
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' go ./internal/trading/ TestMaybeSettleDayNotConnectedDoesNotBookTheDay '§P2-E 反例'

# D10 未连接腿不再归零差异读数（这是本批**改判方向**的那枚：跳过腿保留上一轮残值）。
#     为什么要为"改错了半步"专门留反证：§P2-E 的缺陷本体是「不适用与无差异不可分辨」，
#     中途我把它读成「跳过腿不该写差异读数」，于是删了归零那行——而 §69（§DEADGAUGE 负锁③）
#     当场判红，理由成立：规则 settlement_diff 是 `gt 0`，不写数＝上一轮真比对的条数常驻，
#     断线日会天天拿旧差异刷 p1。最终口径是"两个键合起来说"（diff 归零 + state 给 2/3）。
#     反证钉住这个结论的两端：删掉归零 ⇒ 行为腿必红（残值形态复活），而不是只靠 §69 的静态串。
dys112 D10 "$SET112" 'p="internal/trading/settlement.go"
s=open(p,encoding="utf-8").read()
a="\t\toutcome = SettleOutcomeSkippedNotConnected\n\t\tmetrics.SetGauge(\"settlement_diff_count\", 0)\n"
b="\t\toutcome = SettleOutcomeSkippedNotConnected\n"
print(s.count(a))
open(p,"w",encoding="utf-8").write(s.replace(a,b,1))' go ./internal/trading/ TestSettleDaySkipBranchesZeroDiffButNotVerified '未连接腿同样必须归零 settlement_diff_count'

# 组⑨的快照必须打在 D10 **之后**：打在前面时最后那一票反证会落进组⑩的窗口，
# 于是收尾写「十枚反证」而组⑨只数到九枚，求和自证锁照样绿（每票仍只落一组，只是落错了组）。
SNAP112 9

# ── ⑩ 覆盖面诚实交代（本段验到哪、没验到哪）──
# 快照调用点自身对账：每个组号**恰好调用一次**。这一枚是本批真踩出来的——D9 加进组⑨时把
# SNAP112 9 留在了 D8 之后（旧边界），于是组⑨只数到 D9 一票、组⑩多背了一票，而「每组 >0」
# 的在位锁与求和自证锁双双绿着：每票仍只落进一组，只是落错了组，收尾的分组读数从此不可信。
CNT112=$((CNT112 + 1))
SNAP_BAD112=""
for _g in 1 2 3 4 5 6 7 8 9 10; do
	_c=$( { grep -cE "^SNAP112 ${_g}$" "$REPO112/scripts/verify_changes.sh" 2>/dev/null || true; } | head -1)
	[ "${_c:-0}" = "1" ] || SNAP_BAD112="${SNAP_BAD112} ${_g}(命中${_c:-0})"
done
if [ -n "$SNAP_BAD112" ]; then
	echo "--- FAIL: §112 快照调用点等值锁 ${CNT112}（这些组号的 SNAP112 调用不是恰好一次：${SNAP_BAD112}）"
	echo "    少一次＝有一组的锁并进了邻组（分组读数假）；多一次＝后一次把增量切成一小截，收尾「⑨ 镜像反证 N 道」数到的不是那一批"
	exit 1
fi
CNT112=$((CNT112 + 1))
DEGONLY=$(scan_go112 'DailySourceDegradedOnly(' | wc -l | tr -d ' ')
if [ "${DEGONLY:-0}" != "0" ] && [ "${DEGONLY:-0}" != "1" ]; then
	echo "--- FAIL: §112 取向锁 ${CNT112}（DailySourceDegradedOnly 的生产调用点应 0 或 1，实得 ${DEGONLY}）"
	echo "    这一支现在只被测试消费（生产三个统计点都用 NotDegraded）。留着的理由见本段前言①；多到两处＝有人在别处又数一遍降级行"; exit 1
fi
CNT112=$((CNT112 + 1))
if [ -z "${RBK112:-}" ] || [ ! -f "$RBK112" ]; then
	echo "--- FAIL: §112 射程文件在位锁 ${CNT112}（$RBK112 不在位＝广度判据的落点被搬走了，本段的读侧锁全部要重新定位）"; exit 1
fi
# ── 分组自证（本段最后两道锁，收尾那行的分组读数靠它们兜底）──
# ① 在位锁：①–⑨ 每组都必须真记到判定点。少打一处 SNAP 时，那组的锁会被**并进下一组**的读数里
#    （累计数照样对得上），分组清单于是开始撒谎——这正是「累计 100 道却把没跑到的组算进去」的形态。
# ② 求和锁：十组快照之和 == 累计判定点（扣掉本锁自己那一票），拦「改了 CNT112 却没落进任何组」。
CNT112=$((CNT112 + 1))
_EMPTY_G=""
for _g in 1 2 3 4 5 6 7 8 9; do
	_v="G${_g}_N"
	if [ "${!_v}" -le 0 ]; then _EMPTY_G="${_EMPTY_G} ${_g}"; fi
done
if [ -n "$_EMPTY_G" ]; then
	echo "--- FAIL: §112 分组快照在位锁 ${CNT112}（这些组一个判定点都没记到：组${_EMPTY_G}）"
	echo "    要么该组被整组删了，要么组边界上的 SNAP112 漏了——漏一处时那组的锁会并进邻组的读数，累计数看起来正常而分组数是假的"
	exit 1
fi
SNAP112 10
CNT112=$((CNT112 + 1))
SUMG112=$((G1_N + G2_N + G3_N + G4_N + G5_N + G6_N + G7_N + G8_N + G9_N + G10_N))
# 求和里不含本锁自己这一票（SNAP112 10 已经打过，之后的增量只有这一道），所以扣 1。
if [ "$SUMG112" != "$((CNT112 - 1))" ]; then
	echo "--- FAIL: §112 分组求和自证锁 ${CNT112}（十组快照之和 ${SUMG112} != 累计判定点扣本锁 $((CNT112 - 1))）——有判定点没落进任何一组，收尾的覆盖面读数不可信"
	exit 1
fi
rm -rf "$W112"
echo "ok - §112 全段通过：① sidecar 常量族与 K4 射程集合 ${G1_N} 道 + ② 跨语言表头/缺测标记等值 ${G2_N} 道 + ③ 取数三态与落库姿势 ${G3_N} 道 + ④ 读侧单源与 schema 四处同源 ${G4_N} 道 + ⑤ 三态接线/别名 ban/映射表等值 ${G5_N} 道 + ⑥ 文件桥函数段顺序锁与观察位接线 ${G6_N} 道 + ⑦ 测试资产派生登记 ${G7_N} 道 + ⑧ 行为腿（Go ${W5GO_N} 个文件/${W5PKG_N} 包 ${NAME_N} 条用例逐包与派生数等值、pytest ${W5PY_N} 份含防空转正 floor）${G8_N} 道 + ⑨ 镜像基线自证与 ${DYS112_N} 枚反证 D1–D${DYS112_N} ${G9_N} 道 + ⑩ 覆盖面诚实与分组自证 ${G10_N} 道，累计判定点 ${CNT112}（其中十组快照之和 ${SUMG112}，另有 1 道就是求和自证锁本身）"
echo ""
echo "==> 113 §P2-H/§P2-I/§P2-J/§P3-FE 前端一致性契约（2026-10-06 修复批 波 6）：单实现扫描器派生读数、涨跌令牌等值、守卫/台账接线、五组行为腿与七枚镜像反证..."

# 本段守波 6 的四条缺陷（10-05 全量评价 P2-H/I/J + P3 前端组 → 10-06 按 docs/FIX_PLAN_20261006.md 落码）。
#
#  ① §P2-H 后端通道早就齐了、前端零调用点。GET/POST /api/config/d1 自 §0929CFG-D1 起就是
#     「稀疏 merge + 写前快照 + 字段级审计 + effective_source 回显」，而 `grep -rn 'config/d1' web/src`
#     在本批开工前**零命中**：直接参与打分的 D1 参数只能在页面外改，「页面写的值≠引擎吃的值」
#     这道缝没有出口。这条缺陷的锁形状必须是三段都得有（api 出口 / 面板调用 / 页面挂载），
#     少任何一段都等于回到「没人用」——面板写了但没页面 render 就是 §DEADGAUGE「定义没接」同族，
#     所以 M3 用的是**派生挂载页集合**（d1_mounted_pages），不是点名 Settings.jsx。
#  ② §P2-I 轮询后到的旧响应覆盖新读数。旧形态是三页各有一份守卫写法、其余页面干脆没有，
#     而「用不用守卫」是每页一个口头决定（本批开工前实测：11 个有轮询/SSE 的页面只有 3 个接了守卫）。
#     ⑦组钉的是：守卫的**唯一正确写法**收在 useStaleGuard() 里（惰性 useRef），页面侧只剩一个调用点；
#     `useRef(createStaleGuard())` 这种「每渲染 new 一个、代号序列重置、isStale 永远 false」的假绿形态
#     必须被负锁拦住——而这把负锁**只能剥注释之后数**（那句话本身就是本批说明注释的原文，
#     §107 预演实录：恒红的是尺子，不是产品）。
#  ③ §P2-J 四路数据各自 `catch (_) {}`：容错本身对（一路挂掉不该拖黑整页），坏在失败之后
#     什么都不留——界面照常显示上一轮读数或空列表。现按腿记账（loadLedger）+ 单实现红条
#     （LoadFailBanner，testid 常量只有一处）+ KPI 与三张表不同源时明写「哪一侧是上一轮」。
#     ⑧组的形状是**派生**：谁接了台账，谁的 catch 就必须对账、谁就必须 render 红条；
#     点名清单会让下一个新页面自动落在锁外。
#  ④ §P3-FE 一致性组：着色反向（Research 的「胜」配绿、「负」配红，与同页 signColor 与 Dashboard
#     正好相反）、抽屉「实时价配冻结涨幅」、回测超额两口径、watchlist 缺数渲染 ¥0.00、
#     DepthPanel 的 '--' 染绿、胜率取整。这些条的共同点是**不报错**——错了也看不出来，
#     所以判据一律取「等于交付真值」而不是「看起来对」：--app-up/--app-down 的**全部定义值**
#     （亮色 + 暗色两处；只数第一处时，暗色块被改成涨绿跌红照样绿——这是本批实录过的盲点）、
#     缺测占位串等值、三态 pctState 的字面量等值。亚单位不取整按 owner 裁决⑧（99.6% 不得显示成 100%）。
#
#  ★ 本段最重要的一条纪律：**同一判据只有一份实现**。波 6 的覆盖面判据（轮询页守卫、catch 对账、
#     D1 调用点、涨跌令牌真值）原本要在 bash 与 vitest 各写一遍——两份并存的结局本批已经踩过
#     太多次：只修一份、另一份继续读旧判据（§0929DRILL 的 record_freshness()、
#     §BOM-REPO-DERIVE 的派生清单、§107 的三件套同形）。现在判据只在
#     scripts/fe_contract_scan.mjs 一处，两个消费者（vitest 的 p6_derived_coverage + 本段②组）
#     读同一份 JSON。①组因此先锁「扫描器是唯一的尺子」：门禁自己**不许再写第二把剥注释的尺子**，
#     vitest 文件里**不许再出现第二份 classify/auditCatches**。
#  ★ 扫描器自己不判红（`process.exit(1)` 命中数必须为 0）：它只如实报「扫到了什么」，
#     「扫到 0 条该不该红」由两个消费者断言。否则正则失效时它会安静地什么都不报，
#     而那正是本枚要消灭的形态。同理，②组先用 key113 断「分母键本身读到了数」，
#     再用它作等值分母——否则键名一坏，比较会退化成「拿 0 去比 0」的恒绿。
#  ★ 反证七枚 V1–V7：五枚走扫描器读数（在 /tmp 镜像里破坏，断读数按预期翻转、复位复回基线），
#     两枚走 vitest 镜像腿（Paper 摘角标、抽屉涨幅改回 props 冻结值）。镜像先做**基线自证**：
#     读数与真仓等值 + 两份测试文件在镜像里各自绿——镜像不可信时，任何「破坏后变红」
#     都不构成证据（§112 同族）。每枚变异落地数必须恰好 1；归属串必须是**基线里不存在**的串，
#     否则「红了且文案里有它」是免费的（V3 的 #2fbf87 在基线里本来就作为跌色存在，
#     所以那枚的归属串换成整行 `token_up_list=#e34d59,#2fbf87`——这条同族坑写死在函数里）。
#
#  English: §113 locks wave-6 frontend consistency. The judgement code lives once in
# scripts/fe_contract_scan.mjs and both consumers (vitest + group ② here) read the same JSON; colour
# tokens are asserted equal to the delivered truth (light AND dark definitions); guard/ledger wiring is
# asserted by derived sets; seven /tmp mirror reverses prove the locks bite.
CNT113=0
REPO113="$PWD"
SCN113=scripts/fe_contract_scan.mjs
P6T113=web/src/__tests__/p6_derived_coverage.test.js
PAP113=web/src/pages/Paper.jsx
DRW113=web/src/components/StockDetailDrawer.jsx
DPT113=web/src/components/DepthPanel.jsx
WLS113=web/src/pages/Watchlist.jsx
RES113=web/src/pages/Research.jsx
STY113=web/src/styles.css
API113=web/src/api/index.js
PNL113=web/src/components/D1ConfigPanel.jsx
SET113=web/src/pages/Settings.jsx
SGD113=web/src/utils/staleGuard.js
LGD113=web/src/utils/loadLedger.js
LFB113=web/src/components/LoadFailBanner.jsx
SRV113=internal/server/server.go
HND113=internal/server/handlers_fix.go
W113="$(mktemp -d /tmp/p2wave6-XXXXXX 2>/dev/null || true)"
[ -n "$W113" ] || { echo "--- FAIL: §113 建不出镜像目录，扫描器读数腿与七枚反证无法跑（宁可红，不许跳）"; exit 1; }
MIR113="$W113/mirror"

eq113() { # $1=文件 $2=整串 $3=预演读数 $4=说明
	CNT113=$((CNT113 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §113 整串等值锁 ${CNT113}（${4}）：${1} 整串「${2}」got=${got:-0} 预演=$3"; exit 1; }
}
neg113() { # $1=文件 $2=整串 $3=说明 → 彻底没有
	CNT113=$((CNT113 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "0" ] || { echo "--- FAIL: §113 负锁 ${CNT113}（${3}）：${1} 又出现「${2}」got=${got}"; exit 1; }
}
min113() { # $1=说明 $2=实得 $3=应≥ —— 派生面过窄即红（空转正锁家族）
	CNT113=$((CNT113 + 1))
	[ "${2:-0}" -ge "${3:-1}" ] || { echo "--- FAIL: §113 派生正锁 ${CNT113}（${1}）：实得=${2:-0} 应≥${3}"; exit 1; }
}
# strip_ansi113：剥掉终端转义序列。vitest 在被管道接走时**照样上色**（2026-10-09 预演实读：
# 「Test Files」后面紧跟 \033[22m \033[1m\033[32m 再是「5 passed」），于是按「Test Files  5 passed」
# 数行的两条锁（⑨ 的跑满清单、⑩ 的镜像基线两份各自绿）会红在颜色上而不是红在结果上。
# 剥色只在这里一处实现，两个捕获点共用——写两遍的结局是改一遍、另一遍继续读带色的原文。
strip_ansi113() { LC_ALL=C sed $'s/\033\[[0-9;]*m//g'; }
# js113：JS/JSX/CSS 的代码行判据一律走扫描器的 codeOnly()——剥注释只有这一把尺子。
# 门禁若自己再写一份「去掉 // 与 /*」的管道，就成了同一判据的第二份实现（①组的负锁拦的就是它）；
# 而本段拦的正是 useRef(createStaleGuard()) 这类「只在说明注释里出现」的形态，
# 用带注释的尺子数会让负锁在健康代码上恒红。
js113() { # $1=line|re|many  $2=**仓库根相对**路径（many 模式为空格分隔的目录清单） $3=整串/正则
	REL113="$2" PAT113="$3" MODE113="$1" node --input-type=module -e '
const { codeOnly } = await import("./scripts/fe_contract_scan.mjs")
const fs = await import("node:fs")
const pathP = await import("node:path")
const readCode = (p) => codeOnly(fs.readFileSync(p, "utf8"))
const countLines = (text, needle) => {
  let n = 0
  for (const l of text.split("\n")) { if (l.includes(needle)) n++ }
  return n
}
if (process.env.MODE113 === "many") {
  let n = 0
  for (const d of process.env.REL113.split(" ")) {
    if (!d) continue
    for (const f of fs.readdirSync(d).filter((x) => /\.jsx?$/.test(x))) {
      n += countLines(readCode(pathP.join(d, f)), process.env.PAT113)
    }
  }
  process.stdout.write(String(n))
} else if (process.env.MODE113 === "re") {
  const m = readCode(process.env.REL113).match(new RegExp(process.env.PAT113, "g"))
  process.stdout.write(String(m ? m.length : 0))
} else {
  process.stdout.write(String(countLines(readCode(process.env.REL113), process.env.PAT113)))
}
' 2>&1
}
eqj113() { # $1=rel $2=整串 $3=预演 $4=说明 → 代码行行数等值
	local got
	got=$(js113 line "$1" "$2")
	CNT113=$((CNT113 + 1))
	case "$got" in
	*Error*|*error*|*not\ found*) echo "--- FAIL: §113 代码行尺子没出数（${1}「${2}」）：${got}"; exit 1 ;;
	esac
	[ "$got" = "$3" ] || { echo "--- FAIL: §113 代码行等值锁 ${CNT113}（${4}）：$1 的代码行「${2}」got=${got} 预演=$3"; exit 1; }
}
negj113() { # $1=rel $2=整串 $3=说明 → 代码行彻底没有
	local got
	got=$(js113 line "$1" "$2")
	CNT113=$((CNT113 + 1))
	case "$got" in
	*Error*|*error*|*not\ found*) echo "--- FAIL: §113 代码行尺子没出数（${1}「${2}」）：${got}"; exit 1 ;;
	esac
	[ "$got" = "0" ] || { echo "--- FAIL: §113 代码行负锁 ${CNT113}（${3}）：$1 的代码行又出现「${2}」got=${got}"; exit 1; }
}
minj113() { # $1=rel $2=整串 $3=应≥ $4=说明 → 代码行行数下界（派生面缩水即红）
	local got
	got=$(js113 line "$1" "$2")
	CNT113=$((CNT113 + 1))
	case "$got" in
	*Error*|*error*|*not\ found*) echo "--- FAIL: §113 代码行尺子没出数（${1}「${2}」）：${got}"; exit 1 ;;
	esac
	[ "${got:-0}" -ge "$3" ] || { echo "--- FAIL: §113 代码行派生正锁 ${CNT113}（${4}）：$1 的代码行「${2}」实得=${got:-0} 应≥${3}"; exit 1; }
}
eqr113() { # $1=rel $2=正则 $3=预演 $4=说明 → 代码行命中次数等值
	local got
	got=$(js113 re "$1" "$2")
	CNT113=$((CNT113 + 1))
	[ "$got" = "$3" ] || { echo "--- FAIL: §113 次数等值锁 ${CNT113}（${4}）：$1 正则「${2}」got=${got} 预演=${3}（got 不是数字时＝代码行尺子本身坏了）"; exit 1; }
}
PREV113=0
# 十组读数先归零（set -u 下未赋值直接参与算术会中止脚本，报错位置离成因很远；
# 「某一组一个判定点都没有」这件事由收尾的分组在位锁正面拦住，不靠 unbound 侥幸）。
H1_N=0; H2_N=0; H3_N=0; H4_N=0; H5_N=0; H6_N=0; H7_N=0; H8_N=0; H9_N=0; H10_N=0
SNAP113() { # $1=组号 → 记下本组新增判定点数（收尾 ok 行的「① 组 N 道」由这里派生，不是手写清单）
	printf -v "H$1_N" '%s' "$((CNT113 - PREV113))"
	PREV113=$CNT113
}
jget113() { # $1=键 → 从本段的 KEY=VALUE 读数里取值
	printf '%s\n' "$SCAN113_KV" | sed -n "s/^$1=//p" | head -1
}
jline113() { # $1=键前缀 $2=必含串 → 该键的读数行里必须含归属串（整行判，不跨键蹭命中）
	printf '%s\n' "$1" | grep "^$2" | grep -qF -- "$3"
}

# ── ① 单实现扫描器：判据只此一份、它自己不判红、两个消费者都接在同一把尺子上 ──
CNT113=$((CNT113 + 1))
[ -f "$SCN113" ] || { echo "--- FAIL: §113 单实现扫描器不在位（$SCN113 缺失＝波 6 的覆盖面判据没有落点，②–⑧组全部要重新定位）"; exit 1; }
eq113 "$SCN113" 'export function classify(src)' 1 '注释/代码拆分只有这一把尺子（①–⑧组的静态判据都从它派生）'
eq113 "$SCN113" 'export function codeOnly(src)' 1 '纯代码文本出口（负锁不剥注释就会把说明文字当一处实现）'
eq113 "$SCN113" 'export function scanPollers(src)' 1 '轮询特征扫描（N1 射程）'
eq113 "$SCN113" 'export function guardAccount(src)' 1 '守卫接线账（import/begin/isStale）'
eq113 "$SCN113" 'export function pollCoverage(root)' 1 'N1 主入口：pages 派生集合'
eq113 "$SCN113" 'export function auditCatches(src)' 1 'catch 对账（P14）'
eq113 "$SCN113" 'export function catchCoverage(root)' 1 'P14 主入口：射程派生自台账接入面'
eq113 "$SCN113" 'export function d1Coverage(root)' 1 'M3 主入口：api 出口/面板调用/页面挂载三段'
eq113 "$SCN113" 'export function tokenTruths(root)' 1 '涨跌令牌：返回全部定义（只取第一处时暗色块落在锁外）'
eq113 "$SCN113" 'export function report(root)' 1 '汇总出口：两个消费者读同一份键名'
# 现测下界写在扫描器里（写两遍的结局是改一遍、另一遍继续用旧数）。
eq113 "$SCN113" 'pollPages: 11' 1 '轮询页下界（2026-10-09 波 6 实跑 11 页，同提交同步）'
eq113 "$SCN113" 'ledgerFiles: 4' 1 '台账接入页面数下界（实跑 4 页，另有共用红条）'
eq113 "$SCN113" 'catchTotal: 40' 1 'catch 语料量级下界（实跑 44）'
eq113 "$SCN113" 'upTokenDefs: 2' 1 '涨跌令牌定义处数下界（亮色 + 暗色各一份）'
neg113 "$SCN113" 'process.exit(1)' '扫描器自己不判红——判红留给两个消费者（正则失效时它必须如实报 0，而不是自我销案）'
eq113 "$SCN113" 'JSON.stringify(report(argRoot), null, 2)' 1 'CLI 出口：门禁读的就是这份 JSON'
# 入口判据（2026-10-09 预演实录）：镜像反证把扫描器放在 **/tmp** 副本树里执行，而 macOS 的 /tmp
# 是指向 /private/tmp 的符号链接。按字符串比 argv[1] 时两者永不相等 ⇒ 扫描器判成「被人 import」，
# 于是安静地不打印、退出码还是 0；消费端只看到「空读数」，报错会把你引向「扫描器坏了」。
eq113 "$SCN113" 'fs.realpathSync(path.resolve(argv1))' 1 '入口比 realpath（符号链接下空输出＋退出码 0 就是本段 ⑩ 组的第一杀手）'
neg113 "$SCN113" 'const isMain = ' '按字符串比较 argv[1] 的旧入口写法禁止复活（镜像里跑不出读数）'
# 消费者一＝vitest：p6 文件必须 import 扫描器，而不是自带第二份判据。
eq113 "$P6T113" "from '../../../scripts/fe_contract_scan.mjs'" 1 'vitest 消费者接在同一把尺子上'
neg113 "$P6T113" 'function classify(' '第二把 classify＝两份判据分家（只修一份的那条老路）'
neg113 "$P6T113" 'function auditCatches(' '第二份 catch 对账实现'
neg113 "$P6T113" 'function catchBodyOf(' '第二份花括号配平（体读短就看不见体内动作，v1 实录形态）'
neg113 "$P6T113" 'function scanPollers(' '第二份轮询特征扫描'
neg113 "$P6T113" 'function guardAccount(' '第二份守卫接线账'
# 消费者二＝本段：JS 侧静态判据必须走 js113（内部 import 扫描器的 codeOnly），不许自带剥注释管道。
BODY113=$(sed -n '/^echo "==> 113 /,/^echo "==> 全部通过"/p' scripts/verify_changes.sh 2>/dev/null || true)
CNT113=$((CNT113 + 1))
[ -n "$BODY113" ] || { echo "--- FAIL: §113 取不到本段段体（段头到收尾标记之间），尺子形状锁无从判（宁可红，不许跳）"; exit 1; }
min113 '本段代码行判据都经扫描器 codeOnly（命中过少＝门禁自己另写了一把尺子）' "$(printf '%s\n' "$BODY113" | grep -c 'codeOnly' || true)" 2
min113 '本段调用 js113 家族的行数（0＝静态锁退化回 grep 全文，说明注释会冒充一处实现）' "$(printf '%s\n' "$BODY113" | grep -cE '^(eqj113|negj113|minj113|eqr113|js113|BODY113) ' || true)" 40
# A9-SELF：本枚的靶串就写在这一行上，不去掉自己这行的话锁会红在自己的话术上（§89 自指锁同族，
# 区别是那条要求「写法不存在」所以能整串点名，这条是「本段内不许另写一把尺子」——只能按行排除）。
neg113 <(printf '%s\n' "$BODY113" | grep -v 'A9-SELF' || true) "s|//.*||" '门禁内自制的剥注释管道＝第二把尺子（与扫描器分家的起点）'
SNAP113 1

# ── ② 扫描器读数逐键（门禁这个消费者读的 JSON，键名与 vitest 用的完全相同）──
SCAN113_RC=0
node "$SCN113" > "$W113/scan.json" 2>"$W113/scan.err" || SCAN113_RC=$?
CNT113=$((CNT113 + 1))
[ "$SCAN113_RC" = "0" ] || { echo "--- FAIL: §113 扫描器跑失败（rc=${SCAN113_RC}）：$(tail -3 "$W113/scan.err")"; exit 1; }
CNT113=$((CNT113 + 1))
[ -s "$W113/scan.json" ] || { echo "--- FAIL: §113 扫描器输出空文件（rc=0 但没读数＝判据整段退化成恒绿空循环）"; exit 1; }
# JSON → KEY=VALUE 展平（列表给条数与逗号串；字典列表另给「哪个文件哪一行」的归属串）。
cat > "$W113/flat.py" <<'PYFLAT113'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
rows = []
for k, v in d.items():
    if isinstance(v, dict):
        for kk, vv in v.items():
            rows.append("%s_%s=%s" % (k, kk, vv))
    elif isinstance(v, list):
        rows.append("%s_n=%d" % (k, len(v)))
        if all(isinstance(x, str) for x in v):
            rows.append("%s_list=%s" % (k, ",".join(v)))
        elif v and all(isinstance(x, dict) for x in v):
            # 归属串：反证要断「红在哪一列」，只报条数就退化成「有东西红了」
            rows.append("%s_where=%s" % (k, ",".join("%s:%s" % (x.get("file", x.get("page", "")), x.get("line", "")) for x in v)))
    elif isinstance(v, bool):
        rows.append("%s=%s" % (k, "true" if v else "false"))
    else:
        rows.append("%s=%s" % (k, v))
print("\n".join(rows))
PYFLAT113
SCAN113_KV=$(python3 "$W113/flat.py" "$W113/scan.json" 2>&1)
CNT113=$((CNT113 + 1))
[ -n "$SCAN113_KV" ] || { echo "--- FAIL: §113 展平后一条读数都没有（扫描器输出结构变了，逐键锁全部失去分母）：${SCAN113_KV}"; exit 1; }
# 分母键自身必须先读到数：键名一坏，后面的等值会变成「拿 0 比 0」的恒绿。
key113() { # $1=键 → 该键存在且非空
	CNT113=$((CNT113 + 1))
	local v
	v=$(jget113 "$1")
	[ -n "$v" ] || { echo "--- FAIL: §113 读数缺键（$1 不存在或为空＝扫描器的键名与门禁不同源，两个消费者已经分家）"; exit 1; }
}
for _k in poll_total poll_missing_n catch_total catch_unaccounted_n ledger_pages ledger_files_n floors_pollPages floors_ledgerFiles floors_catchTotal floors_upTokenDefs token_up_n token_down_n token_up_list token_down_list d1_endpoint_hits; do
	key113 "$_k"
done
# 扫的必须是**这个仓**：root 漂移时反证会「扫真仓、破坏副本」，读数与破坏都对不上。
eq113 "$W113/scan.json" "\"web_src\": \"$REPO113/web/src\"" 1 '扫描根＝当前仓（镜像反证另传根，见⑩组）'
# N1：轮询页守卫覆盖面
eq113 "$W113/scan.json" '"poll_missing": []' 1 '§P2-I N1：每个轮询页都接了守卫（非空即红，V1 就是它的反证）'
min113 '§P2-I N2：轮询页派生集合不得缩水（掉下界＝扫描模式失效，而不是页面都修好了）' "$(jget113 poll_total)" "$(jget113 floors_pollPages)"
# P14/P15：catch 对账
eq113 "$W113/scan.json" '"catch_unaccounted": []' 1 '§P2-J P14：台账接入面内 0 处静默吞错（V2 是它的反证）'
min113 '§P2-J P15：catch 语料量级（掉下来说明扫描器坏了，而不是「吞错都修完了」）' "$(jget113 catch_total)" "$(jget113 floors_catchTotal)"
min113 '§P2-J P15：接了台账的页面数（接入面被改窄＝P14 扫不到新页面）' "$(jget113 ledger_pages)" "$(jget113 floors_ledgerFiles)"
CNT113=$((CNT113 + 1))
[ "$(jget113 ledger_files_n)" -gt "$(jget113 ledger_pages)" ] || { echo "--- FAIL: §113 接入面锁 ${CNT113}（红条组件不在台账接入文件集合里＝共用实现落在射程外）"; exit 1; }
CNT113=$((CNT113 + 1))
jline113 "$SCAN113_KV" 'ledger_files_list=' 'components/LoadFailBanner.jsx' || { echo "--- FAIL: §113 接入面内容锁 ${CNT113}（共用红条组件没进台账接入清单：它的 catch 与文案从此没人对账）"; exit 1; }
# M3：D1 通道三段
eq113 "$W113/scan.json" '"d1_api_get": 1' 1 '§P2-H M3：GET 出口恰好一处（两处＝第二个 fetchD1Config 冒出来了）'
eq113 "$W113/scan.json" '"d1_api_post": 1' 1 '§P2-H M3：POST 出口恰好一处'
min113 '§P2-H M3：端点串命中（GET/POST 各一处；开工前实测是 0 命中，那条零命中就是缺陷本体）' "$(jget113 d1_endpoint_hits)" 2
eq113 "$W113/scan.json" '"d1_panel_get": 1' 1 '面板里的读取调用点恰好一处'
eq113 "$W113/scan.json" '"d1_panel_post": 1' 1 '面板里的保存调用点恰好一处'
min113 '§P2-H M3：挂载面板的页面数 ≥1（写了面板没人 render＝通道依旧没人用，§DEADGAUGE 定义没接同族）' "$(jget113 d1_mounted_pages_n)" 1
# P1/P1b：涨跌令牌真值（等值到交付稿，不是「更红就行」的单向锁）
eq113 "$W113/scan.json" '"token_up_warm_all": true' 1 '涨侧令牌全部定义为暖色（R>G）'
eq113 "$W113/scan.json" '"token_down_cool_all": true' 1 '跌侧令牌全部定义为冷色（G>R）'
CNT113=$((CNT113 + 1))
[ "$(jget113 token_up_list)" = "#e34d59,#ff5b63" ] || { echo "--- FAIL: §113 令牌等值锁 ${CNT113}（--app-up 全部定义值应等于交付真值 #e34d59,#ff5b63，实得 $(jget113 token_up_list)）"; exit 1; }
CNT113=$((CNT113 + 1))
[ "$(jget113 token_down_list)" = "#00a870,#2fbf87" ] || { echo "--- FAIL: §113 令牌等值锁 ${CNT113}（--app-down 全部定义值应等于交付真值 #00a870,#2fbf87，实得 $(jget113 token_down_list)）"; exit 1; }
CNT113=$((CNT113 + 1))
[ "$(jget113 token_up_n)" = "$(jget113 floors_upTokenDefs)" ] || { echo "--- FAIL: §113 令牌定义处数等值锁 ${CNT113}（--app-up 实得 $(jget113 token_up_n) 应=$(jget113 floors_upTokenDefs)：少一处＝暗色块落在锁外，多一处＝有人在别处覆盖了涨跌色）"; exit 1; }
CNT113=$((CNT113 + 1))
[ "$(jget113 token_down_n)" = "$(jget113 floors_upTokenDefs)" ] || { echo "--- FAIL: §113 令牌定义处数等值锁 ${CNT113}（--app-down 实得 $(jget113 token_down_n) 应=$(jget113 floors_upTokenDefs)）"; exit 1; }
SNAP113 2

# ── ③ §P2-H M1/M2：后端两条路由与面板接线（前端调用点的两端都得在位）──
eq113 "$SRV113" 's.mux.HandleFunc("GET /api/config/d1", s.adminMiddleware(s.handleGetD1Config))' 1 'M1：GET 路由带 admin 门（面板挂载门槛与它同口径）'
eq113 "$SRV113" 's.mux.HandleFunc("POST /api/config/d1", s.adminMiddleware(s.handleSetD1Config))' 1 'M1：POST 路由带 admin 门'
eqj113 "$API113" 'export async function fetchD1Config()' 1 'M2：GET 出口声明恰好一处'
eqj113 "$API113" "return request('/api/config/d1')" 1 'M2：GET 请求串（斜杠开头＝绝对路径，不是拼出来的相对串）'
eqj113 "$API113" 'export async function setD1Config(cfg)' 1 'M2：POST 出口声明恰好一处'
eqj113 "$API113" "return request('/api/config/d1', { method: 'POST', data: cfg })" 1 'M2：POST 请求（method 必显式，缺省是 GET＝静默读而不是写）'
minj113 "$PNL113" 'api.fetchD1Config(' 1 '面板真调用读取（写了组件不叫接线）'
minj113 "$PNL113" 'api.setD1Config(' 1 '面板真调用保存'
eqj113 "$PNL113" "setLoadState('error')" 1 'M2：读取失败落错误态（唯一的一处，写两遍就会有第三份）'
eqj113 "$PNL113" "if (loadState !== 'loaded') return" 1 'M2：非 loaded 态直接拒保存（error 态禁保存，同 §N-4 战法参数口径）'
eqj113 "$PNL113" "'D1 配置保存失败" 1 'M2：保存失败走可见 toast（静默 catch＝用户以为已经落库）'
eqj113 "$PNL113" "{loadState === 'error' && (" 1 'M2：红条出口恰好一处（只加不减的台账会变成永久红）'
eqj113 "$PNL113" 'setEffectiveSource(typeof cfg.effective_source' 1 'effective_source 回显接上（§0929CFG-D1 的自证字段，前端不显示等于白回）'
eqj113 "$SET113" "const d1Admin = api.getRole() === 'admin'" 1 'M2：挂载门槛与两条 admin 路由同口径（非管理员拿 403 只会得到一块红条噪声）'
eqj113 "$SET113" '{d1Admin && <D1ConfigPanel />}' 1 'M2：面板真被页面 render'
negj113 "$SET113" '{<D1ConfigPanel />}' '无条件挂载形态禁止复活（与后端 adminMiddleware 口径矛盾的门，会让成员看到一块打不开的板）'
SNAP113 3

# ── ④ §P3-FE 着色令牌与单出口（等值到交付真值；旧的反向配对禁止复活）──
eq113 "$STY113" '--app-up: #e34d59;' 1 '亮色涨＝A股红涨（交付真值，等值不是「更红就行」）'
eq113 "$STY113" '--app-down: #00a870;' 1 '亮色跌＝A股绿跌'
eq113 "$STY113" '--app-up: #ff5b63;' 1 '暗色涨（本批实录：只数第一处时暗色块被改反也照样绿）'
eq113 "$STY113" '--app-down: #2fbf87;' 1 '暗色跌'
eq113 "$STY113" '.up { color: var(--app-up); }' 1 '类名侧接线单点：.up 用涨令牌'
eq113 "$STY113" '.down { color: var(--app-down); }' 1 '.down 用跌令牌（反向配对＝整页红绿互换，本条缺陷的原形）'
CNT113=$((CNT113 + 1))
UPDEF113=$(grep -cF -- '--app-up:' "$STY113" 2>/dev/null || true)
[ "${UPDEF113:-0}" = "2" ] || { echo "--- FAIL: §113 令牌定义处数等值锁 ${CNT113}（--app-up 定义应恰好 2 处＝亮/暗各一份，实得 ${UPDEF113}：第三处＝有人在主题块外面覆盖了涨跌色）"; exit 1; }
eqj113 "$RES113" 'export function signColor(v)' 1 'P1：全页正负数值只走这一个出口'
eqj113 "$RES113" "return Number(v) >= 0 ? 'var(--app-up)' : 'var(--app-down)'" 1 'P1：正→红、负→绿（旧实现正好相反，且与同页另一个函数打架）'
eqj113 "$RES113" "if (v === null || v === undefined || isNaN(v)) return 'var(--app-faint)'" 1 '缺数不着色（用弱化色而不是涨跌任一色，P8 同族）'
negj113 "$RES113" 'function signClass' 'P3：同页第二把着色尺子禁止复活（旧 signClass 返回 pos/neg 类名，调用点手动配错色的实录见 Research :1405）'
negj113 "$RES113" ">= 0 ? 'var(--app-down)'" 'P2：正数配跌色（绿）的反向配对禁止复活'
negj113 "$RES113" "win > 0 ? 'var(--app-down)'" 'P2：「胜」配绿禁止复活（同页两套账的头号形态）'
eqj113 "$RES113" "return (n >= 0 ? '+' : '') + (n * 100).toFixed(2) + '%'" 1 'P6：回测超额的唯一格式化出口（toast 与表格同源）'
CNT113=$((CNT113 + 1))
FX113=$(js113 line "$RES113" '* 100).toFixed(2)')
[ "${FX113:-0}" = "1" ] || { echo "--- FAIL: §113 P7 同源等值锁 ${CNT113}（格式化表达式应恰好出现在 1 行，实得 ${FX113}：两处＝toast 与表格各写一遍，正是「回测超额两口径」的原形；尺子坏了也会掉到 0）"; exit 1; }
minj113 "$RES113" 'fmtExcess(' 3 'P6：出口被多处消费（0 处＝单源函数写了没接上）'
minj113 "$RES113" 'signColor(' 5 'P1：着色出口被多处消费'
SNAP113 4

# ── ⑤ §P3-FE 缺数渲染三态（watchlist 的 ¥0.00 与 DepthPanel 的 '--' 染绿）──
eqj113 "$WLS113" "export const WL_NO_DATA_PLACEHOLDER = '—'" 1 'P8：缺测占位串只有一处定义（各页自拼会让判据退化成逐页认文案）'
eqj113 "$WLS113" 'export function PriceCell({ value })' 1 'P8：现价单元格单实现'
eqj113 "$WLS113" 'export function PctCell({ value })' 1 'P8：涨跌单元格单实现'
minj113 "$WLS113" 'wl-no-data' 2 'P8：两列各有一枚缺测标记 testid（少于 2＝某一列又回到渲染 0）'
negj113 "$WLS113" 'Number(wlMap[code]?.price) || 0' 'P9：缺数归零形态禁止复活（0 会被当成「一个真实的股价」渲染成 ¥0.00）'
negj113 "$WLS113" 'Number(s.change_pct) || 0' 'P9：行情刷新腿的归零禁止复活'
negj113 "$WLS113" "'¥' + (row.price || 0).toFixed(2)" 'P9：表格列的 || 0 兜底禁止复活'
negj113 "$WLS113" "(row.change_pct || 0) >= 0 ? 'var(--app-up)'" 'P9：缺数归零后还会被判成「涨」上色——0 与真涨幅同色是本条最坏的可见后果'
eqj113 "$DPT113" "export const DEPTH_NO_DATA = '--'" 1 'P10：盘口缺测占位串单一定义'
eqj113 "$DPT113" 'export function pctState(text)' 1 'P10：三态判据提到模块作用域（等值断言与静态锁才钉得住判据本身）'
eqj113 "$DPT113" "if (!/^[+-]\\d/.test(s)) return 'neutral'" 1 "P10：'--' 与空串一律中性态（旧二态写法把缺测归进 else＝染成跌色）"
eqj113 "$DPT113" 'const pctColor = { up: C.up, down: C.down, neutral: C.lv }' 1 'P10：三态各自的色值映射只在一处'
eqj113 "$DPT113" 'data-pct-state={nowState}' 1 'P10：三态可被测试与门禁读出（渲染态而不是只存在判据里）'
negj113 "$DPT113" "const nowCls = pctText.startsWith('+') ? 'up' : 'down'" 'P11：二态 nowCls 禁止复活（缺测走 else 分支被染绿）'
negj113 "$DPT113" "r.volText && r.volText.startsWith('+') ? C.up : C.down" 'P11：挂单行的二态染色禁止复活'
SNAP113 5

# ── ⑥ §P3-FE 抽屉「实时价配冻结涨幅」：三层优先 + 回落必须标注 ──
eqj113 "$DRW113" 'export function numOrNil(v)' 1 '缺数判据单点（合法 0 保留、空串/非数归 nil）'
eqj113 "$DRW113" 'export function batchPrice(quote)' 1 '「同批」价格判据（>0 才可采信，0 是缺数不是价格）'
eqj113 "$DRW113" 'export function deriveChg(price, prevClose)' 1 '同批自算涨幅的唯一算式（两位小数、prev≤0 一律 nil）'
eqj113 "$DRW113" 'export function resolveDrawerChg(quote, propChangePct)' 1 'P4：涨幅三层优先收敛成一个函数（同批 change_pct → 同批自算 → props 冻结值）'
eqj113 "$DRW113" 'const reading = resolveDrawerChg(quote, changePct)' 1 'P4：组件里只有这一个调用点（就地再写一遍 if 就是第二本账）'
eqj113 "$DRW113" 'const chgFrozen = reading.frozen' 1 'P4：回落态必须带出标记'
minj113 "$DRW113" 'sdd-chg-frozen' 1 'P4：冻结标注 testid 在位'
minj113 "$DRW113" '（开抽屉时刻值）' 1 'P4：标注文案（只有角标没有文案＝用户不知道这个数字是哪一刻的）'
negj113 "$DRW113" 'const rawChg = changePct' 'P5：涨幅只看 props 冻结值的旧形态禁止复活（价随轮询刷新、涨幅不刷＝价涨背离）'
negj113 "$DRW113" 'Number.isFinite(Number(quote.price))' 'P5：就地判价格（绕过 batchPrice）禁止复活'
# 后端同源腿：抽屉自算要的是**同批**昨收，取不到行情时三个键必须一起归零（半份读数比没读数更坏）。
eq113 "$HND113" '"change_pct": r2(info.ChangePct)' 1 'M3 同源：lookup 回 change_pct（同批涨幅的第一优先来源）'
eq113 "$HND113" '"prev_close": r2(prev)' 1 'M3 同源：回 prev_close 供自算，昨收缺时回落 Close'
eq113 "$HND113" '"price": 0, "change_pct": 0, "prev_close": 0' 1 '取不到行情时三键一起归零（前端 batchPrice 据此判「不是同批读数」而不是当成 0 价）'
SNAP113 6

# ── ⑦ §P2-I 守卫接线：唯一正确写法 + 假绿形态负锁（跨 pages/components 派生求和）──
eqj113 "$SGD113" 'export function useStaleGuard()' 1 'N1：hook 入口恰好一处定义（页面只剩一个调用点）'
eqj113 "$SGD113" 'if (!ref.current) ref.current = createStaleGuard()' 1 'N1：惰性初始化（代号序列跨渲染稳定）'
CNT113=$((CNT113 + 1))
FAKE113=$(js113 many "web/src/pages web/src/components" 'useRef(createStaleGuard())')
case "$FAKE113" in
*Error*|*error*) echo "--- FAIL: §113 跨目录尺子没出数：${FAKE113}"; exit 1 ;;
esac
[ "${FAKE113:-0}" = "0" ] || { echo "--- FAIL: §113 假绿形态负锁 ${CNT113}（pages/components 的代码行里出现 useRef(createStaleGuard()) ${FAKE113} 处：每渲染 new 一个守卫、代号重置、isStale 永远 false＝守卫恒绿）"; exit 1; }
CNT113=$((CNT113 + 1))
IMP113=$(grep -rlF "utils/staleGuard.js" web/src/pages web/src/components 2>/dev/null | grep -c . || true)
min113 '§P2-I N2：import 守卫的文件数（派生而不是点名；低于轮询页下界＝有人把整批 import 删了）' "$IMP113" "$(jget113 floors_pollPages)"
eqr113 "$RES113" 'isStale\(t' 10 'N4：判污调用点在位（只 begin 不 isStale＝半边守卫；2026-10-09 预演实测 10 处＝Research 一页多个守卫实例各自的判污点，改名或删调用即红）'
SNAP113 7

# ── ⑧ §P2-J 台账与红条：单实现 testid + 派生接入面 + Paper 六枚角标 ──
eqj113 "$LGD113" "export const LOAD_LEDGER_TESTID = 'load-ledger'" 1 'O1：testid 常量只有一处（各页自拼会让「红条在位」锁退化成逐页认文案）'
eqj113 "$LFB113" 'export default function LoadFailBanner({ fails, page })' 1 'O1：红条单实现'
eqj113 "$LFB113" 'data-testid={LOAD_LEDGER_TESTID}' 1 'O1：红条只认这一个常量'
LEDGER113_LIST=$(jget113 ledger_files_list)
CNT113=$((CNT113 + 1))
[ -n "$LEDGER113_LIST" ] || { echo "--- FAIL: §113 接入面清单为空（下面的逐页循环会一次都不走，「红条在位」就此变成假锁）"; exit 1; }
# 扫描器回的接入面清单是 **web/src 相对**路径（它整个射程就建立在 web/src 根上），
# 而本段的尺子 js113 一律吃**仓库根相对**路径（与段头变量表同源）。这里在派生点一次性换根，
# 而不是在每个循环里各拼一次——两处拼法的结局是第三处忘了拼然后 ENOENT 被当成「没红条」。
IFS=',' read -r -a LEDGER113_RAW <<< "$LEDGER113_LIST"
LEDGER113=()
for _f in "${LEDGER113_RAW[@]}"; do
	LEDGER113+=("web/src/$_f")
done
BANNER113=0
for _f in "${LEDGER113[@]}"; do
	case "$_f" in
	web/src/components/LoadFailBanner.jsx) continue ;;
	esac
	_c=$(js113 line "$_f" '<LoadFailBanner ')
	CNT113=$((CNT113 + 1))
	case "$_c" in
	''|*[!0-9]*) echo "--- FAIL: §113 接入面循环尺子没出数（${_f}「<LoadFailBanner 」实得「${_c}」）——尺子坏了不算通过，红条在位与否无从判"; exit 1 ;;
	esac
	[ "${_c:-0}" -ge 1 ] || { echo "--- FAIL: §113 接入面等值锁 ${CNT113}（$_f import 了台账却没 render 红条：失败记了账而用户看不见，正是本条缺陷的原形）"; exit 1; }
	BANNER113=$((BANNER113 + 1))
done
key113 floors_ledgerFiles
[ "$BANNER113" -ge "$(jget113 floors_ledgerFiles)" ] || { echo "--- FAIL: §113 接入面循环枚数锁（逐页验红条只跑了 ${BANNER113} 页，应≥$(jget113 floors_ledgerFiles)：循环没走到＝清单派生坏了，锁恒绿）"; exit 1; }
# 各页不许自拼红条 testid（第二本 testid 会让「红条在位」数不到那一页）。
CNT113=$((CNT113 + 1))
SELF113=$(js113 many "web/src/pages" 'load-ledger')
[ "${SELF113:-0}" = "0" ] || { echo "--- FAIL: §113 testid 唯一性负锁 ${CNT113}（pages 里还有 ${SELF113} 处自拼 load-ledger：判据得逐页认文案，加一页改一次锁）"; exit 1; }
# Paper 的四腿装载与角标（O1 的产品形状）。
eqj113 "$PAP113" 'function loadPaperLeg(' 1 'O1：四条腿的唯一装载器（各腿自己 try/catch＝第五处吞错只是时间问题）'
eqr113 "$PAP113" 'await loadPaperLeg\(token, ' 4 'O1：四条腿各走唯一装载器（持仓/成交/委托/净值）'
minj113 "$PAP113" "legFailFlag('" 6 'O1：按腿角标调用点 ≥6（实跑 6 处：持仓/成交/委托/净值曲线/战法开关清单/撮合配置）'
eqj113 "$PAP113" 'paper-kpi-mismatch' 1 'O2：KPI 与三张表不同源时的说明恰好一处'
eqj113 "$PAP113" 'Number(activeStats.win_rate_pct).toFixed(1)' 1 'P12：胜率保留一位小数（亚单位不取整，owner 裁决⑧）'
negj113 "$PAP113" 'win_rate_pct).toFixed(0)' 'P13：99.6% 显示成 100% 的取整形态禁止复活（胜率是把几胜几负折成一个数的判据，取整后 100% 冒充零亏损）'
minj113 "$PAP113" 'paper-win-rate' 1 'P12：胜率格整格挂 testid（只框数字会让「/ N仓」落在断言外）'
# 空吞形态在接入面内必须清零（旧四腿的 `} catch (_) {}` 不许复活）。
CNT113=$((CNT113 + 1))
SWALLOW113=0
for _f in "${LEDGER113[@]}"; do
	_c=$(js113 line "$_f" '} catch (_) {}')
	case "$_c" in
	''|*[!0-9]*) echo "--- FAIL: §113 空吞循环尺子没出数（$_f 实得「${_c}」）——读数为空的「0 处空吞」是假的"; exit 1 ;;
	esac
	SWALLOW113=$((SWALLOW113 + _c))
done
[ "$SWALLOW113" = "0" ] || { echo "--- FAIL: §113 空吞负锁 ${CNT113}（台账接入面内仍有 ${SWALLOW113} 处 } catch (_) {}：失败什么都不留，运维只能靠「今天怎么没数据」反推链路坏了）"; exit 1; }
SNAP113 8

# ── ⑨ 行为腿：波 6 的前端测试文件清单由目录**派生**，文件数与实跑等值对账 ──
# 写死「四个文件」的后果和第 31 探针那次一样：加第五份测试（本批的 p2h_d1_panel）时锁看不见它，
# 于是新那份永远在锁外——所以清单从 web/src/__tests__ 派生，并钉一条「派生清单 <5 即红」的正锁。
W6T113=$(cd web/src/__tests__ && ls | grep -E '^(p2h_|p3fe_|p6_derived)' | sed -E 's/\.test\.(js|jsx)$//' | LC_ALL=C sort) || true
W6N113=$(printf '%s\n' "$W6T113" | grep -c . || true)
min113 '波 6 行为腿清单派生为空/过短（正则失效或测试被整批删，本段就只剩静态锁）' "$W6N113" 5
MISS6113=""
for _t in $W6T113; do
	ls web/src/__tests__/"$_t".test.* >/dev/null 2>&1 || MISS6113="$MISS6113 $_t"
done
CNT113=$((CNT113 + 1))
[ -z "$MISS6113" ] || { echo "--- FAIL: §113 行为腿点名失效（清单派生自目录却仍缺文件，说明有半截文件）：${MISS6113}"; exit 1; }
W6RUN113=$( cd web && NO_COLOR=1 npm test -- $W6T113 2>&1 | strip_ansi113 || true )
CNT113=$((CNT113 + 1))
printf '%s\n' "$W6RUN113" | grep -qE 'Test Files +'"$W6N113"' passed' \
	|| { echo "--- FAIL: §113 波 6 行为腿没跑满派生清单（应见「Test Files $W6N113 passed」）："; printf '%s\n' "$W6RUN113" | grep -E 'Test Files|Tests |FAIL|Cannot find|Error' | head -15; exit 1; }
CNT113=$((CNT113 + 1))
if printf '%s\n' "$W6RUN113" | grep -qE ' failed'; then
	echo "--- FAIL: §113 波 6 行为腿判红："
	printf '%s\n' "$W6RUN113" | grep -E 'FAIL|✗|×|AssertionError|Unable to find' | head -20
	exit 1
fi
W6T_N113=$(printf '%s\n' "$W6RUN113" | sed -n 's/.*Tests  *\([0-9][0-9]*\) passed.*/\1/p' | head -1)
min113 '波 6 行为腿用例条数（文件数对上而用例掉到个位数＝整文件被 skip，与 §112 的防空转同族）' "${W6T_N113:-0}" 40
SNAP113 9

# ── ⑩ 镜像反证：/tmp 副本树（源文件零改动），先自证镜像基线 == 真仓读数 ──
mkdir -p "$MIR113/scripts" "$MIR113/web/node_modules" || { echo "--- FAIL: §113 建不出镜像骨架"; exit 1; }
cp "$REPO113/$SCN113" "$MIR113/scripts/" || { echo "--- FAIL: §113 镜像缺扫描器（反证没有尺子）"; exit 1; }
for _e in "$REPO113"/web/*; do
	_b=$(basename "$_e")
	case "$_b" in
	node_modules) continue ;;
	esac
	cp -R "$_e" "$MIR113/web/" || { echo "--- FAIL: §113 镜像复制 web/$_b 失败"; exit 1; }
done
# node_modules 逐条目软链：整体软链会让镜像把 vite 依赖缓存写进**真仓**的 node_modules/.vite，
# 于是反证与真仓互相污染（下一次真跑的读数就不干净了）；.vite 不进软链，让镜像自己建。
for _e in "$REPO113"/web/node_modules/* "$REPO113"/web/node_modules/.[!.]*; do
	[ -e "$_e" ] || continue
	_b=$(basename "$_e")
	case "$_b" in
	.vite) continue ;;
	esac
	ln -s "$_e" "$MIR113/web/node_modules/$_b" || { echo "--- FAIL: §113 镜像软链 node_modules/$_b 失败"; exit 1; }
done
cat > "$W113/mutate.py" <<'PYMUT113'
import sys
# 用法：mutate.py <绝对文件> <old> <new> —— 整串替换并打印落地次数（不是 1 由调用方判红）
p, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(p, encoding="utf-8").read()
n = s.count(old)
if n:
    open(p, "w", encoding="utf-8").write(s.replace(old, new))
print(n)
PYMUT113
scan_mirror() { # 在镜像根上跑扫描器 → KEY=VALUE（root 走参数＝破坏的是副本，读的也是副本）
	node "$MIR113/scripts/fe_contract_scan.mjs" "$MIR113/web/src" > "$W113/mscan.json" 2>"$W113/mscan.err" || return 1
	python3 "$W113/flat.py" "$W113/mscan.json" 2>&1
}
mirror_vitest() { # $1=测试文件名（不含扩展名）→ 打印全文（已剥色，尺子读的是干净文本）
	( cd "$MIR113/web" && ./node_modules/.bin/vitest run "src/__tests__/$1.test."* 2>&1 ) | strip_ansi113
}
# 这里把「跑不起来」拆成两种现形：node 自身的 stderr、以及展平脚本读到空 JSON 时的报错。
# 之前只回显 mscan.err 的那版会把「扫描器安静地没打印」（/tmp 符号链接骗过入口判据那种）说成
# 「扫描器坏了」，而真相在 python 那侧——读数原文必须一起带出来才归属得清。
MIR_BASE=$(scan_mirror) || { echo "--- FAIL: §113 镜像扫描器没有带回读数（node stderr＋展平原文如下）"; tail -3 "$W113/mscan.err"; printf '%s\n' "$MIR_BASE" | tail -5; exit 1; }
kgetb() { printf '%s\n' "$MIR_BASE" | sed -n "s/^$1=//p" | head -1; }
CNT113=$((CNT113 + 1))
[ "$(kgetb poll_total)" = "$(jget113 poll_total)" ] \
	|| { echo "--- FAIL: §113 镜像基线与真仓读数不等值（poll_total 镜像=$(kgetb poll_total) 真仓=$(jget113 poll_total)）——镜像不可信时，任何「破坏后变红」都不构成证据"; exit 1; }
CNT113=$((CNT113 + 1))
[ "$(kgetb catch_total)" = "$(jget113 catch_total)" ] \
	|| { echo "--- FAIL: §113 镜像基线 catch_total 与真仓不等值（镜像 copy 少了一棵目录树，反证会「红了也不知道红在哪」）"; exit 1; }
CNT113=$((CNT113 + 1))
[ "$(kgetb token_up_list)" = "$(jget113 token_up_list)" ] \
	|| { echo "--- FAIL: §113 镜像基线令牌读数与真仓不等值（styles.css 没被整份复制进镜像）"; exit 1; }
BASE6113="$(mirror_vitest p3fe_paper_legfail)
$(mirror_vitest p3fe_drawer_stale)"
CNT113=$((CNT113 + 1))
[ "$(printf '%s\n' "$BASE6113" | grep -c 'Test Files  1 passed')" = "2" ] \
	|| { echo "--- FAIL: §113 镜像内 vitest 基线不是「两份各自绿」（node_modules 软链或 vitest 配置在镜像里坏了），尾部："; printf '%s\n' "$BASE6113" | grep -E 'Test Files|Errors|failed|Cannot' | head -10; exit 1; }
echo "ok - §113 镜像基线自证（扫描器三键与真仓等值 + 两文件 vitest 在镜像里各自绿）"

dys113_scan() { # $1=编号 $2=镜像相对文件 $3=old $4=new $5=读数键 $6=变异后期望 $7=基线期望 $8=归属必含串（可空） $9=归属读数键（缺省＝${5}）
	# 条数键与归属键常常不是同一行：翻转的是 poll_missing_n（0→1），而「是哪一个文件缺的」写在 poll_missing_list 上。
	# 只按 $5 那一行找归属串的话，V1 会因为「n 那行没有文件名」判红在别处——那是尺子的形状错了，不是反证没牙。
	local id="$1" rel="$2" old="$3" new="$4" key="$5" want="$6" base="$7" token="$8" akey="${9:-$5}" applied out got mutgot113
	CNT113=$((CNT113 + 1))
	cp -f "$REPO113/$rel" "$MIR113/$rel" || { echo "--- FAIL: §113 反证 ${id} 无法从主仓复位镜像文件 $rel"; exit 1; }
	if [ -n "$token" ] && printf '%s\n' "$MIR_BASE" | grep "^$akey" | grep -qF -- "$token"; then
		echo "--- FAIL: §113 反证 ${id} 的归属串在镜像**基线**里就已经存在（${token}）——「红了且文案里有它」是免费的，这枚反证不可归属"
		exit 1
	fi
	applied=$(python3 "$W113/mutate.py" "$MIR113/$rel" "$old" "$new" 2>&1 | tail -1)
	if [ "$applied" != "1" ]; then
		echo "--- FAIL: §113 反证 ${id} 变异落地数=${applied}，期望恰好 1（0＝镜像里没找到目标串，这枚等于没跑；>1＝命中面比预期宽，红了也不知道红在哪）"
		exit 1
	fi
	out=$(scan_mirror) || { echo "--- FAIL: §113 反证 ${id} 变异后镜像扫描器跑不起来（harness 坏了不是锁有牙）：$(tail -3 "$W113/mscan.err")"; exit 1; }
	got=$(printf '%s\n' "$out" | sed -n "s/^$5=//p" | head -1)
	if [ "$got" != "$want" ]; then
		echo "--- FAIL: §113 反证 ${id} 没有让读数翻转（$5 期望「${want}」实得「${got}」）——这条锁恒绿，是假锁"
		printf '%s\n' "$out" | grep -E "^$5" | head -3
		exit 1
	fi
	if [ -n "$token" ] && ! printf '%s\n' "$out" | grep "^$akey" | grep -qF -- "$token"; then
		echo "--- FAIL: §113 反证 ${id} 读数翻了，但归属键 ${akey} 的读数行里没有本枚指定的串「${token}」（＝红在别处，成因归属不成立）"
		printf '%s\n' "$out" | grep -E "^$akey" | head -3
		exit 1
	fi
	mutgot113="$got" # 变异那一刻的读数先留底：收尾那行要印的是它，不是复位后的基线（把「翻成 0」打在翻成 1 的枚上＝收尾账在撒谎）
	cp -f "$REPO113/$rel" "$MIR113/$rel" || { echo "--- FAIL: §113 反证 ${id} 复位失败（${rel}）"; exit 1; }
	out=$(scan_mirror) || { echo "--- FAIL: §113 反证 ${id} 复位后镜像扫描器跑不起来"; exit 1; }
	got=$(printf '%s\n' "$out" | sed -n "s/^$5=//p" | head -1)
	[ "$got" = "$base" ] || { echo "--- FAIL: §113 反证 ${id} 复位后 $5 没回到基线「${base}」（实得「${got}」）＝镜像被别处污染，后续反证读数全部作废"; exit 1; }
	echo "ok - §113 反证 ${id}：破坏 $rel → $5 翻成「${mutgot113}」且复位回基线（${base}）"
}
dys113_vitest() { # $1=编号 $2=镜像相对文件 $3=old $4=new $5=测试文件名 $6=红文案必含串
	local id="$1" rel="$2" old="$3" new="$4" tf="$5" token="$6" applied out rc
	CNT113=$((CNT113 + 1))
	cp -f "$REPO113/$rel" "$MIR113/$rel" || { echo "--- FAIL: §113 反证 ${id} 无法从主仓复位镜像文件 $rel"; exit 1; }
	applied=$(python3 "$W113/mutate.py" "$MIR113/$rel" "$old" "$new" 2>&1 | tail -1)
	if [ "$applied" != "1" ]; then
		echo "--- FAIL: §113 反证 ${id} 变异落地数=${applied}，期望恰好 1（0＝目标串在镜像里没有，产品代码可能已被改名；>1＝命中面比预期宽）"
		exit 1
	fi
	rc=0
	out=$(mirror_vitest "$tf" 2>&1) || rc=$?
	if [ "$rc" = "0" ]; then
		echo "--- FAIL: §113 反证 ${id} 没有让 $tf 变红（这条测试腿是假绿——破坏产品代码它照样通过）"
		printf '%s\n' "$out" | tail -12
		exit 1
	fi
	if ! printf '%s\n' "$out" | grep -qF -- "$token"; then
		echo "--- FAIL: §113 反证 ${id} 红了，但红文案里找不到本枚指定的归属串「${token}」（＝红在别处，成因归属不成立）"
		printf '%s\n' "$out" | grep -E 'FAIL|AssertionError|Unable to find|×' | head -10
		exit 1
	fi
	cp -f "$REPO113/$rel" "$MIR113/$rel" || { echo "--- FAIL: §113 反证 ${id} 复位失败（${rel}）"; exit 1; }
	out=$(mirror_vitest "$tf" 2>&1) || { echo "--- FAIL: §113 反证 ${id} 复位后仍红＝镜像被别处污染，后续反证读数全部作废"; printf '%s\n' "$out" | tail -12; exit 1; }
	echo "ok - §113 反证 ${id}：破坏 $rel → $tf 红（归属串 ${token}）且复位复绿"
}
# V1 摘掉一页的守卫 import ⇒ N1 的派生集合必须点名它（覆盖面锁的牙齿，不是点名清单）。
dys113_scan V1 web/src/pages/MsgCenter.jsx "import { useStaleGuard } from '../utils/staleGuard.js' // §P2-I 轮询后到丢弃（统一 hook）" "" poll_missing_n 1 0 MsgCenter.jsx poll_missing_list
# V2 把一处可见错误态换成 void ⇒ P14 对账必须抓到，归属点名到那一页那一行。
dys113_scan V2 web/src/pages/Hotspot.jsx "markLoadFail('交易时段状态', err && err.message)" "void err" catch_unaccounted_n 1 0 'pages/Hotspot.jsx:' catch_unaccounted_where
# V3 把暗色主题的涨令牌改成冷色 ⇒ 「全部定义为暖」判据必须红（只数第一处的旧尺子会照样绿）。
dys113_scan V3 web/src/styles.css "--app-up: #ff5b63;" "--app-up: #2fbf87;" token_up_warm_all false true '#e34d59,#2fbf87' token_up_list
# V4 摘掉页面挂载 ⇒ M3 的挂载页集合必须归零（面板写了没人 render＝通道依旧没人用）。
dys113_scan V4 web/src/pages/Settings.jsx '{d1Admin && <D1ConfigPanel />}' "" d1_mounted_pages_n 0 1 ''
# V5 把 GET 出口改名 ⇒ api 段命中归零（面板调用还在：三段各数一处不是冗余，是三条不同的腿）。
dys113_scan V5 web/src/api/index.js 'export async function fetchD1Config()' 'export async function fetchD1ConfigV2()' d1_api_get 0 1 ''
# V6 Paper 摘掉持仓腿角标 ⇒ 行为腿必须红在「找不到那枚 testid」上（O1 的可见性）。
dys113_vitest V6 web/src/pages/Paper.jsx "{legFailFlag('持仓')}" "" p3fe_paper_legfail 'paper-leg-fail-持仓'
# V7 抽屉涨幅改回 props 冻结值 ⇒ 抽屉行为腿必须红（P4b 那条「同批自算」的用例）。
dys113_vitest V7 web/src/components/StockDetailDrawer.jsx 'const reading = resolveDrawerChg(quote, changePct)' 'const reading = { price: null, chg: Number(changePct) || 0, live: false, frozen: false, up: (Number(changePct) || 0) >= 0 }' p3fe_drawer_stale 'P4b'
DYS113_N=$(grep -cE '^dys113_(scan|vitest) V' scripts/verify_changes.sh || true)
CNT113=$((CNT113 + 1))
[ "${DYS113_N:-0}" -ge 7 ] || { echo "--- FAIL: §113 反证枚数派生异常（读到 ${DYS113_N}，应 ≥7＝点名模式失效或反证被删，收尾分组读数不可信）"; exit 1; }

# ── 覆盖面诚实账（不判红，只把「今天还落在锁外面的东西」如实记下来）──
ALL_CATCH113=$(js113 many "web/src/pages web/src/components" 'catch (')
CNT113=$((CNT113 + 1))
[ "${ALL_CATCH113:-0}" -ge 100 ] || { echo "--- FAIL: §113 覆盖面账派生异常（全仓 catch 代码行读到 ${ALL_CATCH113}，应 ≥100＝many 尺子坏了，上面几枚跨目录锁的读数也一起不可信）"; exit 1; }
echo "INFO - §113 覆盖面账：pages/components 的 catch 代码行共 ${ALL_CATCH113} 处，本段只对账台账接入面内的 $(jget113 catch_total) 处（其余落在锁外是**射程取舍**：硬扫全仓会在尚未接入的页面恒红，那种锁没人敢留）；EventSource 特征在 pages 0 命中（SSE 由 api 层单点持有，App 建连后走 sseBus 分发）；Research 的 heatColor（背景热力量表）与 signColor（文字涨跌色）同页并存、正负方向不一致，待 owner 裁决；Paper 的账户状态主腿没有独立角标，可见性由红条 + paper-kpi-mismatch 两处承担（见 p3fe_paper_legfail 的 O1b 注释）。"

# ── 分组自证（本段最后两道锁）──
CNT113=$((CNT113 + 1))
_EMPTY_H=""
for _g in 1 2 3 4 5 6 7 8 9; do
	_v="H${_g}_N"
	if [ "${!_v}" -le 0 ]; then _EMPTY_H="${_EMPTY_H} ${_g}"; fi
done
[ -z "$_EMPTY_H" ] || { echo "--- FAIL: §113 分组快照在位锁 ${CNT113}（这些组一个判定点都没记到：组${_EMPTY_H}）——漏一处 SNAP113 时那组的锁会被并进邻组读数，累计数正常而分组数是假的"; exit 1; }
SNAP113 10
SUMH113=$((H1_N + H2_N + H3_N + H4_N + H5_N + H6_N + H7_N + H8_N + H9_N + H10_N))
CNT113=$((CNT113 + 1))
[ "$SUMH113" = "$((CNT113 - 1))" ] || { echo "--- FAIL: §113 分组求和自证锁 ${CNT113}（十组快照之和 ${SUMH113} != 累计判定点扣本锁 $((CNT113 - 1))）——有判定点没落进任何一组，收尾的覆盖面读数不可信"; exit 1; }
rm -rf "$W113"
echo "ok - §113 全段通过：① 单实现扫描器与两消费者接线 ${H1_N} 道 + ② 扫描器读数逐键（N1/N2/P14/P15/M3/P1）${H2_N} 道 + ③ D1 通道两端接线 ${H3_N} 道 + ④ 涨跌令牌等值与着色单出口 ${H4_N} 道 + ⑤ 缺数渲染三态 ${H5_N} 道 + ⑥ 抽屉三层优先与冻结标注 ${H6_N} 道 + ⑦ 守卫接线与假绿形态负锁 ${H7_N} 道 + ⑧ 台账接入面派生对账（逐页 ${BANNER113} 枚）${H8_N} 道 + ⑨ 行为腿（派生清单 ${W6N113} 个文件 / ${W6T_N113} 条用例）${H9_N} 道 + ⑩ 镜像基线自证与 ${DYS113_N} 枚反证 V1–V${DYS113_N} ${H10_N} 道，累计判定点 ${CNT113}（其中十组快照之和 ${SUMH113}，另有 1 道就是求和自证锁本身）"
echo ""

echo "==> 114 §W7-A…§W7-G 波 7 低危卫生（2026-10-06 修复批）：窗口判据真接线、启动退出姿势、状态映射 fail-closed、量纲抽检两表分离、路由器落盘、守卫文案分诊、兜底可见——静态锁 + 五组行为腿 + 九枚镜像反证..."

# 本段守波 7 的七条（10-06 修复批按 docs/FIX_PLAN_20261006.md 波 7 落码，10-09 收尾）。
# 波 7 的标题是「低危卫生」，但它守的东西一类是**资金谓词**、一类是**承诺与实现分家**：
#
#  ① §W7-A trigger.Config.Sec 定义了、注释按它调、代码里没有窗口。旧 tickState 只存「上一帧」
#     四个标量，所谓「窗口内秒均涨幅」实际是相邻两帧差分（5s 一帧，间隔 ≤60s 都照算——断流十分钟
#     恢复后第一帧会把 600 秒摊成分母）。owner 裁决⑥按「实装优先禁删除」把 Sec 接进窗口计算，
#     不是把注释改掉。本组的判据形状是「分母必须是窗口起点到当前帧的真实跨度」+「Sec 必须出现在
#     剪窗表达式里」，并把旧形态（prevPrice / dt 分母）钉成负锁——注释里那句「窗口」如果代码没接，
#     调 Sec 的人永远在调一个不存在的旋钮。
#  ② §W7-B cmd/quant 的启动期 log.Fatalf 在 defer 链**之外**终结进程：§0927AUDIT-D4 已经把停机
#     路径的 os.Exit(0) 换成「return → defer」，启动路径留着同一个洞（采集器/行情馈线/新闻代理的
#     Stop、状态文件句柄 Close 全被跳过）。改法是把进程体搬进 run() int，main 只按退出码 os.Exit。
#     fail-fast 语义一条没减（端口占用/staging 残留生产配置照拒），变的是退出姿势。判据取「代码行
#     里 log.Fatalf 命中数为 0」——这三处 Fatalf 在说明注释里都还要出现（解释改了什么），
#     拿全文 grep 数会红在自己的话术上，所以统一走 §113 那把 codeOnly 尺子（第三消费者）。
#  ③ §W7-C 网关把「读不懂的委托状态码」统一伪造成「已报」。这不是保守而是伪造：已报在决策侧是
#     三本资金账的**入口谓词**（买入冻结 / 跨日降废 / 在途卖量按精确中文串匹配），于是一条柜台从未
#     确认的回报会先占住当日买入预算、再在跨日被写成终态「废单」。现改 fail-closed：未登记/缺失/
#     非数字一律「未知(原始值)」，并计数落到处理器属性上、由 /admin/status 的 unknown_status 段回显
#     （「未知…」不进资金账 ≠ 不用管，而是必须有人看见）。判据形状＝前缀常量的**单实现 + 三消费者**
#     （映射、计数、观察位）等值，加一枚「旧 fail-open 那一行」的行尾锚负锁——它在 _status 的
#     文档串里作为「缺陷原文」被引用，全文 grep 会把说明当实现（§107 预演实录同族）。
#  ④ §W7-D ths_daily.amount 的口径此前由**互相矛盾的两处注释**担保（同一列一处写「换手率（%）」、
#     一处写「成交额/换手率」），而同花顺日 K 导入把该列原样写进 amount。取向：注释矛盾时不能挑一个
#     当事实，改由读数担保——抽检尺子从「只量 daily」抬成按 store.AmountProbedTables 量多表，
#     与 daily 同一个判定体、同一组带宽；同时把它**挡在写侧换算白名单外**（data.AmountScaledTables
#     只收上游单位核实过的表，THS 没有核实记录，把猜测的 ×1000 写进库里比矛盾注释更难回滚）。
#     本组锁面因此是**两个集合的分离**：可抽检 ≠ 可自动换算；ths_daily 必须在抽检集合、必须在
#     换算白名单之外。两条现网腿（第 30 探针 + 夜间 scale 腿）同步加宽到两张表，但**判数不动**
#     （七腿齐备与 `PASS -lt 7` 由 §106 单实现守着，本段只断它没被抬成第 8 腿）。
#  ⑤ §W7-E 告警路由器头注释一直写着「进程停机则重启后首个 tick 补发（按记录的日期标注）」，
#     而 day/daily/pendingR/announced 四份状态全在内存——那句承诺是空的：停机跨过日切就把那一天
#     永久吞掉；更糟的是 announced 丢了之后，重启再收到 recover 会被当「没报过」吞掉销案，
#     那条 p1 从此只有开没有销。owner 裁决②要求做持久化。落点选择上偏离了修复单原文的
#     「sqlite/opslog」：metrics 是被 notify 的兄弟层共用的度量面，引 store 会把指标评估焊死在
#     推送/落库实现上（还带来 engine→metrics→store→engine 的注入环风险），故照 notify/outbox.go
#     的同形姿势走 dataDir 下的 JSON + fileutil.AtomicWrite，失败可见只借 opslog 叶节点的 DayOnce。
#     这一条最重要的判据是**注释-实现等值**：声称「重启后补发」的次数 ≥1 ⇒ 装配期必须有
#     SetAlertRouterStatePath 的调用点（缺即红）。落盘写在 Route 内部而不是交给调用方「记得调」，
#     且用整份快照的字节比较决定要不要写——脏标记要在四个变更点各记一次，漏一个就静默丢；
#     文件 IO 一律在 mu 之外（多账号引擎共用这一个进程单例，落盘由 saveMu 串成单写者）。
#  ⑥ §W7-F place_qmt_bridge 的「本机源文件必须与 HEAD 一致」守卫原先只看 `git diff --quiet` 的
#     退出码非 0，把三种失效形态压成一句「有未提交改动」：0=无差异、1=确有改动、≥2=问不成，
#     再加上「根本不是仓库」和「文件没进版本控制」两种特例。后果不是安全阀失效（三种都 exit 1），
#     而是**读数撒谎**：git 坏掉的机器（safe.directory / index.lock 残留 / 拷树没 git init）会让人
#     去提交一个并不存在的改动，而未纳管的文件在旧写法里 rc=0 直接放行——那才是这条守卫原本要挡的。
#     判据取三态文案各恰一枚 + 前提判据早于派生判据的**顺序锁**（顺序倒过来＝拿问不成的读数当前提）。
#  ⑦ §W7-G 持仓页实盘总盈亏在网关成交汇总未回来时按持仓浮动本地兜底。owner 裁决③：不删兜底，
#     把口径说清 + 让兜底可见。身份判据必须与兜底分支**同一个条件**派生（两枚就会分家：数还是兜底
#     数、标记却亮在权威值上），标记文案要点名两条口径之差（否则标记只是装饰，而这条缺陷的本体
#     就是「看不出这是哪个口径」）。
#
#  ★ 本段与 §113 共用同一把剥注释尺子（scripts/fe_contract_scan.mjs 的 codeOnly），门禁内不出现
#     第二把；Go/Python/JS 的「代码行 vs 说明文字」判据一律走它。
#  ★ 反证九枚 V1–V9 全在 /tmp 镜像树上做（Go 树用软链 + 只把被改包实体化，Python/web 各建副本），
#     镜像先做**基线自证**：五组测试在镜像里各自全绿——镜像不可信时，任何「破坏后变红」
#     都不构成证据（§112/§113 同族）。每枚变异落地数必须恰好 1，归属串必须是基线里不存在的串，
#     并断「本枚独有的那条用例红、别的那条仍绿」，否则一枚破坏会洗出九枚红、读不出归属。
#
#  English: §114 locks wave 7 (low-severity hygiene that touches money predicates). Sec now bounds a
#  real window; startup aborts return an exit code so the defer chain unwinds; unreadable order status
#  codes fail closed to 未知(raw) and become visible on /admin/status; the amount-caliber probe covers
#  daily AND ths_daily while ths_daily stays outside the conversion whitelist; the alert router's
#  "replay after restart" promise is backed by a journaled state rehydrated at assembly time; the
#  bridge placement guard distinguishes "repo unreadable" from "real uncommitted changes"; the
#  positions fallback announces its caliber. Nine /tmp-mirror reversals prove the locks bite.
CNT114=0
REPO114="$PWD"
TRG114=internal/trigger/trigger.go
TRGT114=internal/trigger/trigger_test.go
MAIN114=cmd/quant/main.go
STG114=cmd/quant/staging_test.go
HND114=qmt_gateway/handler.py
GWY114=qmt_gateway/gateway.py
PYP114=qmt_gateway/tests/test_xt_mapping.py
PBP114=internal/store/amount_scale_probe.go
AML114=cmd/dataload/amount_check.go
ASY114=cmd/dataload/hithink_sync.go
ASW114=internal/data/amountscale.go
ADP114=internal/data/hithink_dump.go
VDP114=scripts/verify_deploy_guangzhou.sh
VNG114=scripts/verify_nightly_guangzhou.sh
RTG114=internal/metrics/alert_routing.go
RST114=internal/metrics/alert_router_state.go
RSTT114=internal/metrics/alert_router_state_test.go
ART114=internal/metrics/alert_routing_test.go
BRG114=scripts/place_qmt_bridge.sh
POS114=web/src/pages/Positions.jsx
PGT114=web/src/__tests__/w7g_positions_pnl_fallback.test.jsx
GS114=scripts/verify_changes.sh
W114="$(mktemp -d /tmp/w7gate-XXXXXX 2>/dev/null || true)"
[ -n "$W114" ] || { echo "--- FAIL: §114 建不出镜像目录，五组行为腿与九枚反证无法跑（宁可红，不许跳）"; exit 1; }

eq114() { # $1=文件 $2=整串 $3=预演读数 $4=说明
	CNT114=$((CNT114 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §114 整串等值锁 ${CNT114}（${4}）：${1} 整串「${2}」got=${got:-0} 预演=${3}"; exit 1; }
}
neg114() { # $1=文件 $2=整串 $3=说明 → 彻底没有
	CNT114=$((CNT114 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "0" ] || { echo "--- FAIL: §114 负锁 ${CNT114}（${3}）：${1} 又出现「${2}」got=${got}"; exit 1; }
}
re114() { # $1=文件 $2=ERE $3=预演 $4=说明 → 按「整行形状」数，用于缩进级语句形态锁
	CNT114=$((CNT114 + 1))
	local got
	got=$(grep -cE -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §114 行形等值锁 ${CNT114}（${4}）：${1} 行形「${2}」got=${got:-0} 预演=${3}"; exit 1; }
}
min114() { # $1=说明 $2=实得 $3=应≥ —— 派生面过窄即红（空转正锁家族）
	CNT114=$((CNT114 + 1))
	[ "${2:-0}" -ge "${3:-1}" ] || { echo "--- FAIL: §114 派生正锁 ${CNT114}（${1}）：实得=${2:-0} 应≥${3}"; exit 1; }
}
# code114：剥注释只此一把尺子（scripts/fe_contract_scan.mjs 的 codeOnly，§113 是它的第一个消费者，
# 本段是第三个）。Go/Python/JSX 的「代码行 vs 说明文字」判据全部经它——波 7 的三处负锁
# （log.Fatalf / prevPrice / m.get(...已报)）的靶串都同时出现在解释性注释里，
# 拿原文数会红在话术上，或者反过来「删掉真代码、注释还写着」时恒绿。
code114() { # $1=**仓库根相对**路径 → 纯代码文本
	REL114="$1" node --input-type=module -e '
const { codeOnly } = await import("./scripts/fe_contract_scan.mjs")
const fs = await import("node:fs")
const raw = fs.readFileSync(process.env.REL114, "utf8")
const code = codeOnly(raw)
if (code.length > raw.length) {
  process.stderr.write("codeOnly-长过了原文：" + process.env.REL114 + "\n")
  process.exit(3)
}
process.stdout.write(code)
' 2>&1
}
codeline114() { # $1=rel $2=整串 → 该串在代码行里出现的行数（尺子坏了就报错，不静默回 0）
	REL114="$1" PAT114="$2" node --input-type=module -e '
const { codeOnly } = await import("./scripts/fe_contract_scan.mjs")
const fs = await import("node:fs")
const code = codeOnly(fs.readFileSync(process.env.REL114, "utf8"))
const n = code.split("\n").filter((l) => l.includes(process.env.PAT114)).length
process.stdout.write(String(n))
' 2>&1
}
eqc114() { # $1=rel $2=整串 $3=预演 $4=说明 → 代码行等值
	local got
	got=$(codeline114 "$1" "$2")
	CNT114=$((CNT114 + 1))
	case "$got" in
	*[Ee]rror*|*not\ found*|*codeOnly*) echo "--- FAIL: §114 代码行尺子没出数（${1}「${2}」）：${got}"; exit 1 ;;
	esac
	[ "$got" = "$3" ] || { echo "--- FAIL: §114 代码行等值锁 ${CNT114}（${4}）：${1} 的代码行「${2}」got=${got} 预演=${3}"; exit 1; }
}
negc114() { # $1=rel $2=整串 $3=说明 → 代码行彻底没有
	local got
	got=$(codeline114 "$1" "$2")
	CNT114=$((CNT114 + 1))
	case "$got" in
	*[Ee]rror*|*not\ found*|*codeOnly*) echo "--- FAIL: §114 代码行尺子没出数（${1}「${2}」）：${got}"; exit 1 ;;
	esac
	[ "$got" = "0" ] || { echo "--- FAIL: §114 代码行负锁 ${CNT114}（${3}）：${1} 的代码行又出现「${2}」got=${got}"; exit 1; }
}
minc114() { # $1=rel $2=整串 $3=应≥ $4=说明 → 代码行命中下界（派生面缩水即红）
	local got
	got=$(codeline114 "$1" "$2")
	CNT114=$((CNT114 + 1))
	case "$got" in
	*[Ee]rror*|*not\ found*|*codeOnly*) echo "--- FAIL: §114 代码行尺子没出数（${1}「${2}」）：${got}"; exit 1 ;;
	esac
	[ "${got:-0}" -ge "$3" ] || { echo "--- FAIL: §114 代码行派生正锁 ${CNT114}（${4}）：${1} 的代码行「${2}」实得=${got:-0} 应≥${3}"; exit 1; }
}
# 尺子自身的夹具自检（两枚）：先证明 codeOnly 在本仓真跑得动、并且真的在剥注释。
# 少了这两枚，「代码行 log.Fatalf=0」既可能是"改干净了"也可能是"尺子安静地没出数"。
CNT114=$((CNT114 + 1))
_CODE_OK=$(code114 "$MAIN114")
printf '%s\n' "$_CODE_OK" | grep -qF -- 'func run() int' \
	|| { echo "--- FAIL: §114 剥注释尺子在 cmd/quant/main.go 上读不到「func run() int」（尺子坏了，本段全部代码行锁的读数不可信）：$(printf '%s\n' "$_CODE_OK" | head -3)"; exit 1; }
CNT114=$((CNT114 + 1))
if printf '%s\n' "$_CODE_OK" | grep -qF -- 'log.Fatalf'; then
	echo "--- FAIL: §114 尺子自检：main.go 的代码行里仍有 log.Fatalf（要么启动期又长出一条退出分支，要么 codeOnly 没剥掉注释而本段的负锁全是假的）"
	exit 1
fi
PREV114=0
J1_N=0; J2_N=0; J3_N=0; J4_N=0; J5_N=0; J6_N=0; J7_N=0; J8_N=0; J9_N=0; J10_N=0
SNAP114() { # $1=组号 → 记下本组新增判定点数（收尾 ok 行的「① 组 N 道」由这里派生，不是手写清单）
	printf -v "J$1_N" '%s' "$((CNT114 - PREV114))"
	PREV114=$CNT114
}

# ── ① §W7-A trigger：Sec 真接进窗口，差分分母＝窗口跨度 ──
eq114 "$TRG114" "st.win = append(st.win, sample{at: now, price: si.Price, amt: si.Amount, turn: si.Turnover})" 1 '采样入窗（每帧进窗口，不是只留上一帧）'
eq114 "$TRG114" 'cutoff := now.Add(-time.Duration(e.cfg.Sec) * time.Second)' 1 '缺陷本体这条：Sec 必须出现在剪窗表达式里（旧形态下 Sec 只在默认值/兜底/启动日志三处，没人读它）'
eq114 "$TRG114" 'if e.cfg.Sec > 0 && len(st.win) > 2 {' 1 '剪窗下限守卫：至少留两帧（剪成一帧＝退回「永不判定」，把"定义未接"换成"定义把判定打死"同样不可接受）'
eq114 "$TRG114" 'base := st.win[0]' 1 '差分基准＝窗口起点'
eq114 "$TRG114" 'elapsed := now.Sub(base.at).Seconds()' 1 '分母＝窗口起点到当前帧的真实跨度（不是相邻两帧的间隔）'
neg114 "$TRG114" 'st.prevPrice' '旧的「上一帧」四标量禁止复活（复活＝Sec 又变回没人读的数字）'
neg114 "$TRG114" 'dt := now.Sub(st.lastAt).Seconds()' '旧的相邻帧间隔当分母（断流十分钟后恢复，第一帧会把 600 秒摊成「秒均」）'
eq114 "$TRG114" 'if gap := now.Sub(st.lastAt).Seconds(); gap <= 0 || gap > 60 {' 1 '断流判定现在只负责整窗重置，不再决定分母'
eq114 "$TRG114" 'func (e *Engine) windowSpan(code string) float64 {' 1 '窗口跨度的自证出口（测试按它断「跨度≤Sec」，不靠返回值猜窗口）'
minc114 "$TRG114" 'cfg.Sec' 2 'Sec 的代码行消费点至少两处（剪窗 + 兜底/默认值参与计算；只剩一处＝接线被摘，本组头三条会一起红）'
eqc114 "$TRGT114" 'e.windowSpan(' 4 '四条用例真读了自证出口（0＝用例改成只看返回值，窗口跨度就没人断）'
eq114 "$TRGT114" 'func TestWindowBoundedBySec(t *testing.T) {' 1 'Sec 界定窗口的正腿'
eq114 "$TRGT114" 'func TestWiderSecKeepsOlderBase(t *testing.T) {' 1 '反向腿：Sec 调大要留更旧的基准（只有一条腿时，"永远剪到两帧"也能绿）'
eq114 "$TRGT114" 'func TestGapResetsWindow(t *testing.T) {' 1 '断流整窗重置'
eq114 "$TRGT114" 'func TestNegativeCumulativeResetsWindow(t *testing.T) {' 1 '累计量倒退（数据源回补/重排）整窗重置——负差分摊成「秒均」是另一种伪造'
SNAP114 1

# ── ② §W7-B cmd/quant：启动期 fail-fast 也走 defer 收尾链 ──
eq114 "$MAIN114" 'if code := run(); code != 0 {' 1 'main 只剩进程外壳：退出码交给 os.Exit，收尾交给 run 的 defer 链'
eq114 "$MAIN114" 'func run() int {' 1 '进程体在 run 里（写在 main 里就没法 return）'
eqc114 "$MAIN114" 'log.Fatalf' 0 '代码行里一条启动期 Fatalf 都不许留（§0927AUDIT-D4 修了停机路径，本条补启动路径；这三处 Fatalf 仍出现在说明注释里，所以判据必须在代码行上）'
eqc114 "$MAIN114" 'return 1' 3 '启动期 fail-fast 三处 return 1（认证库初始化 / staging 守卫 / 端口占用）——少于 3＝某条 fail-fast 被软化成"继续启动"'
eqc114 "$MAIN114" 'return 0' 1 '正常路径 return 0（两条路共用同一段 defer 收尾，退出码只决定外壳要不要 os.Exit）'
eq114 "$MAIN114" 'func verifyDeployment(cfgMgr *config.Manager, authMgr *auth.Manager) error {' 1 '自检函数改为交 error（原来直接 Fatalf，调用点看不见拒绝原因）'
eq114 "$MAIN114" 'if err := verifyDeployment(cfgMgr, authMgr); err != nil {' 1 '调用点接住拒绝并 return 1（定义没接＝§DEADGAUGE「定义未接」同族）'
# 「裸调用禁止复活」由两枚代码行计数合起来守：整串 verifyDeployment(cfgMgr, authMgr) 在代码行里
# 恰一枚，而这一枚就是接 error 的那枚 ⇒ 不可能再有第三处丢弃返回值的调用形态。
# （不用带换行的整串负锁：那会把「grep 命中 0」和「文件读不到」混成同一个绿，set -e 下还只报个行号）
eqc114 "$MAIN114" 'verifyDeployment(cfgMgr, authMgr)' 1 '调用点总数恰一枚（多于 1＝又长出一处不接返回值的调用）'
eq114 "$STG114" 'func TestStagingFailFastRefusesRealGateway(t *testing.T) {' 1 'staging 拒绝语义的正腿（裁决⑥/§WS-G 的语义没动，只改退出姿势）'
eq114 "$STG114" 'func TestNonStagingNeverRefused(t *testing.T) {' 1 '反向腿：非 staging 不许被这条守卫拒掉（否则"修好 defer 链"会把生产进程改成起不来）'
eq114 "$STG114" 'func TestStagingAllowedWhenDisabled(t *testing.T) {' 1 'staging 且 enabled=false 必须放行（守卫射程的第三条边界）'
SNAP114 2

# ── ③ §W7-C 网关状态映射：fail-closed + 三消费者共用一个前缀 + 观察位 ──
eq114 "$HND114" 'UNKNOWN_STATUS_PREFIX = "未知"' 1 '前缀单实现（映射、计数、观察位三处判的是同一个串，写三遍字面「未知」的结局是改一处漏两处）'
eq114 "$HND114" 'XT_STATUS_CODES = {' 1 '状态码登记表提到模块级（测试与映射共用一份）'
eq114 "$HND114" 'if status.startswith(UNKNOWN_STATUS_PREFIX):' 1 '消费者一：映射失败在 on_stock_order 上计数（不只是写进日志等人翻）'
eq114 "$HND114" 'return "%s(缺失)" % UNKNOWN_STATUS_PREFIX' 1 '属性缺失/空串的落点——旧实现正是从这里滑进「已报」（getattr 缺省就是空串）'
eq114 "$HND114" 'return "%s(%s)" % (UNKNOWN_STATUS_PREFIX, raw)' 2 '未登记码与非数字码两条落点（原始值留在串里，现网才知道该补哪个码）'
re114 "$HND114" '^[[:space:]]*return m\.get\(' 0 '旧 fail-open 那一行禁止复活（按整行形状数而不是裸短语：文档串里两处「缺陷原文」引用了同一段代码，全文 grep 会把说明当实现，反过来删掉引用又会让这条锁恒绿）'
eq114 "$GWY114" 'payload["unknown_status"] = {' 1 '消费者三：/admin/status 的观察位（「未知…」不进三本资金账，total>0 是"柜台有一种我们读不懂的状态码"的唯一现网信号）'
eq114 "$GWY114" '"total": int(getattr(self.handler, "unknown_status_total", 0) or 0)' 1 '观察位读的就是计数属性（键名与 handler 属性等值，分家时现网读数恒 0）'
eq114 "$GWY114" '"last": str(getattr(self.handler, "last_unknown_status", "") or "")' 1 '最后一次原始值（补表要看它）'
eq114 "$PYP114" 'def test_unknown_never_impersonates_known(self):' 1 '主断言：读不懂的码不得冒充任何已知态'
eq114 "$PYP114" 'known = set(handler_mod.XT_STATUS_CODES.values())' 1 '已知态集合从模块派生（写死一份清单的话，表里加了码测试照样绿）'
eq114 "$PYP114" 'self.assertEqual(set(cases.keys()), set(handler_mod.XT_STATUS_CODES.keys()),' 1 '等值锁非单向锁：字面真值与运行时表双向对账（只逐码断言时，表里**多**一个码没人看得见）'
eq114 "$PYP114" 'def test_known_codes_do_not_count(self):' 1 '反向腿：已登记码（含 50「已报」真值）不得计数，否则现网正常单把告警刷满'
eq114 "$PYP114" 'def test_broker_unknown_255_counts(self):' 1 '柜台自带的 255「未知」同样计数——它与映射失败的处置一样：不进三本账、必须有人看'
SNAP114 3

# ── ④ §W7-D 量纲：一把尺子量两张表，两个集合各管各的 ──
eq114 "$PBP114" 'var AmountProbedTables = map[string]float64{' 1 '抽检集合单实现（键同时是 SQL 标识符白名单，表名不许外部传串清洗）'
PROBED114=$(grep -cE '^\s+"[a-z0-9_]+": +100\.0,$' "$PBP114" || true)
min114 '抽检集合条目数派生异常（读到 0＝正则坏了，本组下面几枚等值全部不可信）' "$PROBED114" 2
eq114 "$ASW114" 'var AmountScaledTables = map[string]bool{' 1 '写侧换算白名单仍是那份（§0929SCALE-⑩ 的产物，本批没动它的内容）'
neg114 "$ASW114" '"ths_daily": true,' 'ths_daily 不许进换算白名单：THS 侧没有单位核实记录，把猜测的 ×1000 写进库里比矛盾注释更难回滚（可抽检 ≠ 可自动换算，这正是两个集合分开放的理由）'
eq114 "$PBP114" 'func (d *DB) ProbeAmountScale(table, date string, maxRows int) (AmountScaleProbe, error) {' 1 '判定体单实现（daily 与 ths_daily 共用一把尺子）'
eq114 "$PBP114" 'return d.ProbeAmountScale("daily", date, maxRows)' 1 '旧的 ProbeDailyAmountScale 退成薄封装（三个既有消费者零改动），而不是留第二份判定体'
eq114 "$PBP114" 'sharesPerUnit, ok := AmountProbedTables[table]' 1 '倍率只从集合取（第二处判表名＝白名单形同虚设）'
eq114 "$AML114" 'func checkAmountScaleOn(db *store.DB, table string) {' 1 '两条装载自检的唯一实现（两份的结局是修一处漏一处）'
eq114 "$AML114" 'func checkLoadedAmountScale(db *store.DB) { checkAmountScaleOn(db, "daily") }' 1 '日线装载腿'
eq114 "$AML114" 'func checkThsAmountScale(db *store.DB) { checkAmountScaleOn(db, "ths_daily") }' 1 '同花顺日 K 腿（本批新增的读数面）'
eq114 "$ASY114" 'checkThsAmountScale(db)' 1 '导入收尾真的调它（定义没接＝ths_daily 的口径仍然只有注释在担保）'
eq114 "$AML114" 'table := fs.String("table", "daily"' 1 '--table 缺省 daily：第 30 探针与夜间腿的既有调用形态零改动'
eq114 "$AML114" 'p, err := db.ProbeAmountScale(*table, *date, *rows)' 1 '独立腿按 --table 拨抽检（非法表名由 ProbeAmountScale 报错→退 2，不冒充"库里量纲错了"的 1）'
eq114 "$ASY114" 'Amount: row.Turnover,' 1 '写侧姿势：turnover 列原样落 amount，一条换算都没有（改了这条就要同时动本组上面那枚白名单负锁）'
neg114 "$ADP114" 'Turnover   float64 // 换手率（%）' '矛盾的旧注释（字段行形态）禁止复活；说明注释里引用它作为"曾经的两种口径之一"，所以判据锚在字段定义行上而不是裸短语'
neg114 "$ADP114" 'parquet:"turnover"`    // 成交额/换手率' '「两个语义挤在一行」那种注释形态禁止复活（它就是本条缺陷的成因）'
eq114 "$VDP114" '--table" "ths_daily' 1 '第 30 探针加宽到第二张表'
eq114 "$VNG114" '--table" "ths_daily' 1 '夜间 scale 腿同样加宽（两侧不一起动，现网就只有一张表有读数）'
eq114 "$VDP114" '$scaleBad = (($scaleRc -ne 0) -or ($scaleThsRc -ne 0))' 1 '两腿任一红才算红（写成 -and 就是把"其中一张表量纲错了"洗成绿）'
eq114 "$VNG114" '$scaleBad = (($scaleRc -ne 0) -or ($scaleThsRc -ne 0))' 1 '同上，夜间腿同形'
eq114 "$VDP114" '$scaleThsRc = 2' 2 '执行体缺席时两个 rc 都要落 2（只落一个，另一条会以 rc=-1「从未执行」的形态进读数＝半态被读成成功）'
eq114 "$VNG114" '$scaleThsRc = 2' 2 '同上'
# 判数没有被这条加宽带跑：七腿齐备与 `PASS -lt 7` 的实现归 §106（本段只断「没被抬成第 8 腿」，
# 第二处阈值实现在这里长出来，下次改判数就只改得动一份）。
neg114 "$VNG114" 'PASS -lt 8' '夜间判数不许被这张新表抬成 8（一腿两表≠两腿；§106 的七腿齐备锁是单实现）'
SNAP114 4

# ── ⑤ §W7-E 告警路由状态落盘：注释-实现等值 + 单写者 + 忘不掉的持久化 ──
CNT114=$((CNT114 + 1))
[ -f "$RST114" ] || { echo "--- FAIL: §114 路由器 journal 实现在 $RST114 之外不存在（本组与行为腿全部落空）"; exit 1; }
eq114 "$RTG114" '重启后首个 tick 补发' 1 '头注释仍写着这句承诺（这条锁的意义是让"删掉句子来绕开等值锁"不成立：删了它等于删了需求）'
WIRE114=$(codeline114 "$MAIN114" 'metrics.SetAlertRouterStatePath(')
CNT114=$((CNT114 + 1))
if [ "${WIRE114:-0}" -ne 1 ]; then
	echo "--- FAIL: §114 注释-实现等值锁 ${CNT114}：alert_routing.go 声称「重启后首个 tick 补发」（命中 ${WIRE114:-0} 处该接线），而 cmd/quant/main.go 的代码行里 SetAlertRouterStatePath 调用点=${WIRE114:-0}，应为 1——承诺没有实现（§W7-E 本体的原形态就是这句假话）"
	exit 1
fi
eqc114 "$MAIN114" 'alert_router_state.json' 1 'journal 落在 dataDir 下且只有一处名字来源（与 notify_outbox.json 同一姿势）'
eq114 "$RST114" 'func SetAlertRouterStatePath(path string) { globalAlertRouter.setStatePath(path) }' 1 '装配期入口（灌的是进程单例，不是某个引擎实例）'
eq114 "$RST114" 'func (r *alertRouter) restoreLocked(snap AlertRouterState) bool' 1 '回灌单实现'
eq114 "$RST114" 'func (r *alertRouter) snapshotLocked() AlertRouterState' 1 '快照单实现'
eq114 "$RST114" 'func (r *alertRouter) writeState(path string, snap AlertRouterState) {' 1 '落盘单实现（除 Route 内没有第二个调用姿势可写）'
eq114 "$RST114" 'if bytes.Equal(r.lastWritten, data) {' 1 '整份快照字节比较（脏标记要在四个变更点各记一次，漏一个就静默丢；比较序列化结果忘不掉）'
eq114 "$RST114" 'fileutil.AtomicWrite(path, data, 0o600)' 1 '原子写 + 0600（journal 里是告警内容，不是公开读数）'
eq114 "$RST114" 'r.saveMu.Lock()' 1 '落盘串行闸：多账号引擎共用这一个进程单例，同一份 journal 只有一个写者在前'
eq114 "$RST114" 'defer r.saveMu.Unlock()' 1 '与上一枚成对（只 Lock 不 Unlock 时，第一次落盘失败会把后续全部冻死）'
eq114 "$RST114" 'alertRouterStateVersion = 1' 1 '格式版本：版本不符退回空态，不猜旧格式的含义'
eq114 "$RST114" 'opslog.DayOnce("alert-router-state-persist-fail"' 1 '落盘失败可见：首报 + 每 10 次一报 + 每日一条升级留痕（静默丢失正是本批要消灭的东西）'
for k in '"day"' '"daily"' '"announced"' '"pending_resolve"' '"last_fire"' '"suppressed"'; do
	eq114 "$RST114" "json:${k}" 1 "四组跨重启状态之一 ${k}（少一组＝那一组事实重启后仍是内存态）"
done
eq114 "$RTG114" 'out := r.routeEventsLocked(events)' 1 'Route 内部：判路由'
eq114 "$RTG114" 'snap := r.snapshotLocked()' 1 'Route 内部：取快照（持久化写在 Route 里，不靠调用方"记得调"——依赖调用方就是 §高-3 当年「评估了但没出口」的同族）'
eq114 "$RTG114" 'r.writeState(path, snap)' 1 'Route 内部：落盘'
# 顺序锁：文件 IO 必须在 mu 之外（mu 还被首轮评估的 Warn 与只读读数共用，持锁写盘＝把网络/磁盘
# 抖动带进锁；倒序就是"在 mu 内写文件"，只看赋值点字符串的锁看不见这件事）。
LN_UNLOCK114=$(grep -n 'r.mu.Unlock()' "$RTG114" | head -1 | cut -d: -f1 || true)
LN_WRITE114=$(grep -n 'r.writeState(path, snap)' "$RTG114" | head -1 | cut -d: -f1 || true)
CNT114=$((CNT114 + 1))
if ! { [ -n "$LN_UNLOCK114" ] && [ -n "$LN_WRITE114" ]; }; then
	echo "--- FAIL: §114 顺序锁取不到行号（unlock=${LN_UNLOCK114:-空} write=${LN_WRITE114:-空}）：锚点被改名，「文件 IO 在 mu 外」这条纪律没人在守"
	exit 1
fi
CNT114=$((CNT114 + 1))
[ "$LN_UNLOCK114" -lt "$LN_WRITE114" ] \
	|| { echo "--- FAIL: §114 顺序锁 ${CNT114}：${RTG114} 里 r.mu.Unlock()（第 ${LN_UNLOCK114} 行）没有早于 writeState（第 ${LN_WRITE114}  行）——文件 IO 回到持锁区，磁盘抖动会卡住整个路由面"; exit 1; }
# 依赖方向纪律：本包仍不 import store / notify / engine（偏离修复单「sqlite」的代价就是这条必须钉住，
# 否则"小状态"会一路长成"指标面包住落库与推送"）。
neg114 "$RST114" '"quant-trading-v2/internal/store"' 'metrics 不引 store（引了就把指标评估焊在落库实现上，还带来注入环）'
neg114 "$RST114" '"quant-trading-v2/internal/notify"' 'metrics 不引 notify（双发事故同族）'
neg114 "$RST114" '"quant-trading-v2/internal/engine"' 'metrics 不引 engine（engine→metrics→engine）'
eq114 "$RST114" '"quant-trading-v2/internal/fileutil"' 1 '只借叶节点：原子写'
eq114 "$RST114" '"quant-trading-v2/internal/opslog"' 1 '只借叶节点：失败可见'
eq114 "$RSTT114" 'func TestDailySummarySurvivesRestart(t *testing.T) {' 1 'P26 跨日 + 重启补发前一日汇总'
eq114 "$RSTT114" 'func TestPendingResolveSurvivesRestart(t *testing.T) {' 1 'P27 待补发销案跨重启'
eq114 "$RSTT114" 'func TestAnnouncedSurvivesRestart(t *testing.T) {' 1 'P27b 已报未销标记跨重启（丢了它＝只报不销）'
eq114 "$RSTT114" 'func TestSameDayRestartKeepsAccumulating(t *testing.T) {' 1 '同日重启不产生幽灵汇总'
eq114 "$RSTT114" 'func TestCorruptAndVersionMismatchFallBackEmpty(t *testing.T) {' 1 '损坏/版本不符退回空态并说清'
eq114 "$RSTT114" 'func TestWriteSkippedOnlyWhenUnchanged(t *testing.T) {' 1 '内容未变跳写（字节比较那一枚的行为腿）'
eq114 "$RSTT114" 'func TestProductionEntryPathHydratesGlobal(t *testing.T) {' 1 '生产入口真走到 SetAlertRouterStatePath（只测内部函数＝装配那行没接也绿）'
SNAP114 5

# ── ⑥ §W7-F 落位守卫的三态分诊 + 先后顺序 ──
eq114 "$BRG114" 'REPO_Q="$(git rev-parse --is-inside-work-tree 2>&1)" || REPO_RC=$?' 1 '前提判据：先问"这台机器是不是仓库"（不问就派生 HEAD，拿到的是 unknown 还当读数用）'
eq114 "$BRG114" 'REPO_RC="${REPO_RC:-0}"' 1 'set -u 下的退出码兜底（不兜底时"仓库可查"这条路径会 unbound 中止，报错位置离成因很远）'
eq114 "$BRG114" 'X 仓库不可查' 3 '三态里"问不成"那一态的三条文案：不是仓库 / 没进版本控制 / git 自己出错（旧写法把三类压成一句"有未提交改动"，读数撒方向）'
eq114 "$BRG114" '没进版本控制，HEAD 里没有这一版可比' 1 '未纳管这一支：旧守卫对它 rc=0 直接放行，等于把无 HEAD 可对齐的半成品贴进 QMT 策略目录'
eq114 "$BRG114" '>=2 表示 git 自己出错而不是有改动' 1 '把 ≥2 与 1 分开的那句（不写清时，值班的人会把 git 坏了读成"该提交改动"）'
eq114 "$BRG114" '确有未提交改动（工作区 rc=' 1 '只有这一支才给改动清单'
eq114 "$BRG114" 'git diff --quiet -- "$BRIDGE_SRC"' 1 '工作区问一次'
eq114 "$BRG114" 'git diff --cached --quiet -- "$BRIDGE_SRC"' 1 '暂存区再问一次（只看工作区时，已 add 未 commit 的改动会被判成干净）'
LN_REPO114=$(grep -n 'is-inside-work-tree' "$BRG114" | head -1 | cut -d: -f1 || true)
LN_DIFF114=$(grep -n '^DIFF_RC=0' "$BRG114" | head -1 | cut -d: -f1 || true)
LN_SSH114=$(grep -n 'if ! \$SSH ' "$BRG114" | head -1 | cut -d: -f1 || true)
CNT114=$((CNT114 + 1))
if [ -z "$LN_REPO114" ] || [ -z "$LN_DIFF114" ] || [ -z "$LN_SSH114" ]; then
	echo "--- FAIL: §114 守卫顺序锁 ${CNT114} 取不到行号（仓库=${LN_REPO114:-空} 差异=${LN_DIFF114:-空} 首次外呼=${LN_SSH114:-空}）：三段锚点少一段就是守卫被拆"
	exit 1
fi
CNT114=$((CNT114 + 1))
if ! { [ "$LN_REPO114" -lt "$LN_DIFF114" ] && [ "$LN_DIFF114" -lt "$LN_SSH114" ]; }; then
	echo "--- FAIL: §114 守卫顺序锁 ${CNT114}：顺序不是「问仓库(${LN_REPO114}) → 问差异(${LN_DIFF114}) → 才外呼(${LN_SSH114})」——先外呼再分诊＝守卫只对已经动过生产的那次运行负责"
	exit 1
fi
SNAP114 6

# ── ⑦ §W7-G 持仓页兜底可见（代码行尺子；说明注释里也写了这些锚点）──
eqc114 "$POS114" '(price - (p.cost_price || 0)) * (p.qty || 0)' 1 '本地兜底那条重算腿还在（owner 裁决③明令不删：删了＝改变现网显示行为）'
eqc114 "$POS114" 'const pnlFallback = useMemo(' 1 '兜底身份只有一枚派生（与兜底分支同一条件；两枚＝数与标记各说一套）'
eqc114 "$POS114" '{pnlFallback && (' 1 '标记渲染恰一处'
eqc114 "$POS114" '本地兜底' 1 '标记文案恰一枚（多一枚＝另有一处兜底读数没进本锁）'
eqc114 "$POS114" 'data-testid="real-pnl-fallback"' 1 '标记锚点（改名＝vitest 与 Playwright 双双失明）'
eqc114 "$POS114" 'data-testid="real-pnl-value"' 1 '数值锚点'
eqc114 "$POS114" 'title="这个数是前端' 1 '悬停说明必须点名两条口径之差（不写清时标记只是装饰，而缺陷本体就是"看不出这是哪个口径"）'
eqc114 "$PGT114" "it('G" 4 '四条行为腿都在（G1 兜底亮 / G2 权威值到就摘标 / G3 无实盘数据不亮 / G4 结构锁）'
SNAP114 7

# ── ⑧ §W7-COUNT-SYNC 说明条数＝源码派生条数（本批踩过的那类：加一条规则只改代码不改文案）──
N_RULES114=$(grep -cE '^\s+\{Name: "[a-z0-9_]+", Metric:' internal/metrics/alerter.go || true)
N_PUSH114=$(grep -cE '^\s+"[a-z0-9_]+": +RoutePush,' "$RTG114" || true)
N_DAILY114=$(grep -cE '^\s+"[a-z0-9_]+": +RouteDaily,' "$RTG114" || true)
min114 '规则条数派生异常（读到 0＝正则坏了，本组四枚等值全部是拿 0 比 0 的假绿）' "$N_RULES114" 20
min114 '必推条数派生异常' "$N_PUSH114" 12
min114 '日汇总条数派生异常' "$N_DAILY114" 8
CNT114=$((CNT114 + 1))
[ "$((N_PUSH114 + N_DAILY114))" = "$N_RULES114" ] \
	|| { echo "--- FAIL: §114 分桶闭合锁 ${CNT114}：路由表派生 ${N_PUSH114}+${N_DAILY114}=$((N_PUSH114 + N_DAILY114)) != 规则派生 ${N_RULES114}（漏路由条目会掉进 DefaultRoute，等值测试也拦不住"谁都没点名"这种形态）"; exit 1; }
eq114 internal/metrics/alert_routing.go "全部规则（现 ${N_RULES114} 条：" 1 '源码头注释的条数＝派生真值'
eq114 "$MAIN114" "现 ${N_RULES114} 条规则全覆盖：必推 ${N_PUSH114} 条 /" 1 '装配注释的必推数＝派生真值'
eq114 "$MAIN114" "日汇总 ${N_DAILY114} 条，2026-10-09 波 7 按脚本实数同步" 1 '装配注释的日汇总数＝派生真值'
eq114 "$GS114" "（${N_RULES114} 条：必推 ${N_PUSH114} / 日汇总 ${N_DAILY114}）" 1 '门禁 §106 行为腿文案＝派生真值（本批开工前这里写的是 19：波 5 加了第 20 条没同步，同 §0929「加腿没同步计数」家族）'
# 负锁取「行形」而不是旧串逐字：本段就写在门禁文件里，把旧文案整串抄进 neg114 的 needle，
# 这条锁会永远红在自己身上（got=1 来自下一行 itself），而「删掉真代码留着注释」那种复活反而看不见。
# 行形 `^leg106 '…（N 条）'`（只报一个光秃条数、没有分桶）本身就是过时形态——交付形态恒带派生分桶。
re114 "$GS114" "^leg106 'metrics 路由覆盖全规则（[0-9]+ 条）'" 0 '旧文案（光秃条数、无分桶）禁止复活（复活＝说明与实现又分家，而这正是上面四枚等值锁要拦的形态）'
SNAP114 8

# ── ⑨ 行为腿：五组用例先跑真仓（各包/各栈一条，红项带全文回显）──
go_leg114() { # $1=说明 $2=包 $3=-run 正则
	CNT114=$((CNT114 + 1))
	local out
	out=$(go test -count=1 "$2" -run "$3" 2>&1 || true)
	if printf '%s\n' "$out" | grep -qE '^(--- FAIL|FAIL)'; then
		echo "--- FAIL: §114 行为腿判红（${1}），全文如下："
		printf '%s\n' "$out" | head -40
		exit 1
	fi
	CNT114=$((CNT114 + 1))
	printf '%s\n' "$out" | grep -qE '^ok' \
		|| { echo "--- FAIL: §114 行为腿没跑到（$1 无 ok 行＝包编译失败或用例被删）：$(printf '%s\n' "$out" | head -8)"; exit 1; }
	echo "ok - §114 行为腿 $1"
}
go_leg114 'trigger 窗口族（Sec 界定/反向/断流/负差分）' ./internal/trigger/ 'TestAdvanceWindow|TestWindowBoundedBySec|TestWiderSecKeepsOlderBase|TestGapResetsWindow|TestNegativeCumulativeResetsWindow'
go_leg114 'cmd/quant 启动守卫族（staging 拒/放行/非 staging 不误伤）' ./cmd/quant/ 'TestStagingFailFastRefusesRealGateway|TestStagingAllowedWhenDisabled|TestNonStagingNeverRefused|TestGetDataDirStagingForced|TestGetDataDirNormalEnv'
go_leg114 'metrics 路由器 journal 族（P26/P27/P27b + 假绿反证 + 生产入口）' ./internal/metrics/ 'TestDailySummarySurvivesRestart|TestPendingResolveSurvivesRestart|TestAnnouncedSurvivesRestart|TestSameDayRestartKeepsAccumulating|TestCorruptAndVersionMismatchFallBackEmpty|TestWriteSkippedOnlyWhenUnchanged|TestProductionEntryPathHydratesGlobal'
PY_LEG114=$(cd qmt_gateway && python3 -m pytest -q tests/test_xt_mapping.py 2>&1 || true)
N_PYTEST114=$(printf '%s\n' "$PY_LEG114" | grep -E '[0-9]+ passed' | head -1 | grep -oE '[0-9]+ passed' | head -1 | cut -d' ' -f1 || true)
CNT114=$((CNT114 + 1))
if printf '%s\n' "$PY_LEG114" | grep -qE ' failed|error'; then
	echo "--- FAIL: §114 网关映射行为腿判红："
	printf '%s\n' "$PY_LEG114" | tail -30
	exit 1
fi
N_DEFTEST114=$(grep -cE '^\s+def test_' "$PYP114" || true)
CNT114=$((CNT114 + 1))
[ "${N_PYTEST114:-0}" = "${N_DEFTEST114:-0}" ] \
	|| { echo "--- FAIL: §114 pytest 实跑与派生清单不等值（实跑 ${N_PYTEST114:-0} 条 / 文件里 ${N_DEFTEST114:-0} 个 test 定义）：多半是收集阶段就报错或被 skip，等于这条腿根本没跑"; exit 1; }
min114 'pytest 用例数下限（波 7 后本文件应有 ≥18 条：映射 4 + 计数可见 5 + 既有 side/signal 9）' "${N_PYTEST114:-0}" 18
VT_LEG114=$( cd web && NO_COLOR=1 npm test -- w7g_positions_pnl_fallback 2>&1 || true )
N_VT114=$(printf '%s\n' "$VT_LEG114" | grep -E 'Tests +[0-9]+ passed' | head -1 | grep -oE '[0-9]+ passed' | head -1 | cut -d' ' -f1 || true)
CNT114=$((CNT114 + 1))
if printf '%s\n' "$VT_LEG114" | grep -qE ' failed'; then
	echo "--- FAIL: §114 持仓页兜底行为腿判红："
	printf '%s\n' "$VT_LEG114" | grep -E 'FAIL|AssertionError|Unable to find' | head -20
	exit 1
fi
CNT114=$((CNT114 + 1))
[ "${N_VT114:-0}" = "4" ] \
	|| { echo "--- FAIL: §114 vitest 实跑不是「4 条用例」（读到 ${N_VT114:-空}）：G1–G4 少一条就是这条锁面的行为腿有洞"; exit 1; }
echo "ok - §114 行为腿：Go 三组 + pytest ${N_PYTEST114} 条 + vitest ${N_VT114} 条全绿"
SNAP114 9

# ── ⑩ 镜像反证：/tmp 副本树（源文件零改动），先自证镜像基线全绿 ──
GOMIR114="$W114/gomirror"
PYMIR114="$W114/qmt_gateway"
WEBMIR114="$W114/web"
mkdir -p "$GOMIR114/internal" "$GOMIR114/cmd" "$WEBMIR114" \
	|| { echo "--- FAIL: §114 镜像骨架建不出来（反证没有落点，宁可红不许跳）"; exit 1; }
for _e in "$REPO114"/*; do
	_b=$(basename "$_e")
	case "$_b" in
	internal | cmd) continue ;;
	esac
	ln -s "$_e" "$GOMIR114/$_b" || { echo "--- FAIL: §114 镜像软链 ${_b} 失败"; exit 1; }
done
for _e in "$REPO114"/internal/*; do
	_b=$(basename "$_e")
	case "$_b" in
	trigger | metrics) continue ;;
	esac
	ln -s "$_e" "$GOMIR114/internal/$_b" || { echo "--- FAIL: §114 镜像软链 internal/${_b} 失败"; exit 1; }
done
for _e in "$REPO114"/cmd/*; do
	_b=$(basename "$_e")
	case "$_b" in
	quant) continue ;;
	esac
	ln -s "$_e" "$GOMIR114/cmd/$_b" || { echo "--- FAIL: §114 镜像软链 cmd/${_b} 失败"; exit 1; }
done
cp -R "$REPO114/internal/trigger" "$GOMIR114/internal/trigger" || { echo "--- FAIL: §114 镜像实体化 internal/trigger 失败"; exit 1; }
cp -R "$REPO114/internal/metrics" "$GOMIR114/internal/metrics" || { echo "--- FAIL: §114 镜像实体化 internal/metrics 失败"; exit 1; }
cp -R "$REPO114/cmd/quant" "$GOMIR114/cmd/quant" || { echo "--- FAIL: §114 镜像实体化 cmd/quant 失败"; exit 1; }
# Python 侧只取被测模块与用例目录：config*.json / *.db / 日志一律不进镜像
# （反证不需要凭据，把凭据复制到 /tmp 只是多一处泄漏面——§105 家族纪律）。
mkdir -p "$PYMIR114" || { echo "--- FAIL: §114 Python 镜像目录建不出来"; exit 1; }
for _e in "$REPO114"/qmt_gateway/*; do
	_b=$(basename "$_e")
	case "$_b" in
	tests) cp -R "$_e" "$PYMIR114/" ;;
	__pycache__ | .pytest_cache | *.db | *.log | outbox*) : ;;
	config*) : ;;
	*) cp -R "$_e" "$PYMIR114/" ;;
	esac
done
for _e in "$REPO114"/web/*; do
	_b=$(basename "$_e")
	case "$_b" in
	node_modules | dist | test-results | .auth) continue ;;
	esac
	cp -R "$_e" "$WEBMIR114/" || { echo "--- FAIL: §114 镜像复制 web/$_b 失败"; exit 1; }
done
mkdir -p "$WEBMIR114/node_modules" || { echo "--- FAIL: §114 镜像 node_modules 目录建不出来"; exit 1; }
for _e in "$REPO114"/web/node_modules/* "$REPO114"/web/node_modules/.[!.]*; do
	[ -e "$_e" ] || continue
	_b=$(basename "$_e")
	case "$_b" in
	.vite) continue ;;
	esac
	ln -s "$_e" "$WEBMIR114/node_modules/$_b" || { echo "--- FAIL: §114 镜像软链 node_modules/${_b} 失败"; exit 1; }
done
cat > "$W114/mutate.py" <<'PYMUT114'
import sys
# 用法：mutate.py <绝对文件> <old> <new> —— 整串替换并打印落地次数（不是 1 由调用方判红）。
# 一律整串替换：改名式/子串式破坏会让「按旧前缀匹配的负锁」自己踩雷（本仓实录两次：
# dimNaMode→dimNaModeV2、--json→--json=1 都因保留原前缀而恒绿）。
p, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(p, encoding="utf-8").read()
n = s.count(old)
if n:
    open(p, "w", encoding="utf-8").write(s.replace(old, new))
print(n)
PYMUT114
reset114() { # $1=仓库相对路径（镜像里的那份）→ 从主仓真值整体覆盖回来
	src_for="$(printf '%s' "$1" | sed "s#^$GOMIR114/#$REPO114/#; s#^$PYMIR114/#$REPO114/qmt_gateway/#; s#^$WEBMIR114/#$REPO114/web/#")"
	cp "$src_for" "$1" || { echo "--- FAIL: §114 复位失败（${1}）：复位不是「按清单看一眼」，复不了位的镜像后面所有读数都不可信"; return 1; }
}
mut114() { # $1=镜像绝对文件 $2=old $3=new → 打印落地次数
	python3 "$W114/mutate.py" "$1" "$2" "$3" 2>&1 || true
}
strip_ansi114() { LC_ALL=C sed $'s/\033\[[0-9;]*m//g'; }
go_mir114() { # $1=包 $2=-run → 在 Go 镜像里跑（返回文本，退出码不回传：反证读的是有没有 --- FAIL）
	( cd "$GOMIR114" && go test -count=1 "$1" -run "$2" 2>&1 ) || true
}
py_mir114() { # → 在 Python 镜像里跑 test_xt_mapping
	( cd "$PYMIR114" && python3 -m pytest -q tests/test_xt_mapping.py 2>&1 ) || true
}
vt_mir114() { # $1=测试文件名（不含扩展名）
	# 末尾 || true：反证腿里 vitest 必红（退出码 1），而 set -euo pipefail 下管道非零会让赋值语句静默中止整段
	# ——没有这条 || true 时表现是「日志停在上一枚 ok 之后、一行 FAIL 都没有」，归属读不出来。
	( cd "$WEBMIR114" && npx --prefix "$REPO114/web" vitest run --reporter=verbose "src/__tests__/$1.test."* 2>&1 || true ) | strip_ansi114
}
# 基线自证五条：镜像不可信时「破坏后变红」不是证据（§112/§113 同族）。
BASE_TRG114=$(go_mir114 ./internal/trigger/ 'TestWindowBoundedBySec|TestWiderSecKeepsOlderBase')
CNT114=$((CNT114 + 1))
printf '%s\n' "$BASE_TRG114" | grep -qE '^ok' \
	|| { echo "--- FAIL: §114 Go 镜像基线不是绿的（trigger 包），尾部："; printf '%s\n' "$BASE_TRG114" | tail -20; exit 1; }
BASE_MET114=$(go_mir114 ./internal/metrics/ 'TestDailySummarySurvivesRestart|TestPendingResolveSurvivesRestart|TestAnnouncedSurvivesRestart|TestProductionEntryPathHydratesGlobal')
CNT114=$((CNT114 + 1))
printf '%s\n' "$BASE_MET114" | grep -qE '^ok' \
	|| { echo "--- FAIL: §114 Go 镜像基线不是绿的（metrics journal 族），尾部："; printf '%s\n' "$BASE_MET114" | tail -20; exit 1; }
BASE_CMD114=$(go_mir114 ./cmd/quant/ 'TestStagingFailFastRefusesRealGateway|TestNonStagingNeverRefused')
CNT114=$((CNT114 + 1))
printf '%s\n' "$BASE_CMD114" | grep -qE '^ok' \
	|| { echo "--- FAIL: §114 Go 镜像基线不是绿的（cmd/quant 守卫族），尾部："; printf '%s\n' "$BASE_CMD114" | tail -20; exit 1; }
BASE_PY114=$(py_mir114)
CNT114=$((CNT114 + 1))
BASE_PYN114=$(printf '%s\n' "$BASE_PY114" | grep -E '[0-9]+ passed' | head -1 | grep -oE '[0-9]+ passed' | head -1 | cut -d' ' -f1 || true)
if printf '%s\n' "$BASE_PY114" | grep -qE ' failed|error'; then
	echo "--- FAIL: §114 Python 镜像基线就是红的（镜像不可信 ⇒ 后面「破坏后变红」不构成证据）："
	printf '%s\n' "$BASE_PY114" | tail -15
	exit 1
fi
CNT114=$((CNT114 + 1))
[ "${BASE_PYN114:-0}" -ge 18 ] \
	|| { echo "--- FAIL: §114 Python 镜像基线没跑满（passed=${BASE_PYN114:-0} 应≥18，文件里用例定义=${N_DEFTEST114:-未算}）：收集阶段就出错＝这棵反证树没有 Python 腿"; printf '%s\n' "$BASE_PY114" | tail -15; exit 1; }
BASE_VT114=$(vt_mir114 w7g_positions_pnl_fallback)
CNT114=$((CNT114 + 1))
printf '%s\n' "$BASE_VT114" | grep -qE 'Tests +4 passed' \
	|| { echo "--- FAIL: §114 web 镜像基线不是「4 passed」（node_modules 软链或 vitest 配置在镜像里坏了）："; printf '%s\n' "$BASE_VT114" | grep -E 'Test Files|Tests |FAIL|Cannot' | head -10; exit 1; }
echo "ok - §114 镜像基线自证（Go 三包 / pytest / vitest 在 /tmp 副本树里各自全绿）"

# dys114_go：镜像里改 Go 源 → 断「本枚点名的用例红」+「点名的另一条仍绿」→ 复位复绿。
dys114_go() { # $1=编号 $2=镜像绝对文件 $3=old $4=new $5=包 $6=-run $7=必红用例 $8=必仍绿用例(可空) $9=说明
	local id="$1" file="$2" old="$3" new="$4" pkg="$5" rx="$6" red="$7" keep="$8" why="$9" n out rc
	CNT114=$((CNT114 + 1))
	n=$(mut114 "$file" "$old" "$new")
	[ "$n" = "1" ] || { echo "--- FAIL: §114 反证 ${id} 变异落地数=${n}（应恰好 1；0＝靶串没命中，>1＝破坏面比本枚射程大，后面归因不成立）：${why}"; exit 1; }
	out=$(go_mir114 "$pkg" "$rx")
	rc=0
	printf '%s\n' "$out" | grep -qE "^--- FAIL: ${red}" || rc=$?
	CNT114=$((CNT114 + 1))
	[ "$rc" = "0" ] || { echo "--- FAIL: §114 反证 ${id} 破坏后 ${red} 没有红（${why}）——锁/用例是装饰，全文尾部："; printf '%s\n' "$out" | tail -15; exit 1; }
	if [ -n "$keep" ]; then
		CNT114=$((CNT114 + 1))
		if printf '%s\n' "$out" | grep -qE "^--- FAIL: ${keep}"; then
			echo "--- FAIL: §114 反证 ${id} 把 ${keep} 也带红了（本枚要「独有」：连带红说明破坏面越出了这一条腿，归属读不出来）"
			exit 1
		fi
	fi
	reset114 "$file" || exit 1
	out=$(go_mir114 "$pkg" "$rx")
	CNT114=$((CNT114 + 1))
	printf '%s\n' "$out" | grep -qE '^ok' || { echo "--- FAIL: §114 反证 ${id} 复位后仍红＝镜像被别处污染，后续反证读数全部作废"; printf '%s\n' "$out" | tail -12; exit 1; }
	echo "ok - §114 反证 ${id}（${why}）：${red} 必红${keep:+ / ${keep} 仍绿} + 复位复绿"
}
dys114_go V1 "$GOMIR114/internal/trigger/trigger.go" \
	'cutoff := now.Add(-time.Duration(e.cfg.Sec) * time.Second)' \
	'cutoff := now.Add(-3650 * 24 * time.Hour)' \
	./internal/trigger/ 'TestWindowBoundedBySec|TestWiderSecKeepsOlderBase' \
	TestWindowBoundedBySec TestWiderSecKeepsOlderBase \
	'Sec 不参与剪窗（窗口无限长）时，跨度判据那条必红而"Sec 调大留更旧基准"那条仍绿——证明这两条用例分别管两件事'
dys114_go V2 "$GOMIR114/cmd/quant/main.go" \
	'if cfgMgr.Get().QMT.Enabled {' \
	'if false && cfgMgr.Get().QMT.Enabled {' \
	./cmd/quant/ 'TestStagingFailFastRefusesRealGateway|TestNonStagingNeverRefused' \
	TestStagingFailFastRefusesRealGateway TestNonStagingNeverRefused \
	'把 staging 守卫改成"永远放行"时，正腿必红、"非 staging 不被误伤"那条仍绿——退出姿势改了，资损级拒绝语义还在被盯'
dys114_go V7 "$GOMIR114/internal/metrics/alert_router_state.go" \
	'if restored := r.restoreLocked(snap); restored {' \
	'if restored := false; restored { // MUTATED-V7：回灌调用整条摘掉（读文件但不装回）' \
	./internal/metrics/ 'TestDailySummarySurvivesRestart|TestPendingResolveSurvivesRestart' \
	TestDailySummarySurvivesRestart '' \
	'FIX_PLAN 要的 P28 反证：清掉回灌（读到了但不灌）⇒ 跨日补发那条必红，证明落盘文件不是自说自话'

# dys114_py / dys114_vitest：另两栈同形（红→归属串→复位复绿）。
dys114_py() { # $1=编号 $2=绝对文件 $3=old $4=new $5=必红的 pytest 节点串 $6=必仍绿的节点串(可空) $7=说明
local id="$1" file="$2" old="$3" new="$4" node="$5" keep="$6" why="$7" n out
	CNT114=$((CNT114 + 1))
	n=$(mut114 "$file" "$old" "$new")
	[ "$n" = "1" ] || { echo "--- FAIL: §114 反证 ${id} 变异落地数=${n}（应恰好 1）：${why}"; exit 1; }
	out=$(py_mir114)
	CNT114=$((CNT114 + 1))
	printf '%s\n' "$out" | grep -qF -- "$node" \
		|| { echo "--- FAIL: §114 反证 ${id} 破坏后没让 ${node} 现形（${why}），尾部："; printf '%s\n' "$out" | tail -20; exit 1; }
	printf '%s\n' "$out" | grep -qE ' failed' \
		|| { echo "--- FAIL: §114 反证 ${id} 破坏后 pytest 一条都没红（${why}）——用例是装饰"; printf '%s\n' "$out" | tail -20; exit 1; }
	if [ -n "$keep" ] && printf '%s\n' "$out" | grep -qF -- "$keep"; then
		echo "--- FAIL: §114 反证 ${id} 把 ${keep} 也带红了（本枚要「独有」：连带红＝破坏面越出这一条腿，归属读不出来）"
		exit 1
	fi
	CNT114=$((CNT114 + 1))
	reset114 "$file" || exit 1
	out=$(py_mir114)
	CNT114=$((CNT114 + 1))
	printf '%s\n' "$out" | grep -qE ' passed' && ! printf '%s\n' "$out" | grep -qE ' failed' \
		|| { echo "--- FAIL: §114 反证 ${id} 复位后仍红＝镜像污染"; printf '%s\n' "$out" | tail -12; exit 1; }
	echo "ok - §114 反证 ${id}（${why}）：${node} 必红${keep:+ / ${keep} 仍绿} + 复位复绿"
}
dys114_py V3 "$PYMIR114/handler.py" \
	$'            return mapped\n        return "%s(%s)" % (UNKNOWN_STATUS_PREFIX, raw)' \
	$'            return mapped\n        return "已报"' \
	test_unknown_never_impersonates_known test_known_codes_do_not_count \
	'回到旧 fail-open（读不懂一律冒充「已报」）时主断言必红；test_known_codes_do_not_count 同时仍绿，证明"已登记码不误伤"不是靠运气'
dys114_py V4 "$PYMIR114/handler.py" \
	'            return "%s(缺失)" % UNKNOWN_STATUS_PREFIX' \
	'            return "已报"' \
	test_missing_attribute_counts test_broker_unknown_255_counts \
	'属性缺失这一支单独反证（跨构建字段名变化是常态路径，V3 打的是数字未登记那一支）'
dys114_vitest() { # $1=编号 $2=绝对文件 $3=old $4=new $5=测试文件名 $6=必红用例前缀 $7=必仍绿用例前缀(可空) $8=说明
local id="$1" file="$2" old="$3" new="$4" tf="$5" want="$6" keep="$7" why="$8" n out
	CNT114=$((CNT114 + 1))
	n=$(mut114 "$file" "$old" "$new")
	[ "$n" = "1" ] || { echo "--- FAIL: §114 反证 ${id} 变异落地数=${n}（应恰好 1）：${why}"; exit 1; }
	out=$(vt_mir114 "$tf")
	CNT114=$((CNT114 + 1))
	if ! { printf '%s\n' "$out" | grep -qE "× .*${want}" && printf '%s\n' "$out" | grep -qE 'Tests +[0-9]+ failed'; }; then
		echo "--- FAIL: §114 反证 ${id} 破坏后 vitest 没让 ${want} 现形（${why}），尾部："
		printf '%s\n' "$out" | grep -E 'Test Files|Tests |×' | head -14
		exit 1
	fi
	if [ -n "$keep" ] && printf '%s\n' "$out" | grep -qE "× .*${keep}"; then
		echo "--- FAIL: §114 反证 ${id} 把 ${keep} 也带红了（本枚要「独有」：连带红＝破坏面越出这一条腿，归属读不出来）"
		exit 1
	fi
	CNT114=$((CNT114 + 1))
	reset114 "$file" || exit 1
	out=$(vt_mir114 "$tf")
	CNT114=$((CNT114 + 1))
	printf '%s\n' "$out" | grep -qE 'Tests +4 passed' \
		|| { echo "--- FAIL: §114 反证 ${id} 复位后 vitest 没复绿＝镜像污染"; printf '%s\n' "$out" | grep -E 'Test Files|Tests |FAIL|×' | head -12; exit 1; }
	echo "ok - §114 反证 ${id}（${why}）：${want} 必红${keep:+ / ${keep} 仍绿} + 复位复绿"
}
dys114_vitest V5 "$WEBMIR114/src/pages/Positions.jsx" '{pnlFallback && (' '{false && (' \
	w7g_positions_pnl_fallback 'G1' 'G3' \
	'摘掉标记渲染点：兜底数照旧亮着而没人知道（这条缺陷的本体就是"看不出是哪个口径"），G1/G4 双双必红'
dys114_vitest V6 "$WEBMIR114/src/pages/Positions.jsx" 'const pnlFallback = useMemo(' 'const pnlFallbackRenamedX = useMemo(' \
	w7g_positions_pnl_fallback 'G4' '' \
	'身份派生改名（整串替换）：G4 的「恰一枚实现」必红——留旧前缀的改名式破坏会让按前缀匹配的锁恒绿'
# 静态镜像反证：不跑测试，只断「同一把尺子在副本上的读数按预期翻转」，用于 §114 的三枚静态锁面。
dys114_static() { # $1=编号 $2=仓库相对文件 $3=old $4=new $5=尺子(c|g) $6=needle $7=基线读数 $8=破坏后读数 $9=说明
	local id="$1" rel="$2" old="$3" new="$4" ruler="$5" needle="$6" base="$7" want="$8" why="$9"
	local mf="$W114/static/$rel" n got
	CNT114=$((CNT114 + 1))
	mkdir -p "$(dirname "$mf")" || true
	cp "$REPO114/$rel" "$mf" || { echo "--- FAIL: §114 反证 ${id} 取不到 $rel 的副本"; exit 1; }
	n=$(mut114 "$mf" "$old" "$new")
	[ "$n" = "1" ] || { echo "--- FAIL: §114 反证 ${id} 变异落地数=${n}（应恰好 1）：${why}"; exit 1; }
	if [ "$ruler" = "c" ]; then
		got=$(REL114="$mf" PAT114="$needle" node --input-type=module -e '
const { codeOnly } = await import("./scripts/fe_contract_scan.mjs")
const fs = await import("node:fs")
const code = codeOnly(fs.readFileSync(process.env.REL114, "utf8"))
process.stdout.write(String(code.split("\n").filter((l) => l.includes(process.env.PAT114)).length))
' 2>&1)
	else
		got=$(grep -cF -- "$needle" "$mf" 2>/dev/null || true)
	fi
	CNT114=$((CNT114 + 1))
	[ "$got" = "$want" ] || { echo "--- FAIL: §114 反证 ${id} 读数没按预期翻转（needle=${needle} 基线=${base} 破坏后应=${want} 实得=${got}）：${why}"; rm -rf "$W114/static"; exit 1; }
	rm -rf "$W114/static"
	echo "ok - §114 反证 ${id}（${why}）：读数 ${base}→${got}"
}
dys114_static V8 cmd/quant/main.go '	return 0' '	log.Fatalf("MUTATED-V8")' c 'log.Fatalf' 0 1 \
	'再长出一条启动期 Fatalf 时，代码行尺子必须现形（全文 grep 会被注释里的三处 Fatalf 干扰，测不出这条）'
dys114_static V9 internal/data/amountscale.go 'var AmountScaledTables = map[string]bool{' 'var AmountScaledTables = map[string]bool{
	"ths_daily": true,' g '"ths_daily": true,' 0 1 \
	'有人"加了抽检就顺手换算"时（ths_daily 进换算白名单），第 ④ 组那枚负锁必须红——这是资金量纲上一道真实的门'
DYS114_N=$(grep -cE '^dys114_(go|py|vitest|static) V' "$GS114" || true)
CNT114=$((CNT114 + 1))
[ "${DYS114_N:-0}" -ge 9 ] || { echo "--- FAIL: §114 反证枚数派生异常（读到 ${DYS114_N:-0}，应≥9）：枚数从调用点派生，写死的收尾文案对下一个新增反证天生失明"; exit 1; }

# ── 覆盖面诚实账（不判红，只把「今天还落在锁外面的东西」如实记下来）──
echo "INFO - §114 覆盖面账：THS dump 的 turnover 单位仍无抽样实录（第 30 探针与夜间 scale 腿的 ths_daily 读数在现网首跑前是空集，本段只能锁"尺子接到了这张表"，锁不了"这张表是元"）；cmd/dataload 里仍有 log.Fatalf（波 7-B 的射程是 cmd/quant 进程体，装载器是一次性命令、没有 defer 链可跳过，不在本批承诺内）；路由器 journal 没有 /health 键也没有 verify 探针（可见性=启动日志 + 落盘失败 opslog.DayOnce，加探针要同批改 §88 的 INFO 计数与 §106 的判数，本批按"不留半截现网面"处理而不是顺手加一格）；告警路由冷却窗与 journal 的多进程形态未测（本机是单进程多账号引擎，跨机器多实例并写同一文件这件事没有反证覆盖）。"

# ── 分组自证（本段最后两道锁）──
CNT114=$((CNT114 + 1))
_EMPTY_J=""
for _g in 1 2 3 4 5 6 7 8 9; do
	_v="J${_g}_N"
	if [ "${!_v}" -le 0 ]; then _EMPTY_J="${_EMPTY_J} ${_g}"; fi
done
[ -z "$_EMPTY_J" ] || { echo "--- FAIL: §114 分组快照在位锁 ${CNT114}（这些组一个判定点都没记到：组${_EMPTY_J}）——漏一处 SNAP114 时那组的锁会被并进邻组读数，累计数正常而分组数是假的"; exit 1; }
SNAP114 10
SUMJ114=$((J1_N + J2_N + J3_N + J4_N + J5_N + J6_N + J7_N + J8_N + J9_N + J10_N))
CNT114=$((CNT114 + 1))
[ "$SUMJ114" = "$((CNT114 - 1))" ] || { echo "--- FAIL: §114 分组求和自证锁 ${CNT114}（十组快照之和 ${SUMJ114} != 累计判定点扣本锁 $((CNT114 - 1))）——有判定点没落进任何一组，收尾的覆盖面读数不可信"; exit 1; }
rm -rf "$W114"
echo "ok - §114 全段通过：① trigger 窗口接线 ${J1_N} 道 + ② 启动退出姿势 ${J2_N} 道 + ③ 状态映射 fail-closed 与三消费者 ${J3_N} 道 + ④ 量纲两表分离与现网两腿加宽 ${J4_N} 道 + ⑤ 路由器落盘（含注释-实现等值与顺序锁）${J5_N} 道 + ⑥ 落位守卫三态分诊 ${J6_N} 道 + ⑦ 持仓兜底可见（代码行尺子）${J7_N} 道 + ⑧ 计数同源 ${J8_N} 道（派生 ${N_RULES114}=${N_PUSH114}+${N_DAILY114}）+ ⑨ 行为腿（Go 三组 / pytest ${N_PYTEST114} 条 / vitest ${N_VT114} 条）${J9_N} 道 + ⑩ 镜像基线自证与 ${DYS114_N} 枚反证 V1–V${DYS114_N} ${J10_N} 道，累计判定点 ${CNT114}（其中十组快照之和 ${SUMJ114}，另有 1 道就是求和自证锁本身）"
echo ""


echo "==> 115 §CONTRACT-LEDGER 前后端契约台账（2026-10-06 修复批 波 7 收尾，§七 断言 Q1–Q3）：重算落成机器判据——台账与注册全集双向相等、无标签=0、幽灵调用=0，静态锁 + 镜像基线自证 + 十枚反证..."

# 本段守的是「数字有没有人看着」。2026-10-05 全量评价里那句「35 条零调用 / 16 条无台账」是代理
# 读数，10-06 逐条复核时抽查两例即偏（PUT /api/tenants/{id} 其实有前端调用，真零命中的是
# GET /api/tenant/usage），于是两个数字被整体撤回「待重算」。修复单 §七 对重算提了三条断言：
#
#   Q1 路由全集 R 从 internal/server/server.go 的 HandleFunc **派生**（现值 186，须等值打印），
#      台账 scripts/contract_ledger.tsv 与 R 逐条双向相等，每条带分类标签；
#   Q2 无标签条目 = 0（等值锁）；|R| < 180 即红；
#   Q3 幽灵调用（C − R）= 0。
#
# 取向：判据落在**核对器**（scripts/contract_ledger_check.py）里，本段只做三件事——
#   ① 核台账自身的形态（行数/列数/落点诚实/不许把凭据或镜像路径写进证据列）；
#   ② 读核对器打印的 KEY=VALUE 汇总做等值锁（本段禁止自己再数一遍：数两遍＝两套口径，
#      §0929DRILL 那轮「两份判读函数只修一份」的教训同族）；
#   ③ 建一棵只装「核对器真读的那些文件」的 /tmp 镜像，先自证它在镜像里也绿，再逐枚破坏。
#
# 为什么台账必须是 .tsv 而不是再写进报告正文（修复单 §七 明写「落一份台账文件，不是再写进报告」）：
# 本仓 *.md 被 gitignore（文档永不进库），写进 md 的账等于没账——下一轮 checkout 就看不见，
# 而 .tsv 进库、能被核对器读、能被门禁锁住。
#
# ★ 反证十枚 C1–C10 全在镜像里做，且**每枚断一个本枚独有的读数**（KEY=VALUE 那一层，
#   不是只断「有 FAIL 行」）：C1 与 C7 都会打「台账漏了」这句话，但 C1 翻的是 LEDGER_ROWS=185、
#   C7 翻的是 ROUTES_DERIVED=187——只断文案的两枚锁会同色，只断读数的一枚锁才分得开。
# ★ C9/C10 破坏的是**核对器自己**（把两个派生正则改坏）：这两枚是 Q2 那两条「空转正」正锁的
#   存在性证据。没有它们，「|R| < 180 即红」只是一句没人试过的话——解析模式与 Go 侧注册格式
#   脱节时读回 0，红的是「全都没调用」这种**看起来更像结论**的东西（§107 预演实录同族）。
#
# English: §115 locks the recomputed front/back contract ledger. R is derived from HandleFunc
# registrations; the TSV ledger must equal R two ways with a label + verifiable evidence per row;
# untagged and ghost calls must be zero. The gate reads the checker's KEY=VALUE summary (it never
# recounts), builds a /tmp mirror containing only the files the checker actually reads, proves the
# mirror is green first, then runs ten reversals — each asserting the one reading that only it can
# flip, including two that break the checker's own derivations to prove the floor guards bite.
CNT115=0
REPO115="$PWD"
LED115=scripts/contract_ledger.tsv
CLC115=scripts/contract_ledger_check.py
SRV115=internal/server/server.go
API115=web/src/api/index.js
GS115=scripts/verify_changes.sh
W115="$(mktemp -d /tmp/contract115-XXXXXX 2>/dev/null || true)"
[ -n "$W115" ] || { echo "--- FAIL: §115 建不出镜像目录，基线自证与十枚反证无法跑（宁可红，不许跳）"; exit 1; }

eq115() { # $1=文件 $2=整串 $3=预演 $4=说明
	CNT115=$((CNT115 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §115 整串等值锁 ${CNT115}（${4}）：${1} 整串「${2}」got=${got:-0} 预演=${3}"; exit 1; }
}
neg115() { # $1=文件 $2=整串 $3=说明 → 彻底没有
	CNT115=$((CNT115 + 1))
	local got
	got=$(grep -cF -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "0" ] || { echo "--- FAIL: §115 负锁 ${CNT115}（${3}）：${1} 又出现「${2}」got=${got}"; exit 1; }
}
re115() { # $1=文件 $2=ERE $3=预演 $4=说明 → 按整行形状数
	CNT115=$((CNT115 + 1))
	local got
	got=$(grep -cE -- "$2" "$1" 2>/dev/null || true)
	[ "${got:-0}" = "$3" ] || { echo "--- FAIL: §115 行形等值锁 ${CNT115}（${4}）：${1} 行形「${2}」got=${got:-0} 预演=${3}"; exit 1; }
}
min115() { # $1=说明 $2=实得 $3=应≥ —— 派生面过窄即红（空转正锁家族）
	CNT115=$((CNT115 + 1))
	[ "${2:-0}" -ge "${3:-1}" ] || { echo "--- FAIL: §115 派生正锁 ${CNT115}（${1}）：实得=${2:-0} 应≥${3}"; exit 1; }
}
# run_ledger115：跑核对器并把**退出码**留住（LEDGER_RC115）。
# 这里是本段唯一允许「期望失败」的地方：反证腿里核对器必红，而 set -euo pipefail 下
# 命令替换里的非零退出会把整段静默带走——表现是日志停在上一枚 ok 之后、一行 FAIL 都没有（§114 实录）。
# 用 `|| VAR=$?` 收码而不是 `|| true`：吞掉码就等于把「核对器根本没跑起来」也读成绿。
run_ledger115() { # $1=根目录（仓库根或镜像根）
	LEDGER_RC115=0
	LEDGER_OUT115=$(python3 "$1/scripts/contract_ledger_check.py" 2>&1) || LEDGER_RC115=$?
}
lget115() { # $1=KEY → 从上一次 run_ledger115 的输出里取值（取不到返回空串，由调用方判红）
	printf '%s\n' "$LEDGER_OUT115" | sed -n "s/^$1=//p" | head -1
}
eqkey115() { # $1=KEY $2=应等值 $3=说明 → 派生读数与 prose 等值（等值锁，不是单向锁）
	CNT115=$((CNT115 + 1))
	local got
	got=$(lget115 "$1")
	[ "$got" = "$2" ] || { echo "--- FAIL: §115 读数等值锁 ${CNT115}（${3}）：$1 实读=${got:-<空>} 应=$2"; printf '%s\n' "$LEDGER_OUT115" | grep -E "^$1=|^--- FAIL" | head -6; exit 1; }
}

# ── ① 台账自身的形态：在位、每行五列、落点诚实、不许夹带凭据 ──
CNT115=$((CNT115 + 1))
[ -f "$LED115" ] || { echo "--- FAIL: §115 台账 $LED115 不在位（核对器读不到它＝整个契约账不存在）"; exit 1; }
LED_DATA=$(grep -vE '^[[:space:]]*#|^[[:space:]]*$' "$LED115" || true)
LED_DATA_N=$(printf '%s\n' "$LED_DATA" | grep -c . || true)
min115 "台账数据行数（现值 186＝R 全集；掉到 180 以下＝有人在没改核对器的情况下删了账）" "$LED_DATA_N" 180
CNT115=$((CNT115 + 1))
_BADCOL=$(printf '%s\n' "$LED_DATA" | awk -F'\t' 'NF!=5 {c++} END{print c+0}' || true)
[ "${_BADCOL:-0}" = "0" ] || { echo "--- FAIL: §115 有 ${_BADCOL} 行不是恰好 5 列（method/path/class/evidence/note）：列一错位，核对器读到的 class 会是上一条的 note（§表格行劈开同族）"; printf '%s\n' "$LED_DATA" | awk -F'\t' 'NF!=5 {print "  第" NR "行 " NF "列: " $0}' | head -4; exit 1; }
# 证据列不许点名镜像/临时树/依赖目录：那些位置的文字在下一轮 checkout 里不存在，
# 「可核落点」就退化成「曾经可核」——本段的立段理由恰恰是反对这种账。
CNT115=$((CNT115 + 1))
_BADPATH=$(printf '%s\n' "$LED_DATA" | awk -F'\t' '$4 ~ /(^|\/)(tmp|\.qoder|node_modules|\.uat-data)\// {c++} END{print c+0}' || true)
[ "${_BADPATH:-0}" = "0" ] || { echo "--- FAIL: §115 有 ${_BADPATH} 行的落点指向镜像/临时/依赖目录（/tmp、.qoder、node_modules、.uat-data）：那种证据下次没人拨得动"; printf '%s\n' "$LED_DATA" | awk -F'\t' '$4 ~ /(^|\/)(tmp|\.qoder|node_modules|\.uat-data)\// {print "  " $1 " " $2 " → " $4}' | head -4; exit 1; }
# 台账不许内嵌字面公网 IPv4（仓库纪律同 §Mac 侧调度链负锁：出口只走 ssh 别名或运行期参数）。
# 允许的是回环与文档保留段——它们是夹具，不是现网地址。
CNT115=$((CNT115 + 1))
_IPV4=$(printf '%s\n' "$LED_DATA" | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' | grep -vE '^(127\.|0\.0\.0\.0|203\.0\.113\.|192\.0\.2\.|198\.51\.)' || true)
[ -z "$_IPV4" ] || { echo "--- FAIL: §115 台账里出现字面公网 IPv4：$(printf '%s\n' "$_IPV4" | head -3 | tr '\n' ' ')（点名机器要写文件行号，不要写地址）"; exit 1; }
neg115 "$LED115" "admin_session_token" '台账不许把口令/令牌文件名当证据（证据要能公开复核）'
# 核对器自身：下限常量与判据条数都在射程内，防止「掏空判据只留打印」。
re115 "$CLC115" '^ROUTE_FLOOR = 180$' 1 'Q2 那枚 |R|<180 的下限只有一个出处（改成 0 就等于没有）'
CNT115=$((CNT115 + 1))
_LOCKN115=$(grep -cE '^[[:space:]]+lock\(' "$CLC115" || true)
min115 "核对器里的判据条数（lock( 调用点；现值 14）——判据被整段删空而汇总打印还在是最难发现的坏法" "$_LOCKN115" 12
eq115 "$CLC115" 'sys.exit(2)' 2 '读不到文件/列数错位走退出码 2（「判据没跑起来」不许冒充「判据不成立」）'
# 接线锁：门禁必须**既读退出码又读汇总**。只看文案的接线会把「python 抛异常」读成「没打印 FAIL＝绿」。
CNT115=$((CNT115 + 1))
_RCUSE115=$(grep -cE 'LEDGER_RC115' "$GS115" || true)
min115 "门禁内 LEDGER_RC115 的使用点（定义 1 + 基线断言 + 反证断言 + 本锁＝现值 ≥4）：rc 没被任何判断读过＝整段假绿" "$_RCUSE115" 4
SNAP115() { # $1=组号 → 记下本组新增判定点数（收尾 ok 行的「① 组 N 道」由这里派生，不是手写清单）
	printf -v "K$1_N" '%s' "$((CNT115 - PREV115))"
	PREV115=$CNT115
}
PREV115=0
SNAP115 1

# ── ② 正跑：读核对器的 KEY=VALUE，逐键等值（本段不自己数）──
run_ledger115 "$REPO115"
CNT115=$((CNT115 + 1))
[ "$LEDGER_RC115" = "0" ] || { echo "--- FAIL: §115 契约台账核对器在主仓退出码=${LEDGER_RC115}（应 0）：$(printf '%s\n' "$LEDGER_OUT115" | grep -E '^--- FAIL' | head -4)"; exit 1; }
eqkey115 LEDGER_CHECK GREEN '台账闭合（Q1 双向相等 + Q2 无标签=0 + Q3 幽灵=0 + 撒谎=0 全齐）'
eqkey115 ROUTES_DERIVED 186 'Q1 的 R：HandleFunc 派生读数须等值打印（修复单 §七 明写现值 186）'
eqkey115 CALLPATH_DERIVED 150 'C 的规范化路径条数（前端真拨得出去的地址）'
eqkey115 CALLSITES_DERIVED 153 '调用点条数（一条路径可被多处拨；这两个数不等不是矛盾，是分账）'
eqkey115 OUT_OF_RANGE 0 '射程账：request() 里展开不出路径的调用必须为 0——静默跳过＝Q1/Q3 对它一起失明'
eqkey115 LEDGER_ROWS 186 '台账行数须等于 R（多一行＝幽灵账，少一行＝新注册没归类）'
eqkey115 COVERED_BY_WEB 150 '被前端覆盖的路由条数'
eqkey115 UNCALLED 36 'R−C：每条都必须有分类标签＋可核落点（本批重算的交付物就是这个数）'
eqkey115 GHOST_CALLS 0 'Q3：前端拨了服务端没注册的地址＝运行期 404，比缺 UI 严重'
eqkey115 UNTAGGED 0 'Q2：分类/依据/说明三者缺一的条数'
eqkey115 NO_BASIS 0 '「无依据」档必须为空：说不清为什么还留着＝待决策，不许混进台账假装闭合'
eqkey115 BROKEN_EVIDENCE 0 '可核落点核不过的条数（运维 curl 不能只是形容词）'
eqkey115 LEDGER_LIES 0 '标了「前端调用」却在实时派生里找不到的条数（前端删了而账没改＝假勾）'
# 三枚求和自证：单点等值会被「两边同时错」绕过，和式锁把口径闭起来。
CNT115=$((CNT115 + 1))
_SUMCLS=$(printf '%s\n' "$LEDGER_OUT115" | awk -F= '/^CLASS_/{s+=$2} END{print s+0}')
[ "$_SUMCLS" = "$(lget115 LEDGER_ROWS)" ] || { echo "--- FAIL: §115 分类求和自证（Σ CLASS_*=${_SUMCLS} != LEDGER_ROWS=$(lget115 LEDGER_ROWS)）：有行的标签没进任何一类（新增标签没登记进 CLASSES 时的形态）"; exit 1; }
CNT115=$((CNT115 + 1))
[ "$(( $(lget115 COVERED_BY_WEB) + $(lget115 UNCALLED) ))" = "$(lget115 ROUTES_DERIVED)" ] || { echo "--- FAIL: §115 覆盖求和自证（COVERED=$(lget115 COVERED_BY_WEB) + UNCALLED=$(lget115 UNCALLED) != ROUTES=$(lget115 ROUTES_DERIVED)）：账不闭合，等值锁再多也只是逐条自洽"; exit 1; }
CNT115=$((CNT115 + 1))
[ "$(lget115 CLASS_前端调用)" = "$(lget115 CALLPATH_DERIVED)" ] || { echo "--- FAIL: §115 「前端调用」条数须等于派生路径条数（实得 $(lget115 CLASS_前端调用)/$(lget115 CALLPATH_DERIVED)）：不等就有派生调用没落账，或落账的调用已经不是前端"; exit 1; }
# 台账格式与核对器读数必须同源：两处各数一遍迟早分家（§0929DRILL「两份判读函数只修一份」同族）。
CNT115=$((CNT115 + 1))
[ "$LED_DATA_N" = "$(lget115 LEDGER_ROWS)" ] || { echo "--- FAIL: §115 台账行数两把尺子分家：门禁数到 ${LED_DATA_N}，核对器数到 $(lget115 LEDGER_ROWS)（注释/空行判定不一致＝其中一把会漏行）"; exit 1; }
SNAP115 2

# ── ③ 镜像：只装核对器真读的那些文件，清单从台账证据列**派生** ──
# 为什么按派生而不是写死清单（§BOM-REPO-DERIVE 教训）：写死的文件清单对下一个新增的
# 「运维 curl」条目天生失明——台账加一行新证据而镜像没带那个文件，反证腿会红在「文件不存在」上，
# 读起来像锁有牙，其实是镜像残缺。
MIR115="$W115/mir"
mkdir -p "$MIR115/scripts" || { echo "--- FAIL: §115 建不出镜像 scripts 目录"; exit 1; }
MIRFILES115=$(
	{
		printf '%s\n' "$LED_DATA" | awk -F'\t' '{split($4, a, ":"); if (a[1] != "") print a[1]}'
		echo "$CLC115"
		echo "$SRV115"
		echo "$API115"
		echo "$LED115"   # 核对器的第四个必读文件（LEDGER 自身）：镜像清单必须从**核对器读什么**派生，
						# 而不是从「证据列写了什么」派生——10-09 首版只并了三件，基线自证当场 rc=2 报
						# 「镜像里没有 contract_ledger.tsv」，这正是这枚自证存在的理由（少文件的镜像与代码坏了长得一样）。
	} | sort -u
)
MIRN115=$(printf '%s\n' "$MIRFILES115" | grep -c . || true)
min115 "镜像文件数（派生自台账证据列 ∪ 核对器必读四件；现值 12）＝0 或过窄说明 awk 那把尺子失效了" "$MIRN115" 10
MIRCOPY_OK=0
while IFS= read -r _f; do
	[ -n "$_f" ] || continue
	if [ ! -f "$REPO115/$_f" ]; then
		echo "--- FAIL: §115 台账点名的证据文件在主仓不存在：${_f}（证据列在撒谎，或文件被改名而账没跟着改）"
		exit 1
	fi
	mkdir -p "$MIR115/$(dirname "$_f")"
	cp "$REPO115/$_f" "$MIR115/$_f" || { echo "--- FAIL: §115 镜像拷贝失败：$_f"; exit 1; }
	MIRCOPY_OK=$((MIRCOPY_OK + 1))
done <<< "$MIRFILES115"
CNT115=$((CNT115 + 1))
[ "$MIRCOPY_OK" = "$MIRN115" ] || { echo "--- FAIL: §115 镜像拷贝只成功 ${MIRCOPY_OK}/${MIRN115} 个文件（残缺镜像上的任何「破坏后变红」都不构成证据）"; exit 1; }
# 镜像里不许有凭据：清单派生自证据列，所以这条是「证据列本身不许指向凭据」的第二道闸。
CNT115=$((CNT115 + 1))
if printf '%s\n' "$MIRFILES115" | grep -qE '\.db$|\.log$|config[^/]*\.json$|admin_session_token|\.key$|\.pem$'; then
	echo "--- FAIL: §115 镜像清单里出现凭据/数据库/日志形态的路径：$(printf '%s\n' "$MIRFILES115" | grep -E '\.db$|\.log$|config[^/]*\.json$|admin_session_token|\.key$|\.pem$' | head -2)（反证树不该带这些，凭据不外拷是硬纪律）"
	exit 1
fi
# 基线自证：同一份判据在镜像里也必须绿，且逐键读数与主仓一致（不一致＝镜像少带了文件，
# 少一个证据文件会让 BROKEN_EVIDENCE 变红，而那和「代码坏了」长得一模一样）。
run_ledger115 "$MIR115"
CNT115=$((CNT115 + 1))
[ "$LEDGER_RC115" = "0" ] || { echo "--- FAIL: §115 镜像基线不绿（rc=${LEDGER_RC115}）：$(printf '%s\n' "$LEDGER_OUT115" | grep -E '^--- FAIL' | head -3)"; exit 1; }
eqkey115 ROUTES_DERIVED 186 '镜像基线：R 与主仓同值'
eqkey115 LEDGER_ROWS 186 '镜像基线：台账行数与主仓同值'
eqkey115 BROKEN_EVIDENCE 0 '镜像基线：证据落点在镜像里全部拨得动（少文件就是这里红，别处绿的镜像不可信）'
eqkey115 LEDGER_CHECK GREEN '镜像基线整体闭合'
SNAP115 3

# ── ④ 十枚反证：每枚只断「本枚独有」的那个读数 ──
cat > "$W115/mutate.py" <<'PYMUT115'
import sys
# 用法：mutate.py <绝对文件> <old> <new> —— 整串替换并打印落地次数（不是 1 由调用方判红）。
# 一律整串替换：改名式/子串式破坏会让「按旧前缀匹配的锁」自己踩雷（本仓实录：
# dimNaMode→dimNaModeV2、--json→--json=1 都因保留原前缀而恒绿）。
p, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(p, encoding="utf-8").read()
n = s.count(old)
if n:
    open(p, "w", encoding="utf-8").write(s.replace(old, new))
print(n)
PYMUT115
dys115() { # $1=编号 $2=镜像相对文件 $3=old $4=new $5=本枚独有读数 KEY $6=翻转后的值 $7=说明
	local id="$1" rel="$2" old="$3" new="$4" key="$5" want="$6" why="$7" n got
	CNT115=$((CNT115 + 1))
	cp "$REPO115/$rel" "$MIR115/$rel" || { echo "--- FAIL: §115 反证 ${id} 无法从主仓复位镜像文件 $rel"; exit 1; }
	n=$(python3 "$W115/mutate.py" "$MIR115/$rel" "$old" "$new" 2>&1 || true)
	[ "$n" = "1" ] || { echo "--- FAIL: §115 反证 ${id} 变异落地数=${n}（应恰好 1）：$why"; exit 1; }
	run_ledger115 "$MIR115"
	CNT115=$((CNT115 + 1))
	if [ "$LEDGER_RC115" = "0" ]; then
		echo "--- FAIL: §115 反证 ${id} 破坏后核对器仍然退出 0（这枚是假锁：${why}）"
		exit 1
	fi
	got=$(lget115 "$key")
	[ "$got" = "$want" ] || { echo "--- FAIL: §115 反证 ${id} 读数没按预期翻转（${key} 应=${want} 实得=${got:-<空>}）：${why}；FAIL 行：$(printf '%s\n' "$LEDGER_OUT115" | grep -E '^--- FAIL' | head -2)"; exit 1; }
	printf '%s\n' "$LEDGER_OUT115" | grep -qE '^--- FAIL' || { echo "--- FAIL: §115 反证 ${id} 红了却没有 --- FAIL 行（rc 非 0 来自异常而不是判据：判据没跑起来不算验过）"; exit 1; }
	cp "$REPO115/$rel" "$MIR115/$rel" || { echo "--- FAIL: §115 反证 ${id} 复位失败（${rel}）"; exit 1; }
	run_ledger115 "$MIR115"
	CNT115=$((CNT115 + 1))
	[ "$LEDGER_RC115" = "0" ] || { echo "--- FAIL: §115 反证 ${id} 复位后仍红＝镜像被污染，后面所有反证读数作废：$(printf '%s\n' "$LEDGER_OUT115" | grep -E '^--- FAIL' | head -3)"; exit 1; }
	echo "ok - §115 反证 ${id}：破坏 $rel → $key 翻成「${want}」且复位回基线"
}
KLINE_ROW=$(printf '%s\n' "$LED_DATA" | grep -F "$(printf '\t')/api/kline$(printf '\t')" | head -1 || true)
CNT115=$((CNT115 + 1))
[ -n "$KLINE_ROW" ] || { echo "--- FAIL: §115 反证靶行取不到（台账里没有带 tab 分隔的 /api/kline 行＝列格式变了，上面那些 awk 尺子也一起失效）"; exit 1; }
# C1 整行删除：账少一条，R 照旧 186 → 翻的是 LEDGER_ROWS。
dys115 C1 "$LED115" "$KLINE_ROW" "" LEDGER_ROWS 185 '新增/改名注册没进分类账（台账漏了一条路由）'
# C2 幽灵台账行：行数不变、R 不变，红在「对不上现网注册」那一侧。
# 取向：不写 printf 拼串的占位写法（拼错一次会静默变成 no-op，而 no-op 的反证看起来像跑过了）。
KLINE_PATH_GHOST=$(printf '%s' "$KLINE_ROW" | sed "s#$(printf '\t')/api/kline$(printf '\t')#$(printf '\t')/api/kline-renamed-x$(printf '\t')#")
dys115 C2 "$LED115" "$KLINE_ROW" "$KLINE_PATH_GHOST" LEDGER_ROWS 186 '路由已删/改名而台账没跟着改：行数仍 186，红在「对不上现网注册」——所以本枚的独有读数是 LEDGER_ROWS 不变（与 C1 的 185 分得开）'
KLINE_CLASS=$(printf '%s' "$KLINE_ROW" | sed "s#$(printf '\t')兼容端点$(printf '\t')internal/server/handlers_fix.go#$(printf '\t\t')internal/server/handlers_fix.go#")
dys115 C3 "$LED115" "$KLINE_ROW" "$KLINE_CLASS" UNTAGGED 1 '分类标签被清空：空标签＝「没看过」被算成「看过了」（Q2 的等值锁）'
KLINE_NOBASIS=$(printf '%s' "$KLINE_ROW" | sed "s#$(printf '\t')兼容端点$(printf '\t')internal/server/handlers_fix.go#$(printf '\t')无依据$(printf '\t')internal/server/handlers_fix.go#")
dys115 C4 "$LED115" "$KLINE_ROW" "$KLINE_NOBASIS" NO_BASIS 1 '把说不清的条目塞进「无依据」假装闭合：那一档必须是待决策而不是台账的一行'
KLINE_EVID=$(printf '%s' "$KLINE_ROW" | sed 's#internal/server/handlers_fix.go:531#internal/server/handlers_ghost.go:531#')
dys115 C5 "$LED115" "$KLINE_ROW" "$KLINE_EVID" BROKEN_EVIDENCE 1 '落点指向不存在的文件：证据列退化成记忆（运维 curl 不能只是形容词）'
# C6 前端把调用点删了而台账没改：翻 LEDGER_LIES（撒谎检测），R 与台账都不动。
dys115 C6 web/src/api/index.js "  return request('/api/research/library')" "  return [] // §115 C6 反证：调用点被摘掉而台账仍写「前端调用」" LEDGER_LIES 1 '「标签必须被实时派生证实」这条不在 Q1–Q3 里，但没有它，台账可以把每一条都写成前端调用然后绿着'
# C7 新注册没进账：翻 ROUTES_DERIVED（与 C1 的文案同色、读数不同色——正是本枚要断读数的原因）。
dys115 C7 internal/server/server.go '	s.mux.HandleFunc("GET /api/health", s.authMiddleware(s.handleHealth))' "$(printf '%s\n' '	s.mux.HandleFunc("GET /api/health", s.authMiddleware(s.handleHealth))' '	s.mux.HandleFunc("GET /api/w115-newprobe", s.authMiddleware(s.handleHealth))')" ROUTES_DERIVED 187 '新加路由没归类：与 C1 的区别是 R 涨了（187）而台账还是 186——只看文案的锁分不开这两枚'
# C8 幽灵调用（Q3）：前端拨一个服务端没注册的地址，GHOST_CALLS 才现形。
dys115 C8 web/src/api/index.js "export async function fetchSignals() {" "$(printf '%s\n' 'export async function w115GhostProbe() {' "  return request('/api/w115-ghost-target', { method: 'POST' })" '}' '' 'export async function fetchSignals() {')" GHOST_CALLS 1 '拨未注册地址＝运行期 404：这条比缺 UI 严重，所以单独立数'
# C9 派生模式失效的「空转正」：把 R 的正则改坏，读回 0 —— ROUTE_FLOOR 必须当场红，
# 而不是安静地变成「路由只有 0 条、台账 186 条全对不上」那种看起来很有道理的结论。
dys115 C9 "$CLC115" 'HandleFunc\("([A-Z]+) ([^"]+)"' 'HandleFuncZZ\("([A-Z]+) ([^"]+)"' ROUTES_DERIVED 0 'Q2 的 |R|<180 即红：解析格式与 Go 侧注册脱节时读出很小的数，比红更危险（它把「一行都没解析到」报成「全都没调用」）'
# C10 同一枚空转正锁的调用侧：api/index.js 的 request() 形态变了会静默读空。
dys115 C10 "$CLC115" '(?<!function )\brequest\(' '(?<!function )\brequestZZ\(' CALLSITES_DERIVED 0 'C 侧空转正：调用点派生归零时 153<100 即红，不许把「没解析到」报成「前端零调用」'
DYS115_N=$(grep -cE '^dys115 C[0-9]+ ' "$GS115" || true)
CNT115=$((CNT115 + 1))
[ "${DYS115_N:-0}" -ge 10 ] || { echo "--- FAIL: §115 反证枚数派生异常（读到 ${DYS115_N:-0}，应≥10）：枚数从调用点派生，写死的收尾文案对下一个新增反证天生失明"; exit 1; }
SNAP115 4

# ── 覆盖面诚实账（不判红，只把「今天还落在锁外面的东西」如实记下来）──
echo "INFO - §115 覆盖面账：台账只覆盖 request() 的字面量/三元调用＋人工点名的三条前端直连（登录 fetch / 登出 fetch / SSE EventSource）——web/src 的 *.jsx 里 /api/ 字面量实测 0 处（除 api/index.js 自身），所以「页面绕过 api 层直接拨」这件事现在没有发生，但**没有机器锁拦着它发生**：新增一条页面级 fetch 会走 C9/C10 那两把派生尺子的盲区（它们只读 api/index.js）。三条「运维 curl」的 /api/metrics* 在仓内没有自动拨测（发版链刻意不消费它，§FQ 凭据负锁钉死），这三行登记的是「人工拨」，机器只能核注册在位、核不到有人真拨。台账对查询串不敏感（norm 切掉 ? 之前才算路径），所以 GET 却改状态这类副作用口径不在本段射程，那是 §56 写端点收权普查的事。36 条未调用里 admin 按账号代配 8 条 + 运营账本 2 条 + 租户 2 条属「保留尺寸等页面」，本轮不删任何一条（实装优先禁删除）。"

# ── 分组自证（本段最后两道锁）──
CNT115=$((CNT115 + 1))
_EMPTY_K=""
for _g in 1 2 3 4; do
	_v="K${_g}_N"
	if [ "${!_v}" -le 0 ]; then _EMPTY_K="${_EMPTY_K} ${_g}"; fi
done
[ -z "$_EMPTY_K" ] || { echo "--- FAIL: §115 分组快照在位锁 ${CNT115}（这些组一个判定点都没记到：组${_EMPTY_K}）——漏一处 SNAP115 时那组的锁会被并进邻组读数，累计数正常而分组数是假的"; exit 1; }
SNAP115 5
SUMK115=$((K1_N + K2_N + K3_N + K4_N + K5_N))
CNT115=$((CNT115 + 1))
[ "$SUMK115" = "$((CNT115 - 1))" ] || { echo "--- FAIL: §115 分组求和自证锁 ${CNT115}（五组快照之和 ${SUMK115} != 累计判定点扣本锁 $((CNT115 - 1))）——有判定点没落进任何一组，收尾的覆盖面读数不可信"; exit 1; }
rm -rf "$W115"
echo "ok - §115 全段通过：① 台账形态与证据诚实（含公网 IP/凭据/镜像路径三枚负锁与核对器掏空负锁）${K1_N} 道 + ② 逐键等值（R=186 / C=150 / 未调用=36 / Q2=0 / Q3=0）+ 三枚求和自证 ${K2_N} 道 + ③ 镜像基线自证（派生清单 ${MIRN115} 个文件、逐键与主仓同值）${K3_N} 道 + ④ ${DYS115_N} 枚反证 C1–C${DYS115_N}（每枚断一个本枚独有读数）${K4_N} 道 + ⑤ 覆盖面诚实账与在位锁 ${K5_N} 道，累计判定点 ${CNT115}（其中五组快照之和 ${SUMK115}，另有 1 道就是求和自证锁本身）"
echo ""

echo "==> 全部通过"
