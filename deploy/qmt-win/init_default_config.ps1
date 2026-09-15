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
    exit 0
}

# 影子模式默认值：qmt.enabled=false → 引擎装配 NoopExecutor，只评分/记账不下真实单，
# 与现网决策链路零冲突（真开实盘走 M4 切流清单 / 设置页，不由部署脚本决定）。
$defaultJson = '{"qmt":{"enabled":false,"mode":"manual","gateway_url":"http://127.0.0.1:8789"},"rules":{"scheduler":{},"paper":{"enabled":false}}}'
[System.IO.File]::WriteAllText($cfgPath, $defaultJson)
Write-Host "  已生成默认 config.json (qmt.enabled=false, 影子模式)"
