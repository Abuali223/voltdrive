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
  sendPasswordResetEmail,
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
// Dependency-free logic shared with the unit tests (see lib/voltdrive-core.js).
import { translate, pinSerialize, pinVerify, isLegacyPin } from "./lib/voltdrive-core.js";

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
let lastRangeKm = 0; // latest live driving range, for the route reach calculator
let poiMarkers = [];
let destPoint = null; // current destination {lat,lng,name}
let routeStops = []; // intermediate stops (charging/fuel) [{lat,lng,name}]
let stopMarkers = []; // leaflet markers for the intermediate stops
let lastBattery = null; // latest live battery %
let assistantHistory = []; // AI assistant conversation [{role,text}]

if (!CFG.firebase) {
  console.info("VoltDrive: live mode OFF (no config) — running in demo mode.");
  window.App?.hideSplash?.(); // no auth to resolve — reveal the (demo) UI now
} else {
  boot();
}

// App-shell features that work regardless of Firebase config.
setupPullToRefresh();
setupInstallPrompt();
wireExtras();
setupA11y();
applyBranding();
loadRemoteBranding(); // pull super-admin-set white-label theme (if any)

// Fetch the saved white-label theme from the backend and re-apply it, so a
// dealer's branding (set in the admin panel) themes the app for everyone.
async function loadRemoteBranding() {
  if (!CFG.apiBase) return;
  try {
    const r = await fetch(`${CFG.apiBase}/v1/branding`);
    if (!r.ok) return;
    const b = await r.json();
    const clean = Object.fromEntries(Object.entries(b || {}).filter(([, v]) => v !== "" && v != null));
    if (Object.keys(clean).length) {
      CFG.branding = { ...(CFG.branding || {}), ...clean };
      applyBranding();
    }
  } catch (e) {}
}

// setupA11y makes the div-based controls keyboard- and screen-reader-friendly:
// it tags clickable elements with role="button" + tabindex and lets Enter/Space
// activate the focused one. Runs once for the static screens and re-tags any
// dynamically created controls (modals, member rows) via a debounced observer.
function setupA11y() {
  const SEL = ".qbtn,.ibtn,.row,.obtn,.ctrl-btn,.gi,.map-chip,.seat-tab,.prow,.lpill,.ni,.ctab,#power-btn,[onclick]";
  const enhance = () => {
    document.querySelectorAll(SEL).forEach((el) => {
      if (el.dataset.a11y) return;
      const tag = el.tagName;
      if (tag === "BUTTON" || tag === "A" || tag === "INPUT") { el.dataset.a11y = "1"; return; }
      el.dataset.a11y = "1";
      if (!el.hasAttribute("role")) el.setAttribute("role", "button");
      if (!el.hasAttribute("tabindex")) el.setAttribute("tabindex", "0");
    });
  };
  enhance();
  let timer;
  new MutationObserver(() => { clearTimeout(timer); timer = setTimeout(enhance, 200); })
    .observe(document.body, { childList: true, subtree: true });
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    const el = document.activeElement;
    if (el && el.getAttribute && el.getAttribute("role") === "button") {
      e.preventDefault();
      el.click();
    }
  });
}

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
          aiFab(true);
          try { checkRemindersDue(); } catch (e) {}
          // Home-screen shortcut actions (PWA manifest shortcuts).
          try {
            const act = new URLSearchParams(location.search).get("action");
            if (act) {
              setTimeout(() => {
                if (act === "lock") sendCommand("lock", null);
                else if (act === "unlock") sendCommand("unlock", null);
                else if (act === "ai") openAssistant();
                else if (act === "sos") sos();
                history.replaceState(null, "", location.pathname);
              }, 900);
            }
          } catch (e) {}
          try { subscribe(); } catch (e) { console.warn("subscribe:", e); }
          refreshSubscription(); // load the user's plan for the profile + gating
          checkAdminAccess();    // show the Admin panel row for admins
          // Security gate: ask for a PIN before entering (set up first time).
          await lockGate(user.uid);
          // Winter convenience: offer a one-tap warm-up before entering.
          await warmupPrompt();
          // Enter home as the navigation root, then reveal (no auth flash, and
          // Back can never return to the sign-in screen while signed in).
          if (window.App) { App.resetRoot("home"); App.hideSplash(); }
        } else {
          hideUser();
          hideLock();
          aiFab(false);
          // Signed out → auth screen is the root; reveal it.
          if (window.App) { App.resetRoot("auth"); App.hideSplash(); }
        }
      } catch (e) {
        console.error("authState error:", e);
        authStatus((uzNow() ? "Ichki xato: " : "Internal error: ") + (e?.message || e), true);
        // Don't trap the user on the lock screen if PIN UI failed: go home.
        if (user && window.App) { hideLock(); App.resetRoot("home"); }
        if (window.App) App.hideSplash();
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
  const forgotEl = document.getElementById("auth-forgot");
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
    if (forgotEl) {
      forgotEl.style.display = m === "signin" ? "block" : "none";
      forgotEl.textContent = uz() ? "Parolni unutdingizmi?" : "Forgot password?";
    }
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

  // Forgot password → send a Firebase reset email to the typed address.
  if (forgotEl) {
    forgotEl.addEventListener("click", async () => {
      const email = (emailEl?.value || "").trim();
      if (!email) return authStatus(uz() ? "Avval emailni kiriting" : "Enter your email first", true);
      authStatus(uz() ? "Yuborilmoqda…" : "Sending…");
      try {
        await sendPasswordResetEmail(auth, email);
        authStatus(uz() ? "Tiklash havolasi emailga yuborildi ✓" : "Reset link sent to your email ✓");
      } catch (e) {
        authStatus(explain(e), true);
      }
    });
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
// Transient toast / auth-status text is localised by lib/voltdrive-core.js.
// For RU users the inline `uz ? … : …` ternaries already resolve to English,
// so translate() maps that to Russian centrally (see the unit tests).
function ruMsg(msg) { return translate(window.App?.lang, msg); }

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
    el.textContent = ruMsg(msg) || "";
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
      if (window.App) App.resetRoot("auth");
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
    lastRangeKm = e.rangeKm;
  }
  if (hasBatt) {
    setHTML("cs-battery", `${e.batteryLevel}<span style="font-size:12px;color:#7e8086">%</span>`);
    lastBattery = e.batteryLevel;
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

  // --- Teen / young-driver mode: speed + curfew monitoring ---
  try { checkTeenMode(v); } catch (_) {}

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
  App.toggleBiometric = toggleBiometric;
  // Family sharing → invite a member by email.
  App.inviteMember = inviteMember;
  // Notifications (profile row) → enable push.
  App.enableNotifications = enableNotifications;
  // Alerts screen → clear notification history.
  App.clearAlerts = clearAlerts;
  // Family screen → switch which car's member list is shown.
  App.pickFamilyCar = pickFamilyCar;
  // Map → flash lights + honk to locate the car.
  App.findCar = findCar;
  // Home → departure timer (server-side daily warm-up).
  App.openSchedule = openSchedule;
  // Security: guest keys, geofence, panic; guest redeem entry.
  App.openGuestKeys = openGuestKeys;
  App.openGuestEntry = openGuestEntry;
  App.openGeofence = openGeofence;
  App.panic = panic;
  App.sos = sos;
  App.parking = openParking;
  App.openReminders = openReminders;
  App.shareLocation = shareLocation;
  App.openReferral = openReferral;
  App.openAutomation = openAutomation;
  App.openTrips = openTrips;
  App.openTripPlanner = openTripPlanner;
  App.openTeenMode = openTeenMode;
  App.openInsights = openInsights;
  App.openImmobilizer = openImmobilizer;
  // Premium / subscription + Fleet dashboard.
  App.openPremium = openPremium;
  App.openFleet = openFleet;
  App.openAssistant = openAssistant;
  App.startTrial = startTrial;
  App.requestTier = requestTier;
  App.copyCard = copyCard;
  App.loadFleet = loadFleet;
  App.fleetAll = fleetAll;
  App.fleetAdd = fleetAdd;
  App.fleetRemove = fleetRemove;
  App.openAdmin = () => { window.location.href = "admin.html"; };

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
  t.textContent = ruMsg(msg);
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

  // Sliders: tap or drag to set a 0-100% value. Pointer capture keeps the drag
  // tracking even when the finger leaves the bar, and touch-action:none stops
  // the page from scrolling mid-drag — together they make the drag feel smooth.
  document.querySelectorAll(".vd-slider").forEach((sl) => {
    sl.style.touchAction = "none";
    const fill = sl.querySelector(".vd-fill");
    const val = sl.parentElement.querySelector(".vd-sval");
    const apply = (clientX) => {
      const rect = sl.getBoundingClientRect();
      const raw = Math.max(0, Math.min(100, ((clientX - rect.left) / rect.width) * 100));
      if (fill) fill.style.width = raw + "%";          // precise → buttery fill
      if (val) val.textContent = Math.round(raw) + "%"; // integer label
    };
    let down = false;
    sl.addEventListener("pointerdown", (e) => {
      down = true;
      try { sl.setPointerCapture(e.pointerId); } catch (_) {}
      apply(e.clientX);
      e.preventDefault();
    });
    sl.addEventListener("pointermove", (e) => { if (down) { apply(e.clientX); e.preventDefault(); } });
    sl.addEventListener("pointerup", (e) => { down = false; try { sl.releasePointerCapture(e.pointerId); } catch (_) {} });
    sl.addEventListener("pointercancel", () => { down = false; });
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

  // Temperature dial — drag around the ring (16–30°). Pointer capture +
  // touch-action:none keep it smooth, and a wrap-guard stops the value from
  // flipping min↔max when the finger crosses 12 o'clock.
  const ring = document.getElementById("climate-ring");
  if (ring) {
    ring.style.touchAction = "none";
    ring.style.cursor = "pointer";
    let dragging = false;
    const fromPointer = (clientX, clientY) => {
      const r = ring.getBoundingClientRect();
      const cx = r.left + r.width / 2, cy = r.top + r.height / 2;
      let deg = Math.atan2(clientX - cx, -(clientY - cy)) * 180 / Math.PI; // 0 = top, clockwise
      if (deg < 0) deg += 360;
      let t = Math.max(16, Math.min(30, Math.round(16 + (deg / 360) * 14)));
      if (dragging && Math.abs(t - climateTemp) > 7) return; // ignore boundary wrap
      if (t !== climateTemp) { climateTemp = t; climateChanged(); }
    };
    ring.addEventListener("pointerdown", (e) => {
      try { ring.setPointerCapture(e.pointerId); } catch (_) {}
      fromPointer(e.clientX, e.clientY); // dragging still false → tap jumps freely
      dragging = true;
      e.preventDefault();
    });
    ring.addEventListener("pointermove", (e) => { if (dragging) { fromPointer(e.clientX, e.clientY); e.preventDefault(); } });
    ring.addEventListener("pointerup", (e) => { dragging = false; try { ring.releasePointerCapture(e.pointerId); } catch (_) {} });
    ring.addEventListener("pointercancel", () => { dragging = false; });
  }

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
      // 3D seat viewer: load the (heavy) model-viewer module on demand.
      if (s === "seat") ensureModelViewer();
    };
  }
  if (App.current === "map") ensureMap();
  if (App.current === "seat") ensureModelViewer();

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
  const cancelBtn = document.getElementById("map-cancel-btn");
  if (cancelBtn) cancelBtn.addEventListener("click", () => {
    clearRoute();
    toast(window.App?.lang === "uz" ? "Bekor qilindi" : (window.App?.lang === "ru" ? "Отменено" : "Cancelled"));
  });

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

// ensureModelViewer injects the <model-viewer> module the first time the Seat
// screen is opened, keeping ~1MB off the initial page load.
let modelViewerLoading = false;
function ensureModelViewer() {
  if (modelViewerLoading || customElements.get("model-viewer")) return;
  modelViewerLoading = true;
  const s = document.createElement("script");
  s.type = "module";
  s.src = "https://unpkg.com/@google/model-viewer@3.5.0/dist/model-viewer.min.js";
  document.head.appendChild(s);
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
let geoDeniedShown = false; // show the "permission denied" toast at most once
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
      // Permission denied: watchPosition would otherwise keep firing this
      // callback, leaving the toast stuck on screen. Stop the watch and show
      // the message only once per session.
      if (err.code === 1) {
        if (watchId !== null) { navigator.geolocation.clearWatch(watchId); watchId = null; }
        if (!geoDeniedShown) {
          geoDeniedShown = true;
          toast((uz ? "Joylashuv: " : "Location: ") + (uz ? "ruxsat berilmadi" : "permission denied"));
        }
        return;
      }
      const m = err.code === 2 ? (uz ? "GPS o'chiq / signal yo'q" : "GPS off / no signal")
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
    `<div class="vd-res" data-i="${i}" style="padding:12px 14px;border-bottom:1px solid rgba(255,255,255,.06);color:#fff;font-size:13px;cursor:pointer;">${escapeHtml((it.display_name || "").split(",").slice(0, 3).join(","))}</div>`
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
  destPoint = { lat: +lat, lng: +lng, name };
  routeStops = []; // a new destination clears any intermediate stops
  clearStopMarkers();
  destMarker = L.marker([lat, lng], {
    icon: L.divIcon({
      className: "",
      html: '<div style="width:18px;height:18px;border-radius:50% 50% 50% 0;transform:rotate(-45deg);background:linear-gradient(150deg,#FF8A2B,#FF4D00);border:2px solid #fff;box-shadow:0 4px 10px rgba(0,0,0,.5)"></div>',
      iconSize: [18, 18], iconAnchor: [9, 18],
    }),
  }).addTo(leafletMap);
  set("map-name", name);
  rebuildRoute();
}

// Pick a charging/fuel station from the map. With no destination yet the
// station becomes the destination; if a destination is already set, it is
// inserted as an extra stop (waypoint) along the way.
function addStop(lat, lng, name) {
  const uz = window.App?.lang === "uz";
  lat = +lat; lng = +lng;
  if (!destPoint) { setDestination(lat, lng, name); return; }
  if (routeStops.some((s) => Math.abs(s.lat - lat) < 1e-6 && Math.abs(s.lng - lng) < 1e-6)) return;
  if (routeStops.length >= 3) { toast(uz ? "Ko'pi bilan 3 ta to'xtash" : "Up to 3 stops"); return; }
  routeStops.push({ lat, lng, name });
  addStopMarker(lat, lng, name, routeStops.length);
  rebuildRoute();
  toast(uz ? `Qo'shimcha to'xtash · ${name}` : `Stop added · ${name}`, "ok");
}

function addStopMarker(lat, lng, name, idx) {
  const m = L.marker([lat, lng], {
    icon: L.divIcon({
      className: "",
      html: `<div style="width:22px;height:22px;border-radius:50%;background:#15171b;border:2px solid #FF8A2B;color:#FF8A2B;font:700 11px 'Sora',sans-serif;display:flex;align-items:center;justify-content:center;box-shadow:0 3px 8px rgba(0,0,0,.5)">${idx}</div>`,
      iconSize: [22, 22], iconAnchor: [11, 11],
    }),
  }).addTo(leafletMap);
  m.bindPopup(escapeHtml(name));
  stopMarkers.push(m);
}

function clearStopMarkers() {
  stopMarkers.forEach((m) => leafletMap && leafletMap.removeLayer(m));
  stopMarkers = [];
}

// Fastest driving route via OSRM: current location → stops → destination.
async function rebuildRoute() {
  if (!destPoint) return;
  const from = deviceLoc || lastLoc || { lat: 41.311081, lng: 69.240562 };
  const uz = window.App?.lang === "uz";
  toast(uz ? "Yo'l qurilmoqda…" : "Routing…");
  try {
    const pts = [from, ...routeStops, destPoint];
    const coords = pts.map((p) => `${p.lng},${p.lat}`).join(";");
    const url = `https://router.project-osrm.org/route/v1/driving/${coords}?overview=full&geometries=geojson`;
    const d = await (await fetch(url)).json();
    if (!d.routes || !d.routes.length) { toast(uz ? "Yo'l topilmadi" : "No route"); return; }
    const rt = d.routes[0];
    if (routeLine) leafletMap.removeLayer(routeLine);
    routeLine = L.geoJSON(rt.geometry, { style: { color: "#FF6A1A", weight: 6, opacity: 0.9 } }).addTo(leafletMap);
    leafletMap.fitBounds(routeLine.getBounds(), { padding: [60, 60] });
    const kmNum = rt.distance / 1000;
    const km = kmNum.toFixed(1), min = Math.round(rt.duration / 60);
    set("map-dist", km + " km");
    const stopTxt = routeStops.length ? ` · ${routeStops.length} ${uz ? "to'xtash" : "stop"}` : "";
    set("map-eta", `${km} km · ${min} ${uz ? "daqiqa" : "min"}${stopTxt}`, true);
    showReach(kmNum);
    showCancelBtn(true);
  } catch (_) { toast(uz ? "Yo'l xatosi" : "Route error"); }
}

// Shows/hides the "Cancel route" button on the map card.
function showCancelBtn(show) {
  const b = document.getElementById("map-cancel-btn");
  if (!b) return;
  if (show) {
    const lang = window.App?.lang;
    const t = document.getElementById("map-cancel-txt");
    if (t) t.textContent = lang === "ru" ? "Отменить" : (lang === "en" ? "Cancel" : "Bekor qilish");
    b.style.display = "flex";
  } else {
    b.style.display = "none";
  }
}

// showReach compares the route distance with the live driving range and tells
// the driver whether the charge will get them there (with a 15% safety buffer).
function showReach(km) {
  const uz = window.App?.lang === "uz";
  const el = document.getElementById("map-stations");
  if (!lastRangeKm) {
    if (el) { el.textContent = ruMsg(uz ? "Zaryad ma'lumoti yo'q" : "Range unknown"); el.removeAttribute("data-i18n"); }
    return;
  }
  const after = Math.round(lastRangeKm - km);
  const ok = lastRangeKm >= km * 1.15; // keep a 15% reserve
  const msg = ok
    ? (uz ? `✓ Yetadi · ~${after} km zaxira qoladi` : `✓ Reachable · ~${after} km left`)
    : (lastRangeKm >= km
        ? (uz ? `⚠️ Zo'rg'a yetadi · faqat ~${after} km qoladi` : `⚠️ Tight · only ~${after} km spare`)
        : (uz ? `✕ Yetmaydi · ~${Math.abs(after)} km kam` : `✕ Won't reach · ~${Math.abs(after)} km short`));
  if (el) {
    el.textContent = ruMsg(msg);
    el.removeAttribute("data-i18n");
    el.style.color = ok ? "#43d684" : (lastRangeKm >= km ? "#FF8A3D" : "#ff5252");
  }
  toast(msg, ok ? "ok" : "err");
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
      m.bindPopup(
        `<div style="text-align:center;font-family:'Manrope',system-ui;min-width:132px;">` +
        `<div style="font-weight:700;margin-bottom:8px;font-size:13px;">${escapeHtml(it.name)} · ${it.d.toFixed(1)} km</div>` +
        `<button class="vd-go" style="width:100%;border:0;border-radius:9px;padding:8px 10px;background:linear-gradient(135deg,#FF8A2B,#FF4D00);color:#fff;font-weight:700;font-size:12px;cursor:pointer;"></button>` +
        `</div>`
      );
      m.on("popupopen", (e) => {
        const root = e.popup.getElement();
        const btn = root && root.querySelector(".vd-go");
        if (!btn) return;
        btn.textContent = destPoint
          ? (uz ? "+ Qo'shimcha to'xtash" : "+ Add stop")
          : (uz ? "▶ Yo'l ko'rsat" : "▶ Route");
        btn.onclick = () => { addStop(it.lat, it.lng, it.name); leafletMap.closePopup(); };
      });
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

// ===== AI assistant (Gemini-backed chat that can control the car) =====
function aiFab(show) {
  const f = document.getElementById("ai-fab");
  if (f) f.style.display = show ? "flex" : "none";
}

// Snapshot of the active car for the assistant's context.
function carContext() {
  const c = (window.App && App.activeCar) || {};
  return {
    name: c.name || "VoltDrive",
    locked: App?.locked === true,
    engineOn: App?.engineOn === true,
    batteryPct: lastBattery,
    rangeKm: lastRangeKm || null,
    location: lastLoc ? { lat: +lastLoc.lat.toFixed(4), lng: +lastLoc.lng.toFixed(4) } : null,
  };
}

function aiBubble(role, text) {
  const box = document.getElementById("ai-msgs");
  if (!box) return null;
  const me = role === "user";
  const b = document.createElement("div");
  b.style.cssText = "max-width:82%;padding:10px 13px;border-radius:15px;font-size:14px;line-height:1.45;white-space:pre-wrap;word-break:break-word;" +
    (me ? "align-self:flex-end;background:linear-gradient(135deg,#FF8A2B,#FF4D00);color:#fff;border-bottom-right-radius:5px;"
        : "align-self:flex-start;background:rgba(255,255,255,.06);color:#e8e9ec;border-bottom-left-radius:5px;");
  b.textContent = text;
  box.appendChild(b);
  box.scrollTop = box.scrollHeight;
  return b;
}

function openAssistant() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  if (document.getElementById("ai-sheet")) return;
  const o = document.createElement("div");
  o.id = "ai-sheet";
  o.style.cssText = "position:fixed;inset:0;z-index:10050;background:rgba(0,0,0,.55);backdrop-filter:blur(4px);display:flex;flex-direction:column;justify-content:flex-end;font-family:'Manrope',system-ui;";
  o.addEventListener("click", (e) => { if (e.target === o) o.remove(); });
  o.innerHTML =
    '<div style="background:#121317;border:1px solid rgba(255,255,255,.08);border-radius:24px 24px 0 0;max-height:84vh;display:flex;flex-direction:column;animation:scrIn .26s ease;">' +
      '<div style="display:flex;align-items:center;gap:11px;padding:16px 18px;border-bottom:1px solid rgba(255,255,255,.06);">' +
        '<div style="width:34px;height:34px;border-radius:11px;background:linear-gradient(135deg,#FF8A2B,#FF4D00);display:flex;align-items:center;justify-content:center;"><i data-lucide="sparkles" style="width:18px;height:18px;color:#fff"></i></div>' +
        '<div style="flex:1;"><div style="font-family:\'Sora\';font-weight:800;font-size:16px;color:#fff;">AI yordamchi</div><div style="font-size:11px;color:#7e8086;">' + (uz ? "Mashina bilan gaplashing" : "Talk to your car") + '</div></div>' +
        '<div id="ai-close" style="color:#9a9ca2;font-size:24px;line-height:1;cursor:pointer;padding:4px 6px;">×</div>' +
      '</div>' +
      '<div style="padding:9px 14px 0;display:flex;gap:8px;flex-wrap:wrap;">' +
        '<div id="ai-diag" style="display:inline-flex;align-items:center;gap:6px;padding:7px 12px;border-radius:11px;background:rgba(255,138,43,.14);border:1px solid rgba(255,138,43,.3);color:#FF9D5C;font-size:12px;font-weight:700;cursor:pointer;"><i data-lucide="stethoscope" style="width:14px;height:14px"></i>' + (uz ? "Diagnostika" : "Diagnose") + '</div>' +
      '</div>' +
      '<div id="ai-msgs" style="flex:1;overflow-y:auto;padding:16px;display:flex;flex-direction:column;gap:10px;min-height:220px;"></div>' +
      '<div style="padding:12px 14px;border-top:1px solid rgba(255,255,255,.06);display:flex;gap:9px;align-items:center;padding-bottom:max(12px,env(safe-area-inset-bottom));">' +
        '<input id="ai-input" placeholder="' + (uz ? "Yozing yoki 🎤 bosing" : "Type or tap 🎤") + '" style="flex:1;height:46px;border-radius:14px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 14px;font-size:14px;outline:none;font-family:Manrope;">' +
        '<div id="ai-mic" style="width:46px;height:46px;border-radius:14px;background:rgba(255,255,255,.08);display:flex;align-items:center;justify-content:center;cursor:pointer;flex-shrink:0;transition:box-shadow .2s,background .2s;"><i data-lucide="mic" style="width:20px;height:20px;color:#fff"></i></div>' +
        '<div id="ai-send" style="width:46px;height:46px;border-radius:14px;background:linear-gradient(135deg,#FF8A2B,#FF4D00);display:flex;align-items:center;justify-content:center;cursor:pointer;flex-shrink:0;"><i data-lucide="arrow-up" style="width:20px;height:20px;color:#fff"></i></div>' +
      '</div>' +
    '</div>';
  document.body.appendChild(o);
  if (window.lucide) lucide.createIcons();
  const input = o.querySelector("#ai-input");
  o.querySelector("#ai-close").onclick = () => o.remove();
  const doSend = () => { const t = input.value.trim(); if (t) { input.value = ""; assistantSend(t, false); } };
  o.querySelector("#ai-send").onclick = doSend;
  o.querySelector("#ai-mic").onclick = () => recordToggle(o.querySelector("#ai-mic"));
  o.querySelector("#ai-diag").onclick = runDiagnose;
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") doSend(); });
  if (assistantHistory.length) {
    assistantHistory.forEach((t) => aiBubble(t.role, t.text));
  } else {
    aiBubble("model", uz
      ? "Salom! Mashinangiz haqida so'rang yoki buyruq bering — \"eshikni och\", \"zaryad qancha?\", \"salonni 22 ga isit\"."
      : "Hi! Ask about your car or give a command — \"unlock\", \"how much charge?\", \"set cabin to 22\".");
  }
  setTimeout(() => input.focus(), 100);
}

async function assistantSend(text, speak) {
  aiBubble("user", text);
  assistantHistory.push({ role: "user", text });
  await assistantReply(text, speak);
}

// Sends the message to the assistant, shows the reply, runs the action and —
// when speak is true (voice input) — reads the reply aloud in Uzbek.
async function assistantReply(text, speak) {
  const uz = window.App?.lang === "uz";
  const typing = aiBubble("model", "…");
  try {
    const tk = await auth.currentUser.getIdToken();
    const res = await fetch(`${CFG.apiBase}/v1/assistant`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${tk}` },
      body: JSON.stringify({ message: text, car: carContext(), history: assistantHistory.slice(0, -1).slice(-12) }),
    });
    if (res.status === 402) { if (typing) typing.textContent = uz ? "Bu Premium funksiya — obuna kerak." : "This is a Premium feature."; return; }
    if (!res.ok) throw new Error("HTTP " + res.status);
    const rep = await res.json();
    if (typing) typing.textContent = rep.reply || "…";
    assistantHistory.push({ role: "model", text: rep.reply || "" });
    runAssistantAction(rep);
    if (speak && rep.reply) ttsPlay(rep.reply);
  } catch (e) {
    if (typing) typing.textContent = uz ? "Xatolik — qayta urinib ko'ring." : "Error — try again.";
  }
}

// ----- Uzbek voice (UzbekVoice STT/TTS via the backend) -----
let mediaRec = null, recChunks = [], recording = false;

async function recordToggle(btn) {
  const uz = window.App?.lang === "uz";
  if (recording) { try { mediaRec && mediaRec.stop(); } catch (_) {} return; }
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    recChunks = [];
    const mime = (typeof MediaRecorder !== "undefined" && MediaRecorder.isTypeSupported("audio/webm")) ? "audio/webm"
      : (typeof MediaRecorder !== "undefined" && MediaRecorder.isTypeSupported("audio/mp4")) ? "audio/mp4" : "";
    mediaRec = new MediaRecorder(stream, mime ? { mimeType: mime } : undefined);
    mediaRec.ondataavailable = (e) => { if (e.data && e.data.size) recChunks.push(e.data); };
    mediaRec.onstop = async () => {
      recording = false; setRecUI(btn, false);
      stream.getTracks().forEach((t) => t.stop());
      const blob = new Blob(recChunks, { type: (mediaRec && mediaRec.mimeType) || "audio/webm" });
      if (blob.size < 800) { toast(uz ? "Ovoz juda qisqa" : "Too short"); return; }
      await sttSend(blob);
    };
    mediaRec.start();
    recording = true; setRecUI(btn, true);
    toast(uz ? "Gapiring… (to'xtatish uchun mikrofonni bosing)" : "Speak… (tap the mic to stop)");
  } catch (e) {
    toast(uz ? "Mikrofon ochilmadi / ruxsat yo'q" : "Mic unavailable / denied");
  }
}

function setRecUI(btn, on) {
  if (!btn) return;
  btn.style.background = on ? "#ff4d4d" : "rgba(255,255,255,.08)";
  btn.style.boxShadow = on ? "0 0 0 4px rgba(255,77,77,.25)" : "none";
}

async function sttSend(blob) {
  const uz = window.App?.lang === "uz";
  const bubble = aiBubble("user", "🎤 …");
  try {
    const tk = await auth.currentUser.getIdToken();
    const ext = (blob.type.indexOf("mp4") >= 0) ? "mp4" : "webm";
    const fd = new FormData();
    fd.append("file", blob, "audio." + ext);
    const res = await fetch(`${CFG.apiBase}/v1/voice/stt`, { method: "POST", headers: { Authorization: `Bearer ${tk}` }, body: fd });
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    const text = (data.text || "").trim();
    if (!text) { if (bubble) bubble.textContent = uz ? "🎤 (tushunolmadim)" : "🎤 (didn't catch that)"; return; }
    if (bubble) bubble.textContent = text;
    assistantHistory.push({ role: "user", text });
    await assistantReply(text, true); // speak the answer
  } catch (e) {
    if (bubble) bubble.textContent = uz ? "🎤 xato" : "🎤 error";
  }
}

async function ttsPlay(text) {
  try {
    const tk = await auth.currentUser.getIdToken();
    const res = await fetch(`${CFG.apiBase}/v1/voice/tts`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${tk}` },
      body: JSON.stringify({ text }),
    });
    if (!res.ok) return; // TTS is best-effort; text reply already shown
    const blob = await res.blob();
    if (!blob.size) return;
    const url = URL.createObjectURL(blob);
    const audio = new Audio(url);
    audio.onended = () => URL.revokeObjectURL(url);
    audio.play().catch(() => {});
  } catch (_) {}
}

