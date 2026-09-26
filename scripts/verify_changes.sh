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
SET_CALL=$(grep -n 'diff, err := c.SettleDay(' internal/trading/settlement.go | grep -vE '^[0-9]+:[[:space:]]*(//|\*)' | head -1 | cut -d: -f1 || true)
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
( cd web && npm test -- h2_positions_balance m9_error_tri_state m13_forbidden_poll ) 2>&1 | grep -E 'Test Files|passed|failed'
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
# 行为锁：三条出口 + 键名一致性 + 窗口换算。
go test -count=1 ./internal/trading/ -run 'TestSettleDayFeedsDiffGauge|TestSettleDaySkipBranchesWriteZero' 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
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
echo "ok - §DEADGAUGE 专项守卫通过（行为锁 4 组 + 静态锁 4 道 + 通用死规则守卫 1 条 + 负锁 3 道）"

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
for ps1 in deploy/qmt-win/backup_snapshot.ps1 deploy/qmt-win/register_backup_task.ps1; do
	python3 - "$ps1" <<'PY' || { echo "--- FAIL: $ps1 仓库字节 BOM 不合规（手工安装路径首跑必炸）"; exit 1; }
import sys
d = open(sys.argv[1], 'rb').read()
n = 0
while d[n:].startswith(b'\xef\xbb\xbf'):
	n += 3
