// VoltDrive core: dependency-free logic shared by the app (app-live.js) and the
// unit tests. No DOM, no Firebase, no globals beyond Web Crypto (available in
// browsers and in Node 19+), so it can be imported and tested in isolation.

// ---- PIN hashing (PBKDF2-SHA256 + per-PIN salt) ----

export const PIN_ITERATIONS = 150000;

const hexFromBuf = (buf) =>
  [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");

export async function sha256Hex(text) {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return hexFromBuf(buf);
}

export async function pbkdf2Hex(secret, saltBytes) {
  const key = await crypto.subtle.importKey("raw", new TextEncoder().encode(secret), "PBKDF2", false, ["deriveBits"]);
  const bits = await crypto.subtle.deriveBits(
    { name: "PBKDF2", salt: saltBytes, iterations: PIN_ITERATIONS, hash: "SHA-256" }, key, 256);
  return hexFromBuf(bits);
}

// pinSerialize returns the JSON to persist for a (uid, pin) pair.
export async function pinSerialize(uid, pin) {
  const salt = crypto.getRandomValues(new Uint8Array(16));
  return JSON.stringify({ v: 2, s: hexFromBuf(salt), h: await pbkdf2Hex(uid + ":" + pin, salt) });
}

// pinVerify checks a (uid, pin) against a stored value. Understands both the
// v2 salted PBKDF2 format and the legacy plain-SHA256 string.
export async function pinVerify(uid, pin, stored) {
  if (!stored) return false;
  try {
    const o = JSON.parse(stored);
    if (o && o.v === 2 && o.s && o.h) {
      const salt = Uint8Array.from(o.s.match(/.{2}/g).map((x) => parseInt(x, 16)));
      return (await pbkdf2Hex(uid + ":" + pin, salt)) === o.h;
    }
  } catch (e) { /* not JSON → legacy */ }
  return (await sha256Hex(uid + ":" + pin)) === stored; // legacy v1
}

// isLegacyPin reports whether a stored value uses the old (unsalted) format and
// should be upgraded after a successful verify.
export function isLegacyPin(stored) {
  if (!stored) return false;
  try { return JSON.parse(stored).v !== 2; } catch (e) { return true; }
}

// ---- Russian for transient toasts / auth status ----
//
// For RU users the inline `uz ? "…" : "…"` ternaries already resolve to the
// English string, so this single English→Russian table (plus a few Uzbek
// sources, "prefix + value" patterns and interpolated patterns) lets the app
// render Russian centrally, without per-call-site changes.

export const RU_MSG = {
  "Account created ✓": "Аккаунт создан ✓",
  "Approached — unlocked ✓": "Приближение — открыто ✓",
  "Biometric cancelled — enter PIN": "Биометрия отменена — введите PIN",
  "Biometric unlock disabled": "Биометрический вход отключён",
  "Biometric unlock enabled ✓": "Биометрический вход включён ✓",
  "Centered on location": "Центрировано по местоположению",
  "Charging stations…": "Зарядные станции…",
  "Cleared": "Очищено",
  "Climate updated": "Климат обновлён",
  "Code copied ✓": "Код скопирован ✓",
  "Couldn't enable biometrics": "Не удалось включить биометрию",
  "Disabled": "Отключено",
  "Email in use — switch to Sign in": "Email занят — перейдите ко входу",
  "Enter a valid email": "Введите корректный email",
  "Enter email": "Введите email",
  "Enter the code": "Введите код",
  "Enter your email first": "Сначала введите email",
  "Flashing lights & horn 📍": "Мигают фары и сигнал 📍",
  "Fuel stations…": "Заправки…",
  "No geolocation": "Нет геолокации",
  "No other vehicle": "Нет другого автомобиля",
  "No route": "Маршрут не найден",
  "Notification permission denied": "Уведомления запрещены",
  "Notifications enabled ✓": "Уведомления включены ✓",
  "PIN updated ✓": "PIN обновлён ✓",
  "POI error": "Ошибка POI",
  "Panic! Horn & lights on": "Тревога! Сигнал и фары включены",
  "Password min 6 chars": "Пароль минимум 6 символов",
  "Passwords don't match": "Пароли не совпадают",
  "Pick at least one permission": "Выберите хотя бы одно право",
  "Profile saved ✓": "Профиль сохранён ✓",
  "Push not supported here": "Push здесь не поддерживается",
  "Registering…": "Регистрация…",
  "Reset link sent to your email ✓": "Ссылка для сброса отправлена на email ✓",
  "Route error": "Ошибка маршрута",
  "Routing…": "Построение маршрута…",
  "Safe zone enabled ✓": "Безопасная зона включена ✓",
  "Sending…": "Отправка…",
  "Sign in first": "Сначала войдите",
  "Signed in — PIN...": "Вход выполнен — PIN...",
  "Signed in ✓": "Вход выполнен ✓",
  "Signing in with Gmail…": "Вход через Gmail…",
  "Signing in…": "Вход…",
  "This device has no biometrics": "На устройстве нет биометрии",
  "VAPID key not configured": "VAPID-ключ не настроен",
  "Walked away — locked ✓": "Удаление — заблокировано ✓",
  "Authentication not enabled (Console).": "Аутентификация не включена (Console).",
  "Car is nearby": "Автомобиль рядом",
  "Car is far": "Автомобиль далеко",
  "API offline": "API офлайн",
  "Range unknown": "Запас хода неизвестен",
  "Off": "Выкл",
  // Hardcoded Uzbek sources:
  "Chiqdingiz": "Вы вышли",
  "Ilova o'rnatildi ✓": "Приложение установлено ✓",
};

export const RU_PREFIX = {
  "Added: ": "Добавлено: ",
  "Removed: ": "Удалено: ",
  "Error: ": "Ошибка: ",
  "Failed: ": "Ошибка: ",
  "Internal error: ": "Внутренняя ошибка: ",
  "Location: ": "Местоположение: ",
  "Located ✓ ±": "Определено ✓ ±",
  "Chiqishda xato: ": "Ошибка выхода: ",
};

export const RU_PATTERN = [
  [/^✓ Reachable · ~(\d+) km left$/, "✓ Хватит · ~$1 км в запасе"],
  [/^⚠️ Tight · only ~(-?\d+) km spare$/, "⚠️ Впритык · всего ~$1 км запаса"],
  [/^✕ Won't reach · ~(\d+) km short$/, "✕ Не хватит · ~$1 км не достаёт"],
  [/^Saved · daily (\d{2}:\d{2})$/, "Сохранено · ежедневно в $1"],
  [/^Daily (\d{2}:\d{2})$/, "Ежедневно $1"],
];

// translate maps a UI string into the active language. Only "ru" transforms;
// "uz"/"en" return the input unchanged (those resolve at the call site).
export function translate(lang, msg) {
  if (lang !== "ru" || typeof msg !== "string") return msg;
  if (RU_MSG[msg]) return RU_MSG[msg];
  for (const p in RU_PREFIX) {
    if (msg.startsWith(p)) return RU_PREFIX[p] + msg.slice(p.length);
  }
  for (const [re, rep] of RU_PATTERN) {
    if (re.test(msg)) return msg.replace(re, rep);
  }
  return msg; // unknown → leave as-is
}