// Executes the action the assistant chose, reusing the existing (RBAC-guarded)
// command endpoints. Informational replies (status/none) do nothing.
function runAssistantAction(rep) {
  const a = rep && rep.action;
  if (!a || a === "none" || a === "status") return;
  if (a === "locate") { try { App.go && App.go("map"); App.findCar && App.findCar(); } catch (_) {} return; }
  if (a === "climate") {
    const t = rep.params && rep.params.temp;
    sendCommand("climate", { on: true, targetC: Math.max(14, Math.min(32, Math.round(t || 22))) });
    return;
  }
  if (["lock", "unlock", "start", "stop"].includes(a)) sendCommand(a, null);
}

// AI car-health diagnostics: analyze the current telemetry and render a report.
async function runDiagnose() {
  const uz = window.App?.lang === "uz";
  aiBubble("user", "🩺 " + (uz ? "Diagnostika" : "Diagnose"));
  const typing = aiBubble("model", "…");
  try {
    const tk = await auth.currentUser.getIdToken();
    const langMap = { uz: "Uzbek", ru: "Russian", en: "English" };
    const res = await fetch(`${CFG.apiBase}/v1/diagnose`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${tk}` },
      body: JSON.stringify({ car: carContext(), lang: langMap[window.App?.lang] || "Uzbek" }),
    });
    if (res.status === 402) { if (typing) typing.textContent = uz ? "Bu Premium funksiya." : "Premium feature."; return; }
    if (!res.ok) throw new Error("HTTP " + res.status);
    const rep = await res.json();
    if (typing) typing.remove();
    diagBubble(rep);
  } catch (e) {
    if (typing) typing.textContent = uz ? "Xatolik — qayta urinib ko'ring." : "Error — try again.";
  }
}

function diagBubble(rep) {
  const box = document.getElementById("ai-msgs");
  if (!box) return;
  const uz = window.App?.lang === "uz";
  const st = rep.status || "ok";
  const color = st === "critical" ? "#ff5252" : st === "warning" ? "#FF8A3D" : "#43d684";
  const icon = st === "critical" ? "⛔" : st === "warning" ? "⚠️" : "✅";
  const label = st === "critical" ? (uz ? "Shoshilinch" : "Critical") : st === "warning" ? (uz ? "E'tibor bering" : "Warning") : (uz ? "Hammasi joyida" : "All good");
  let html = `<div style="display:flex;align-items:center;gap:8px;margin-bottom:6px;"><span style="font-size:16px">${icon}</span><b style="color:${color};">${label}</b></div>`;
  html += `<div style="font-size:13px;color:#e8e9ec;">${escapeHtml(rep.summary || "")}</div>`;
  (rep.issues || []).forEach((it) => {
    const c = it.severity === "critical" ? "#ff5252" : it.severity === "warning" ? "#FF8A3D" : "#9a9ca2";
    html += `<div style="border-left:3px solid ${c};padding:5px 0 5px 9px;margin-top:8px;"><div style="font-weight:700;font-size:13px;color:#fff;">${escapeHtml(it.title || "")}</div>` +
      (it.advice ? `<div style="font-size:12px;color:#9a9ca2;margin-top:2px;">${escapeHtml(it.advice)}</div>` : "") + `</div>`;
  });
  const b = document.createElement("div");
  b.style.cssText = "align-self:flex-start;max-width:90%;padding:12px 14px;border-radius:15px;background:rgba(255,255,255,.06);border-bottom-left-radius:5px;";
  b.innerHTML = html;
  box.appendChild(b);
  box.scrollTop = box.scrollHeight;
}

function clearRoute() {
  clearPOI();
  clearStopMarkers();
  destPoint = null; routeStops = [];
  if (routeLine) { leafletMap.removeLayer(routeLine); routeLine = null; }
  if (destMarker) { leafletMap.removeLayer(destMarker); destMarker = null; }
  document.querySelectorAll(".map-chip").forEach((c) => c.classList.remove("on"));
  showCancelBtn(false);
  const uz = window.App?.lang === "uz";
  set("map-dist", "");
  set("map-eta", uz ? "Manzil yoki shoxobcha tanlang" : "Pick a destination or station", true);
  set("map-stations", "", true);
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
    slider.style.touchAction = "none";
    const fromX = (clientX) => {
      const r = slider.getBoundingClientRect();
      setA(30 + Math.max(0, Math.min(1, (clientX - r.left) / r.width)) * 90);
    };
    let down = false;
    slider.addEventListener("pointerdown", (e) => {
      down = true;
      try { slider.setPointerCapture(e.pointerId); } catch (_) {}
      fromX(e.clientX);
      e.preventDefault();
    });
    slider.addEventListener("pointermove", (e) => { if (down) { fromX(e.clientX); e.preventDefault(); } });
    slider.addEventListener("pointerup", (e) => { down = false; try { slider.releasePointerCapture(e.pointerId); } catch (_) {} });
    slider.addEventListener("pointercancel", () => { down = false; });
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
  // Reflect the active car in the header card.
  if (App.activeCar) setFamilyCarUI(App.activeCar.name || "—", App.activeCar.img || "");
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
      '<div class="av">' + escapeHtml(initials) + "</div>" +
      '<div style="flex:1;min-width:0"><div style="color:#fff;font-weight:600;font-size:14px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">' + escapeHtml(m.email) + "</div>" +
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

// carsInGarage reads the current vehicles from the garage screen (kept in sync
// when cars are added).
function carsInGarage() {
  return [...document.querySelectorAll("#s-garage .gi")]
    .map((g) => ({
      brand: g.dataset.brand || "",
      name: (g.querySelector("[style*='Sora']")?.textContent || g.dataset.brand || "Car").trim(),
      img: g.querySelector("img")?.getAttribute("src") || "",
    }))
    .filter((c) => c.brand);
}

const CAR_SVG =
  '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="#FF8A3D" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 17h2c.6 0 1-.4 1-1v-3c0-.9-.7-1.7-1.5-1.9C18.7 10.6 16 10 16 10s-1.3-1.4-2.2-2.3c-.5-.4-1.1-.7-1.8-.7H5c-.6 0-1.1.4-1.4.9l-1.4 2.9A3.7 3.7 0 0 0 2 12v4c0 .6.4 1 1 1h2"/><circle cx="7" cy="17" r="2"/><circle cx="17" cy="17" r="2"/></svg>';

function setFamilyCarUI(name, img) {
  set("family-car-name", name);
  const wrap = document.getElementById("family-car-img");
  if (wrap) {
    wrap.innerHTML = img
      ? '<img src="' + img + '" alt="" style="width:100%;height:100%;object-fit:cover;object-position:center 32%">'
      : '<div style="width:100%;height:100%;display:flex;align-items:center;justify-content:center;background:linear-gradient(150deg,#2a2c31,#15161a)">' + CAR_SVG + "</div>";
  }
}

// pickFamilyCar lets the owner choose which car's family list to manage.
function pickFamilyCar() {
  const uz = window.App?.lang === "uz";
  const cars = carsInGarage();
  if (cars.length < 2) {
    toast(uz ? "Boshqa mashina yo‘q" : "No other vehicle");
    return;
  }
  const o = document.createElement("div");
  o.style.cssText = "position:fixed;inset:0;z-index:10002;background:rgba(0,0,0,.6);backdrop-filter:blur(4px);display:flex;align-items:center;justify-content:center;padding:28px;font-family:'Manrope',system-ui;";
  const close = () => o.remove();
  o.addEventListener("click", (e) => { if (e.target === o) close(); });
  const active = App.activeCar?.id;
  const rows = cars.map((c) =>
    '<div class="vd-car-opt" data-brand="' + c.brand + '" data-name="' + escapeHtml(c.name) + '" data-img="' + c.img + '" style="display:flex;align-items:center;gap:12px;padding:11px;border-radius:14px;background:' + (c.brand === active ? "rgba(255,106,26,.14)" : "rgba(255,255,255,.05)") + ';border:1px solid rgba(255,255,255,.08);cursor:pointer;margin-bottom:9px;">' +
    '<div style="width:46px;height:34px;border-radius:9px;overflow:hidden;flex-shrink:0;background:linear-gradient(150deg,#2a2c31,#15161a);display:flex;align-items:center;justify-content:center;">' +
    (c.img ? '<img src="' + c.img + '" style="width:100%;height:100%;object-fit:cover">' : CAR_SVG) + "</div>" +
    '<div style="flex:1;color:#fff;font-weight:700;font-size:14px;font-family:\'Sora\';">' + escapeHtml(c.name) + "</div>" +
    (c.brand === active ? '<i data-lucide="check" style="width:18px;height:18px;color:#FF8A3D"></i>' : "") + "</div>"
  ).join("");
  const box = document.createElement("div");
  box.style.cssText = "width:100%;max-width:330px;background:#16171a;border:1px solid rgba(255,255,255,.08);border-radius:22px;padding:20px;animation:scrIn .26s ease;";
  box.innerHTML =
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:14px;">' + (uz ? "Mashinani tanlang" : "Choose vehicle") + "</div>" +
    rows +
    '<div id="vd-car-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:9px;">' + (uz ? "Yopish" : "Close") + "</div>";
  o.appendChild(box);
  document.body.appendChild(o);
  if (window.lucide) lucide.createIcons();

  box.querySelector("#vd-car-close").addEventListener("click", close);
  box.querySelectorAll(".vd-car-opt").forEach((el) => el.addEventListener("click", () => {
    const id = el.dataset.brand, name = el.dataset.name, img = el.dataset.img;
    App.activeCar = { id, name, img, pos: "center 32%", range: App.activeCar?.range || "" };
    setFamilyCarUI(name, img);
    close();
    loadMembers();
    haptic(10);
  }));
}

// --- Departure timer (server-side daily warm-up) ---

async function openSchedule() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  if (!requirePremium()) return; // Departure timer is a Premium feature
  const vid = VMAP[App.activeCar?.id] || "voyah-001";
  let cur = { enabled: false, hour: 7, minute: 0, targetC: 24 };
  try {
    const token = await auth.currentUser.getIdToken();
    const r = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/schedule`, { headers: { Authorization: `Bearer ${token}` } });
    if (r.ok) cur = await r.json();
  } catch (e) {}

  const o = document.createElement("div");
  o.style.cssText = "position:fixed;inset:0;z-index:10002;background:rgba(0,0,0,.6);backdrop-filter:blur(4px);display:flex;align-items:center;justify-content:center;padding:28px;font-family:'Manrope',system-ui;";
  const close = () => o.remove();
  o.addEventListener("click", (e) => { if (e.target === o) close(); });
  const hhmm = String(cur.hour).padStart(2, "0") + ":" + String(cur.minute).padStart(2, "0");
  const box = document.createElement("div");
  box.style.cssText = "width:100%;max-width:330px;background:#16171a;border:1px solid rgba(255,255,255,.08);border-radius:22px;padding:20px;animation:scrIn .26s ease;";
  box.innerHTML =
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:6px;">' + (uz ? "Vaqt bo‘yicha qizdirish" : "Departure timer") + "</div>" +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:16px;line-height:1.45;">' + (uz ? "Har kuni shu vaqtda mashina avtomatik qiziydi (dvigatel + isitish)." : "The car warms up automatically at this time each day (engine + heater).") + "</div>" +
    '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:14px;"><span style="color:#fff;font-weight:600;font-size:14px;">' + (uz ? "Yoqilgan" : "Enabled") + '</span><div id="sch-tgl" class="tgl ' + (cur.enabled ? "on" : "off") + '"></div></div>' +
    '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:14px;"><span style="color:#fff;font-weight:600;font-size:14px;">' + (uz ? "Vaqt" : "Time") + '</span><input id="sch-time" type="time" value="' + hhmm + '" style="background:rgba(255,255,255,.06);border:1px solid rgba(255,255,255,.1);border-radius:12px;color:#fff;padding:8px 12px;font-size:15px;font-family:Manrope;"></div>' +
    '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:18px;"><span style="color:#fff;font-weight:600;font-size:14px;">' + (uz ? "Harorat" : "Temperature") + '</span><div style="display:flex;align-items:center;gap:12px;"><div id="sch-minus" class="ibtn" style="width:34px;height:34px;font-size:20px;">−</div><span id="sch-temp" style="color:#fff;font-family:\'Sora\';font-weight:700;font-size:17px;min-width:40px;text-align:center;">' + (cur.targetC || 24) + '°</span><div id="sch-plus" class="ibtn" style="width:34px;height:34px;font-size:20px;">+</div></div></div>' +
    '<div id="sch-save" class="obtn">' + (uz ? "Saqlash" : "Save") + "</div>" +
    '<div id="sch-cancel" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "Bekor qilish" : "Cancel") + "</div>";
  o.appendChild(box);
  document.body.appendChild(o);
  if (window.lucide) lucide.createIcons();

  const tgl = box.querySelector("#sch-tgl");
  tgl.addEventListener("click", () => { tgl.classList.toggle("on"); tgl.classList.toggle("off"); });
  let temp = cur.targetC || 24;
  const tempEl = box.querySelector("#sch-temp");
  box.querySelector("#sch-minus").addEventListener("click", () => { temp = Math.max(14, temp - 1); tempEl.textContent = temp + "°"; });
  box.querySelector("#sch-plus").addEventListener("click", () => { temp = Math.min(32, temp + 1); tempEl.textContent = temp + "°"; });
  box.querySelector("#sch-cancel").addEventListener("click", close);
  box.querySelector("#sch-save").addEventListener("click", async () => {
    const [h, m] = (box.querySelector("#sch-time").value || "07:00").split(":").map(Number);
    const enabled = tgl.classList.contains("on");
    try {
      const token = await auth.currentUser.getIdToken();
      const res = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/schedule`, {
        method: "PUT",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify({ enabled, hour: h, minute: m, targetC: temp }),
      });
      if (!res.ok) throw new Error("HTTP " + res.status);
      close();
      updateScheduleSub({ enabled, hour: h, minute: m });
      toast(enabled
        ? (uz ? `Saqlandi · har kuni ${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}` : `Saved · daily ${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`)
        : (uz ? "O‘chirildi" : "Disabled"), "ok");
      haptic([10, 40, 12]);
    } catch (e) {
      toast((uz ? "Xato: " : "Failed: ") + e.message, "err");
    }
  });
}

function updateScheduleSub(sc) {
  const uz = window.App?.lang === "uz";
  const el = document.getElementById("home-schedule-sub");
  if (!el) return;
  el.removeAttribute("data-i18n");
  el.textContent = ruMsg(sc.enabled
    ? (uz ? `Har kuni ${String(sc.hour).padStart(2, "0")}:${String(sc.minute).padStart(2, "0")}` : `Daily ${String(sc.hour).padStart(2, "0")}:${String(sc.minute).padStart(2, "0")}`)
    : (uz ? "O‘chiq" : "Off"));
}

// --- Shared modal shell ---
function vdModal(innerHTML, maxw) {
  const o = document.createElement("div");
  o.style.cssText = "position:fixed;inset:0;z-index:10002;background:rgba(0,0,0,.62);backdrop-filter:blur(4px);display:flex;align-items:center;justify-content:center;padding:26px;font-family:'Manrope',system-ui;";
  const box = document.createElement("div");
  box.style.cssText = "width:100%;max-width:" + (maxw || 340) + "px;background:#16171a;border:1px solid rgba(255,255,255,.08);border-radius:22px;padding:20px;animation:scrIn .26s ease;max-height:84vh;overflow:auto;";
  box.innerHTML = innerHTML;
  o.appendChild(box);
  o.addEventListener("click", (e) => { if (e.target === o) o.remove(); });
  document.body.appendChild(o);
  if (window.lucide) lucide.createIcons();
  return { o, box, close: () => o.remove() };
}

function curVid() { return VMAP[window.App?.activeCar?.id] || "voyah-001"; }

// --- Panic alarm: flash lights + sound the horn ---
// --- Subscriptions (Premium) ---

let currentSub = null;
const PLAN_LABEL = { trial: { uz: "Premium (sinov)", ru: "Премиум (триал)", en: "Premium (trial)" },
  active: { uz: "Premium", ru: "Премиум", en: "Premium" } };

async function apiAuthFetch(path, opts = {}) {
  const token = await auth.currentUser.getIdToken();
  return fetch(`${CFG.apiBase}${path}`, { ...opts, headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}`, ...(opts.headers || {}) } });
}

// Fetch the user's plan after sign-in and reflect it on the profile card.
async function refreshSubscription() {
  if (!CFG.apiBase || !auth?.currentUser) return;
  try {
    const r = await apiAuthFetch("/v1/subscription");
    if (r.ok) { currentSub = await r.json(); renderPlanBadge(); }
  } catch (e) {}
}

// Reveal the in-app Admin panel row only for admins / super admins.
async function checkAdminAccess() {
  if (!CFG.apiBase || !auth?.currentUser) return;
  const row = document.getElementById("admin-row");
  if (!row) return;
  try {
    const r = await apiAuthFetch("/v1/admin/whoami");
    if (!r.ok) { row.style.display = "none"; return; }
    const d = await r.json();
    row.style.display = d.admin ? "flex" : "none";
    const badge = document.getElementById("admin-badge");
    if (badge && d.super) badge.textContent = "SUPER";
  } catch (e) { row.style.display = "none"; }
}

function planActive() { return currentSub && (currentSub.status === "trial" || currentSub.status === "active"); }

function renderPlanBadge() {
  const lang = window.App?.lang || "en";
  const t = document.getElementById("plan-title");
  const s = document.getElementById("plan-sub");
  if (!t || !s) return;
  s.removeAttribute("data-i18n");
  if (planActive()) {
    const lab = (PLAN_LABEL[currentSub.status] || PLAN_LABEL.active);
    t.textContent = "VoltDrive " + (lab[lang] || lab.en);
    const days = Math.max(0, Math.ceil((currentSub.expiresAt - Date.now() / 1000) / 86400));
    s.textContent = (lang === "ru" ? `Активно · осталось ${days} дн.` : lang === "uz" ? `Faol · ${days} kun qoldi` : `Active · ${days} days left`);
  } else {
    t.textContent = "VoltDrive Free";
    s.textContent = (lang === "ru" ? "Нажмите, чтобы открыть Премиум-тарифы" : lang === "uz" ? "Premium tariflarni ko'rish uchun bosing" : "Tap to see Premium plans");
  }
}

async function openPremium() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  // Prices from config.
  const pr = (CFG.payment && CFG.payment.prices) || {};
  ["1m", "2m", "1y"].forEach((k) => { const el = document.getElementById("price-" + k); if (el && pr[k]) el.textContent = pr[k]; });
  document.getElementById("prem-pay").style.display = "none";
  window.App.go("premium");
  await refreshSubscription();
  renderPremiumStatus();
}

