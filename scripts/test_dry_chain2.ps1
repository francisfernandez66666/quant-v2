# test_dry_chain2.ps1 - inject dry order only when queued heartbeat is fresh; self-verify
$h = @{ Authorization = 'Bearer .Zzf101817'; 'Content-Type' = 'application/json' }
$base = 'http://127.0.0.1:8789'

"=== 1) wait queued heartbeat fresh (max 60s) ==="
$deadline = (Get-Date).AddSeconds(60)
while ((Get-Date) -lt $deadline) {
    $h = (Invoke-RestMethod -Uri "$base/health" -TimeoutSec 3)
    $h | ConvertTo-Json -Depth 2
    if ($h.queued_connected -eq $true) { break }
    Start-Sleep -Seconds 5
}
if (-not $h.queued_connected) { Write-Output 'ABORT: queued heartbeat never went fresh'; exit 1 }

"=== 2) switch broker -> queued ==="
$h2 = @{ Authorization = 'Bearer .Zzf101817'; 'Content-Type' = 'application/json' }
Invoke-RestMethod -Uri "$base/admin/broker" -Method Post -Headers $h2 -Body '{"broker":"queued"}' | ConvertTo-Json
Start-Sleep -Seconds 2

"=== 3) inject small dry order (100 x 600000.SH @ 9.30) ==="
$stamp = (Get-Date -Format "yyyyMMdd-HHmmss")
$orderBody = '{"signal_id":"TEST-DRY-' + $stamp + '","code":"600000.SH","side":"买入","price_type":"limit","price":9.30,"qty":100,"created_at":"now"}'
Invoke-RestMethod -Uri "$base/order" -Method Post -Headers $h2 -Body $orderBody | ConvertTo-Json
Start-Sleep -Seconds 20

"=== 4) bridge log tail ==="
Get-Content 'C:\qmt\quant-trading-v2\qmt_gateway\bridge_boot.log' -Tail 15 | Out-String -Width 200

"=== 5) health + switch back to xt ==="
(Invoke-RestMethod -Uri "$base/health") | ConvertTo-Json -Depth 2
Invoke-RestMethod -Uri "$base/admin/broker" -Method Post -Headers $h2 -Body '{"broker":"xt"}' | ConvertTo-Json
Start-Sleep -Seconds 3
(Invoke-RestMethod -Uri "$base/health") | ConvertTo-Json -Depth 2
