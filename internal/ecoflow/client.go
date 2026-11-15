package ecoflow

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// HTTPTimeout is the login/certification request timeout.
	HTTPTimeout = 30 * time.Second
	// MQTTConnectWait is how long StartMQTT waits for the first CONNACK.
	MQTTConnectWait = 10 * time.Second
	// MQTTKeepalive is the MQTT keepalive interval.
	MQTTKeepalive = 15 * time.Second
)

// AuthFailureReasons mirror hassio-ecoflow-cloud ecoflow_mqtt.
var AuthFailureReasons = map[string]bool{
	"Bad user name or password": true,
	"Not authorized":            true,
	"Banned":                    true,
}

// ErrAuth is returned for credential rejections (EcoflowAuthError).
type ErrAuth struct{ Msg string }

func (e *ErrAuth) Error() string { return e.Msg }

// ErrAPI is returned for other API failures (EcoflowApiError).
type ErrAPI struct {
	Msg  string
	Code string
}

func (e *ErrAPI) Error() string { return e.Msg }

func apiErr(format string, a ...any) *ErrAPI { return &ErrAPI{Msg: fmt.Sprintf(format, a...)} }

// HTTPDoer is the HTTP transport (satisfied by *http.Client).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// ConnackReason is an MQTT connection result. Code 0 is accepted. Name is
// the paho v2 ReasonCode name when known ("Not authorized", ...); empty for
// bare integer codes (v3.1.1 CONNACK 4/5 are auth failures).
type ConnackReason struct {
	Code int
	Name string
}

func (r ConnackReason) isAuthFailure() bool {
	if AuthFailureReasons[r.Name] {
		return true
	}
	return r.Code == 4 || r.Code == 5
}

func (r ConnackReason) label() string {
	if r.Name != "" {
		return r.Name
	}
	return fmt.Sprintf("rc=%d", r.Code)
}

// Subscription is a topic with a QoS.
type Subscription struct {
	Topic string
	QoS   byte
}

// MQTTSession is one live broker connection.
type MQTTSession interface {
	Subscribe(subs []Subscription) error
	Publish(topic string, qos byte, payload []byte) error
	Stop()
}

// MQTTCallbacks are invoked from transport goroutines, mirroring paho's
// on_connect / on_disconnect / on_message.
type MQTTCallbacks struct {
	OnConnect    func(s MQTTSession, r ConnackReason)
	OnDisconnect func(s MQTTSession, r ConnackReason)
	OnMessage    func(topic string, payload []byte)
}

// MQTTOptions describe the broker connection.
type MQTTOptions struct {
	Host      string
	Port      int
	ClientID  string
	Username  string
	Password  string
	KeepAlive time.Duration
}

// MQTTTransport opens a session. Start connects asynchronously: the result
// arrives through callbacks.OnConnect. A returned error means the dial
// itself failed ("MQTT connect error").
type MQTTTransport interface {
	Start(opts MQTTOptions, cb MQTTCallbacks) (MQTTSession, error)
}

// DeviceMeta is the per-device registry row the client needs.
type DeviceMeta struct {
	RawSN      string `json:"raw_sn"`
	SN         string `json:"sn,omitempty"`
	Alias      string `json:"alias"`
	DeviceType string `json:"device_type"`
	CapacityWh int    `json:"capacity_wh"`
}

// UpdateFunc receives each decoded telemetry update.
type UpdateFunc func(sn string, tele Telemetry, detail Detail)

// MQTTStats is the RX counter snapshot for the credentials status API.
type MQTTStats struct {
	MsgCount       int      `json:"mqtt_msg_count"`
	DecodeEmpty    int      `json:"mqtt_decode_empty"`
	UpdateCount    int      `json:"mqtt_update_count"`
	LastTopic      *string  `json:"last_mqtt_topic"`
	LastBytes      int      `json:"last_mqtt_bytes"`
	LastDecodeCmds []string `json:"last_decode_cmds"`
}

// SnapshotEntry is one device's snapshot().
type SnapshotEntry struct {
	Telemetry Telemetry `json:"telemetry"`
	Detail    Detail    `json:"detail"`
	Params    Params    `json:"params"`
}

