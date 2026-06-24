#!/usr/bin/env bash
#
# VoltDrive — to'liq deploy skripti (bitta buyruqda hammasi).
#
# Nima qiladi:
#   1. Kerakli Google Cloud API'larni yoqadi
#   2. Backend'ni Cloud Run'ga deploy qiladi (Firebase Auth + RTDB + FCM env bilan)
#   3. Cloud Run service account'ga kerakli ruxsatlarni beradi
#   4. Olingan URL'ni voltdrive.config.js (apiBase) ga yozadi
#   5. Firebase Hosting + Realtime Database qoidalarini deploy qiladi
#
# Ishga tushirish:
#   bash deploy.sh
#
# Talablar: gcloud va firebase CLI o'rnatilgan + login qilingan (skript tekshiradi).

set -euo pipefail

# ----------------------- SOZLAMALAR -----------------------
PROJECT_ID="eldi-79bf9"
REGION="europe-west1"                 # kerak bo'lsa o'zgartiring (masalan us-central1)
SERVICE="voltdrive-api"
RTDB_URL="https://eldi-79bf9-default-rtdb.firebaseio.com"
# ----------------------------------------------------------

bold() { printf "\033[1m%s\033[0m\n" "$1"; }
step() { printf "\n\033[1;33m▶ %s\033[0m\n" "$1"; }
ok()   { printf "\033[1;32m✓ %s\033[0m\n" "$1"; }
die()  { printf "\033[1;31m✗ %s\033[0m\n" "$1"; exit 1; }

cd "$(dirname "$0")"

# 0. Talablarni tekshirish
step "0/6 — Talablarni tekshirish"
command -v gcloud >/dev/null   || die "gcloud topilmadi. O'rnating: https://cloud.google.com/sdk/docs/install"
command -v firebase >/dev/null || die "firebase CLI topilmadi. O'rnating: npm install -g firebase-tools"
gcloud auth list --filter=status:ACTIVE --format="value(account)" | grep -q . \
  || die "gcloud login qiling: gcloud auth login"
ok "gcloud va firebase tayyor"

gcloud config set project "$PROJECT_ID" >/dev/null
ok "Loyiha: $PROJECT_ID"

# 1. API'larni yoqish
step "1/6 — Kerakli API'larni yoqish"
gcloud services enable \
  run.googleapis.com \
  cloudbuild.googleapis.com \
  firebasedatabase.googleapis.com \
  firestore.googleapis.com \
  fcm.googleapis.com \
  iam.googleapis.com \
  --quiet
ok "API'lar yoqildi"

# 2. Backend'ni Cloud Run'ga deploy
step "2/6 — Backend'ni Cloud Run'ga deploy qilish (bir necha daqiqa)"
gcloud run deploy "$SERVICE" \
  --source backend \
  --region "$REGION" \
  --platform managed \
  --allow-unauthenticated \
  --min-instances 1 \
  --port 8080 \
  --set-env-vars "FIREBASE_PROJECT_ID=${PROJECT_ID},RTDB_URL=${RTDB_URL},FCM_ENABLED=1" \
  --quiet
SERVICE_URL="$(gcloud run services describe "$SERVICE" --region "$REGION" --format='value(status.url)')"
[ -n "$SERVICE_URL" ] || die "Service URL olinmadi"
ok "Backend joylandi: $SERVICE_URL"

# 3. Cloud Run service account'ga ruxsatlar
step "3/6 — Service account'ga ruxsat berish (RTDB + Firestore + FCM)"
PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
RUNTIME_SA="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"
for ROLE in roles/datastore.user roles/firebase.admin; do
  gcloud projects add-iam-policy-binding "$PROJECT_ID" \
    --member="serviceAccount:${RUNTIME_SA}" \
    --role="$ROLE" --quiet >/dev/null || true
done
ok "Ruxsatlar berildi ($RUNTIME_SA)"

# 4. apiBase'ni configga yozish
step "4/6 — voltdrive.config.js ga apiBase yozish"
python3 - "$SERVICE_URL" <<'PY'
import re, sys
url = sys.argv[1]
p = "voltdrive.config.js"
s = open(p, encoding="utf-8").read()
s = re.sub(r'apiBase:\s*"[^"]*"', f'apiBase: "{url}"', s)
open(p, "w", encoding="utf-8").write(s)
print("apiBase =", url)
PY
ok "Config yangilandi"

# 5. Firebase Hosting + Database qoidalari
step "5/6 — Firebase Hosting + RTDB qoidalarini deploy qilish"
firebase use "$PROJECT_ID" >/dev/null 2>&1 || true
firebase deploy --only database,hosting --project "$PROJECT_ID"
ok "Hosting va qoidalar joylandi"

# 6. Yakun
step "6/6 — Tayyor!"
bold "Web ilova:  https://${PROJECT_ID}.web.app"
bold "Backend:    ${SERVICE_URL}"
cat <<EOF

Keyingi (bir martalik):
  • Firebase Console → Authentication → Email/Password va Google'ni yoqing
  • Realtime Database → Import JSON → rtdb-seed.example.json
    (REPLACE_WITH_USER_UID ni Authentication'dagi UID'ingizga almashtiring)

Test:
  • https://${PROJECT_ID}.web.app oching, ro'yxatdan o'ting
  • Mashina holati jonli ko'rinadi; Lock/Engine bosing — backend bajaradi
EOF
