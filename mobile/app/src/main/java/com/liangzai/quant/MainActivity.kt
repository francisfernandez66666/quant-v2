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
import org.json.JSONObject

/**
 * 移动端薄壳：加载内嵌前端 assets/（web/dist 构建产物），API/SSE 指向云服务器。
 *
 * 关键设计：
 *  - WebViewAssetLoader 把 assets/ 映射到 https://appassets.androidplatform.net/（安全源），
 *    使 localStorage / EventSource(SSE) / fetch 均按标准 https 语义工作，且无需明文权限。
 *  - 服务器地址：登录页输入框填写（前端已有该功能，存 localStorage）。
 *    若配置了 DEFAULT_SERVER_URL，首次打开会预填，减少输入成本。
 *  - JS 桥三件套：AndroidNotify（系统通知）、AndroidConfig（服务器地址持久化）、
 *    AndroidAuth（§NATIVEAUTH 2026-09-22 C批：登录 token 迁原生加密存储）。
 *  - UpdateGate（§APPVER 同批）：启动后台强更检查，v1 无更新通道故版本直接跃迁到 2。
 *
 * 等云资源就绪后要改的地方（只此一处）：
 *  - DEFAULT_SERVER_URL：预填的服务器地址（如 https://your-domain.com），
 *    留空表示不预填、由用户在登录页手填。
 */
class MainActivity : AppCompatActivity() {

    companion object {
        /** 预填服务器地址。改为 https://你的域名 后，首次打开登录页即带出。 */
        const val DEFAULT_SERVER_URL = "https://quant-trading.top"

        /**
         * §M-11（2026-09-22 修复批）极光推送默认别名：仅作为「未登录 / 历史设备」的兜底值
         * （与后端 config.json notify.push.alias 默认一致）。
         * 缺陷原文：旧版把该常量当成全App唯一别名无条件 setAlias，且 alias_set 标记让它只在
         * 首装生效一次——换账号后别名仍指向 quant_owner，服务端 §GAP2-W2 的按用户 alias 下发
         * 因设备根本没注册账号别名而失效，定向推送退化成广播。
         * 现在别名由登录 uid 派生（见 pushAliasFor），此常量只留未登录兜底语义。
         */
        const val QUANT_PUSH_ALIAS = "quant_owner"

        /**
         * §M-11 pushAliasFor：按登录账号派生设备别名，统一 quant_ 前缀——
         * 账号名全部落在极光合法字符集（字母/数字/_ - = .）内时拼可读别名 quant_<uid>；
         * 含非法字符（如中文账号名）时不做有损替换，直接退化到 quant_<账号hashCode十六进制>，
         * 防不同账号被过滤成同一串互相碰撞。总长截到 64 字节内。
         * English: derive the JPush alias from the logged-in uid so per-user targeted pushes
         * actually land on the owner's device instead of the shared quant_owner broadcast alias.
         */
        fun pushAliasFor(account: String?): String {
            val acc = (account ?: "").trim()
            if (acc.isEmpty()) return QUANT_PUSH_ALIAS // 未登录/登出：回落默认别名（与后端默认一致）
            val legal = acc.all { ch ->
                ch in 'a'..'z' || ch in 'A'..'Z' || ch in '0'..'9' ||
                    ch == '_' || ch == '-' || ch == '=' || ch == '.'
            }
            // 账号名本身全合法（admin/tester 等）：直接拼可读别名；
            // 含非法字符（如中文账号名）不做有损替换——改用 hashCode 十六进制，
            // 避免「过滤成下划线后不同账号互相碰撞、推送又串了」。
            return if (legal) "quant_$acc".take(64)
            else "quant_${Integer.toHexString(acc.hashCode())}".take(64)
        }

        /** 旧的固定 setAlias 请求序列号。§M-11 起改用 alias_seq 持久化自增（换号重设需要新 seq），本常量仅留档。 */
        @Deprecated("§M-11：改用 jpush_prefs/alias_seq 自增序列，固定值 1 无法支撑换号重设。")
        const val JPUSH_ALIAS_SEQ = 1

        // §M-11 jpush_prefs 键名（与 JPushMessageReceiver 内部常量必须保持一致，两处各自声明）：
        // alias_desired    = 当前期望别名（换账号即更新）
        // alias_registered = 极光回调确证设置成功的别名
        // alias_seq        = 单调递增的请求序列号（替代旧固定 JPUSH_ALIAS_SEQ=1）
        const val PREFS_JPUSH = "jpush_prefs"
        const val KEY_ALIAS_DESIRED = "alias_desired"
        const val KEY_ALIAS_REGISTERED = "alias_registered"
        const val KEY_ALIAS_SEQ = "alias_seq"
        const val KEY_ALIAS_RETRY = "alias_retry_count"

        /** §M-11 登录账号在 quant_prefs 中的持久化键（AndroidAuth.setAccount 桥写入）。 */
        const val KEY_PUSH_ACCOUNT = "push_account"
    }

