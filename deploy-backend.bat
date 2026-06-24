@echo off
chcp 65001 >nul
cd /d "%~dp0"
echo.
echo ============================================
echo   VoltDrive - Backend (Cloud Run) deploy
echo ============================================
echo.
echo Bu qadam gcloud talab qiladi. Agar yo'q bo'lsa:
echo   https://cloud.google.com/sdk/docs/install
echo.

where gcloud >nul 2>nul
if errorlevel 1 (
  echo [!] gcloud topilmadi. Yuqoridagi havoladan o'rnating, keyin qayta bosing.
  echo.
  pause
  exit /b 1
)

set PROJECT_ID=eldi-79bf9
set REGION=europe-west1
set SERVICE=voltdrive-api
set RTDB_URL=https://eldi-79bf9-default-rtdb.firebaseio.com

echo Loyiha: %PROJECT_ID%  Region: %REGION%
echo.

call gcloud config set project %PROJECT_ID%
call gcloud services enable run.googleapis.com cloudbuild.googleapis.com firebasedatabase.googleapis.com firestore.googleapis.com fcm.googleapis.com iam.googleapis.com --quiet

echo.
echo Backend deploy (bir necha daqiqa)...
call gcloud run deploy %SERVICE% --source backend --region %REGION% --platform managed --allow-unauthenticated --min-instances 1 --port 8080 --set-env-vars "FIREBASE_PROJECT_ID=%PROJECT_ID%,RTDB_URL=%RTDB_URL%,FCM_ENABLED=1" --quiet

echo.
echo Service URL:
call gcloud run services describe %SERVICE% --region %REGION% --format="value(status.url)"

echo.
echo ============================================
echo   Backend tayyor.
echo   Endi URL'ni voltdrive.config.js -> apiBase ga yozing,
echo   keyin deploy-web.bat ni qayta bosing.
echo ============================================
echo.
pause
