// emotion_matrix.go — §情绪×战法矩阵（2026-09-13 情绪面板 B 档）。
//
// 把 backtest_event_results（候选逐事件回测断点，result_json=EventResult）按
// "候选战法 × 当日情绪相位" 分桶聚合：每桶给出事件数、H 日平均超额、命中率、
// 平均涨停家数。相位标注优先读 market_risk_daily 的引擎判定（当日最新一轮快照），
// 缺失日回退 EmotionStatsRange + PhaseFromEmotionStat（涨停家数×最高连板口径）现算——
// 与 engine 判定同阈值，避免双口径漂移。
//
// 性能护栏：单桶事件数 < minEvents 的格子标记 thin（前端降透明度），防"3 个事件算出
// 5% 超额"被当成可靠结论（W7 样本纪律在分相回测上的同款问题）。
// §ADJ-BASIS（2026-09-23）：聚合前先按**复权口径位**过滤 backtest_event_results——本聚合跨
// 全部候选/全部事件日扫整表，不过滤就会把改前（空串口径）与改后的数值平均进同一格。
// English: emotion×strategy matrix. Groups cached per-event backtest results (B4 chain cache)
// by candidate × daily sentiment phase. Phase labels prefer market_risk_daily, falling back to
// EmotionStatsRange + PhaseFromEmotionStat (same thresholds). Cells under minEvents are flagged
// "thin" so small-sample buckets don't masquerade as conclusions. Only rows written on the caller's
// adjustment basis are aggregated.
package store

import (
	"encoding/json"
	"fmt"
	"log"
)

// EmotionCell 矩阵一格：某候选在某情绪相位下的分桶统计。
type EmotionCell struct {
	Phase      string          `json:"phase"`        // 冰点/启动/发酵/高潮/退潮/背离（背离需实时广度，历史回退不产出该档）
	Events     int             `json:"events"`       // 桶内事件数
	Thin       bool            `json:"thin"`         // 样本不足（Events < minEvents）
	AvgExcess  map[int]float64 `json:"avg_excess"`   // horizon → 平均超额%
	HitRate    map[int]float64 `json:"hit_rate"`     // horizon → 平均命中率%（0-100）
	AvgLimitUp float64         `json:"avg_limit_up"` // 桶内平均板块涨停家数
}

// EmotionStrategyRow 矩阵一行：一个候选战法的全部相位分桶。
type EmotionStrategyRow struct {
	CandidateID int64         `json:"candidate_id"`
	Kind        string        `json:"kind"` // factor | pattern | ...
	Name        string        `json:"name"` // 展示名（reason 首行截断或 kind#id）
	Horizons    []int         `json:"horizons"`
	Cells       []EmotionCell `json:"cells"` // 按六相位固定顺序输出（无数据的相位省略）
	TotalEvents int           `json:"total_events"`
}

// EmotionMatrixRowMinEvents 单桶最小事件数：低于此标 thin（前端灰显）。
// 与 W7 扫参门槛同世界观：20 个以下事件的均值统计意义有限。
const EmotionMatrixRowMinEvents = 20

// emotionPhaseOrder 固定相位顺序（与 data.DetectEmotionPhaseV2 六阶段一致）。
var emotionPhaseOrder = []string{"冰点", "启动", "发酵", "高潮", "退潮", "背离"}

// emotionEventRow result_json 的轻量解析面（与 backtest.EventResult JSON 形状对齐；
// 本包不能 import backtest——会循环依赖，因此本地声明同名最小结构）。
type emotionEventRow struct {
	Date         string             `json:"date"` // YYYYMMDD
	MeanExcess   map[string]float64 `json:"mean_excess"`
	HitRate      map[string]float64 `json:"hit_rate"`
	LimitUpCount int                `json:"limit_up_count"`
}

