package ecoflow

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/compdani/jackery-monitor/internal/ecoflow/efpb"
)

var clientIDRe = regexp.MustCompile(`^ANDROID_[0-9A-F]{32}_\d+$`)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestResolveClientIDSimpleFormat(t *testing.T) {
	cid, err := ResolveClientID("2104939217409142785", "")
	if err != nil || !clientIDRe.MatchString(cid) {
		t.Fatalf("cid=%q err=%v", cid, err)
	}
	// No legacy MD5/millis suffix (would be 4+ underscore-separated segments).
	if strings.Count(cid, "_") != 2 {
		t.Fatalf("underscores in %q", cid)
	}
}

func TestResolveClientIDReusesStable(t *testing.T) {
	uid := "2104939217409142785"
	first, _ := ResolveClientID(uid, "")
	again, _ := ResolveClientID(uid, first)
	if again != first {
		t.Fatalf("%q != %q", again, first)
	}
}

func TestResolveClientIDRejectsLegacyMD5Form(t *testing.T) {
	uid := "2104939217409142785"
	legacy := "ANDROID_ABCDEF0123456789ABCDEF0123456789_" + uid + "_" +
		strings.Repeat("0", 32) + "_1700000000000_deadbeefcafebabe0123456789abcdef"
	fresh, _ := ResolveClientID(uid, legacy)
	if fresh == legacy || !clientIDRe.MatchString(fresh) || strings.Count(fresh, "_") != 2 {
		t.Fatalf("fresh=%q", fresh)
	}
	if _, err := ResolveClientID("", ""); err == nil {
		t.Fatal("expected user_id error")
	}
}

func TestSolarInputsDualPV(t *testing.T) {
	rows := SolarInputsFromParams(Params{"pow_get_pv": 420.0, "pow_get_pv2": 310.0}, "Garage")
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	want := SolarInput{ID: "pow_get_pv", Label: "Garage PV1", Source: "ecoflow", Watts: 420}
	if rows[0] != want {
		t.Fatalf("rows[0] = %+v", rows[0])
	}
	if rows[1].Watts != 310 || rows[1].ID != "pow_get_pv2" {
		t.Fatalf("rows[1] = %+v", rows[1])
	}
}

func TestParamsToTelemetryDelta3MaxPlus(t *testing.T) {
	tele := ParamsToTelemetry(Params{
		"cms_batt_soc":      77.5,
		"cms_chg_dsg_state": 2.0,
		"pow_get_pv":        420.0,
		"pow_get_pv2":       310.0,
		"pow_get_ac_in":     50.0,
		"pow_out_sum_w":     180.0,
		"pow_in_sum_w":      780.0,
		"flow_info_ac_out":  14.0,
		"flow_info_12v":     4.0,
		"bms_max_cell_temp": 28.0,
		"cms_dsg_rem_time":  120.0,
	}, TelemetryOpts{DeviceType: "DELTA_3_MAX_PLUS", Alias: "Garage", CapacityWh: 2048})
	checks := []struct {
		name string
		ok   bool
	}{
		{"source", tele.Source == "ecoflow"},
		{"battery_percent", tele.BatteryPercent != nil && *tele.BatteryPercent == 78},
		{"battery_status", tele.BatteryStatus == 2},
		{"solar_input_w", tele.SolarInputW == 730},
		{"solar_inputs", len(tele.SolarInputs) == 2},
		{"ac_input_w", tele.ACInputW == 50},
		{"output_power_w", tele.OutputPowerW == 180},
		{"input_power_w", tele.InputPowerW == 780},
		{"ac_on", tele.ACOn},
		{"dc_on", !tele.DCOn},
		{"battery_temp_c", tele.BatteryTempC != nil && *tele.BatteryTempC == 28.0},
		{"time_remaining_h", tele.TimeRemainingH != nil && *tele.TimeRemainingH == 2.0},
		{"capacity_wh", tele.CapacityWh != nil && *tele.CapacityWh == 2048},
		{"detail pv", tele.EcoflowDetail.PowGetPv != nil && *tele.EcoflowDetail.PowGetPv == 420},
		{"detail pv2", tele.EcoflowDetail.PowGetPv2 != nil && *tele.EcoflowDetail.PowGetPv2 == 310},
		{"model_name", tele.ModelName == "Delta 3 Max Plus"},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("check %q failed: %+v", c.name, tele)
		}
	}
}

