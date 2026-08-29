$hdr = @{ Authorization = 'Bearer REPLACE_WITH_QMT_REPORT_TOKEN' }
$body = '{"type":"heartbeat","account":"2069008957","ts":0}'
try {
  $r = Invoke-WebRequest -Uri http://127.0.0.1:8080/api/qmt/report -Method POST -ContentType 'application/json' -Headers $hdr -Body $body -UseBasicParsing
  Write-Host ("REPORT AUTH STATUS: " + $r.StatusCode)
} catch {
  $sc = $_.Exception.Response.StatusCode.Value__
  Write-Host ("REPORT AUTH STATUS: " + $sc + " body=" + $_.ErrorDetails.Message)
}
