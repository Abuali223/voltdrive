# VoltDrive — Play Market'ga joylash qo'llanmasi

Bu papka (`android/`) — VoltDrive PWA'sini **TWA (Trusted Web Activity)** orqali
Android ilovaga o'rab beradi. Ilova ichida xuddi web-sayt ishlaydi, lekin Play
Market'da to'liq ilova sifatida ko'rinadi, brauzer manzil qatorisiz (URL bar yo'q).

> Ilova `https://eldi-79bf9.web.app` ni ochadi. Sayt yangilansa — ilova ham
> avtomatik yangilanadi (qayta nashr qilish shart emas).

---

## 0. Bir martalik tayyorgarlik

1. **Google Play Console** akkaunti — bir martalik **$25** to'lov: https://play.google.com/console/signup
2. **Maxfiylik siyosati (Privacy Policy)** sahifasi kerak (majburiy). Sayingizda
   `/privacy.html` qiling yoki bepul generatordan foydalaning.
3. **Account deletion** (akkaunt o'chirish) — Play talab qiladi. Ilovada yoki
   sayt orqali foydalanuvchi o'z akkauntini o'chira olishi kerak.

---

## 1. AAB faylini yig'ish (build)

`.aab` (Android App Bundle) — Play Market'ga yuklanadigan fayl. Uchta yo'l bor.
Eng osoni — **A variant (GitHub Actions)**.

### A) GitHub Actions (tavsiya etiladi — kompyuterga hech narsa o'rnatilmaydi)

1. GitHub'da repo → **Settings → Secrets and variables → Actions** → quyidagilarni
   qo'shing (qiymatlar sizga alohida yuborilgan `keystore-credentials` faylida):
   - `ANDROID_KEYSTORE_BASE64` — `keystore.b64` fayl ichidagi matn
   - `ANDROID_KEYSTORE_PASSWORD` — store paroli
   - `ANDROID_KEY_PASSWORD` — key paroli (store bilan bir xil)
2. **Actions** tab → **Build Android Release (AAB)** → **Run workflow**.
3. Tugagach, **Artifacts** bo'limidan `voltdrive-android` ni yuklab oling —
   ichida `app-release.aab` (Play uchun) va `app-release.apk` (telefonда sinash uchun).

> Workflow fayli repoda `.github/android-release.workflow.yml` nomida turadi.
> CI'ni yoqish uchun uni GitHub web orqali `.github/workflows/android-release.yml`
> ga ko'chiring (push token'da `workflow` ruxsati yo'qligi sabab shu joyda saqlangan).

### B) Android Studio (grafik interfeys)

1. Android Studio'da `android/` papkasini oching (Gradle sync o'zi SDK'ni yuklaydi).
2. **Build → Generate Signed Bundle / APK → Android App Bundle**.
3. Keystore: `voltdrive-upload.keystore`, alias `voltdrive`, parollar — credentials faylda.
4. `release` → **Finish**. `.aab` `app/build/outputs/bundle/release/` da paydo bo'ladi.

### C) Buyruq qatori (terminal)

Kerak: JDK 17 + Android SDK. `voltdrive-upload.keystore` ni `android/` ichiga qo'ying, so'ng:

```bash
cd android
export ANDROID_KEYSTORE_PASSWORD='...'   # credentials faylдан
export ANDROID_KEY_PASSWORD='...'
./gradlew bundleRelease          # -> app/build/outputs/bundle/release/app-release.aab
./gradlew assembleRelease        # -> app/build/outputs/apk/release/app-release.apk (sinash uchun)
```

---

## 2. Play Console'da nashr qilish

1. **Create app** → nom "VoltDrive", til, ilova turi (App), bepul/pullik.
2. **App bundle** (`app-release.aab`) ni **Internal testing** ga yuklang (avval o'zingiz sinang).
3. **Store listing**: nom, qisqa/to'liq tavsif, ikonka (512×512), feature graphic
   (1024×500), kamida 2 ta screenshot. Maxfiylik siyosati havolasi.
4. **Data safety** formasi va **Content rating** anketasini to'ldiring.
5. Tayyor bo'lsa **Production** ga chiqaring.

---

## 3. ⚠️ MUHIM: Play App Signing barmoq izi (URL bar yo'qolishi uchun)

Play yangi ilovalar uchun **Play App Signing** ni avtomatik yoqadi: siz yuklagan
AAB *upload key* bilan imzolangan, lekin foydalanuvchi telefoniga o'rnatiladigan
ilovani **Google o'z kaliti bilan qayta imzolaydi**.

Shu sababli URL bar yashirilishi uchun saytdagi `assetlinks.json` da **Google'ning
barmoq izi** bo'lishi shart (hozir faqat upload key turibdi):

1. Play Console → ilova → **Setup → App integrity → App signing**.
2. **App signing key certificate → SHA-256** ni nusxalang.
3. `/.well-known/assetlinks.json` dagi `sha256_cert_fingerprints` ro'yxatiga
   **qo'shing** (eskisini o'chirmang — ikkalasi ham tursin):

```json
"sha256_cert_fingerprints": [
  "D3:23:16:...(upload key)",
  "AA:BB:CC:...(Play App Signing key — Play Console'dan)"
]
```

4. Saytni qayta nashr qiling. Bir necha daqiqada Chrome qайta tekshiradi.

> Tekshirish: https://developers.google.com/digital-asset-links/tools/generator

---

## 4. Yangilanish chiqarish

Saytdagi o'zgarishlar avtomatik ko'rinadi — ilovani qayta yig'ish SHART EMAS.
Faqat ilovaning **o'zini** (ikonka, ruxsatlar, target SDK) o'zgartirsangiz:

1. `app/build.gradle` da `versionCode` ni +1 (masalan 1 → 2), `versionName` ni yangilang.
2. Yangi `.aab` yig'ing va Play Console'ga yuklang.

---

## Texnik ma'lumot

| | |
|---|---|
| Package (applicationId) | `uz.voltdrive.app` — **birinchi yuklamadan keyin o'zgarmaydi** |
| Launch URL | `https://eldi-79bf9.web.app/index.html` |
| minSdk / targetSdk | 21 / 35 |
| TWA kutubxonasi | `com.google.androidbrowserhelper:2.6.0` |
| Imzo (upload) SHA-256 | `D3:23:16:20:B4:52:FB:00:0E:F5:0D:C7:1F:F7:44:A4:14:DA:D4:12:42:A7:4A:E8:13:50:5E:80:26:A1:23:AC` |

**Keystore'ni yo'qotmang.** U bilan har bir yangilanish imzolanadi. Offline,
xavfsiz joyda saqlang (parol bilan birga).