func TestParamsCamelCaseAliases(t *testing.T) {
	tele := ParamsToTelemetry(Params{
		"cmsBattSoc": 50.0, "powGetPv": 100.0, "powOutSumW": 40.0, "flowInfoAcOut": 4.0,
	}, TelemetryOpts{})
	if *tele.BatteryPercent != 50 || tele.SolarInputW != 100 || tele.OutputPowerW != 40 || tele.ACOn {
		t.Fatalf("%+v", tele)
	}
}

func TestTelemetryJSONKeysMatchPython(t *testing.T) {
	b, _ := json.Marshal(ParamsToTelemetry(Params{"cms_batt_soc": 55.0}, TelemetryOpts{}))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"battery_percent", "battery_status", "battery_temp_c", "solar_input_w",
		"solar_inputs", "ac_input_w", "output_power_w", "input_power_w", "car_input_w", "feed_in_w",
		"ac_on", "dc_on", "usb_on", "car_on", "time_to_full_h", "time_remaining_h", "source",
		"model_name", "ecoflow_detail"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
	if _, ok := m["capacity_wh"]; ok {
		t.Error("capacity_wh must be omitted when unknown")
	}
}

func TestParseJSONParamsEnvelope(t *testing.T) {
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x"})
	defer c.Close()
	payload, _ := json.Marshal(map[string]any{"params": map[string]any{
		"cms_batt_soc": 61, "pow_get_pv": 200, "pow_get_pv2": 150, "pow_out_sum_w": 90,
	}})
	params := c.ParsePayload(payload)
	if asF(params["cms_batt_soc"]) != 61 || asF(params["pow_get_pv"]) != 200 {
		t.Fatalf("params = %v", params)
	}
	tele := ParamsToTelemetry(params, TelemetryOpts{Alias: "Delta"})
	if tele.SolarInputW != 350 || len(tele.SolarInputs) != 2 {
		t.Fatalf("%+v", tele)
	}
}

func TestOnConnectAuthFailureSetsFlags(t *testing.T) {
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x"})
	c.mqttClientID = "ANDROID_ABCDEF0123456789ABCDEF0123456789_1"
	c.OnConnect(nil, ConnackReason{Code: 5})
	if c.Connected() || !c.AuthFailed() {
		t.Fatalf("connected=%v auth_failed=%v", c.Connected(), c.AuthFailed())
	}
	e := c.MQTTError()
	if !strings.Contains(strings.ToLower(e), "not authorized") || !strings.Contains(e, "rc=5") {
		t.Fatalf("err = %q", e)
	}
}

type fakeSession struct {
	mu        sync.Mutex
	subs      []Subscription
	published []pub
	stopped   bool
	pubErr    error
}
type pub struct {
	topic   string
	qos     byte
	payload []byte
}

func (f *fakeSession) Subscribe(s []Subscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, s...)
	return nil
}
func (f *fakeSession) Publish(topic string, qos byte, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pubErr != nil {
		return f.pubErr
	}
	f.published = append(f.published, pub{topic, qos, payload})
	return nil
}
func (f *fakeSession) Stop() { f.stopped = true }

func TestOnConnectReasonCodeSuccess(t *testing.T) {
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x"})
	c.mqttClientID = "ANDROID_ABCDEF0123456789ABCDEF0123456789_1"
	c.userID = "1"
	c.OnConnect(&fakeSession{}, ConnackReason{Code: 0, Name: "Success"})
	if !c.Connected() || c.AuthFailed() || c.MQTTError() != "" {
		t.Fatal("expected clean connect")
	}
}

