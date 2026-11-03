@echo off
REM export_ledger.bat -- dump today's ledger to the Desktop before swapping/reinstalling the machine
REM (B-N9/B-N11: export first, then uninstall). Refuses to run when the sqlite integrity check fails.
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0export_ledger.ps1"
pause
