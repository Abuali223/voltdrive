// VoltDrive live telemetry for the web app.
//
// Signs the user in with Firebase Auth, then subscribes to their vehicle's
// node in Realtime Database. The backend mirrors each snapshot to
// /vehicles/{id}, so the UI updates instantly with no polling.
//
// Usage in index.html:
//   <script type="module">
//     import { signIn, watchVehicle } from "./web/live.js";
//     await signIn(email, password);
//     watchVehicle("voyah-001", (snap) => { /* update UI */ });
//   </script>
import { app } from "./firebase-config.js";
import {
  getAuth,
  signInWithEmailAndPassword,
  onAuthStateChanged,
} from "https://www.gstatic.com/firebasejs/10.13.0/firebase-auth.js";
import {
  getDatabase,
  ref,
  onValue,
} from "https://www.gstatic.com/firebasejs/10.13.0/firebase-database.js";

const auth = getAuth(app);
const db = getDatabase(app);

/** Sign in and resolve once authenticated. */
export function signIn(email, password) {
  return signInWithEmailAndPassword(auth, email, password);
}

/** Returns the current Firebase ID token (for calling the Go backend). */
export async function idToken() {
  return auth.currentUser ? auth.currentUser.getIdToken() : null;
}

/** Subscribe to live snapshots of a vehicle. Returns an unsubscribe fn. */
export function watchVehicle(vehicleId, onSnapshot) {
  const node = ref(db, `vehicles/${vehicleId}`);
  return onValue(node, (snap) => {
    const data = snap.val();
    if (data) onSnapshot(data);
  });
}

/** Notify when auth state changes (signed in / out). */
export function onAuth(cb) {
  return onAuthStateChanged(auth, cb);
}

/**
 * Send a control command to the Go backend (lock/unlock/start/stop/climate).
 * The backend verifies the same Firebase ID token and checks RBAC.
 */
export async function sendCommand(apiBase, vehicleId, action, body) {
  const token = await idToken();
  const res = await fetch(`${apiBase}/v1/vehicles/${vehicleId}/${action}`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error(`command ${action} failed: ${res.status}`);
  return res.json();
}