function renderPremiumStatus() {
  const lang = window.App?.lang || "en";
  const box = document.getElementById("prem-status");
  const trialWrap = document.getElementById("prem-trial-wrap");
  if (!box) return;
  if (planActive()) {
    const lab = (PLAN_LABEL[currentSub.status] || PLAN_LABEL.active);
    const days = Math.max(0, Math.ceil((currentSub.expiresAt - Date.now() / 1000) / 86400));
    document.getElementById("prem-status-t").textContent = "VoltDrive " + (lab[lang] || lab.en);
    document.getElementById("prem-status-s").textContent = (lang === "ru" ? `Осталось ${days} дн.` : lang === "uz" ? `${days} kun qoldi` : `${days} days left`);
    box.style.display = "block";
    if (trialWrap) trialWrap.style.display = "none";
  } else {
    box.style.display = "none";
    // Hide the trial button if a trial was already used.
    if (trialWrap) trialWrap.style.display = (currentSub && currentSub.status === "expired" && currentSub.tier === "trial") ? "none" : "block";
  }
}

async function startTrial() {
  const uz = window.App?.lang === "uz";
  try {
    const r = await apiAuthFetch("/v1/subscription/trial", { method: "POST" });
    if (r.status === 409) { toast(uz ? "Sinov allaqachon ishlatilgan" : "Trial already used"); return; }
    if (!r.ok) throw new Error("HTTP " + r.status);
    currentSub = await r.json();
    renderPlanBadge(); renderPremiumStatus();
    toast(uz ? "14 kunlik Premium yoqildi ✓" : "14-day Premium activated ✓", "ok");
    haptic([10, 40, 12]);
  } catch (e) { toast((uz ? "Xato: " : "Failed: ") + e.message, "err"); }
}

