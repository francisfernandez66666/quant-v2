#!/bin/bash
# 构建 Android APK：编译前端 web/dist → 拷入 assets/ → Gradle 打包。
#
# 前置条件（首次运行自动准备）：
#   - JDK 17（Homebrew openjdk@17，脚本会自动探测）
#   - Android SDK（脚本探测 ANDROID_HOME / ~/Library/Android/sdk / ~/Android/Sdk）
#   - Gradle 8.9（脚本下载到 ~/.gradle-bootstrap 生成 wrapper，无需全局安装）
#
# 用法：./scripts/build_apk.sh [debug|release]
#   默认 debug（免签名，可直接安装）；release 需要签名配置（见下）。
#
# 打 release 前配置签名（二选一）：
#   A) 自动生成自签名 keystore：MOBILE_KEYSTORE_PASS=xxx ./scripts/build_apk.sh release
#   B) 提供现有 keystore：MOBILE_KEYSTORE=xx.jks MOBILE_KEYSTORE_PASS=xxx ./scripts/build_apk.sh release
set -euo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MODE="${1:-debug}"
cd "$APP_DIR"

GRADLE_VERSION="8.9"
GRADLE_BOOT="$HOME/.gradle-bootstrap"
GRADLE_HOME="$GRADLE_BOOT/gradle-$GRADLE_VERSION"
# 下载源：国内优先阿里云镜像（海外/官方源很慢），失败再回退官方
GRADLE_DIST_URL="${GRADLE_DIST_URL:-https://mirrors.aliyun.com/macports/distfiles/gradle/gradle-$GRADLE_VERSION-bin.zip}"

echo "=============================================="
echo " quant-trading-v2 APK 构建 (mode=$MODE)"
echo "=============================================="

# ── 1. JDK 17 ──
JAVA_BIN=""
JAVA_HOME_CFG="${JAVA_HOME:-}"
if [ -n "$JAVA_HOME_CFG" ] && [ -x "$JAVA_HOME_CFG/bin/java" ]; then
    JAVA_BIN="$JAVA_HOME_CFG/bin/java"
fi
if [ -z "$JAVA_BIN" ]; then
    for cand in "/opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home/bin/java" \
                "/usr/local/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home/bin/java"; do
        if [ -x "$cand" ]; then JAVA_BIN="$cand"; break; fi
    done
fi
if [ -z "$JAVA_BIN" ] && command -v java >/dev/null 2>&1; then JAVA_BIN="$(command -v java)"; fi
if [ -z "$JAVA_BIN" ]; then
    echo "[!] 未找到 JDK 17。安装: brew install openjdk@17"
    exit 1
fi
JAVA_HOME_DIR="$(cd "$(dirname "$(dirname "$JAVA_BIN")")" && pwd)"
echo "[1/5] JDK: $JAVA_BIN ($JAVA_HOME_DIR)"

# ── 2. Android SDK ──
ANDROID_SDK="${ANDROID_HOME:-}"
if [ -z "$ANDROID_SDK" ]; then
    for cand in "$HOME/Library/Android/sdk" "$HOME/Android/Sdk" "/opt/android-sdk"; do
        [ -d "$cand" ] && ANDROID_SDK="$cand" && break
    done
fi
if [ -z "$ANDROID_SDK" ] || [ ! -d "$ANDROID_SDK/platforms" ]; then
    echo "[!] 未找到 Android SDK（设置 ANDROID_HOME 或安装到默认路径）"
    exit 1
fi
echo "[2/5] Android SDK: $ANDROID_SDK"
# 写入 local.properties 供 Gradle 定位 SDK
cat > "$APP_DIR/mobile/local.properties" <<EOF
sdk.dir=$ANDROID_SDK
EOF

# ── 3. 前端构建 + 拷入 assets/ 根 ──
echo "[3/5] 构建前端并拷入 assets/..."
(cd "$APP_DIR/web" && npm run build >/dev/null)
rm -rf "$APP_DIR/mobile/app/src/main/assets"
mkdir -p "$APP_DIR/mobile/app/src/main/assets"
cp -R "$APP_DIR/web/dist/." "$APP_DIR/mobile/app/src/main/assets/"
echo "      assets/ 共 $(find "$APP_DIR/mobile/app/src/main/assets" -type f | wc -l | tr -d ' ') 个文件"