func TestOnConnectReasonCodeNotAuthorized(t *testing.T) {
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x"})
	c.OnConnect(nil, ConnackReason{Code: -1, Name: "Not authorized"})
	e := c.MQTTError()
	if c.Connected() || !c.AuthFailed() ||
		!strings.Contains(strings.ToLower(e), "not authorized") ||
		strings.Contains(e, "rc=-1") || !strings.Contains(e, "Not authorized") {
		t.Fatalf("connected=%v auth=%v err=%q", c.Connected(), c.AuthFailed(), e)
	}
}

func TestOnConnectSubscribesAndRequestsQuota(t *testing.T) {
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x"})
	c.userID = "42"
	c.ConfigureDevices([]DeviceMeta{{RawSN: "R351"}})
	s := &fakeSession{}
	c.SetSession(s, "42", false)
	c.OnConnect(s, ConnackReason{})
	if len(s.subs) != 3 || s.subs[0].Topic != "/app/device/property/R351" ||
		s.subs[1].Topic != "/app/42/R351/thing/property/set_reply" ||
		s.subs[2].Topic != "/app/42/R351/thing/property/get_reply" {
		t.Fatalf("subs = %+v", s.subs)
	}
	if len(s.published) != 1 || s.published[0].topic != "/app/42/R351/thing/property/get" || s.published[0].qos != 1 {
		t.Fatalf("published = %+v", s.published)
	}
}

type fakeHTTP struct {
	req  *http.Request
	body []byte
	resp func(*http.Request) *http.Response
}

func (f *fakeHTTP) Do(r *http.Request) (*http.Response, error) {
	f.req = r
	if r.Body != nil {
		f.body, _ = io.ReadAll(r.Body)
	}
	return f.resp(r), nil
}

func jsonResp(code int, v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}
}

func TestCallAPISendsUserIDAsFormBody(t *testing.T) {
	h := &fakeHTTP{resp: func(*http.Request) *http.Response {
		return jsonResp(200, map[string]any{"message": "Success", "code": "0", "data": map[string]any{"ok": true}})
	}}
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x", HTTP: h})
	c.token, c.userID = "tok", "2104939217409142785"
	body, err := c.callAPI(context.Background(), "/iot-auth/app/certification", nil)
	if err != nil {
		t.Fatal(err)
	}
	if mapOf(body["data"])["ok"] != true {
		t.Fatalf("body = %v", body)
	}
	if h.req.Method != "GET" || !strings.HasSuffix(h.req.URL.Path, "/iot-auth/app/certification") {
		t.Fatalf("req = %s %s", h.req.Method, h.req.URL)
	}
	if h.req.URL.Query().Has("userId") {
		t.Fatal("userId must not be in the query string")
	}
	if h.req.Header.Get("content-type") != "application/x-www-form-urlencoded" {
		t.Fatalf("content-type = %q", h.req.Header.Get("content-type"))
	}
	if string(h.body) != "userId=2104939217409142785" {
		t.Fatalf("body = %q", h.body)
	}
	if h.req.Header.Get("authorization") != "Bearer tok" {
		t.Fatalf("authorization = %q", h.req.Header.Get("authorization"))
	}
}

