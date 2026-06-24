@echo off
chcp 65001 >nul
cd /d "%~dp0"
echo.
echo ============================================
echo   VoltDrive - Web (PWA) deploy
echo ============================================
echo.

where firebase >nul 2>nul
if errorlevel 1 (
  echo [!] firebase topilmadi. Avval o'rnating:
  echo     npm install -g firebase-tools
  echo.
  pause
  exit /b 1
)

echo Deploy boshlandi... (eldi-79bf9)
echo.
call firebase deploy --only hosting --project eldi-79bf9

echo.
echo ============================================
echo   Tugadi. Manzil: https://eldi-79bf9.web.app
echo ============================================
echo.
pause
