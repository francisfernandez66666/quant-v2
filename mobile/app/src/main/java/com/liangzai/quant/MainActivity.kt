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
import androidx.core.app.ActivityCompat
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import androidx.webkit.WebViewAssetLoader

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
                // 每次页面开始加载即无条件写入默认服务器地址。
                // WebView 内嵌资源运行在 appassets 源，若 localStorage 的 server_url 缺失或残留脏值
                // （如历史版本的 https://www.quant-trading.top），前端 fetch 会打到无效主机而报
                // "Failed to fetch"。这里保证请求永远指向当前默认地址。
                // （Unconditionally persist the default server URL before any page load. Without it,
                // stale/missing localStorage values make the frontend fetch an invalid host from the
                // appassets origin, surfacing as "Failed to fetch".）
                if (DEFAULT_SERVER_URL.isNotEmpty()) {
                    view.evaluateJavascript(
                        "localStorage.setItem('liangzai_server_url','$DEFAULT_SERVER_URL');",
                        null
                    )
                }
            }

            override fun onPageFinished(view: WebView, url: String?) {
                super.onPageFinished(view, url)
                // 页面加载完成后再次确认默认地址已写入（部分机型 onPageStarted 时机过早，JS 尚未就绪）
                if (DEFAULT_SERVER_URL.isNotEmpty()) {
                    view.evaluateJavascript(
                        "localStorage.setItem('liangzai_server_url','$DEFAULT_SERVER_URL');",
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
        webView.addJavascriptInterface(object {
            @android.webkit.JavascriptInterface
            fun show(title: String, body: String) {
                val manager = getSystemService(NotificationManager::class.java)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                    ContextCompat.checkSelfPermission(this@MainActivity, Manifest.permission.POST_NOTIFICATIONS)
                    != PackageManager.PERMISSION_GRANTED) {
                    return
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
            }
        }, "AndroidNotify")

        webView.loadUrl("https://appassets.androidplatform.net/index.html")
    }

    /** 创建通知渠道（Android 8+ 通知必需要有渠道，否则 WebView 内 Notification 静默丢弃） */
    private fun ensureNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val manager = getSystemService(NotificationManager::class.java)
            val channel = NotificationChannel(
                "quant_signals", "量仔期货信号", NotificationManager.IMPORTANCE_HIGH
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