async function requestTier(tier) {
  const uz = window.App?.lang === "uz";
  try {
    const r = await apiAuthFetch("/v1/subscription/request", { method: "POST", body: JSON.stringify({ tier }) });
    if (!r.ok) throw new Error("HTTP " + r.status);
    currentSub = await r.json();
    // Show payment instructions.
    const pay = CFG.payment || {};
    document.getElementById("pay-card").textContent = pay.card || "—";
    document.getElementById("pay-holder").textContent = pay.holder || "";
    const price = (pay.prices && pay.prices[tier]) || "";
    const names = { "1m": uz ? "1 oy" : "1 month", "2m": uz ? "2 oy" : "2 months", "1y": uz ? "1 yil" : "1 year" };
    document.getElementById("pay-amount").textContent = (uz ? "To'lov: " : "Amount: ") + price + " so'm · " + names[tier];
    const box = document.getElementById("prem-pay");
    box.style.display = "block";
    box.scrollIntoView({ behavior: "smooth", block: "center" });
    toast(uz ? "So'rov yuborildi — kartaga to'lang" : "Requested — transfer to the card", "ok");
  } catch (e) { toast((uz ? "Xato: " : "Failed: ") + e.message, "err"); }
}

function copyCard() {
  const uz = window.App?.lang === "uz";
  const card = (CFG.payment && CFG.payment.card) || "";
  navigator.clipboard?.writeText(card.replace(/\s/g, "")).then(
    () => toast(uz ? "Karta raqami nusxalandi ✓" : "Card number copied ✓", "ok"),
    () => toast(card));
}

// requirePremium gates a premium-only feature; returns true if allowed,
// otherwise opens the Premium screen.
function requirePremium() {
  if (planActive()) return true;
  const uz = window.App?.lang === "uz";
  toast(uz ? "Bu funksiya Premium uchun" : "This is a Premium feature");
  openPremium();
  return false;
}

// --- Fleet dashboard ---

async function openFleet() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  window.App.go("fleet");
  loadFleet();
}

// Known vehicle pool (display names) the operator can add to their fleet.
const FLEET_POOL = {
  "voyah-001": "Voyah Free", "deepal-002": "Deepal S07",
  "dongfeng-003": "Dongfeng 008", "byd-004": "BYD",
};
let fleetState = { name: "My Fleet", slots: 10, vehicleIds: [] };

async function loadFleet() {
  const uz = window.App?.lang === "uz";
  const list = document.getElementById("fleet-list");
  if (!list) return;
  list.innerHTML = `<div style="color:#6a6c72;text-align:center;padding:30px;font-size:13px;">${uz ? "Yuklanmoqda…" : "Loading…"}</div>`;
  try {
    const fr = await apiAuthFetch("/v1/fleet");
    if (fr.ok) fleetState = await fr.json();
  } catch (e) {}
  if (!fleetState.vehicleIds) fleetState.vehicleIds = [];

  const results = await Promise.all(fleetState.vehicleIds.map(async (id) => {
    try {
      const r = await apiAuthFetch(`/v1/vehicles/${id}`);
      if (r.ok) return { id, name: FLEET_POOL[id] || id, snap: await r.json() };
    } catch (e) {}
    return { id, name: FLEET_POOL[id] || id, snap: null };
  }));

  let online = 0, lockedN = 0, battSum = 0, battN = 0;
  const rows = results.map((v) => {
    const s = v.snap || {};
    const ok = !!v.snap; if (ok) online++;
    const batt = typeof s.batteryPct === "number" ? s.batteryPct : (typeof s.soc === "number" ? s.soc : (typeof s.energy?.batteryLevel === "number" ? s.energy.batteryLevel : null));
    const locked = s.locked !== undefined ? s.locked : (s.lock === "locked" || s.lock === true);
    if (locked) lockedN++;
    if (batt != null) { battSum += batt; battN++; }
    const range = typeof s.rangeKm === "number" ? s.rangeKm : (s.energy?.rangeKm ?? null);
    const dotC = !ok ? "#5a5c62" : batt != null && batt < 20 ? "#ff5252" : "#43d684";
    const lockTxt = locked ? (uz ? "Qulflangan" : "Locked") : (uz ? "Ochiq" : "Unlocked");
    return `<div class="fleet-card">
      <div class="fleet-dot" style="background:${dotC}"></div>
      <div style="flex:1;min-width:0">
        <div style="color:#fff;font-weight:700;font-size:14px;font-family:'Sora'">${escapeHtml(v.name)}</div>
        <div style="color:#8a8c92;font-size:12px;margin-top:2px;">${batt != null ? batt + "% · " : ""}${range != null ? range + " km · " : ""}${lockTxt}</div>
      </div>
      <i data-lucide="${locked ? "lock" : "lock-open"}" style="width:17px;height:17px;color:${locked ? "#43d684" : "var(--acc)"}"></i>
      <div class="ibtn" style="width:32px;height:32px;" onclick="App.fleetRemove('${v.id}')"><i data-lucide="x" style="width:15px;height:15px;color:#ff8a8a"></i></div>
    </div>`;
  });
  list.innerHTML = rows.length ? rows.join("") : `<div style="color:#6a6c72;text-align:center;padding:30px;font-size:13px;">${uz ? "Park bo'sh — mashina qo'shing" : "Empty — add a vehicle"}</div>`;

  // Stats tiles.
  const avgBatt = battN ? Math.round(battSum / battN) : 0;
  const stat = (v, l) => `<div style="background:rgba(255,255,255,.04);border:1px solid rgba(255,255,255,.08);border-radius:13px;padding:9px 6px;text-align:center;"><div style="font-family:'Sora';font-weight:800;font-size:17px;color:#fff;">${v}</div><div style="color:#8a8c92;font-size:10px;margin-top:1px;">${l}</div></div>`;
  const stats = document.getElementById("fleet-stats");
  if (stats) stats.innerHTML =
    stat(results.length, uz ? "Mashina" : "Vehicles") +
    stat(online, uz ? "Onlayn" : "Online") +
    stat(lockedN, uz ? "Qulf" : "Locked") +
    stat(avgBatt + "%", uz ? "O'rt. zaryad" : "Avg batt");

  const sub = document.getElementById("fleet-sub");
  if (sub) { sub.removeAttribute("data-i18n"); sub.textContent = `${results.length}/${fleetState.slots} ${uz ? "joy band" : "slots used"}`; }

  // Populate the "add vehicle" dropdown with pool vehicles not yet in the fleet.
  const sel = document.getElementById("fleet-add-sel");
  if (sel) {
    const avail = Object.keys(FLEET_POOL).filter((id) => !fleetState.vehicleIds.includes(id));
    sel.innerHTML = avail.length
      ? avail.map((id) => `<option value="${id}">${FLEET_POOL[id]}</option>`).join("")
      : `<option value="">${uz ? "Barchasi qo'shilgan" : "All added"}</option>`;
  }
  if (window.lucide) lucide.createIcons();
}

async function saveFleet() {
  try {
    const r = await apiAuthFetch("/v1/fleet", { method: "PUT", body: JSON.stringify(fleetState) });
    if (r.ok) fleetState = await r.json();
  } catch (e) {}
}

async function fleetAdd() {
  const uz = window.App?.lang === "uz";
  const sel = document.getElementById("fleet-add-sel");
  const id = sel && sel.value;
  if (!id) return;
  if ((fleetState.vehicleIds || []).length >= fleetState.slots) {
    toast(uz ? "Joylar to'lgan — rejani kengaytiring" : "No free slots — upgrade plan", "err");
    return;
  }
  fleetState.vehicleIds = [...(fleetState.vehicleIds || []), id];
  await saveFleet();
  toast(uz ? "Qo'shildi ✓" : "Added ✓", "ok");
  loadFleet();
}

async function fleetRemove(id) {
  fleetState.vehicleIds = (fleetState.vehicleIds || []).filter((x) => x !== id);
  await saveFleet();
  loadFleet();
}

async function fleetAll(action) {
  const uz = window.App?.lang === "uz";
  const ids = fleetState.vehicleIds || [];
  if (!ids.length) return;
  toast(uz ? "Bajarilmoqda…" : "Working…");
  await Promise.all(ids.map((id) => apiAuthFetch(`/v1/vehicles/${id}/${action}`, { method: "POST" }).catch(() => {})));
  toast(action === "lock" ? (uz ? "Hammasi qulflandi ✓" : "All locked ✓") : (uz ? "Hammasi ochildi ✓" : "All unlocked ✓"), "ok");
  haptic([10, 30, 10]);
  loadFleet();
}

// --- White-label branding (apply config.branding) ---
function applyBranding() {
  const b = (CFG && CFG.branding) || {};
  const root = document.documentElement.style;
  // Accent colours — one or two colours recolour every button / ring / logo.
  if (b.accent) root.setProperty("--g1", b.accent);
  if (b.accent2) root.setProperty("--g2", b.accent2);
  if (b.accentSolid || b.accent) root.setProperty("--acc", b.accentSolid || b.accent);
  // Brand name everywhere it appears.
  if (b.name && b.name !== "VoltDrive") {
    document.querySelectorAll(".brand-name").forEach((el) => (el.textContent = b.name));
    const pn = document.getElementById("profile-name");
    if (pn && pn.textContent === "VoltDrive") pn.textContent = b.name;
  }
  document.title = (b.name || "VoltDrive") + (b.tagline ? " — " + b.tagline : "");
  // Dealer logo — replace the bolt mark with an image when provided.
  if (b.logo) {
    document.querySelectorAll(".brand-mark").forEach((el) => {
      el.style.background = "#fff";
      el.style.backgroundImage = `url("${b.logo}")`;
      el.style.backgroundSize = "78%";
      el.style.backgroundPosition = "center";
      el.style.backgroundRepeat = "no-repeat";
      el.querySelectorAll("svg,i").forEach((c) => (c.style.display = "none"));
    });
  }
}

async function panic() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  try {
    const token = await auth.currentUser.getIdToken();
    const r = await fetch(`${CFG.apiBase}/v1/vehicles/${curVid()}/panic`, { method: "POST", headers: { Authorization: `Bearer ${token}` } });
    if (!r.ok) throw new Error("HTTP " + r.status);
    toast(uz ? "Trevoga! Signal va faralar yoqildi" : "Panic! Horn & lights on", "ok");
    haptic([20, 60, 20, 60, 20]);
  } catch (e) { toast((uz ? "Xato: " : "Failed: ") + e.message, "err"); }
}

