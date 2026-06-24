// VoltDrive — live integration layer.
//
// Non-invasive: the existing demo in index.html keeps working as-is. When
// window.VOLTDRIVE_CONFIG.firebase is present, this module upgrades the app to
// real Firebase Auth + live Realtime Database telemetry + backend commands.
// Every external call is guarded so the UI never breaks if the backend or
// network is unavailable — it simply falls back to the local demo behaviour.
import { initializeApp } from "https://www.gstatic.com/firebasejs/10.13.0/firebase-app.js";
import {
  getAuth,
  setPersistence,
  browserLocalPersistence,
  signInWithEmailAndPassword,
  createUserWithEmailAndPassword,
  GoogleAuthProvider,
  signInWithPopup,
  signInWithRedirect,
  getRedirectResult,
  onAuthStateChanged,
  signOut,
  updateProfile,
} from "https://www.gstatic.com/firebasejs/10.13.0/firebase-auth.js";
import {
  getDatabase,
  ref,
  onValue,
} from "https://www.gstatic.com/firebasejs/10.13.0/firebase-database.js";
import {
  getMessaging,
  getToken,
  onMessage,
} from "https://www.gstatic.com/firebasejs/10.13.0/firebase-messaging.js";

const CFG = window.VOLTDRIVE_CONFIG || {};
// Map the UI car ids to backend / RTDB vehicle ids.
const VMAP = { voyah: "voyah-001", deepal: "deepal-002", dongfeng: "dongfeng-003", byd: "byd-004" };

// Current language helper.
const uzNow = () => window.App?.lang === "uz";

// All mutable module state is declared up here, BEFORE boot() runs, to avoid
// temporal-dead-zone ReferenceErrors (boot() and its callees touch these).
let auth, db, unsubscribe = null;
let signOutWired = false;
let selectCarHooked = false;
let lockState = null;
let pollTimer = null;
let climateTemp = 22;
let climateOn = false;
let climateSendT = null;
let seatSendT = null;
let seatSel = "driver";
let seatVals = { driver: 62, passenger: 58, rear: 70 };
let seatSpin = 0;
let lastLoc = null;
let leafletMap = null;
let carMarker = null;
let deviceLoc = null;
let destMarker = null;
let routeLine = null;
let poiMarkers = [];

if (!CFG.firebase) {
  console.info("VoltDrive: live mode OFF (no config) — running in demo mode.");
} else {
  boot();
}

// App-shell features that work regardless of Firebase config.
setupPullToRefresh();
setupInstallPrompt();
wireExtras();

function boot() {
  try {
    const app = initializeApp(CFG.firebase);
    auth = getAuth(app);
    db = getDatabase(app);
    // Keep the session on this device so reopening doesn't ask for email again.
    setPersistence(auth, browserLocalPersistence).catch((e) => console.warn("persistence:", e?.code));
    wireAuthScreen();
    wireControls();
    wireSignOut();
    // Complete a Google redirect sign-in if we're returning from one.
    getRedirectResult(auth).catch((e) => console.warn("redirect:", e?.code));
    onAuthStateChanged(auth, async (user) => {
      try {
        if (user) {
          authStatus(uzNow() ? "Kirildi — PIN..." : "Signed in — PIN...");
          showUser(user);
          try { subscribe(); } catch (e) { console.warn("subscribe:", e); }
          // Security gate: ask for a PIN before entering (set up first time).
          await lockGate(user.uid);
          // Winter convenience: offer a one-tap warm-up before entering.
          await warmupPrompt();
          if (window.App) App.go("home");
        } else {
          hideUser();
          hideLock();
        }
      } catch (e) {
        console.error("authState error:", e);
        authStatus((uzNow() ? "Ichki xato: " : "Internal error: ") + (e?.message || e), true);
        // Don't trap the user on the lock screen if PIN UI failed: go home.
        if (user && window.App) { hideLock(); App.go("home"); }
      }
    });
    console.info("VoltDrive: live mode ON.");
  } catch (e) {
    console.warn("VoltDrive live init failed, demo mode:", e);
  }
}

// --- Authentication ---

// Standard auth flow: a single screen with two modes — Sign in (default) and
// Sign up (with password confirmation). After a successful sign-up/sign-in,
// onAuthStateChanged drives the PIN lock (set up on first run).
function wireAuthScreen() {
  const emailEl = document.getElementById("email-input");
  const pwEl = document.getElementById("pw-input");
  const pw2El = document.getElementById("pw2-input");
  const pw2Field = document.getElementById("pw2-field");
  const submitBtn = document.getElementById("auth-submit");
  const submitLabel = document.getElementById("auth-submit-label");
  const googleBtn = document.getElementById("google-btn");
  const toggle = document.getElementById("auth-toggle");
  const toggleText = document.getElementById("auth-toggle-text");
  const titleEl = document.getElementById("auth-title");
  const subEl = document.getElementById("auth-sub");

  let mode = "signin";
  const uz = () => window.App?.lang === "uz";

  const T = {
    signin: {
      en: { title: "Sign in", sub: "Welcome back — control your vehicles", btn: "Sign in", tgt: "Don't have an account?", tog: "Sign up" },
      uz: { title: "Kirish", sub: "Xush kelibsiz — mashinangizni boshqaring", btn: "Kirish", tgt: "Hisobingiz yo'qmi?", tog: "Ro'yxatdan o'tish" },
    },
    signup: {
      en: { title: "Create account", sub: "Register to control your vehicles", btn: "Create account", tgt: "Already have an account?", tog: "Sign in" },
      uz: { title: "Ro'yxatdan o'tish", sub: "Mashinangizni boshqarish uchun ro'yxatdan o'ting", btn: "Ro'yxatdan o'tish", tgt: "Hisobingiz bormi?", tog: "Kirish" },
    },
  };

  function setMode(m) {
    mode = m;
    const t = T[m][uz() ? "uz" : "en"];
    if (titleEl) titleEl.textContent = t.title;
    if (subEl) subEl.textContent = t.sub;
    if (submitLabel) submitLabel.textContent = t.btn;
    if (toggleText) toggleText.textContent = t.tgt;
    if (toggle) toggle.textContent = t.tog;
    if (pw2Field) pw2Field.style.display = m === "signup" ? "flex" : "none";
    if (pw2El) pw2El.value = "";
    authStatus("");
  }

  const explain = (e) => {
    const c = String(e?.code || e?.message || "");
    if (c.includes("operation-not-allowed") || c.includes("configuration-not-found"))
      return uz() ? "Authentication yoqilmagan (Console)." : "Authentication not enabled (Console).";
    if (c.includes("invalid-email")) return uz() ? "Email noto'g'ri." : "Invalid email.";
    if (c.includes("weak-password")) return uz() ? "Parol kamida 6 belgi." : "Password min 6 chars.";
    if (c.includes("wrong-password") || c.includes("invalid-credential"))
      return uz() ? "Email yoki parol noto'g'ri. Hisobingiz bo'lmasa, ro'yxatdan o'ting." : "Wrong email or password.";
    if (c.includes("network")) return uz() ? "Internet ulanishi yo'q." : "No internet.";
    return (uz() ? "Xatolik: " : "Error: ") + c;
  };

  // Toggle between Sign in and Sign up.
  if (toggle) {
    toggle.style.cursor = "pointer";
    toggle.addEventListener("click", () => setMode(mode === "signin" ? "signup" : "signin"));
  }

  async function submit() {
    const email = (emailEl?.value || "").trim();
    const pw = pwEl?.value || "";
    const pw2 = pw2El?.value || "";
    if (!email) return authStatus(uz() ? "Email kiriting" : "Enter email", true);
    if (pw.length < 6) return authStatus(uz() ? "Parol kamida 6 belgi bo'lsin" : "Password min 6 chars", true);

    if (mode === "signup") {
      if (pw !== pw2) return authStatus(uz() ? "Parollar mos kelmadi" : "Passwords don't match", true);
      authStatus(uz() ? "Ro'yxatdan o'tilmoqda…" : "Registering…");
      try {
        await createUserWithEmailAndPassword(auth, email, pw);
        authStatus(uz() ? "Hisob yaratildi ✓" : "Account created ✓");
      } catch (e) {
        if (String(e?.code || "").includes("email-already-in-use")) {
          authStatus(uz() ? "Bu email band — 'Kirish' ga o'ting" : "Email in use — switch to Sign in", true);
          setMode("signin");
        } else authStatus(explain(e), true);
      }
    } else {
      authStatus(uz() ? "Kirilmoqda…" : "Signing in…");
      try {
        await signInWithEmailAndPassword(auth, email, pw);
        authStatus(uz() ? "Kirildi ✓" : "Signed in ✓");
      } catch (e) {
        authStatus(explain(e), true);
      }
    }
  }

  if (submitBtn) {
    submitBtn.removeAttribute("onclick");
    submitBtn.addEventListener("click", submit);
  }
  // Enter key submits.
  [pwEl, pw2El, emailEl].forEach((el) =>
    el?.addEventListener("keydown", (e) => { if (e.key === "Enter") submit(); })
  );

  if (googleBtn) {
    googleBtn.removeAttribute("onclick");
    googleBtn.addEventListener("click", async () => {
      authStatus(uz() ? "Gmail bilan kirilmoqda…" : "Signing in with Gmail…");
      try {
        await signInWithPopup(auth, new GoogleAuthProvider());
      } catch (e) {
        const c = String(e?.code || "");
        if (c.includes("popup-blocked") || c.includes("popup-closed") || c.includes("cancelled-popup")) {
          try { await signInWithRedirect(auth, new GoogleAuthProvider()); return; } catch (_) {}
        }
        authStatus(explain(e), true);
      }
    });
  }

  setMode("signin"); // default: Sign in
}

// authStatus shows a visible status line under the auth form so the user can
// see exactly what is happening (idle / signing in / error / signed in).
function authStatus(msg, isError) {
  let el = document.getElementById("auth-status");
  if (!el) {
    const host = document.querySelector("#s-auth [data-i18n='auth.have']")?.closest("div");
    el = document.createElement("div");
    el.id = "auth-status";
    el.style.cssText = "text-align:center;font-size:12px;font-weight:600;margin-top:14px;min-height:16px;";
    (host?.parentElement || document.querySelector("#s-auth > div"))?.appendChild(el);
  }
  if (el) {
    el.textContent = msg || "";
    el.style.color = isError ? "#ff7a7a" : "#7fd28a";
  }
}

