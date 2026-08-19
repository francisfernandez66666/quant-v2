// 因子验证报告：JSON 数据 + HTML 展示。
// English: factor validation report: JSON data + HTML display.
package research

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"sort"

	"quant-trading-v2/internal/factor"
)

// FactorReport 单因子的验证汇总（供 B3 cmd/research 输出）。
// English: FactorReport aggregates one factor's validation for the B3 tool output.
// （FactorReport aggregates one factor's validation for the B3 tool output.）
type FactorReport struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Horizon  int    `json:"horizon"` // 前瞻天数
	// English: forward horizon in days.
	Quantiles int `json:"quantiles"` // 分层数
	// English: number of quantiles.
	MinStocks int `json:"min_stocks"` // 每日最小样本
	// English: minimum daily sample.
	IC []ICRow `json:"ic"` // 逐日 IC
	// English: daily IC.
	ICMean float64 `json:"ic_mean"` // IC 均值
	// English: IC mean.
	ICStd float64 `json:"ic_std"` // IC 标准差
	// English: IC standard deviation.
	IR float64 `json:"ir"` // 信息比率
	// English: information ratio.
	Layers []LayerSummary `json:"layers"` // 分层收益
	// English: layer returns.
	Monotonic bool `json:"monotonic"` // 是否单调
	// English: whether monotonic.
	MonotonicDir int `json:"monotonic_dir"` // +1 递增 / -1 递减 / 0 无
	// English: +1 increasing / -1 decreasing / 0 none.
}

// Summarize 对单个因子做完整验证汇总。
// English: Summarize runs the full validation pipeline for one factor.
// （Summarize runs the full validation pipeline for one factor.）
func Summarize(panels []*Panel, d factor.Def, start, end string, h, quantiles, minStocks int) *FactorReport {
	ics := ICByDate(panels, d.ID, h, minStocks)
	layers := LayerReturns(panels, d.ID, h, quantiles, minStocks)
	mono, dir := Monotonic(layers)
	return &FactorReport{
		ID: d.ID, Name: d.Name, Category: d.Cat.CategoryName(),
		Start: start, End: end, Horizon: h, Quantiles: quantiles, MinStocks: minStocks,
		IC:     ics,
		ICMean: meanIC(ics), ICStd: stdIC(ics), IR: IR(ics),
		Layers: layers, Monotonic: mono, MonotonicDir: dir,
	}
}

func meanIC(rows []ICRow) float64 {
	if len(rows) == 0 {
		return nan()
	}
	s := 0.0
	for _, r := range rows {
		s += r.IC
	}
	return s / float64(len(rows))
}

func stdIC(rows []ICRow) float64 {
	if len(rows) < 2 {
		return nan()
	}
	m := meanIC(rows)
	var v float64
	for _, r := range rows {
		d := r.IC - m
		v += d * d
	}
	return math.Sqrt(v / float64(len(rows)))
}

// JSONReport 序列化报告列表为 JSON（NaN 输出为 null）。
// English: JSONReport marshals factor reports to JSON, writing NaN as null.
// （JSONReport marshals factor reports to JSON, writing NaN as null.）
func JSONReport(reports []*FactorReport) ([]byte, error) {
	return json.MarshalIndent(reports, "", "  ")
}

// fptr 把 NaN 转为 nil（JSON null），其余取地址。
// English: fptr converts NaN to nil (JSON null) and takes the address otherwise.
func fptr(v float64) *float64 {
	if isNaN(v) {
		return nil
	}
	return &v
}

// MarshalJSON NaN→null 适配（Go 原生 JSON 无法编码 NaN）。
// English: MarshalJSON adapts NaN to null (Go's native JSON cannot encode NaN).
type icRowJSON struct {
	Date string
	N    int
	IC   *float64
}

type layerJSON struct {
	Layer      int
	N          int
	MeanReturn *float64
}

