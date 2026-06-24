// Firebase Cloud Messaging background handler. Runs as its own service worker
// (separate from sw.js) and shows notifications when the app is closed/backgrounded.
importScripts("https://www.gstatic.com/firebasejs/10.13.0/firebase-app-compat.js");
importScripts("https://www.gstatic.com/firebasejs/10.13.0/firebase-messaging-compat.js");

// Public Firebase web config (same as voltdrive.config.js — not secret).
firebase.initializeApp({
  apiKey: "AIzaSyBr7Q4bA7h43DLL1P2QeHaqkvKlqWWyET4",
  authDomain: "eldi-79bf9.firebaseapp.com",
  projectId: "eldi-79bf9",
  storageBucket: "eldi-79bf9.firebasestorage.app",
  messagingSenderId: "236709813792",
  appId: "1:236709813792:web:caf8604a1dd96442e86645",
});

const messaging = firebase.messaging();

messaging.onBackgroundMessage((payload) => {
  const n = payload.notification || {};
  self.registration.showNotification(n.title || "VoltDrive", {
    body: n.body || "",
    icon: "/icons/icon-192.png",
    badge: "/icons/icon-192.png",
    data: payload.data || {},
    vibrate: [60, 30, 60],
  });
});

// Focus/open the app when a notification is tapped.
self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(
    clients.matchAll({ type: "window", includeUncontrolled: true }).then((list) => {
      for (const c of list) { if ("focus" in c) return c.focus(); }
      if (clients.openWindow) return clients.openWindow("/");
    })
  );
});
