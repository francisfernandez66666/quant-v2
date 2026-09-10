$dir = 'C:\qmt\quant-trading-v2\qmt_gateway'
"=== boot log tail ==="
if (Test-Path ($dir + '\bridge_boot.log')) { Get-Content ($dir + '\bridge_boot.log') -Tail 20 | Out-String -Width 200 } else { "boot log absent" }
"=== report file ==="
$r = Get-Item ($dir + '\bridge_report.jsonl') -ErrorAction SilentlyContinue
if ($r) { Write-Output ("report len=" + $r.Length + " mtime=" + $r.LastWriteTime) } else { "report missing" }
