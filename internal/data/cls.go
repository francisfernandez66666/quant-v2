// 财联社电报客户端。电报自带正文（Content），无需二次抓取，天然规避标题党问题。
// 接口要求按参数排序后计算 sign 签名（sha1→md5），请求走 CLSLimiter 限流。
// CLS (Cailianpress) telegraph client. Telegraph entries carry their own body,
// avoiding clickbait titles; requests require a sign (sha1→md5 of sorted params)
// and are rate-limited by CLSLimiter.
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
	// CLS API 端点与请求参数常量。
	// CLS API endpoint, referer and client constants.
	clsRollURL      = "https://www.cls.cn/v1/roll/get_roll_list" // 电报滚动列表 API
	clsRollReferer  = "https://www.cls.cn/telegraph"             // 请求 Referer（电报页）
	clsApp          = "CailianpressWeb"                          // 客户端标识
	clsOS           = "web"                                      // 操作系统标识
	clsSV           = "7.7.5"                                    // 客户端版本号
	clsDefaultLimit = 20                                         // 默认返回条数上限
)

// clsNewsRaw 财联社电报原始响应条目（只取需要的字段）。
// clsNewsRaw is one raw CLS telegraph item (only the needed fields kept).
type clsNewsRaw struct {
	Title     string `json:"title"`   // 标题
	Content   string `json:"content"` // 正文（电报自带，无需二次抓取）
	CTime     int64  `json:"ctime"`   // 发布时间（Unix 秒）
	ID        int64  `json:"id"`      // 电报唯一 ID
	StockList []struct {
		Name    string `json:"name"`    // 关联股票名称
		StockID string `json:"StockID"` // 关联股票内部 ID
	} `json:"stock_list"`
}

// clsSign 计算财联社接口签名：
// sign = md5( sha1( urlencode(sorted(params)) ).hexdigest() ).hexdigest()
// clsSign computes the CLS API signature as
// md5( sha1( urlencode(sorted(params)) ).hexdigest() ).hexdigest().
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
// GetCLSNews fetches the CLS telegraph rolling news (limit ≤ 50); full bodies
// are included and ctime is Unix seconds.
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
// parseCLSNews converts CLS telegraph JSON into NewsItem; the Content comes
// directly from the telegraph, and stock_list names fill Stocks for attribution.
func parseCLSNews(body []byte) ([]NewsItem, error) {
	var raw struct {
		Errno int    `json:"errno"` // 接口错误码（0 表示成功）
		Msg   string `json:"msg"`   // 接口错误信息
		Data  struct {
			RollData []clsNewsRaw `json:"roll_data"` // 滚动电报列表
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
