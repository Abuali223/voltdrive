// Package brands is the single place that wires every car-brand adapter into
// VoltDrive. To support a new manufacturer: create its adapter package
// (implementing provider.VehicleProvider) and add one line to the factory map
// below — nothing else in the app or API changes.
package brands

import (
	"context"
	"sort"

	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/byd"
	"voltdrive/backend/internal/provider/deepal"
	"voltdrive/backend/internal/provider/dongfeng"
	"voltdrive/backend/internal/provider/tesla"
	"voltdrive/backend/internal/provider/voyah"
)

// Config is the common connection config passed to every brand adapter.
// Per-brand secrets (base URL, token source) come from Secret Manager.
type Config struct {
	BaseURL     string
	TokenSource func(ctx context.Context) (string, error)
}

// factory maps a brand id to its adapter constructor. Add new brands here.
var factory = map[string]func(Config) provider.VehicleProvider{
	"tesla":    func(c Config) provider.VehicleProvider { return tesla.New(tesla.Config{BaseURL: c.BaseURL, TokenSource: c.TokenSource}) },
	"voyah":    func(c Config) provider.VehicleProvider { return voyah.New(voyah.Config{BaseURL: c.BaseURL, TokenSource: c.TokenSource}) },
	"deepal":   func(c Config) provider.VehicleProvider { return deepal.New(deepal.Config{BaseURL: c.BaseURL, TokenSource: c.TokenSource}) },
	"dongfeng": func(c Config) provider.VehicleProvider { return dongfeng.New(dongfeng.Config{BaseURL: c.BaseURL, TokenSource: c.TokenSource}) },
	"byd":      func(c Config) provider.VehicleProvider { return byd.New(byd.Config{BaseURL: c.BaseURL, TokenSource: c.TokenSource}) },
}

// New builds the adapter for a brand id (e.g. "voyah"). ok is false if the
// brand is unknown.
func New(brand string, cfg Config) (provider.VehicleProvider, bool) {
	f, ok := factory[brand]
	if !ok {
		return nil, false
	}
	return f(cfg), true
}

// Supported returns the sorted list of brand ids VoltDrive can talk to.
func Supported() []string {
	out := make([]string, 0, len(factory))
	for b := range factory {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}
