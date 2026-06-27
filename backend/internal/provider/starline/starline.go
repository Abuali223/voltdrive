// Package starline implements provider.VehicleProvider on top of the StarLine
// telematics cloud (developer.starline.ru). It lets VoltDrive read state from
// and send commands to a real car fitted with a StarLine CAN device — without
// the manufacturer's own API.
//
// Auth is StarLine's multi-step flow (cached and refreshed automatically):
//
//  1. getCode   GET id.starline.ru/apiV3/application/getCode  (appId, secret=md5(appSecret))
//  2. getToken  GET id.starline.ru/apiV3/application/getToken (appId, secret=md5(appSecret+code))
//  3. user login POST id.starline.ru/apiV3/user/login         (token=appToken; {login, pass=sha1(password)})
//  4. slnet     POST developer.starline.ru/json/v2/auth.slid  ({slid_token}) -> user_id + slnet cookie (24h)
//  5. data      GET  developer.starline.ru/json/v3/user/{uid}/data        -> devices
//  6. control   POST developer.starline.ru/json/v1/device/{id}/set_param  -> arm/ign/webasto
//
// The registry binds each StarLine device_id as a vehicle id, so Snapshot/Lock/
// etc. receive that id directly. Configure via env (see cmd/server/main.go):
//
//	STARLINE_APP_ID, STARLINE_APP_SECRET, STARLINE_LOGIN, STARLINE_PASSWORD,
//	STARLINE_DEVICES=deviceId1,deviceId2,...
//
// NOTE (verify on a real device): the exact telemetry field names — especially
// EV battery State-of-Charge — vary per car/firmware (Deepal/Voyah CAN support
// is alpha). The mapping below covers the common fields and is easy to extend
// once you can see the live JSON from /user/{uid}/data.
package starline

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"voltdrive/backend/internal/provider"
)

// Config holds StarLine API credentials (all from the environment).
type Config struct {
	AppID     string
	AppSecret string
	Login     string // StarLine account login (email/phone)
	Password  string // StarLine account password
}

func (c Config) Ready() bool {
	return c.AppID != "" && c.AppSecret != "" && c.Login != "" && c.Password != ""
}

// Client talks to the StarLine cloud and implements provider.VehicleProvider.
type Client struct {
	cfg  Config
	http *http.Client

	mu        sync.Mutex
	slnet     string                     // slnet auth cookie value
	userID    string                     // StarLine user id
	authExp   time.Time                  // when the slnet session expires
	dataCache map[string]json.RawMessage // deviceID -> last device JSON
	cacheAt   time.Time
}

// New returns a StarLine-backed provider.
func New(cfg Config) *Client {
	return &Client{
		cfg:       cfg,
		http:      &http.Client{Timeout: 15 * time.Second},
		dataCache: map[string]json.RawMessage{},
	}
}

func (c *Client) Brand() string { return "starline" }

// Capabilities declares what a StarLine device reliably supports. Control +
// location are solid; EV battery/charging/range and TPMS are intentionally
// excluded until confirmed on a live device (CAN support for those is alpha on
// new Chinese EVs). Extend this list once /user/{uid}/data shows the fields.
func (c *Client) Capabilities() []string {
	return []string{
		provider.CapLock, provider.CapEngine, provider.CapClimate,
		provider.CapLocation, provider.CapOdometer, provider.CapDoors,
		provider.CapTrunk, provider.CapLights, provider.CapHorn, provider.CapSeat,
		provider.CapFuel,
	}
}

func md5hex(s string) string  { sum := md5.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }
func sha1hex(s string) string { sum := sha1.Sum([]byte(s)); return hex.EncodeToString(sum[:]) }

