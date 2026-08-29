Get-CimInstance Win32_Process -Filter "Name='python.exe'" | ForEach-Object {
  Write-Host ($_.ProcessId.ToString() + "  PPID=" + $_.ParentProcessId + "  " + $_.CommandLine)
}