// ClientOptions configure a Client.
type ClientOptions struct {
	Email    string
	Password string
	APIHost  string
	// MQTTClientID is a preferred (stable) client id.
	MQTTClientID string
	OnUpdate     UpdateFunc
	HTTP         HTTPDoer
	MQTT         MQTTTransport
	Log          *slog.Logger
}

// Client is the private-API session for one EcoFlow app account
// (EcoflowPrivateClient).
type Client struct {
	Email    string
	Password string
	APIHost  string

	onUpdate UpdateFunc
	http     HTTPDoer
	mqtt     MQTTTransport
	log      *slog.Logger

	mu sync.Mutex
	// guarded by mu:
	token          string
	userID         string
	mqttURL        string
	mqttPort       int
	mqttUser       string
	mqttPassword   string
	preferredID    string
	mqttClientID   string
	session        MQTTSession
	connectEvent   chan struct{}
	params         map[string]Params
	devices        map[string]DeviceMeta
	connected      bool
	authFailed     bool
	mqttError      string
	msgCount       int
	decodeEmpty    int
	updateCount    int
	lastTopic      *string
	lastBytes      int
	lastDecodeCmds []string
	loggedFirstMsg bool
	loggedFirstUpd bool

	// QuotaBuilder builds the "get all" payload; tests may stub it. An empty
	// result skips the publish.
	QuotaBuilder func(rawSN string) []byte
}

// NewClient creates a client; the zero HTTP defaults to net/http with a
// 30s timeout.
func NewClient(o ClientOptions) *Client {
	host := strings.ToLower(strings.TrimSpace(o.APIHost))
	if host == "" {
		host = DefaultAPIHost
	}
	h := o.HTTP
	if h == nil {
		h = &http.Client{Timeout: HTTPTimeout}
	}
	lg := o.Log
	if lg == nil {
		lg = slog.Default()
	}
	return &Client{
		Email: strings.TrimSpace(o.Email), Password: o.Password, APIHost: host,
		onUpdate: o.OnUpdate, http: h, mqtt: o.MQTT, log: lg,
		preferredID:  strings.TrimSpace(o.MQTTClientID),
		mqttPort:     8883,
		params:       map[string]Params{},
		devices:      map[string]DeviceMeta{},
		connectEvent: make(chan struct{}, 1),
		QuotaBuilder: BuildQuotaRequest,
	}
}

// SetSession is a test hook (and for transports that connect elsewhere).
func (c *Client) SetSession(s MQTTSession, userID string, connected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = s
	c.userID = userID
	c.connected = connected
}

// Token returns the current API token.
func (c *Client) Token() string { c.mu.Lock(); defer c.mu.Unlock(); return c.token }

// UserID returns the logged-in user id.
func (c *Client) UserID() string { c.mu.Lock(); defer c.mu.Unlock(); return c.userID }

// MQTTClientID returns the resolved client id.
func (c *Client) MQTTClientID() string { c.mu.Lock(); defer c.mu.Unlock(); return c.mqttClientID }

// Connected reports whether the MQTT session is up.
func (c *Client) Connected() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.connected }

// AuthFailed reports a broker auth refusal.
func (c *Client) AuthFailed() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.authFailed }

// MQTTError is the last MQTT error text.
func (c *Client) MQTTError() string { c.mu.Lock(); defer c.mu.Unlock(); return c.mqttError }

// Close stops MQTT. The HTTP client is not owned.
func (c *Client) Close() { c.StopMQTT() }

