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
        minSdk = 24
        targetSdk = 35
        versionCode = 1
        versionName = "1.0.0"
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
}