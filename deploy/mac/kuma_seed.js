// seed.js — Uptime Kuma 无头初始化脚本（HARDENING 件2）：
// 创建 admin（首次 setup）→ 登录 → 建 ntfy 通知通道 → 建监控项。可重复执行（幂等：按名称去重）。
// 凭据不硬编码：admin 密码从 macOS 钥匙串取（security find-generic-password -a kuma -s quant-kuma-admin -w）。
//
// §KUMA-SECREDTO（2026-10-07 修复批 波 3）——两个必传参数，仓库里不再留任何缺省值：
//   用法：cd ~/services/uptime-kuma && NODE_PATH=$PWD/node_modules \
//         node /path/to/seed.js <ntfy_topic> <gz_public_ip>
//   为什么改：
//   ① ntfy 的口径是「知道主题就能往那个主题发帖」⇒ 主题串本身就是凭据。它以前作为 32-hex
//      缺省值写在这里（同一份值还抄在 restic_pull_backup.sh 与 verify_restore.sh），已经进了
//      git 历史——**改文件洗不掉历史**，所以这个主题按「已泄露」处理（轮换＝owner 当面看预演的
//      现网动作，不在本批自动执行面内）。
//   ② 公网 IP 同样不许内嵌（本仓纪律：部署/校验命令与仓库文件只走参数或 ssh 别名）。以前
//      内嵌字面 IP 让「§0929DRILL 新增腿不许内嵌字面公网 IP」那把负锁**结构性失明**——
//      那把锁的判据从 deploy/mac 的 *.sh + plist 派生，.js 文件类型压根不在射程里
//      （清单式锁第三次复发：§BOM-REPO → §BOM-REPO-DERIVE → 本次；本批把文件面改成从
//      `git ls-files deploy/mac` 派生，见门禁 §110）。
//   缺参数即**非零退出**并打印缺哪一个（只报参数名，绝不把主题值回显进日志/终端）。
// English: the ntfy topic and the Guangzhou public IP are now mandatory CLI arguments. Nothing
// secret stays in the file — the topic that used to be a default value is already in git history,
// so it must be treated as leaked (rotation is an owner action), and no literal public IP may be
// embedded in a tracked file.
// ⚠ 两个 require 刻意**排在参数校验之后**（见下面 §KUMA-SECREDTO 校验块再往后的位置）。
//   理由不是风格：本文件要在 uptime-kuma 的 node_modules 里才找得到 socket.io-client，
//   而在仓库工作目录里直接 `node kuma_seed.js` 会先撞 MODULE_NOT_FOUND——非零退出是"意外发生
//   对了"，SEED_FAIL 那条永远看不见。于是"缺参必非零退出"这条判据会在**没有校验代码**的情况下
//   照样通过（§测试断言自伤里"判据的失败原因必须是我以为的原因"那一族）。
//   校验挪到最前面之后，缺参退出与依赖是否装齐彻底解耦，门禁 §110 的 F5 腿才证的是它想证的事。
const URL = "http://127.0.0.1:3001";
const USERNAME = "kuma";

// 参数校验放在模块加载最前面：这里 exit 得越早，越不会出现「连上 kuma、建了一半监控」的半态。
const NTFY_TOPIC = process.argv[2] || "";
const GZ_PUBLIC_IP = process.argv[3] || "";
{
    const missing = [];
    if (!NTFY_TOPIC) missing.push("ntfy_topic(argv[2])");
    if (!GZ_PUBLIC_IP) missing.push("gz_public_ip(argv[3])");
    if (missing.length > 0) {
        console.error("SEED_FAIL: 缺少必传参数 " + missing.join(", ") +
            "（用法：node seed.js <ntfy_topic> <gz_public_ip>；值不写入仓库，见文件头 §KUMA-SECREDTO）");
        process.exit(2);
    }
    // 形状的最低要求：IPv4 四段点分十进制。不是格式洁癖——这个串会拼进监控项 URL，
    // 拼错了 kuma 会照样建出一个"永远连不上"的监控项并把它标成红，看起来像现网坏了。
    const ipOk = /^\d{1,3}(\.\d{1,3}){3}$/.test(GZ_PUBLIC_IP) &&
        GZ_PUBLIC_IP.split(".").every((p) => Number(p) >= 0 && Number(p) <= 255);
    if (!ipOk) {
        console.error("SEED_FAIL: gz_public_ip 不是四段点分十进制的 IPv4（不回显值，只报形状不符）");
        process.exit(2);
    }
}