// Login authenticates and fetches the MQTT certification.
func (c *Client) Login(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{
		"email":    c.Email,
		"password": base64.StdEncoding.EncodeToString([]byte(c.Password)),
		"scene":    "IOT_APP",
		"userType": "ECOFLOW",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+c.APIHost+"/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("lang", "en_US")
	req.Header.Set("content-type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	login, err := c.decodeJSON(resp, true)
	if err != nil {
		return err
	}
	token, userID, ok := loginFields(login)
	if !ok {
		return apiErr("login response missing fields")
	}
	c.mu.Lock()
	c.token, c.userID = token, userID
	c.mu.Unlock()

	cert, err := c.callAPI(ctx, "/iot-auth/app/certification", nil)
	if err != nil {
		return err
	}
	data, _ := cert["data"].(map[string]any)
	urlS, _ := data["url"].(string)
	port, okPort := toInt(data["port"])
	user, _ := data["certificateAccount"].(string)
	pass, _ := data["certificatePassword"].(string)
	if urlS == "" || !okPort || user == "" || pass == "" {
		return apiErr("MQTT certification missing fields")
	}
	c.mu.Lock()
	c.mqttURL, c.mqttPort, c.mqttUser, c.mqttPassword = urlS, port, user, pass
	pref := c.preferredID
	c.mu.Unlock()
	cid, err := ResolveClientID(userID, pref)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.mqttClientID, c.preferredID = cid, cid
	c.mu.Unlock()
	c.log.Info("EcoFlow login OK", "user_id", userID, "mqtt", fmt.Sprintf("%s:%d", urlS, port), "client_id", cid, "user", user)
	return nil
}

func loginFields(body map[string]any) (token, userID string, ok bool) {
	data, _ := body["data"].(map[string]any)
	token, _ = data["token"].(string)
	user, _ := data["user"].(map[string]any)
	switch v := user["userId"].(type) {
	case string:
		userID = v
	case float64:
		userID = fmt.Sprintf("%.0f", v)
	case json.Number:
		userID = v.String()
	}
	return token, userID, token != "" && userID != ""
}

// callAPI is a private-API GET matching hassio-ecoflow-cloud: userId is sent
// as a form body on the GET (HA uses aiohttp data=), not as a query param.
func (c *Client) callAPI(ctx context.Context, endpoint string, params url.Values) (map[string]any, error) {
	c.mu.Lock()
	token, uid := c.token, c.userID
	c.mu.Unlock()
	var body io.Reader
	ct := "application/json"
	if uid != "" {
		body = strings.NewReader(url.Values{"userId": {uid}}.Encode())
		ct = "application/x-www-form-urlencoded"
	}
	u := "https://" + c.APIHost + endpoint
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("lang", "en_US")
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("content-type", ct)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	return c.decodeJSON(resp, false)
}

func (c *Client) decodeJSON(resp *http.Response, authEndpoint bool) (map[string]any, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr("HTTP %d: %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, apiErr("non-JSON response: %v", err)
	}
	msg, _ := body["message"].(string)
	code := body["code"]
	if strings.ToLower(msg) != "success" {
		detail := msg
		if detail == "" {
			detail = "request failed"
		}
		codeStr := ""
		if code != nil {
			codeStr = fmt.Sprint(code)
		}
		if authEndpoint || (code != nil && codeStr != "0" && codeStr != "None") {
			return nil, &ErrAuth{Msg: detail}
		}
		return nil, &ErrAPI{Msg: detail, Code: codeStr}
	}
	return body, nil
}

// ConfigureDevices registers devices to subscribe (registry rows).
func (c *Client) ConfigureDevices(devs []DeviceMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.devices = map[string]DeviceMeta{}
	for _, d := range devs {
		raw := d.RawSN
		if raw == "" {
			raw = RawSNFromSN(d.SN)
		}
		if raw == "" {
			continue
		}
		d.RawSN = raw
		c.devices[raw] = d
		if _, ok := c.params[raw]; !ok {
			c.params[raw] = Params{}
		}
	}
}

// ConfiguredRawSNs returns the raw serials registered for subscribe.
func (c *Client) ConfiguredRawSNs() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.devices))
	for k := range c.devices {
		out[k] = true
	}
	return out
}

// Stats snapshots the MQTT RX counters.
func (c *Client) Stats() MQTTStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return MQTTStats{
		MsgCount: c.msgCount, DecodeEmpty: c.decodeEmpty, UpdateCount: c.updateCount,
		LastTopic: c.lastTopic, LastBytes: c.lastBytes,
		LastDecodeCmds: append([]string(nil), c.lastDecodeCmds...),
	}
}

