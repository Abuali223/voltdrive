package provider

import (
	"strings"
	"sync"
)

// Registry maps a vehicle id to the brand adapter that controls it.
// In production the lookup comes from Firestore (which user owns which car
// on which brand). Here it is an in-memory map populated at startup.
type Registry struct {
	mu        sync.RWMutex
	byVehicle map[string]VehicleProvider
	def       VehicleProvider
}

// NewRegistry creates a registry with a default fallback provider.
func NewRegistry(def VehicleProvider) *Registry {
	return &Registry{byVehicle: map[string]VehicleProvider{}, def: def}
}

// Bind associates a vehicle id with a specific brand adapter.
func (r *Registry) Bind(vehicleID string, p VehicleProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byVehicle[strings.ToLower(vehicleID)] = p
}

// For returns the adapter responsible for a vehicle, or the default.
func (r *Registry) For(vehicleID string) VehicleProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.byVehicle[strings.ToLower(vehicleID)]; ok {
		return p
	}
	return r.def
}