    /**
     * §M12（2026-09-22 修复批）注入 JS 字符串的完整转义。
     * 旧实现只对单引号做「反斜杠+单引号」替换（单引号伪转义）——值里若含反斜杠、换行、双引号或
     * U+2028/U+2029 等即可撕裂字符串字面量，向 evaluateJavascript 注入任意 JS（服务器地址来自
     * SharedPreferences/用户输入，非纯可信源）。现统一走 org.json.JSONObject.quote：
     * 输出自带双引号的完整 JSON 字符串字面量（转义 `"`、`\`、控制字符），JSON 转义集是 JS 的
     * 子集，可直接作为表达式拼进注入脚本。
     */
    private fun jsQuote(raw: String): String = JSONObject.quote(raw)

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        // §M12（2026-09-22 修复批）：旧版 release 也恒开 WebView 远程调试——设备一旦被物理接触，
        // chrome://devtools / adb 即可挂载 WebView，直接翻读 localStorage 里的登录 token 与全部
        // 会话数据（凭据暴露面）。现在仅 debug 构建开启（BuildConfig.DEBUG 由 AGP buildConfig=true
        // 生成，见 app/build.gradle.kts）；release 进程默认即为关，无需显式调用。
        if (BuildConfig.DEBUG) {
            android.webkit.WebView.setWebContentsDebuggingEnabled(true)
        }

        ensureNotificationChannel()
        requestNotificationPermission()
        setupJPushAlias()

        // §NATIVEAUTH（2026-09-22 C批）：加密偏好存储初始化，必须先于 AndroidAuth 桥注册——
        // 桥回调随时可能被前端调用，init 只缓存 applicationContext，实际密钥解锁懒到首次读写，
        // 不阻塞 onCreate。
        SecureAuthStore.init(this)

        val webView = findViewById<WebView>(R.id.webview)

        // assets/ → https://appassets.androidplatform.net/
        val assetLoader = WebViewAssetLoader.Builder()
            .addPathHandler("/", WebViewAssetLoader.AssetsPathHandler(this))
            .build()