func TestLoginFlow(t *testing.T) {
	var calls []string
	h := &fakeHTTP{}
	h.resp = func(r *http.Request) *http.Response {
		calls = append(calls, r.URL.Path)
		if r.URL.Path == "/auth/login" {
			return jsonResp(200, map[string]any{"message": "Success", "data": map[string]any{
				"token": "T", "user": map[string]any{"userId": "2104939217409142785"}}})
		}
		return jsonResp(200, map[string]any{"message": "Success", "code": "0", "data": map[string]any{
			"url": "mqtt-e.ecoflow.com", "port": "8883",
			"certificateAccount": "cert-user", "certificatePassword": "cert-pass"}})
	}
	c := NewClient(ClientOptions{Email: " a@b.c ", Password: "pw", HTTP: h})
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || c.Token() != "T" || c.UserID() != "2104939217409142785" || !clientIDRe.MatchString(c.MQTTClientID()) {
		t.Fatalf("calls=%v token=%s uid=%s cid=%s", calls, c.Token(), c.UserID(), c.MQTTClientID())
	}
	if c.mqttURL != "mqtt-e.ecoflow.com" || c.mqttPort != 8883 {
		t.Fatalf("mqtt = %s:%d", c.mqttURL, c.mqttPort)
	}
}

func TestLoginRejected(t *testing.T) {
	h := &fakeHTTP{resp: func(*http.Request) *http.Response {
		return jsonResp(200, map[string]any{"message": "Bad credentials", "code": "8521"})
	}}
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x", HTTP: h})
	err := c.Login(context.Background())
	var ae *ErrAuth
	if err == nil || !asErr(err, &ae) {
		t.Fatalf("want auth error, got %v", err)
	}
}