// --- SOS: emergency — alert family with location + quick call to services ---
function sos() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  const m = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:19px;color:#ff5252;margin-bottom:4px;">🆘 ' + (uz ? "Favqulodda holat" : "Emergency") + '</div>' +
    '<div style="color:#9a9ca2;font-size:13px;margin-bottom:18px;line-height:1.45;">' + (uz ? "Oilangizga joriy joylashuvingiz bilan favqulodda xabar yuboriladi." : "An emergency alert with your live location is sent to your family.") + '</div>' +
    '<div id="sos-send" class="obtn" style="background:linear-gradient(135deg,#ff5a5a,#e02020);">' + (uz ? "Oilaga SOS yuborish" : "Send SOS to family") + '</div>' +
    '<a href="tel:103" style="display:flex;align-items:center;justify-content:center;gap:8px;height:50px;border-radius:15px;background:rgba(255,255,255,.06);border:1px solid rgba(255,255,255,.12);color:#fff;font-weight:700;font-size:15px;text-decoration:none;margin-top:10px;">📞 103 — ' + (uz ? "Tez yordam" : "Ambulance") + '</a>' +
    '<a href="tel:102" style="display:flex;align-items:center;justify-content:center;gap:8px;height:50px;border-radius:15px;background:rgba(255,255,255,.06);border:1px solid rgba(255,255,255,.12);color:#fff;font-weight:700;font-size:15px;text-decoration:none;margin-top:8px;">🚓 102 — ' + (uz ? "Politsiya" : "Police") + '</a>' +
    '<div id="sos-cancel" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:14px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>'
  );
  m.box.querySelector("#sos-cancel").onclick = m.close;
  m.box.querySelector("#sos-send").onclick = async () => {
    const btn = m.box.querySelector("#sos-send");
    btn.textContent = uz ? "Yuborilmoqda…" : "Sending…";
    let lat = 0, lng = 0;
    try {
      const pos = await new Promise((res, rej) => navigator.geolocation.getCurrentPosition(res, rej, { timeout: 6000 }));
      lat = pos.coords.latitude; lng = pos.coords.longitude;
    } catch (_) { if (lastLoc) { lat = lastLoc.lat; lng = lastLoc.lng; } }
    try {
      const tk = await auth.currentUser.getIdToken();
      const r = await fetch(`${CFG.apiBase}/v1/sos`, { method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${tk}` }, body: JSON.stringify({ lat, lng }) });
      const d = await r.json().catch(() => ({}));
      haptic([30, 50, 30, 50, 30]);
      toast(uz ? `SOS yuborildi (${d.sent || 0} qurilma)` : `SOS sent (${d.sent || 0} devices)`, "ok");
      m.close();
    } catch (e) {
      toast(uz ? "Xatolik — qayta urinib ko'ring" : "Failed — try again", "err");
      btn.textContent = uz ? "Oilaga SOS yuborish" : "Send SOS to family";
    }
  };
}

// --- Parking memory: remember where the car was parked (local, no backend) ---
function openParking() {
  const uz = window.App?.lang === "uz";
  let saved = null;
  try { saved = JSON.parse(localStorage.getItem("vd_parking") || "null"); } catch (_) {}
  let inner = '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">🅿️ ' + (uz ? "Avtoturargoh" : "Parking") + '</div>';
  if (saved && saved.lat) {
    const ago = Math.round((Date.now() - saved.t) / 60000);
    const agoTxt = ago < 1 ? (uz ? "hozir" : "just now") : (ago < 60 ? ago + (uz ? " daqiqa oldin" : "m ago") : Math.round(ago / 60) + (uz ? " soat oldin" : "h ago"));
    inner += '<div style="color:#9a9ca2;font-size:13px;margin:6px 0 16px;">' + (uz ? "Saqlangan: " : "Saved ") + agoTxt + '</div>' +
      '<a href="https://www.google.com/maps/dir/?api=1&destination=' + saved.lat + ',' + saved.lng + '&travelmode=walking" target="_blank" rel="noopener" class="obtn" style="text-decoration:none;display:flex;align-items:center;justify-content:center;gap:8px;">🚶 ' + (uz ? "Mashinaga yo'l" : "Walk to car") + '</a>' +
      '<div id="pk-save" style="height:48px;border-radius:14px;background:rgba(255,255,255,.06);border:1px solid rgba(255,255,255,.12);color:#fff;font-weight:700;font-size:14px;display:flex;align-items:center;justify-content:center;cursor:pointer;margin-top:10px;">' + (uz ? "Joyni yangilash" : "Update spot") + '</div>' +
      '<div id="pk-clear" style="text-align:center;color:#ff7a7a;font-weight:700;font-size:13px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "O'chirish" : "Clear") + '</div>';
  } else {
    inner += '<div style="color:#9a9ca2;font-size:13px;margin:6px 0 16px;line-height:1.45;">' + (uz ? "Mashinani qoldirgan joyingizni saqlang — keyin piyoda oson topasiz." : "Save where you parked — walk back to it easily.") + '</div>' +
      '<div id="pk-save" class="obtn">' + (uz ? "Joriy joyni saqlash" : "Save current spot") + '</div>';
  }
  inner += '<div id="pk-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>';
  const o = vdModal(inner);
  o.box.querySelector("#pk-close").onclick = o.close;
  const cl = o.box.querySelector("#pk-clear");
  if (cl) cl.onclick = () => { localStorage.removeItem("vd_parking"); o.close(); toast(uz ? "O'chirildi" : "Cleared"); };
  o.box.querySelector("#pk-save").onclick = async () => {
    let lat = 0, lng = 0;
    try {
      const p = await new Promise((res, rej) => navigator.geolocation.getCurrentPosition(res, rej, { timeout: 6000 }));
      lat = p.coords.latitude; lng = p.coords.longitude;
    } catch (_) {
      if (deviceLoc) { lat = deviceLoc.lat; lng = deviceLoc.lng; }
      else if (lastLoc) { lat = lastLoc.lat; lng = lastLoc.lng; }
    }
    if (!lat && !lng) { toast(uz ? "Joylashuv aniqlanmadi" : "No location"); return; }
    localStorage.setItem("vd_parking", JSON.stringify({ lat, lng, t: Date.now() }));
    haptic(12);
    toast(uz ? "Avtoturargoh saqlandi 🅿️" : "Parking saved 🅿️", "ok");
    o.close(); openParking();
  };
}

// --- Reminders: service / insurance / tax dates (local, with due alerts) ---
function remStore() { try { return JSON.parse(localStorage.getItem("vd_reminders") || "[]"); } catch (_) { return []; } }
function remSet(list) { localStorage.setItem("vd_reminders", JSON.stringify(list)); updateRemBadge(); }
function daysLeft(date) { return Math.ceil((new Date(date + "T00:00:00").getTime() - Date.now()) / 86400000); }
function updateRemBadge() {
  const el = document.getElementById("rem-badge"); if (!el) return;
  const soon = remStore().filter((r) => { const d = daysLeft(r.date); return d >= 0 && d <= 30; }).length;
  el.textContent = soon ? "⏰ " + soon : "";
}
function checkRemindersDue() {
  updateRemBadge();
  const uz = window.App?.lang === "uz";
  const due = remStore().map((r) => ({ ...r, d: daysLeft(r.date) })).filter((r) => r.d >= 0 && r.d <= 3).sort((a, b) => a.d - b.d);
  if (due.length) {
    const r = due[0];
    toast("🔔 " + r.title + " — " + (r.d === 0 ? (uz ? "bugun" : "today") : r.d + (uz ? " kun qoldi" : "d left")));
  }
}
function openReminders() {
  const uz = window.App?.lang === "uz";
  const render = () => {
    const list = remStore().slice().sort((a, b) => a.date.localeCompare(b.date));
    return list.map((r) => {
      const d = daysLeft(r.date);
      const col = d < 0 ? "#ff6a6a" : d <= 7 ? "#FF8A3D" : "#43d684";
      const txt = d < 0 ? (uz ? "o'tdi" : "overdue") : d === 0 ? (uz ? "bugun" : "today") : d + (uz ? " kun" : "d");
      return '<div style="display:flex;align-items:center;gap:10px;background:rgba(255,255,255,.04);border-radius:12px;padding:11px 12px;margin-bottom:8px;">' +
        '<div style="flex:1;min-width:0"><div style="color:#fff;font-weight:600;font-size:14px;">' + escapeHtml(r.title) + '</div><div style="color:#7e8086;font-size:11px;margin-top:2px;">' + escapeHtml(r.date) + '</div></div>' +
        '<div style="color:' + col + ';font-weight:700;font-size:12px;white-space:nowrap;">' + txt + '</div>' +
        '<div data-del="' + r.id + '" style="color:#ff6a6a;font-size:18px;cursor:pointer;padding:0 2px;">×</div></div>';
    }).join("") || '<div style="color:#6a6c72;text-align:center;font-size:13px;padding:14px;">' + (uz ? "Eslatma yo'q" : "No reminders") + '</div>';
  };
  const presets = uz ? ["Texnik ko'rik", "Sug'urta", "Soliq", "Moy almashtirish"] : ["Inspection", "Insurance", "Tax", "Oil change"];
  const inner =
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">🔔 ' + (uz ? "Eslatmalar" : "Reminders") + '</div>' +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:14px;">' + (uz ? "Texnik ko'rik, sug'urta, soliq muddatlari" : "Service, insurance, tax dates") + '</div>' +
    '<div id="rem-list">' + render() + '</div>' +
    '<div style="border-top:1px solid rgba(255,255,255,.07);margin:12px 0;padding-top:13px;">' +
      '<div style="display:flex;gap:7px;flex-wrap:wrap;margin-bottom:10px;">' + presets.map((p) => '<span class="rem-p" style="font-size:12px;padding:6px 11px;border-radius:20px;background:rgba(255,138,43,.13);border:1px solid rgba(255,138,43,.3);color:#FF9D5C;cursor:pointer;">' + p + '</span>').join("") + '</div>' +
      '<input id="rem-title" placeholder="' + (uz ? "Nomi" : "Title") + '" style="width:100%;height:44px;border-radius:12px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 13px;font-size:14px;outline:none;margin-bottom:9px;font-family:Manrope;">' +
      '<input id="rem-date" type="date" style="width:100%;height:44px;border-radius:12px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 13px;font-size:14px;outline:none;margin-bottom:11px;font-family:Manrope;">' +
      '<div id="rem-add" class="obtn" style="height:46px;">' + (uz ? "Qo'shish" : "Add") + '</div>' +
    '</div>' +
    '<div id="rem-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>';
  const o = vdModal(inner, 360);
  const bindDel = () => o.box.querySelectorAll("[data-del]").forEach((b) => b.onclick = () => { remSet(remStore().filter((x) => x.id != b.getAttribute("data-del"))); o.box.querySelector("#rem-list").innerHTML = render(); bindDel(); });
  bindDel();
  o.box.querySelector("#rem-close").onclick = o.close;
  o.box.querySelectorAll(".rem-p").forEach((p) => p.onclick = () => { o.box.querySelector("#rem-title").value = p.textContent; });
  o.box.querySelector("#rem-add").onclick = () => {
    const t = o.box.querySelector("#rem-title").value.trim();
    const d = o.box.querySelector("#rem-date").value;
    if (!t || !d) { toast(uz ? "Nomi va sanani kiriting" : "Enter title and date"); return; }
    const list = remStore(); list.push({ id: Date.now().toString(36), title: t, date: d }); remSet(list);
    o.box.querySelector("#rem-title").value = ""; o.box.querySelector("#rem-date").value = "";
    o.box.querySelector("#rem-list").innerHTML = render(); bindDel();
    toast(uz ? "Qo'shildi ✓" : "Added ✓", "ok");
  };
}

// --- Share my live location + ETA (Web Share / clipboard) ---
async function shareLocation() {
  const uz = window.App?.lang === "uz";
  let lat = 0, lng = 0;
  try {
    const p = await new Promise((res, rej) => navigator.geolocation.getCurrentPosition(res, rej, { timeout: 6000 }));
    lat = p.coords.latitude; lng = p.coords.longitude;
  } catch (_) {
    if (deviceLoc) { lat = deviceLoc.lat; lng = deviceLoc.lng; }
    else if (lastLoc) { lat = lastLoc.lat; lng = lastLoc.lng; }
  }
  if (!lat && !lng) { toast(uz ? "Joylashuv aniqlanmadi" : "No location"); return; }
  const link = "https://maps.google.com/?q=" + lat + "," + lng;
  let txt = (uz ? "Mening joylashuvim: " : "My location: ") + link;
  const eta = document.getElementById("map-eta");
  if (destPoint && eta && eta.textContent && eta.textContent.indexOf("km") >= 0) {
    txt = (uz ? "Yo'ldaman — " : "On my way — ") + eta.textContent.trim() + "\n" + link;
  }
  try {
    if (navigator.share) await navigator.share({ title: "VoltDrive", text: txt });
    else { await navigator.clipboard.writeText(txt); toast(uz ? "Nusxalandi" : "Copied", "ok"); }
  } catch (_) {}
}

// --- Refer a friend: shareable code + invite link ---
function openReferral() {
  const uz = window.App?.lang === "uz";
  let code = localStorage.getItem("vd_ref");
  if (!code) {
    code = "VD" + ((auth && auth.currentUser && auth.currentUser.uid) || Date.now().toString(36)).slice(-6).toUpperCase();
    localStorage.setItem("vd_ref", code);
  }
  const link = "https://eldi-79bf9.web.app/?ref=" + code;
  const msg = (uz ? "VoltDrive — mashinangizni telefondan boshqaring! Mening kodim bilan qo'shiling: " : "VoltDrive — control your car from your phone! Join with my code: ") + code + " " + link;
  const inner =
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">🎁 ' + (uz ? "Do'st taklif qiling" : "Refer a friend") + '</div>' +
    '<div style="color:#9a9ca2;font-size:13px;margin-bottom:16px;line-height:1.45;">' + (uz ? "Do'stingiz qo'shilsa — ikkalangizga 1 oy Premium bonus." : "When your friend joins, you both get 1 month Premium.") + '</div>' +
    '<div style="background:rgba(255,138,43,.12);border:1px dashed rgba(255,138,43,.4);border-radius:14px;padding:16px;text-align:center;margin-bottom:14px;"><div style="color:#9a9ca2;font-size:11px;">' + (uz ? "Sizning kodingiz" : "Your code") + '</div><div style="font-family:\'Sora\';font-weight:800;font-size:26px;color:#FF9D5C;letter-spacing:2px;margin-top:4px;">' + code + '</div></div>' +
    '<div id="ref-share" class="obtn">' + (uz ? "Ulashish" : "Share") + '</div>' +
    '<div id="ref-copy" style="height:46px;border-radius:14px;background:rgba(255,255,255,.06);border:1px solid rgba(255,255,255,.12);color:#fff;font-weight:700;font-size:14px;display:flex;align-items:center;justify-content:center;cursor:pointer;margin-top:9px;">' + (uz ? "Kodni nusxalash" : "Copy code") + '</div>' +
    '<div id="ref-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>';
  const o = vdModal(inner);
  o.box.querySelector("#ref-close").onclick = o.close;
  o.box.querySelector("#ref-share").onclick = async () => { try { if (navigator.share) await navigator.share({ title: "VoltDrive", text: msg }); else { await navigator.clipboard.writeText(msg); toast(uz ? "Nusxalandi" : "Copied", "ok"); } } catch (_) {} };
  o.box.querySelector("#ref-copy").onclick = async () => { try { await navigator.clipboard.writeText(code); toast(uz ? "Kod nusxalandi" : "Code copied", "ok"); } catch (_) {} };
}

// --- Automation: time-based routines ("every day at 07:00 warm up") ---
async function openAutomation() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  let list = [];
  try {
    const tk = await auth.currentUser.getIdToken();
    const r = await fetch(`${CFG.apiBase}/v1/routines`, { headers: { Authorization: `Bearer ${tk}` } });
    if (r.ok) list = (await r.json()) || [];
  } catch (_) {}
  const actLabel = { climate_on: uz ? "Isitish/Klimat" : "Climate on", climate_off: uz ? "Klimatni o'chirish" : "Climate off", lock: uz ? "Qulflash" : "Lock", unlock: uz ? "Ochish" : "Unlock", start: uz ? "Dvigatel yoqish" : "Start engine", stop: uz ? "Dvigatel o'chirish" : "Stop engine" };
  const dayLabels = uz ? ["Ya", "Du", "Se", "Ch", "Pa", "Ju", "Sh"] : ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"];
  const save = async () => { try { const tk = await auth.currentUser.getIdToken(); await fetch(`${CFG.apiBase}/v1/routines`, { method: "PUT", headers: { "Content-Type": "application/json", Authorization: `Bearer ${tk}` }, body: JSON.stringify(list) }); } catch (_) { toast(uz ? "Saqlanmadi" : "Save failed", "err"); } };
  let o;
  const renderList = () => {
    if (!list.length) return '<div style="color:#6a6c72;text-align:center;font-size:13px;padding:14px;">' + (uz ? "Avtomatlashtirish yo'q" : "No automations") + '</div>';
    return list.map((r, i) => {
      const t = String(r.h).padStart(2, "0") + ":" + String(r.m).padStart(2, "0");
      const days = (!r.days || !r.days.length) ? (uz ? "Har kuni" : "Daily") : r.days.map((d) => dayLabels[d]).join(" ");
      return '<div style="display:flex;align-items:center;gap:10px;background:rgba(255,255,255,.04);border-radius:12px;padding:11px 12px;margin-bottom:8px;">' +
        '<div style="flex:1;min-width:0"><div style="color:#fff;font-weight:700;font-size:15px;font-family:\'Sora\'">' + t + ' · <span style="font-weight:600;font-size:13px;color:#c8cace">' + (actLabel[r.action] || r.action) + (r.action === "climate_on" ? " " + r.temp + "°" : "") + '</span></div><div style="color:#7e8086;font-size:11px;margin-top:2px;">' + days + '</div></div>' +
        '<div data-tog="' + i + '" style="width:40px;height:23px;border-radius:12px;background:' + (r.on ? "#FF6A1A" : "rgba(255,255,255,.15)") + ';position:relative;cursor:pointer;flex-shrink:0;"><div style="width:19px;height:19px;border-radius:50%;background:#fff;position:absolute;top:2px;left:' + (r.on ? "19px" : "2px") + ';transition:.2s;"></div></div>' +
        '<div data-del="' + i + '" style="color:#ff6a6a;font-size:18px;cursor:pointer;padding:0 2px;">×</div></div>';
    }).join("");
  };
  const bind = () => {
    o.box.querySelectorAll("[data-tog]").forEach((b) => b.onclick = async () => { const i = +b.getAttribute("data-tog"); list[i].on = !list[i].on; o.box.querySelector("#au-list").innerHTML = renderList(); bind(); await save(); });
    o.box.querySelectorAll("[data-del]").forEach((b) => b.onclick = async () => { list.splice(+b.getAttribute("data-del"), 1); o.box.querySelector("#au-list").innerHTML = renderList(); bind(); await save(); });
  };
  const acts = Object.keys(actLabel);
  const inner =
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">⏰ ' + (uz ? "Avtomatlashtirish" : "Automation") + '</div>' +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:14px;">' + (uz ? "Belgilangan vaqtda avtomatik bajariladi" : "Runs automatically at the set time") + '</div>' +
    '<div id="au-list">' + renderList() + '</div>' +
    '<div style="border-top:1px solid rgba(255,255,255,.07);margin:12px 0;padding-top:13px;">' +
      '<div style="display:flex;gap:9px;margin-bottom:10px;"><input id="au-time" type="time" value="07:00" style="flex:1;height:44px;border-radius:12px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 13px;font-size:14px;outline:none;font-family:Manrope;">' +
      '<select id="au-act" style="flex:1.4;height:44px;border-radius:12px;background:#16171a;border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 10px;font-size:13px;outline:none;font-family:Manrope;">' + acts.map((a) => '<option value="' + a + '">' + actLabel[a] + '</option>').join("") + '</select></div>' +
      '<div id="au-temp-row" style="display:none;align-items:center;gap:9px;margin-bottom:10px;"><span style="color:#9a9ca2;font-size:13px;">' + (uz ? "Harorat" : "Temp") + '</span><input id="au-temp" type="number" value="22" min="14" max="32" style="width:80px;height:40px;border-radius:10px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 12px;font-size:14px;outline:none;"><span style="color:#9a9ca2;">°C</span></div>' +
      '<div id="au-days" style="display:flex;gap:5px;margin-bottom:11px;"></div>' +
      '<div id="au-add" class="obtn" style="height:46px;">' + (uz ? "Qo'shish" : "Add") + '</div>' +
    '</div>' +
    '<div id="au-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>';
  o = vdModal(inner, 380);
  bind();
  const selDays = new Set();
  const dayWrap = o.box.querySelector("#au-days");
  dayLabels.forEach((lab, idx) => {
    const c = document.createElement("div");
    c.textContent = lab;
    c.style.cssText = "flex:1;text-align:center;padding:8px 0;border-radius:9px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#9a9ca2;font-size:12px;font-weight:700;cursor:pointer;";
    c.onclick = () => { if (selDays.has(idx)) { selDays.delete(idx); c.style.background = "rgba(255,255,255,.05)"; c.style.color = "#9a9ca2"; } else { selDays.add(idx); c.style.background = "rgba(255,138,43,.2)"; c.style.color = "#FF9D5C"; } };
    dayWrap.appendChild(c);
  });
  const actSel = o.box.querySelector("#au-act");
  const tempRow = o.box.querySelector("#au-temp-row");
  const syncTemp = () => { tempRow.style.display = actSel.value === "climate_on" ? "flex" : "none"; };
  actSel.onchange = syncTemp; syncTemp();
  o.box.querySelector("#au-close").onclick = o.close;
  o.box.querySelector("#au-add").onclick = async () => {
    const tv = o.box.querySelector("#au-time").value || "07:00";
    const parts = tv.split(":");
    const r = { id: Date.now().toString(36), h: +parts[0], m: +parts[1], days: [...selDays].sort(), action: actSel.value, temp: +o.box.querySelector("#au-temp").value || 22, vid: curVid(), on: true };
    list.push(r);
    o.box.querySelector("#au-list").innerHTML = renderList(); bind();
    await save();
    toast(uz ? "Qo'shildi ✓" : "Added ✓", "ok");
  };
}

// --- Remote immobilizer: anti-theft engine lockout (owner only) ---
async function openImmobilizer() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  const vid = curVid();
  const o = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">🔒 ' + (uz ? "Masofadan bloklash" : "Remote immobilizer") + '</div>' +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:16px;line-height:1.45;">' + (uz ? "Yoqilganda mashina dvigatelini masofadan ishga tushirib bo'lmaydi. O'g'irlikka qarshi himoya." : "When engaged, the engine cannot be remote-started. Anti-theft protection.") + '</div>' +
    '<div id="im-state" style="text-align:center;padding:18px;border-radius:16px;margin-bottom:14px;background:rgba(255,255,255,.04);"><div style="color:#9a9ca2;font-size:13px;">' + (uz ? "Yuklanmoqda…" : "Loading…") + '</div></div>' +
    '<div id="im-toggle" class="obtn" style="height:50px;display:none;"></div>' +
    '<div id="im-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:14px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>',
    360);
  o.box.querySelector("#im-close").onclick = o.close;
  const stateEl = o.box.querySelector("#im-state");
  const btn = o.box.querySelector("#im-toggle");
  const token = () => auth.currentUser.getIdToken();
  let on = false;
  const render = () => {
    stateEl.innerHTML = on
      ? '<div style="font-size:34px;margin-bottom:6px;">🛑</div><div style="color:#ff5252;font-weight:800;font-family:\'Sora\';font-size:17px;">' + (uz ? "Bloklangan" : "Immobilized") + '</div><div style="color:#9a9ca2;font-size:12px;margin-top:3px;">' + (uz ? "Dvigatel ishga tushmaydi" : "Engine start is blocked") + '</div>'
      : '<div style="font-size:34px;margin-bottom:6px;">✅</div><div style="color:#43d684;font-weight:800;font-family:\'Sora\';font-size:17px;">' + (uz ? "Faol" : "Active") + '</div><div style="color:#9a9ca2;font-size:12px;margin-top:3px;">' + (uz ? "Mashina normal ishlaydi" : "Vehicle operates normally") + '</div>';
    stateEl.style.background = on ? "rgba(255,82,82,.1)" : "rgba(67,214,132,.08)";
    btn.style.display = "block";
    btn.textContent = on ? (uz ? "Blokni ochish" : "Release lockout") : (uz ? "Mashinani bloklash" : "Immobilize vehicle");
    btn.style.background = on ? "linear-gradient(135deg,#43d684,#2fa968)" : "linear-gradient(135deg,#ff5252,#c62828)";
  };
  try {
    const r = await fetch(`${CFG.apiBase}/v1/vehicles/${encodeURIComponent(vid)}/immobilizer`, { headers: { Authorization: `Bearer ${await token()}` } });
    if (r.ok) { on = !!(await r.json()).on; render(); }
    else { stateEl.innerHTML = '<div style="color:#ff6a6a;font-size:13px;">' + (uz ? "Holatni o'qib bo'lmadi" : "Could not read state") + '</div>'; }
  } catch (_) { stateEl.innerHTML = '<div style="color:#ff6a6a;font-size:13px;">' + (uz ? "Xatolik" : "Error") + '</div>'; }
  btn.onclick = async () => {
    const next = !on;
    if (next && !confirm(uz ? "Mashinani bloklaysizmi? Dvigatel masofadan ishga tushmaydi." : "Immobilize the vehicle? Remote start will be blocked.")) return;
    btn.style.opacity = ".6"; btn.style.pointerEvents = "none";
    try {
      const r = await fetch(`${CFG.apiBase}/v1/vehicles/${encodeURIComponent(vid)}/immobilizer`, {
        method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${await token()}` },
        body: JSON.stringify({ on: next }),
      });
      if (r.status === 403) { toast(uz ? "Faqat egasi o'zgartira oladi" : "Owner only"); return; }
      if (!r.ok) throw new Error("HTTP " + r.status);
      on = next; render();
      toast(on ? (uz ? "Mashina bloklandi 🔒" : "Vehicle immobilized 🔒") : (uz ? "Blok ochildi ✓" : "Lockout released ✓"), on ? "err" : "ok");
      logAlert({ title: uz ? "Masofadan bloklash" : "Immobilizer", body: on ? (uz ? "Mashina bloklandi" : "Vehicle immobilized") : (uz ? "Blok ochildi" : "Lockout released"), type: "moved_while_locked" });
    } catch (e) {
      toast(uz ? "Xatolik — qayta urinib ko'ring" : "Error — try again", "err");
    } finally { btn.style.opacity = "1"; btn.style.pointerEvents = "auto"; }
  };
}

