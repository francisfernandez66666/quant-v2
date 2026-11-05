// carrier.go — §CARRIER-UPDATE（2026-11-05 外脑化 B2）：执行腿「载体」的期望态与回执。
//
// 这一层为什么必须存在（券商 2026-11-03 公告，11-05 起生效，见 AGENTS「通道前提变更」）：
// QMT **内置策略**从此不能读本地文件、不能连任何网络，因此那个真正装着战法的载体文件
// （qmt_gateway/qmt_internal_strategy.py）**结构上无法自更新**——它既看不见"该装哪一版"，
// 也拿不到新版字节。能干活的是同一台物理机上那个普通本地进程（carrier_updater.py /
// local_executor.py，文件与网络都合法），于是分工固定成两半：
//
//	盒子（另一个 agent 负责，qmt_gateway/carrier_updater.py 已在仓）：
//	    轮询 GET /api/qmt/carrier/manifest → 与本机装的那份比哈希 → 取字节 → 验 → 交给
//	    scripts/update_qmt_strategy.ps1 投递（停策略/写文件/重启都在那支脚本里，不在这里）→
//	    POST /api/qmt/carrier/ack 回执"这台机器上现在真装着什么"。
//	广州外脑（本文件）：
//	    存"这一根腿**应该**跑什么"（期望版本 + 期望 SHA256），把盒子的回答记下来，
//	    并给运营一个可点的人工触发（request_state=pending）。
//
// 四条不许被"顺手优化"掉的边界：
//  1. **归属只由那把钥匙决定**（§安全 T1 / §TOKEN-PERAACCT 同口径）：两条盒子侧路由都挂在
//     qmtReportMiddleware 下——命中该 token 的账号即归属，请求体/查询串里自报的 user_id
//     一律不信（收下、不一致时打一行日志，绝不采信）。
//  2. **读不到就报"未知"，绝不报"已是最新"**：清单文件缺失/坏 JSON/期望版本或哈希没填齐
//     ⇒ GET 响应 ok=false + request_state=unknown。这条是本仓反复出现的那类缺陷的正面处置
//     （§STARVE-STALE 的 J0"零回包不算健康"、§0925EVE-W3-E"读不到不装已知"、
//     report_uplink"missing≠empty"）：把"不知道"折成"没问题"，盒子就会安静地什么都不做，
//     而页面上写着"正常"。
//  3. **状态机只有一条推进腿**：request_state 只从 pending 推进（且回执报的版本必须等于
//     期望版本），acked/failed/idle 收到一次无人请求的回执只更新 last_ack、不动状态。
//     否则"运营刚把期望版升到 v03、盒子那条 v02 的迟到回执就把 pending 吃掉"会变成一个
//     永远等不到答复的死格。
//  4. **解析不了的回执一律不落地**：缺 ok、缺版本、超长的先 400 拒掉，一个字段都不写。
//     半截回执覆盖掉上一份好回执＝把证据换成垃圾（§F18 取向）。
//
// 产物字节这一半本批**刻意没做**：manifest 里带着 SHA256，盒子拿它去验任何它取到的字节
// （远程 URL 或本机重烘，carrier_updater 两条腿都验）。将来加下载路由时的三条硬边界已写在
// carrierArtifactsDir 的注释上——照 manifest 里的文件名白名单拼路径，绝不信请求里的路径。
//
// English: brain-side "what this leg SHOULD be running" manifest + the box's ack ledger.
// Attribution comes from the gateway key only; an unreadable manifest reads as unknown
// (never as up-to-date); request_state only advances out of pending, and only when the acked
// version equals the desired one; an unparseable ack is refused without touching the file.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/opslog"
)

// carrierManifestFileName 账号目录下的落盘文件名（与 leg_status.json / t_rounds_live.json 同根：
// <dataDir>/accounts/<userID>/ 下）。文件名是常量而非请求参数——路径只能由服务端拼。
const carrierManifestFileName = "carrier_manifest.json"

// carrierArtifactsDirName 预留给"产物字节"那一条腿的仓库级目录名（<dataDir>/<此名>/）。
// 本批**不供字节、不建路由**，把名字写在这里只有一个原因：将来加 GET /api/qmt/carrier/artifact
// 时必须满足三条（缺一不可，且都已在盒子侧被 carrier_updater.py 的验哈希腿盯着）：
//
//	① 仍走 qmtReportMiddleware（钥匙鉴权、按钥匙归属账号），不给任何免鉴权豁口；
//	② 文件名只从该账号 manifest 里的白名单字段取（strategy_file 之类），请求里不许出现路径段；
//	③ 拼出的绝对路径必须仍在 <dataDir>/<此名>/ 之内（含 ".."、绝对路径、符号链接出逃一律拒），
//	   并且响应字节必须与 manifest 的 SHA256 相符——哈希不符宁可不供。
const carrierArtifactsDirName = "carrier_artifacts"