// auth runs (or refreshes) the StarLine login flow and caches the slnet session.
func (c *Client) auth(ctx context.Context) error {
	if c.slnet != "" && time.Now().Before(c.authExp) {
		return nil
	}
	// 1. app code
	code, err := c.appStep(ctx, "getCode", md5hex(c.cfg.AppSecret))
	if err != nil {
		return fmt.Errorf("getCode: %w", err)
	}
	// 2. app token
	appToken, err := c.appStep(ctx, "getToken", md5hex(c.cfg.AppSecret+code))
	if err != nil {
		return fmt.Errorf("getToken: %w", err)
	}
	// 3. user (SLID) token
	slid, err := c.userLogin(ctx, appToken)
	if err != nil {
		return fmt.Errorf("user login: %w", err)
	}
	// 4. exchange SLID for an slnet session cookie + user id
	if err := c.slnetAuth(ctx, slid); err != nil {
		return fmt.Errorf("slnet auth: %w", err)
	}
	return nil
}

// appStep calls getCode/getToken and returns the "code" or "token" from desc.
func (c *Client) appStep(ctx context.Context, op, secret string) (string, error) {
	u := fmt.Sprintf("https://id.starline.ru/apiV3/application/%s/?appId=%s&secret=%s",
		op, url.QueryEscape(c.cfg.AppID), secret)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var d struct {
		State int `json:"state"`
		Desc  struct {
			Code  string `json:"code"`
			Token string `json:"token"`
		} `json:"desc"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return "", err
	}
	if d.State != 1 {
		return "", fmt.Errorf("state=%d", d.State)
	}
	if d.Desc.Token != "" {
		return d.Desc.Token, nil
	}
	return d.Desc.Code, nil
}

// userLogin exchanges the app token + account credentials for a SLID user token.
func (c *Client) userLogin(ctx context.Context, appToken string) (string, error) {
	u := "https://id.starline.ru/apiV3/user/login/?token=" + url.QueryEscape(appToken)
	body, _ := json.Marshal(map[string]string{"login": c.cfg.Login, "pass": sha1hex(c.cfg.Password)})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var d struct {
		State int `json:"state"`
		Desc  struct {
			UserToken string `json:"user_token"`
		} `json:"desc"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return "", err
	}
	if d.Desc.UserToken == "" {
		return "", fmt.Errorf("no user_token (state=%d) — check login/password or SMS 2FA", d.State)
	}
	return d.Desc.UserToken, nil
}

// slnetAuth exchanges the SLID token for an slnet session cookie + user id.
func (c *Client) slnetAuth(ctx context.Context, slid string) error {
	body, _ := json.Marshal(map[string]string{"slid_token": slid})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://developer.starline.ru/json/v2/auth.slid", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var d struct {
		UserID json.Number `json:"user_id"`
		Code   int         `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return err
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "slnet" {
			c.slnet = ck.Value
		}
	}
	if c.slnet == "" || d.UserID.String() == "" {
		return fmt.Errorf("no slnet cookie / user_id (code=%d)", d.Code)
	}
	c.userID = d.UserID.String()
	c.authExp = time.Now().Add(20 * time.Hour) // slnet lasts ~24h; refresh early
	return nil
}

// refreshData pulls the account's devices and caches each by id. Cached briefly
// so rapid Snapshot calls don't hammer the API.
func (c *Client) refreshData(ctx context.Context) error {
	if time.Since(c.cacheAt) < 5*time.Second && len(c.dataCache) > 0 {
		return nil
	}
	u := fmt.Sprintf("https://developer.starline.ru/json/v3/user/%s/data", c.userID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Cookie", "slnet="+c.slnet)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var d struct {
		Devices  []json.RawMessage `json:"devices"`
		UserData struct {
			Devices []json.RawMessage `json:"devices"`
		} `json:"user_data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return err
	}
	devs := d.Devices
	if len(devs) == 0 {
		devs = d.UserData.Devices
	}
	for _, raw := range devs {
		var id struct {
			DeviceID json.Number `json:"device_id"`
		}
		_ = json.Unmarshal(raw, &id)
		if id.DeviceID.String() != "" {
			c.dataCache[id.DeviceID.String()] = raw
		}
	}
	c.cacheAt = time.Now()
	return nil
}