assert n == 3, 'BOM 个数=%d（需恰好 1 个）' % (n // 3)
d.decode('utf-8')
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
echo "ok - §LIVEBACKUP-DEPLOY 专项守卫通过（清单正锁 3 + 同源锁 2 + 探针锁 3 + 锁面正锁 2 + restic 自愈形状锁 5 + 负锁 4）"

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
# §RESTIC-LOCK 补锁：Invoke-Native 的参数名绝不能叫 $Args——PS 自动变量，占用会静默改语义。
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
# ① 锁文件名同源（脚本端 Join-Path $SnapRoot，探针端 Join-Path $SnapDir）。
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
# ② 接管龄上界等值：脚本用分钟（$LockMaxMin），探针用小时，换算后必须相等。
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
	|| { echo "--- FAIL: "已撤+零成交可重放"的谓词不再现算配对 SQL（退回编译期常量即漏掉反向腿）"; exit 1; }
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
#    行数=4：逐文件空态/逐文件明细/当日聚合（§SIGNAL-DIST 原三条）+ 日历读数（§CAL-READOUT，
#    2026-09-26 owner 令新增第 27 探针的恒回显腿；§101 另有 INFO|cal_readout 在位锁）。
grep -qF 'INFO\|*) echo' "$VD" \
	|| { echo '--- FAIL: bash 侧 INFO 分支丢失（观测读数会被当成未知行，或被误接进 PASS/FAIL 计数）'; exit 1; }
infop=$(grep -c 'Write-Output ("INFO|' "$VD" || true)
[ "$infop" = "4" ] || { echo "--- FAIL: INFO 观测行数不是 4（逐文件空态/逐文件明细/当日聚合/日历读数，计数=${infop}）"; exit 1; }
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
  · $1（读到 ${2}，应为 ${3}）"; fi; }
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
  · $1（读到 ${2}，应为 ${3}）"; fi; }
lg_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then LG_ERRS="${LG_ERRS}
  · $1（读到 ${2}，应 ≥ ${3}）"; fi; }
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
  · $1（读到 ${2}，应为 ${3}）"; fi; }
cg_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then CG_ERRS="${CG_ERRS}
  · $1（读到 ${2}，应 ≥ ${3}）"; fi; }

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
  · $1（读到 ${2}，应为 ${3}）"; fi; }
cp_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then CP_ERRS="${CP_ERRS}
  · $1（读到 ${2}，应 ≥ ${3}）"; fi; }
cp_absent() { if [ "${2:-0}" -ne "0" ]; then CP_ERRS="${CP_ERRS}
  · $1（应彻底没有，实得 ${2} 处）"; fi; }


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
sqlite3 "$CP_DB" "CREATE TABLE research_candidates (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'proposed', factors TEXT, weights TEXT, metric REAL, ic_mean REAL, ir REAL, avg_excess REAL, horizon INTEGER, reason TEXT, guard TEXT DEFAULT 'standard', params TEXT DEFAULT '');"
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
sqlite3 "$CP_EMPTY" "CREATE TABLE research_candidates (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'proposed', factors TEXT, weights TEXT, metric REAL, ic_mean REAL, ir REAL, avg_excess REAL, horizon INTEGER, reason TEXT, guard TEXT DEFAULT 'standard', params TEXT DEFAULT '');"
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
  · $1（读到 ${2}，应为 ${3}）"; fi; }
ev_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then EV_ERRS="${EV_ERRS}
  · $1（读到 ${2}，应 ≥ ${3}）"; fi; }
ev_absent() { if [ "${2:-0}" -ne "0" ]; then EV_ERRS="${EV_ERRS}
  · $1（应彻底没有，实得 ${2} 处）"; fi; }
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
#      （五类特权变更各有一行）。
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
  · $1（读到 ${2}，应为 ${3}）"; fi; }
ew_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then EW_ERRS="${EW_ERRS}
  · $1（读到 ${2}，应 ≥ ${3}）"; fi; }
ew_absent() { if [ "${2:-0}" -ne "0" ]; then EW_ERRS="${EW_ERRS}
  · $1（应彻底没有，实得 ${2} 处）"; fi; }
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
ew_chk "admin.go 审计行 3→8（五类特权变更补齐）" "$(grep -c 'opslog.Audit' internal/server/admin.go || true)" "8"

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
  · $1（读到 ${2}，应为 ${3}）"; fi; }
gw_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then GW_ERRS="${GW_ERRS}
  · $1（读到 ${2}，应 ≥ ${3}）"; fi; }
gw_absent() { if [ "${2:-0}" -ne "0" ]; then GW_ERRS="${GW_ERRS}
  · $1（应彻底没有，实得 ${2} 处）"; fi; }
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
gw_chk "C7 消费脚本 dot-source 恰 6 个（三套并存的收编面）" "$(grep -l 'Join-Path \$PSScriptRoot "service_definitions.ps1"' deploy/qmt-win/*.ps1 | wc -l | tr -d ' ')" "6"
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
# 一物一锁，三族：
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
# ③ §RESTIC-LOCK Mac 拉取腿陈旧锁自愈：两仓 unlock + 三败中途补一次（03:09 遗留锁拦死 07:00
#    窗的实录修法）；自愈必须非致命（每行带 || 兜底），且 copy 成败判定语义一字未动。
# 全部判据是静态 grep + 离网一次性临时库真跑（段尾统一 rm，测试数据不落工作树）。
# ════════════════════════════════════════════════════════════════════════════
echo "==> 101 §0926PM 三季报通道+日历读数探针+restic 自愈（2026-09-26 下午批）..."
FQ_ERRS=""
fq_chk() { if [ "$2" != "$3" ]; then FQ_ERRS="${FQ_ERRS}
  · $1（读到 ${2}，应为 ${3}）"; fi; }
fq_min() { if [ "${2:-0}" -lt "${3:-1}" ]; then FQ_ERRS="${FQ_ERRS}
  · $1（读到 ${2}，应 ≥ ${3}）"; fi; }
fq_absent() { if [ "${2:-0}" -ne "0" ]; then FQ_ERRS="${FQ_ERRS}
  · $1（应彻底没有，实得 ${2} 处）"; fi; }

FQ_SCR=scripts/backfill_fina_q3_guangzhou.sh
FQ_VD=scripts/verify_deploy_guangzhou.sh
FQ_RS=deploy/mac/restic_pull_backup.sh
FQ_SY=0; bash -n "$FQ_SCR" 2>/dev/null || FQ_SY=$?
fq_chk "§FINA-Q3 脚本语法自检 bash -n" "$FQ_SY" "0"
FQ_SY2=0; bash -n "$FQ_VD" 2>/dev/null || FQ_SY2=$?
fq_chk "verify_deploy 语法自检 bash -n（第 27 探针插入后）" "$FQ_SY2" "0"
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

# ── ③ §RESTIC-LOCK 拉取腿陈旧锁自愈 ──
fq_min "unlock 点名（两仓首清+三败中途补+口径注释，≥6 处）" "$(grep -c 'unlock' "$FQ_RS" || true)" "6"
FQ_UNLOCK_RAW="$({ grep -E '^[[:space:]]*restic .*unlock' "$FQ_RS" || true; } | { grep -vc '|| ' || true; })"
fq_absent "unlock 命令行无 || 兜底（自愈必须非致命，真并发锁照样交给 copy 判红）" "$FQ_UNLOCK_RAW"
fq_chk "copy 成败判定语义一字未动（COPY_OK 判定行恰 1）" "$(grep -c '\[ "\$COPY_OK" = "1" \] || fail' "$FQ_RS" || true)" "1"

if [ -z "$FQ_ERRS" ]; then
	echo "ok - §0926PM 守卫通过（FINA-Q3 静态锁 11 + 离网反证 5 + 日历探针锁 8 + restic 自愈锁 3）"
else
	echo "--- FAIL: §0926PM 断言不符:${FQ_ERRS}"
	exit 1
fi


echo ""
echo "==> 全部通过"
