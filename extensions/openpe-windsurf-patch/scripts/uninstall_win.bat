@echo off
REM Double-click launcher for openpe-windsurf-patch uninstall on Windows.

setlocal enableextensions

set "SCRIPT_DIR=%~dp0"
set "SUBPROJECT_DIR=%SCRIPT_DIR%.."
pushd "%SUBPROJECT_DIR%" >nul
set "SUBPROJECT_DIR=%CD%"
popd >nul

echo ============================================================================
echo   openpe-windsurf-patch  .  uninstall (Windows)
echo ============================================================================
echo.

where python >nul 2>nul
if errorlevel 1 (
  echo X python not found in PATH.
  pause
  exit /b 1
)

cd /d "%SUBPROJECT_DIR%"
python -m installer uninstall %*
set UNINSTALL_RC=%ERRORLEVEL%

echo.
echo ============================================================================
if %UNINSTALL_RC% equ 0 (
  echo   + uninstall complete . restart Windsurf
) else (
  echo   X uninstall exited with code %UNINSTALL_RC%
)
echo ============================================================================
pause
exit /b %UNINSTALL_RC%
