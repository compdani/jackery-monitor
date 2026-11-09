package siseli

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func frame(regs ...uint16) []byte {
	b := []byte{1, 3, byte(2 * len(regs))}
	for _, r := range regs {
		b = binary.BigEndian.AppendUint16(b, r)
	}
	return binary.LittleEndian.AppendUint16(b, CRC16Modbus(b))
}
func payload(blocks map[string][]byte) []byte {
	entries := []Values{}
	for name, b := range blocks {
		entries = append(entries, Values{"cn": name, "co": base64.StdEncoding.EncodeToString(b)})
	}
	b, _ := json.Marshal(Values{"b": Values{"ct": entries}})
	return b
}
func TestLocalFixturesReachSink(t *testing.T) {
	for _, tc := range []struct {
		name   string
		blocks map[string][]byte
		want   Values
	}{{"ascii", map[string][]byte{"2ONL": []byte("4 53.3 88 5.0 0.0"), "2l0E": []byte("229.9 49.9 390 267"), "Mpod": []byte("100.0 2.5 200")}, Values{"bat_cap": 88, "load_w": 267, "pv_w": 200}}, {"panel", map[string][]byte{"WfP8": frame(55, 523, 90, 0, 0, 0, 0), "8eyo": frame(645, 80, 50), "CSu4": {1, 3, 2, 0, 2, 0x39, 0x85}}, Values{"pv_v": 64.5, "pv_a": .8, "pv_w": 50, "generation_power_w": 50, "bat_v": 52.3, "bat_cap": 55, "bat_charge_current": .9}}, {"load", map[string][]byte{"8eyo": frame(679, 30, 22), "WfP8": frame(40, 509, 0, 10537, 517, 2350, 1218)}, Values{"pv_w": 22, "pv_v": 67.9, "pv_a": .3, "bat_cap": 40, "bat_v": 50.9, "bat_charge_current": 0, "dischg_current": 23.5, "load_w": 1218}}} {
		t.Run(tc.name, func(t *testing.T) {
			var got Values
			l := NewLocalClient(LocalConfig{}, SinkFunc(func(ctx context.Context, v Values) error { got = v; return nil }))
			ok, e := l.ParsePayload(context.Background(), Message{Broker: "203.0.113.10", Topic: "dtu/x/pub", Payload: payload(tc.blocks)})
			if !ok || e != nil || got == nil {
				t.Fatal(ok, e)
			}
			for k, want := range tc.want {
				equalJSON(t, got[k], want)
			}
			equalJSON(t, got["broker_ip"], "203.0.113.10")
			tele := ToTelemetry(DecodedToCanonical(got))
			equalJSON(t, tele["battery_percent"], tc.want["bat_cap"])
			equalJSON(t, tele["solar_input_w"], tc.want["pv_w"])
		})
	}
}
func TestCaptureRingAndStreams(t *testing.T) {
	l := NewLocalClient(LocalConfig{}, nil)
	l.NoteStream("198.51.100.8", 1883, Values{"encrypted": false, "payload_bytes": 120})
	row := l.MQTTStreams()[0]
	equalJSON(t, row["payload_bytes"], 120)
	if row["readings"] != nil || row["saw_mqtt"] != nil {
		t.Fatal(row)
	}
	l.NoteStream("198.51.100.8", 1883, Values{"payload_bytes": 10, "saw_mqtt": true, "mqtt_packets": 2})
	row = l.MQTTStreams()[0]
	equalJSON(t, row["payload_bytes"], 130)
	equalJSON(t, row["mqtt_packets"], 2)
	equalJSON(t, row["saw_mqtt"], true)
	m := Message{Broker: "203.0.113.10", Topic: "dtu/x/pub", Payload: payload(map[string][]byte{"WfP8": frame(55, 523, 90), "8eyo": frame(645, 80, 50), "bcCu": frame(0, 428, 710)})}
	_, _ = l.ParsePayload(context.Background(), m)
	if len(l.MQTTCaptures()) != 0 {
		t.Fatal("capture disabled")
	}
	l.SetMQTTCapture(true)
	_, _ = l.ParsePayload(context.Background(), m)
	rows := l.MQTTCaptures()
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	r := rows[0]
	equalJSON(t, r["broker"], m.Broker)
	equalJSON(t, r["topic"], m.Topic)
	equalJSON(t, r["load"], "not mapped")
	equalJSON(t, object(r["mapped"])["pv_w"], 50)
	blocks := r["blocks"].([]Values)
	for _, b := range blocks {
		switch b["name"] {
		case "8eyo":
			equalJSON(t, b["registers"], []int{645, 80, 50})
			equalJSON(t, b["used"], []int{0, 1, 2})
		case "bcCu":
			equalJSON(t, b["registers"], []int{0, 428, 710})
			equalJSON(t, b["used"], []int{})
		}
	}
	if len(l.MQTTCaptures()) != 0 {
		t.Fatal("not drained")
	}
}
func TestLocalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan Message)
	l := NewLocalClient(LocalConfig{}, nil)
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, ChannelTransport{ch}) }()
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation hung")
	}
	if l.Running() {
		t.Fatal("running")
	}
}
func TestModbusRejectsBadCRC(t *testing.T) {
	b := frame(645, 80, 50)
	b[len(b)-1] ^= 1
	if len(DecodeModbus(map[string][]byte{"8eyo": b})) != 0 {
		t.Fatal("bad CRC accepted")
	}
}
func TestMQTTFragments(t *testing.T) {
	body := payload(map[string][]byte{"8eyo": frame(645, 80, 50)})
	topic := "dtu/x/pub"
	content := binary.BigEndian.AppendUint16(nil, uint16(len(topic)))
	content = append(content, []byte(topic)...)
	content = append(content, body...)
	packet := []byte{0x30}
	n := len(content)
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 128
		}
		packet = append(packet, b)
		if n == 0 {
			break
		}
	}
	packet = append(packet, content...)
	stream := MQTTStream{}
	messages := []Message{}
	for _, b := range packet {
		m, e := stream.Feed([]byte{b})
		if e != nil {
			t.Fatal(e)
		}
		messages = append(messages, m...)
	}
	if len(messages) != 1 || messages[0].Topic != topic || string(messages[0].Payload) != string(body) {
		t.Fatal(messages)
	}
}
func piFrame(s string) []byte {
	b := append([]byte{'('}, []byte(s)...)
	return append(binary.BigEndian.AppendUint16(b, CRC16Xmodem(b)), '\r')
}
func TestPI30EvidenceAndCRC(t *testing.T) {
	b := map[string][]byte{"cCft": piFrame("PI30"), "zZ3K": piFrame("B"), "cT7S": piFrame("00001500")}
	r := DecodePI30(b)
	if r.Detection.Verdict != "pi30" {
		t.Fatal(r)
	}
	equalJSON(t, r.Values["pi30_mode"], "Battery")
	equalJSON(t, r.Values["pi30_pv_energy_total_kwh"], 1.5)
	b["cCft"] = piFrame("PI41")
	if DetectPI30(b).Verdict != "not-pi30" {
		t.Fatal("protocol veto ignored")
	}
	b["cCft"][2] ^= 1
	if _, ok := VerifyPI30Frame(b["cCft"]); ok {
		t.Fatal("bad CRC")
	}
}

