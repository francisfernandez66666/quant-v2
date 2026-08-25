// delta.go 增量数据管线（阶段2.1 本地下载→云端导入）：export-delta / import-delta 子命令。
// 背景：云端 baostock IP 被封，数据下载职责迁移到本地（本地 IP 正常）——本地 dataload daily
// 更新本地库后，export-delta 导出 trade_date > since 的增量行，scp 上传云端，云端
// import-delta 幂等合入（全部表 INSERT OR REPLACE 自然主键，重复安全）。
// English: incremental data pipeline (local-download → cloud-import): export-delta dumps rows newer
// than --since into a compact gzipped JSONL file; import-delta upserts them into the target DB.
// All tables use INSERT OR REPLACE on natural keys, so re-imports are idempotent.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"quant-trading-v2/internal/store"
)

// deltaTable 增量导出的一张表定义：日期过滤列 + 是否全量（元数据小表直接全导，幂等覆盖）。
// English: one table's delta-export definition: the date-filter column, or full dump for small metadata.
type deltaTable struct {
	name    string // 表名
	dateCol string // 增量过滤列（trade_date/end_date）；空=全量导出（stocks/trade_cal）
}

// deltaTables 导出顺序（行情 → 财务 → 元数据；import 顺序无关，仅日志可读性）。
// English: export order (bars → financials → metadata); import order doesn't matter, logs only.
var deltaTables = []deltaTable{
	{"daily", "trade_date"},
	{"adj_factor", "trade_date"},
	{"daily_basic", "trade_date"},
	{"stk_limit", "trade_date"},
	{"index_daily", "trade_date"},
	{"fina_indicator", "end_date"},
	{"income", "end_date"},
	{"cashflow", "end_date"},
	{"stocks", ""},    // 元数据小表全量（INSERT OR REPLACE 幂等）
	{"trade_cal", ""}, // 交易日历全量
}

// deltaLine delta 文件的一行：一张表的列名 + 行数组（与 cols 一一对应，NULL=null）。
// English: one delta-file line — a table's columns plus row arrays (aligned with cols; NULL as null).
type deltaLine struct {
	Table string   `json:"table"`
	Cols  []string `json:"cols"`
	Rows  [][]any  `json:"rows"`
}

