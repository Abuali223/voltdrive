package realtime

import (
	"context"
	"fmt"
	"math"

	"voltdrive/backend/internal/provider"
)

// Alerter delivers a security alert for a vehicle. Implemented by the notify
// layer (FCM). Kept as an interface so the hub does not depend on FCM directly.
type Alerter interface {
	VehicleAlert(ctx context.Context, vehicleID, kind, message string)
}

// securityWatch compares the previous and current snapshot of a vehicle and
// emits alerts on suspicious transitions. Returns the snapshot to store as
// the new "previous".
func (h *Hub) securityWatch(ctx context.Context, prev *provider.Snapshot, cur provider.Snapshot) {
	if h.alerter == nil || prev == nil {
		return
	}

	// 1. A locked vehicle that has moved -> possible theft/tow.
	if cur.Lock == provider.Locked && moved(prev.Location, cur.Location) {
		h.alerter.VehicleAlert(ctx, cur.VehicleID, "moved_while_locked",
			cur.Name+" qulflangan holatda harakatlandi")
	}

	// 2. Battery dropped below 15% (only alert on the crossing).
	if prev.Energy.BatteryLevel >= 15 && cur.Energy.BatteryLevel < 15 {
		h.alerter.VehicleAlert(ctx, cur.VehicleID, "low_battery",
			cur.Name+" batareyasi 15% dan pastga tushdi")
	}

	// 3. Doors unlocked unexpectedly while owner away (lock->unlock).
	if prev.Lock == provider.Locked && cur.Lock == provider.Unlocked {
		h.alerter.VehicleAlert(ctx, cur.VehicleID, "unlocked",
			cur.Name+" eshiklari ochildi")
	}

	// 4. Charging finished (was charging, stopped with a healthy battery).
	if prev.Energy.Charging && !cur.Energy.Charging && cur.Energy.BatteryLevel >= 80 {
		h.alerter.VehicleAlert(ctx, cur.VehicleID, "charge_complete",
			fmt.Sprintf("%s zaryadlandi — %d%%", cur.Name, cur.Energy.BatteryLevel))
	}

	// 5. Battery reached full.
	if prev.Energy.BatteryLevel < 100 && cur.Energy.BatteryLevel >= 100 {
		h.alerter.VehicleAlert(ctx, cur.VehicleID, "charge_full",
			cur.Name+" to‘liq zaryadlandi (100%)")
	}
}

// moved reports whether two positions differ by more than ~50 meters.
func moved(a, b provider.Location) bool {
	const threshold = 0.0005 // ~50m in degrees, good enough for theft detection
	return math.Abs(a.Lat-b.Lat) > threshold || math.Abs(a.Lng-b.Lng) > threshold
}