// showUser fills the profile with the signed-in account and reveals sign-out.
function showUser(user) {
  const email = user.email || "";
  const name = user.displayName || email.split("@")[0] || "Foydalanuvchi";
  set("profile-name", name);
  set("profile-email", email);
  set("family-self", name); // current user in the family list
  const av = document.getElementById("profile-av");
  if (av) av.textContent = (name[0] || "U").toUpperCase() + (name.split(/[ .]/)[1]?.[0] || "").toUpperCase();
  const row = document.getElementById("signout-row");
  if (row) row.style.display = "flex";
}

function hideUser() {
  const row = document.getElementById("signout-row");
  if (row) row.style.display = "none";
}

// wireSignOut binds the profile "Sign out" row to Firebase signOut.
function wireSignOut() {
  if (signOutWired) return;
  const row = document.getElementById("signout-row");
  if (!row) return;
  signOutWired = true;
  row.addEventListener("click", async () => {
    try {
      if (unsubscribe) { unsubscribe(); unsubscribe = null; }
      await signOut(auth);
      toast("Chiqdingiz");
      if (window.App) App.go("auth");
    } catch (e) {
      toast("Chiqishda xato: " + (e.code || e.message));
    }
  });
}

// --- Live telemetry ---
//
// Primary source: REST polling of the backend (works everywhere, no extra
// setup). Secondary: Realtime Database push (used if security rules grant the
// user read access). Both feed applyLive().

function subscribe() {
  if (!window.App) return;
  // REST polling — the reliable live source.
  startPolling();
  // RTDB push — best effort (silently ignored if rules deny).
  try {
    if (db) {
      if (unsubscribe) unsubscribe();
      const vid = VMAP[App.activeCar?.id] || "voyah-001";
      unsubscribe = onValue(ref(db, `vehicles/${vid}`), (snap) => {
        const v = snap.val();
        if (v) applyLive(v);
      });
    }
  } catch (e) {
    console.warn("RTDB subscribe skipped:", e?.code);
  }
  hookSelectCar();
}

// startPolling fetches the active vehicle's state from the backend every few
// seconds so the UI shows real, live data (battery, range, lock, engine, ...).
function startPolling() {
  if (pollTimer) clearInterval(pollTimer);
  fetchState();
  pollTimer = setInterval(fetchState, 4000);
}

async function fetchState() {
  if (!CFG.apiBase || !auth?.currentUser || !window.App) return;
  try {
    const vid = VMAP[App.activeCar?.id] || "voyah-001";
    const token = await auth.currentUser.getIdToken();
    const res = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (res.ok) applyLive(await res.json());
  } catch (e) {
    /* offline / transient — keep last values */
  }
}

function hookSelectCar() {
  if (selectCarHooked || !window.App || !App.selectCar) return;
  selectCarHooked = true;
  const orig = App.selectCar.bind(App);
  App.selectCar = (...args) => {
    orig(...args);
    subscribe();
  };
}

function applyLive(v) {
  if (!window.App) return;
  const uz = App.lang === "uz";

  // --- Lock state ---
  if (typeof v.lock === "string") {
    App.locked = v.lock === "locked";
    App._syncLockUI && App._syncLockUI();
  }

  // --- Engine state ---
  if (typeof v.engineOn === "boolean") {
    if (App.engineOn !== v.engineOn) {
      App.engineOn = v.engineOn;
      setEngineUI(v.engineOn);
    }
    set("cs-engine-state", v.engineOn ? (uz ? "Yoniq" : "Running") : (uz ? "O‘chiq" : "Off"), true);
  }

  // --- Lights + trunk (auxiliary state) ---
  if (typeof v.lightsOn === "boolean") {
    App.lightsOn = v.lightsOn;
    const lb = document.getElementById("lights-btn");
    if (lb) lb.classList.toggle("lkd", v.lightsOn);
  }
  if (typeof v.trunkOpen === "boolean") {
    App.trunkOpen = v.trunkOpen;
    const tb = document.getElementById("trunk-btn");
    if (tb) tb.classList.toggle("lkd", v.trunkOpen);
  }

  // --- Energy: range + battery ---
  const e = v.energy || {};
  const hasRange = typeof e.rangeKm === "number";
  const hasBatt = typeof e.batteryLevel === "number";

  if (hasRange) {
    set("home-range", `${e.rangeKm} km` + (uz ? " masofa" : " range"), true);
    setHTML("engine-range", `${e.rangeKm}<span style="font-size:18px;color:#FF7A2E;margin-left:3px">km</span>`);
    set("cs-range", String(e.rangeKm));
    set("cs-engine-range2", String(e.rangeKm)); // Engine tab on the Car-status screen
  }
  if (hasBatt) {
    setHTML("cs-battery", `${e.batteryLevel}<span style="font-size:12px;color:#7e8086">%</span>`);
  }
  if (hasRange && hasBatt) {
    set("cs-rangepct", `km · ${e.batteryLevel}%`);
  }

  // --- Health: tyre pressure + odometer ---
  const h = v.health || {};
  if (Array.isArray(h.tirePressures) && h.tirePressures.length) {
    const kpa = h.tirePressures[0];
    const psi = Math.round(kpa * 0.145);
    set("cs-tyre", String(psi));
  }
  if (typeof h.odometerKm === "number") {
    const km = h.odometerKm.toLocaleString("en-US");
    set("cs-odo", (uz ? "Umumiy masofa · " : "Total distance · ") + km + " km", true);
  }

  // --- Climate (interior temperature + control state) ---
  const c = v.climate || {};
  if (typeof c.insideC === "number") {
    const t = Math.round(c.insideC);
    set("home-climate-sub", (uz ? "Salon " : "Interior ") + t + "°C", true);
    set("cs-cl-inside", String(t)); // Climate tab on the Car-status screen
  }
  if (typeof c.targetC === "number" && c.targetC > 0) {
    set("cs-cl-target", String(Math.round(c.targetC)));
  }
  if (typeof c.on === "boolean") {
    climateOn = c.on;
    if (typeof c.targetC === "number" && c.targetC > 0) climateTemp = Math.round(c.targetC);
    updateClimateUI();
  }

  // --- Location ---
  const loc = v.location || {};
  if (typeof loc.lat === "number" && typeof loc.lng === "number" && (loc.lat || loc.lng)) {
    lastLoc = { lat: loc.lat, lng: loc.lng };
    const shown = deviceLoc || lastLoc; // prefer the phone's real location
    const coords = `${shown.lat.toFixed(4)}, ${shown.lng.toFixed(4)}`;
    set("home-loc", coords);
    set("map-addr", coords);
    set("map-name", (v.name || "VoltDrive") + (uz ? " — jonli" : " — live"));
    updateMapMarker();
  }

  // --- Personalize engine greeting with the signed-in user ---
  if (auth?.currentUser) {
    const who = (auth.currentUser.displayName || auth.currentUser.email || "").split("@")[0];
    if (who) set("engine-hello", (uz ? "Salom " : "Hello ") + who + (uz ? ", xush kelibsiz" : ", welcome back"), true);
  }
}

function setEngineUI(on) {
  const btn = document.getElementById("power-btn");
  if (btn) {
    btn.classList.toggle("on", on);
    btn.classList.toggle("off", !on);
  }
  const status = document.getElementById("engine-status");
  if (status) {
    status.textContent = on
      ? (App.lang === "uz" ? "Dvigatel ishlayapti" : "Engine is running")
      : (App.lang === "uz" ? "Dvigatelni yoqish/o'chirish" : "Tap to start & stop engine");
    status.removeAttribute("data-i18n");
  }
}

// --- Commands (backend) ---

function wireControls() {
  if (!window.App) {
    document.addEventListener("DOMContentLoaded", wireControls, { once: true });
    return;
  }
  // Lock: optimistic local flip, then authoritative backend command.
  const origLock = App.toggleLock.bind(App);
  const doLock = () => {
    origLock();
    sendCommand(App.locked ? "lock" : "unlock");
  };
  App.toggleLock = doLock;
  App.toggleLockCtrl = () => {
    App.locked = !App.locked;
    App._syncLockUI();
    sendCommand(App.locked ? "lock" : "unlock");
  };

  // Engine.
  const origEngine = App.toggleEngine.bind(App);
  App.toggleEngine = () => {
    origEngine();
    sendCommand(App.engineOn ? "start" : "stop");
  };

  // Security & PIN (profile row) → change the 4-digit PIN.
  App.changePin = changePin;
  // Family sharing → invite a member by email.
  App.inviteMember = inviteMember;
  // Notifications (profile row) → enable push.
  App.enableNotifications = enableNotifications;
  // Alerts screen → clear notification history.
  App.clearAlerts = clearAlerts;

  // Lights: local flip (toggles .lkd), then backend with the new state.
  const origLights = App.toggleLights.bind(App);
  App.toggleLights = (el) => {
    origLights(el);
    sendCommand("lights", { on: el.classList.contains("lkd") });
  };

  // Trunk: local flip, then backend with the new open/closed state.
  const origTrunk = App.toggleTrunk.bind(App);
  App.toggleTrunk = (el) => {
    origTrunk(el);
    sendCommand("trunk", { on: !!App.trunkOpen });
  };

  // Horn: momentary — local pop animation, then fire the backend command.
  const origHonk = App.honk.bind(App);
  App.honk = (el) => {
    origHonk(el);
    sendCommand("horn");
  };
}

// cmdFeedback maps an action to the control element to mark busy + the
// success message shown on completion.
function cmdFeedback(action) {
  const uz = window.App?.lang === "uz";
  const lock = () => document.getElementById("lock-btn");
  const power = () => document.getElementById("power-btn");
  const byId = (id) => () => document.getElementById(id);
  const M = {
    lock:    [lock,  uz ? "Qulflandi" : "Locked"],
    unlock:  [lock,  uz ? "Ochildi" : "Unlocked"],
    start:   [power, uz ? "Dvigatel yoqildi" : "Engine started"],
    stop:    [power, uz ? "Dvigatel o‘chirildi" : "Engine stopped"],
    climate: [() => null, uz ? "Iqlim yangilandi" : "Climate updated"],
    lights:  [byId("lights-btn"), uz ? "Faralar" : "Lights"],
    trunk:   [byId("trunk-btn"),  uz ? "Bagaj" : "Trunk"],
    horn:    [byId("horn-btn"),   uz ? "Signal berildi" : "Horn"],
    seat:    [() => null, uz ? "O‘rindiq saqlandi" : "Seat saved"],
  };
  const [getEl, msg] = M[action] || [() => null, uz ? "Bajarildi" : "Done"];
  return { el: getEl(), msg };
}

