@echo off
REM Double-click launcher for the openpe-windsurf-patch installer on Windows.
REM Validates Python + npm, ensures the inject payload is built, then
REM re-invokes itself elevated via PowerShell when the target Windsurf
REM install needs admin rights.

setlocal enableextensions enabledelayedexpansion

set "SCRIPT_DIR=%~dp0"
set "SUBPROJECT_DIR=%SCRIPT_DIR%.."
pushd "%SUBPROJECT_DIR%" >nul
set "SUBPROJECT_DIR=%CD%"
popd >nul

echo ============================================================================
echo   openpe-windsurf-patch  .  install (Windows)
echo ============================================================================
echo subproject: %SUBPROJECT_DIR%
echo.

where python >nul 2>nul
if errorlevel 1 (
  echo X python not found in PATH.
  echo   Install Python 3.8+ from https://www.python.org/downloads/ then retry.
  pause
  exit /b 1
)
for /f "delims=" %%V in ('python --version 2^>^&1') do echo + %%V

set "INJECT_PAYLOAD=%SUBPROJECT_DIR%\inject\dist\inject.js"
if not exist "%INJECT_PAYLOAD%" (
  echo   ! inject payload missing at %INJECT_PAYLOAD%
  where npm >nul 2>nul
  if errorlevel 1 (
    echo X npm not found; install Node.js 18+ from https://nodejs.org/ then retry.
    pause
    exit /b 1
  )
  pushd "%SUBPROJECT_DIR%\inject" >nul
  call npm install --no-audit --no-fund
  if errorlevel 1 ( popd >nul & echo X npm install failed & pause & exit /b 1 )
  call npm run build
  if errorlevel 1 ( popd >nul & echo X npm run build failed & pause & exit /b 1 )
  popd >nul
)

echo.
echo ^>^> Running installer (may prompt for UAC elevation)
echo.

cd /d "%SUBPROJECT_DIR%"
python -m installer install %*
set INSTALL_RC=%ERRORLEVEL%

echo.
echo ============================================================================
if %INSTALL_RC% equ 0 (
  echo   + install complete . restart Windsurf to load the openPE logo button
) else (
  echo   X install exited with code %INSTALL_RC%
)
echo ============================================================================
pause
exit /b %INSTALL_RC%
