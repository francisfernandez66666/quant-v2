# init_default_config.ps1 — 部署步 [3/5] 的远程配置初始化（§UAT 20260915 部署加固）。
# 职责：仅在数据目录尚无 config.json 时生成"影子模式"默认配置；已有配置一律保留不覆盖。
# 背景：该逻辑原先内联在 deploy_guangzhou.sh 的 SSH 命令里，bash→PowerShell 双层引号
# 转义在每个部署日都产生一段 ParserError 噪音（$cfg 变量被外层吞掉）；现抽为独立脚本
# 由 scp 上传后执行，转义链路归零，输出干净可判读。
#
# 用法：powershell -NoProfile -ExecutionPolicy Bypass -File init_default_config.ps1 -DataDir C:/var/lib/quant-trading-v2
# 注意：全文件保持 UTF-8 BOM——Windows PowerShell 5.1 无 BOM 时按 GBK 读取中文注释会乱码。
# English: deployment step [3/5] helper — writes the shadow-mode default config.json only when
# absent; an existing config is always preserved. Extracted from an inline SSH heredoc whose
# bash→PowerShell quoting chain produced a ParserError on every run.
param(
    [string]$DataDir = "C:/var/lib/quant-trading-v2"
)

$cfgPath = Join-Path $DataDir "config.json"
if (Test-Path $cfgPath) {
    Write-Host "  保留现有 config.json（未覆盖）"
    # §M7a 兼容检测：现网若存有更早版本生成的"根级 qmt"旧文件，提醒运维（只告警，不改写、不拦截）。
    # Go 解析侧（internal/config/config.go Manager.Load 的 wrapper 只有 rules/d1 两段）从不读取
    # 根级 qmt——该段是死数据，留着无害但会误导人工排障。
    # §ROOTQMT（2026-09-22 C批）：清理已收编进部署链，无需手工——见同目录 clean_root_qmt.ps1
    # 与 deploy_guangzhou.sh 步 [3a]（每次部署幂等清理根级 qmt，备份 + 自校验 + 原子写回）。
    try {
        $parsed = Get-Content $cfgPath -Raw | ConvertFrom-Json
        if ($parsed.PSObject.Properties.Name -contains "qmt") {
            Write-Warning "检测到 config.json 根级残留 `"qmt`" 段（旧生成器产物，Go 侧静默忽略，见 §M7a 注释）——本脚本不改写既有配置；清理由部署链步 [3a] 自动执行，脚本见 deploy/qmt-win/clean_root_qmt.ps1（§ROOTQMT），也可手工重跑：powershell -File clean_root_qmt.ps1 -ConfigPath $cfgPath"
        }
    } catch {
        Write-Warning "现有 config.json 解析失败（仅影响 §M7a 残留检测，不影响保留策略）: $($_.Exception.Message)"
    }
    exit 0
}

# 影子模式默认值：qmt.enabled=false → 引擎装配 NoopExecutor，只评分/记账不下真实单，
# 与现网决策链路零冲突（真开实盘走 M4 切流清单 / 设置页，不由部署脚本决定）。
#
# §M7a（2026-09-22 修复批）层级修正：本行旧版把 "qmt" 写在 JSON **根级**——但 Go 解析侧
# （internal/config/config.go Manager.Load，wrapper 仅含 rules/d1 两段）对根级未知键
# **静默忽略**，即旧文件的 qmt.enabled=false 从未被读进 rules.qmt，生成器意图落空。
# 正确层级是 rules.qmt（Rules struct 字段 QMT 的 json tag = "qmt"）。
# 兼容策略（现网存量旧结构文件）：
#   * 行为无差：旧根级 qmt 被忽略时 rules.qmt 走 DefaultQMTConfig（enabled=false/manual），
#     与旧文件想表达的"影子模式"恰好一致，故不会因本次修正产生行为变化；
#   * 不自动改写存量 config.json：引擎与 researchd 双进程 atomic 写同一文件（Save 全量落盘），
#     部署侧改写有竞态风险；且下次 UI 保存即整体覆盖。检测到根级 qmt 时仅告警提示人工处理；
#   * gateway_url 端口与 §H8 service_probe_config.ps1 同源（$ProbeGatewayPort，缺配置回退 8789）。
$gwPort = 8789
$probeCfg = Join-Path $PSScriptRoot "service_probe_config.ps1"
if (Test-Path $probeCfg) {
    . $probeCfg
    $gwPort = $ProbeGatewayPort
}

$defaultJson = '{"rules":{"qmt":{"enabled":false,"mode":"manual","gateway_url":"http://127.0.0.1:' + $gwPort + '"},"scheduler":{},"paper":{"enabled":false}}}'
[System.IO.File]::WriteAllText($cfgPath, $defaultJson)
Write-Host "  已生成默认 config.json (rules.qmt.enabled=false, 影子模式; §M7a 层级修正)"
