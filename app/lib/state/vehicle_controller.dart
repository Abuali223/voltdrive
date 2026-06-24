import 'package:flutter/foundation.dart';
import '../models/vehicle.dart';
import '../services/api_client.dart';

/// VehicleController is the single source of truth for the active vehicle.
/// The UI listens to it; it talks to the backend through ApiClient.
class VehicleController extends ChangeNotifier {
  VehicleController(this._api, {this.vehicleId = 'voyah-001'});

  final ApiClient _api;
  String vehicleId;

  Vehicle? _vehicle;
  bool _busy = false;
  String? _error;

  Vehicle? get vehicle => _vehicle;
  bool get busy => _busy;
  String? get error => _error;

  Future<void> refresh() => _run(() => _api.snapshot(vehicleId));

  Future<void> toggleLock() => _run(() =>
      (_vehicle?.locked ?? true) ? _api.unlock(vehicleId) : _api.lock(vehicleId));

  Future<void> toggleEngine() => _run(() =>
      (_vehicle?.engineOn ?? false) ? _api.stop(vehicleId) : _api.start(vehicleId));

  Future<void> setClimate(bool on, {double? targetC}) =>
      _run(() => _api.setClimate(vehicleId, on: on, targetC: targetC));

  Future<void> switchVehicle(String id) async {
    vehicleId = id;
    await refresh();
  }

  /// Applies a snapshot pushed over WebSocket (real-time telemetry).
  void applyLive(Vehicle v) {
    _vehicle = v;
    notifyListeners();
  }

  Future<void> _run(Future<Vehicle> Function() action) async {
    _busy = true;
    _error = null;
    notifyListeners();
    try {
      _vehicle = await action();
    } catch (e) {
      _error = e.toString();
    } finally {
      _busy = false;
      notifyListeners();
    }
  }
}