async function sendCommand(action, body) {
  if (!CFG.apiBase || !auth?.currentUser) return; // demo mode: local only
  const { el, msg } = cmdFeedback(action);
  haptic(12);
  if (el) el.classList.add("vd-busy");
  try {
    const token = await auth.currentUser.getIdToken();
    const vid = VMAP[App.activeCar?.id] || "voyah-001";
    const res = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/${action}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${token}`,
      },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    // The backend returns the fresh snapshot — reflect it immediately.
    const snap = await res.json().catch(() => null);
    if (snap && snap.vehicleId) applyLive(snap);
    toast(msg + " ✓", "ok");
    haptic([10, 40, 12]);
    if (el) { el.classList.add("vd-ok"); setTimeout(() => el.classList.remove("vd-ok"), 440); }
  } catch (e) {
    const uz = window.App?.lang === "uz";
    toast((uz ? "Xato: " : "Failed: ") + action + " (" + e.message + ")", "err");
    haptic([40, 30, 40]);
  } finally {
    if (el) el.classList.remove("vd-busy");
  }
}

// --- tiny helpers ---

// set updates an element's text by id. When stripI18n is true the data-i18n
// attribute is removed so the language toggle won't overwrite the live value.
function set(id, value, stripI18n) {
  if (value == null) return;
  const el = document.getElementById(id);
  if (!el) return;
  el.textContent = value;
  if (stripI18n) el.removeAttribute("data-i18n");
}

function setHTML(id, html) {
  const el = document.getElementById(id);
  if (el) el.innerHTML = html;
}

// toast shows a transient message. type: "ok" | "err" | undefined (neutral).
function toast(msg, type) {
  let t = document.getElementById("vd-toast");
  if (!t) {
    t = document.createElement("div");
    t.id = "vd-toast";
    t.style.cssText =
      "position:fixed;left:50%;bottom:96px;transform:translateX(-50%) translateY(8px);" +
      "background:#1A1C22;color:#fff;padding:11px 18px;border-radius:12px;" +
      "font:500 13px/1.3 system-ui;z-index:9999;box-shadow:0 8px 24px rgba(0,0,0,.4);" +
      "max-width:80vw;text-align:center;opacity:0;pointer-events:none;" +
      "transition:opacity .2s,transform .25s cubic-bezier(.34,1.56,.64,1);";
    document.body.appendChild(t);
  }
  t.className = type || "";
  t.textContent = msg;
  t.style.opacity = "1";
  t.style.transform = "translateX(-50%) translateY(0)";
  clearTimeout(t._h);
  t._h = setTimeout(() => {
    t.style.opacity = "0";
    t.style.transform = "translateX(-50%) translateY(8px)";
  }, 2600);
}

// haptic fires a short vibration when the device supports it (no-op otherwise).
function haptic(ms = 12) {
  try { navigator.vibrate && navigator.vibrate(ms); } catch (e) {}
}

// --- Pull-to-refresh (native app feel) ---
//
// Pulling down from the top of the active screen reveals a spinner; releasing
// past the threshold refreshes live data (or reloads when offline/no session).
function setupPullToRefresh() {
  const THRESHOLD = 72;
  let startY = 0, pulling = false, scroller = null, dist = 0;

  // Visual indicator.
  const ind = document.createElement("div");
  ind.id = "vd-ptr";
  ind.style.cssText =
    "position:fixed;left:50%;top:0;transform:translate(-50%,-44px);" +
    "width:34px;height:34px;border-radius:50%;background:#1A1C22;z-index:9998;" +
    "display:flex;align-items:center;justify-content:center;transition:transform .15s;" +
    "box-shadow:0 6px 18px rgba(0,0,0,.45);";
  ind.innerHTML =
    '<div style="width:16px;height:16px;border:2px solid #FF6A1A;border-top-color:transparent;border-radius:50%"></div>';
  document.addEventListener("DOMContentLoaded", () => document.body.appendChild(ind));
  if (document.body) document.body.appendChild(ind);

  const spinner = ind.firstElementChild;

  function scrollableUnder(el) {
    while (el && el !== document.body) {
      const oy = getComputedStyle(el).overflowY;
      if ((oy === "auto" || oy === "scroll") && el.scrollHeight > el.clientHeight) return el;
      el = el.parentElement;
    }
    return null;
  }

  window.addEventListener("touchstart", (e) => {
    // Only a single finger, starting near the very top, and never over the map
    // (so pinch-zoom / panning the map is never mistaken for a refresh).
    if (e.touches.length !== 1) return;
    if (window.App && App.current === "map") return;
    if (e.target.closest && e.target.closest("#vd-map")) return;
    const y = e.touches[0].clientY;
    if (y > 140) return;
    scroller = scrollableUnder(e.target);
    const atTop = !scroller || scroller.scrollTop <= 0;
    if (atTop) {
      startY = y;
      pulling = true;
      dist = 0;
    }
  }, { passive: true });

  window.addEventListener("touchmove", (e) => {
    if (!pulling) return;
    if (e.touches.length !== 1) { pulling = false; ind.style.transform = "translate(-50%,-44px)"; return; }
    dist = e.touches[0].clientY - startY;
    if (dist > 0) {
      const pull = Math.min(dist, 140);
      ind.style.transition = "none";
      ind.style.transform = `translate(-50%, ${Math.min(pull - 44, 70)}px)`;
      spinner.style.transform = `rotate(${pull * 3}deg)`;
    }
  }, { passive: true });

  window.addEventListener("touchend", async () => {
    if (!pulling) return;
    pulling = false;
    ind.style.transition = "transform .25s";
    if (dist > THRESHOLD) {
      ind.style.transform = "translate(-50%, 24px)";
      spinner.style.animation = "vdspin .7s linear infinite";
      // Refresh live data WITHOUT reloading the page (so the PIN/login screen
      // never reappears just from a pull/gesture).
      await refreshData();
      setTimeout(() => {
        spinner.style.animation = "";
        ind.style.transform = "translate(-50%, -44px)";
      }, 600);
    } else {
      ind.style.transform = "translate(-50%, -44px)";
    }
  }, { passive: true });

  // Spin keyframes.
  const st = document.createElement("style");
  st.textContent = "@keyframes vdspin{to{transform:rotate(360deg)}}";
  document.head.appendChild(st);
}

// refreshData re-pulls the latest snapshot when signed in.
async function refreshData() {
  try {
    if (db && window.App) subscribe();
  } catch (_) {}
}

// --- Install prompt (one-tap "install as app") ---
function setupInstallPrompt() {
  // Already running as an installed app? Then no prompt needed.
  const standalone =
    window.matchMedia("(display-mode: standalone)").matches ||
    window.navigator.standalone === true;
  if (standalone) return;

  let deferred = null;
  window.addEventListener("beforeinstallprompt", (e) => {
    e.preventDefault();
    deferred = e;
    showInstallButton();
  });

  function showInstallButton() {
    if (document.getElementById("vd-install")) return;
    const b = document.createElement("button");
    b.id = "vd-install";
    b.textContent = "⤓ Ilovani o'rnatish";
    b.style.cssText =
      "position:fixed;left:50%;bottom:18px;transform:translateX(-50%);z-index:9997;" +
      "background:linear-gradient(150deg,#FF8A2B,#FF4D00);color:#fff;border:none;" +
      "padding:12px 20px;border-radius:14px;font:700 14px/1 'Sora',system-ui;" +
      "box-shadow:0 10px 26px -6px rgba(255,90,0,.6);cursor:pointer;";
    b.onclick = async () => {
      if (!deferred) return;
      deferred.prompt();
      const { outcome } = await deferred.userChoice;
      deferred = null;
      b.remove();
      if (outcome === "accepted") toast("Ilova o'rnatildi ✓");
    };
    document.body.appendChild(b);
    // Auto-hide after 12s so it doesn't block the UI.
    setTimeout(() => b && b.remove && b.remove(), 12000);
  }
}

// --- Make the control widgets interactive ---
//
// Wires up the climate sliders, temperature dial, seat-heat, defrost and the
// climate power button (which talks to the backend), plus tap feedback for
// action buttons that otherwise did nothing.
function wireExtras() {
  if (!window.App) {
    document.addEventListener("DOMContentLoaded", wireExtras, { once: true });
    return;
  }

  // Sliders: tap or drag to set a 0-100% value.
  document.querySelectorAll(".vd-slider").forEach((sl) => {
    const apply = (clientX) => {
      const rect = sl.getBoundingClientRect();
      let pct = Math.round(((clientX - rect.left) / rect.width) * 100);
      pct = Math.max(0, Math.min(100, pct));
      const fill = sl.querySelector(".vd-fill");
      if (fill) fill.style.width = pct + "%";
      const val = sl.parentElement.querySelector(".vd-sval");
      if (val) val.textContent = pct + "%";
    };
    let down = false;
    sl.addEventListener("pointerdown", (e) => { down = true; apply(e.clientX); });
    sl.addEventListener("pointermove", (e) => { if (down) apply(e.clientX); });
    window.addEventListener("pointerup", () => { down = false; });
    sl.addEventListener("click", (e) => apply(e.clientX));
  });

  // Seat heat L / R toggles (independent).
  document.querySelectorAll(".vd-seat").forEach((s) => {
    s.addEventListener("click", () => {
      const on = !s.classList.contains("on");
      s.classList.toggle("on", on);
      s.style.background = on ? "linear-gradient(150deg,#FF8A2B,#FF4D00)" : "rgba(255,255,255,.06)";
      s.style.color = on ? "#fff" : "#7e8086";
    });
  });

  // Temperature dial − / +  → updates UI and pushes target to the backend.
  const dec = document.getElementById("climate-minus");
  const inc = document.getElementById("climate-plus");
  if (dec) dec.addEventListener("click", () => { climateTemp = Math.max(16, climateTemp - 1); climateChanged(); });
  if (inc) inc.addEventListener("click", () => { climateTemp = Math.min(30, climateTemp + 1); climateChanged(); });

  // Climate power: turn HVAC on/off (backend command).
  const pw = document.getElementById("climate-power");
  if (pw) pw.addEventListener("click", () => { climateOn = !climateOn; climateChanged(); });

  updateClimateUI();

  // In-app map: initialise Leaflet whenever the Map screen opens.
  if (App.go) {
    const origGo = App.go.bind(App);
    App.go = (s) => {
      origGo(s);
      if (s === "map") ensureMap();
      // Smart Access needs a live GPS fix to measure distance to the car.
      if (s === "access") locateDevice(true);
      // Family sharing: load the current member list from the backend.
      if (s === "family") loadMembers();
      // Notification history.
      if (s === "alerts") renderAlerts();
    };
  }
  if (App.current === "map") ensureMap();

  // "Let's Go" recenters the map on the current location.
  document.querySelectorAll("#s-map .obtn").forEach((el) => {
    const target = el.closest(".obtn") || el;
    target.addEventListener("click", () => {
      ensureMap();
      if (leafletMap) leafletMap.setView(mapCenter(), 16, { animate: true });
      toast(window.App?.lang === "uz" ? "Joylashuvga yo'naltirildi" : "Centered on location");
    });
  });
  // The navigation arrow (top-right) = "find me": (re)request the real GPS fix.
  document.querySelectorAll("#s-map [data-lucide='navigation']").forEach((el) => {
    const btn = el.closest(".ibtn") || el;
    btn.addEventListener("click", () => { ensureMap(); locateDevice(true); });
  });

  // Address search (debounced) → Nominatim → pick → OSRM route.
  const search = document.getElementById("map-search");
  if (search) {
    let t;
    search.addEventListener("input", () => {
      clearTimeout(t);
      const q = search.value.trim();
      const box = document.getElementById("map-results");
      if (q.length < 3) { if (box) box.style.display = "none"; return; }
      t = setTimeout(async () => renderResults(await geocode(q)), 450);
    });
    search.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { clearTimeout(t); geocode(search.value.trim()).then(renderResults); }
    });
  }
  wireSeat();
  wireAccess();

  // Profile edit (pencil): change the display name.
  const pe = document.getElementById("profile-edit");
  if (pe) pe.addEventListener("click", async () => {
    const uz = window.App?.lang === "uz";
    if (!auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
    const cur = auth.currentUser.displayName || (auth.currentUser.email || "").split("@")[0];
    const name = prompt(uz ? "Ismingizni kiriting:" : "Enter your name:", cur);
    if (name == null || !name.trim()) return;
    try {
      await updateProfile(auth.currentUser, { displayName: name.trim() });
      showUser(auth.currentUser);
      toast(uz ? "Profil saqlandi ✓" : "Profile saved ✓");
    } catch (e) {
      toast((uz ? "Xato: " : "Error: ") + (e.code || e.message));
    }
  });

  // POI layer chips.
  const pc = document.getElementById("poi-charge");
  const pf = document.getElementById("poi-fuel");
  const px = document.getElementById("poi-clear");
  if (pc) pc.addEventListener("click", () => { pc.classList.add("on"); pf && pf.classList.remove("on"); loadPOI("charging_station"); });
  if (pf) pf.addEventListener("click", () => { pf.classList.add("on"); pc && pc.classList.remove("on"); loadPOI("fuel"); });
  if (px) px.addEventListener("click", () => { clearRoute(); toast(window.App?.lang === "uz" ? "Tozalandi" : "Cleared"); });

  // Tap feedback for buttons that otherwise did nothing.
  const feedback = (sel, msg) =>
    document.querySelectorAll(sel).forEach((el) =>
      el.addEventListener("click", () => toast(msg)));
  feedback("[data-i18n='family.invite']", "Taklif havolasi yuborildi (demo)");
  feedback("#s-family .ibtn.org", "A'zo qo'shish (demo)");
}

// ensureMap initialises the in-app Leaflet map (dark theme tiles, no API key)
// and a marker at the vehicle's live location.
function mapCenter() {
  const c = deviceLoc || lastLoc || { lat: 41.311081, lng: 69.240562 };
  return [c.lat, c.lng];
}

function ensureMap() {
  if (typeof L === "undefined") return; // Leaflet not loaded
  const el = document.getElementById("vd-map");
  if (!el) return;
  if (!leafletMap) {
    // rotate/touchRotate (from leaflet-rotate) → two-finger map rotation.
    const opts = { zoomControl: false, attributionControl: false, rotate: true, touchRotate: true, rotateControl: false };
    try {
      leafletMap = L.map(el, opts).setView(mapCenter(), 15);
    } catch (_) {
      leafletMap = L.map(el, { zoomControl: false, attributionControl: false }).setView(mapCenter(), 15);
    }
    L.tileLayer("https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png", {
      maxZoom: 19, subdomains: "abcd",
    }).addTo(leafletMap);
    // Marker = orange dot + fading heading cone (divIcon).
    const icon = L.divIcon({
      className: "",
      html: '<div class="vd-locwrap"><div class="vd-cone"></div><div class="vd-dot"></div></div>',
      iconSize: [0, 0],
    });
    carMarker = L.marker(mapCenter(), { icon, interactive: false }).addTo(leafletMap);
  }
  // The container only has a size once the screen is visible.
  setTimeout(() => {
    leafletMap.invalidateSize();
    leafletMap.setView(mapCenter(), leafletMap.getZoom() || 15);
    if (carMarker) carMarker.setLatLng(mapCenter());
  }, 180);
  // Center on the phone's REAL location the first time the map opens.
  if (!deviceLoc) locateDevice(false);
}

let locating = false;
let watchId = null;
let centeredOnce = false;
let accCircle = null;

function locateDevice(force) {
  const uz = window.App?.lang === "uz";
  if (!navigator.geolocation) { toast(uz ? "Geolokatsiya yo'q" : "No geolocation"); return; }

  // Already tracking and not a forced re-locate → just recenter.
  if (watchId !== null && !force) { if (deviceLoc) recenterDevice(); return; }
  if (force) centeredOnce = false;
  if (watchId !== null) { navigator.geolocation.clearWatch(watchId); watchId = null; }

  toast(uz ? "Joylashuv aniqlanmoqda…" : "Locating…");
  startHeading();
  let announced = false;
  // watchPosition keeps refining the fix (network → GPS) for Google-Maps-level
  // accuracy, instead of a single coarse getCurrentPosition reading.
  watchId = navigator.geolocation.watchPosition(
    (p) => {
      locating = false;
      deviceLoc = { lat: p.coords.latitude, lng: p.coords.longitude, acc: p.coords.accuracy || 0 };
      geofenceTick(); // proximity auto lock/unlock
      if (carMarker) carMarker.setLatLng([deviceLoc.lat, deviceLoc.lng]);
      // GPS heading while moving (compass handles the stationary case).
      if (typeof p.coords.heading === "number" && !isNaN(p.coords.heading) && p.coords.speed > 0.5) {
        setHeading(p.coords.heading);
      }
      // Blue/orange accuracy halo, like Google's.
      if (leafletMap) {
        if (!accCircle) {
          accCircle = L.circle([deviceLoc.lat, deviceLoc.lng], {
            radius: deviceLoc.acc, color: "#FF6A1A", weight: 1, fillColor: "#FF6A1A", fillOpacity: 0.12,
          }).addTo(leafletMap);
        } else {
          accCircle.setLatLng([deviceLoc.lat, deviceLoc.lng]);
          accCircle.setRadius(deviceLoc.acc);
        }
        if (!centeredOnce) { leafletMap.setView([deviceLoc.lat, deviceLoc.lng], 17); centeredOnce = true; }
      }
      const c = `${deviceLoc.lat.toFixed(5)}, ${deviceLoc.lng.toFixed(5)}`;
      set("map-addr", c);
      set("home-loc", c);
      if (!announced) {
        announced = true;
        toast((uz ? "Joylashuv aniqlandi ✓ ±" : "Located ✓ ±") + Math.round(deviceLoc.acc) + "m");
      }
    },
    (err) => {
      locating = false;
      const m = err.code === 1 ? (uz ? "ruxsat berilmadi" : "permission denied")
        : err.code === 2 ? (uz ? "GPS o'chiq / signal yo'q" : "GPS off / no signal")
        : (uz ? "vaqt tugadi" : "timed out");
      toast((uz ? "Joylashuv: " : "Location: ") + m);
    },
    { enableHighAccuracy: true, timeout: 20000, maximumAge: 0 }
  );
}

function recenterDevice() {
  if (leafletMap && deviceLoc) {
    leafletMap.setView([deviceLoc.lat, deviceLoc.lng], 17);
    if (carMarker) carMarker.setLatLng([deviceLoc.lat, deviceLoc.lng]);
  }
}

// setHeading rotates the heading cone to a compass bearing (0 = north).
// Uses a continuous accumulator + easing + dead-band so the cone glides
// smoothly and never spins the long way around the 0/360 boundary.
let contHeading = null;
function setHeading(raw) {
  if (raw == null || isNaN(raw)) return;
  if (contHeading == null) contHeading = raw;
  let diff = (((raw - contHeading) % 360) + 540) % 360 - 180; // shortest signed turn
  if (Math.abs(diff) < 2) return; // ignore sensor jitter
  contHeading += diff * 0.18;     // ease toward target (continuous, no modulo)
  const el = carMarker && carMarker.getElement && carMarker.getElement();
  const cone = el && el.querySelector(".vd-cone");
  if (cone) cone.style.transform = `rotate(${contHeading.toFixed(1)}deg)`;
}

// startHeading listens to the device compass so the cone shows which way the
// phone is pointing (like Google's blue beam).
let headingStarted = false;
function startHeading() {
  if (headingStarted) return;
  const handler = (e) => {
    let h = null;
    if (typeof e.webkitCompassHeading === "number") h = e.webkitCompassHeading; // iOS
    else if (typeof e.alpha === "number") h = (360 - e.alpha) % 360; // Android
    setHeading(h);
  };
  const attach = () => {
    headingStarted = true;
    window.addEventListener("deviceorientationabsolute", handler, true);
    window.addEventListener("deviceorientation", handler, true);
  };
  // iOS needs explicit permission (must be called from a user gesture).
  if (typeof DeviceOrientationEvent !== "undefined" && typeof DeviceOrientationEvent.requestPermission === "function") {
    DeviceOrientationEvent.requestPermission().then((s) => { if (s === "granted") attach(); }).catch(() => {});
  } else {
    attach();
  }
}

// updateMapMarker moves the car marker as live telemetry arrives. The device's
// real location takes priority over the simulated backend coordinate.
function updateMapMarker() {
  if (leafletMap && carMarker) carMarker.setLatLng(mapCenter());
}

// --- Search, routing and POIs (OpenStreetMap services, no API key) ---

function haversine(a, lat, lng) {
  const R = 6371, toR = Math.PI / 180;
  const dLat = (lat - a.lat) * toR, dLng = (lng - a.lng) * toR;
  const x = Math.sin(dLat / 2) ** 2 + Math.cos(a.lat * toR) * Math.cos(lat * toR) * Math.sin(dLng / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(x));
}

// Geocode an address with Nominatim (biased to Uzbekistan).
async function geocode(q) {
  const url = `https://nominatim.openstreetmap.org/search?format=json&limit=6&accept-language=uz&countrycodes=uz&q=${encodeURIComponent(q)}`;
  try {
    const r = await fetch(url, { headers: { Accept: "application/json" } });
    return r.ok ? await r.json() : [];
  } catch (_) { return []; }
}

function renderResults(list) {
  const box = document.getElementById("map-results");
  if (!box) return;
  if (!list || !list.length) { box.style.display = "none"; return; }
  box.innerHTML = list.map((it, i) =>
    `<div class="vd-res" data-i="${i}" style="padding:12px 14px;border-bottom:1px solid rgba(255,255,255,.06);color:#fff;font-size:13px;cursor:pointer;">${(it.display_name || "").split(",").slice(0, 3).join(",")}</div>`
  ).join("");
  box.style.display = "block";
  box.querySelectorAll(".vd-res").forEach((el) =>
    el.addEventListener("click", () => {
      const it = list[+el.dataset.i];
      box.style.display = "none";
      const search = document.getElementById("map-search");
      if (search) search.value = (it.display_name || "").split(",")[0];
      setDestination(+it.lat, +it.lon, (it.display_name || "").split(",").slice(0, 2).join(","));
    })
  );
}

function setDestination(lat, lng, name) {
  ensureMap();
  if (destMarker) leafletMap.removeLayer(destMarker);
  destMarker = L.marker([lat, lng], {
    icon: L.divIcon({
      className: "",
      html: '<div style="width:18px;height:18px;border-radius:50% 50% 50% 0;transform:rotate(-45deg);background:linear-gradient(150deg,#FF8A2B,#FF4D00);border:2px solid #fff;box-shadow:0 4px 10px rgba(0,0,0,.5)"></div>',
      iconSize: [18, 18], iconAnchor: [9, 18],
    }),
  }).addTo(leafletMap);
  set("map-name", name);
  routeTo(lat, lng);
}

// Fastest driving route via OSRM, drawn on the map.
async function routeTo(dlat, dlng) {
  const from = deviceLoc || lastLoc || { lat: 41.311081, lng: 69.240562 };
  const uz = window.App?.lang === "uz";
  toast(uz ? "Yo'l qurilmoqda…" : "Routing…");
  try {
    const url = `https://router.project-osrm.org/route/v1/driving/${from.lng},${from.lat};${dlng},${dlat}?overview=full&geometries=geojson`;
    const d = await (await fetch(url)).json();
    if (!d.routes || !d.routes.length) { toast(uz ? "Yo'l topilmadi" : "No route"); return; }
    const rt = d.routes[0];
    if (routeLine) leafletMap.removeLayer(routeLine);
    routeLine = L.geoJSON(rt.geometry, { style: { color: "#FF6A1A", weight: 6, opacity: 0.9 } }).addTo(leafletMap);
    leafletMap.fitBounds(routeLine.getBounds(), { padding: [60, 60] });
    const km = (rt.distance / 1000).toFixed(1), min = Math.round(rt.duration / 60);
    set("map-dist", km + " km");
    set("map-eta", `${km} km · ${min} ${uz ? "daqiqa" : "min"}`, true);
  } catch (_) { toast(uz ? "Yo'l xatosi" : "Route error"); }
}

// Charging / fuel stations near the map centre (Overpass), nearest first.
async function loadPOI(kind) {
  ensureMap();
  const uz = window.App?.lang === "uz";
  const c = (leafletMap && leafletMap.getCenter()) || deviceLoc || { lat: 41.311081, lng: 69.240562 };
  clearPOI();
  toast(kind === "fuel" ? (uz ? "Benzin shaxobchalari…" : "Fuel stations…") : (uz ? "Zaryad shaxobchalari…" : "Charging stations…"));
  const q = `[out:json][timeout:25];(node["amenity"="${kind}"](around:8000,${c.lat},${c.lng}););out body 50;`;
  try {
    const d = await (await fetch("https://overpass-api.de/api/interpreter", { method: "POST", body: q })).json();
    let items = (d.elements || []).filter((e) => e.lat && e.lon).map((e) => ({
      lat: e.lat, lng: e.lon,
      name: (e.tags && (e.tags.name || e.tags.brand)) || (kind === "fuel" ? "Benzin" : "Zaryad"),
      d: haversine(c, e.lat, e.lon),
    }));
    items.sort((a, b) => a.d - b.d);
    const ic = kind === "fuel" ? "⛽" : "⚡";
    items.slice(0, 50).forEach((it) => {
      const m = L.marker([it.lat, it.lng], {
        icon: L.divIcon({ className: "", html: `<div class="vd-poi">${ic}</div>`, iconSize: [26, 26], iconAnchor: [13, 13] }),
      }).addTo(leafletMap);
      m.bindPopup(`${it.name} · ${it.d.toFixed(1)} km`);
      poiMarkers.push(m);
    });
    if (items.length) {
      const n = items[0];
      set("map-stations", `${items.length} ${uz ? "ta — eng yaqini" : "found — nearest"} ${n.d.toFixed(1)} km`, true);
    } else {
      set("map-stations", uz ? "Yaqin atrofda topilmadi" : "None nearby", true);
    }
  } catch (_) { toast(uz ? "Shaxobcha xatosi" : "POI error"); }
}

function clearPOI() {
  poiMarkers.forEach((m) => leafletMap && leafletMap.removeLayer(m));
  poiMarkers = [];
}

function clearRoute() {
  clearPOI();
  if (routeLine) { leafletMap.removeLayer(routeLine); routeLine = null; }
  if (destMarker) { leafletMap.removeLayer(destMarker); destMarker = null; }
  document.querySelectorAll(".map-chip").forEach((c) => c.classList.remove("on"));
}

// --- Seat adjustment: reclining seat + per-seat memory + 360° spin ---
function wireSeat() {
  const byId = (id) => document.getElementById(id);
  const setA = (a) => {
    seatVals[seatSel] = Math.max(30, Math.min(120, Math.round(a)));
    updateSeat();
    // Persist the seat position to the backend (debounced).
    clearTimeout(seatSendT);
    seatSendT = setTimeout(
      () => sendCommand("seat", { seat: seatSel, recline: seatVals[seatSel] }),
      450
    );
  };

  const minus = byId("seat-minus"), plus = byId("seat-plus"), slider = byId("seat-slider");
  if (minus) minus.addEventListener("click", () => setA(seatVals[seatSel] - 2));
  if (plus) plus.addEventListener("click", () => setA(seatVals[seatSel] + 2));
  if (slider) {
    const fromX = (clientX) => {
      const r = slider.getBoundingClientRect();
      setA(30 + Math.max(0, Math.min(1, (clientX - r.left) / r.width)) * 90);
    };
    let down = false;
    slider.addEventListener("pointerdown", (e) => { down = true; fromX(e.clientX); });
    slider.addEventListener("pointermove", (e) => { if (down) fromX(e.clientX); });
    window.addEventListener("pointerup", () => { down = false; });
  }
  document.querySelectorAll(".seat-tab").forEach((t) =>
    t.addEventListener("click", () => {
      document.querySelectorAll(".seat-tab").forEach((x) => x.classList.remove("on"));
      t.classList.add("on");
      seatSel = t.dataset.seat || "driver";
      updateSeat();
    })
  );
  const rot = byId("seat-rotate");
  if (rot) rot.addEventListener("click", () => {
    const el = byId("seat-3d");
    if (!el) return;
    // Toggle the model's continuous auto-rotation.
    if (el.hasAttribute("auto-rotate")) el.removeAttribute("auto-rotate");
    else el.setAttribute("auto-rotate", "");
  });
  updateSeat();

  // If a custom car-seat model is bundled (project/models/seat.glb), use it.
  const mv = byId("seat-3d");
  if (mv) {
    fetch("project/models/seat.glb", { method: "HEAD" })
      .then((r) => { if (r.ok) mv.setAttribute("src", "project/models/seat.glb"); })
      .catch(() => {});
  }
}

function updateSeat() {
  const a = seatVals[seatSel];
  set("seat-angle", String(a));
  const back = document.getElementById("seat-back");
  if (back) back.setAttribute("transform", `rotate(${(-(a - 30) * 0.55).toFixed(1)} 62 148)`);
  const p = ((a - 30) / 90) * 100;
  const fill = document.getElementById("seat-fill");
  if (fill) fill.style.width = p + "%";
  const knob = document.getElementById("seat-knob");
  if (knob) knob.style.left = p + "%";
  const arc = document.getElementById("seat-arc");
  if (arc) {
    const deg = 20 + ((a - 30) / 90) * 120;
    arc.style.background = `conic-gradient(from 200deg,#FF5A00 0deg,#FF9D3D ${deg}deg,transparent ${deg}deg 360deg)`;
  }
}

// climateChanged updates the UI and (debounced) sends the climate command.
function climateChanged() {
  updateClimateUI();
  clearTimeout(climateSendT);
  climateSendT = setTimeout(() => sendCommand("climate", { on: climateOn, targetC: climateTemp }), 400);
}

function updateClimateUI() {
  const t = document.getElementById("climate-temp");
  if (t) t.textContent = String(climateTemp);
  const ring = document.getElementById("climate-ring");
  if (ring) {
    const ang = Math.round(((climateTemp - 16) / 14) * 360);
    ring.style.background = `conic-gradient(#FF5A00 0deg,#FF8A2B ${ang}deg,rgba(255,255,255,.06) ${ang}deg 360deg)`;
  }
  const st = document.getElementById("climate-state");
  if (st) {
    const uz = window.App?.lang === "uz";
    st.textContent = climateOn ? `${climateTemp}° · ${uz ? "Yoqilgan" : "ON"}` : `${climateTemp}° · ${uz ? "O'chiq" : "OFF"}`;
    st.removeAttribute("data-i18n");
  }
  const pw = document.getElementById("climate-power");
  if (pw) pw.style.color = climateOn ? "#FF6A1A" : "";
}

// --- Proximity auto lock/unlock (geofence) ---
//
// While the app is open and a GPS fix is available, we measure the distance
// between the phone and the car's last known location. Crossing the NEAR
// threshold (approaching) auto-unlocks; crossing the FAR threshold (walking
// away) auto-locks. Hysteresis (NEAR < FAR) prevents flapping at the edge.
//
// Limits to be honest about: this runs only while the PWA is open in the
// foreground (browsers can't geofence reliably in the background — that needs
// a native app), and phone GPS is accurate to ~10-20 m, so the radii are tens
// of metres, not 2 m. The car reference point is its live telemetry location.

const NEAR_M = 25; // within this → "arrived"
const FAR_M = 70; // beyond this → "left"
let geoNear = null; // null = unknown, true = near, false = far

function distMeters(a, b) {
  if (!a || !b) return Infinity;
  const R = 6371000, toR = Math.PI / 180;
  const dLat = (b.lat - a.lat) * toR, dLng = (b.lng - a.lng) * toR;
  const la1 = a.lat * toR, la2 = b.lat * toR;
  const x = Math.sin(dLat / 2) ** 2 + Math.cos(la1) * Math.cos(la2) * Math.sin(dLng / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(x));
}

function tglOn(id) {
  const el = document.getElementById(id);
  return !!el && el.classList.contains("on");
}

function geofenceTick() {
  if (!window.App || !deviceLoc || !lastLoc) return;
  const d = distMeters(deviceLoc, lastLoc);
  updateAccessUI(d);

  // Determine near/far with hysteresis.
  let near = geoNear;
  if (d <= NEAR_M) near = true;
  else if (d >= FAR_M) near = false;
  if (near === geoNear) return; // no state change (or inside the dead band)
  const first = geoNear === null;
  geoNear = near;
  if (first) return; // don't fire a command on the very first reading

  const uz = App.lang === "uz";
  if (near && tglOn("auto-unlock-tgl") && App.locked) {
    sendCommand("unlock");
    toast(uz ? "Yaqinlashdingiz — eshik ochildi ✓" : "Approached — unlocked ✓", "ok");
  } else if (!near && tglOn("walk-lock-tgl") && !App.locked) {
    sendCommand("lock");
    toast(uz ? "Uzoqlashdingiz — qulflandi ✓" : "Walked away — locked ✓", "ok");
  }
}

function updateAccessUI(d) {
  const uz = window.App?.lang === "uz";
  set("acc-dist", d < 1000 ? Math.round(d) + " m" : (d / 1000).toFixed(1) + " km");
  const near = d <= NEAR_M;
  const card = document.getElementById("acc-card");
  const icon = document.getElementById("acc-icon");
  const status = document.getElementById("acc-status");
  const sub = document.getElementById("acc-substatus");
  if (status) { status.textContent = near ? (uz ? "Mashina yaqin" : "Car is nearby") : (uz ? "Mashina uzoqda" : "Car is far"); status.removeAttribute("data-i18n"); }
  if (sub) { sub.textContent = (uz ? "Masofa · " : "Distance · ") + (d < 1000 ? Math.round(d) + " m" : (d / 1000).toFixed(1) + " km"); sub.removeAttribute("data-i18n"); }
  if (icon) icon.setAttribute("data-lucide", near ? "lock-open" : "lock");
  if (card) {
    const g = "linear-gradient(120deg,rgba(55,214,122,.12),rgba(55,214,122,.04))";
    const o = "linear-gradient(120deg,rgba(255,106,26,.12),rgba(255,106,26,.04))";
    card.style.background = near ? g : o;
    card.style.borderColor = near ? "rgba(55,214,122,.3)" : "rgba(255,122,46,.3)";
  }
  if (window.lucide) lucide.createIcons();
}

// wireAccess persists the two toggles and starts a GPS watch while either is on.
function wireAccess() {
  ["auto-unlock-tgl", "walk-lock-tgl"].forEach((id) => {
    const el = document.getElementById(id);
    if (!el) return;
    const saved = localStorage.getItem("vd_" + id);
    if (saved !== null) {
      const on = saved === "1";
      el.classList.toggle("on", on);
      el.classList.toggle("off", !on);
    }
    el.addEventListener("click", () => {
      // runs after the inline class toggle; read + persist the final state
      setTimeout(() => {
        localStorage.setItem("vd_" + id, el.classList.contains("on") ? "1" : "0");
        if (tglOn("auto-unlock-tgl") || tglOn("walk-lock-tgl")) locateDevice(true);
      }, 0);
    });
  });
  // If a toggle is already on at startup, begin tracking so it works from home.
  if (tglOn("auto-unlock-tgl") || tglOn("walk-lock-tgl")) {
    setTimeout(() => { try { locateDevice(true); } catch (e) {} }, 1500);
  }
}

// --- Family / shared access ---
//
// The owner manages who can use the car (driver / guest). The list lives in
// Firestore via the backend; this renders it, supports inviting by Gmail
// address and removing members.

const ROLE_LABEL = {
  owner: { uz: "Egasi", en: "Owner", sub: { uz: "To‘liq kirish", en: "Full access" }, cls: "org" },
  driver: { uz: "Haydovchi", en: "Driver", sub: { uz: "Haydash · qulf · iqlim", en: "Drive · lock · climate" }, cls: "gry" },
  guest: { uz: "Cheklangan", en: "Limited", sub: { uz: "Joylashuv · faqat qulf", en: "Location · lock only" }, cls: "gry" },
};

async function loadMembers() {
  if (!CFG.apiBase || !auth?.currentUser) return;
  const uz = window.App?.lang === "uz";
  // Fill the owner (self) row from the signed-in account.
  const me = auth.currentUser;
  const myName = (me.displayName || (me.email || "").split("@")[0] || "You");
  set("family-self", myName);
  const av = document.getElementById("family-self-av");
  if (av) av.textContent = (myName[0] || "U").toUpperCase();
  try {
    const token = await me.getIdToken();
    const vid = VMAP[App.activeCar?.id] || "voyah-001";
    const res = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/members`, { headers: { Authorization: `Bearer ${token}` } });
    if (!res.ok) return;
    const data = await res.json();
    renderMembers(data.members || []);
  } catch (e) { /* offline — keep owner row */ }
}

function renderMembers(list) {
  const wrap = document.getElementById("family-members");
  if (!wrap) return;
  const uz = window.App?.lang === "uz";
  // Keep the first child (owner/self row); drop previously rendered members.
  [...wrap.querySelectorAll("[data-member]")].forEach((el) => el.remove());
  list.forEach((m) => {
    const meta = ROLE_LABEL[m.role] || ROLE_LABEL.guest;
    const initials = (m.email[0] || "?").toUpperCase();
    const row = document.createElement("div");
    row.dataset.member = m.email;
    row.style.cssText = "display:flex;align-items:center;gap:12px;";
    row.innerHTML =
      '<div class="av">' + initials + "</div>" +
      '<div style="flex:1;min-width:0"><div style="color:#fff;font-weight:600;font-size:14px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">' + m.email + "</div>" +
      '<div style="color:#7e8086;font-size:11px;">' + (uz ? meta.sub.uz : meta.sub.en) + "</div></div>" +
      '<div class="badge ' + meta.cls + '">' + (uz ? meta.uz : meta.en) + "</div>" +
      '<div class="vd-rm" style="color:#ff6b6b;cursor:pointer;padding:4px;font-size:18px;line-height:1;">×</div>';
    row.querySelector(".vd-rm").addEventListener("click", () => removeMember(m.email));
    wrap.appendChild(row);
  });
  const count = document.getElementById("family-count");
  if (count) {
    const n = list.length + 1; // + owner
    count.textContent = uz ? ("Ulashilgan · " + n + " kishi") : ("Shared with " + n + (n === 1 ? " person" : " people"));
    count.removeAttribute("data-i18n");
  }
}

function inviteMember() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  const o = document.createElement("div");
  o.style.cssText = "position:fixed;inset:0;z-index:10002;background:rgba(0,0,0,.6);backdrop-filter:blur(4px);display:flex;align-items:center;justify-content:center;padding:28px;font-family:'Manrope',system-ui;";
  const close = () => o.remove();
  o.addEventListener("click", (e) => { if (e.target === o) close(); });
  o.innerHTML =
    '<div style="width:100%;max-width:330px;background:#16171a;border:1px solid rgba(255,255,255,.08);border-radius:22px;padding:20px;animation:scrIn .26s ease;">' +
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:14px;">' + (uz ? "Oilaga taklif" : "Invite to family") + "</div>" +
    '<input id="vd-inv-email" type="email" inputmode="email" placeholder="email@gmail.com" style="width:100%;height:50px;border-radius:14px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 14px;font-size:14px;outline:none;margin-bottom:12px;font-family:Manrope;">' +
    '<div style="display:flex;gap:8px;margin-bottom:16px;" id="vd-inv-roles">' +
    '<div class="vd-inv-role on" data-role="driver" style="flex:1;text-align:center;padding:11px;border-radius:13px;background:rgba(255,106,26,.16);color:#FF8A3D;font-weight:700;font-size:13px;cursor:pointer;">' + (uz ? "Haydovchi" : "Driver") + "</div>" +
    '<div class="vd-inv-role" data-role="guest" style="flex:1;text-align:center;padding:11px;border-radius:13px;background:rgba(255,255,255,.05);color:#9a9ca2;font-weight:700;font-size:13px;cursor:pointer;">' + (uz ? "Cheklangan" : "Limited") + "</div></div>" +
    '<div id="vd-inv-send" style="height:50px;border-radius:15px;background:linear-gradient(150deg,#FF8A2B,#FF4D00);color:#fff;font-family:\'Sora\';font-weight:700;font-size:15px;display:flex;align-items:center;justify-content:center;cursor:pointer;">' + (uz ? "Taklif yuborish" : "Send invite") + "</div>" +
    '<div id="vd-inv-cancel" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "Bekor qilish" : "Cancel") + "</div></div>";
  document.body.appendChild(o);

  let role = "driver";
  o.querySelectorAll(".vd-inv-role").forEach((el) => el.addEventListener("click", () => {
    role = el.dataset.role;
    o.querySelectorAll(".vd-inv-role").forEach((x) => {
      const on = x === el;
      x.style.background = on ? "rgba(255,106,26,.16)" : "rgba(255,255,255,.05)";
      x.style.color = on ? "#FF8A3D" : "#9a9ca2";
    });
  }));
  o.querySelector("#vd-inv-cancel").addEventListener("click", close);
  o.querySelector("#vd-inv-send").addEventListener("click", async () => {
    const email = (o.querySelector("#vd-inv-email").value || "").trim().toLowerCase();
    if (!email.includes("@")) { toast(uz ? "To‘g‘ri email kiriting" : "Enter a valid email", "err"); return; }
    close();
    await putMember(email, role);
  });
}

async function putMember(email, role) {
  const uz = window.App?.lang === "uz";
  try {
    const token = await auth.currentUser.getIdToken();
    const vid = VMAP[App.activeCar?.id] || "voyah-001";
    const res = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/members`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body: JSON.stringify({ email, role }),
    });
    if (!res.ok) throw new Error("HTTP " + res.status);
    renderMembers((await res.json()).members || []);
    toast((uz ? "Qo‘shildi: " : "Added: ") + email, "ok");
    haptic([10, 40, 12]);
    sendInviteEmail(email, role); // open Gmail/share with a real invite
  } catch (e) {
    toast((uz ? "Xato: " : "Failed: ") + e.message, "err");
  }
}

