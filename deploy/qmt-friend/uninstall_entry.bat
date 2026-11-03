@echo off
REM uninstall_entry.bat -- take the leg down, keep the ledger (FRIEND-PKG batch 6).
REM Two-stage on purpose: first run shows the plan (dry-run, nothing is touched), only an explicit
REM Y hands it to -Apply. Default keeps data.db and logs -- they are the audit trail (F18, B-N9).
cd /d "%~dp0"
echo == preview (nothing is changed yet) ==
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0uninstall.ps1"
echo.
set /p GO=Type YES to uninstall this leg (ledger is kept): 
if /I not "%GO%"=="YES" goto skip
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0uninstall.ps1" -Apply
:skip
pause
