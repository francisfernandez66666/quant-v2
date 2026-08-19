// 回测专用查询（B4 合成事件/板块成分）。
package store

// Industries 返回全部股票的行业映射（ts_code → industry，仅非空）。
// （Industries returns ts_code → industry for all stocks with a known industry.）
func (d *DB) Industries() (map[string]string, error) {
	rows, err := d.db.Query(`SELECT ts_code, industry FROM stocks WHERE industry IS NOT NULL AND industry != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var code, ind string
		if err := rows.Scan(&code, &ind); err != nil {
			return nil, err
		}
		out[code] = ind
	}
	return out, rows.Err()
}

// IndustryConstituents 返回某行业在 date 当日有成交的股票代码。
// （IndustryConstituents returns codes of an industry that traded on date.）
func (d *DB) IndustryConstituents(industry, date string) ([]string, error) {
	rows, err := d.db.Query(`SELECT s.ts_code FROM stocks s
		JOIN daily d ON d.ts_code = s.ts_code
		WHERE s.industry = ? AND d.trade_date = ? AND d.close > 0`, industry, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LimitUpCountsByIndustry 统计 date 当日各行业涨停家数（close ≥ up_limit×0.995 防四舍五入误差）。
// （LimitUpCountsByIndustry counts limit-up stocks per industry on date.）
func (d *DB) LimitUpCountsByIndustry(date string) (map[string]int, error) {
	rows, err := d.db.Query(`SELECT s.industry, COUNT(*) FROM daily dv
		JOIN stk_limit l ON l.ts_code = dv.ts_code AND l.trade_date = dv.trade_date
		JOIN stocks s ON s.ts_code = dv.ts_code
		WHERE dv.trade_date = ? AND dv.close >= l.up_limit * 0.995 AND dv.close > 0
		GROUP BY s.industry`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var ind string
		var n int
		if err := rows.Scan(&ind, &n); err != nil {
			return nil, err
		}
		out[ind] = n
	}
	return out, rows.Err()
}

// LimitUpStocks 返回 date 当日某行业涨停的股票代码。
// （LimitUpStocks returns limit-up codes of an industry on date.）
func (d *DB) LimitUpStocks(date, industry string) ([]string, error) {
	rows, err := d.db.Query(`SELECT s.ts_code FROM daily dv
		JOIN stk_limit l ON l.ts_code = dv.ts_code AND l.trade_date = dv.trade_date
		JOIN stocks s ON s.ts_code = dv.ts_code
		WHERE dv.trade_date = ? AND s.industry = ? AND dv.close >= l.up_limit * 0.995 AND dv.close > 0`,
		date, industry)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
