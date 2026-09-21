package com.liangzai.quant

import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.view.KeyEvent
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import com.liangzai.quant.BuildConfig
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.atomic.AtomicBoolean

/**
 * §APPVER（2026-09-22 C批）APK 服务端驱动强制更新闸。
 *
 * 职责：启动后后台查询一次 GET {baseUrl}/api/app/version（服务端公开端点，免鉴权——
 * 强制更新检查必须发生在登录之前），若 BuildConfig.VERSION_CODE < min_version_code
 * 则弹「不可取消」对话框引导去下载/退出。
 *
 * 设计要点：
 *  - fail-open：网络/解析任何失败一律静默放过——App 核心场景是盘内看信号，离线可用性
 *    优先，更新检查绝不允许成为启动阻塞或误拦（服务端未发布 app_release 时 min=0 同样不拦）。
 *  - 单线程 + AtomicBoolean 防重入：进程内同一时刻只允许一条检查链，避免 Activity 重建
 *    （旋转屏等）导致并发请求与重复弹窗。
 *  - HTTPS 证书：quant-trading.top 为 Let's Encrypt 正常公信证书，走系统默认 trust chain
 *    即可，无需任何自定义 trust 处理；server URL 理论上可被用户改为自签地址，那种场景
 *    握手失败即落入 fail-open 分支，不拦启动（有意为之，注释在此备查）。
 *  - URL 来路（SharedPreferences）可能含任意字符：只做字符串拼接后交给 HttpURLConnection
 *    发起请求，不注入任何 JS，因此无 jsQuote 需求（与 MainActivity 的 evaluateJavascript 链路面不同）。
 */
object UpdateGate {

    /** 防重入标志：检查进行中为 true，结束后复位。 */
    private val running = AtomicBoolean(false)

    /**
     * 启动后台更新检查（MainActivity.onCreate 末尾调用）。
     * baseUrlProvider 在工作线程内求值，保持「prefs > DEFAULT_SERVER_URL」优先级实时性。
     */
    fun start(activity: MainActivity, baseUrlProvider: () -> String) {
        if (!running.compareAndSet(false, true)) {
            return
        }
        Thread {
            try {
                checkAndUpdate(activity, baseUrlProvider())
            } catch (e: Exception) {
                // fail-open：任何异常（DNS/超时/JSON/KeyStore 之外的意外）静默，不影响使用
                android.util.Log.d("QUANT_UPDATE", "更新检查失败（静默放行）: ${e.message}")
            } finally {
                running.set(false)
            }
        }.start()
    }

    /** 请求 + 解析 + 决策。所有失败路径均 return（静默）。 */
    private fun checkAndUpdate(activity: MainActivity, baseUrl: String) {
        val base = baseUrl.trim().trimEnd('/')
        // 仅接受 http/https 前缀（防 prefs 被写成 content:// 等意外 scheme）
        if (!base.startsWith("https://", true) && !base.startsWith("http://", true)) {
            return
        }
        val url = URL("$base/api/app/version")
        val conn = url.openConnection() as HttpURLConnection
        try {
            conn.requestMethod = "GET"
            conn.connectTimeout = 10_000
            conn.readTimeout = 10_000
            conn.instanceFollowRedirects = true
            if (conn.responseCode != HttpURLConnection.HTTP_OK) {
                return
            }
            val body = conn.inputStream.bufferedReader().use { it.readText() }
            val json = JSONObject(body)
            if (!json.optBoolean("ok", false)) {
                return
            }
            val minVersionCode = json.optInt("min_version_code", 0)
            // 零值=服务端未发布强制更新（config app_release 缺省），或本包已达标
            if (minVersionCode <= 0 || BuildConfig.VERSION_CODE >= minVersionCode) {
                return
            }
            val apkUrl = json.optString("apk_url", "")
            val note = json.optString("note", "")
            activity.runOnUiThread {
                showDialog(activity, note, apkUrl)
            }
        } finally {
            conn.disconnect()
        }
    }

    /** 强制更新对话框：不可取消（点外/back 均拦），正按钮去下载、负按钮退出。 */
    private fun showDialog(activity: MainActivity, note: String, apkUrl: String) {
        if (activity.isFinishing || activity.isDestroyed) {
            return
        }
        val message = if (note.isBlank()) {
            "当前版本过低，存在安全或功能问题，请更新到最新版本后继续使用。"
        } else {
            note
        }
        val dialog = AlertDialog.Builder(activity)
            .setTitle("需要更新")
            .setMessage(message)
            .setCancelable(false)
            // back 键拦截：setCancelable(false) 已挡点外取消，这里再显式吞掉 BACK 按下事件，
            // 双保险确保对话框只能走「去下载/退出」两个出口
            .setOnKeyListener { _, keyCode, _ -> keyCode == KeyEvent.KEYCODE_BACK }
            .apply {
                if (apkUrl.isNotBlank()) {
                    setPositiveButton("去下载") { _, _ ->
                        try {
                            // 交给系统浏览器/下载器打开 APK 直链；无匹配应用时兜底 Toast 提示
                            activity.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(apkUrl)))
                        } catch (e: ActivityNotFoundException) {
                            Toast.makeText(activity, "未找到可打开下载链接的应用", Toast.LENGTH_LONG).show()
                        }
                    }
                }
                setNegativeButton("退出") { _, _ -> activity.finish() }
            }
            .create()
        dialog.show()
    }
}
