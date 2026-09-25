@echo off
cd /d "%~dp0"
taskkill /IM localserver.exe /F >nul 2>&1
exit /b 0
