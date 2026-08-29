$ErrorActionPreference = "SilentlyContinue"
Write-Host "=== services (qmt/quant/pydata) ==="
Get-Service | Where-Object { $_.Name -match "qmt|quant|pydata" } | Format-Table Name,Status,StartType -AutoSize
Write-Host "=== gateway config.xt.json ==="
$p = "C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json"
Write-Host "config path: $p"
if (Test-Path $p) {
    $c = Get-Content $p -Raw | ConvertFrom-Json
    $out = [pscustomobject]@{
        report_url = $c.report_url
        broker     = $c.broker
        account    = $c.account
        token_len  = ($c.token.Length)
        xt_path    = $c.xt_path
    }
    $out | Format-List
}
Write-Host "=== python ==="
(Get-Command python).Source
Write-Host "=== paths ==="
"opt_quant=" + (Test-Path C:\opt\quant)
"data_cfg=" + (Test-Path C:\var\lib\quant-trading-v2\config.json)
"gw_dir=" + (Test-Path C:\qmt\quant-trading-v2\qmt_gateway)
"userprofile_ssh_ak=" + (Test-Path C:\Users\Administrator\.ssh\authorized_keys)
"progdata_ssh_ak=" + (Test-Path C:\ProgramData\ssh\administrators_authorized_keys)
