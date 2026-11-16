package siseli

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const APIBaseURL = "https://solar.siseli.com"
const IOTAppID = "rBrTRfAPXz"
const loginPath = "/apis/login/account"
const refreshPath = "/apis/login/refresh/access/token"
const latestPath = "/apis/deviceState/simple/state/latest/v1"
const historyPath = "/apis/deviceState/simple/attribute/keys/history/v1"
const flowPath = "/apis/deviceState/simple/energy/flow/v1"
const settingsPath = "/apis/remote/device/configs/cache/get"
const writePath = "/apis/remote/device/config/write"

var ErrAuthentication = errors.New("Siseli authentication rejected")
var ErrTokenExpired = errors.New("Siseli token expired")
var ErrEnergyFlowNotConfigured = errors.New("Siseli energy flow rule not configured")

type PortalError struct {
	Code    any
	Message string
}

func (e *PortalError) Error() string {
	return fmt.Sprintf("Siseli portal code=%v: %s", e.Code, e.Message)
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func md5hex(b []byte) string { h := md5.Sum(b); return hex.EncodeToString(h[:]) }
func MakeSignedHeaders(body []byte) (http.Header, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return signedHeaders(body, hex.EncodeToString(nonce)), nil
}
func signedHeaders(body []byte, nonce string) http.Header {
	digest := md5hex([]byte(IOTAppID))
	block, _ := aes.NewCipher([]byte(digest[:16]))
	secret, _ := base64.StdEncoding.DecodeString("I4D0KRr2339z3pQ/at91V9BpFAOe54DaTafwSm6suIQ=")
	cipher.NewCBCDecrypter(block, []byte(digest[16:])).CryptBlocks(secret, secret)
	secret = bytes.TrimRight(secret, "\x00")
	hash := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(hash[:])
	query := "IOT-Open-AppID=" + IOTAppID + "&IOT-Open-Body-Hash=" + bodyHash + "&IOT-Open-Nonce=" + nonce
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(base64.StdEncoding.EncodeToString([]byte(query))))
	h := http.Header{}
	h.Set("IOT-Open-AppID", IOTAppID)
	h.Set("IOT-Open-Nonce", nonce)
	h.Set("IOT-Open-Body-Hash", bodyHash)
	h.Set("IOT-Open-Sign", md5hex(mac.Sum(nil)))
	return h
}

type ClientOptions struct {
	Credentials      Credentials
	HTTP             HTTPDoer
	BaseURL          string
	Now              func() time.Time
	OnTokenRefreshed func(Credentials)
}
type Client struct {
	mu       sync.Mutex
	creds    Credentials
	http     HTTPDoer
	base     string
	now      func() time.Time
	callback func(Credentials)
}

func NewClient(o ClientOptions) (*Client, error) {
	c := o.Credentials.Normalize()
	if c.UserID == "" || c.Password == "" {
		return nil, errors.New("user_id and password are required")
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.BaseURL == "" {
		o.BaseURL = APIBaseURL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Client{creds: c, http: o.HTTP, base: strings.TrimRight(o.BaseURL, "/"), now: o.Now, callback: o.OnTokenRefreshed}, nil
}
func (c *Client) Credentials() Credentials { c.mu.Lock(); defer c.mu.Unlock(); return c.creds }
func success(v Values) error {
	code := text(v["code"])
	if code == "" || code == "0" {
		return nil
	}
	if code == "70132" {
		return ErrEnergyFlowNotConfigured
	}
	return &PortalError{Code: v["code"], Message: text(first(v, "message", "msg"))}
}
func (c *Client) raw(ctx context.Context, method, path string, query url.Values, payload any, signed bool) (Values, int, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	if signed {
		req.Header, err = MakeSignedHeaders(body)
		if err != nil {
			return nil, 0, err
		}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Origin", APIBaseURL)
	req.Header.Set("Referer", APIBaseURL+"/")
	req.Header.Set("User-Agent", "SolarPowMonitor-Siseli/1.0 (+https://github.com/YanivErel-code/jackery-monitor)")
	req.Header.Set("IOT-Token", c.creds.AccessToken)
	tz := c.creds.TimeZone
	if tz == "" {
		tz = "UTC"
	}
	req.Header.Set("IOT-Time-Zone", tz)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("Siseli HTTP %d", resp.StatusCode)
	}
	v := Values{}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 16<<20))
	decoder.UseNumber() // Portal device IDs exceed IEEE-754 integer precision.
	err = decoder.Decode(&v)
	return v, resp.StatusCode, err
}
func (c *Client) storeTokens(v Values) error {
	p, _ := v["data"].(map[string]any)
	if len(p) == 0 {
		p = v
	}
	access := text(first(p, "accessToken", "iotToken", "token"))
	if access == "" {
		return ErrAuthentication
	}
	c.creds = c.creds.WithTokens(access, text(p["refreshToken"]), text(first(p, "accessTokenWillExpiredAt", "accessTokenExpiredAt")), text(first(p, "refreshTokenWillExpiredAt", "refreshTokenExpiredAt")))
	return nil
}
func (c *Client) login(ctx context.Context) error {
	v, _, err := c.raw(ctx, "POST", loginPath, nil, Values{"account": c.creds.UserID, "password": md5hex([]byte(c.creds.Password))}, true)
	if err != nil {
		return err
	}
	if err = success(v); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	return c.storeTokens(v)
}