// cmdExportDelta 导出增量：SELECT 各表 date_col > since 的行（元数据表全量），写 gzip JSONL。
// 两种起点模式（二选一，同时给时 --fill-from 优先）：
//   - --since YYYYMMDD：全部日期表统一门槛；
//   - --fill-from <目标库>：逐表读目标库 MAX(date_col) 作为该表起点（"补齐"语义——目标缺多少补多少，
//     目标空表全量导出）。适合首次同步/历史缺口修补（如本地 adj_factor 缺失而云端齐全）。
//
// 用法：dataload --db <源库> export-delta --since 20260820 | --fill-from <目标库> --out /tmp/delta.jsonl.gz
// English: exports deltas — rows with date_col > since per table (metadata tables in full) into a
// gzipped JSONL file. Two start-point modes: a single --since for all tables, or --fill-from <targetDB>
// which reads each table's MAX(date_col) from the target DB (gap-fill semantics; empty target tables
// export in full).
func cmdExportDelta(db *store.DB, args []string) {
	fs := flag.NewFlagSet("export-delta", flag.ExitOnError)
	since := fs.String("since", "", "起始日期 YYYYMMDD（该日之后的数据导出；空=配合 --fill-from 或全量）")
	fillFrom := fs.String("fill-from", "", "目标库路径（逐表读其 MAX(date_col) 作为该表增量起点，补齐语义）")
	out := fs.String("out", "/tmp/delta.jsonl.gz", "输出文件（gzip JSONL）")
	fs.Parse(args)

	// fill-from 模式：打开目标库，逐表取 max（只读）。
	// English: fill-from mode — open the target DB read-only and take per-table max dates.
	var target *store.DB
	if *fillFrom != "" {
		var err error
		target, err = store.Open(*fillFrom)
		if err != nil {
			log.Fatalf("打开目标库失败: %v", err)
		}
		defer target.Close()
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("创建输出文件失败: %v", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	w := bufio.NewWriter(gz)
	defer w.Flush()

	total := 0
	for _, t := range deltaTables {
		cols := store.TableColumns(t.name)
		if len(cols) == 0 {
			continue
		}
		q := fmt.Sprintf("SELECT %s FROM %s", quoteCols(cols), t.name)
		qargs := []any{}
		switch {
		case t.dateCol != "" && target != nil:
			// 补齐语义：以目标库该表 max 为起点（空表 → max="" → 全量导出）。
			// 日期列按表而异（行情=trade_date，财务=end_date），直接按 dateCol 查询。
			// English: gap-fill — start after the target table's max (empty table → full dump). The date
			// column differs per table (bars=trade_date, financials=end_date), so query by dateCol.
			mrows, err := target.QueryRows(
				fmt.Sprintf("SELECT COALESCE(MAX(%s),'') AS m FROM %s", t.dateCol, t.name))
			if err != nil {
				log.Fatalf("读目标库 %s max 失败: %v", t.name, err)
			}
			tmax := ""
			if len(mrows) > 0 {
				if v, ok := mrows[0]["m"].(string); ok {
					tmax = v
				}
			}
			if tmax == "" {
				log.Printf("[export-delta] %s: 目标库为空 → 全量导出", t.name)
			} else {
				q += fmt.Sprintf(" WHERE %s > ?", t.dateCol)
				qargs = append(qargs, tmax)
				log.Printf("[export-delta] %s: 目标 max=%s → 导出其后增量", t.name, tmax)
			}
		case t.dateCol != "" && *since != "":
			q += fmt.Sprintf(" WHERE %s > ?", t.dateCol)
			qargs = append(qargs, *since)
		}
		rows, err := db.QueryRows(q, qargs...)
		if err != nil {
			log.Fatalf("读取 %s 失败: %v", t.name, err)
		}
		if len(rows) == 0 {
			log.Printf("[export-delta] %s: 0 行（无增量）", t.name)
			continue
		}
		line := deltaLine{Table: t.name, Cols: cols, Rows: make([][]any, 0, len(rows))}
		for _, r := range rows {
			arr := make([]any, len(cols))
			for i, c := range cols {
				v := r[c]
				if b, ok := v.([]byte); ok {
					v = string(b) // sqlite TEXT 以 []byte 返回，转字符串便于 JSON
				}
				arr[i] = v
			}
			line.Rows = append(line.Rows, arr)
		}
		b, err := json.Marshal(line)
		if err != nil {
			log.Fatalf("序列化 %s 失败: %v", t.name, err)
		}
		w.Write(b)
		w.WriteByte('\n')
		total += len(rows)
		log.Printf("[export-delta] %s: %d 行", t.name, len(rows))
	}
	log.Printf("[export-delta] 完成：%d 行 → %s (since=%q)", total, *out, *since)
}

// cmdImportDelta 导入增量：逐行读 delta 文件，按表 INSERT OR REPLACE 合入（幂等，重复安全）。
// 用法：dataload --db <云端库> import-delta --file /tmp/delta.jsonl.gz
// English: imports a delta file line by line, upserting each table (idempotent, safe to re-run).
func cmdImportDelta(db *store.DB, args []string) {
	fs := flag.NewFlagSet("import-delta", flag.ExitOnError)
	file := fs.String("file", "", "delta 文件（gzip JSONL）")
	fs.Parse(args)
	if *file == "" {
		log.Fatalf("import-delta 需要 --file")
	}
	f, err := os.Open(*file)
	if err != nil {
		log.Fatalf("打开 delta 文件失败: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		log.Fatalf("解压失败: %v", err)
	}
	defer gz.Close()

	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1024*1024), 512*1024*1024) // 单表一行可能很大（daily_basic 全市场）
	total := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var dl deltaLine
		if err := json.Unmarshal(line, &dl); err != nil {
			log.Fatalf("解析 delta 行失败: %v", err)
		}
		rows := make([]map[string]any, 0, len(dl.Rows))
		for _, arr := range dl.Rows {
			m := make(map[string]any, len(dl.Cols))
			for i, c := range dl.Cols {
				if i < len(arr) && arr[i] != nil {
					m[c] = arr[i]
				} else {
					m[c] = nil
				}
			}
			rows = append(rows, m)
		}
		n, err := db.InsertRows(dl.Table, dl.Cols, rows)
		if err != nil {
			log.Fatalf("写入 %s 失败: %v", dl.Table, err)
		}
		total += int(n)
		log.Printf("[import-delta] %s: %d 行", dl.Table, n)
	}
	if err := sc.Err(); err != nil {
		log.Fatalf("读取 delta 文件失败: %v", err)
	}
	log.Printf("[import-delta] 完成：%d 行", total)
}

// quoteCols 列名加引号（防关键字冲突）。
// English: quotes column names against keyword clashes.
func quoteCols(cols []string) string {
	s := ""
	for i, c := range cols {
		if i > 0 {
			s += ", "
		}
		s += `"` + c + `"`
	}
	return s
}