// --- Driving insights: analytics computed from server trip history ---
async function openInsights() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  const vid = curVid();
  const o = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">📊 ' + (uz ? "Haydash tahlili" : "Driving insights") + '</div>' +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:14px;">' + (uz ? "Sayohatlaringiz asosida tahlil" : "Computed from your trips") + '</div>' +
    '<div id="in-body"><div style="color:#6a6c72;text-align:center;font-size:13px;padding:18px;">' + (uz ? "Yuklanmoqda…" : "Loading…") + '</div></div>' +
    '<div id="in-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:14px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>',
    380);
  o.box.querySelector("#in-close").onclick = o.close;
  const body = o.box.querySelector("#in-body");
  let list = [];
  try {
    const tk = await auth.currentUser.getIdToken();
    const r = await fetch(`${CFG.apiBase}/v1/trips?vid=${encodeURIComponent(vid)}`, { headers: { Authorization: `Bearer ${tk}` } });
    if (r.ok) list = (await r.json()) || [];
  } catch (_) {}
  if (!list.length) {
    body.innerHTML = '<div style="color:#6a6c72;text-align:center;font-size:13px;padding:20px;line-height:1.5;">' + (uz ? "Tahlil uchun ma'lumot yo'q.<br>Bir necha marta yuring." : "Not enough data yet.<br>Drive a few trips first.") + '</div>';
    return;
  }
  const nowS = Date.now() / 1000;
  const totKm = list.reduce((s, t) => s + (t.d || 0), 0);
  const trips = list.length;
  const avg = Math.round(totKm / trips);
  const totSoc = list.reduce((s, t) => s + Math.max(0, (t.ss || 0) - (t.es || 0)), 0);
  const eff = totSoc > 0 ? (totKm / totSoc).toFixed(1) : "—"; // km per % battery
  // Last-7-days bar chart (by day).
  const days = uz ? ["Ya", "Du", "Se", "Ch", "Pa", "Ju", "Sh"] : ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"];
  const buckets = [0, 0, 0, 0, 0, 0, 0]; // index by weekday over last 7 days
  const labels = [];
  for (let i = 6; i >= 0; i--) { const d = new Date(Date.now() - i * 86400000); labels.push(days[d.getDay()]); }
  const dayKm = [0, 0, 0, 0, 0, 0, 0]; // aligned to labels (oldest→newest)
  list.forEach((t) => {
    const ageDays = Math.floor((nowS - (t.st || 0)) / 86400);
    if (ageDays >= 0 && ageDays < 7) dayKm[6 - ageDays] += (t.d || 0);
  });
  const maxKm = Math.max(1, ...dayKm);
  // Eco score: better efficiency + moderate trip length → higher score (0-100).
  const ecoBase = totSoc > 0 ? Math.min(100, Math.round((totKm / totSoc) * 12)) : 70;
  const eco = Math.max(20, Math.min(100, ecoBase));
  const ecoColor = eco >= 75 ? "#43d684" : eco >= 50 ? "#FF9D5C" : "#ff6a6a";
  const card = (big, lbl, sub) => '<div style="flex:1;background:rgba(255,255,255,.04);border-radius:13px;padding:13px 10px;text-align:center;"><div style="color:#fff;font-family:\'Sora\';font-weight:800;font-size:19px;">' + big + '</div><div style="color:#7e8086;font-size:11px;margin-top:2px;">' + lbl + '</div>' + (sub ? '<div style="color:#5a5c62;font-size:10px;">' + sub + '</div>' : '') + '</div>';
  let html = '';
  // Eco score ring-ish header.
  html += '<div style="display:flex;align-items:center;gap:14px;background:rgba(255,255,255,.04);border-radius:14px;padding:14px;margin-bottom:12px;">' +
    '<div style="width:58px;height:58px;border-radius:50%;border:5px solid ' + ecoColor + ';display:flex;align-items:center;justify-content:center;flex-shrink:0;"><span style="color:#fff;font-family:\'Sora\';font-weight:800;font-size:18px;">' + eco + '</span></div>' +
    '<div style="flex:1;"><div style="color:#fff;font-weight:700;font-size:15px;">' + (uz ? "Eko-ball" : "Eco score") + '</div><div style="color:#9a9ca2;font-size:12px;margin-top:2px;">' + (eco >= 75 ? (uz ? "A'lo — tejamkor haydash" : "Great — efficient driving") : eco >= 50 ? (uz ? "Yaxshi — yana yaxshilanadi" : "Good — room to improve") : (uz ? "Tejamkorlikni oshiring" : "Try to drive more gently")) + '</div></div></div>';
  html += '<div style="display:flex;gap:9px;margin-bottom:10px;">' + card(totKm + " km", uz ? "Jami" : "Total") + card(trips, uz ? "Sayohat" : "Trips") + card(avg + " km", uz ? "O'rtacha" : "Avg") + '</div>';
  html += '<div style="display:flex;gap:9px;margin-bottom:14px;">' + card(eff, uz ? "km / 1% batareya" : "km per 1% batt") + card(totSoc + "%", uz ? "Sarflangan quvvat" : "Energy used") + '</div>';
  // Bar chart.
  html += '<div style="color:#7e8086;font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.5px;margin-bottom:9px;">' + (uz ? "So'nggi 7 kun" : "Last 7 days") + '</div>';
  html += '<div style="display:flex;align-items:flex-end;gap:6px;height:90px;background:rgba(255,255,255,.03);border-radius:12px;padding:10px;">';
  for (let i = 0; i < 7; i++) {
    const hpct = Math.round((dayKm[i] / maxKm) * 100);
    html += '<div style="flex:1;display:flex;flex-direction:column;align-items:center;justify-content:flex-end;gap:4px;height:100%;">' +
      '<div style="color:#9a9ca2;font-size:9px;">' + (dayKm[i] > 0 ? dayKm[i] : "") + '</div>' +
      '<div style="width:100%;height:' + Math.max(3, hpct) + '%;background:linear-gradient(180deg,#FF8A2B,#FF4D00);border-radius:5px;min-height:3px;"></div>' +
      '<div style="color:#7e8086;font-size:10px;">' + labels[i] + '</div></div>';
  }
  html += '</div>';
  body.innerHTML = html;
}

// --- Teen / young-driver mode: speed limit + curfew safety monitoring ---
function teenGet() {
  try { return JSON.parse(localStorage.getItem("vd_teen") || "null") || {}; } catch (_) { return {}; }
}
function teenSet(cfg) { localStorage.setItem("vd_teen", JSON.stringify(cfg)); }

let _teenLastAlert = 0; // throttle: at most one alert per minute
function checkTeenMode(v) {
  const cfg = teenGet();
  if (!cfg.on) return;
  const now = Date.now();
  if (now - _teenLastAlert < 60000) return;
  const uz = window.App?.lang === "uz";
  const loc = (v && v.location) || {};
  // Speed limit (km/h).
  if (cfg.speed && typeof loc.speed === "number" && loc.speed > cfg.speed + 2) {
    _teenLastAlert = now;
    const msg = (uz ? "Tezlik oshib ketdi: " : "Over speed limit: ") + Math.round(loc.speed) + " km/h";
    toast("🏎️ " + msg, "err");
    logAlert({ title: uz ? "Yosh haydovchi rejimi" : "Teen mode", body: msg, type: "moved_while_locked" });
    return;
  }
  // Curfew: engine running during restricted hours.
  if (cfg.curfewOn && (v && v.engineOn)) {
    const d = new Date(), cur = d.getHours() * 60 + d.getMinutes();
    const toMin = (s) => { const p = (s || "0:0").split(":"); return (+p[0]) * 60 + (+p[1]); };
    const a = toMin(cfg.cStart || "22:00"), b = toMin(cfg.cEnd || "06:00");
    const inWindow = a <= b ? (cur >= a && cur < b) : (cur >= a || cur < b); // handles overnight
    if (inWindow) {
      _teenLastAlert = now;
      const msg = (uz ? "Komendant soatida haydash (" : "Driving during curfew (") + (cfg.cStart || "22:00") + "–" + (cfg.cEnd || "06:00") + ")";
      toast("🌙 " + msg, "err");
      logAlert({ title: uz ? "Yosh haydovchi rejimi" : "Teen mode", body: msg, type: "moved_while_locked" });
    }
  }
}

async function openTeenMode() {
  const uz = window.App?.lang === "uz";
  const cfg = teenGet();
  if (cfg.speed == null) cfg.speed = 100;
  if (!cfg.cStart) cfg.cStart = "22:00";
  if (!cfg.cEnd) cfg.cEnd = "06:00";
  const o = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">👶 ' + (uz ? "Yosh haydovchi rejimi" : "Teen driver mode") + '</div>' +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:16px;line-height:1.45;">' + (uz ? "Tezlik va komendant soati nazorati. Chegaradan oshilsa, ogohlantirish keladi." : "Speed & curfew monitoring with alerts when limits are crossed.") + '</div>' +
    '<div style="display:flex;align-items:center;gap:10px;background:rgba(255,255,255,.04);border-radius:12px;padding:13px;margin-bottom:12px;"><div style="flex:1;color:#fff;font-weight:700;font-size:15px;">' + (uz ? "Rejim yoqilgan" : "Mode enabled") + '</div><div id="tm-on" style="width:46px;height:26px;border-radius:13px;background:' + (cfg.on ? "#FF6A1A" : "rgba(255,255,255,.15)") + ';position:relative;cursor:pointer;flex-shrink:0;"><div style="width:22px;height:22px;border-radius:50%;background:#fff;position:absolute;top:2px;left:' + (cfg.on ? "22px" : "2px") + ';transition:.2s;"></div></div></div>' +
    '<div style="background:rgba(255,255,255,.04);border-radius:12px;padding:13px;margin-bottom:12px;"><div style="display:flex;justify-content:space-between;margin-bottom:9px;"><span style="color:#fff;font-weight:700;font-size:14px;">' + (uz ? "Tezlik chegarasi" : "Speed limit") + '</span><span id="tm-spv" style="color:#FF9D5C;font-weight:800;font-family:\'Sora\'">' + cfg.speed + ' km/h</span></div>' +
    '<input id="tm-sp" type="range" min="40" max="160" step="10" value="' + cfg.speed + '" style="width:100%;accent-color:#FF6A1A;"></div>' +
    '<div style="background:rgba(255,255,255,.04);border-radius:12px;padding:13px;margin-bottom:14px;"><div style="display:flex;align-items:center;gap:10px;margin-bottom:11px;"><div style="flex:1;color:#fff;font-weight:700;font-size:14px;">' + (uz ? "Komendant soati" : "Curfew hours") + '</div><div id="tm-cf" style="width:42px;height:24px;border-radius:12px;background:' + (cfg.curfewOn ? "#FF6A1A" : "rgba(255,255,255,.15)") + ';position:relative;cursor:pointer;flex-shrink:0;"><div style="width:20px;height:20px;border-radius:50%;background:#fff;position:absolute;top:2px;left:' + (cfg.curfewOn ? "20px" : "2px") + ';transition:.2s;"></div></div></div>' +
    '<div style="display:flex;align-items:center;gap:9px;"><input id="tm-cs" type="time" value="' + cfg.cStart + '" style="flex:1;height:42px;border-radius:10px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 12px;font-size:14px;outline:none;"><span style="color:#7e8086;">—</span><input id="tm-ce" type="time" value="' + cfg.cEnd + '" style="flex:1;height:42px;border-radius:10px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 12px;font-size:14px;outline:none;"></div></div>' +
    '<div id="tm-save" class="obtn" style="height:46px;">' + (uz ? "Saqlash" : "Save") + '</div>' +
    '<div id="tm-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:13px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>',
    380);
  let on = !!cfg.on, curfewOn = !!cfg.curfewOn;
  const tmOn = o.box.querySelector("#tm-on"), tmCf = o.box.querySelector("#tm-cf");
  const paint = (el, val, w) => { el.style.background = val ? "#FF6A1A" : "rgba(255,255,255,.15)"; el.firstElementChild.style.left = val ? (w + "px") : "2px"; };
  tmOn.onclick = () => { on = !on; paint(tmOn, on, 22); };
  tmCf.onclick = () => { curfewOn = !curfewOn; paint(tmCf, curfewOn, 20); };
  const sp = o.box.querySelector("#tm-sp"), spv = o.box.querySelector("#tm-spv");
  sp.oninput = () => { spv.textContent = sp.value + " km/h"; };
  o.box.querySelector("#tm-close").onclick = o.close;
  o.box.querySelector("#tm-save").onclick = () => {
    teenSet({ on, speed: +sp.value, curfewOn, cStart: o.box.querySelector("#tm-cs").value || "22:00", cEnd: o.box.querySelector("#tm-ce").value || "06:00" });
    toast(uz ? "Saqlandi ✓" : "Saved ✓", "ok");
    o.close();
  };
}

