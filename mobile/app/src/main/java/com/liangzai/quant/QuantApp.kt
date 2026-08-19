package com.liangzai.quant

import android.app.Application
import cn.jpush.android.api.JPushInterface

/**
 * 应用入口：初始化极光推送 JPush。
 *
 * 极光 SDK 建议在 Application.onCreate 中完成初始化，进程拉起（含推送保活进程）时即注册，
 * 从而保证 App 在后台/被杀后仍能收到系统级通知。setDebugMode 需在 init 之前调用。
 */
class QuantApp : Application() {

    override fun onCreate() {
        super.onCreate()
        // 调试模式：开发阶段置 true 查看极光日志，正式发布改回 false
        JPushInterface.setDebugMode(false)
        JPushInterface.init(this)
    }
}
