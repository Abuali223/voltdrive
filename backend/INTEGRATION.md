# Brend API integratsiyasi — haqiqiy mashinaga ulanish

VoltDrive backendi **plug-and-play**: kod tayyor, faqat brendning rasmiy
credentials (API manzil + token) kerak. Credentials kelishi bilan adapter
avtomatik simulyatordan haqiqiy mashinaga o'tadi — **kod o'zgartirilmaydi**,
faqat env o'zgaruvchilari qo'shiladi.

## 1. Texnik tomon — qanday ulanadi (tayyor)

Har bir brend adapteri (`internal/provider/voyah`, `/deepal`, `/dongfeng`,
`/byd`) ikki rejimda ishlaydi:

- **Simulyator** (hozir) — credentials yo'q.
- **Live** — `*_API_URL` + `*_API_TOKEN` env berilsa, barcha buyruq va holat
  `internal/provider/oemrest` umumiy REST-klienti orqali haqiqiy bulutga ketadi.

Live rejimni yoqish (Cloud Run env):

```
VOYAH_API_URL    = https://api.voyah.com/fleet/v1
VOYAH_API_TOKEN  = <oem-bergan-token>
DEEPAL_API_URL   = ...
DEEPAL_API_TOKEN = ...
# dongfeng / byd ham xuddi shunday
```

`oemrest` kutadigan umumiy shartnoma (real spec bo'yicha moslab olinadi —
`oemrest.go` dagi yo'l va JSON nomlarini tahrirlash kifoya):

```
GET  {BASE}/vehicles/{id}/status      -> holat JSON (battery, lock, climate, location...)
POST {BASE}/vehicles/{id}/commands    -> {"command":"lock|unlock|engineStart|engineStop|climate|lights|trunk|horn|seat", ...}
Auth: Authorization: Bearer <token>
```

> Har bir ishlab chiqaruvchining API'si farq qiladi. Spec olingach,
> `oemrest.go` dagi `apiStatus` maydonlari va endpoint yo'llarini o'sha spec'ga
> moslang — adapter va ilova qismi tegilmaydi.

## 2. Biznes tomon — rasmiy API'ni qanday olish

Bu eng muhim qadam va u **huquqiy/biznes** jarayon (kod emas). Variantlar:

### A) Rasmiy distribyutor orqali (eng ishonchli yo'l O'zbekiston uchun)
- **Voyah / Dongfeng** — O'zbekistondagi rasmiy distribyutor (Dongfeng Motor
  vakili) bilan bog'laning. "Connected Vehicle / Telematics API" yoki "Fleet
  API" so'rang.
- **Deepal / Changan** — Changan O'zbekiston vakolatxonasi.
- **BYD** — BYD Uzbekistan (rasmiy diler) → "BYD Cloud / DiLink API".

So'raladigan narsalar:
1. **Fleet/Telematics API** ga kirish (B2B developer dasturi).
2. **OAuth client_id + client_secret** yoki fleet **API token**.
3. **API hujjati** (endpointlar, JSON sxemasi, rate-limitlar).
4. Har bir mashinani biriktirish: **VIN** + egasining roziligi (consent).

### B) To'g'ridan-to'g'ri ishlab chiqaruvchi developer portali
- Ko'p OEM'larda "Developer / Open Platform" bo'ladi (masalan BYD, Tesla'da bor).
- Ro'yxatdan o'tib, biznes-hujjatlar (kompaniya, maqsad) topshiriladi.
- Tasdiqdan keyin sandbox → production credentials beriladi.

### C) Standart protokollar (API berilmasa, muqobil)
- **Tesla** — rasmiy Fleet API mavjud (developer.tesla.com).
- Boshqalar uchun API berilmasa: **OBD-II + telematics qurilma** (masalan,
  4G OBD dongle) orqali ma'lumot olinadi. Bu sizning "internet orqali, qurilmasiz"
  talabingizga zid — shuning uchun rasmiy bulut API afzal.

### Amaliy maslahat
- Bitta brenddan boshlang (mijozlaringizda eng ko'pi qaysi bo'lsa).
- Avval **sandbox** credentials bilan sinang (bizda live path tayyor — test
  qilingan, `oemrest_test.go`).
- Consent (egasining roziligi) — huquqiy jihatdan majburiy: foydalanuvchi o'z
  mashinasini ilovaga ulashga ruxsat berishi kerak.

## 3. Tekshirish

Credentials qo'yilgach:

```bash
# Cloud Run env qo'shiladi, keyin:
curl -H "Authorization: Bearer <firebase-id-token>" \
     https://<service-url>/v1/vehicles/voyah-001
# -> haqiqiy mashina holati qaytadi (simulyator emas)
```

Loglarda `brand voyah: LIVE API at https://...` ko'rinadi.