func asErr[T any](err error, target *T) bool {
	for err != nil {
		if e, ok := err.(T); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

type fakeTransport struct {
	opts MQTTOptions
	cb   MQTTCallbacks
	sess *fakeSession
	// auto: CONNACK result delivered right away (synchronously after Start).
	auto *ConnackReason
	err  error
}

func (f *fakeTransport) Start(o MQTTOptions, cb MQTTCallbacks) (MQTTSession, error) {
	f.opts, f.cb = o, cb
	if f.err != nil {
		return nil, f.err
	}
	f.sess = &fakeSession{}
	if f.auto != nil {
		r := *f.auto
		go cb.OnConnect(f.sess, r)
	}
	return f.sess, nil
}

func readyClient(tr MQTTTransport) *Client {
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x", MQTT: tr})
	c.mqttURL, c.mqttPort = "mqtt-e.ecoflow.com", 8883
	c.mqttUser, c.mqttPassword = "cert-user", "cert-pass"
	c.mqttClientID = "ANDROID_ABCDEF0123456789ABCDEF0123456789_1"
	c.userID = "1"
	return c
}

func TestStartMQTTUsesKeepalive15AndClientID(t *testing.T) {
	ok := ConnackReason{}
	tr := &fakeTransport{auto: &ok}
	c := readyClient(tr)
	defer c.Close()
	if MQTTKeepalive != 15*time.Second {
		t.Fatalf("keepalive = %v", MQTTKeepalive)
	}
	got, err := c.StartMQTT(context.Background(), time.Second)
	if err != nil || !got {
		t.Fatalf("StartMQTT = %v, %v", got, err)
	}
	want := MQTTOptions{Host: "mqtt-e.ecoflow.com", Port: 8883, ClientID: c.MQTTClientID(),
		Username: "cert-user", Password: "cert-pass", KeepAlive: 15 * time.Second}
	if tr.opts != want {
		t.Fatalf("opts = %+v", tr.opts)
	}
}

func TestStartMQTTAuthRefused(t *testing.T) {
	bad := ConnackReason{Code: 5}
	tr := &fakeTransport{auto: &bad}
	c := readyClient(tr)
	defer c.Close()
	got, _ := c.StartMQTT(context.Background(), time.Second)
	if got || !c.AuthFailed() || !tr.sess.stopped {
		t.Fatalf("got=%v auth=%v stopped=%v", got, c.AuthFailed(), tr.sess.stopped)
	}
}

func TestStartMQTTTimeoutAndDialError(t *testing.T) {
	c := readyClient(&fakeTransport{})
	got, _ := c.StartMQTT(context.Background(), time.Second)
	if got || c.MQTTError() != "MQTT connect timed out" {
		t.Fatalf("got=%v err=%q", got, c.MQTTError())
	}
	c2 := readyClient(&fakeTransport{err: io.ErrUnexpectedEOF})
	got, _ = c2.StartMQTT(context.Background(), time.Second)
	if got || !strings.HasPrefix(c2.MQTTError(), "MQTT connect error:") {
		t.Fatalf("got=%v err=%q", got, c2.MQTTError())
	}
	c3 := NewClient(ClientOptions{})
	if _, err := c3.StartMQTT(context.Background(), time.Second); err == nil {
		t.Fatal("expected 'login first'")
	}
}

func TestDecodePropertyPayloadRejectsGarbage(t *testing.T) {
	if got := DecodePropertyPayload([]byte("not-a-protobuf")); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func encVarint(n uint64) []byte {
	var out []byte
	for {
		b := byte(n & 0x7F)
		n >>= 7
		if n != 0 {
			out = append(out, b|0x80)
		} else {
			return append(out, b)
		}
	}
}

func f32le(f float32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, math.Float32bits(f))
	return b
}

func TestExtractUndeclaredFloatsPowGetPv2(t *testing.T) {
	// Max Plus PV2 is wire field 70 — absent from the vendored descriptor.
	pdata := append(encVarint((70<<3)|5), f32le(214.0)...)
	if got := ExtractUndeclaredFloats(pdata, nil)["pow_get_pv2"]; got != 214.0 {
		t.Fatalf("got %v", got)
	}
}

func TestDecodeDisplayUploadRecoversMaxPlusPV2(t *testing.T) {
	pdata := append(encVarint((361<<3)|5), f32le(79.0)...)
	pdata = append(pdata, encVarint((70<<3)|5)...)
	pdata = append(pdata, f32le(214.0)...)
	i32 := func(v int32) *int32 { return &v }
	pkt := &efpb.Delta3HeaderMessage{Header: []*efpb.Delta3Header{{
		Src: i32(2), CmdFunc: i32(254), CmdId: i32(21), EncType: i32(0), Seq: i32(1), Pdata: pdata,
	}}}
	raw, _ := proto.Marshal(pkt)
	decoded := DecodePropertyPayload(raw)
	if decoded["pow_get_pv"] != 79.0 || decoded["pow_get_pv2"] != 214.0 {
		t.Fatalf("decoded = %v", decoded)
	}
	tele := ParamsToTelemetry(decoded, TelemetryOpts{Alias: "Delta 3 Max Plus"})
	if tele.SolarInputW != 293 || len(tele.SolarInputs) != 2 || tele.SolarInputs[1].ID != "pow_get_pv2" ||
		*tele.EcoflowDetail.PowGetPv2 != 214.0 {
		t.Fatalf("%+v", tele)
	}
	if cmds := PeekHeaderCmds(raw); len(cmds) != 1 || cmds[0] != "254/21" {
		t.Fatalf("cmds = %v", cmds)
	}
}

func TestDecodeXorEncodedAndBase64(t *testing.T) {
	seq := int32(7)
	plain := &efpb.Delta3DisplayPropertyUpload{}
	soc := float32(55)
	plain.CmsBattSoc = &soc
	pdata, _ := proto.Marshal(plain)
	enc := make([]byte, len(pdata))
	for i, b := range pdata {
		enc[i] = b ^ byte(seq)
	}
	i32 := func(v int32) *int32 { return &v }
	raw, _ := proto.Marshal(&efpb.Delta3HeaderMessage{Header: []*efpb.Delta3Header{{
		Src: i32(2), CmdFunc: i32(254), CmdId: i32(21), EncType: i32(1), Seq: &seq, Pdata: enc,
	}}})
	if got := DecodePropertyPayload(raw); got["cms_batt_soc"] != 55.0 {
		t.Fatalf("xor decode: %v", got)
	}
}

func TestOnMessageEmptyProtobufIncrementsDecodeEmpty(t *testing.T) {
	var updates []string
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x",
		OnUpdate: func(sn string, _ Telemetry, _ Detail) { updates = append(updates, sn) }})
	c.ConfigureDevices([]DeviceMeta{{RawSN: "R351TEST", Alias: "G"}})
	c.OnMessage("/app/device/property/R351TEST", []byte("not-a-protobuf"))
	st := c.Stats()
	if st.MsgCount != 1 || st.DecodeEmpty != 1 || st.UpdateCount != 0 || len(updates) != 0 {
		t.Fatalf("%+v updates=%v", st, updates)
	}
}

