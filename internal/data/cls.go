// 财联社电报客户端。电报自带正文（Content），无需二次抓取，天然规避标题党问题。
package data

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	clsRollURL      = "https://www.cls.cn/v1/roll/get_roll_list"
	clsRollReferer  = "https://www.cls.cn/telegraph"
	clsApp          = "CailianpressWeb"
	clsOS           = "web"
	clsSV           = "7.7.5"
	clsDefaultLimit = 20
)

// clsNewsRaw 财联社电报原始响应条目（只取需要的字段）。
type clsNewsRaw struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	CTime     int64  `json:"ctime"`
	ID        int64  `json:"id"`
	StockList []struct {
		Name    string `json:"name"`
		StockID string `json:"StockID"`
	} `json:"stock_list"`
}

// clsSign 计算财联社接口签名：
// sign = md5( sha1( urlencode(sorted(params)) ).hexdigest() ).hexdigest()
func clsSign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteString("&")
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(url.QueryEscape(params[k]))
	}

	sha1sum := sha1.Sum([]byte(sb.String()))
	sha1hex := hex.EncodeToString(sha1sum[:])
	md5sum := md5.Sum([]byte(sha1hex))
	return hex.EncodeToString(md5sum[:])
}

// GetCLSNews 获取财联社电报滚动新闻。
// limit 限制返回条数（≤ 50）。正文全量自带，ctime 为 Unix 秒。
func (m *MarketAPI) GetCLSNews(limit int) ([]NewsItem, error) {
	if limit <= 0 || limit > 50 {
		limit = clsDefaultLimit
	}
	params := map[string]string{
		"app": clsApp,
		"os":  clsOS,
		"sv":  clsSV,
		"rn":  fmt.Sprintf("%d", limit),
	}
	params["sign"] = clsSign(params)
	query := make([]string, 0, len(params))
	for k, v := range params {
		query = append(query, k+"="+url.QueryEscape(v))
	}
	sort.Strings(query)

	apiURL := clsRollURL + "?" + strings.Join(query, "&")
	CLSLimiter.Wait()

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cls news request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Referer", clsRollReferer)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cls news http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cls news status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cls news read: %v", err)
	}
	return parseCLSNews(body)
}

// parseCLSNews 解析财联社电报 JSON，转为统一 NewsItem。
// 电报自带正文，Content 直接用；stock_list 名称存入 Stocks 供归因预填。
func parseCLSNews(body []byte) ([]NewsItem, error) {
	var raw struct {
		Errno int `json:"errno"`
		Msg   string `json:"msg"`
		Data  struct {
			RollData []clsNewsRaw `json:"roll_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("cls news json: %v", err)
	}
	if raw.Errno != 0 {
		return nil, fmt.Errorf("cls news errno=%d msg=%s", raw.Errno, raw.Msg)
	}

	items := make([]NewsItem, 0, len(raw.Data.RollData))
	for _, r := range raw.Data.RollData {
		if r.Title == "" {
			continue
		}
		dt := ""
		if r.CTime > 0 {
			dt = time.Unix(r.CTime, 0).Format("2006-01-02 15:04:05")
		}
		var stocks []string
		for _, s := range r.StockList {
			if s.Name != "" {
				stocks = append(stocks, s.Name)
			}
		}
		items = append(items, NewsItem{
			Title:    r.Title,
			Content:  r.Content,
			Datetime: dt,
			Source:   "财联社",
			Stocks:   stocks,
		})
	}
	return items, nil
}
