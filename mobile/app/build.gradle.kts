// 移动端应用模块：WebView 薄壳，内嵌前端构建产物 assets/www，指向远程云服务器 API。
//
// 重要参数化点（等你资源就绪后只改这一处）：
//   applicationId / namespace —— APK 的包名（决定装到手机上的应用标识）
//   defaultConfig 里的 versionName / versionCode —— 版本号
//   res/values/strings.xml 里的 app_name —— 显示名
//   MainActivity.kt 里的 DEFAULT_SERVER_URL —— 预填的服务器地址（可留空，登录页手填）
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.liangzai.quant"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.liangzai.quant"
        minSdk = 24          // Android 7.0：覆盖仍活跃的低版本机型；低于此的 WebView 不支持 SSE
        targetSdk = 35        // 目标 Android 15：新装/更新走最新隐私与权限模型
        // §NATIVEAUTH+§APPVER（2026-09-22 C批）：版本跃迁 1→2 / 1.0.0→1.1.0。
        // 为什么直接跳 2：v1 包没有任何更新通道（服务端驱动强制更新自 v2 起才生效），
        // 旧 v1 用户无法被远程引导升级，须由 owner 重装一次 v2；此后 v2+ 由 UpdateGate
        // 强制更新链路接管（min_version_code 门槛拦截）。
        versionCode = 2       // 整数递增版本号（每次上架 +1，Google Play 与覆盖安装据此判新旧）
        versionName = "1.1.0" // 展示用语义版本号（v2：登录 token 迁原生加密存储 + 强制更新闸）

        // 极光推送 JPush：包名 + AppKey（极光控制台创建应用后获得）+ 渠道号
        // （JPush placeholders: package name, AppKey from the JPush console, channel label.）
        manifestPlaceholders["JPUSH_PKGNAME"] = "com.liangzai.quant"
        manifestPlaceholders["JPUSH_APPKEY"] = "bf0fd9cb1beafa282f88329c"
        manifestPlaceholders["JPUSH_CHANNEL"] = "developer-default"
    }

    buildTypes {
        release {
            // 加固：开启 R8 代码混淆 + 资源收缩，提高反编译/逆向成本。
            // （Hardening: enable R8 code obfuscation and resource shrinking to raise reverse-engineering cost.）
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    // §A7 修复（2026-09-20）：MainActivity.kt 引用 com.liangzai.quant.BuildConfig（BuildConfig.DEBUG
    // 用于 debug/release 下服务器地址校验策略分流），AGP 8.x 默认不再为应用模块生成 BuildConfig，
    // 必须显式开启，否则 compileDebugKotlin 报 "Unresolved reference: BuildConfig"。
    buildFeatures {
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
    // WebViewAssetLoader：把内嵌 assets/www 映射为 https 安全源，localStorage/SSE 正常工作
    implementation("androidx.webkit:webkit:1.11.0")
    // 极光推送 JPush Android SDK（5.0.0 起自动拉取 JCore，无需单独配置）
    implementation("cn.jiguang.sdk:jpush:5.8.0")
    // §NATIVEAUTH（2026-09-22 C批）：EncryptedSharedPreferences——AES256_GCM 主密钥托管于
    // AndroidKeyStore（密钥本身不出安全硬件/系统密钥区），登录 token 静态落盘加密，
    // 收小 root/adb backup 场景直接读库拿明文票的攻击面（详见 SecureAuthStore.kt 文件头）。
    implementation("androidx.security:security-crypto:1.1.0-alpha06")
}