# ── 4. Gradle wrapper（首次下载 8.9）──
echo "[4/5] 准备 Gradle $GRADLE_VERSION..."
cd "$APP_DIR/mobile"
if [ ! -f gradle/wrapper/gradle-wrapper.jar ]; then
    if [ ! -x "$GRADLE_HOME/bin/gradle" ]; then
        echo "      下载 Gradle $GRADLE_VERSION ..."
        mkdir -p "$GRADLE_BOOT"
        curl -fsSL -o "$GRADLE_BOOT/gradle.zip" "$GRADLE_DIST_URL" || \
            curl -fsSL -o "$GRADLE_BOOT/gradle.zip" "https://services.gradle.org/distributions/gradle-$GRADLE_VERSION-bin.zip"
        (cd "$GRADLE_BOOT" && unzip -q -o gradle.zip)
        rm -f "$GRADLE_BOOT/gradle.zip"
    fi
    echo "      生成 wrapper..."
    JAVA_HOME="$JAVA_HOME_DIR" "$GRADLE_HOME/bin/gradle" wrapper --gradle-version "$GRADLE_VERSION" --distribution-type bin
fi
chmod +x gradlew

# ── 5. 打包 ──
echo "[5/5] 打包 APK (${MODE})..."
export JAVA_HOME="$JAVA_HOME_DIR"
export ANDROID_HOME="$ANDROID_SDK"
if [ "$MODE" = "release" ]; then
    # 签名配置：keystore 默认 mobile/keystore.jks。
    # §GAP2-W1（P0）：密码不再有任何硬编码默认值——旧默认口令已随 gradle.properties 入 git 泄漏，
    # 必须通过环境变量 MOBILE_KEYSTORE_PASS 提供；首次生成新 keystore 时自动创建随机强口令
    # 并保存到本地不入库的 mobile/keystore.pass（chmod 600）。
    KS="${MOBILE_KEYSTORE:-$APP_DIR/mobile/keystore.jks}"
    PASSFILE="$APP_DIR/mobile/keystore.pass"
    if [ -n "$MOBILE_KEYSTORE_PASS" ]; then
        KSPASS="$MOBILE_KEYSTORE_PASS"
    elif [ -f "$PASSFILE" ] && [ "$KS" = "$APP_DIR/mobile/keystore.jks" ]; then
        # 复用首跑生成的随机口令（文件仅本机，已被 .gitignore 覆盖）
        KSPASS="$(cat "$PASSFILE")"
    else
        KSPASS="$(openssl rand -base64 24 | tr -d '\n')"
        umask 177; printf '%s' "$KSPASS" > "$PASSFILE"; umask 022
        echo "      已生成随机签名口令 → $PASSFILE（请离线另存一份；切勿提交仓库）"
    fi
    if [ ! -f "$KS" ]; then
        echo "      生成自签名 keystore: $KS"
        "$JAVA_HOME/bin/keytool" -genkeypair -v \
            -keystore "$KS" -storepass "$KSPASS" -alias quant \
            -keyalg RSA -keysize 2048 -validity 3650 \
            -dname "CN=liangzai, OU=quant, O=quant, L=Beijing, ST=Beijing, C=CN"
    fi
    # §GAP2-W1 签名传参：android.injected.signing.* 是带点号的属性名，无法经环境变量表达
    # （ORG_GRADLE_PROJECT_ 下划线变体会被 Gradle 当成另一个属性导致静默不签名），
    # 必须用 -P 点号原样传入；属性仅本次构建生效，不落任何文件。
    # English: §GAP2-W1 signing args — the dotted android.injected.signing.* property names cannot be
    # expressed via environment variables (the underscore variant becomes a different, ignored
    # property, yielding a silently unsigned APK); pass them verbatim with -P for this invocation only.
    SIGN_ARGS=(
        -Pandroid.injected.signing.store.file="$KS"
        -Pandroid.injected.signing.store.password="$KSPASS"
        -Pandroid.injected.signing.key.alias=quant
        -Pandroid.injected.signing.key.password="$KSPASS"
    )
    ./gradlew assembleRelease "${SIGN_ARGS[@]}"
    OUT="app/build/outputs/apk/release/app-release.apk"
else
    ./gradlew assembleDebug
    OUT="app/build/outputs/apk/debug/app-debug.apk"
fi

echo "=============================================="
echo " APK 已生成: $APP_DIR/mobile/$OUT"
echo " 安装: adb install $APP_DIR/mobile/$OUT"
echo "=============================================="