// sendInviteEmail opens the phone's share sheet (Gmail/Telegram/…) or the email
// composer with a ready-to-send invite — a real message from the owner.
function sendInviteEmail(email, role) {
  const uz = window.App?.lang === "uz";
  const link = location.origin || "https://eldi-79bf9.web.app";
  const roleTxt = role === "driver" ? (uz ? "Haydovchi" : "Driver") : (uz ? "Cheklangan" : "Limited");
  const subject = uz ? "VoltDrive — sizni mashinaga taklif qilishdi" : "VoltDrive — you’ve been invited";
  const body = uz
    ? `Assalomu alaykum!\n\nSizni VoltDrive ilovasida mashinani boshqarishga taklif qilishdi.\nRol: ${roleTxt}\n\nIlovani oching va shu email (${email}) bilan ro‘yxatdan o‘ting:\n${link}\n\n— VoltDrive`
    : `Hello!\n\nYou’ve been invited to control a car in VoltDrive.\nRole: ${roleTxt}\n\nOpen the app and sign in with this email (${email}):\n${link}\n\n— VoltDrive`;
  try {
    if (navigator.share) {
      navigator.share({ title: subject, text: body }).catch(() => {});
    } else {
      window.location.href =
        `mailto:${encodeURIComponent(email)}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
    }
  } catch (e) { /* user cancelled */ }
}

async function removeMember(email) {
  const uz = window.App?.lang === "uz";
  try {
    const token = await auth.currentUser.getIdToken();
    const vid = VMAP[App.activeCar?.id] || "voyah-001";
    const res = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/members?email=${encodeURIComponent(email)}`, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!res.ok) throw new Error("HTTP " + res.status);
    renderMembers((await res.json()).members || []);
    toast((uz ? "O‘chirildi: " : "Removed: ") + email, "ok");
  } catch (e) {
    toast((uz ? "Xato: " : "Failed: ") + e.message, "err");
  }
}

