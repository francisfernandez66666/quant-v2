package com.liangzai.quant

import android.content.Context
import android.util.Log
import cn.jpush.android.api.JPushMessage
import cn.jpush.android.service.JPushMessageReceiver

/**
 * 极光推送回调接收器：接收通知/自定义消息到达事件，以及 tag/alias 设置结果回调。
 *
 * 通知栏消息由极光 SDK 内部自动展示（走默认通知渠道），这里主要用于：
 *  - 记录 alias 设置结果（setAlias 的异步结果回这里确认是否成功）
 *  - 记录通知/自定义消息到达日志，便于排查推送链路
 */
class JPushMessageReceiver : JPushMessageReceiver() {

    override fun onAliasOperatorResult(context: Context, jPushMessage: JPushMessage) {
        super.onAliasOperatorResult(context, jPushMessage)
        val code = jPushMessage.errorCode
        val alias = jPushMessage.alias
        // code=0 表示设置成功，非 0 见极光错误码文档
        if (code == 0) {
            Log.d(TAG, "JPush alias 设置成功: $alias")
        } else {
            Log.e(TAG, "JPush alias 设置失败 code=$code alias=$alias")
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

    private companion object {
        const val TAG = "QUANT_JPUSH"
    }
}
