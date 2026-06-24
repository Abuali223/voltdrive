import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import 'services/api_client.dart';
import 'state/vehicle_controller.dart';
import 'screens/home_screen.dart';

/// Backend base URL.
///   - Android emulator -> http://10.0.2.2:8080
///   - iOS simulator    -> http://localhost:8080
///   - Production        -> your Cloud Run URL
/// Override at build time: --dart-define=API_BASE=https://voltdrive-api-xxxx.run.app
const apiBase = String.fromEnvironment('API_BASE', defaultValue: 'http://10.0.2.2:8080');

void main() {
  // Dev auth token (uid:email). In production this returns the Firebase
  // Auth ID token: () => FirebaseAuth.instance.currentUser?.getIdToken().
  Future<String?> devToken() async => 'u-ali:ali@voltdrive.uz';

  final api = ApiClient(baseUrl: apiBase, tokenProvider: devToken);

  runApp(
    ChangeNotifierProvider(
      create: (_) => VehicleController(api),
      child: const VoltDriveApp(),
    ),
  );
}

class VoltDriveApp extends StatelessWidget {
  const VoltDriveApp({super.key});
  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'VoltDrive',
      debugShowCheckedModeBanner: false,
      theme: ThemeData.dark(useMaterial3: true).copyWith(
        scaffoldBackgroundColor: const Color(0xFF0E0F13),
        colorScheme: const ColorScheme.dark(primary: Color(0xFFFF6A1A)),
      ),
      home: const HomeScreen(),
    );
  }
}