// StartMQTT connects and waits for the first CONNACK. It returns true on
// success.
func (c *Client) StartMQTT(ctx context.Context, wait time.Duration) (bool, error) {
	c.mu.Lock()
	ready := c.mqttURL != "" && c.mqttUser != "" && c.mqttPassword != "" && c.mqttClientID != ""
	c.mu.Unlock()
	if !ready {
		return false, apiErr("login first")
	}
	if c.mqtt == nil {
		return false, apiErr("no MQTT transport configured")
	}
	c.StopMQTT()
	c.mu.Lock()
	c.connected, c.authFailed, c.mqttError = false, false, ""
	c.msgCount, c.decodeEmpty, c.updateCount = 0, 0, 0
	c.lastTopic, c.lastBytes, c.lastDecodeCmds = nil, 0, nil
	c.loggedFirstMsg, c.loggedFirstUpd = false, false
	// fresh event channel so a stale signal can't satisfy this wait
	c.connectEvent = make(chan struct{}, 1)
	ev := c.connectEvent
	opts := MQTTOptions{Host: c.mqttURL, Port: c.mqttPort, ClientID: c.mqttClientID,
		Username: c.mqttUser, Password: c.mqttPassword, KeepAlive: MQTTKeepalive}
	c.mu.Unlock()
	c.log.Info("EcoFlow MQTT connecting", "host", opts.Host, "port", opts.Port, "client_id", opts.ClientID, "user", opts.Username)

	sess, err := c.mqtt.Start(opts, MQTTCallbacks{
		OnConnect: c.onConnect, OnDisconnect: c.onDisconnect, OnMessage: c.onMessage,
	})
	if err != nil {
		c.mu.Lock()
		c.mqttError = fmt.Sprintf("MQTT connect error: %v", err)
		c.connected = false
		c.mu.Unlock()
		c.log.Error("EcoFlow MQTT connect error", "err", err)
		c.StopMQTT()
		return false, nil
	}
	c.mu.Lock()
	c.session = sess
	c.mu.Unlock()
	if wait < time.Second {
		wait = time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ev:
	case <-timer.C:
		c.mu.Lock()
		c.mqttError = "MQTT connect timed out"
		c.connected = false
		c.mu.Unlock()
		c.log.Error("EcoFlow MQTT connect timed out", "after", wait)
		c.StopMQTT()
		return false, nil
	case <-ctx.Done():
		c.StopMQTT()
		return false, ctx.Err()
	}
	if !c.Connected() {
		c.StopMQTT()
		return false, nil
	}
	return true, nil
}

// StopMQTT tears down the session.
func (c *Client) StopMQTT() {
	c.mu.Lock()
	s := c.session
	c.session = nil
	c.connected = false
	c.mu.Unlock()
	if s != nil {
		s.Stop()
	}
}

func (c *Client) signalConnect() {
	c.mu.Lock()
	ev := c.connectEvent
	c.mu.Unlock()
	select {
	case ev <- struct{}{}:
	default:
	}
}

// RequestQuota asks the device for a full snapshot (Delta 3 protobuf get).
func (c *Client) RequestQuota(rawSN string) {
	c.mu.Lock()
	s, uid, builder := c.session, c.userID, c.QuotaBuilder
	c.mu.Unlock()
	if s == nil || uid == "" {
		return
	}
	var payload []byte
	if builder != nil {
		payload = builder(rawSN)
	}
	if len(payload) == 0 {
		c.log.Warn("quota request skipped: payload unavailable", "sn", rawSN)
		return
	}
	topic := fmt.Sprintf("/app/%s/%s/thing/property/get", uid, rawSN)
	if err := s.Publish(topic, 1, payload); err != nil {
		c.log.Debug("quota request failed", "sn", rawSN, "err", err)
	}
}

// RequestQuotaAll re-requests quota for every configured device.
func (c *Client) RequestQuotaAll() {
	c.mu.Lock()
	sns := make([]string, 0, len(c.devices))
	for k := range c.devices {
		sns = append(sns, k)
	}
	c.mu.Unlock()
	for _, sn := range sns {
		c.RequestQuota(sn)
	}
}

