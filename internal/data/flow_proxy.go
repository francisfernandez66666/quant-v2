// flow_proxy.go — §FLOW-MONEY 资金流取数中继客户端（决策机侧，2026-11-19）。
//
// 为什么要有它：广州对东财全族在 TLS 层被 RST（2026-11-19 实测——push2 / push2his /
// push2delay 的 /api/qt/* 一律 Empty reply from server；当日熔断日志按路径去重 3495 次，
// 日K 734 / fflow 521 / clist 476+383 / 指数点位 412+373，四条腿全断，系统靠腾讯/新浪兜底），
// 而盒子（Win 物理机）实测可达。owner 裁决"资金路选 A"：由盒子代取，广州经既有
// WireGuard HTTP 通道（10.77.0.2:8789，Bearer 同一把钥匙）来拿。
//
// 三条纪律（改动前必读）：
//  1. 只走 /flow/money 这一条白名单中继，**不在这里另写一份东财 URL**：参数面
//     （secid/klt/lmt/fs/fields/pn/pz）的校验真源在盒子侧 flow_proxy.py，两边各写一份必然漂开。
//  2. 上游 body **原样**交给既有解析器（parseMoneyFlowHistory / parseSectorList），
//     本文件不新增任何字段映射——形状只有一份，就不会出现"两边各长一版"的错位。
//  3. 失败即弃权（返回 error），绝不拿 0 或旧值冒充资金流；未装配代理时调用方走原直连腿。
//
// English: client for the box-side /flow/money relay (allow-listed EastMoney passthrough).
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// flowRelayEnvelope 是盒子 /flow/money 的响应外壳：upstream_body 是东财原始 body 字符串。
type flowRelayEnvelope struct {
	OK             bool   `json:"ok"`
	UpstreamStatus int    `json:"upstream_status"`
	UpstreamBody   string `json:"upstream_body"`
	Src            string `json:"src"`
	Cache          string `json:"cache"`
	Err            string `json:"err"`
}

// FlowProxy 是盒子网关资金流中继的只读客户端（并发安全，可长期复用）。
type FlowProxy struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewFlowProxy 构造中继客户端；baseURL 为空或 token 为空时返回 nil（调用方据此保持直连腿）。
func NewFlowProxy(baseURL, token string) *FlowProxy {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	if baseURL == "" || token == "" {
		return nil
	}
	return &FlowProxy{
		baseURL: baseURL,
		token:   token,
		// 超时略短于引擎 60s 资金流 TTL：宁可这一轮弃权，也不能让一次取数卡住整个装配。
		client: &http.Client{Timeout: 12 * time.Second},
	}
}

// relay 取一次中继响应并解包上游 body（HTTP 非 200 或 ok=false 一律当失败）。
func (p *FlowProxy) relay(query url.Values) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("flow proxy: not configured")
	}
	endpoint := p.baseURL + "/flow/money?" + query.Encode()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("flow proxy: build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flow proxy: http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("flow proxy: read: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("flow proxy: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var env flowRelayEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("flow proxy: decode envelope: %v", err)
	}
	if !env.OK {
		return nil, fmt.Errorf("flow proxy: upstream not ok (status=%d err=%s)", env.UpstreamStatus, env.Err)
	}
	if strings.TrimSpace(env.UpstreamBody) == "" {
		return nil, fmt.Errorf("flow proxy: empty upstream body")
	}
	return []byte(env.UpstreamBody), nil
}

// IndexMoneyFlow 经中继取 fflow 序列（secid 必须显式：大盘 1.000001、板块 90.BKxxxx）。
// klt=1 → 当日分时（末根=当日累计）；klt=101 → 逐日历史。解析复用 parseMoneyFlowHistory。
func (p *FlowProxy) IndexMoneyFlow(secid string, klt, lmt int) ([]FlowDay, error) {
	q := url.Values{}
	q.Set("kind", "fflow")
	q.Set("secid", secid)
	q.Set("klt", fmt.Sprintf("%d", klt))
	q.Set("lmt", fmt.Sprintf("%d", lmt))
	body, err := p.relay(q)
	if err != nil {
		return nil, err
	}
	return parseMoneyFlowHistory(body, secid)
}