// Snapshot maps the StarLine device JSON onto provider.Snapshot.
func (c *Client) Snapshot(ctx context.Context, deviceID string) (provider.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.auth(ctx); err != nil {
		return provider.Snapshot{}, err
	}
	if err := c.refreshData(ctx); err != nil {
		return provider.Snapshot{}, err
	}
	raw, ok := c.dataCache[deviceID]
	if !ok {
		return provider.Snapshot{}, provider.ErrNotFound
	}
	// StarLine device JSON (subset; field availability depends on CAN support).
	var d struct {
		DeviceID json.Number `json:"device_id"`
		Alias    string      `json:"alias"`
		Online   int         `json:"online"`
		Position struct {
			X   float64 `json:"x"` // longitude
			Y   float64 `json:"y"` // latitude
			S   float64 `json:"s"` // speed km/h
			Dir float64 `json:"dir"`
		} `json:"position"`
		CarState struct {
			Arm   int `json:"arm"`   // 1 = armed/locked
			Ign   int `json:"ign"`   // 1 = ignition/engine on
			Doors int `json:"doors"` // 1 = a door open
			Hood  int `json:"hood"`
			Trunk int `json:"trunk"`
		} `json:"car_state"`
		OBD struct {
			FuelPercent int     `json:"fuel_percent"`
			Mileage     float64 `json:"mileage"`
		} `json:"obd"`
		Common struct {
			Battery float64 `json:"battery"` // device supply voltage (not EV SoC!)
			ETemp   int     `json:"etemp"`
			GSMLvl  int     `json:"gsm_lvl"`
		} `json:"common"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return provider.Snapshot{}, err
	}

	lock := provider.Unlocked
	if d.CarState.Arm == 1 {
		lock = provider.Locked
	}
	snap := provider.Snapshot{
		VehicleID: deviceID,
		Name:      d.Alias,
		Online:    d.Online == 1,
		Lock:      lock,
		EngineOn:  d.CarState.Ign == 1,
		TrunkOpen: d.CarState.Trunk == 1,
		Energy: provider.EnergyState{
			// For EVs, SoC arrives via CAN — map it here once the live JSON is
			// known (e.g. d.OBD or a vendor-specific field). FuelPercent is used
			// for ICE cars; for EVs it may carry SoC depending on CAN config.
			FuelPercent:  d.OBD.FuelPercent,
			BatteryLevel: d.OBD.FuelPercent, // best-effort until EV SoC field confirmed
		},
		Climate:   provider.ClimateState{InsideC: float64(d.Common.ETemp)},
		Location:  provider.Location{Lat: d.Position.Y, Lng: d.Position.X, Speed: d.Position.S, Heading: d.Position.Dir, UpdatedAt: time.Now().Unix()},
		Health:    provider.Health{OdometerKm: int(d.OBD.Mileage)},
		UpdatedAt: time.Now().Unix(),
	}
	return snap, nil
}

// setParam sends a control command to a device (arm/ign/webasto, value 1/0).
func (c *Client) setParam(ctx context.Context, deviceID, typ string, on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.auth(ctx); err != nil {
		return err
	}
	val := 0
	if on {
		val = 1
	}
	body, _ := json.Marshal(map[string]any{"type": typ, typ: val})
	u := fmt.Sprintf("https://developer.starline.ru/json/v1/device/%s/set_param", deviceID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "slnet="+c.slnet)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var d struct {
		Code int    `json:"code"`
		Desc string `json:"desc"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&d)
	if d.Code != 0 && d.Code != 200 {
		return fmt.Errorf("starline set_param %s: code=%d %s", typ, d.Code, d.Desc)
	}
	c.cacheAt = time.Time{} // force a fresh read next Snapshot
	return nil
}

func (c *Client) Lock(ctx context.Context, id string) error { return c.setParam(ctx, id, "arm", true) }
func (c *Client) Unlock(ctx context.Context, id string) error {
	return c.setParam(ctx, id, "arm", false)
}
func (c *Client) RemoteStart(ctx context.Context, id string) error {
	return c.setParam(ctx, id, "ign", true)
}
func (c *Client) RemoteStop(ctx context.Context, id string) error {
	return c.setParam(ctx, id, "ign", false)
}

// SetClimate maps to StarLine's pre-heat/climate (webasto) channel. On EVs this
// pre-conditions the cabin from the battery. targetC is not used by StarLine.
func (c *Client) SetClimate(ctx context.Context, id string, on bool, _ float64) error {
	return c.setParam(ctx, id, "webasto", on)
}
