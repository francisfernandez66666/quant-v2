// 顶层构建脚本：声明插件版本（供子模块 app 复用），不在此应用插件。
plugins {
    id("com.android.application") version "8.5.2" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
}