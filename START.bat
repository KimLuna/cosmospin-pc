@echo off
cd /d "%~dp0"
title tripleS Cosmo Tool - Transfer + SPIN

set "EXPECTED_BUILD=v4-spin-20260925-5"
set "NEED_BUILD=0"

if not exist ".runtime\localserver.exe" set "NEED_BUILD=1"
if not exist ".runtime\build-version.txt" set "NEED_BUILD=1"

if "%NEED_BUILD%"=="0" (
  set /p BUILT=<".runtime\build-version.txt"
  if /I not "%BUILT%"=="%EXPECTED_BUILD%" set "NEED_BUILD=1"
)

if "%NEED_BUILD%"=="0" (
  start "tripleS Cosmo Tool" ".runtime\localserver.exe"
  exit /b 0
)

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\first-run-windows.ps1"
