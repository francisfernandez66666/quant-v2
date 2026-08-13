// quant-trading-v2 Android 客户端
// 薄壳 APK：内嵌前端构建产物 web/dist（assets/www），通过 WebViewAssetLoader 提供安全源
// https://appassets.androidplatform.net/ 加载页面；API/SSE 全部指向云服务器域名（登录页填写）。
//
// 仓库镜像：国内构建默认走阿里云镜像（官方 Maven 在境内很慢/不稳定），
// 可设 QUANT_USE_OFFICIAL_REPOS=1 切回官方 google()/mavenCentral()。
pluginManagement {
    repositories {
        if (System.getenv("QUANT_USE_OFFICIAL_REPOS") == "1") {
            google()
            mavenCentral()
            gradlePluginPortal()
        } else {
            maven { url = uri("https://maven.aliyun.com/repository/google") }
            maven { url = uri("https://maven.aliyun.com/repository/central") }
            maven { url = uri("https://maven.aliyun.com/repository/gradle-plugin") }
        }
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        if (System.getenv("QUANT_USE_OFFICIAL_REPOS") == "1") {
            google()
            mavenCentral()
        } else {
            maven { url = uri("https://maven.aliyun.com/repository/google") }
            maven { url = uri("https://maven.aliyun.com/repository/central") }
        }
    }
}

rootProject.name = "quant-mobile"
include(":app")