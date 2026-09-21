# clean_root_qmt.ps1 — §ROOTQMT（2026-09-22 C 批）config.json 根级 "qmt" 死键幂等清理。
# 职责：把根级残留的 "qmt" 段（旧 init_default_config.ps1 生成器写错层级的产物）删掉，
# 让文件根键回到 {rules, d1} 与 Go 解析侧一致；无残留则直接报 CLEAN，属幂等常态步骤。
# 背景：Go 解析侧（internal/config/config.go Manager.Load 的 wrapper 只有 rules / d1 两段）
# 从不读取根级 "qmt"——该段是死数据，留着不改变任何行为，却会让人工排障时误以为
# 影子模式开关写在根级（§M7a 缺陷本体）。生成器层级修正后，存量文件里的残留由本脚本收口。
# 为什么可以一次性清理且不会回写：引擎与 researchd 的 Save 都是全量落盘，写出的根键
# 只可能是 {rules, d1}，删掉的根级 qmt 不可能被回写；引擎另有每分钟热载，改完自动生效。
# 部署链位置：deploy_guangzhou.sh 步 [3a]（随链常态执行 = 防回潮），失败仅告警不中断。
# 输出契约（供部署侧与运维判读，标记全 ASCII）：
#   ROOTQMT: NOFILE      配置文件不存在（尚未生成，天然无残留）
#   ROOTQMT: CLEAN       根级无 "qmt" 段
#   ROOTQMT: CLEANED     已删除并原子写回，附 backup=备份文件名
#   ROOTQMT: PARSE_FAIL  JSON 解析失败或顶层非对象——绝不动文件
#   ROOTQMT: ROLLBACK    写回后自校验未通过，已恢复备份
# 注意：全文件保持 UTF-8 BOM——Windows PowerShell 5.1 无 BOM 时按 GBK 读取中文注释会乱码；
# 中文注释与输出串里 $变量 后一律不直接紧贴全角字符（§A7-B 元检查教训：会被吞进变量名）。
# English: idempotent cleanup of the legacy root-level "qmt" key in config.json (dead data: the
# Go config wrapper only ever reads rules/d1). Runs on every deploy as step [3a] so the stale key
# cannot come back; backs up, validates and rolls back around the rewrite.
param(
    [string]$ConfigPath = 'C:\var\lib\quant-trading-v2\config.json'
)

# 非终止错误也要进 try/catch（备份/写回/复核任一失败都必须走 ROLLBACK 分支，不能带病继续）
$ErrorActionPreference = "Stop"

# ---- 1. 文件不存在：无需清理 ----
if (-not (Test-Path -LiteralPath $ConfigPath)) {
    Write-Host "ROOTQMT: NOFILE"
    Write-Host "  配置文件不存在，天然无根级残留: $ConfigPath"
    exit 0
}

# ---- 2. 解析失败绝不动文件（宁可留给人工，也不能把坏 JSON 再序列化一遍落盘）----
try {
    $parsed = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
} catch {
    Write-Host "ROOTQMT: PARSE_FAIL"
    Write-Host "  JSON 解析失败（文件未改动）: $($_.Exception.Message)"
    exit 1
}
if ($parsed -isnot [System.Management.Automation.PSCustomObject]) {
    Write-Host "ROOTQMT: PARSE_FAIL"
    Write-Host "  顶层不是 JSON 对象（数组/标量），拒绝改写"
    exit 1
}

# ---- 3. 根级无 "qmt"：幂等直通 ----
if (-not ($parsed.PSObject.Properties.Name -contains 'qmt')) {
    Write-Host "ROOTQMT: CLEAN"
    $rootKeys = ($parsed.PSObject.Properties.Name | Sort-Object) -join ","
    Write-Host "  根级无 qmt 残留，当前根键 = { $rootKeys }"
    exit 0
}

# ---- 4. 检出残留：备份 -> 删段 -> 序列化自校验 -> 原子写回 -> 读回复核 ----
$stamp = Get-Date -Format 'yyyyMMddHHmmss'
# 路径拼接用 ${var} 花括号形式：双引号串里 `$ConfigPath.bak-...` 的点号易与属性访问混淆，写死更稳
$backupPath = "${ConfigPath}.bak-rootqmt-$stamp"
$tmpPath = "${ConfigPath}.tmp-rootqmt"
$backupName = Split-Path -Leaf $backupPath

try {
    Copy-Item -LiteralPath $ConfigPath -Destination $backupPath
    Write-Host "  已备份原文件 -> $backupName"

    # 留痕：删掉的段内容压缩打印一份，事后可核对是否与旧 rules.qmt 重复（正常情况是冗余副本）
    $removedJson = ($parsed.qmt | ConvertTo-Json -Compress -Depth 10)
    Write-Host "  待删根级 qmt 段 = $removedJson"

    $parsed.PSObject.Properties.Remove('qmt')
    $json = $parsed | ConvertTo-Json -Depth 100

    # 写盘前自校验①：序列化结果必须仍可解析，且根级确实不再有 qmt
    $reparsed = $json | ConvertFrom-Json
    if ($reparsed.PSObject.Properties.Name -contains 'qmt') {
        throw "自校验失败：序列化结果仍含根级 qmt 段"
    }

    # 原子写回：先落临时文件（UTF8 无 BOM），再 Move-Item -Force 整体替换，避免半截文件
    [System.IO.File]::WriteAllText($tmpPath, $json, (New-Object System.Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $tmpPath -Destination $ConfigPath -Force

    # 写盘后自校验②：读回复核（防 Move 过程被占用/截断）
    $final = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
    if ($final.PSObject.Properties.Name -contains 'qmt') {
        throw "复核失败：落盘文件根级仍有 qmt 段"
    }
} catch {
    Write-Host "ROOTQMT: ROLLBACK"
    Write-Host "  清理或自校验失败，正在恢复备份: $($_.Exception.Message)"
    try {
        Copy-Item -LiteralPath $backupPath -Destination $ConfigPath -Force
        Write-Host "  已恢复原文件（备份保留: ${backupName}）"
    } catch {
        Write-Host "  X 备份恢复同样失败！请手工用 $backupName 覆盖回 $ConfigPath"
    }
    Remove-Item -LiteralPath $tmpPath -Force -ErrorAction SilentlyContinue
    exit 1
}

Remove-Item -LiteralPath $tmpPath -Force -ErrorAction SilentlyContinue   # 兜底：正常路径已被 Move 消费
Write-Host "ROOTQMT: CLEANED backup=$backupName"
Write-Host "  根级 qmt 死键已移除（引擎全量落盘只写 rules/d1，不会回写；引擎每分钟热载自动生效）"
