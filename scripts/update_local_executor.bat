@echo off
REM update_local_executor.bat -- entry point for rolling the local executor onto this box.
REM ASCII-only on purpose: cmd decodes .bat in the OEM codepage (936 here), so any Chinese in
REM this file arrives as '?' -- human text lives in SETUP.txt and in the ps1's own output.
REM -File (not -Command): a command line typed here would be echoed into the process list, while
REM -File keeps the parameter surface out of the transcript.
REM Dry run by default (the ps1 prints the plan and touches nothing); pass -Apply to act.
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0update_local_executor.ps1" %*
echo.
echo If the last line says UPDATE_RESULT=ABORT, open SETUP.txt, section TROUBLESHOOTING.
pause
