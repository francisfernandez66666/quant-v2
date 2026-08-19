package com.liangzai.quant

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.util.Log
import cn.jpush.android.api.JPushInterface
import cn.jpush.android.api.JPushMessage
import cn.jpush.android.service.JPushMessageReceiver

/**
 * 极光推送回调接收器：接收通知/自定义消息到达事件，以及 tag/alias 设置结果回调。
 *
 * 通知栏消息由极光 SDK 内部自动展示（走默认通知渠道），这里主要用于：
 *  - 记录 alias 设置结果（setAlias 的异步结果回这里确认是否成功）
 *  - 记录通知/自定义消息到达日志，便于排查推送链路
 *
 * alias 失败重试：极光错误码 6022 表示「上一次 alias 请求仍在等待响应」，
 * 常见于 App 冷启动后初始化尚未完成即调用 setAlias。官方建议等待 20 秒后再发起
 * 下一次请求，这里按 20s 间隔重试，最多 3 次；成功后写 SharedPreferences 标记，
 * 后续启动不再重复设置。
 * （English: on alias failure, retry every 20s up to 3 times per JPush guidance;
 * persist a success flag so later launches skip re-setting.）
 */
class JPushMessageReceiver : JPushMessageReceiver() {

    override fun onAliasOperatorResult(context: Context, jPushMessage: JPushMessage) {
        super.onAliasOperatorResult(context, jPushMessage)
        val code = jPushMessage.errorCode
        val alias = jPushMessage.alias
        if (code == 0) {
            Log.d(TAG, "JPush alias 设置成功: $alias")
            context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
                .edit().putBoolean(KEY_ALIAS_SET, true).apply()
        } else {
            Log.e(TAG, "JPush alias 设置失败 code=$code alias=$alias，将重试")
            scheduleRetry(context, alias)
        }
    }

    override fun onNotifyMessageArrived(context: Context, notifyMessage: cn.jpush.android.api.NotificationMessage) {
        super.onNotifyMessageArrived(context, notifyMessage)
        Log.d(TAG, "JPush 通知到达: ${notifyMessage.notificationTitle} / ${notifyMessage.notificationContent}")
    }

    override fun onMessage(context: Context, message: cn.jpush.android.api.CustomMessage) {
        super.onMessage(context, message)
        Log.d(TAG, "JPush 自定义消息: ${message.message}")
    }

    /** 失败后延迟 20 秒重试（最多 MAX_RETRY 次，用次数缓存 + seq 递增避免叠加） */
    private fun scheduleRetry(context: Context, alias: String) {
        val prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        val attempts = prefs.getInt(KEY_RETRY_COUNT, 0)
        if (attempts >= MAX_RETRY) {
            Log.e(TAG, "JPush alias 重试次数耗尽，请检查网络/极光控制台配置")
            return
        }
        prefs.edit().putInt(KEY_RETRY_COUNT, attempts + 1).apply()
        val seq = attempts + 1
        Handler(Looper.getMainLooper()).postDelayed({
            Log.i(TAG, "JPush alias 第 $seq 次重试…")
            try {
                JPushInterface.setAlias(context.applicationContext, seq, alias)
            } catch (e: Exception) {
                Log.e(TAG, "JPush setAlias 重试调用异常: ${e.message}")
            }
        }, RETRY_DELAY_MS)
    }

    private companion object {
        const val TAG = "QUANT_JPUSH"

        /** SharedPreferences 文件名：alias 成功标记与重试次数共用 */
        const val PREFS_NAME = "jpush_prefs"

        /** alias 已设置成功的标记（避免每次启动重复设置触发 6022） */
        const val KEY_ALIAS_SET = "alias_set"

        /** 重试计数 */
        const val KEY_RETRY_COUNT = "alias_retry_count"

        /** 极光官方建议的失败重试间隔 */
        const val RETRY_DELAY_MS = 20_000L

        /** 最大重试次数 */
        const val MAX_RETRY = 3
    }
}