func TestSelectedBrokerDoesNotPolluteState(t *testing.T) {
	calls := 0
	l := NewLocalClient(LocalConfig{BrokerIP: "203.0.113.10"}, SinkFunc(func(context.Context, Values) error { calls++; return nil }))
	for _, m := range []Message{{Broker: "203.0.113.10", Payload: payload(map[string][]byte{"8eyo": frame(645, 80, 50)})}, {Broker: "203.0.113.11", Payload: payload(map[string][]byte{"WfP8": frame(99, 523, 90)})}} {
		_, e := l.ParsePayload(context.Background(), m)
		if e != nil {
			t.Fatal(e)
		}
	}
	if calls != 1 || l.Snapshot()["bat_cap"] != nil {
		t.Fatal(calls, l.Snapshot())
	}
}
func TestStateCacheDoesNotRestoreLiveness(t *testing.T) {
	l := NewLocalClient(LocalConfig{}, nil)
	_, _ = l.ParsePayload(context.Background(), Message{Payload: payload(map[string][]byte{"8eyo": frame(645, 80, 50)})})
	store := FileStateStore{Path: t.TempDir() + "/nested/state.json"}
	if e := l.Save(store); e != nil {
		t.Fatal(e)
	}
	restored := NewLocalClient(LocalConfig{}, nil)
	if e := restored.Restore(store); e != nil {
		t.Fatal(e)
	}
	equalJSON(t, l.Snapshot(), restored.Snapshot())
	if restored.Fresh(time.Now()) {
		t.Fatal("persisted cache must not be live")
	}
}