// IndexMoneyFlowToday 大盘（上证）当日累计主力净额（元）——与 MarketAPI 同名方法逐字同口径
// （klt=1 分时序列取末根）。两处必须同口径：一边取末根、一边取全序列，读数会差出一天。
func (p *FlowProxy) IndexMoneyFlowToday() (*FlowDay, error) {
	series, err := p.IndexMoneyFlow(indexSecID("000001"), emFFlowKLType, emFFlowLimitAll)
	if err != nil || len(series) == 0 {
		if err == nil {
			err = fmt.Errorf("flow proxy: empty index money flow series")
		}
		return nil, err
	}
	last := series[len(series)-1]
	return &last, nil
}

// IndexMoneyFlowHistory 大盘（上证）逐日主力净额历史（元），最近 n 个交易日。
func (p *FlowProxy) IndexMoneyFlowHistory(n int) ([]FlowDay, error) {
	return p.IndexMoneyFlow(indexSecID("000001"), emFFlowDayKLType, n)
}

// SectorMoneyFlowHistory 板块（BK 代码）逐日主力净额历史（元）。secid 显式 "90.<bk>"。
func (p *FlowProxy) SectorMoneyFlowHistory(bkCode string, n int) ([]FlowDay, error) {
	if bkCode == "" {
		return nil, fmt.Errorf("flow proxy: empty bk code")
	}
	return p.IndexMoneyFlow("90."+bkCode, emFFlowDayKLType, n)
}

// SectorList 经中继取行业板块列表（fs=m:90+t:2），逐页取全。
// 契约与镜像腿同一条：**要么返回完整全量，要么返回错误**——绝不把部分结果当完整列表
// 返回（"静默截断"正是当初把 pz>100 排除在转移之外要防的形态）。页数硬顶沿用
// emSectorListMaxPages，上游 total 异常时宁可报错也不做无界请求。
// 页大小用 emMirrorMaxPageSize(100)：上游对 pz 有 100 硬顶，请求 500 也只回 100
// （2026-11-19 盒子实测 total=496 returned=100），按 500 判"短页"会把第一页就当成末页。
func (p *FlowProxy) SectorList(fs string) ([]SectorInfo, error) {
	if p == nil {
		return nil, fmt.Errorf("flow proxy: not configured")
	}
	if fs == "" {
		fs = "m:90+t:2"
	}
	seen := make(map[string]bool, emMirrorMaxPageSize*5)
	out := make([]SectorInfo, 0, emMirrorMaxPageSize*5)
	total := -1
	for page := 1; page <= emSectorListMaxPages; page++ {
		q := url.Values{}
		q.Set("kind", "clist")
		q.Set("fs", fs)
		q.Set("fields", sectorListFields)
		q.Set("pn", fmt.Sprintf("%d", page))
		q.Set("pz", fmt.Sprintf("%d", emMirrorMaxPageSize))
		body, err := p.relay(q)
		if err != nil {
			return nil, err
		}
		rows, perr := parseSectorList(body)
		if perr != nil {
			return nil, fmt.Errorf("flow proxy: parse page %d: %v", page, perr)
		}
		if page == 1 {
			total = parseSectorListTotal(body)
		}
		for _, s := range rows {
			if s.Code == "" || seen[s.Code] {
				continue
			}
			seen[s.Code] = true
			out = append(out, s)
		}
		if len(rows) == 0 {
			break
		}
		if total > 0 && len(out) >= total {
			break
		}
		if len(rows) < emMirrorMaxPageSize {
			// 该页未满却仍未凑齐 total ⇒ 上游分页语义变了，宁可报错也不返回带空洞的列表。
			if total > 0 && len(out) < total {
				return nil, fmt.Errorf("flow proxy: page %d short (%d rows) but only %d/%d collected",
					page, len(rows), len(out), total)
			}
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("flow proxy: empty sector list")
	}
	if total > 0 && len(out) < total {
		return nil, fmt.Errorf("flow proxy: incomplete sector list %d/%d", len(out), total)
	}
	return out, nil
}
