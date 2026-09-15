// seed.js — Uptime Kuma 无头初始化脚本（HARDENING 件2）：
// 创建 admin（首次 setup）→ 登录 → 建 ntfy 通知通道 → 建监控项。可重复执行（幂等：按名称去重）。
// 凭据不硬编码：admin 密码从 macOS 钥匙串取（security find-generic-password -a kuma -s quant-kuma-admin -w）。
// 用法：cd ~/services/uptime-kuma && NODE_PATH=$PWD/node_modules node /path/to/seed.js [ntfy_topic]
const { io } = require("socket.io-client");
const { execSync } = require("child_process");

const URL = "http://127.0.0.1:3001";
const USERNAME = "kuma";
const NTFY_TOPIC = process.argv[2] || "5fc177ea37320815462af122b5218849";
const PASSWORD = execSync(
    'security find-generic-password -a "kuma" -s quant-kuma-admin -w'
).toString().trim();

const MONITORS = [
    { name: "quant-site", type: "http", url: "https://quant-trading.top/", interval: 60, accepted_statuscodes: ["200"], expiryNotification: true },
    { name: "quant-api-health", type: "http", url: "https://quant-trading.top/api/health", interval: 60, accepted_statuscodes: ["200", "401"] },
    { name: "quant-emergency-8080", type: "http", url: "http://81.71.69.17:8080/", interval: 300, accepted_statuscodes: ["200", "302", "404"] },
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
