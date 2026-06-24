import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../state/vehicle_controller.dart';

/// HomeScreen renders the VoltDrive control surface (dark theme, orange
/// accent) and drives the backend through VehicleController.
class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key});
  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  static const _orange = Color(0xFFFF6A1A);
  static const _bg = Color(0xFF0E0F13);
  static const _card = Color(0xFF1A1C22);

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      context.read<VehicleController>().refresh();
    });
  }

  @override
  Widget build(BuildContext context) {
    final c = context.watch<VehicleController>();
    final v = c.vehicle;

    return Scaffold(
      backgroundColor: _bg,
      body: SafeArea(
        child: RefreshIndicator(
          onRefresh: c.refresh,
          color: _orange,
          child: ListView(
            padding: const EdgeInsets.all(20),
            children: [
              Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  Text(v?.name ?? 'VoltDrive',
                      style: const TextStyle(
                          color: Colors.white,
                          fontSize: 24,
                          fontWeight: FontWeight.w800)),
                  if (c.busy)
                    const SizedBox(
                        width: 18,
                        height: 18,
                        child: CircularProgressIndicator(
                            strokeWidth: 2, color: _orange)),
                ],
              ),
              const SizedBox(height: 6),
              Text(
                v == null
                    ? 'Ulanmoqda...'
                    : '${v.online ? "Onlayn" : "Oflayn"} • ${v.energy.batteryLevel}% • ${v.energy.rangeKm} km',
                style: TextStyle(color: Colors.white.withValues(alpha: 0.6)),
              ),
              if (c.error != null)
                Padding(
                  padding: const EdgeInsets.only(top: 12),
                  child: Text(c.error!, style: const TextStyle(color: Colors.redAccent)),
                ),
              const SizedBox(height: 24),
              _quickGrid(c, v),
            ],
          ),
        ),
      ),
    );
  }

  Widget _quickGrid(VehicleController c, v) {
    final locked = v?.locked ?? true;
    final engineOn = v?.engineOn ?? false;
    final climateOn = v?.climate.on ?? false;
    return GridView.count(
      crossAxisCount: 2,
      shrinkWrap: true,
      physics: const NeverScrollableScrollPhysics(),
      mainAxisSpacing: 14,
      crossAxisSpacing: 14,
      childAspectRatio: 1.3,
      children: [
        _tile(
          icon: locked ? Icons.lock : Icons.lock_open,
          label: locked ? 'Qulflangan' : 'Ochiq',
          active: !locked,
          onTap: c.busy ? null : c.toggleLock,
        ),
        _tile(
          icon: Icons.power_settings_new,
          label: engineOn ? 'Dvigatel ON' : 'Dvigatel OFF',
          active: engineOn,
          onTap: c.busy ? null : c.toggleEngine,
        ),
        _tile(
          icon: Icons.ac_unit,
          label: climateOn ? 'Klimat ON' : 'Klimat OFF',
          active: climateOn,
          onTap: c.busy ? null : () => c.setClimate(!climateOn, targetC: 22),
        ),
        _tile(
          icon: Icons.location_on,
          label: 'Joylashuv',
          active: false,
          onTap: () {}, // map screen
        ),
      ],
    );
  }

  Widget _tile({
    required IconData icon,
    required String label,
    required bool active,
    required VoidCallback? onTap,
  }) {
    return GestureDetector(
      onTap: onTap,
      child: Container(
        decoration: BoxDecoration(
          color: active ? _orange : _card,
          borderRadius: BorderRadius.circular(20),
        ),
        padding: const EdgeInsets.all(18),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisAlignment: MainAxisAlignment.spaceBetween,
          children: [
            Icon(icon, color: active ? Colors.white : _orange, size: 28),
            Text(label,
                style: TextStyle(
                    color: active ? Colors.white : Colors.white70,
                    fontWeight: FontWeight.w600)),
          ],
        ),
      ),
    );
  }
}
