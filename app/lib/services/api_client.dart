import 'dart:convert';
import 'package:http/http.dart' as http;
import '../models/vehicle.dart';

/// ApiClient talks to the VoltDrive Go backend.
///
/// Every request carries the Firebase ID token as a Bearer credential.
/// In local dev the token is "uid:email" (accepted by DevVerifier);
/// in production it is the real Firebase Auth ID token.
class ApiClient {
  ApiClient({required this.baseUrl, this.tokenProvider});

  /// e.g. http://10.0.2.2:8080 (Android emulator) or your Cloud Run URL.
  final String baseUrl;

  /// Returns the current auth token, or null if signed out.
  final Future<String?> Function()? tokenProvider;

  Future<Map<String, String>> _headers() async {
    final token = await tokenProvider?.call();
    return {
      'Content-Type': 'application/json',
      if (token != null) 'Authorization': 'Bearer $token',
    };
  }

  Uri _u(String path) => Uri.parse('$baseUrl$path');

  Future<Vehicle> snapshot(String vehicleId) async {
    final r = await http.get(_u('/v1/vehicles/$vehicleId'), headers: await _headers());
    return _decode(r);
  }

  Future<Vehicle> lock(String id) => _command('/v1/vehicles/$id/lock');
  Future<Vehicle> unlock(String id) => _command('/v1/vehicles/$id/unlock');
  Future<Vehicle> start(String id) => _command('/v1/vehicles/$id/start');
  Future<Vehicle> stop(String id) => _command('/v1/vehicles/$id/stop');

  Future<Vehicle> setClimate(String id, {required bool on, double? targetC}) async {
    final r = await http.post(
      _u('/v1/vehicles/$id/climate'),
      headers: await _headers(),
      body: jsonEncode({'on': on, if (targetC != null) 'targetC': targetC}),
    );
    return _decode(r);
  }

  Future<Vehicle> _command(String path) async {
    final r = await http.post(_u(path), headers: await _headers());
    return _decode(r);
  }

  Vehicle _decode(http.Response r) {
    if (r.statusCode == 401) throw ApiException('Tizimga kiring (401)');
    if (r.statusCode == 403) throw ApiException('Ruxsat yo\'q (403)');
    if (r.statusCode == 404) throw ApiException('Mashina topilmadi (404)');
    if (r.statusCode >= 400) throw ApiException('Xatolik: ${r.statusCode}');
    return Vehicle.fromJson(jsonDecode(r.body) as Map<String, dynamic>);
  }
}

class ApiException implements Exception {
  final String message;
  ApiException(this.message);
  @override
  String toString() => message;
}