// SetOutput toggles AC / 12V DC / USB via private MQTT ConfigWrite. rawSN is
// the bare EcoFlow serial (no "ecoflow:" prefix).
func (c *Client) SetOutput(rawSN, port string, on bool) error {
	field, ok := OutputSetFields[port]
	if !ok {
		return apiErr("EcoFlow does not support output port '%s'", port)
	}
	c.mu.Lock()
	s, uid, connected := c.session, c.userID, c.connected
	c.mu.Unlock()
	if s == nil || uid == "" || !connected {
		return apiErr("EcoFlow MQTT not connected")
	}
	sn := strings.TrimSpace(rawSN)
	if sn == "" {
		return apiErr("device serial required")
	}
	c.mu.Lock()
	_, known := c.devices[sn]
	c.mu.Unlock()
	if !known {
		return apiErr("unknown EcoFlow device %s", sn)
	}
	v := 0
	if on {
		v = 1
	}
	payload := BuildSetCommand(field, v, sn)
	if len(payload) == 0 {
		return apiErr("EcoFlow set command unavailable")
	}
	topic := fmt.Sprintf("/app/%s/%s/thing/property/set", uid, sn)
	if err := s.Publish(topic, 1, payload); err != nil {
		var ae *ErrAPI
		if errors.As(err, &ae) {
			return err
		}
		return apiErr("MQTT publish failed: %v", err)
	}
	// Optimistic local merge so the next snapshot reflects the toggle before
	// SetReply / flow_info_* arrives.
	c.mu.Lock()
	c.params[sn] = MergeParams(c.params[sn], Params{field: float64(v)})
	c.mu.Unlock()
	state := "off"
	if on {
		state = "on"
	}
	c.log.Info("EcoFlow set_output", "sn", sn, "port", port, "state", state)
	return nil
}

func topicsFor(userID, rawSN string) []string {
	return []string{
		"/app/device/property/" + rawSN,
		fmt.Sprintf("/app/%s/%s/thing/property/set_reply", userID, rawSN),
		fmt.Sprintf("/app/%s/%s/thing/property/get_reply", userID, rawSN),
	}
}

// onConnect mirrors _on_connect.
func (c *Client) onConnect(s MQTTSession, r ConnackReason) {
	if r.Code == 0 {
		c.mu.Lock()
		c.connected, c.authFailed, c.mqttError = true, false, ""
		sns := make([]string, 0, len(c.devices))
		for k := range c.devices {
			sns = append(sns, k)
		}
		uid := c.userID
		c.mu.Unlock()
		var subs []Subscription
		for _, sn := range sns {
			for _, t := range topicsFor(uid, sn) {
				subs = append(subs, Subscription{Topic: t, QoS: 1})
			}
		}
		if len(subs) > 0 && s != nil {
			if err := s.Subscribe(subs); err != nil {
				c.log.Warn("EcoFlow MQTT subscribe failed", "err", err)
			} else {
				c.log.Info("EcoFlow MQTT subscribed", "topics", len(subs))
			}
			for _, sn := range sns {
				c.RequestQuota(sn)
			}
		}
		c.signalConnect()
		return
	}
	label := r.label()
	c.mu.Lock()
	c.connected = false
	c.authFailed = r.isAuthFailure()
	if c.authFailed {
		c.mqttError = fmt.Sprintf("MQTT not authorized (%s)", label)
	} else {
		c.mqttError = fmt.Sprintf("MQTT connect failed (%s)", label)
	}
	cid := c.mqttClientID
	c.mu.Unlock()
	c.log.Error("EcoFlow MQTT connect failed", "reason", label, "client_id", cid)
	c.signalConnect()
}

// onDisconnect mirrors _on_disconnect.
func (c *Client) onDisconnect(_ MQTTSession, r ConnackReason) {
	c.mu.Lock()
	c.connected = false
	if r.Code != 0 {
		label := r.label()
		c.log.Warn("EcoFlow MQTT disconnected", "reason", label)
		if r.isAuthFailure() {
			c.authFailed = true
			c.mqttError = fmt.Sprintf("MQTT not authorized (%s)", label)
		}
	}
	c.mu.Unlock()
	// If connect never completed, unblock waiters.
	c.signalConnect()
}

