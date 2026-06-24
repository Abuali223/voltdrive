# VoltDrive — Arxitektura

Zamonaviy elektromobil va gibrid mashinalar uchun masofadan boshqaruv tizimi.
Brendga bog'liq emas: har bir ishlab chiqaruvchi (Tesla, BYD, Voyah, ...)
adapter sifatida ulanadi. Tizim **bugun to'liq ishlaydi** (mock fleet bilan),
rasmiy API'lar kelganda kod o'zgarmasdan real mashinaga ulanadi.

```
┌──────────────┐   HTTPS/WSS    ┌─────────────────────────────┐   Brand API
│  Flutter app │ ─────────────► │  Go backend (Cloud Run)     │ ───────────►  Tesla / BYD /
│  (Android/iOS)│ ◄───────────── │  REST + WebSocket telemetry │ ◄───────────  Voyah cloud
└──────┬───────┘   live state   └──────┬───────────────┬──────┘
       │                                │               │
   Firebase Auth                   Firestore (RBAC)   FCM (alerts)
   (ID token)                      Secret Manager     Cloud Logging
```

## Komponentlar

| Qatlam | Texnologiya | Holat |
|--------|-------------|-------|
| Frontend | Flutter (`app/`) | ✅ kod yozilgan |
| Backend | Go, Cloud Run (`backend/`) | ✅ ishlaydi + test |
| Universal interfeys | `provider.VehicleProvider` | ✅ |
| Mock fleet | `provider/mock` | ✅ ishlaydi |
| Tesla adapter | `provider/tesla` (Fleet API) | ✅ kod, credential kerak |
| Auth (real JWT) | `auth.FirebaseVerifier` | ✅ |
| Ruxsatlar (RBAC) | `auth.FirestorePermissions` | ✅ |
| Real-time | `realtime` (WebSocket) | ✅ ishlaydi + test |
| Push/alarm | `notify` (FCM) | ✅ kod |

## Funksiyalar → kod

| Funksiya | Endpoint / kod |
|----------|----------------|
| 🔓 Eshik ochish/yopish | `POST /v1/vehicles/{id}/(un)lock` |
| 🚗 Masofadan start | `POST /v1/vehicles/{id}/start` |
| ❄️ Klimat | `POST /v1/vehicles/{id}/climate` |
| 📍 Joylashuv | snapshot `.location` (jonli stream) |
| 🔋 Batareya/yoqilg'i | snapshot `.energy` |
| 📈 Masofa/holat | snapshot `.health` |
| 🔔 Signalizatsiya | `realtime/security.go` → FCM |
| 👥 Ko'p foydalanuvchi | `auth` RBAC: owner/driver/guest |

## Ishga tushirish (lokal)

```bash
# Backend (mock fleet bilan to'liq ishlaydi)
cd backend && go run ./cmd/server     # :8080
go test ./...                          # testlar

# Flutter app (Android emulator -> 10.0.2.2)
cd app && flutter pub get && flutter run
```

## Production'ga o'tish (Google Cloud)

```bash
# 1. Backend -> Cloud Run
gcloud run deploy voltdrive-api --source backend \
  --region europe-west1 --min-instances 1 --allow-unauthenticated

# 2. Frontend (PWA) -> Firebase Hosting
firebase deploy --only hosting        # https://eldi-79bf9.web.app
```

`main.go` da dev sozlamalarni production'ga almashtiring:

```go
// auth.DevVerifier{}      -> auth.NewFirebaseVerifier("eldi-79bf9")
// auth.DevPermissions{}   -> auth.NewFirestorePermissions("eldi-79bf9", tokenSource)
// registry.Bind(vin, tesla.New(tesla.Config{...}))   // real brend
// hub.WithAlerter(notify.NewFCMAlerter(fcm, deviceResolver))
```

Sirlar (`ClientSecret`, service-account) — **Google Secret Manager**'dan
yuklanadi, hech qachon kodga yozilmaydi.

## Rasmiy API olish (real boshqaruv uchun)

1. **Tesla** — developer.tesla.com (ochiq, hujjatlangan) → adapter tayyor
2. **BYD** — Fleet API (B2B shartnoma)
3. **Voyah / Deepal** — O'zbekiston rasmiy diler / Xitoy HQ orqali

Har yangi brend = bitta yangi adapter fayl; app va API o'zgarmaydi.
