# Android 打包混淆规则（release 已开启 R8 混淆 + 资源收缩）
-keep class androidx.webkit.** { *; }

# 保留全部 @JavascriptInterface 注解方法：WebView JS 桥（window.AndroidNotify.show）
# 若被 R8 混淆/移除，前端 JS 将调用不到原生通知。
-keepclassmembers class * {
    @android.webkit.JavascriptInterface <methods>;
}

# 保留 MainActivity（入口 Activity，Manifest 引用，防混淆/裁剪误删）
-keep class com.liangzai.quant.MainActivity { *; }
-keep class com.liangzai.quant.QuantApp { *; }

# 极光推送 JPush 混淆规则（官方推荐：SDK 类全部保留，Receiver 子类保留）
-dontoptimize
-dontpreverify
-dontwarn cn.jpush.**
-keep class cn.jpush.** { *; }
-keep class * extends cn.jpush.android.service.JPushMessageReceiver { *; }
-dontwarn cn.jiguang.**
-keep class cn.jiguang.** { *; }