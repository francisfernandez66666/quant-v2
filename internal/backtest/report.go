// 回测报告：JSON 数据 + HTML 展示。
package backtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"sort"
)

// Pick 单只入选股票的前瞻验证结果。
type Pick struct {
	Code       string          `json:"code"`
	Score      float64         `json:"score"`
	EntryDate  string          `json:"entry_date"`
	EntryPrice float64         `json:"entry_price"`
	Returns    map[int]float64 `json:"returns"` // horizon → 收益
	Excess     map[int]float64 `json:"excess"`  // horizon → 超额收益
}

// EventResult 单事件回测结果。
type EventResult struct {
	Date         string          `json:"date"`
	Industry     string          `json:"industry"`
	LimitUpCount int             `json:"limit_up_count"`
	Constituents int             `json:"constituents"`
	Picks        []Pick          `json:"picks"`
	MeanExcess   map[int]float64 `json:"mean_excess"`
	HitRate      map[int]float64 `json:"hit_rate"`
}

// ChainReport 全链路回测汇总。
type ChainReport struct {
	Start       string             `json:"start"`
	End         string             `json:"end"`
	Benchmark   string             `json:"benchmark"`
	Horizons    []int              `json:"horizons"`
	Rule        SignalRule         `json:"rule"`
	Events      []EventResult      `json:"events"`
	TotalEvents int                `json:"total_events"`
	TotalPicks  int                `json:"total_picks"`
	AvgExcess   map[int]float64    `json:"avg_excess"`  // horizon → 事件级平均超额
	OverallHit  map[int]float64    `json:"overall_hit"` // horizon → 股票级命中率
}

// Summarize 汇总全部事件的平均超额与命中率。
// （Summarize aggregates average excess return and hit rate across events.）
func (r *ChainReport) Summarize() {
	r.AvgExcess = map[int]float64{}
	r.OverallHit = map[int]float64{}
	for _, h := range r.Horizons {
		var sum float64
		var n int
		for _, e := range r.Events {
			if v, ok := e.MeanExcess[h]; ok {
				sum += v
				n++
			}
		}
		if n > 0 {
			r.AvgExcess[h] = sum / float64(n)
		}
		var ps, pw int
		for _, e := range r.Events {
			for _, p := range e.Picks {
				if v, ok := p.Excess[h]; ok {
					ps++
					if v > 0 {
						pw++
					}
				}
			}
		}
		if ps > 0 {
			r.OverallHit[h] = float64(pw) / float64(ps)
		}
	}
}

// JSONReport 序列化为 JSON（NaN→null）。
// （JSONReport marshals the chain report to JSON.）
func (r *ChainReport) JSONReport() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// HTMLReport 渲染自包含 HTML。
// （HTMLReport renders the chain report as a self-contained HTML page.）
func (r *ChainReport) HTMLReport() ([]byte, error) {
	type evRow struct {
		Date, Industry string
		LimitUp, Cons  int
		Mean5          float64
		HasMean        bool
	}
	var evs []evRow
	for _, e := range r.Events {
		m, ok := e.MeanExcess[5]
		evs = append(evs, evRow{e.Date, e.Industry, e.LimitUpCount, e.Constituents, m, ok})
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].Mean5 > evs[j].Mean5 })
	var buf bytes.Buffer
	if err := btTpl.Execute(&buf, struct {
		R   *ChainReport
		Evs []evRow
	}{r, evs}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func fv(v float64) string {
	if math.IsNaN(v) {
		return "—"
	}
	return fmt.Sprintf("%.4f", v)
}

var btTpl = template.Must(template.New("bt").Funcs(template.FuncMap{"fv": fv}).Parse(`<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>全链路回测报告</title>
<style>
body{font-family:system-ui,"PingFang SC",sans-serif;margin:24px;color:#222}
table{border-collapse:collapse;margin:10px 0;font-size:13px}
th,td{border:1px solid #ccc;padding:5px 9px;text-align:right}
th{background:#eef2f7}td.l{text-align:left}
.pos{color:#0a7a2a}.neg{color:#b00020}
h2{font-size:17px;border-bottom:2px solid #334;padding-bottom:4px}
</style></head><body>
<h1>全链路回测报告（板块涨停潮 → 多因子 → 前瞻验证）</h1>
<p>事件区间 {{.R.Start}} ~ {{.R.End}}，基准 {{.R.Benchmark}}，前瞻 {{range .R.Horizons}}{{.}}日 {{end}}</p>
<p>事件数 {{.R.TotalEvents}}，入选股票 {{.R.TotalPicks}}</p>
<h2>汇总（超额收益）</h2>
<table><tr><th>前瞻</th>{{range .R.Horizons}}<th>{{.}}日</th>{{end}}</tr>
<tr><td class="l">事件级平均超额</td>{{range .R.Horizons}}<td>{{fv (index $.R.AvgExcess .)}}</td>{{end}}</tr>
<tr><td class="l">股票级命中率</td>{{range .R.Horizons}}<td>{{fv (index $.R.OverallHit .)}}</td>{{end}}</tr>
</table>
<h2>事件明细（按 5 日超额降序）</h2>
<table><tr><th>事件日</th><th>行业</th><th>涨停</th><th>成分</th><th>5日超额</th></tr>
{{range .Evs}}<tr>
<td class="l">{{.Date}}</td><td class="l">{{.Industry}}</td>
<td>{{.LimitUp}}</td><td>{{.Cons}}</td>
<td class="{{if lt .Mean5 0.0}}neg{{else}}pos{{end}}">{{if .HasMean}}{{printf "%.4f" .Mean5}}{{else}}—{{end}}</td>
</tr>{{end}}</table>
</body></html>`))