// 请求态四个持久值 + 一个只读值。
// unknown 只出现在**读侧**（清单不可读时的如实回答），永远不会被写进文件——
// 把"读不到"存成一种状态，会让下一次真读到之后那条状态没人清理。
const (
	carrierStateIdle    = "idle"    // 没有待处理的人工请求（初始态）
	carrierStatePending = "pending" // 运营点过「更新载体」，等盒子下一次轮询接住
	carrierStateAcked   = "acked"   // 盒子回了话且 ok=true
	carrierStateFailed  = "failed"  // 盒子回了话且 ok=false
	carrierStateUnknown = "unknown" // 读侧专属：清单缺失/坏/期望值没填齐
)

// carrierVersionMaxLen / carrierReasonMaxLen 是回执字段的硬上限。
// 为什么要上限：这条腿的写入方是一台别人家里的机器，一次超长写入会把账号目录里那份
// 人类要取证的 JSON 变成垃圾（也顺手把日志刷满）。超限直接 400，不截断保存——
// 截断会让"版本 v02-2026110…"看起来像一个合法版本，而那正是查不出来的时候。
const (
	carrierVersionMaxLen = 64
	carrierReasonMaxLen  = 200
)

// carrierShaRe 期望 SHA256 的形状（64 位十六进制，大小写都收）。
// 形状校验发生在读侧：形状不对的哈希永远匹配不上，盒子的每一轮都会"下载→验不过→失败"，
// 不如这里直接判 ok=false 并把原因说清楚（一次配置错误不该变成每天的失败回执）。
var carrierShaRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// carrierVersionRe 版本号形状：可读字符集，禁空白与路径段。
// 禁空白是因为这些值会进盒子那侧一行 `CARRIER_MANIFEST k=v` 的空格分隔日志，
// 带空格的值会把一条机器可读的记录劈成两半（AGENTS：现网两次假象同源于"锚点被换掉"）。
var carrierVersionRe = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,64}$`)

// carrierMu 串行化清单的"读-改-写"。
// 三个写入方是并发到的：盒子的回执（每台腿各自 POST）、运营的点击（另一条 HTTP 线程）、
// 以及同一账号连续两条回执。不加锁的形态是"后写的把前写的 last_ack 抹掉"——
// 与 leg_status.go 那把锁同一个成因。一把进程级小锁足够：这条链每个账号每天几条。
var carrierMu sync.Mutex

// carrierAck 一次回执的内容（同时也是 last_ack 的落盘/响应形状）。
// 字段名与盒子侧 post_ack 的四键一字不差（改任何一侧必须同批改另一侧，
// qmt_gateway/carrier_uplink 那边的契约就是这四个名字）。
type carrierAck struct {
	StrategyVersion string `json:"strategy_version"` // 这台机器**实际装着**的那一版（不是清单说要的那版）
	ExecutorVersion string `json:"executor_version"` // 执行器版本；盒子读不到时如实报 "unknown"
	OK              bool   `json:"ok"`               // 盒子这一轮的结论：动作成功/被拒/失败
	Reason          string `json:"reason"`           // ASCII 短原因（up_to_date / applied / updater_abort …）
	At              string `json:"at"`               // 服务端收到该回执的时刻（RFC3339 北京串）
}

// carrierManifest 是账号目录里那份 JSON 的完整形态：**文件形态＝响应形态**，
// 出问题时直接 cat 那个文件就能和页面对上账（与 leg_status.json 同一取向）。
// 期望段（strategy_*/executor_*/updated_at）由发布侧写，回执段（request_state/
// requested_at/last_ack）由本文件写；两段互不覆盖。
type carrierManifest struct {
	// StrategyVersion 期望的内置策略版本（如 "v02-20261104"），与载体文件里的 STRATEGY_VERSION 同值域。
	StrategyVersion string `json:"strategy_version"`
	// StrategySha256 期望载体文件的完整 SHA256——盒子只把**验过哈希**的字节交给投递脚本，
	// 所以这一枚缺失/形状不对就等于"这一轮谁也不许动盘"。
	StrategySha256 string `json:"strategy_sha256"`
	// ExecutorVersion/ExecutorSha256 期望的本地执行器版本与哈希。
	// 执行器在本批是**只报不投**（carrier_updater._note_executor_diff 只点名差别，
	// 换执行器是 update_local_executor.ps1 那条独立腿）：所以这两枚缺失不阻塞策略更新。
	ExecutorVersion string `json:"executor_version"`
	ExecutorSha256  string `json:"executor_sha256"`
	// UpdatedAt 期望段最后一次被改动的时间（不是"盒子装好的时间"，两个时刻经常被读成一回事）。
	UpdatedAt string `json:"updated_at"`
	// RequestedAt 运营点「更新载体」的时刻；从没点过是 JSON null（不是零值串）。
	RequestedAt *string `json:"requested_at"`
	// RequestState idle / pending / acked / failed（读侧可能报 unknown，那个值不会落盘）。
	RequestState string `json:"request_state"`
	// LastAck 最近一次**被接受**的回执；从没收到过是 null。
	LastAck *carrierAck `json:"last_ack"`
}

// carrierMu 使用说明：下面三个函数都在调用方持锁的状态下读写同一个路径，
// 所以**不要**在这些函数里再加锁（自锁一次就把这条链整段挂住）。

// carrierManifestPath 拼出该账号的清单路径；cacheDir 或 uid 任一为空即返回空串。
// 空 uid 落盘会把不同账号的期望版本灌进同一个文件——那是"朋友的腿装着运营的版本"这种
// 事故的形状，宁可整条腿不工作。
func (s *Server) carrierManifestPath(uid string) string {
	if s.cacheDir == "" || strings.TrimSpace(uid) == "" {
		return ""
	}
	return filepath.Join(s.cacheDir, "accounts", uid, carrierManifestFileName)
}

// carrierDirUsable 判定这条腿在不在（目录建不出来＝写不了，直接如实报错而不是假装成功）。
func carrierDirUsable(path string) bool {
	return path != ""
}

// readCarrierManifest 读一份清单。返回 (doc, unknownReason)：
// doc==nil 时**任何字段都不存在**，调用方一律按"未知"处理（缺省成空壳再判"已一致"
// 就是本文件边界 2 要防的那次折叠）。
// reason 逐字面区分三种成因，因为它们对应的处置动作完全不同：
//
//	"manifest_absent"   ⇒ 发布侧还没给这个账号写过期望值（去补文件）；
//	"manifest_unreadable" ⇒ 文件在但读不出（磁盘/权限/半截写入）；
//	"manifest_bad_json"   ⇒ 文件内容不是合法 JSON（多半是手工编辑时漏了个括号）。
func readCarrierManifest(path string) (*carrierManifest, string) {
	if !carrierDirUsable(path) {
		return nil, "manifest_unavailable"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "manifest_absent"
		}
		return nil, "manifest_unreadable"
	}
	var doc carrierManifest
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, "manifest_bad_json"
	}
	return &doc, ""
}

// writeCarrierManifest 原子写（tmp + rename，0600 与账号目录里其他状态文件同权）。
// 半截 JSON 会被下一次读收成 manifest_bad_json，那一刻已知版本就等于弄丢了——
// 与 leg_status 同一处置：杜绝出现半态，而不是事后报错。
func writeCarrierManifest(path string, doc *carrierManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	body, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// normalizeCarrierState 把持久值收敛到已知四态：空串＝idle（从没点过），
// 不认识的串＝unknown（只进读侧，绝不写回）。
// 为什么不能"不认识就当 idle"：手改文件时写错一个值，页面会立刻显示"空闲、无需动作"，
// 而真实情况是"这份状态读不懂"——两者对值班是完全不同的下一步。
func normalizeCarrierState(v string) string {
	switch strings.TrimSpace(v) {
	case "":
		return carrierStateIdle
	case carrierStateIdle, carrierStatePending, carrierStateAcked, carrierStateFailed:
		return strings.TrimSpace(v)
	default:
		return carrierStateUnknown
	}
}

// carrierDesiredReady 判定"期望段填齐了没有"，返回 (ready, reason)。
// 只有版本与 SHA256 都在、且哈希形状合法才算 ready：
// 缺哈希＝盒子那一轮会 manifest_incomplete_no_version_or_sha；哈希形状不对＝会每轮下载再验不过。
// 两种都该在这一步就被拒绝，而不是让一根物理机每小时替我们复现一次。
func carrierDesiredReady(doc *carrierManifest) (bool, string) {
	if doc == nil {
		return false, "manifest_unknown"
	}
	ver := strings.TrimSpace(doc.StrategyVersion)
	sha := strings.TrimSpace(doc.StrategySha256)
	if ver == "" || sha == "" {
		return false, "desired_incomplete_no_version_or_sha"
	}
	if !carrierShaRe.MatchString(sha) {
		return false, "desired_sha256_malformed"
	}
	return true, ""
}

// carrierStamp 统一时间形态（RFC3339 + 北京时区）。
// 与 leg_status.SeenAt 同格式，两个字段才能放在同一屏上比先后。
func carrierStamp(t time.Time) string {
	return cntime.In(t).Format(time.RFC3339)
}

// ───────────────────────── 盒子侧：GET 清单 / POST 回执（钥匙鉴权） ─────────────────────────

// carrierManifestPayload 装配给盒子的响应体。**字段名就是跨机器契约**：
// 与 qmt_gateway/carrier_updater.py 里读的那几个键一一对应（ok / strategy_version /
// strategy_sha256 / executor_version / executor_sha256 / updated_at / request_state /
// requested_at / last_ack / unknown_reason）。
//
// 三条响应侧的取向：
//
//	① 期望段填齐才 ok=true；否则 ok=false + unknown_reason 点名——盒子按 REFUSED 收场、一个字节都不动；
//	② ok=false 时 request_state 一律 "unknown"（读不到期望态时，"运营点没点过"也无从谈起）；
//	③ 恒带 last_ack（无则为 null）：让盒子/排障的人能看见上一轮答没答上，不必猜。
func carrierManifestPayload(doc *carrierManifest, unknownReason string) map[string]interface{} {
	ready, readyReason := carrierDesiredReady(doc)
	// 组装顺序无关，但两个"不成立"必须都进 unknown_reason：清单本身读不出（IO/解析）
	// 与清单读得出但期望段没填齐，是两条不同的处置腿。
	reason := strings.TrimSpace(unknownReason)
	if reason == "" && !ready {
		reason = readyReason
	}
	out := map[string]interface{}{
		"ok":               reason == "",
		"source":           "carrier_manifest",
		"unknown_reason":   reason,
		"strategy_version": "",
		"strategy_sha256":  "",
		"executor_version": "",
		"executor_sha256":  "",
		"updated_at":       "",
		"requested_at":     nil,
		"last_ack":         nil,
		"request_state":    carrierStateUnknown,
		"has_artifact_url": false, // 本批不供字节；盒子据此走本机重烘那条兜底腿
		"brain_ack_schema": "carrier-ack-v1",
	}
	if doc == nil {
		return out
	}
	out["strategy_version"] = doc.StrategyVersion
	out["strategy_sha256"] = doc.StrategySha256
	out["executor_version"] = doc.ExecutorVersion
	out["executor_sha256"] = doc.ExecutorSha256
	out["updated_at"] = doc.UpdatedAt
	out["requested_at"] = doc.RequestedAt
	out["last_ack"] = doc.LastAck
	if reason == "" {
		out["request_state"] = normalizeCarrierState(doc.RequestState)
	}
	return out
}

// handleCarrierManifestGet 处理 GET /api/qmt/carrier/manifest（盒子侧本地进程调用，钥匙鉴权）。
// 归属完全来自 qmtReportMiddleware 用那把 token 反查出的账号；查询串里任何 user_id 都不看。
// 这是**只读**路由：一次轮询不改变任何状态——幂等性很重要，盒子按 cron 一小时可以跑好几次，
// 而"点一次「更新载体」"应当只被运营那一次点击置位，不能被随后的 N 次轮询重复消费。
func (s *Server) handleCarrierManifestGet(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	if uid == "" {
		// 中间件结构上保证了非空；留这条是 fail-closed 的兜底，绝不能退化成"落到运营账号"。
		writeError(w, http.StatusForbidden, "无法确定账号（网关钥匙未解析出归属）")
		return
	}
	// 读侧不加锁：只有写侧需要串行（原子 rename 保证读者不会读到半截文件）。
	doc, reason := readCarrierManifest(s.carrierManifestPath(uid))
	writeJSON(w, 200, carrierManifestPayload(doc, reason))
}

// carrierAckReq 是 POST /api/qmt/carrier/ack 的请求体（四键与盒子侧同名）。
// 三个指针字段用来区分"没带这个键"与"带了但值是空的/假的"——
// 回执语义里这两者完全不同：`ok:false` 是一次失败的回答，缺 `ok` 是一份没法落的东西。
type carrierAckReq struct {
	StrategyVersion *string `json:"strategy_version"`
	ExecutorVersion *string `json:"executor_version"`
	OK              *bool   `json:"ok"`
	Reason          *string `json:"reason"`
	// UserID 是**收下但永不采信**的字段（§安全 T1）：归属只由钥匙决定。
	// 保留它（而不是报错）是为了兼容旧脚本；不一致时打一行日志，让"有人以为能指名"这件事留痕。
	UserID string `json:"user_id,omitempty"`
}

// sanitizeCarrierText 去掉控制字符与首尾空白。
// 为什么不截断而是拒绝（调用方判超长即 400）：这些值会进日志与 JSON，
// 一个被悄悄剪短的版本号看起来完全合法，而它对应的真实文件是另一个。
func sanitizeCarrierText(v string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' ' // 控制字符（含换行/制表）一律换成空格：日志锚点必须是一行
		}
		return r
	}, v)
}

// handleCarrierAckPost 处理 POST /api/qmt/carrier/ack（盒子侧，钥匙鉴权）。
// 状态机只有一条推进腿（见文件头边界 3）：**pending 且回执版本==期望版本**才动 request_state，
// 其余情形只更新 last_ack。理由写在下面的分支注释里。
func (s *Server) handleCarrierAckPost(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	if uid == "" {
		writeError(w, http.StatusForbidden, "无法确定账号（网关钥匙未解析出归属）")
		return
	}
	var req carrierAckReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&req); err != nil {
		// 解析不了就一条都不写：旧回执是证据，不能因为这一次 body 坏了就被覆盖成垃圾。
		writeError(w, http.StatusBadRequest, "invalid ack body: "+err.Error())
		return
	}
	if req.UserID != "" && req.UserID != uid {
		log.Printf("[carrier] 回执体 user_id=%s 与钥匙归属 %s 不一致，按钥匙归属（§安全 T1：自报身份一律不信）",
			req.UserID, uid)
	}
	// 三个必带项逐一校验（缺哪个就点名哪个，别让调用方猜）：
	// 缺 ok 的那份 body 是"没有结论的回执"，落进去会让页面显示一条看起来像结论的记录。
	if req.OK == nil {
		writeError(w, http.StatusBadRequest, "ack 必须带 ok（true/false 之一；缺键表示没有结论，没法记录）")
		return
	}
	ver := sanitizeCarrierText(derefStr(req.StrategyVersion))
	if ver == "" {
		writeError(w, http.StatusBadRequest, "ack 必须带非空 strategy_version（回执说的是这台机器装着哪一版）")
		return
	}
	// 形状校验：盒子报的版本来自它正在读的载体文件，带空白/斜杠的版本说明读歪了，
	// 这种值一旦记进 last_ack，之后所有按版本比对的判定都会静默失真。
	if !carrierVersionRe.MatchString(ver) || len(ver) > carrierVersionMaxLen {
		writeError(w, http.StatusBadRequest, "strategy_version 形状非法（只允许 [A-Za-z0-9._-]，长度 ≤"+fmt.Sprint(carrierVersionMaxLen)+"）")
		return
	}
	execVer := sanitizeCarrierText(derefStr(req.ExecutorVersion))
	if len(execVer) > carrierVersionMaxLen {
		writeError(w, http.StatusBadRequest, "executor_version 超长（>"+fmt.Sprint(carrierVersionMaxLen)+"）")
		return
	}
	// 空 reason 合法（盒子全成功时也可能无话可说），但形状与长度必须有界。
	reason := sanitizeCarrierText(derefStr(req.Reason))
	if len(reason) > carrierReasonMaxLen {
		writeError(w, http.StatusBadRequest, "reason 超长（>"+fmt.Sprint(carrierReasonMaxLen)+"）")
		return
	}

	path := s.carrierManifestPath(uid)
	if !carrierDirUsable(path) {
		// 账号目录不可用＝这本账没地方记。503 而不是 200：盒子按"回执失败"重试比按"回执成功"
		// 把这一轮忘掉安全（后者会让页面显示一次根本没落地的 acked）。
		writeError(w, http.StatusServiceUnavailable, "carrier manifest unavailable（账号目录未装配）")
		return
	}

	carrierMu.Lock()
	defer carrierMu.Unlock()
	// 写侧走"读-改-写"：期望段（谁发布的哪一版）绝不能被回执那一段覆盖掉。
	// 文件缺失时这里**新建**一份只含回执段的壳：盒子在期望值还没填齐时按设计不会发回执，
	// 真收到了（旧版本盒子/手工调用）就照实记下它的回答，同时保持 desired_ready=false。
	doc, readReason := readCarrierManifest(path)
	if doc == nil {
		doc = &carrierManifest{RequestState: carrierStateIdle}
	}
	now := cntime.Now()
	ack := &carrierAck{
		StrategyVersion: ver,
		ExecutorVersion: execVer,
		OK:              *req.OK,
		Reason:          reason,
		At:              carrierStamp(now),
	}
	doc.LastAck = ack

	// ── 状态推进：唯一一条腿 pending → acked/failed，且必须答的是当前这一版 ──
	// 为什么不放开这条腿：运营把期望版升到 v03 之后，盒子那条**上一轮 v02** 的迟到回执若能把
	// pending 吃掉，v03 这一次点击就永远显示"已确认"，而这台机器实际上从没被要求换版——
	// 看起来像"已生效"的死格比一个明显悬着的 pending 更危险。
	stateChanged := false
	newState := normalizeCarrierState(doc.RequestState)
	if normalizeCarrierState(doc.RequestState) == carrierStatePending && strings.TrimSpace(doc.StrategyVersion) == ver {
		if *req.OK {
			newState = carrierStateAcked
		} else {
			newState = carrierStateFailed
		}
		stateChanged = true
	}
	doc.RequestState = newState
	if err := writeCarrierManifest(path, doc); err != nil {
		// 落盘失败必须响亮：回执是"这台机器答了话"的唯一凭据，丢了就等于那次点击没有下文。
		log.Printf("[carrier] 回执落盘失败(用户=%s): %v", uid, err)
		writeError(w, 500, "persist carrier ack failed: "+err.Error())
		return
	}
	note := ""
	switch {
	case stateChanged:
		note = "state_advanced_from_pending"
	case normalizeCarrierState(doc.RequestState) == carrierStatePending:
		// pending 没动：说明这轮回执答的不是当前期望版本（或期望段还没填齐）。
		note = "ack_kept_pending_version_differs"
	default:
		note = "ack_recorded_without_pending"
	}
	opslog.Audit("carrier_ack", uid, uid, fmt.Sprintf("version=%s ok=%v reason=%s state=%s changed=%v read=%s",
		ver, *req.OK, reason, doc.RequestState, stateChanged, readReason))
	writeJSON(w, 200, map[string]interface{}{
		"ok":            true,
		"request_state": doc.RequestState,
		"state_changed": stateChanged,
		"last_ack":      ack,
		"note":          note,
	})
}

// derefStr 取字符串指针的值（nil ⇒ 空串）；只在"缺键与空串都不需要区分"的地方用。
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ───────────────────────── 运营侧：点一下「更新载体」/ 上屏读数 ─────────────────────────

// carrierUpdateReq POST /api/qmt/carrier/update 的请求体。
// UserID 在这里**是**有效字段——但只在"跨账号代点"时，且必须另持 user_admin（见 handler）。
// 这与回执侧的 user_id 完全不同方向：这里是人在量仔里替别人点名，那里是机器自报身份。
type carrierUpdateReq struct {
	UserID string `json:"user_id,omitempty"`
}

// handleCarrierUpdatePost 处理 POST /api/qmt/carrier/update（trading_admin；跨账号再加 user_admin）。
// 它做的**只有一件事**：把该账号清单里的 request_state 置成 pending、盖上请求时刻。
// 一个字节的载体文件都没换、一台机器都没被叫醒——响应必须把这句话说清楚，
// 因为盒子那一侧是"下一次轮询才接住"（收盘后的计划任务，或手工 --once），不是即时的。
func (s *Server) handleCarrierUpdatePost(w http.ResponseWriter, r *http.Request) {
	actor := requestUserID(r)
	if actor == "" {
		writeError(w, http.StatusForbidden, "无法确定账号")
		return
	}
	var req carrierUpdateReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		// 空 body 是合法调用（给自己点一下）；除此之外解析失败必须拒，
		// 否则会有一次"拼错字段名"的点击被当成"给运营自己点了一下"落进别人的账。
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	// 缺省只作用于**请求者自己那根腿**——成员点一下绝不会碰到别人的机器。
	target := strings.TrimSpace(req.UserID)
	if target == "" {
		target = actor
	}
	if target != actor {
		// 跨账号代点要"账号管理 + 交易管理"两位，与 /api/admin/users/{id}/config/qmt 同判据源：
		// 只持 trading_admin 的人能改所有账号的实盘参数，但不能凭这个端点去**重启别人家里**的策略。
		u := userFromContext(r)
		if u == nil || !u.HasExplicitPerm(auth.PermUserAdmin) {
			opslog.Audit("carrier_update_denied", actor, target, "cross_account_requires_user_admin+trading_admin")
			writeError(w, http.StatusForbidden, "跨账号更新载体需要 user_admin + trading_admin 两位")
			return
		}
	}
	if s.auth.UserByID(target) == nil {
		writeError(w, http.StatusNotFound, "目标账号不存在: "+target)
		return
	}
	path := s.carrierManifestPath(target)
	if !carrierDirUsable(path) {
		writeError(w, http.StatusServiceUnavailable, "carrier manifest unavailable（数据目录未装配）")
		return
	}

	carrierMu.Lock()
	defer carrierMu.Unlock()
	doc, readReason := readCarrierManifest(path)
	ready, readyReason := carrierDesiredReady(doc)
	if !ready {
		// **拒，而不是置 pending**。期望段没填齐时置 pending 的后果是：盒子下一次轮询拿到
		// ok=false 直接 REFUSED，那一次点击既没产生动作也没产生回执，状态会一直悬在 pending，
		// 看起来像"盒子没响应"。真实缺口在发布侧（这份清单里没写该装哪一版），
		// 所以这里当场把它说破，并把该补的东西逐条列出来。
		why := readReason
		if why == "" {
			why = readyReason
		}
		opslog.Audit("carrier_update_refused", actor, target, "desired_incomplete:"+why)
		// 提示里只给**账号目录内的相对位置**（不给数据目录绝对路径，也不给任何凭证）：
		// 发布侧要改的就是这一个文件，报相对位置足够定位，而绝对路径会把服务器目录结构
		// 抄进每一条 409 响应里（§F-13 同口径：响应不是日志，不该带出主机形状）。
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"ok":      false,
			"reason":  why,
			"updated": false,
			"file":    "accounts/" + target + "/" + carrierManifestFileName,
			"message": "该账号的载体清单还没写明「该装哪一版」（strategy_version / strategy_sha256 缺或哈希形状非法），" +
				"已拒绝置位：请发布侧先补齐 accounts/" + target + "/" + carrierManifestFileName +
				" 再点一次。现在点只会让盒子那一轮按「清单不完整」拒绝执行，一个字节都不动。",
		})
		return
	}
	if doc == nil { // carrierDesiredReady 已保证不会到这里，留作结构兜底（nil 解引用一次就把整条链打死）
		writeError(w, 500, "carrier manifest vanished between read and write")
		return
	}
	now := cntime.Now()
	stamp := carrierStamp(now)
	// 先记下"从哪儿变过来的"再改：置位之后再问原值就问不出来了（那是回显里唯一有信息量的部分）。
	prev := normalizeCarrierState(doc.RequestState)
	doc.RequestedAt = &stamp
	doc.RequestState = carrierStatePending
	if err := writeCarrierManifest(path, doc); err != nil {
		log.Printf("[carrier] 置 pending 失败(用户=%s): %v", target, err)
		writeError(w, 500, "persist carrier request failed: "+err.Error())
		return
	}
	opslog.Audit("carrier_update_request", actor, target, fmt.Sprintf("strategy_version=%s executor_version=%s requested_at=%s",
		doc.StrategyVersion, doc.ExecutorVersion, stamp))
	// 响应里的 "updated": false 是给前端/脚本读的那句话——**这一笔没有更新任何东西**，
	// 只登记了一次请求。任何把它渲染成"已更新"的调用方都在这里被明确否认。
	writeJSON(w, 200, map[string]interface{}{
		"ok":                 true,
		"updated":            false,
		"request_state":      carrierStatePending,
		"requested_at":       stamp,
		"target_user_id":     target,
		"strategy_version":   doc.StrategyVersion,
		"strategy_sha256":    doc.StrategySha256,
		"executor_version":   doc.ExecutorVersion,
		"prev_request_state": prev,
		"when":               "该账号那台物理机上的本地进程下一次轮询时接住（收盘后的计划任务，或手工 --once）",
		"message": "已记下这次请求（request_state=pending）。此刻**还没有任何东西被更新**：广州只登记了一次意图，" +
			"停策略、写文件、重启三件事都由那台机器自己的脚本在轮询到时完成，做完才回执。",
	})
}

// carrierReadout 是并进 GET /api/qmt/state 的那一键（**纯增量**，与 leg_status 同一取向）：
// 只给读数，不参与熔断、不参与任何资金/持仓判定。
// 为什么把"装的≠跑的"也放进来（installed_matches_desired）：这一屏的唯一用途就是回答
// "点了之后到底落下去没有"，让前端各拉一份 leg_status 再自己比，会把比对逻辑复制成两份。
type carrierReadout struct {
	// Known 清单是否读得出（false ⇒ 下面所有期望段字段都不可信）。
	Known bool `json:"known"`
	// UnknownReason 读不出/没填齐的点名原因（健康时为空串）。
	UnknownReason string `json:"unknown_reason"`
	// RequestState idle/pending/acked/failed，或读侧专属的 unknown。
	RequestState string `json:"request_state"`
	// DesiredReady 期望段填齐（版本 + 合法哈希）——决定「更新载体」这个按钮点了有没有意义。
	DesiredReady bool `json:"desired_ready"`
	// Want 四键：该装什么。
	StrategyVersion string `json:"strategy_version"`
	StrategySha256  string `json:"strategy_sha256"`
	ExecutorVersion string `json:"executor_version"`
	ExecutorSha256  string `json:"executor_sha256"`
	// UpdatedAt 期望段最后改动的时间；RequestedAt 最近一次点击（null＝从没点过）。
	UpdatedAt   string  `json:"updated_at"`
	RequestedAt *string `json:"requested_at"`
	// LastAck 最近一次被接受的回执（null＝盒子从没答过话）。
	LastAck *carrierAck `json:"last_ack"`
	// Installed 上行腿报回来的"这台机器装着什么"（null＝还没有任何回报读数）。
	Installed *legStatus `json:"installed"`
	// InstalledMatchesDesired 三态：null=判不了（任一侧读不到），true/false 才是结论。
	// 折成 false 会让"版本对不上"与"还不知道"共用一句话，折成 true 更危险——
	// 所以这里刻意用指针。
	InstalledMatchesDesired *bool `json:"installed_matches_desired"`
	// AckIsStale 回执里报的版本 ≠ 当前期望版本（迟到回执/装了别的版）：只点名，不改状态。
	AckIsStale bool `json:"ack_is_stale"`
}

// carrierStateReadout 装配该账号的载体读数（uid 为空 ⇒ 整键按未知处理，不落到运营账号）。
func (s *Server) carrierStateReadout(uid string) *carrierReadout {
	out := &carrierReadout{
		Known:         false,
		UnknownReason: "uid_absent",
		RequestState:  carrierStateUnknown,
	}
	if strings.TrimSpace(uid) == "" {
		return out
	}
	doc, readReason := readCarrierManifest(s.carrierManifestPath(uid))
	ready, readyReason := carrierDesiredReady(doc)
	out.Known = doc != nil && readReason == ""
	// 两个原因只报第一个非空：清单读不出时，"期望段没填齐"是同一个事实的第二种说法，
	// 并列展示会让人以为是两个故障。
	out.UnknownReason = firstNonEmptyStr(readReason, readyReason)
	out.DesiredReady = ready
	// 状态先收敛一次再判：doc 为 nil 时读它的字段会当场 panic，而"清单读不出"正是常态之一。
	state := carrierStateUnknown
	if doc != nil {
		state = normalizeCarrierState(doc.RequestState)
		out.StrategyVersion = doc.StrategyVersion
		out.StrategySha256 = doc.StrategySha256
		out.ExecutorVersion = doc.ExecutorVersion
		out.ExecutorSha256 = doc.ExecutorSha256
		out.UpdatedAt = doc.UpdatedAt
		out.RequestedAt = doc.RequestedAt
		out.LastAck = doc.LastAck
	}
	// pending 但期望段读不齐（比如手工把文件里的版本清掉了）：状态照实回显，
	// 另外补一句原因——否则页面只剩一个孤零零的 pending，看不出它永远等不到答复。
	if state == carrierStatePending && !ready {
		out.UnknownReason = firstNonEmptyStr(out.UnknownReason, "pending_without_desired")
	}
	out.RequestState = state
	// 在跑的那一份来自上行腿（leg_status.json）——它由回报驱动，与本清单互不覆盖。
	leg := s.readLegStatus(uid)
	out.Installed = leg
	if leg != nil && strings.TrimSpace(leg.StrategyVersion) != "" && strings.TrimSpace(out.StrategyVersion) != "" {
		m := strings.TrimSpace(leg.StrategyVersion) == strings.TrimSpace(out.StrategyVersion)
		out.InstalledMatchesDesired = &m
	}
	// 迟到回执只点名、不改状态（状态推进的判定在回执那一路已经做过，这里不再判第二次）。
	if out.LastAck != nil && strings.TrimSpace(out.StrategyVersion) != "" {
		out.AckIsStale = strings.TrimSpace(out.LastAck.StrategyVersion) != strings.TrimSpace(out.StrategyVersion)
	}
	return out
}
