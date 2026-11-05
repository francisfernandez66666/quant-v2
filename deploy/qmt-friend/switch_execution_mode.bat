@echo off
REM CUTOVER-1105 bridge/local cutover. Preview by default; -Apply commits.
REM Pure ASCII on purpose: this file is launched by double-click on a GBK box.
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0switch_execution_mode.ps1" %*