func TestOnMessageJSONParamsIncrementsUpdateCount(t *testing.T) {
	type upd struct {
		sn   string
		tele Telemetry
	}
	var updates []upd
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x",
		OnUpdate: func(sn string, tele Telemetry, _ Detail) { updates = append(updates, upd{sn, tele}) }})
	c.ConfigureDevices([]DeviceMeta{{RawSN: "R351TEST", Alias: "Garage", DeviceType: "DELTA_3_MAX_PLUS", CapacityWh: 2048}})
	topic := "/app/device/property/R351TEST"
	payload, _ := json.Marshal(map[string]any{"params": map[string]any{
		"cms_batt_soc": 55, "pow_get_pv": 120, "pow_out_sum_w": 40}})
	c.OnMessage(topic, payload)
	st := c.Stats()
	if st.MsgCount != 1 || st.DecodeEmpty != 0 || st.UpdateCount != 1 || len(updates) != 1 {
		t.Fatalf("%+v", st)
	}
	if updates[0].sn != "ecoflow:R351TEST" || *updates[0].tele.BatteryPercent != 55 || updates[0].tele.SolarInputW != 120 {
		t.Fatalf("%+v", updates[0])
	}
	if st.LastTopic == nil || *st.LastTopic != topic {
		t.Fatalf("last topic = %v", st.LastTopic)
	}
	snap := c.Snapshot()["R351TEST"]
	if *snap.Telemetry.BatteryPercent != 55 || *snap.Telemetry.CapacityWh != 2048 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestOnUpdateCallbackPanicDoesNotEscape(t *testing.T) {
	c := NewClient(ClientOptions{OnUpdate: func(string, Telemetry, Detail) { panic("boom") }})
	c.ConfigureDevices([]DeviceMeta{{RawSN: "R1"}})
	payload, _ := json.Marshal(map[string]any{"params": map[string]any{"cms_batt_soc": 1}})
	c.OnMessage("/app/device/property/R1", payload) // must not panic
}

func TestRequestQuotaSkipsPublishWhenPayloadEmpty(t *testing.T) {
	c := NewClient(ClientOptions{})
	s := &fakeSession{}
	c.SetSession(s, "123", false)
	c.QuotaBuilder = func(string) []byte { return nil }
	c.RequestQuota("R351TEST")
	if len(s.published) != 0 {
		t.Fatal("must not publish")
	}
	c.QuotaBuilder = BuildQuotaRequest
	c.RequestQuota("R351TEST")
	if len(s.published) != 1 || len(s.published[0].payload) == 0 {
		t.Fatalf("published = %+v", s.published)
	}
}