const { io } = require("socket.io-client");
const { execSync } = require("child_process");

const PASSWORD = execSync(
    'security find-generic-password -a "kuma" -s quant-kuma-admin -w'
).toString().trim();

const MONITORS = [
    { name: "quant-site", type: "http", url: "https://quant-trading.top/", interval: 60, accepted_statuscodes: ["200"], expiryNotification: true },
    { name: "quant-api-health", type: "http", url: "https://quant-trading.top/api/health", interval: 60, accepted_statuscodes: ["200", "401"] },
    // 应急直连腿（域名/反代坏了的时候这条还能报活），端口＝引擎端口，按参数拼、不内嵌地址。
    { name: "quant-emergency-8080", type: "http", url: `http://${GZ_PUBLIC_IP}:8080/`, interval: 300, accepted_statuscodes: ["200", "302", "404"] },
];

const socket = io(URL, { transports: ["websocket"], forceNew: true });

// 一次性等待服务器推送（notificationList/monitorList 登录后自动推，非请求-响应式）
function pushed(event, timeoutMs = 8000) {
    return new Promise((resolve) => {
        const t = setTimeout(() => resolve(null), timeoutMs);
        socket.once(event, (data) => { clearTimeout(t); resolve(data); });
    });
}

function call(event, ...args) {
    return new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error(`timeout ${event}`)), 20000);
        socket.emit(event, ...args, (res) => { clearTimeout(timer); resolve(res); });
    });
}

async function main() {
    await new Promise((resolve, reject) => {
        socket.on("connect", resolve);
        socket.on("connect_error", reject);
    });
    console.log("connected to kuma");

    const need = await call("needSetup");
    if (need) {
        const r = await call("setup", USERNAME, PASSWORD);
        if (!r.ok) throw new Error("setup failed: " + r.msg);
        console.log("admin created");
    }
    const login = await call("login", { username: USERNAME, password: PASSWORD });
    if (!login.ok) throw new Error("login failed: " + (login.msg || JSON.stringify(login)));
    console.log("logged in");

    // 等登录后推送
    const notifList = (await pushed("notificationList")) || [];
    const monList = await pushed("monitorList");
    const monitors = monList ? Object.values(monList) : [];
    console.log(`state: notifications=${notifList.length} monitors=${monitors.length}`);

    if (!notifList.some((n) => n.name === "ntfy-ops")) {
        const r = await call("addNotification", {
            name: "ntfy-ops",
            type: "ntfy",
            isDefault: true,
            active: true,
            userKey: "",
            ntfyserverurl: "https://ntfy.sh",
            ntfytopic: NTFY_TOPIC,
            ntfyPriority: 4,
            ntfyTags: "",
        }, null);
        console.log("add ntfy-ops:", JSON.stringify(r).slice(0, 80));
    } else {
        console.log("ntfy-ops exists, skip");
    }

    for (const m of MONITORS) {
        if (monitors.some((x) => x.name === m.name)) {
            console.log("monitor exists:", m.name);
            continue;
        }
        const r = await call("add", {
            type: m.type,
            name: m.name,
            url: m.url,
            interval: m.interval,
            retryInterval: 60,
            weight: 2000,
            maxretries: 2,
            ignoreTls: false,
            upsideDown: false,
            maxredirects: 3,
            expiryNotification: !!m.expiryNotification,
            method: "GET",
            accepted_statuscodes: m.accepted_statuscodes,
            conditions: {},
            rabbitmqNodes: null,
            kafkaProducerBrokers: null,
            kafkaProducerSaslOptions: null,
            notificationIDList: {},
            active: true,
        });
        console.log("added monitor", m.name, "id=", r);
    }
    console.log("SEED_DONE");
    process.exit(0);
}

main().catch((e) => { console.error("SEED_FAIL:", e.message); process.exit(1); });
