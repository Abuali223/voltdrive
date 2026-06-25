// VoltDrive runtime configuration (plain script, loaded before app-live.js).
//
// - firebase: web config (apiKey here is NOT a secret; security comes from
//   Firebase Auth + Realtime Database rules).
// - apiBase: your deployed Go backend URL (Cloud Run). While empty/unset,
//   commands stay local (demo); live RTDB telemetry still works once signed in.
window.VOLTDRIVE_CONFIG = {
  firebase: {
    apiKey: "AIzaSyBr7Q4bA7h43DLL1P2QeHaqkvKlqWWyET4",
    authDomain: "eldi-79bf9.firebaseapp.com",
    databaseURL: "https://eldi-79bf9-default-rtdb.firebaseio.com",
    projectId: "eldi-79bf9",
    storageBucket: "eldi-79bf9.firebasestorage.app",
    messagingSenderId: "236709813792",
    appId: "1:236709813792:web:caf8604a1dd96442e86645",
    measurementId: "G-303SPGX6VP",
  },
  // Deployed Go backend (Cloud Run).
  apiBase: "https://voltdrive-api-iv6iceq35q-ew.a.run.app",
  // Web Push (FCM) public VAPID key from Firebase Console → Cloud Messaging.
  vapidKey: "BAn1RT5ijNZwns1IW8WGhWU3ypMYrv7Q3O3CmkeqT3ZTCwrj0YGDJOrMY-6J-nzphj_3Zv5E7duBuJZGKgekYkE",

  // --- White-label branding (a dealer/OEM can rebrand by editing this) ---
  branding: {
    name: "VoltDrive",
    tagline: "Smart EV Control",
    accent: "#FF6A1A", // primary accent; "" keeps the default orange
  },

  // --- Subscription / billing (temporary manual flow until a gateway) ---
  payment: {
    // Card the user transfers to; the admin then activates the plan.
    card: "8600 1234 5678 9012",
    holder: "VoltDrive LLC",
    prices: { "1m": "39 000", "2m": "69 000", "1y": "349 000" }, // UZS
  },
};