        webView.settings.javaScriptEnabled = true
        // §M12c 残余风险——沿革标注（AUDIT M12 / FIX_PLAN §3 M12 行）：2026-09-22 C批已由本批
        // AndroidAuth 桥收口，登录 token 不再落 WebView localStorage，改存原生加密偏好
        // （EncryptedSharedPreferences，见 SecureAuthStore.kt 文件头），静态落盘面（root/adb backup
        // 直接读域文件取票）已消除；动态注入面另由 §M12（release 远程调试 DEBUG 门控）+ §M12
        // jsQuote 完整转义收掉。domStorage 本身仍需保留：server_url/账号角色等非敏感数据与旧前端
        // 回落路径都依赖 localStorage，故此处只留台账注释、不加机制说明。
        webView.settings.domStorageEnabled = true          // localStorage 持久化（token 已迁原生，见 §NATIVEAUTH）
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
                    // §M12：seed 含 SharedPreferences 持久化值，不可信源一律走 jsQuote 完整转义
                    view.evaluateJavascript(
                        "(function(){var v=localStorage.getItem('liangzai_server_url');" +
                        "if(!v||!/^https?:\\/\\//i.test(v)){" +
                        "localStorage.setItem('liangzai_server_url'," + jsQuote(seed) + ");}})();",
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
                // §M12：value 已通过 https 正则门（debug 构建放宽到 http），但正则只约束前缀，
                // 仍不可依赖其内容安全——注入 JS 一律走 jsQuote 完整转义，不再手工 replace 单引号。
                webView.evaluateJavascript(
                    "localStorage.setItem('liangzai_server_url'," + jsQuote(value) + ");",
                    null
                )
                return true
            }
        }, "AndroidConfig")

        // §NATIVEAUTH（2026-09-22 C批）原生鉴权存储桥：登录 token 迁出 WebView localStorage，
        // 存入原生加密偏好（SecureAuthStore/EncryptedSharedPreferences），收小静态落盘读取面。
        // 前端 api/index.js 的 nativeAuthBridge() 检测到 window.AndroidAuth 即改走原生存取；
        // 旧前端/纯浏览器无感——不检测就不使用，行为与迁移前完全一致。
        // setToken 入参校验：非空白 + UTF-8 字节数 ≤4096（token 合理量级远小于此，超限即
        // 视为异常写入拒绝，防任意大对象塞进加密偏好）。
        webView.addJavascriptInterface(object {
            @android.webkit.JavascriptInterface
            fun getToken(): String = SecureAuthStore.getToken()

            @android.webkit.JavascriptInterface
            fun setToken(t: String): Boolean {
                if (t.isBlank() || t.toByteArray(Charsets.UTF_8).size > 4096) return false
                return SecureAuthStore.putToken(t)
            }

            @android.webkit.JavascriptInterface
            fun clearToken(): Boolean {
                SecureAuthStore.clearToken()
                return true
            }

            /**
             * §M-11（2026-09-22 修复批）推送别名随登录账号走：前端 storeAuth/clearAuth 时经本桥
             * 上报当前登录账号名（登出传空串），原生持久化后立即重设极光别名。
             * 不校验身份的账号串只做「派生别名的材料」，且别名派生自带字符白名单/哈希退化，
             * 不会被拼进任何 JS 注入面；长度上限 64，防异常长串塞进偏好。
             * English: §M-11 — the frontend reports the logged-in account on login/logout; the
             * JPush alias is re-derived and re-registered immediately, so targeted pushes stop
             * being a global broadcast after an account switch.
             */
            @android.webkit.JavascriptInterface
            fun setAccount(account: String): Boolean {
                val acc = account.trim().take(64)
                getSharedPreferences("quant_prefs", MODE_PRIVATE)
                    .edit().putString(KEY_PUSH_ACCOUNT, acc).apply()
                setupJPushAlias()
                return true
            }
        }, "AndroidAuth")

        webView.loadUrl("https://appassets.androidplatform.net/index.html")

        // §APPVER（2026-09-22 C批）服务端驱动强制更新闸：后台查询 /api/app/version，
        // versionCode 低于服务端 min_version_code 时弹不可取消更新框；网络/解析失败一律
        // fail-open 静默（离线可用性优先，更新检查不得成为启动阻塞）。
        // baseUrl 优先级与 onPageStarted 预填一致：SharedPreferences server_url > DEFAULT_SERVER_URL。
        UpdateGate.start(this) {
            getSharedPreferences("quant_prefs", MODE_PRIVATE)
                .getString("server_url", "")
                ?.takeIf { it.isNotBlank() }
                ?: DEFAULT_SERVER_URL
        }
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
     * 设置极光推送设备别名：§M-11（2026-09-22 修复批）后别名由登录账号派生（pushAliasFor），
     * 服务端按「quant_<uid>」别名定向下发关键提醒，后台/离线也能收到系统通知。
     * 幂等策略重写（旧缺陷半）：
     *  - 旧版用布尔 alias_set「一旦成功永不重设」——换账号后别名永远停在 quant_owner 上
     *    变广播。现改为记「期望别名 alias_desired」：期望值变了（登录/换号/登出）必须重设，
     *    期望值没变才跳过（防 6022「alias 操作进行中」重复触发）。
     *  - 序列号不再固定为 1：极光要求同一进程内递增，alias_seq 持久化自增，
     *    换号重设与冷启动首设共用一条正确序列。
     * 设置结果通过 JPushMessageReceiver.onAliasOperatorResult 回调确认（成功落 alias_registered，
     * 失败按其既有 20s×3 重试，且只重试仍与 alias_desired 一致的别名）。
     */
    private fun setupJPushAlias() {
        val prefs = getSharedPreferences(PREFS_JPUSH, MODE_PRIVATE)
        // §M-11 清理旧布尔标记：它只表示"曾经成功过"，正是它抑制了换号重设（保留会误导排查）
        if (prefs.contains("alias_set")) {
            prefs.edit().remove("alias_set").apply()
        }
        val account = getSharedPreferences("quant_prefs", MODE_PRIVATE)
            .getString(KEY_PUSH_ACCOUNT, "") ?: ""
        val desired = pushAliasFor(account)
        if (prefs.getString(KEY_ALIAS_DESIRED, null) == desired &&
            prefs.getString(KEY_ALIAS_REGISTERED, null) == desired
        ) {
            // 期望别名未变且极光确证注册成功过：不重复设置（等价旧版"已设置"跳过语义，
            // 但基准从"设备级一次性"改为"账号级当前态"）
            return
        }
        val seq = prefs.getInt(KEY_ALIAS_SEQ, 0) + 1
        prefs.edit()
            .putInt(KEY_ALIAS_SEQ, seq)
            .putString(KEY_ALIAS_DESIRED, desired)
            .remove(KEY_ALIAS_RETRY) // 换号即新一轮设置，重试计数归零
            .apply()
        try {
            JPushInterface.setAlias(this, seq, desired)
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