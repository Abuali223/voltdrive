package notify

import (
	"context"
	"log"
)

// DeviceResolver returns the FCM device tokens that should be notified for a
// given vehicle (typically every user with access, from Firestore).
type DeviceResolver func(ctx context.Context, vehicleID string) ([]string, error)

// FCMAlerter adapts the FCM client to the realtime.Alerter interface so the
// telemetry hub can raise security alerts without depending on FCM directly.
type FCMAlerter struct {
	FCM     *FCM
	Devices DeviceResolver
}

// NewFCMAlerter wires an FCM client to a device-token resolver.
func NewFCMAlerter(fcm *FCM, devices DeviceResolver) *FCMAlerter {
	return &FCMAlerter{FCM: fcm, Devices: devices}
}

// VehicleAlert implements realtime.Alerter: fan a security alert out to every
// device registered for the vehicle.
func (a *FCMAlerter) VehicleAlert(ctx context.Context, vehicleID, kind, message string) {
	tokens, err := a.Devices(ctx, vehicleID)
	if err != nil {
		log.Printf("alert: resolve devices for %s: %v", vehicleID, err)
		return
	}
	for _, t := range tokens {
		_, err := a.FCM.Send(ctx, Alert{
			DeviceToken: t,
			Title:       titleForKind(kind),
			Body:        message,
			Data:        map[string]string{"vehicleId": vehicleID, "type": kind},
		})
		if err != nil {
			log.Printf("alert: send to device: %v", err)
		}
	}
}

// titleForKind gives each alert category its own notification title.
func titleForKind(kind string) string {
	switch kind {
	case "charge_complete", "charge_full":
		return "VoltDrive — Zaryad"
	case "low_battery":
		return "VoltDrive — Batareya"
	case "moved_while_locked", "unlocked":
		return "VoltDrive — Xavfsizlik ⚠️"
	case "geofence_exit":
		return "VoltDrive — Xavfsiz hudud ⚠️"
	default:
		return "VoltDrive"
	}
}
