/// Vehicle domain models. These mirror the backend JSON snapshot 1:1 so the
/// app stays brand-agnostic — the same model works for Voyah, Tesla, BYD, etc.

class Energy {
  final int batteryLevel; // %
  final int rangeKm;
  final bool charging;
  final double chargeRateKw;
  final int fuelPercent;
  final bool pluggedIn;

  const Energy({
    this.batteryLevel = 0,
    this.rangeKm = 0,
    this.charging = false,
    this.chargeRateKw = 0,
    this.fuelPercent = 0,
    this.pluggedIn = false,
  });

  factory Energy.fromJson(Map<String, dynamic> j) => Energy(
        batteryLevel: j['batteryLevel'] ?? 0,
        rangeKm: j['rangeKm'] ?? 0,
        charging: j['charging'] ?? false,
        chargeRateKw: (j['chargeRateKw'] ?? 0).toDouble(),
        fuelPercent: j['fuelPercent'] ?? 0,
        pluggedIn: j['pluggedIn'] ?? false,
      );
}

class Climate {
  final bool on;
  final double targetC;
  final double insideC;
  final double outsideC;
  final bool defrostOn;

  const Climate({
    this.on = false,
    this.targetC = 22,
    this.insideC = 0,
    this.outsideC = 0,
    this.defrostOn = false,
  });

  factory Climate.fromJson(Map<String, dynamic> j) => Climate(
        on: j['on'] ?? false,
        targetC: (j['targetC'] ?? 22).toDouble(),
        insideC: (j['insideC'] ?? 0).toDouble(),
        outsideC: (j['outsideC'] ?? 0).toDouble(),
        defrostOn: j['defrostOn'] ?? false,
      );
}

class VehicleLocation {
  final double lat;
  final double lng;
  final double heading;
  final double speed;

  const VehicleLocation({
    this.lat = 0,
    this.lng = 0,
    this.heading = 0,
    this.speed = 0,
  });

  factory VehicleLocation.fromJson(Map<String, dynamic> j) => VehicleLocation(
        lat: (j['lat'] ?? 0).toDouble(),
        lng: (j['lng'] ?? 0).toDouble(),
        heading: (j['heading'] ?? 0).toDouble(),
        speed: (j['speed'] ?? 0).toDouble(),
      );
}

class Vehicle {
  final String id;
  final String name;
  final bool online;
  final bool locked;
  final bool engineOn;
  final Energy energy;
  final Climate climate;
  final VehicleLocation location;
  final int odometerKm;

  const Vehicle({
    required this.id,
    required this.name,
    this.online = false,
    this.locked = true,
    this.engineOn = false,
    this.energy = const Energy(),
    this.climate = const Climate(),
    this.location = const VehicleLocation(),
    this.odometerKm = 0,
  });

  factory Vehicle.fromJson(Map<String, dynamic> j) => Vehicle(
        id: j['vehicleId'] ?? '',
        name: j['name'] ?? '',
        online: j['online'] ?? false,
        locked: (j['lock'] ?? 'locked') == 'locked',
        engineOn: j['engineOn'] ?? false,
        energy: Energy.fromJson(j['energy'] ?? const {}),
        climate: Climate.fromJson(j['climate'] ?? const {}),
        location: VehicleLocation.fromJson(j['location'] ?? const {}),
        odometerKm: (j['health']?['odometerKm']) ?? 0,
      );
}
