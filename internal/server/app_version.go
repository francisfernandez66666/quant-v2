// app_version.go — §APPVER 2026-09-22 C批：APK 服务端驱动强制更新的公开版本端点。
//
// 职责：把 config.Rules.AppRelease（APK 发布单：最低强制版本 / 最新发布版本 / 下载地址 /
// 更新说明）以固定 JSON 形状暴露给 APK 原生壳，供其在**登录前**做强制更新检查——
// 客户端 BuildConfig.VERSION_CODE < min_version_code 时弹不可取消更新框。
//
// 为什么单独成文件而不下沉进 server.go：本端点是全仓少数刻意免鉴权的业务端点之一，
// 独立文件+文件头说明让「公开面」可被静态审计一眼枚举（verify 负锁也按注册行校验）。
//
// 只读端点、无任何副作用：不改配置、不落库、不写日志表；配置管理器缺席时按
// 「未发布」（min=0）返回，绝不让更新检查接口自身成为故障点。
// English: §APPVER public APK version endpoint — serves the release manifest read-only so the
// native shell can enforce updates before login; zero side effects, absent config = not published.
package server

import (
	"net/http"

	"quant-trading-v2/internal/config"
)

// handleAppVersion GET /api/app/version：返回当前 APK 发布单快照。
// 响应形状固定 {ok, min_version_code, latest_version_code, apk_url, note}——
// 原生壳用低成本 JSON 解析（不动用 Retrofit/Gson 模型），字段名即契约，只增不改不删。
// 零值=未发布：min_version_code=0 时客户端判定恒不触发（不拦），与"没有更新通道"等价，
// 因此本端点对旧客户端/未配置环境都是安全无感增量。
// English: fixed-shape read-only release snapshot; min=0 means "nothing enforced", safe for
// every client and unconfigured deployments.
func (s *Server) handleAppVersion(w http.ResponseWriter, r *http.Request) {
	// 读当前配置快照（cfgMgr 缺席时按零值发布单返回=未发布，不 panic——公开端点必须最健壮）。
	var ar config.AppReleaseConfig
	if s.cfg != nil {
		ar = s.cfg.Get().AppRelease
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":                  true,
		"min_version_code":    ar.MinVersionCode,
		"latest_version_code": ar.LatestVersionCode,
		"apk_url":             ar.ApkURL,
		"note":                ar.Note,
	})
}