func TestBuildSetCommandACDC(t *testing.T) {
	ac := BuildSetCommand("cfg_ac_out_open", 1, "D3M1TEST")
	dc := BuildSetCommand("cfg_dc12v_out_open", 0, "D3M1TEST")
	if len(ac) <= 10 || len(dc) <= 10 {
		t.Fatalf("lens %d %d", len(ac), len(dc))
	}
	if BuildSetCommand("not_a_field", 1, "D3M1TEST") != nil {
		t.Fatal("unknown field must return nil")
	}
	// Round-trip: the header carries cmd 254/17 and the field value.
	var pkt efpb.Delta3SendHeaderMsg
	if err := proto.Unmarshal(ac, &pkt); err != nil || len(pkt.Msg) != 1 {
		t.Fatalf("unmarshal: %v", err)
	}
	h := pkt.Msg[0]
	if h.GetCmdFunc() != 254 || h.GetCmdId() != 17 || h.GetDeviceSn() != "D3M1TEST" || h.GetDest() != 2 || h.GetVersion() != 19 {
		t.Fatalf("header = %v", h)
	}
	var cmd efpb.Delta3SetCommand
	_ = proto.Unmarshal(h.GetPdata(), &cmd)
	if cmd.CfgAcOutOpen == nil || *cmd.CfgAcOutOpen != 1 {
		t.Fatalf("cmd = %v", &cmd)
	}
}

func TestSetOutputPublishesConfigWrite(t *testing.T) {
	c := NewClient(ClientOptions{Email: "a@b.c", Password: "x"})
	s := &fakeSession{}
	c.SetSession(s, "2104", true)
	c.ConfigureDevices([]DeviceMeta{{RawSN: "D3M1TEST", Alias: "G"}})
	if err := c.SetOutput("D3M1TEST", "ac", true); err != nil {
		t.Fatal(err)
	}
	if len(s.published) != 1 || s.published[0].topic != "/app/2104/D3M1TEST/thing/property/set" || len(s.published[0].payload) <= 10 {
		t.Fatalf("published = %+v", s.published)
	}
	if c.ParamsFor("D3M1TEST")["cfg_ac_out_open"] != 1.0 {
		t.Fatalf("params = %v", c.ParamsFor("D3M1TEST"))
	}
	// guards
	if err := c.SetOutput("D3M1TEST", "bogus", true); err == nil {
		t.Fatal("unsupported port")
	}
	if err := c.SetOutput("UNKNOWN", "ac", true); err == nil {
		t.Fatal("unknown device")
	}
	c.SetSession(s, "2104", false)
	if err := c.SetOutput("D3M1TEST", "ac", true); err == nil {
		t.Fatal("not connected")
	}
}

func TestDeriveOutputsFromFlowInfo(t *testing.T) {
	r := Params{"flow_info_12v": 14.0, "flow_info_ac_out": 4.0,
		"flow_info_qcusb1": 4.0, "flow_info_typec1": 14.0}
	deriveOutputs(r)
	if r["cfg_dc12v_out_open"] != 1.0 || r["cfg_ac_out_open"] != 0.0 || r["cfg_usb_open"] != 1.0 {
		t.Fatalf("%v", r)
	}
	r = Params{"flow_info_qcusb1": 4.0, "flow_info_typec1": 4.0}
	deriveOutputs(r)
	if r["cfg_usb_open"] != 0.0 {
		t.Fatalf("%v", r)
	}
}

func TestSnFromTopicVariants(t *testing.T) {
	c := NewClient(ClientOptions{})
	c.ConfigureDevices([]DeviceMeta{{RawSN: "R1"}})
	cases := map[string]string{
		"/app/device/property/R1":             "R1",
		"/app/device/property/R9":             "R9",
		"/app/42/R1/thing/property/get_reply": "R1",
		"/app/42/R9/thing/property/get_reply": "",
		"/other/topic":                        "",
		"/app/device/other/R5":                "",
	}
	for topic, want := range cases {
		if got := c.snFromTopic(topic); got != want {
			t.Errorf("snFromTopic(%q) = %q, want %q", topic, got, want)
		}
	}
}

func TestMergeParamsIgnoresNilAndDoesNotMutate(t *testing.T) {
	dst := Params{"a": 1.0}
	out := MergeParams(dst, Params{"a": nil, "b": 2.0})
	if out["a"] != 1.0 || out["b"] != 2.0 || len(dst) != 1 {
		t.Fatalf("out=%v dst=%v", out, dst)
	}
}