// onMessage mirrors _on_message.
func (c *Client) onMessage(topic string, payload []byte) {
	c.mu.Lock()
	c.msgCount++
	c.lastTopic = &topic
	c.lastBytes = len(payload)
	first := !c.loggedFirstMsg
	c.loggedFirstMsg = true
	c.mu.Unlock()
	if first {
		c.log.Info("EcoFlow MQTT first message", "topic", topic, "bytes", len(payload))
	}
	rawSN := c.snFromTopic(topic)
	if rawSN == "" {
		c.log.Info("EcoFlow MQTT message on unmatched topic", "topic", topic)
		return
	}
	params := c.ParsePayload(payload)
	if len(params) == 0 {
		cmds := PeekHeaderCmds(payload)
		c.mu.Lock()
		c.decodeEmpty++
		c.lastDecodeCmds = cmds
		c.mu.Unlock()
		c.log.Info("EcoFlow MQTT decode empty", "topic", topic, "bytes", len(payload), "cmds", strings.Join(cmds, ","))
		return
	}
	c.mu.Lock()
	merged := MergeParams(c.params[rawSN], params)
	c.params[rawSN] = merged
	meta := c.devices[rawSN]
	c.mu.Unlock()
	sn, _ := MakeSN(rawSN)
	tele := ParamsToTelemetry(merged, telemetryOpts(meta))
	c.mu.Lock()
	c.updateCount++
	firstUpd := !c.loggedFirstUpd
	c.loggedFirstUpd = true
	cb := c.onUpdate
	c.mu.Unlock()
	if firstUpd {
		c.log.Info("EcoFlow telemetry", "sn", sn, "keys", len(params))
	}
	if cb != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					c.log.Error("on_update panicked", "sn", sn, "panic", r)
				}
			}()
			cb(sn, tele, tele.EcoflowDetail)
		}()
	}
}

func telemetryOpts(m DeviceMeta) TelemetryOpts {
	return TelemetryOpts{DeviceType: m.DeviceType, Alias: m.Alias, CapacityWh: m.CapacityWh}
}

// snFromTopic extracts the raw serial from
// /app/device/property/{sn} or /app/{uid}/{sn}/thing/property/...
func (c *Client) snFromTopic(topic string) string {
	parts := strings.Split(strings.Trim(topic, "/"), "/")
	c.mu.Lock()
	known := make(map[string]bool, len(c.devices))
	for k := range c.devices {
		known[k] = true
	}
	c.mu.Unlock()
	for _, p := range parts {
		if known[p] {
			return p
		}
	}
	if len(parts) >= 4 && parts[0] == "app" && parts[1] == "device" {
		if parts[2] == "property" {
			return parts[3]
		}
		return ""
	}
	if len(parts) >= 3 && parts[0] == "app" {
		if known[parts[2]] {
			return parts[2]
		}
	}
	return ""
}

// ParsePayload turns a message body into a params dict: JSON envelope first
// (params / quotaMap / flat heartbeat), then Delta 3 protobuf. Returns nil
// when nothing decodes.
func (c *Client) ParsePayload(payload []byte) Params {
	var data map[string]any
	if json.Unmarshal(payload, &data) == nil && data != nil {
		if p, ok := data["params"].(map[string]any); ok {
			return Params(p)
		}
		if d, ok := data["data"].(map[string]any); ok {
			if q, ok := d["quotaMap"].(map[string]any); ok {
				return Params(q)
			}
		}
		for _, k := range []string{"powGetPv", "cmsBattSoc", "pow_get_pv", "cms_batt_soc"} {
			if _, ok := data[k]; ok {
				return Params(data)
			}
		}
	}
	return DecodePropertyPayload(payload)
}

// Snapshot returns raw_sn → {telemetry, detail, params} for all devices.
func (c *Client) Snapshot() map[string]SnapshotEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]SnapshotEntry, len(c.devices))
	for raw, meta := range c.devices {
		params := make(Params, len(c.params[raw]))
		for k, v := range c.params[raw] {
			params[k] = v
		}
		tele := ParamsToTelemetry(params, telemetryOpts(meta))
		out[raw] = SnapshotEntry{Telemetry: tele, Detail: tele.EcoflowDetail, Params: params}
	}
	return out
}

// Params returns a copy of the accumulated params for a raw serial.
func (c *Client) ParamsFor(rawSN string) Params {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(Params, len(c.params[rawSN]))
	for k, v := range c.params[rawSN] {
		out[k] = v
	}
	return out
}

// OnConnect, OnDisconnect and OnMessage are exposed for transports and
// tests that drive the callbacks directly.
func (c *Client) OnConnect(s MQTTSession, r ConnackReason)    { c.onConnect(s, r) }
func (c *Client) OnDisconnect(s MQTTSession, r ConnackReason) { c.onDisconnect(s, r) }
func (c *Client) OnMessage(topic string, payload []byte)      { c.onMessage(topic, payload) }
