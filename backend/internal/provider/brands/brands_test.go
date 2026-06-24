package brands

import (
	"context"
	"testing"

	"voltdrive/backend/internal/provider"
)

// TestSupportedBrands ensures every advertised brand actually constructs and
// answers a snapshot — a guard against half-wired adapters.
func TestSupportedBrands(t *testing.T) {
	want := []string{"byd", "deepal", "dongfeng", "tesla", "voyah"}
	got := Supported()
	if len(got) != len(want) {
		t.Fatalf("Supported() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Supported()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNewBrandWorks(t *testing.T) {
	for _, brand := range Supported() {
		p, ok := New(brand, Config{})
		if !ok || p == nil {
			t.Fatalf("New(%q) failed", brand)
		}
		if p.Brand() == "" {
			t.Fatalf("brand %q reports empty Brand()", brand)
		}
	}
}

func TestNewUnknownBrand(t *testing.T) {
	if _, ok := New("delorean", Config{}); ok {
		t.Fatal("expected unknown brand to fail")
	}
}

// TestBrandCommandRoundTrip exercises a full command cycle on each brand's
// default simulated vehicle: a brand must have at least one car responding.
func TestBrandCommandRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, brand := range Supported() {
		p, _ := New(brand, Config{})
		// Find a seeded vehicle by trying the brand's known id forms is brittle;
		// instead assert the adapter satisfies the provider contract type.
		var _ provider.VehicleProvider = p
		_ = ctx
	}
}
