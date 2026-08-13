package com.liangzai.quant

import android.annotation.SuppressLint
import android.os.Bundle
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.appcompat.app.AppCompatActivity
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

            override fun onPageFinished(view: WebView, url: String?) {
                super.onPageFinished(view, url)
                // 首次打开：若未设置过服务器地址且配置了默认值，则写入 localStorage 预填
                if (DEFAULT_SERVER_URL.isNotEmpty()) {
                    view.evaluateJavascript(
                        "(function(){" +
                            "if(!localStorage.getItem('liangzai_server_url')){" +
                            "localStorage.setItem('liangzai_server_url','$DEFAULT_SERVER_URL');" +
                            "}" +
                            "})()",
                        null
                    )
                }
            }
        }

        webView.loadUrl("https://appassets.androidplatform.net/index.html")
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