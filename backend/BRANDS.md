# Brendlar — API qo'shish va mashina biriktirish qo'llanmasi

VoltDrive'da har avtomobil markasi **alohida modul**. Hammasi bitta universal
interfeys (`provider.VehicleProvider`) orqali ishlaydi. Yangi brend yoki real
API qo'shganda **app va API qatlami o'zgarmaydi**.

```
App ──HTTP──> API ──> Registry ──> Brand adapter ──> (Simulyator yoki Real API)
                                    ├── tesla    (real Fleet API namunasi)
                                    ├── voyah    ┐
                                    ├── deepal   ├ simulyator bilan ishlaydi,
                                    ├── dongfeng ┤ real API kelganda ulanadi
                                    └── byd      ┘
```

Hozir barcha brendlar **simulyator** bilan ishlaydi (real ma'lumot qaytaradi,
buyruq qabul qiladi). Rasmiy API kelganda — faqat o'sha modulга HTTP chaqiruv
yoziladi.

---

## 1. Yangi brend qo'shish (3 qadam)

**Misol: "Zeekr" brendi qo'shamiz.**

### 1-qadam: modul yarating
`backend/internal/provider/zeekr/zeekr.go` (mavjud `voyah.go` dan nusxa oling):

```go
package zeekr

import (
	"context"
	"net/http"
	"time"
	"voltdrive/backend/internal/provider"
	"voltdrive/backend/internal/provider/sim"
)

type Config struct {
	BaseURL     string
	TokenSource func(ctx context.Context) (string, error)
}

type Adapter struct {
	cfg    Config
	client *http.Client
	sim    *sim.Engine
}

func New(cfg Config) *Adapter {
	seed := []provider.Snapshot{{
		VehicleID: "zeekr-001", Name: "Zeekr 001", Online: true,
		Lock: provider.Locked,
		Energy: provider.EnergyState{BatteryLevel: 85, RangeKm: 480},
	}}
	return &Adapter{cfg: cfg, client: &http.Client{Timeout: 20 * time.Second}, sim: sim.New("zeekr", seed)}
}

func (a *Adapter) Brand() string { return "zeekr" }
func (a *Adapter) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) { return a.sim.Snapshot(ctx, id) }
func (a *Adapter) Lock(ctx context.Context, id string) error        { return a.sim.Lock(ctx, id) }
func (a *Adapter) Unlock(ctx context.Context, id string) error      { return a.sim.Unlock(ctx, id) }
func (a *Adapter) RemoteStart(ctx context.Context, id string) error { return a.sim.RemoteStart(ctx, id) }
func (a *Adapter) RemoteStop(ctx context.Context, id string) error  { return a.sim.RemoteStop(ctx, id) }
func (a *Adapter) SetClimate(ctx context.Context, id string, on bool, t float64) error { return a.sim.SetClimate(ctx, id, on, t) }
```

### 2-qadam: registratsiyaga 1 qator
`backend/internal/provider/brands/brands.go` → `factory` map'ga:

```go
"zeekr": func(c Config) provider.VehicleProvider { return zeekr.New(zeekr.Config{BaseURL: c.BaseURL, TokenSource: c.TokenSource}) },
```
(va importга `".../provider/zeekr"`).

### 3-qadam: mashinani biriktiring
`cmd/server/main.go` dagi `brandOf` map'ga: `"zeekr-001": "zeekr",`

Tayyor — `go build ./...`. App va API o'zgarmadi.

---

## 2. Real API qo'shish (skeletonni jonli qilish)

Misol — **Voyah** ga real API. `provider/voyah/voyah.go` da `Snapshot` ni
to'ldiramiz. Hozir:

```go
func (a *Adapter) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) {
	// if a.live() { return a.snapshotHTTP(ctx, id) }  // TODO
	return a.sim.Snapshot(ctx, id)
}
```

Real API kelganda shunday qiling:

```go
func (a *Adapter) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) {
	if !a.live() { return a.sim.Snapshot(ctx, id) } // API yo'q -> simulyator

	// 1) Token olish (Secret Manager / OAuth)
	token, err := a.cfg.TokenSource(ctx)
	if err != nil { return provider.Snapshot{}, err }

	// 2) Real so'rov
	req, _ := http.NewRequestWithContext(ctx, "GET", a.cfg.BaseURL+"/vehicle/"+id+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.client.Do(req)
	if err != nil { return provider.Snapshot{}, err }
	defer resp.Body.Close()

	// 3) Voyah javobini bizning modelga o'tkazish (map qilish)
	var v struct {
		Battery int     `json:"soc"`
		RangeKm int     `json:"range"`
		Locked  bool    `json:"doorLocked"`
		Lat     float64 `json:"lat"`
		Lng     float64 `json:"lng"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil { return provider.Snapshot{}, err }

	lock := provider.Unlocked
	if v.Locked { lock = provider.Locked }
	return provider.Snapshot{
		VehicleID: id, Name: "Voyah Free", Online: true, Lock: lock,
		Energy:   provider.EnergyState{BatteryLevel: v.Battery, RangeKm: v.RangeKm},
		Location: provider.Location{Lat: v.Lat, Lng: v.Lng, UpdatedAt: time.Now().Unix()},
	}, nil
}
```

Buyruqlar (Lock/Start/...) ham xuddi shunday — POST so'rov yuboriladi.
**Eng yaxshi real namuna:** `provider/tesla/tesla.go` (to'liq yozilgan Fleet API).

### Eslatma — har brendning javobi har xil
Har manufacturer JSON'i boshqacha (maydon nomlari farq qiladi). Sizning ishingiz —
o'sha javobni `provider.Snapshot` ga **map qilish** (yuqorida 3-qadam). App esa
faqat `provider.Snapshot` ni biladi — shuning uchun u hech qachon o'zgarmaydi.

---

## 3. Mashinani biriktirish (real, production)

### Dev (hozir)
`main.go` da kod orqali: `registry.Bind("voyah-001", adapter)`.

### Production — foydalanuvchi o'z mashinasini qo'shadi
Real tizimда mashina **Firestore** orqali biriktiriladi:

```
Firestore:
  vehicles/{vehicleId}:
    { brand: "voyah", vin: "LVTDB21B...", ownerUid: "abc123", name: "Voyah Free" }

  vehicle_access/{vehicleId}_{uid}:
    { vehicleId, uid, role: "owner" }   // RBAC: kim boshqaradi
```

Oqim:
```
1. Foydalanuvchi app'da "Mashina qo'shish" → brend tanlaydi + manufacturer
   akkaunti bilan ulanadi (OAuth) yoki VIN kiritadi.
2. Backend Firestore'ga vehicles/{id} yozadi (brand + vin + ownerUid) va
   vehicle_access yozadi (owner roli).
3. Backend startda (yoki so'rov kelганда) Firestore'дан o'qib:
       adapter, _ := brands.New(doc.brand, cfg)
       registry.Bind(doc.vehicleId, adapter)
4. Endi shu mashinaga buyruqlar o'sha brend adapteriga boradi.
```

### Mashina ulanish usullari (real)
| Usul | Tafsilot |
|------|----------|
| **Manufacturer akkaunt (OAuth)** | Eng yaxshi — Tesla/BYD kabi. Foydalanuvchi o'z manufacturer akkauntiga kiradi, token olinadi. Jihoz kerak emas. |
| **VIN + Fleet API** | B2B shartnoma (BYD). VIN orqali mashina aniqlanadi. |
| **OBD/Telematics qurilma** | Rasmiy API bo'lmaganда — mashinaga jihoz o'rnatiladi (kafolatga e'tibor). |

---

## Test
```bash
cd backend
go test ./...
go run ./cmd/server     # barcha brendlar simulyator bilan ishlaydi
# curl -H "Authorization: Bearer u-ali" localhost:8080/v1/vehicles/voyah-001
```
