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
  vapidKey: "QvHZLp-mJqsvt5xhhMceAkTIlQVRdzZ0zYFthFKeAXM",
};
