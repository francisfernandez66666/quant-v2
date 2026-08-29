package com.liangzai.quant

import android.Manifest
import android.annotation.SuppressLint
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebChromeClient
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.appcompat.app.AppCompatActivity
import com.liangzai.quant.BuildConfig
import androidx.core.app.ActivityCompat
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import androidx.webkit.WebViewAssetLoader
import cn.jpush.android.api.JPushInterface

/**
 * 移动端薄壳：加载内嵌前端 assets/（web/dist 构建产物），API/SSE 指向云服务器。
 *
 * 关键设计：
 *  - WebViewAssetLoader 把 assets/ 映射到 https://appassets.androidplatform.net/（安全源），
 *    使 localStorage / EventSource(SSE) / fetch 均按标准 https 语义工作，且无需明文权限。
 *  - 服务器地址：登录页输入框填写（前端已有该功能，存 localStorage）。
 *    若配置了 DEFAULT_SERVER_URL，首次打开会预填，减少输入成本。
 *
 * 等云资源就绪后要改的地方（只此一处）：
 *  - DEFAULT_SERVER_URL：预填的服务器地址（如 https://your-domain.com），
 *    留空表示不预填、由用户在登录页手填。
 */
class MainActivity : AppCompatActivity() {

    companion object {
        /** 预填服务器地址。改为 https://你的域名 后，首次打开登录页即带出。 */
        const val DEFAULT_SERVER_URL = "https://quant-trading.top"

        /** 极光推送设备别名（必须与后端 config.json notify.push.alias 一致，默认 quant_owner）。 */
        const val QUANT_PUSH_ALIAS = "quant_owner"

        /** setAlias 请求序列号（极光要求递增，用于回调匹配；单次设置固定值即可）。 */
        const val JPUSH_ALIAS_SEQ = 1
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.KITKAT) {
            android.webkit.WebView.setWebContentsDebuggingEnabled(true)
        }

        ensureNotificationChannel()
        requestNotificationPermission()
        setupJPushAlias()

        val webView = findViewById<WebView>(R.id.webview)

        // assets/ → https://appassets.androidplatform.net/
        val assetLoader = WebViewAssetLoader.Builder()
            .addPathHandler("/", WebViewAssetLoader.AssetsPathHandler(this))
            .build()

        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true          // localStorage 持久化
        webView.settings.databaseEnabled = true
        webView.settings.allowFileAccess = false
        webView.settings.loadsImagesAutomatically = true
        webView.settings.mixedContentMode = WebSettings.MIXED_CONTENT_COMPATIBILITY_MODE
        // 内嵌 assets 始终从最新打包产物加载，避免升级 APK 后 WebView 缓存旧版前端
        // (Always load the latest bundled assets so an app upgrade never serves a stale cached page.)
        webView.settings.cacheMode = WebSettings.LOAD_NO_CACHE

        webView.webViewClient = object : WebViewClient() {
            // 内嵌资源统一走 assetLoader，网络请求放行系统默认（https）
            override fun shouldInterceptRequest(
                view: WebView,
                request: WebResourceRequest
            ): WebResourceResponse? {
                return assetLoader.shouldInterceptRequest(request.url)
            }

            // WebView 内 JS 的 Notification API 需要 WebChromeClient.onShowNotification 才会显示系统通知
            // 同时把 JS console.log 转发到 Android logcat（调试定位用）
            override fun onPageStarted(view: WebView, url: String, favicon: android.graphics.Bitmap?) {
                super.onPageStarted(view, url, favicon)
                // §P1-14 服务器地址覆盖：仅在 localStorage 尚无有效地址时预填。
                // 优先级：原生持久化（SharedPreferences server_url，移动端设置入口写入）> DEFAULT_SERVER_URL。
                // 不强制覆盖用户已生效的自定义地址；历史脏值（非法域名）重置为上一优先级的地址。
                // （Seed the server URL only when localStorage has no valid one. Precedence:
                // native SharedPreferences override > DEFAULT_SERVER_URL, so a server address set via
                // the mobile settings entry survives even if WebView localStorage is cleared.）
                val prefs = getSharedPreferences("quant_prefs", MODE_PRIVATE)
                val nativeUrl = prefs.getString("server_url", "") ?: ""
                val seed = if (nativeUrl.isNotEmpty()) nativeUrl else DEFAULT_SERVER_URL
                if (seed.isNotEmpty()) {
                    view.evaluateJavascript(
                        "(function(){var v=localStorage.getItem('liangzai_server_url');" +
                        "if(!v||!/^https?:\\/\\//i.test(v)){" +
                        "localStorage.setItem('liangzai_server_url','" + seed.replace("'", "\\'") + "');}})();",
                        null
                    )
                }
            }
        }

        // WebView 内 JS 的 Notification API 需要 WebChromeClient.onShowNotification 才会显示系统通知
        // 同时把 JS console.log 转发到 Android logcat（调试定位用）
        webView.webChromeClient = object : WebChromeClient() {
            override fun onConsoleMessage(msg: android.webkit.ConsoleMessage?): Boolean {
                if (msg != null) android.util.Log.d("QUANT_WEB", msg.message())
                return true
            }
        }

