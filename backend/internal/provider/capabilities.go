package provider

// Capabilities describe which features a vehicle actually supports through its
// current connection. A fully-simulated provider supports everything; a real
// aftermarket device (e.g. StarLine on a car whose CAN battery data isn't
// decoded yet) supports only a subset. The app reads these and shows ONLY the
// controls/metrics that work, instead of dead buttons or empty values.
const (
	CapLock        = "lock"        // lock / unlock doors
	CapEngine      = "engine"      // remote start / stop (or EV power)
	CapClimate     = "climate"     // pre-conditioning / HVAC
	CapLocation    = "location"    // GPS position + trips
	CapBattery     = "battery"     // EV state-of-charge %
	CapCharging    = "charging"    // plugged-in / charging status
	CapRange       = "range"       // remaining range km
	CapFuel        = "fuel"        // ICE fuel level
	CapOdometer    = "odometer"    // total mileage
	CapDoors       = "doors"       // door / lock open state
	CapTrunk       = "trunk"       // trunk control
	CapLights      = "lights"      // exterior lights
	CapHorn        = "horn"        // horn / find-car
	CapSeat        = "seat"        // seat heater / memory
	CapTPMS        = "tpms"        // tyre pressure
	CapDiagnostics = "diagnostics" // fault codes
)

// AllCapabilities is the full feature set a fully-simulated provider supports.
var AllCapabilities = []string{
	CapLock, CapEngine, CapClimate, CapLocation, CapBattery, CapCharging,
	CapRange, CapFuel, CapOdometer, CapDoors, CapTrunk, CapLights, CapHorn,
	CapSeat, CapTPMS, CapDiagnostics,
}

// Capabler is an OPTIONAL interface. A provider implements it to declare a
// reduced capability set; providers that don't are assumed to support all
// features (the simulator does).
type Capabler interface {
	Capabilities() []string
}

// CapabilitiesOf returns p's declared capabilities, or the full set when p does
// not implement Capabler.
func CapabilitiesOf(p VehicleProvider) []string {
	if c, ok := p.(Capabler); ok {
		return c.Capabilities()
	}
	return AllCapabilities
}
