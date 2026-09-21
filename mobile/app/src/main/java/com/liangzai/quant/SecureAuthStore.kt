package com.liangzai.quant

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKeys

/**
 * §NATIVEAUTH（2026-09-22 C批）登录 token 原生安全存储。
 *
 * 职责：把登录 token 从 WebView localStorage（domStorage 明文域文件，root/越狱设备或
 * adb backup 场景可直接读库取票）迁入系统级加密偏好（EncryptedSharedPreferences），
 * 由 AndroidKeyStore 托管 AES256_GCM 主密钥，token 静态落盘时密钥密文分离。
 *
 * 威胁模型说明（为什么这不算根治、但确实收小了面）：
 *  - JS 运行时可达性与 localStorage 等价——任何注入进该 Origin 的 JS 依旧能通过
 *    window.AndroidAuth.getToken() 拿票，这一点不劣化也不改善；
 *  - 本机制真正收小的是「静态落盘面」：设备丢失/离线取证下，明文域文件不再直接可读；
 *  - 动态注入面（release 远程调试）已另修——§M12 将 setWebContentsDebuggingEnabled
 *    收进 BuildConfig.DEBUG 门控。两者合起来即 §M12c 残余风险的收口方案。
 */
object SecureAuthStore {

    /** 加密偏好文件名（与旧 WebView localStorage 域文件完全隔离，互不影响）。 */
    private const val PREFS_NAME = "quant_auth"

    /** token 键名。 */
    private const val KEY_TOKEN = "token"

    /** 应用级 Context（init 时缓存 applicationContext，避免泄漏 Activity）。 */
    @Volatile
    private var appContext: Context? = null

    /** 懒建的加密偏好实例；初始化失败（KeyStore 异常等）时保持 null，读写退化为安全失败。 */
    @Volatile
    private var encryptedPrefs: SharedPreferences? = null

    /**
     * 初始化：MainActivity 在注册 JS 桥之前调用一次。
     * 真正的 EncryptedSharedPreferences 创建放到首次读写（懒初始化），
     * 因为 MasterKeys 首次生成/解锁可能涉及 KeyStore IPC，不放在 onCreate 关键路径上。
     */
    fun init(context: Context) {
        appContext = context.applicationContext
    }

    /**
     * 取（或懒建）加密偏好实例。
     * 标准写法：MasterKeys.getOrCreate(AES256_GCM_SPEC) 生成/复用 AndroidKeyStore 里的
     * 主密钥别名，再以此建 EncryptedSharedPreferences（键名 AES256_SIV、值 AES256_GCM 双方案）。
     * 任何异常（KeyStore 不可用、设备策略限制等）→ 返回 null，调用方按「无 token」处理，
     * 绝不回退明文存储（回退即失去本机制的意义）。
     */
    private fun prefs(): SharedPreferences? {
        encryptedPrefs?.let { return it }
        val ctx = appContext ?: return null
        synchronized(this) {
            encryptedPrefs?.let { return it }
            return try {
                val masterKeyAlias = MasterKeys.getOrCreate(MasterKeys.AES256_GCM_SPEC)
                val created = EncryptedSharedPreferences.create(
                    PREFS_NAME,
                    masterKeyAlias,
                    ctx,
                    EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                    EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
                )
                encryptedPrefs = created
                created
            } catch (e: Exception) {
                android.util.Log.e("QUANT_AUTH", "加密偏好初始化失败: ${e.message}")
                null
            }
        }
    }

    /** 写入 token；返回 false 表示加密存储不可用（前端据此感知并可回落自身存储）。 */
    @Synchronized
    fun putToken(token: String): Boolean {
        return prefs()?.edit()?.putString(KEY_TOKEN, token)?.commit() ?: false
    }

    /** 读取 token；无票或加密存储不可用时返回空串（桥语义与 localStorage.getItem 缺键一致）。 */
    @Synchronized
    fun getToken(): String {
        return prefs()?.getString(KEY_TOKEN, "") ?: ""
    }

    /** 清除 token（登出/换票前调用）。 */
    @Synchronized
    fun clearToken() {
        prefs()?.edit()?.remove(KEY_TOKEN)?.apply()
    }
}
