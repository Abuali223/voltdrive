# Voyah (va boshqa brend) ilova-API'sini aniqlash — qo'llanma

Maqsad: Voyah rasmiy ilovasi ↔ bulut o'rtasidagi trafikni yozib olib, eshik/iqlim/holat
buyruqlari qaysi endpointga ketishini aniqlash. Keyin shu endpointlarni
`internal/provider/oemrest` adapteriga moslab, dasturimiz to'liq boshqarishni oladi.

> **Kerak:** Voyah mashinasi + telefonda rasmiy ilova + akkaunt (login). Mashina yoningizda bo'lishi shart (real buyruq yuborish uchun).

---

## 1. Asboblardan birini tanlang (telefon trafigini ko'rish)

| Asbob | Platforma | Izoh |
|---|---|---|
| **HTTP Toolkit** | Android/PC | Eng oson, bepul, sertifikat avto |
| **mitmproxy** | PC + telefon Wi-Fi | Bepul, kuchli, terminal |
| **Proxyman** | iOS/Mac | iPhone uchun qulay |

## 2. Sozlash (mitmproxy misolida)

1. PC'da: `pip install mitmproxy` → `mitmweb` (brauzerda ochiladi).
2. Telefon va PC bir Wi-Fi'da. Telefon Wi-Fi → Proxy → Manual → PC IP, port `8080`.
3. Telefon brauzerida `mitm.it` ochib, **sertifikatni o'rnating** (HTTPS'ni ochish uchun).
   - Android: Settings → Security → Install certificate → CA.
4. mitmweb'da trafik oqib kela boshlaydi.

## 3. Trafikni yozib olish (eng muhim qism)

Voyah ilovasini ochib, **har bir amalni alohida** bajaring (orasida 5–10s kuting,
qaysi so'rov qaysi amalga tegishli ekanini ajratish oson bo'lsin):

1. **Login** (akkauntga kirish) — token qanday olinishini ko'ramiz.
2. **Holatni yangilash** (mashina holati ekrani) — `status`/`vehicle` endpoint.
3. **Eshikni ochish** → keyin **yopish**.
4. **Iqlim/precondition yoqish** → o'chirish.
5. (Bo'lsa) **zaryad**, **signal**, **bagaj**.

Har amaldan keyin mitmweb'da yangi paydo bo'lgan so'rovni belgilang.

## 4. Menga yuborADIGAN ma'lumot (har so'rov uchun)

- **URL** (to'liq, masalan `https://api.voyah.../v1/...`)
- **Method** (GET/POST)
- **Headers** (ayniqsa `Authorization`, `token`, `appId`, imzo header'lari)
- **Request body** (JSON)
- **Response body** (JSON)

> ⚠️ **Maxfiylik:** login parol va shaxsiy token'larni olib tashlang yoki
> `XXX` bilan almashtiring — menga faqat **struktura** kerak (maydon nomlari, format).

## 5. Mumkin bo'lgan to'siqlar

- **Certificate pinning** — ilova soxta sertifikatni rad etsa, trafik ko'rinmaydi.
  Yechim: `Frida` + objection (ildizlangan telefon yoki emulyator) bilan pinning'ni
  o'chirish. Buni keyin birga ko'ramiz.
- **Imzo (signature) header'lari** — ba'zi API'lar har so'rovga HMAC imzo qo'yadi.
  Bu holda imzo algoritmini ham aniqlashimiz kerak (ilovani decompile qilish).

## 6. Keyin nima bo'ladi

Siz yuborgan so'rovlar asosida men:
1. `oemrest.go` dagi endpoint yo'llari + JSON maydonlarini Voyah formatiga moslayman.
2. Login/token oqimini `TokenSource` ga yozaman.
3. Cloud Run'ga env qo'shaman → Voyah adapteri jonli ulanadi.

Natija: ilova nima qila olsa — dasturimiz ham qiladi, **qurilmasiz**.
