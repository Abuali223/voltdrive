// Firebase web configuration for VoltDrive (project: eldi-79bf9).
//
// NOTE: the web apiKey is NOT a secret — it only identifies the Firebase
// project to Google. Real protection comes from Firebase Auth + the
// Realtime Database security rules in database.rules.json. (Do not put the
// service-account key or any server secret here.)
import { initializeApp } from "https://www.gstatic.com/firebasejs/10.13.0/firebase-app.js";

export const firebaseConfig = {
  apiKey: "AIzaSyBr7Q4bA7h43DLL1P2QeHaqkvKlqWWyET4",
  authDomain: "eldi-79bf9.firebaseapp.com",
  databaseURL: "https://eldi-79bf9-default-rtdb.firebaseio.com",
  projectId: "eldi-79bf9",
  storageBucket: "eldi-79bf9.firebasestorage.app",
  messagingSenderId: "236709813792",
  appId: "1:236709813792:web:caf8604a1dd96442e86645",
  measurementId: "G-303SPGX6VP",
};

export const app = initializeApp(firebaseConfig);