// --- Trip history: server-logged drives (distance, energy, route) ---
async function openTrips() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  const vid = curVid();
  const o = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">🛣️ ' + (uz ? "Sayohatlar tarixi" : "Trip history") + '</div>' +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:14px;">' + (uz ? "Har bir yurishingiz avtomatik yoziladi" : "Every drive is logged automatically") + '</div>' +
    '<div id="tr-sum" style="display:none;gap:9px;margin-bottom:13px;"></div>' +
    '<div id="tr-list"><div style="color:#6a6c72;text-align:center;font-size:13px;padding:18px;">' + (uz ? "Yuklanmoqda…" : "Loading…") + '</div></div>' +
    '<div id="tr-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:14px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>',
    380);
  o.box.querySelector("#tr-close").onclick = o.close;
  const fmtDate = (ts) => { const d = new Date(ts * 1000); return d.toLocaleDateString(uz ? "uz" : "en", { day: "numeric", month: "short" }) + " " + String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0"); };
  const dur = (a, b) => { const m = Math.max(0, Math.round((b - a) / 60)); return m >= 60 ? (Math.floor(m / 60) + (uz ? " soat " : "h ") + (m % 60) + (uz ? " daq" : "m")) : (m + (uz ? " daq" : "m")); };
  let list = [];
  try {
    const tk = await auth.currentUser.getIdToken();
    const r = await fetch(`${CFG.apiBase}/v1/trips?vid=${encodeURIComponent(vid)}`, { headers: { Authorization: `Bearer ${tk}` } });
    if (r.ok) list = (await r.json()) || [];
  } catch (_) {}
  const listEl = o.box.querySelector("#tr-list");
  if (!list.length) {
    listEl.innerHTML = '<div style="color:#6a6c72;text-align:center;font-size:13px;padding:20px;line-height:1.5;">' + (uz ? "Hozircha sayohat yo'q.<br>Mashina yurganda bu yerda ko'rinadi." : "No trips yet.<br>They appear here once you drive.") + '</div>';
    return;
  }
  // Summary header (this month).
  const now = Date.now() / 1000, monthAgo = now - 30 * 86400;
  const recent = list.filter((t) => t.st >= monthAgo);
  const totKm = recent.reduce((s, t) => s + (t.d || 0), 0);
  const totSoc = recent.reduce((s, t) => s + Math.max(0, (t.ss || 0) - (t.es || 0)), 0);
  const sum = o.box.querySelector("#tr-sum");
  sum.style.display = "flex";
  const card = (big, lbl) => '<div style="flex:1;background:rgba(255,255,255,.04);border-radius:13px;padding:12px;text-align:center;"><div style="color:#fff;font-family:\'Sora\';font-weight:800;font-size:20px;">' + big + '</div><div style="color:#7e8086;font-size:11px;margin-top:2px;">' + lbl + '</div></div>';
  sum.innerHTML = card(totKm + " km", uz ? "30 kunda" : "Last 30d") + card(recent.length, uz ? "sayohat" : "trips") + card(totSoc + "%", uz ? "batareya" : "battery");
  listEl.innerHTML = list.map((t) => {
    const soc = Math.max(0, (t.ss || 0) - (t.es || 0));
    return '<div style="display:flex;align-items:center;gap:11px;background:rgba(255,255,255,.04);border-radius:12px;padding:11px 12px;margin-bottom:8px;">' +
      '<div style="width:36px;height:36px;border-radius:10px;background:rgba(255,106,26,.14);display:flex;align-items:center;justify-content:center;flex-shrink:0;font-size:16px;">🚗</div>' +
      '<div style="flex:1;min-width:0"><div style="color:#fff;font-weight:700;font-size:15px;font-family:\'Sora\'">' + (t.d || 0) + ' km <span style="font-weight:500;font-size:12px;color:#7e8086">· ' + dur(t.st, t.et) + '</span></div>' +
      '<div style="color:#7e8086;font-size:11px;margin-top:2px;">' + fmtDate(t.st) + (soc > 0 ? ' · −' + soc + '%' : '') + '</div></div></div>';
  }).join("");
}