func (r *FactorReport) MarshalJSON() ([]byte, error) {
	j := struct {
		ID           string
		Name         string
		Category     string
		Start        string
		End          string
		Horizon      int
		Quantiles    int
		MinStocks    int
		IC           []icRowJSON
		ICMean       *float64
		ICStd        *float64
		IR           *float64
		Layers       []layerJSON
		Monotonic    bool
		MonotonicDir int
	}{
		ID: r.ID, Name: r.Name, Category: r.Category,
		Start: r.Start, End: r.End, Horizon: r.Horizon,
		Quantiles: r.Quantiles, MinStocks: r.MinStocks,
		ICMean: fptr(r.ICMean), ICStd: fptr(r.ICStd), IR: fptr(r.IR),
		Monotonic: r.Monotonic, MonotonicDir: r.MonotonicDir,
	}
	for _, c := range r.IC {
		j.IC = append(j.IC, icRowJSON{Date: c.Date, N: c.N, IC: fptr(c.IC)})
	}
	for _, l := range r.Layers {
		j.Layers = append(j.Layers, layerJSON{Layer: l.Layer, N: l.N, MeanReturn: fptr(l.MeanReturn)})
	}
	return json.Marshal(j)
}

// renderHTML 用内嵌模板渲染报告 HTML（自包含，无外部依赖）。
// English: renderHTML renders the report HTML with an inline template (self-contained, no external dependencies).
func renderHTML(reports []*FactorReport) ([]byte, error) {
	// 按 |IR| 降序汇总表
	// English: summary table sorted by |IR| descending.
	sorted := make([]*FactorReport, len(reports))
	copy(sorted, reports)
	sort.SliceStable(sorted, func(i, j int) bool {
		return abs(sorted[i].IR) > abs(sorted[j].IR)
	})
	head := ""
	if len(reports) > 0 {
		head = fmt.Sprintf("%s ~ %s", reports[0].Start, reports[0].End)
	}
	var buf bytes.Buffer
	if err := htmlTpl.Execute(&buf, struct {
		Head    string
		Reports []*FactorReport
		Sorted  []*FactorReport
	}{head, reports, sorted}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// HTMLReport 渲染报告 HTML。
// English: HTMLReport renders the factor report as a self-contained HTML page.
// （HTMLReport renders the factor report as a self-contained HTML page.）
func HTMLReport(reports []*FactorReport) ([]byte, error) {
	return renderHTML(reports)
}

var htmlTpl = template.Must(template.New("report").Parse(`<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>多因子验证报告</title>
<style>
body{font-family:system-ui,-apple-system,"PingFang SC",sans-serif;margin:24px;color:#222}
table{border-collapse:collapse;margin:12px 0;font-size:14px}
th,td{border:1px solid #ccc;padding:6px 10px;text-align:right}
th{background:#f0f4f8}td.l{text-align:left}
h2{font-size:18px;border-bottom:2px solid #334;padding-bottom:4px}
.pos{color:#0a7a2a}.neg{color:#b00020}.mono{font-weight:bold}
.summary td:first-child{text-align:left}
</style></head><body>
<h1>多因子验证报告</h1>
<p>区间 {{.Head}}</p>
<h2>因子汇总（按 |IR| 排序）</h2>
<table class="summary"><tr><th>因子</th><th>大类</th><th>IC均值</th><th>IC标准差</th><th>IR</th><th>样本日</th><th>单调</th><th>方向</th></tr>
{{range .Sorted}}<tr>
<td class="l">{{.Name}} (<code>{{.ID}}</code>)</td>
<td class="l">{{.Category}}</td>
<td>{{printf "%.4f" .ICMean}}</td>
<td>{{printf "%.4f" .ICStd}}</td>
<td class="{{if lt .IR 0.0}}neg{{else}}pos{{end}}">{{printf "%.3f" .IR}}</td>
<td>{{len .IC}}</td>
<td>{{if .Monotonic}}<span class="mono">是</span>{{else}}否{{end}}</td>
<td>{{if eq .MonotonicDir 1}}递增{{else if eq .MonotonicDir -1}}递减{{else}}—{{end}}</td>
</tr>{{end}}</table>

{{range .Reports}}
<h2>{{.Name}} <code>{{.ID}}</code>（{{.Category}}）</h2>
<p>IC均值 {{printf "%.4f" .ICMean}}，IR {{printf "%.3f" .IR}}，有效日 {{len .IC}}</p>
<table><tr><th>层（0=最低）</th><th>样本</th><th>平均前瞻收益</th></tr>
{{range .Layers}}<tr>
<td>{{.Layer}}</td><td>{{.N}}</td>
<td class="{{if lt .MeanReturn 0.0}}neg{{else}}pos{{end}}">{{if .N}}{{printf "%.4f" .MeanReturn}}{{else}}—{{end}}</td>
</tr>{{end}}</table>
{{end}}
</body></html>`))
