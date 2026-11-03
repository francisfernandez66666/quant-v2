@echo off
REM install_entry.bat -- one-click entry for the friend execution leg (FRIEND-PKG, 2026-11-03).
REM ASCII-only on purpose: cmd decodes .bat in the OEM codepage (936 here), so any Chinese in this
REM file arrives as '?' -- human text lives in SETUP.txt and in install.ps1's own output instead.
REM -File (not -Command): a command line typed here would be echoed into the process list, while
REM -File keeps the parameter surface out of the transcript. Gate 171 asserts this shape.
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" -Apply
echo.
echo If the last line says LEG_INSTALL=ABORT, open SETUP.txt, section TROUBLESHOOTING.
pause