// --- Push notifications (FCM) ---
//
// Requests permission, gets an FCM token via the VAPID key, registers it on
// the backend (Firestore), and shows foreground messages. Security alerts are
// sent by the backend hub to every registered device.

let fcmMessaging = null;

async function enableNotifications() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  if (!CFG.vapidKey) { toast(uz ? "VAPID kaliti sozlanmagan" : "VAPID key not configured", "err"); return; }
  if (!("Notification" in window) || !("serviceWorker" in navigator)) {
    toast(uz ? "Qurilma push'ni qo‘llab-quvvatlamaydi" : "Push not supported here", "err");
    return;
  }
  try {
    const perm = await Notification.requestPermission();
    if (perm !== "granted") {
      toast(uz ? "Bildirishnomaga ruxsat berilmadi" : "Notification permission denied", "err");
      return;
    }
    const reg = await navigator.serviceWorker.register("/firebase-messaging-sw.js");
    fcmMessaging = getMessaging();
    const token = await getToken(fcmMessaging, { vapidKey: CFG.vapidKey, serviceWorkerRegistration: reg });
    if (!token) throw new Error(uz ? "token olinmadi" : "no token");

    const idt = await auth.currentUser.getIdToken();
    const res = await fetch(`${CFG.apiBase}/v1/devices`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${idt}` },
      body: JSON.stringify({ token }),
    });
    if (!res.ok) throw new Error("HTTP " + res.status);

    localStorage.setItem("vd_notif", "1");
    toast(uz ? "Bildirishnomalar yoqildi ✓" : "Notifications enabled ✓", "ok");
    haptic([10, 40, 12]);

    // Foreground messages → in-app toast.
    onMessage(fcmMessaging, (p) => {
      const n = p.notification || {};
      toast((n.title ? n.title + " — " : "") + (n.body || ""), "ok");
      haptic([60, 30, 60]);
      logAlert({ title: n.title, body: n.body, type: p.data && p.data.type });
    });

    // Round-trip test so the user immediately sees it works.
    fetch(`${CFG.apiBase}/v1/devices/test`, {
      method: "POST",
      headers: { Authorization: `Bearer ${idt}` },
    }).catch(() => {});
  } catch (e) {
    toast((uz ? "Xato: " : "Failed: ") + (e?.message || e), "err");
  }
}

// --- Notification history (Alerts screen) ---

const ALERTS_KEY = "vd_alerts";

function loadAlertsStore() {
  try { return JSON.parse(localStorage.getItem(ALERTS_KEY) || "[]"); } catch (e) { return []; }
}
function saveAlertsStore(a) {
  try { localStorage.setItem(ALERTS_KEY, JSON.stringify(a.slice(0, 50))); } catch (e) {}
}

function logAlert(n) {
  const item = { title: n.title || "VoltDrive", body: n.body || "", type: n.type || "info", ts: Date.now() };
  const list = loadAlertsStore();
  list.unshift(item);
  saveAlertsStore(list);
  if (window.App && App.current === "alerts") renderAlerts();
}

function alertIcon(type) {
  if (type === "charge_complete" || type === "charge_full") return ["zap", "#37d67a"];
  if (type === "low_battery") return ["battery-low", "#FF8A3D"];
  if (type === "moved_while_locked" || type === "unlocked") return ["shield-alert", "#ff5252"];
  return ["bell", "#9a9ca2"];
}

function relTime(ts) {
  const uz = window.App?.lang === "uz";
  const s = Math.floor((Date.now() - ts) / 1000);
  if (s < 60) return uz ? "hozir" : "now";
  const m = Math.floor(s / 60); if (m < 60) return m + (uz ? " daq" : "m");
  const h = Math.floor(m / 60); if (h < 24) return h + (uz ? " soat" : "h");
  return Math.floor(h / 24) + (uz ? " kun" : "d");
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

function renderAlerts() {
  const wrap = document.getElementById("alerts-list");
  if (!wrap) return;
  const list = loadAlertsStore();
  const empty = document.getElementById("alerts-empty");
  [...wrap.querySelectorAll("[data-alert]")].forEach((e) => e.remove());
  if (!list.length) { if (empty) empty.style.display = ""; return; }
  if (empty) empty.style.display = "none";
  list.forEach((n) => {
    const [ic, col] = alertIcon(n.type);
    const row = document.createElement("div");
    row.dataset.alert = "1";
    row.style.cssText = "display:flex;gap:12px;padding:13px 14px;border-radius:18px;background:rgba(255,255,255,.035);border:1px solid rgba(255,255,255,.055);margin-bottom:9px;";
    row.innerHTML =
      '<div style="width:38px;height:38px;border-radius:12px;background:rgba(255,255,255,.06);display:flex;align-items:center;justify-content:center;flex-shrink:0;color:' + col + ';"><i data-lucide="' + ic + '" style="width:18px;height:18px"></i></div>' +
      '<div style="flex:1;min-width:0"><div style="display:flex;justify-content:space-between;gap:8px;"><span style="color:#fff;font-weight:600;font-size:13px;">' + escapeHtml(n.title) + '</span><span style="color:#6a6c72;font-size:11px;flex-shrink:0;">' + relTime(n.ts) + '</span></div>' +
      '<div style="color:#9a9ca2;font-size:12px;margin-top:2px;">' + escapeHtml(n.body) + "</div></div>";
    wrap.appendChild(row);
  });
  if (window.lucide) lucide.createIcons();
}

function clearAlerts() {
  saveAlertsStore([]);
  renderAlerts();
  toast(window.App?.lang === "uz" ? "Tozalandi" : "Cleared");
}

// Capture background pushes forwarded by firebase-messaging-sw.js.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.addEventListener("message", (e) => {
    if (e.data && e.data.type === "fcm" && e.data.payload) {
      const p = e.data.payload, n = p.notification || {};
      logAlert({ title: n.title, body: n.body, type: p.data && p.data.type });
    }
  });
}

// --- Warm-up prompt (shown right after the PIN unlock) ---
//
// Winter convenience: one tap to remote-start the car and turn the heater on
// so it warms up before you leave. Skippable; never blocks entry to the app.

function warmupPrompt() {
  return new Promise((resolve) => {
    if (!CFG.apiBase) { resolve(); return; } // demo mode: nothing to start
    const uz = window.App?.lang === "uz";
    let on = !!(window.App && App.engineOn); // reflect the current engine state
    let idleT = null;

    const o = document.createElement("div");
    o.id = "vd-warmup";
    o.style.cssText =
      "position:fixed;inset:0;z-index:10001;background:radial-gradient(120% 80% at 50% -10%,#26272b 0%,#141518 55%,#0b0c0e 100%);" +
      "display:flex;flex-direction:column;align-items:center;justify-content:center;gap:16px;padding:34px;font-family:'Manrope',system-ui;" +
      "animation:scrIn .3s ease;";
    o.innerHTML =
      '<div style="font-family:\'Sora\';font-weight:800;font-size:22px;color:#fff;text-align:center;">' +
      (uz ? "Yo‘lga tayyormisiz?" : "Ready to go?") + "</div>" +
      '<div style="color:#9a9ca2;font-size:13px;text-align:center;max-width:250px;line-height:1.45;">' +
      (uz ? "Chiqishdan oldin mashinani qizdirib oling. Tugma dvigatelni yoqadi va o‘chiradi." :
            "Warm the car up before you leave. The button starts and stops the engine.") + "</div>" +
      '<div id="vd-warmup-btn" style="width:132px;height:132px;border-radius:50%;margin-top:8px;cursor:pointer;' +
      'display:flex;flex-direction:column;align-items:center;justify-content:center;gap:6px;transition:all .25s;">' +
      '<svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/></svg>' +
      '<span id="vd-warmup-lbl" style="font-family:\'Sora\';font-weight:700;font-size:12px;"></span></div>' +
      '<div id="vd-warmup-status" style="font-size:12px;font-weight:600;letter-spacing:.3px;min-height:16px;"></div>' +
      '<div id="vd-warmup-enter" style="margin-top:14px;height:50px;padding:0 30px;border-radius:15px;' +
      'background:linear-gradient(150deg,#FF8A2B,#FF4D00);color:#fff;font-family:\'Sora\';font-weight:700;font-size:15px;' +
      'display:flex;align-items:center;justify-content:center;gap:8px;cursor:pointer;box-shadow:0 14px 28px -8px rgba(255,90,0,.6);">' +
      (uz ? "Ilovaga kirish" : "Enter app") + "</div>";
    document.body.appendChild(o);

    const btn = o.querySelector("#vd-warmup-btn");
    const lbl = o.querySelector("#vd-warmup-lbl");
    const status = o.querySelector("#vd-warmup-status");

    const paint = () => {
      btn.style.background = on
        ? "radial-gradient(circle at 50% 35%,#FF8A2B,#FF4D00)"
        : "radial-gradient(circle at 50% 35%,#222326,#17181b)";
      btn.style.boxShadow = on
        ? "0 0 0 10px rgba(255,90,0,.12),0 18px 40px -8px rgba(255,90,0,.7)"
        : "0 0 0 10px rgba(255,255,255,.04),0 18px 40px -8px rgba(0,0,0,.6)";
      btn.style.color = on ? "#fff" : "#5a5c62";
      lbl.textContent = on ? (uz ? "O‘chirish" : "Turn off") : (uz ? "Qizdirish" : "Warm up");
      status.textContent = on
        ? (uz ? "Dvigatel ishlayapti · isitish 24°" : "Engine running · heat 24°")
        : (uz ? "Mashina o‘chiq" : "Engine off");
      status.style.color = on ? "#FF8A3D" : "#9a9ca2";
    };
    paint();

    const cancelIdle = () => { if (idleT) { clearTimeout(idleT); idleT = null; } };

    // Live cabin-temperature feedback while warming (polls the snapshot).
    let tempT = null;
    const vid = () => VMAP[App.activeCar?.id] || "voyah-001";
    const refreshTemp = async () => {
      try {
        const token = await auth.currentUser.getIdToken();
        const r = await fetch(`${CFG.apiBase}/v1/vehicles/${vid()}`, { headers: { Authorization: `Bearer ${token}` } });
        if (!r.ok) return;
        const s = await r.json();
        if (on && s.climate && typeof s.climate.insideC === "number") {
          status.textContent = (uz ? "Salon " : "Cabin ") + Math.round(s.climate.insideC) + "° → 24°";
        }
      } catch (e) {}
    };
    const startTemp = () => { stopTemp(); refreshTemp(); tempT = setInterval(refreshTemp, 3000); };
    const stopTemp = () => { if (tempT) { clearInterval(tempT); tempT = null; } };

    const finish = () => { cancelIdle(); stopTemp(); o.remove(); resolve(); };

    // Power button: toggles the engine. Does NOT leave this screen, so you can
    // start the heater, watch it warm, then turn it off or enter the app.
    btn.addEventListener("click", () => {
      cancelIdle(); // engaging — never auto-jump away mid-use
      on = !on;
      haptic(on ? [12, 40, 12] : 18);
      if (window.App) { App.engineOn = on; setEngineUI(on); }
      sendCommand(on ? "start" : "stop");
      if (on) sendCommand("climate", { on: true, targetC: 24 });
      paint();
      if (on) startTemp(); else stopTemp();
    });

    o.querySelector("#vd-warmup-enter").addEventListener("click", finish);
    if (on) startTemp(); // engine already running when the screen opened
    // Auto-enter only if the screen is completely untouched.
    idleT = setTimeout(finish, 15000);
  });
}

// changePin re-runs the PIN setup so the user can set a new 4-digit PIN.
// Wired to the profile "Security & PIN" row.
function changePin() {
  const uz = window.App?.lang === "uz";
  const u = auth?.currentUser;
  if (!u) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  buildLockOverlay();
  lockState = {
    uid: u.uid,
    key: "vd_pin_" + u.uid,
    resolve: (ok) => { if (ok) toast(uz ? "PIN yangilandi ✓" : "PIN updated ✓", "ok"); },
    mode: "setup", stage: "first", first: "", entry: "",
  };
  setLockTitle(uz ? "Yangi PIN yarating" : "Create a new PIN");
  updateDots();
  showLock();
}

// --- PIN lock (security gate on every app open) ---
//
// After the first registration the user sets a 4-digit PIN. On every launch,
// while the email session stays signed in (Firebase persists it), the app is
// locked behind the PIN — so the phone owner re-verifies identity without
// re-typing the email/password. "Sign out" lives in the profile.

async function sha256(text) {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function lockGate(uid) {
  const uz = window.App?.lang === "uz";
  const key = "vd_pin_" + uid;
  const stored = localStorage.getItem(key);
  buildLockOverlay();
  return new Promise((resolve) => {
    lockState = {
      uid, key, resolve,
      mode: stored ? "unlock" : "setup",
      stage: "first", first: "", entry: "",
    };
    setLockTitle(stored ? (uz ? "PIN kodni kiriting" : "Enter your PIN")
                        : (uz ? "Yangi PIN yarating" : "Create a 4-digit PIN"));
    updateDots();
    showLock();
  });
}

function buildLockOverlay() {
  if (document.getElementById("vd-lock")) return;
  const uz = window.App?.lang === "uz";
  const o = document.createElement("div");
  o.id = "vd-lock";
  o.style.cssText =
    "position:fixed;inset:0;z-index:10000;background:radial-gradient(120% 80% at 50% -10%,#26272b 0%,#141518 55%,#0b0c0e 100%);" +
    "display:none;flex-direction:column;align-items:center;justify-content:center;gap:22px;padding:30px;font-family:'Manrope',system-ui;";
  o.innerHTML =
    '<div style="width:60px;height:60px;border-radius:18px;background:linear-gradient(150deg,#FF8A2B,#FF4D00);display:flex;align-items:center;justify-content:center;box-shadow:0 12px 26px -8px rgba(255,90,0,.6);">' +
    '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg></div>' +
    '<div id="vd-lock-title" style="font-family:\'Sora\';font-weight:800;font-size:21px;color:#fff;text-align:center;"></div>' +
    '<div id="vd-lock-dots" style="display:flex;gap:16px;margin:4px 0;"></div>' +
    '<div id="vd-lock-pad" style="display:grid;grid-template-columns:repeat(3,72px);gap:18px;"></div>' +
    '<div id="vd-lock-out" style="color:#FF8A3D;font-size:13px;font-weight:700;cursor:pointer;margin-top:8px;">' +
    (uz ? "Boshqa akkaunt / Chiqish" : "Use another account / Sign out") + "</div>";
  document.body.appendChild(o);

  // Number pad.
  const pad = o.querySelector("#vd-lock-pad");
  const keys = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "", "0", "⌫"];
  keys.forEach((k) => {
    const btn = document.createElement("div");
    if (k === "") { pad.appendChild(btn); return; }
    btn.textContent = k;
    btn.style.cssText =
      "width:72px;height:72px;border-radius:50%;display:flex;align-items:center;justify-content:center;" +
      "font-family:'Sora';font-weight:700;font-size:26px;color:#fff;cursor:pointer;user-select:none;" +
      (k === "⌫" ? "background:transparent;font-size:22px;color:#9a9ca2;" : "background:rgba(255,255,255,.06);");
    btn.addEventListener("click", () => onPinKey(k));
    pad.appendChild(btn);
  });

  // Sign out from lock screen.
  o.querySelector("#vd-lock-out").addEventListener("click", async () => {
    try { if (unsubscribe) { unsubscribe(); unsubscribe = null; } await signOut(auth); } catch (_) {}
    hideLock();
    if (lockState?.resolve) lockState.resolve(false);
    lockState = null;
    if (window.App) App.go("auth");
  });

  // Shake keyframes.
  const st = document.createElement("style");
  st.textContent = "@keyframes vdshake{0%,100%{transform:translateX(0)}20%,60%{transform:translateX(-9px)}40%,80%{transform:translateX(9px)}}";
  document.head.appendChild(st);
}

function setLockTitle(t) {
  const el = document.getElementById("vd-lock-title");
  if (el) el.textContent = t;
}

function updateDots() {
  const wrap = document.getElementById("vd-lock-dots");
  if (!wrap || !lockState) return;
  wrap.innerHTML = "";
  for (let i = 0; i < 4; i++) {
    const d = document.createElement("div");
    const filled = i < lockState.entry.length;
    d.style.cssText =
      "width:15px;height:15px;border-radius:50%;transition:all .15s;" +
      (filled ? "background:#FF6A1A;" : "background:transparent;border:2px solid #4a4c52;");
    wrap.appendChild(d);
  }
}

async function onPinKey(k) {
  if (!lockState) return;
  const uz = window.App?.lang === "uz";
  if (k === "⌫") {
    lockState.entry = lockState.entry.slice(0, -1);
    updateDots();
    return;
  }
  if (lockState.entry.length >= 4) return;
  lockState.entry += k;
  updateDots();
  if (lockState.entry.length < 4) return;

  // 4 digits entered — process.
  const pin = lockState.entry;
  if (lockState.mode === "setup") {
    if (lockState.stage === "first") {
      lockState.first = pin;
      lockState.entry = "";
      lockState.stage = "confirm";
      setLockTitle(uz ? "PIN ni takrorlang" : "Confirm your PIN");
      updateDots();
    } else {
      if (pin === lockState.first) {
        localStorage.setItem(lockState.key, await sha256(lockState.uid + ":" + pin));
        finishUnlock();
      } else {
        shakeLock();
        lockState.first = "";
        lockState.entry = "";
        lockState.stage = "first";
        setLockTitle(uz ? "Mos kelmadi. Qaytadan" : "Didn't match. Try again");
        updateDots();
      }
    }
  } else {
    const ok = (await sha256(lockState.uid + ":" + pin)) === localStorage.getItem(lockState.key);
    if (ok) {
      finishUnlock();
    } else {
      shakeLock();
      lockState.entry = "";
      setLockTitle(uz ? "Noto'g'ri PIN" : "Wrong PIN");
      updateDots();
    }
  }
}

function shakeLock() {
  const o = document.getElementById("vd-lock");
  if (!o) return;
  o.style.animation = "vdshake .4s";
  setTimeout(() => (o.style.animation = ""), 420);
}

function finishUnlock() {
  hideLock();
  const r = lockState?.resolve;
  lockState = null;
  if (r) r(true);
}

function showLock() {
  const o = document.getElementById("vd-lock");
  if (o) o.style.display = "flex";
}

function hideLock() {
  const o = document.getElementById("vd-lock");
  if (o) o.style.display = "none";
}