// Token callbacks run after releasing the client lock and may inspect the client.
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	err := c.login(ctx)
	creds := c.creds
	c.mu.Unlock()
	if err == nil && c.callback != nil {
		c.notifyTokens(creds)
	}
	return err
}
func (c *Client) refresh(ctx context.Context) error {
	if c.creds.RefreshToken == "" {
		return ErrTokenExpired
	}
	v, _, err := c.raw(ctx, "POST", refreshPath, nil, Values{"refreshToken": c.creds.RefreshToken}, false)
	if err != nil {
		return err
	}
	if err = success(v); err != nil {
		return fmt.Errorf("%w: %v", ErrTokenExpired, err)
	}
	return c.storeTokens(v)
}
func (c *Client) RefreshAccessToken(ctx context.Context) error {
	c.mu.Lock()
	err := c.refresh(ctx)
	creds := c.creds
	c.mu.Unlock()
	if err == nil && c.callback != nil {
		c.notifyTokens(creds)
	}
	return err
}
func (c *Client) ensure(ctx context.Context, force bool) error {
	expiry, err := time.Parse(time.RFC3339, c.creds.AccessTokenExpires)
	needs := force || c.creds.AccessToken == "" || (err != nil && c.creds.RefreshToken != "") || (err == nil && !c.now().Before(expiry.Add(-5*time.Minute)))
	if !needs {
		return nil
	}
	if c.creds.RefreshToken != "" {
		if err := c.refresh(ctx); err == nil {
			return nil
		}
	}
	if err := c.login(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrTokenExpired, err)
	}
	return nil
}
func (c *Client) request(ctx context.Context, method, path string, q url.Values, payload any) (Values, error) {
	c.mu.Lock()
	before := c.creds
	v, err := func() (Values, error) {
		if err := c.ensure(ctx, false); err != nil {
			return nil, err
		}
		v, status, err := c.raw(ctx, method, path, q, payload, false)
		if status == 401 {
			if err = c.ensure(ctx, true); err != nil {
				return nil, err
			}
			v, _, err = c.raw(ctx, method, path, q, payload, false)
		}
		if err != nil {
			return nil, err
		}
		return v, success(v)
	}()
	after := c.creds
	c.mu.Unlock()
	if after != before && c.callback != nil {
		c.notifyTokens(after)
	}
	return v, err
}
func (c *Client) ListDevices(ctx context.Context, station string, pageSize int) ([]Values, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	out := []Values{}
	for page := 1; ; page++ {
		v, e := c.request(ctx, "POST", "/apis/device/list", nil, Values{"page": page, "count": pageSize, "stationId": station})
		if e != nil {
			return nil, e
		}
		d := object(v["data"])
		batch, _ := d["list"].([]any)
		for _, x := range batch {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
		if n, ok := Number(d["total"]); ok && len(out) >= int(n) {
			break
		}
		if len(batch) == 0 || (d["total"] == nil && len(batch) < pageSize) {
			break
		}
	}
	return out, nil
}
func object(v any) Values              { m, _ := v.(map[string]any); return m }
func deviceQuery(id string) url.Values { return url.Values{"deviceId": {id}, "dataSource": {"1"}} }
func mergeMissing(dst, src Values) {
	for k, v := range src {
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
}
func (c *Client) FetchEnergyFlow(ctx context.Context, id string) (Values, error) {
	v, e := c.request(ctx, "GET", flowPath, deviceQuery(id), nil)
	if e != nil {
		return nil, e
	}
	d := object(v["data"])
	f := object(object(d["deviceAttributeState"])["fields"])
	if f == nil {
		f = object(d["fields"])
	}
	return f, nil
}
func (c *Client) FetchLatestData(ctx context.Context, id string) (Values, error) {
	out := Values{}
	v, err := c.request(ctx, "GET", latestPath, deviceQuery(id), nil)
	if errors.Is(err, ErrTokenExpired) || ctx.Err() != nil {
		return nil, err
	}
	if err == nil {
		data := object(v["data"])
		out = MapLatestStateFields(object(data["fields"]))
		if len(out) > 0 {
			if stamp, e := time.Parse(time.RFC3339, text(data["time"])); e == nil {
				out["_observed_ts"] = float64(stamp.Unix())
			}
		}
	}
	if out["batterySOC"] == nil {
		history, e := c.fetchHistory(ctx, id)
		if errors.Is(e, ErrTokenExpired) || ctx.Err() != nil {
			return nil, e
		}
		if e == nil {
			mergeMissing(out, history)
		}
	}
	if out["batterySOC"] == nil {
		f, e := c.FetchEnergyFlow(ctx, id)
		if errors.Is(e, ErrTokenExpired) || ctx.Err() != nil {
			return nil, e
		}
		if e == nil {
			mapped := MapLatestStateFields(f)
			mergeMissing(mapped, MapEnergyFlowFields(f))
			mergeMissing(out, mapped)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Siseli returned no telemetry from latest state or fallbacks")
	}
	ApplyDerivedValues(out)
	return out, nil
}
func (c *Client) location() *time.Location {
	c.mu.Lock()
	tz := c.creds.TimeZone
	c.mu.Unlock()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}
func (c *Client) fetchHistory(ctx context.Context, id string) (Values, error) {
	now := c.now().In(c.location()).Truncate(time.Second)
	v, e := c.request(ctx, "POST", historyPath, nil, Values{"deviceId": id, "count": 2000, "page": 1, "fromTime": now.Add(-time.Hour).Format(time.RFC3339), "toTime": now.Format(time.RFC3339), "orderByTimeAsc": true, "keys": []string{"pvInputPower", "pvPower", "acOutputActivePower", "outputActivePower", "batteryDischargeCurrent", "batteryChargingCurrent", "batteryVoltage", "feedInPower", "batterySOC", "batteryCapacity"}})
	if e != nil {
		return nil, e
	}
	out := Values{}
	for k, x := range object(object(object(v["data"])["payload"])["fields"]) {
		if a, ok := x.([]any); ok && len(a) > 0 {
			out[k] = a[len(a)-1]
		}
	}
	for _, pair := range [][2]string{{"pvInputPower", "pvPower"}, {"acOutputActivePower", "outputActivePower"}, {"batterySOC", "batteryCapacity"}} {
		if out[pair[0]] == nil && out[pair[1]] != nil {
			out[pair[0]] = out[pair[1]]
			delete(out, pair[1])
			if pair[0] == "pvInputPower" {
				out[pair[0]] = num(out[pair[0]]) * 1000
			}
		}
	}
	if n, ok := Number(out["acOutputActivePower"]); ok {
		out["acOutputActivePower"] = n * 1000
	}
	return out, nil
}
func (c *Client) GetDeviceSettings(ctx context.Context, id string) (Values, error) {
	v, e := c.request(ctx, "POST", settingsPath, url.Values{"deviceId": {id}}, Values{})
	return object(v["data"]), e
}
func CoerceSettingWriteValue(value any, spec Values, cached any) any {
	if spec["kind"] == "switch" {
		if on, ok := spec["on_value"].(string); ok {
			if num(value) == num(on) {
				return on
			}
			return spec["off_value"]
		}
	}
	if _, ok := rawValue(cached).(string); ok {
		if b, ok := value.(bool); ok {
			if b {
				return "1"
			}
			return "0"
		}
		return text(value)
	}
	return value
}
func (c *Client) SetDeviceSetting(ctx context.Context, id, key string, value any, settings Values) error {
	var spec Values
	for _, s := range tables.Controls {
		if s["canonical"] == key {
			spec = s
			break
		}
	}
	if x := text(spec["write_canonical"]); x != "" {
		key = x
	}
	if k := ResolveSettingKey(settings, key); k != "" {
		key = k
	}
	value = CoerceSettingWriteValue(value, spec, settings[key])
	_, err := c.request(ctx, "POST", writePath, url.Values{"deviceId": {id}}, Values{"id": id, "deviceId": id, "key": key, "value": value})
	return err
}
func (c *Client) FetchMonthlySummary(ctx context.Context, station string) (Values, error) {
	now := c.now().In(c.location())
	v, e := c.request(ctx, "POST", "/apis/stationOverView/stateAttributeSummary/category/yearly", url.Values{"stationId": {station}, "summaryCategoryKey": {"pvInverterElectricityQuantityClass"}}, Values{"time": now.Format("2006")})
	if e != nil {
		return nil, e
	}
	d := object(v["data"])
	items, _ := first(d, "properties", "list").([]any)
	result := Values{}
	for _, x := range items {
		m := object(x)
		result[text(first(m, "key", "name"))] = m["value"]
	}
	pv := num(first(result, now.Format("2006-01"), "pvTotal", "pv"))
	grid := num(first(result, "gridImport", "buy"))
	load := num(first(result, "totalConsumption", "load"))
	pct := 0.
	if load > 0 {
		pct = float64(int(1000*pv/load+.5)) / 10
	}
	return Values{"monthly_pv_generated": pv, "monthly_grid_import": grid, "monthly_total_consumption": load, "monthly_solar_percentage": pct}, nil
}

func (c *Client) notifyTokens(creds Credentials) { defer func() { _ = recover() }(); c.callback(creds) }

// FetchDailyProduction returns the portal's per-half-hour generated energy in kWh.
// These are interval values, not a cumulative counter (they can decrease).
func (c *Client) FetchDailyProduction(ctx context.Context, id, date string) ([]Values, error) {
	v, err := c.request(ctx, "POST", "/apis/deviceOverView/generatedEnergy/daily", url.Values{"deviceId": {id}}, Values{"time": date})
	if err != nil {
		return nil, err
	}
	items, ok := v["data"].([]any)
	if !ok {
		return nil, fmt.Errorf("Siseli daily production: expected interval array")
	}
	rows := []Values{}
	for _, item := range items {
		row := object(item)
		if row["isRealValue"] != true {
			continue
		}
		stamp, err := time.ParseInLocation("2006-01-02 15:04:05", text(row["time"]), c.location())
		if err != nil {
			return nil, err
		}
		if stamp.Format("2006-01-02") != date {
			return nil, fmt.Errorf("Siseli production date mismatch")
		}
		kwh, ok := Number(row["generatedEnergy"])
		if !ok {
			kwh, ok = Number(row["value"])
		}
		if !ok || kwh < 0 {
			return nil, fmt.Errorf("invalid Siseli production energy")
		}
		rows = append(rows, Values{"ts": float64(stamp.Unix()), "interval_s": 1800.0, "solar_wh": kwh * 1000, "_energy_kind": "portal_solar"})
	}
	return rows, nil
}

// ProductionDates includes yesterday so delayed final intervals are reconciled
// after midnight and after a monitor restart.
func (c *Client) ProductionDates() []string {
	now := c.now().In(c.location())
	return []string{now.AddDate(0, 0, -1).Format("2006-01-02"), now.Format("2006-01-02")}
}
