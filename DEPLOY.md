# VoltDrive — Deploy qo'llanma

Hammasi bitta skriptda: `deploy.sh`. Quyidagini terminalda ishga tushirasiz.

## 1. Asboblarni o'rnatish (bir martalik)

```bash
# Node + Firebase CLI
npm install -g firebase-tools

# Google Cloud CLI — https://cloud.google.com/sdk/docs/install
# (Linux/macOS uchun)
curl https://sdk.cloud.google.com | bash && exec -l $SHELL
```

## 2. Login (bir martalik)

```bash
gcloud auth login
gcloud auth application-default login
firebase login
```

## 3. Deploy (bitta buyruq)

```bash
bash deploy.sh
```

Skript avtomatik:
1. Kerakli Google Cloud API'larni yoqadi
2. Backend'ni Cloud Run'ga deploy qiladi
3. Service account'ga ruxsat beradi (RTDB + Firestore + FCM)
4. Cloud Run URL'ni `voltdrive.config.js` ga yozadi
5. Firebase Hosting + Realtime Database qoidalarini deploy qiladi

## 4. Console'da (bir martalik)

- **Authentication** → Email/Password va Google'ni yoqing
- **Realtime Database** → Import JSON → `rtdb-seed.example.json`
  (`REPLACE_WITH_USER_UID` ni Authentication'dagi UID'ingizga almashtiring)

## 5. Test

`https://eldi-79bf9.web.app` oching → ro'yxatdan o'ting → mashina holati jonli
ko'rinadi, Lock/Engine buyruqlari backend orqali bajariladi.

---

### Sozlamalar (deploy.sh boshida)

| O'zgaruvchi | Standart | Izoh |
|-------------|----------|------|
| `PROJECT_ID` | `eldi-79bf9` | Firebase loyiha |
| `REGION` | `europe-west1` | Cloud Run region |
| `SERVICE` | `voltdrive-api` | Cloud Run xizmat nomi |

### Xarajat (taxminiy)

- Cloud Run `min-instances=1`: ~$5-15/oy
- Realtime Database, Auth, Hosting: bepul kvota odatda yetarli
- Kerak bo'lsa `min-instances 0` qiling (arzonroq, lekin cold start ~1-2s)