// --- AI trip planner: plan an EV journey with charging stops (Gemini) ---
async function openTripPlanner() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  const o = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">🗺️ ' + (uz ? "AI sayohat rejasi" : "AI trip planner") + '</div>' +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:14px;">' + (uz ? "Manzilni kiriting — zaryad bekatlari bilan reja tuzaman" : "Enter a destination — I'll plan charging stops") + '</div>' +
    '<input id="tp-dest" placeholder="' + (uz ? "Masalan: Samarqand" : "e.g. Samarqand") + '" style="width:100%;height:46px;border-radius:12px;background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1);color:#fff;padding:0 14px;font-size:15px;outline:none;font-family:Manrope;box-sizing:border-box;margin-bottom:11px;">' +
    '<div id="tp-go" class="obtn" style="height:46px;">' + (uz ? "Reja tuzish" : "Plan trip") + '</div>' +
    '<div id="tp-out" style="margin-top:14px;"></div>' +
    '<div id="tp-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:13px 0 2px;">' + (uz ? "Yopish" : "Close") + '</div>',
    380);
  o.box.querySelector("#tp-close").onclick = o.close;
  const out = o.box.querySelector("#tp-out");
  const inp = o.box.querySelector("#tp-dest");
  const plan = async () => {
    const dest = inp.value.trim();
    if (!dest) { inp.focus(); return; }
    out.innerHTML = '<div style="color:#9a9ca2;text-align:center;font-size:13px;padding:14px;">' + (uz ? "AI reja tuzmoqda…" : "AI is planning…") + '</div>';
    try {
      const tk = await auth.currentUser.getIdToken();
      const langMap = { uz: "Uzbek", ru: "Russian", en: "English" };
      const res = await fetch(`${CFG.apiBase}/v1/tripplan`, {
        method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${tk}` },
        body: JSON.stringify({ dest, car: carContext(), lang: langMap[window.App?.lang] || "Uzbek" }),
      });
      if (res.status === 402) { out.innerHTML = '<div style="color:#FF8A3D;text-align:center;font-size:13px;padding:12px;">' + (uz ? "Bu Premium funksiya." : "Premium feature.") + '</div>'; return; }
      if (!res.ok) throw new Error("HTTP " + res.status);
      const p = await res.json();
      const okc = p.feasible ? "#43d684" : "#FF8A3D";
      const icon = p.feasible ? "✅" : "⚠️";
      let html = '<div style="background:rgba(255,255,255,.04);border-radius:13px;padding:13px;">';
      html += '<div style="display:flex;align-items:center;gap:8px;margin-bottom:7px;"><span style="font-size:16px">' + icon + '</span><b style="color:' + okc + ';font-size:14px;">' + (p.feasible ? (uz ? "Yetib borasiz" : "Reachable") : (uz ? "Zaryad kerak" : "Charging needed")) + '</b></div>';
      html += '<div style="font-size:13px;color:#e8e9ec;line-height:1.5;">' + escapeHtml(p.summary || "") + '</div>';
      const meta = [];
      if (p.distanceKm) meta.push((uz ? "Masofa " : "Distance ") + p.distanceKm + " km");
      if (typeof p.arrivalSoc === "number" && p.arrivalSoc > 0) meta.push((uz ? "Yetib borganda " : "On arrival ") + p.arrivalSoc + "%");
      if (meta.length) html += '<div style="color:#7e8086;font-size:11px;margin-top:6px;">' + meta.join(" · ") + '</div>';
      html += '</div>';
      if (Array.isArray(p.stops) && p.stops.length) {
        html += '<div style="color:#7e8086;font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.5px;margin:13px 0 7px;">⚡ ' + (uz ? "Zaryad bekatlari" : "Charging stops") + '</div>';
        html += p.stops.map((s) => '<div style="display:flex;align-items:center;gap:11px;background:rgba(255,138,43,.08);border-radius:11px;padding:10px 12px;margin-bottom:7px;">' +
          '<div style="width:30px;height:30px;border-radius:8px;background:rgba(255,138,43,.18);display:flex;align-items:center;justify-content:center;flex-shrink:0;">⚡</div>' +
          '<div style="flex:1;min-width:0"><div style="color:#fff;font-weight:700;font-size:14px;">' + escapeHtml(s.name || "") + (s.atKm ? ' <span style="font-weight:500;font-size:11px;color:#7e8086">· ' + s.atKm + ' km</span>' : '') + '</div>' +
          (s.note ? '<div style="color:#9a9ca2;font-size:11px;margin-top:1px;">' + escapeHtml(s.note) + '</div>' : '') + '</div></div>').join("");
      }
      if (Array.isArray(p.tips) && p.tips.length) {
        html += '<div style="color:#7e8086;font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.5px;margin:13px 0 7px;">💡 ' + (uz ? "Maslahatlar" : "Tips") + '</div>';
        html += p.tips.map((t) => '<div style="color:#c8cace;font-size:12px;line-height:1.5;padding-left:14px;position:relative;margin-bottom:4px;"><span style="position:absolute;left:0;color:#FF8A3D;">•</span>' + escapeHtml(t) + '</div>').join("");
      }
      out.innerHTML = html;
    } catch (e) {
      out.innerHTML = '<div style="color:#ff6a6a;text-align:center;font-size:13px;padding:12px;">' + (uz ? "Xatolik — qayta urinib ko'ring." : "Error — try again.") + '</div>';
    }
  };
  o.box.querySelector("#tp-go").onclick = plan;
  inp.addEventListener("keydown", (e) => { if (e.key === "Enter") plan(); });
  setTimeout(() => inp.focus(), 100);
}

// --- Guest keys (owner): create / list / revoke time-limited shared access ---
async function openGuestKeys() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  const vid = curVid();
  const m = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:4px;">' + (uz ? "Mehmon kalitlari" : "Guest keys") + "</div>" +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:16px;line-height:1.45;">' + (uz ? "Vaqtinchalik, cheklangan kirish. Kod — kalitning o‘zi; hisob shart emas." : "Time-limited, scoped access. The code is the key — no account needed.") + "</div>" +
    '<div style="color:#7e8086;font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.5px;margin-bottom:7px;">' + (uz ? "Amal qilish muddati" : "Valid for") + "</div>" +
    '<div id="gk-hours" style="display:flex;gap:8px;margin-bottom:14px;"></div>' +
    '<div style="color:#7e8086;font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.5px;margin-bottom:7px;">' + (uz ? "Ruxsatlar" : "Permissions") + "</div>" +
    '<div id="gk-scope" style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:18px;"></div>' +
    '<div id="gk-create" class="obtn">' + (uz ? "Kalit yaratish" : "Create key") + "</div>" +
    '<div id="gk-list" style="margin-top:16px;"></div>' +
    '<div id="gk-cancel" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "Yopish" : "Close") + "</div>"
  );
  let hours = 3;
  const scope = { unlock: true, lock: true, start: false };
  const pill = (active) => "padding:8px 13px;border-radius:11px;font-size:13px;font-weight:700;cursor:pointer;user-select:none;" +
    (active ? "background:linear-gradient(150deg,#FF8A2B,#FF4D00);color:#fff;" : "background:rgba(255,255,255,.06);color:#c8cace;");
  const hoursWrap = m.box.querySelector("#gk-hours");
  [[1, "1 " + (uz ? "soat" : "h")], [3, "3 " + (uz ? "soat" : "h")], [24, "24 " + (uz ? "soat" : "h")], [72, "3 " + (uz ? "kun" : "d")]].forEach(([h, lbl]) => {
    const b = document.createElement("div"); b.textContent = lbl; b.style.cssText = pill(h === hours);
    b.onclick = () => { hours = h; [...hoursWrap.children].forEach((c, i) => c.style.cssText = pill([1, 3, 24, 72][i] === hours)); };
    hoursWrap.appendChild(b);
  });
  const scopeWrap = m.box.querySelector("#gk-scope");
  [["unlock", uz ? "Ochish" : "Unlock"], ["lock", uz ? "Yopish" : "Lock"], ["start", uz ? "Yurgizish" : "Start"]].forEach(([k, lbl]) => {
    const b = document.createElement("div"); b.textContent = lbl; b.style.cssText = pill(scope[k]);
    b.onclick = () => { scope[k] = !scope[k]; b.style.cssText = pill(scope[k]); };
    scopeWrap.appendChild(b);
  });
  m.box.querySelector("#gk-cancel").onclick = m.close;

  async function token() { return auth.currentUser.getIdToken(); }
  async function loadList() {
    const el = m.box.querySelector("#gk-list");
    try {
      const r = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/guestkeys`, { headers: { Authorization: `Bearer ${await token()}` } });
      const d = await r.json();
      const keys = (d.keys || []);
      if (!keys.length) { el.innerHTML = '<div style="color:#6a6c72;font-size:12px;text-align:center;padding:6px;">' + (uz ? "Faol kalit yo‘q" : "No active keys") + "</div>"; return; }
      el.innerHTML = keys.map((k) => {
        const left = Math.max(0, Math.round((k.expiresAt - Date.now() / 1000) / 3600));
        return '<div style="display:flex;align-items:center;gap:10px;background:rgba(255,255,255,.04);border-radius:12px;padding:10px 12px;margin-bottom:8px;">' +
          '<div style="flex:1;"><div style="color:#fff;font-weight:800;font-family:\'Sora\';letter-spacing:1px;">' + escapeHtml(k.code) + "</div>" +
          '<div style="color:#7e8086;font-size:11px;margin-top:2px;">' + escapeHtml((k.scope || []).join(" · ")) + " · " + left + (uz ? " soat qoldi" : "h left") + "</div></div>" +
          '<div data-revoke="' + escapeHtml(k.code) + '" style="color:#ff6a6a;font-size:12px;font-weight:700;cursor:pointer;">' + (uz ? "Bekor" : "Revoke") + "</div></div>";
      }).join("");
      el.querySelectorAll("[data-revoke]").forEach((b) => b.onclick = async () => {
        await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/guestkeys/${b.getAttribute("data-revoke")}`, { method: "DELETE", headers: { Authorization: `Bearer ${await token()}` } });
        loadList();
      });
    } catch (e) { el.innerHTML = ""; }
  }
  m.box.querySelector("#gk-create").onclick = async () => {
    const sel = Object.keys(scope).filter((k) => scope[k]);
    if (!sel.length) { toast(uz ? "Kamida bitta ruxsat tanlang" : "Pick at least one permission"); return; }
    try {
      const r = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/guestkeys`, {
        method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${await token()}` },
        body: JSON.stringify({ hours, scope: sel, label: "" }),
      });
      if (!r.ok) throw new Error("HTTP " + r.status);
      const k = await r.json();
      haptic([10, 40, 12]);
      shareGuestKey(k.code, hours, uz);
      loadList();
    } catch (e) { toast((uz ? "Xato: " : "Failed: ") + e.message, "err"); }
  };
  loadList();
}

function shareGuestKey(code, hours, uz) {
  const text = (uz ? "VoltDrive mehmon kaliti: " : "VoltDrive guest key: ") + code +
    (uz ? ` (${hours} soat amal qiladi). Ilovada "Mehmon kalitingiz bormi?" orqali kiriting.` : ` (valid ${hours}h). Enter it via "Have a guest key?" in the app.`);
  if (navigator.share) { navigator.share({ title: "VoltDrive", text }).catch(() => {}); }
  else if (navigator.clipboard) { navigator.clipboard.writeText(text).then(() => toast(uz ? "Kod nusxalandi ✓" : "Code copied ✓", "ok")); }
  else { toast(code, "ok"); }
}

// --- Guest entry: redeem a code with no account, then a minimal control panel ---
async function openGuestEntry() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase) { toast("API offline"); return; }
  const m = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:6px;">' + (uz ? "Mehmon kirishi" : "Guest access") + "</div>" +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:16px;">' + (uz ? "Sizga berilgan kalit kodini kiriting." : "Enter the key code you were given.") + "</div>" +
    '<input id="ge-code" maxlength="8" placeholder="ABCD2345" style="width:100%;box-sizing:border-box;text-align:center;letter-spacing:4px;text-transform:uppercase;font-family:\'Sora\';font-weight:800;font-size:22px;background:rgba(255,255,255,.06);border:1px solid rgba(255,255,255,.12);border-radius:14px;color:#fff;padding:14px;margin-bottom:16px;">' +
    '<div id="ge-go" class="obtn">' + (uz ? "Davom etish" : "Continue") + "</div>" +
    '<div id="ge-cancel" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "Bekor qilish" : "Cancel") + "</div>"
  );
  m.box.querySelector("#ge-cancel").onclick = m.close;
  m.box.querySelector("#ge-go").onclick = async () => {
    const code = (m.box.querySelector("#ge-code").value || "").trim().toUpperCase();
    if (code.length < 6) { toast(uz ? "Kodni kiriting" : "Enter the code"); return; }
    try {
      const r = await fetch(`${CFG.apiBase}/v1/guest/redeem`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ code }) });
      if (!r.ok) throw new Error(uz ? "Yaroqsiz yoki muddati o‘tgan" : "Invalid or expired");
      const info = await r.json();
      m.close();
      guestPanel(code, info, uz);
    } catch (e) { toast(e.message, "err"); }
  };
}

function guestPanel(code, info, uz) {
  const labels = { unlock: uz ? "Ochish" : "Unlock", lock: uz ? "Yopish" : "Lock", start: uz ? "Yurgizish" : "Start" };
  const icons = { unlock: "lock-open", lock: "lock", start: "power" };
  const left = Math.max(0, Math.round((info.expiresAt - Date.now() / 1000) / 3600));
  const btns = (info.scope || []).map((a) =>
    '<div data-act="' + a + '" class="obtn" style="margin-top:10px;background:rgba(255,255,255,.06);color:#fff;"><i data-lucide="' + (icons[a] || "circle") + '" style="width:18px;height:18px;margin-right:6px;"></i>' + (labels[a] || a) + "</div>"
  ).join("");
  const m = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:2px;">' + (info.vehicleName || "Vehicle") + "</div>" +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:18px;">' + (uz ? "Mehmon kirishi · " : "Guest access · ") + left + (uz ? " soat qoldi" : "h left") + "</div>" +
    btns +
    '<div id="gp-close" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:14px 0 2px;">' + (uz ? "Chiqish" : "Exit") + "</div>"
  );
  m.box.querySelector("#gp-close").onclick = m.close;
  m.box.querySelectorAll("[data-act]").forEach((b) => b.onclick = async () => {
    const action = b.getAttribute("data-act");
    try {
      const r = await fetch(`${CFG.apiBase}/v1/guest/command`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ code, action }) });
      if (!r.ok) throw new Error(uz ? "Bajarilmadi" : "Failed");
      toast(labels[action] + " ✓", "ok");
      haptic([10, 30, 10]);
    } catch (e) { toast(e.message, "err"); }
  });
}

// --- Geofence (safe zone): centre on the car, set radius, enable ---
async function openGeofence() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  if (!requirePremium()) return; // Safe zone is a Premium feature
  const vid = curVid();
  let cur = { enabled: false, lat: 0, lng: 0, radiusM: 300 };
  try {
    const token = await auth.currentUser.getIdToken();
    const r = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/geofence`, { headers: { Authorization: `Bearer ${token}` } });
    if (r.ok) cur = await r.json();
  } catch (e) {}
  const m = vdModal(
    '<div style="font-family:\'Sora\';font-weight:800;font-size:18px;color:#fff;margin-bottom:6px;">' + (uz ? "Xavfsiz hudud" : "Safe zone") + "</div>" +
    '<div style="color:#9a9ca2;font-size:12px;margin-bottom:16px;line-height:1.45;">' + (uz ? "Mashina shu hududdan chiqsa, server ogohlantiradi. Markaz — mashinaning hozirgi joyi." : "If the car leaves this area, the server raises an alert. Centred on the car's current location.") + "</div>" +
    '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:14px;"><span style="color:#fff;font-weight:600;font-size:14px;">' + (uz ? "Yoqilgan" : "Enabled") + '</span><div id="gf-tgl" class="tgl ' + (cur.enabled ? "on" : "off") + '"></div></div>' +
    '<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:18px;"><span style="color:#fff;font-weight:600;font-size:14px;">' + (uz ? "Radius" : "Radius") + '</span><div style="display:flex;align-items:center;gap:12px;"><div id="gf-minus" class="ibtn" style="width:34px;height:34px;font-size:20px;">−</div><span id="gf-rad" style="color:#fff;font-family:\'Sora\';font-weight:700;font-size:16px;min-width:64px;text-align:center;"></span><div id="gf-plus" class="ibtn" style="width:34px;height:34px;font-size:20px;">+</div></div></div>' +
    '<div id="gf-save" class="obtn">' + (uz ? "Saqlash" : "Save") + "</div>" +
    '<div id="gf-cancel" style="text-align:center;color:#9a9ca2;font-weight:700;font-size:14px;cursor:pointer;padding:12px 0 2px;">' + (uz ? "Bekor qilish" : "Cancel") + "</div>"
  );
  let radius = cur.radiusM || 300;
  const radEl = m.box.querySelector("#gf-rad");
  const fmt = () => radEl.textContent = radius >= 1000 ? (radius / 1000).toFixed(1) + " km" : radius + " m";
  fmt();
  const tgl = m.box.querySelector("#gf-tgl");
  tgl.onclick = () => { tgl.classList.toggle("on"); tgl.classList.toggle("off"); };
  m.box.querySelector("#gf-minus").onclick = () => { radius = Math.max(100, radius - (radius > 1000 ? 500 : 100)); fmt(); };
  m.box.querySelector("#gf-plus").onclick = () => { radius = Math.min(20000, radius + (radius >= 1000 ? 500 : 100)); fmt(); };
  m.box.querySelector("#gf-cancel").onclick = m.close;
  m.box.querySelector("#gf-save").onclick = async () => {
    const enabled = tgl.classList.contains("on");
    let lat = cur.lat, lng = cur.lng;
    try { // centre on the car's live location
      const token = await auth.currentUser.getIdToken();
      const sr = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}`, { headers: { Authorization: `Bearer ${token}` } });
      if (sr.ok) { const snap = await sr.json(); if (snap.location) { lat = snap.location.lat; lng = snap.location.lng; } }
      const r = await fetch(`${CFG.apiBase}/v1/vehicles/${vid}/geofence`, {
        method: "PUT", headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify({ enabled, lat, lng, radiusM: radius }),
      });
      if (!r.ok) throw new Error("HTTP " + r.status);
      m.close();
      const badge = document.getElementById("geo-badge");
      if (badge) { badge.setAttribute("data-i18n", enabled ? "profile.bio.on" : "profile.bio.off"); App.applyLang(); }
      toast(enabled ? (uz ? "Xavfsiz hudud yoqildi ✓" : "Safe zone enabled ✓") : (uz ? "O‘chirildi" : "Disabled"), "ok");
      haptic([10, 40, 12]);
    } catch (e) { toast((uz ? "Xato: " : "Failed: ") + e.message, "err"); }
  };
}

// findCar flashes the lights and sounds the horn so you can spot the car in a
// car park, then turns the lights back off after a few seconds.
function findCar() {
  const uz = window.App?.lang === "uz";
  if (!CFG.apiBase || !auth?.currentUser) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  haptic([12, 40, 12]);
  toast(uz ? "Faralar yoqildi va signal chalindi 📍" : "Flashing lights & horn 📍", "ok");
  sendCommand("lights", { on: true });
  sendCommand("horn");
  setTimeout(() => sendCommand("lights", { on: false }), 6000);
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
  const bioBtn = document.getElementById("vd-lock-bio");
  if (bioBtn) bioBtn.style.display = "none"; // no biometrics during PIN setup
  const key = "vd_pin_" + u.uid;
  const hasPin = !!localStorage.getItem(key);
  lockState = {
    uid: u.uid,
    key,
    resolve: (ok) => { if (ok) toast(uz ? "PIN yangilandi ✓" : "PIN updated ✓", "ok"); },
    fromProfile: true, // cancelling here returns to the app, never signs out
    // When a PIN already exists, require the current one before allowing a change.
    mode: hasPin ? "change" : "setup",
    stage: hasPin ? "verify" : "first",
    first: "", entry: "",
  };
  setLockTitle(hasPin ? (uz ? "Joriy PIN ni kiriting" : "Enter current PIN")
                      : (uz ? "Yangi PIN yarating" : "Create a new PIN"));
  setLockOut(uz ? "Bekor qilish" : "Cancel");
  updateDots();
  showLock();
}

// --- Biometric unlock (WebAuthn platform authenticator) ---
//
// Registers a platform credential (Face ID / fingerprint) tied to this device
// and offers it as a faster alternative to the PIN on the lock screen. There is
// no server to verify the signature, so this is a device-side presence gate:
// the OS performs user-verification with the registered credential, and we
// treat a successful ceremony as "unlocked". The PIN stays as the fallback.

function bioSupported() {
  return !!(window.PublicKeyCredential && navigator.credentials && navigator.credentials.create);
}

function b64urlFromBuf(buf) {
  let s = "";
  const b = new Uint8Array(buf);
  for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function bufFromB64url(str) {
  const s = str.replace(/-/g, "+").replace(/_/g, "/");
  const bin = atob(s + "===".slice((s.length + 3) % 4));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

async function bioRegister(uid) {
  const challenge = crypto.getRandomValues(new Uint8Array(32));
  const cred = await navigator.credentials.create({
    publicKey: {
      challenge,
      rp: { name: "VoltDrive", id: location.hostname },
      user: { id: new TextEncoder().encode(uid), name: "voltdrive", displayName: "VoltDrive" },
      pubKeyCredParams: [{ type: "public-key", alg: -7 }, { type: "public-key", alg: -257 }],
      authenticatorSelection: { authenticatorAttachment: "platform", userVerification: "required", residentKey: "preferred" },
      timeout: 60000,
      attestation: "none",
    },
  });
  localStorage.setItem("vd_bio_" + uid, b64urlFromBuf(cred.rawId));
  return true;
}

async function bioVerify(uid) {
  const id = localStorage.getItem("vd_bio_" + uid);
  if (!id) return false;
  const challenge = crypto.getRandomValues(new Uint8Array(32));
  await navigator.credentials.get({
    publicKey: {
      challenge,
      allowCredentials: [{ type: "public-key", id: bufFromB64url(id) }],
      userVerification: "required",
      rpId: location.hostname,
      timeout: 60000,
    },
  });
  return true; // resolves only after a successful on-device user-verification
}

function updateBioBadge(uid) {
  const el = document.getElementById("bio-badge");
  if (!el) return;
  const on = !!localStorage.getItem("vd_bio_" + uid);
  el.setAttribute("data-i18n", on ? "profile.bio.on" : "profile.bio.off");
  if (window.App) App.applyLang();
}

async function toggleBiometric() {
  const uz = window.App?.lang === "uz";
  const u = auth?.currentUser;
  if (!u) { toast(uz ? "Avval tizimga kiring" : "Sign in first"); return; }
  if (!bioSupported()) { toast(uz ? "Qurilma biometrikani qo'llab-quvvatlamaydi" : "This device has no biometrics"); return; }
  const key = "vd_bio_" + u.uid;
  if (localStorage.getItem(key)) {
    localStorage.removeItem(key);
    updateBioBadge(u.uid);
    toast(uz ? "Biometrik kirish o'chirildi" : "Biometric unlock disabled");
    return;
  }
  try {
    await bioRegister(u.uid);
    updateBioBadge(u.uid);
    toast(uz ? "Biometrik kirish yoqildi ✓" : "Biometric unlock enabled ✓", "ok");
    haptic([10, 40, 12]);
  } catch (e) {
    toast(uz ? "Yoqib bo'lmadi" : "Couldn't enable biometrics");
  }
}

// Offer biometric on the unlock screen and auto-prompt once.
function maybeOfferBio(uid, hasPin) {
  const btn = document.getElementById("vd-lock-bio");
  if (!btn) return;
  const ready = hasPin && bioSupported() && localStorage.getItem("vd_bio_" + uid);
  btn.style.display = ready ? "flex" : "none";
  if (ready) setTimeout(tryBioUnlock, 350);
}

async function tryBioUnlock() {
  if (!lockState || lockState.mode !== "unlock") return;
  const uz = window.App?.lang === "uz";
  try {
    if (await bioVerify(lockState.uid)) finishUnlock();
  } catch (e) {
    toast(uz ? "Biometrika bekor qilindi — PIN kiriting" : "Biometric cancelled — enter PIN");
  }
}

// --- PIN lock (security gate on every app open) ---
//
// After the first registration the user sets a 4-digit PIN. On every launch,
// while the email session stays signed in (Firebase persists it), the app is
// locked behind the PIN — so the phone owner re-verifies identity without
// re-typing the email/password. "Sign out" lives in the profile.
//
// PIN hashing (PBKDF2-SHA256 + per-PIN salt) and verification live in
// lib/voltdrive-core.js (imported above) so they can be unit-tested.


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
    setLockOut(uz ? "Boshqa akkaunt / Chiqish" : "Use another account / Sign out");
    updateDots();
    showLock();
    maybeOfferBio(uid, !!stored);
    updateBioBadge(uid);
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
    '<div id="vd-lock-bio" style="display:none;align-items:center;gap:8px;color:#fff;font-weight:700;font-size:14px;cursor:pointer;padding:10px 18px;border-radius:14px;background:rgba(255,255,255,.06);margin-top:2px;">' +
    '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#FF8A3D" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 10v4"/><path d="M12 2a10 10 0 0 0-10 10"/><path d="M22 12A10 10 0 0 0 12 2"/><path d="M5 19.5C5.5 18 6 15 6 12a6 6 0 0 1 .34-2"/><path d="M17.29 21.02c.12-.6.43-2.3.5-3.02"/><path d="M9 18.5a4 4 0 0 1-.36-2.5"/><path d="M12 12a2 2 0 0 0-2 2c0 1.02-.1 2.51-.26 4"/></svg>' +
    '<span>' + (uz ? "Biometrika bilan ochish" : "Unlock with biometrics") + "</span></div>" +
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

  // Biometric unlock button.
  o.querySelector("#vd-lock-bio").addEventListener("click", tryBioUnlock);

  // Bottom action: from the profile PIN-change flow it just cancels (stays
  // signed in); from the launch lock gate it signs out / switches account.
  o.querySelector("#vd-lock-out").addEventListener("click", async () => {
    if (lockState?.fromProfile) {
      hideLock();
      const r = lockState.resolve;
      lockState = null;
      if (r) r(false);
      return;
    }
    try { if (unsubscribe) { unsubscribe(); unsubscribe = null; } await signOut(auth); } catch (_) {}
    hideLock();
    if (lockState?.resolve) lockState.resolve(false);
    lockState = null;
    if (window.App) App.resetRoot("auth");
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

function setLockOut(t) {
  const el = document.getElementById("vd-lock-out");
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
  if (lockState.mode === "change") {
    // Verify the current PIN before allowing a new one to be set.
    const ok = await pinVerify(lockState.uid, pin, localStorage.getItem(lockState.key));
    if (ok) {
      lockState.mode = "setup";
      lockState.stage = "first";
      lockState.first = "";
      lockState.entry = "";
      setLockTitle(uz ? "Yangi PIN yarating" : "Create a new PIN");
      updateDots();
    } else {
      shakeLock();
      lockState.entry = "";
      setLockTitle(uz ? "Noto'g'ri PIN" : "Wrong PIN");
      updateDots();
    }
    return;
  }
  if (lockState.mode === "setup") {
    if (lockState.stage === "first") {
      lockState.first = pin;
      lockState.entry = "";
      lockState.stage = "confirm";
      setLockTitle(uz ? "PIN ni takrorlang" : "Confirm your PIN");
      updateDots();
    } else {
      if (pin === lockState.first) {
        localStorage.setItem(lockState.key, await pinSerialize(lockState.uid, pin));
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
    const stored = localStorage.getItem(lockState.key);
    const ok = await pinVerify(lockState.uid, pin, stored);
    if (ok) {
      // Upgrade legacy (plain-SHA256) PINs to salted PBKDF2 on first match.
      if (isLegacyPin(stored)) {
        localStorage.setItem(lockState.key, await pinSerialize(lockState.uid, pin));
      }
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