// ListEmotionStrategyMatrix 聚合全部有断点缓存的候选 × 情绪相位矩阵。
// phaseByDate：market_risk_daily 的 trade_date(YYYY-MM-DD)→emotion 映射（server 层取好传入，
// 这里做 YYYYMMDD↔YYYY-MM-DD 归一）；fallbackPhase：历史缺失日的情绪现算器（可 nil=跳过该日）。
// adjBasis §ADJ-BASIS（2026-09-23）：本聚合**跨全部候选、跨全部事件日**扫整张
// backtest_event_results（旧实现连 WHERE 都没有），所以口径位必须进来当过滤条件——否则改前
// （空串哨兵）与改后两套数值会被混进同一个桶里平均，发布出去的就是假统计。
// 保守取向：adjBasis 为空（调用方没装配口径位）或口径位主键迁移被中止（降级，表里无法按口径
// 过滤）时**直接拒绝聚合**并报错，端点如实报错，而不是拿混装矩阵继续发布。
// 返回按事件总数降序的候选行。
//
// English: aggregates the candidate × sentiment-phase matrix. Because this scan spans every
// candidate and every event date, it must filter on the adjustment basis: without the filter,
// pre-fix (empty-basis) and post-fix numbers would be averaged into the same published cell. An unset basis
// or a degraded table refuses to aggregate instead of publishing mixed statistics.
func (d *DB) ListEmotionStrategyMatrix(phaseByDate map[string]string, fallbackPhase func(dateStr8 string) string, adjBasis string) ([]EmotionStrategyRow, error) {
	if adjBasis == "" {
		return nil, fmt.Errorf("store: 情绪×战法矩阵拒绝聚合——复权口径位未装配（adj_basis 空串是「改前旧证据行」的哨兵值，无法判定哪些行属于当前口径）")
	}
	if d.EventBasisDegraded() {
		return nil, fmt.Errorf("store: 情绪×战法矩阵拒绝聚合——backtest_event_results 口径位主键未生效（%s），该表无法按口径过滤，混装旧口径行发布会产出假统计",
			d.eventBasisReason)
	}
	// 被口径过滤掉的旧证据行数（>0 即说明"过滤改变了发布数值"）：一次性 WARN，讲清楚
	// 本次是在哪个口径上算的、排除了多少改前行。矩阵是面板端点，每请求都打会刷屏。
	var excluded int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM backtest_event_results WHERE adj_basis <> ?`, adjBasis).Scan(&excluded); err != nil {
		return nil, err
	}
	if excluded > 0 {
		d.eventBasisWarnMatrix.Do(func() {
			log.Printf("[store] WARN §ADJ-BASIS 情绪×战法矩阵已按复权口径过滤：adj_basis=%q，本轮排除 %d 条非当前口径行（含改前旧证据行 ''），矩阵数值相对口径进键前会变化；后续同口径请求不再重复告警",
				adjBasis, excluded)
		})
	}
	rows, err := d.db.Query(`SELECT candidate_id, event_date, result_json FROM backtest_event_results WHERE adj_basis = ?`, adjBasis)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 分桶累加器：bucket=某候选某相位的样本袋（excess/hit 按 horizon 分列存原始值，
	// 均值在输出段求，避免流式更新时的舍入漂移）；candAgg=一个候选的全部相位 + 事件总数。
	type bucket struct {
		count     int
		excessByH map[string][]float64
		hitByH    map[string][]float64
		limitUp   float64
	}
	type candAgg struct {
		buckets map[string]*bucket
		total   int
		hz      map[string]bool
	}
	// 主聚合循环：逐行读断点缓存 → 定相位 → 落入对应候选的相位袋
	aggs := map[int64]*candAgg{}
	for rows.Next() {
		var cid int64
		var date, js string
		if err := rows.Scan(&cid, &date, &js); err != nil {
			return nil, err
		}
		var er emotionEventRow
		if err := json.Unmarshal([]byte(js), &er); err != nil {
			continue // 单行解析失败跳过（历史脏数据防御），不阻断整矩阵
		}
		// 事件日取传入 date 优先（与库主键一致）。相位映射容忍两种键格式：
		// YYYYMMDD（backtest 侧）与 YYYY-MM-DD（market_risk_daily 侧），server 层构建时双写。
		d8 := date
		if len(d8) == 10 {
			d8 = d8[:4] + d8[5:7] + d8[8:10] // YYYY-MM-DD → YYYYMMDD
		}
		phase := phaseByDate[d8]
		if phase == "" {
			phase = phaseByDate[date] // 双格式兼容（date 保持原样再试一次）
		}
		if phase == "" && fallbackPhase != nil {
			phase = fallbackPhase(d8)
		}
		if phase == "" {
			continue // 无法定性的日期跳过（数据缺口宁可少一桶，不瞎归档）
		}
		// 懒建"候选×相位"两级袋：首次遇到该候选/相位先建空桶，再累加样本
		ca := aggs[cid]
		if ca == nil {
			ca = &candAgg{buckets: map[string]*bucket{}, hz: map[string]bool{}}
			aggs[cid] = ca
		}
		b := ca.buckets[phase]
		if b == nil {
			b = &bucket{excessByH: map[string][]float64{}, hitByH: map[string][]float64{}}
			ca.buckets[phase] = b
		}
		b.count++
		b.limitUp += float64(er.LimitUpCount)
		ca.total++
		for h, v := range er.MeanExcess {
			b.excessByH[h] = append(b.excessByH[h], v)
			ca.hz[h] = true
		}
		for h, v := range er.HitRate {
			b.hitByH[h] = append(b.hitByH[h], v)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 候选元信息（kind + 展示名）一次取全
	cands, err := d.ListCandidates("")
	if err != nil {
		return nil, err
	}
	meta := map[int64]Candidate{}
	for _, c := range cands {
		meta[c.ID] = c
	}

	// 输出构建：每个候选按六相位固定顺序展开成行（无数据的相位省略，薄桶带 thin 标记）
	out := make([]EmotionStrategyRow, 0, len(aggs))
	for cid, ca := range aggs {
		row := EmotionStrategyRow{CandidateID: cid}
		if c, ok := meta[cid]; ok {
			row.Kind = c.Kind
			row.Name = candidateDisplayName(c)
		} else {
			row.Name = "候选 #" + itoa64(cid) // 候选已被删除：保留数字身份
		}
		row.TotalEvents = ca.total
		row.Horizons = horizonsOf(ca.hz)
		for _, ph := range emotionPhaseOrder {
			b := ca.buckets[ph]
			if b == nil {
				continue
			}
			cell := EmotionCell{
				Phase: ph, Events: b.count, Thin: b.count < EmotionMatrixRowMinEvents,
				AvgExcess: map[int]float64{}, HitRate: map[int]float64{},
			}
			for h := range b.excessByH {
				hi, ok := atoiKey(h)
				if !ok {
					continue
				}
				cell.AvgExcess[hi] = meanOf(b.excessByH[h])
				if hits := b.hitByH[h]; len(hits) > 0 {
					cell.HitRate[hi] = meanOf(hits)
				}
			}
			cell.AvgLimitUp = b.limitUp / float64(b.count)
			row.Cells = append(row.Cells, cell)
		}
		out = append(out, row)
	}
	// 事件多的候选排前面（更有统计价值的矩阵行先看到）
	sortRowsByTotalDesc(out)
	return out, nil
}

// candidateDisplayName 候选展示名：reason 首行（≤24 字符），空则 kind#id。
func candidateDisplayName(c Candidate) string {
	for _, line := range splitLines(c.Reason) {
		if line != "" {
			if len([]rune(line)) > 24 {
				return string([]rune(line)[:24]) + "…"
			}
			return line
		}
	}
	return c.Kind + "#" + itoa64(c.ID)
}

// meanOf 求均值（空切片返回 0；调用方保证非空）。
func meanOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// itoa 无 strconv 依赖的整数转字符串（候选 ID 均为正）。
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// atoiKey 解析 horizon 键（JSON 数字键反序列化为字符串，"1"/"5"/"10"）。
func atoiKey(s string) (int, bool) {
	n := 0
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// splitLines 按 \n 拆行（避免为一处使用 import strings 的重复：本文件统一走这里）。
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, trimCR(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, trimCR(s[start:]))
	return out
}

// trimCR 去掉行尾 \r（Windows 换行兼容）。
func trimCR(s string) string {
	for len(s) > 0 && s[len(s)-1] == '\r' {
		s = s[:len(s)-1]
	}
	return s
}

// horizonsOf 把 horizon 键集合转成升序 int 列表。
func horizonsOf(set map[string]bool) []int {
	out := make([]int, 0, len(set))
	for k := range set {
		if h, ok := atoiKey(k); ok {
			out = append(out, h)
		}
	}
	// 插入排序（horizon 数量 ≤4，写死避免 import sort 的杀鸡用牛刀）
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// sortRowsByTotalDesc 矩阵行按事件总数降序（同分按 ID 升序保证稳定输出）。
func sortRowsByTotalDesc(rows []EmotionStrategyRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, b := rows[j-1], rows[j]
			if a.TotalEvents < b.TotalEvents || (a.TotalEvents == b.TotalEvents && a.CandidateID > b.CandidateID) {
				rows[j-1], rows[j] = rows[j], rows[j-1]
			} else {
				break
			}
		}
	}
}