        // 原生通知桥：前端 JS 调用 window.AndroidNotify.show(title, body) 显示 Android 系统通知
        // 返回布尔值：true=已发送；false=未获通知权限（前端据此提示用户去授权，避免静默丢失）
        webView.addJavascriptInterface(object {
            @android.webkit.JavascriptInterface
            fun show(title: String, body: String): Boolean {
                val manager = getSystemService(NotificationManager::class.java)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                    ContextCompat.checkSelfPermission(this@MainActivity, Manifest.permission.POST_NOTIFICATIONS)
                    != PackageManager.PERMISSION_GRANTED) {
                    // 未授权：返回 false 供前端感知，并主动触发一次权限申请引导
                    this@MainActivity.requestNotificationPermission()
                    return false
                }
                val intent = android.content.Intent(this@MainActivity, MainActivity::class.java)
                val pending = android.app.PendingIntent.getActivity(
                    this@MainActivity, 0, intent,
                    android.app.PendingIntent.FLAG_UPDATE_CURRENT or android.app.PendingIntent.FLAG_IMMUTABLE
                )
                val notification = NotificationCompat.Builder(this@MainActivity, "quant_signals")
                    .setContentTitle(title)
                    .setContentText(body)
                    .setSmallIcon(android.R.drawable.stat_notify_chat)
                    .setAutoCancel(true)
                    .setPriority(NotificationCompat.PRIORITY_HIGH)
                    .setContentIntent(pending)
                    .build()
                manager.notify((title + body).hashCode(), notification)
                return true
            }
        }, "AndroidNotify")

        // 原生配置桥：前端 JS 调用 window.AndroidConfig.setServerUrl(url) 持久化服务器地址。
        // 写入 SharedPreferences 并在 localStorage 同步，供前端 baseUrl() 立即生效。
        // （Returns true when persisted; frontend may call this from the server-URL input to save a custom address.）
        webView.addJavascriptInterface(object {
            @android.webkit.JavascriptInterface
            fun setServerUrl(url: String): Boolean {
                val value = url.trim()
                // §安全 T6（2026-08-29）：release 构建强制 https（拒绝明文 http，防止 token 明文传输/
                // 中间人注入）；debug 构建允许 http 便于局域网/模拟器联调。
                val pattern = if (BuildConfig.DEBUG) "^https?://.+" else "^https://.+"
                if (!java.util.regex.Pattern.matches(pattern, value)) return false
                getSharedPreferences("quant_prefs", MODE_PRIVATE)
                    .edit().putString("server_url", value).apply()
                webView.evaluateJavascript(
                    "localStorage.setItem('liangzai_server_url','" + value.replace("'", "\\'") + "');",
                    null
                )
                return true
            }
        }, "AndroidConfig")

        webView.loadUrl("https://appassets.androidplatform.net/index.html")
    }

    /** 创建通知渠道（Android 8+ 通知必需要有渠道，否则 WebView 内 Notification 静默丢弃） */
    private fun ensureNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val manager = getSystemService(NotificationManager::class.java)
            val channel = NotificationChannel(
                "quant_signals", "量仔信号", NotificationManager.IMPORTANCE_HIGH
            ).apply {
                description = "策略信号与提醒推送"
            }
            manager.createNotificationChannel(channel)
        }
    }

    /** Android 13+ 需要运行时申请通知权限，否则 Notification API 一律静默失败 */
    private fun requestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            if (ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED) {
                ActivityCompat.requestPermissions(
                    this, arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1001
                )
            }
        }
    }

    /**
     * 设置极光推送设备别名：与服务端 config.json 的 push.alias（默认 quant_owner）保持一致，
     * 服务端按该别名下发关键提醒，后台/离线也能收到系统通知。
     * 设置结果通过 JPushMessageReceiver.onAliasOperatorResult 回调确认。
     * 已设置成功过则跳过（避免重复设置触发极光 6022「alias 操作进行中」）。
     */
    private fun setupJPushAlias() {
        val prefs = getSharedPreferences("jpush_prefs", MODE_PRIVATE)
        if (prefs.getBoolean("alias_set", false)) {
            return
        }
        try {
            JPushInterface.setAlias(this, JPUSH_ALIAS_SEQ, QUANT_PUSH_ALIAS)
        } catch (e: Exception) {
            android.util.Log.e("QUANT_JPUSH", "setAlias 调用异常: ${e.message}")
        }
    }

    /** 请求结果回调：授权后刷新 WebView 中的通知权限状态 */
    override fun onRequestPermissionsResult(
        requestCode: Int,
        permissions: Array<out String>,
        grantResults: IntArray
    ) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == 1001) {
            val webView = findViewById<WebView>(R.id.webview)
            // 重新执行前端通知权限检查逻辑：Notification.permission 会重新求值
            webView.evaluateJavascript("(function(){ if(typeof window.onNotifyPermissionChange==='function'){window.onNotifyPermissionChange();} })()", null)
        }
    }

    // 系统返回键：优先 WebView 内部历史，回退到应用根部再退出
    override fun onBackPressed() {
        val webView = findViewById<WebView>(R.id.webview)
        if (webView.canGoBack()) {
            webView.goBack()
        } else {
            super.onBackPressed()
        }
    }
}