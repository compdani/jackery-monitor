package jackery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"jackery-monitor/internal/secrets"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"
)

// An in-memory MQTT 3.1.1 broker verifies the actual Paho wire path, including
// resubscription after transport loss and QoS 1 command acknowledgement.
func TestMQTTWireReconnectAndCommand(t *testing.T) {
	c := New(secrets.Credentials{})
	c.UserID = "42"
	c.MQTTKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	var attempts atomic.Int32
	connections := make(chan net.Conn, 4)
	deltas := make(chan string, 4)
	published := make(chan *packets.PublishPacket, 4)
	c.configureMQTT = func(opts *mqtt.ClientOptions) {
		opts.SetCustomOpenConnectionFn(func(_ *url.URL, _ mqtt.ClientOptions) (net.Conn, error) {
			client, server := net.Pipe()
			attempts.Add(1)
			connections <- server
			go func() {
				defer server.Close()
				for {
					packet, err := packets.ReadPacket(server)
					if err != nil {
						return
					}
					switch p := packet.(type) {
					case *packets.ConnectPacket:
						if p.ClientIdentifier != "42@APP" || p.Username != "42@"+c.MacID {
							t.Error("incorrect broker identity")
						}
						ack := packets.NewControlPacket(packets.Connack)
						if ack.Write(server) != nil {
							return
						}
					case *packets.SubscribePacket:
						if len(p.Topics) != 1 || p.Topics[0] != "hb/app/42/device" {
							t.Error("incorrect subscription")
						}
						ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
						ack.MessageID = p.MessageID
						ack.ReturnCodes = []byte{1}
						if ack.Write(server) != nil {
							return
						}
						delta := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
						delta.TopicName = "hb/app/42/device"
						delta.Payload = []byte(`{"messageType":"DevicePropertyChange","deviceSn":"SN2","body":{"ip":900}}`)
						if delta.Write(server) != nil {
							return
						}
					case *packets.PublishPacket:
						published <- p
						ack := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
						ack.MessageID = p.MessageID
						if ack.Write(server) != nil {
							return
						}
					case *packets.PingreqPacket:
						if packets.NewControlPacket(packets.Pingresp).Write(server) != nil {
							return
						}
					case *packets.DisconnectPacket:
						return
					}
				}
			}()
			return client, nil
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	defer c.Close()
	if err := c.Subscribe(ctx, func(sn string, _ map[string]any) { deltas <- sn }, func(bool, string) {}); err != nil {
		t.Fatal(err)
	}
	select {
	case sn := <-deltas:
		if sn != "SN2" {
			t.Fatal("wrong delta serial")
		}
	case <-ctx.Done():
		t.Fatal("no initial delta")
	}
	first := <-connections
	first.Close()
	select {
	case <-deltas:
	case <-ctx.Done():
		t.Fatal("did not resubscribe after reconnect")
	}
	if attempts.Load() < 2 {
		t.Fatal("no reconnect")
	}
	if err := c.SetOutput(ctx, "SN2", "ac", false); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-published:
		if p.TopicName != "hb/app/42/command" || p.Qos != 1 || p.Retain {
			t.Fatal("bad publish flags")
		}
		var command map[string]any
		if json.Unmarshal(p.Payload, &command) != nil || command["deviceSn"] != "SN2" || command["actionId"] != float64(4) {
			t.Fatal("bad wire command")
		}
	case <-ctx.Done():
		t.Fatal("no command received")
	}
}
