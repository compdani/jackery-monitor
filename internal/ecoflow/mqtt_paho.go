package ecoflow

import (
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// PahoTransport is the production MQTTTransport on eclipse/paho.mqtt.golang
// (MQTT v3.1.1 over TLS, auto-reconnect like paho-python's loop_start).
type PahoTransport struct {
	// TLSConfig overrides the default (system roots, TLS 1.2+).
	TLSConfig *tls.Config
}

type pahoSession struct {
	c    paho.Client
	once sync.Once
}

func (s *pahoSession) Subscribe(subs []Subscription) error {
	filters := make(map[string]byte, len(subs))
	for _, sub := range subs {
		filters[sub.Topic] = sub.QoS
	}
	t := s.c.SubscribeMultiple(filters, nil)
	if !t.WaitTimeout(10 * time.Second) {
		return errors.New("subscribe timed out")
	}
	return t.Error()
}

func (s *pahoSession) Publish(topic string, qos byte, payload []byte) error {
	t := s.c.Publish(topic, qos, false, payload)
	if !t.WaitTimeout(10 * time.Second) {
		return errors.New("publish timed out")
	}
	return t.Error()
}

func (s *pahoSession) Stop() {
	s.once.Do(func() {
		if s.c.IsConnected() || s.c.IsConnectionOpen() {
			s.c.Disconnect(250)
		}
	})
}

// connackFromError maps paho's CONNACK refusal errors onto reason codes.
func connackFromError(err error) ConnackReason {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "bad user name or password"):
		return ConnackReason{Code: 4, Name: "Bad user name or password"}
	case strings.Contains(msg, "not authorized") || strings.Contains(msg, "not authorised"):
		return ConnackReason{Code: 5, Name: "Not authorized"}
	}
	return ConnackReason{Code: -1, Name: ""}
}

// Start dials the broker asynchronously.
func (p *PahoTransport) Start(o MQTTOptions, cb MQTTCallbacks) (MQTTSession, error) {
	tlsCfg := p.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	sess := &pahoSession{}
	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("ssl://%s:%d", o.Host, o.Port)).
		SetClientID(o.ClientID).
		SetUsername(o.Username).
		SetPassword(o.Password).
		SetKeepAlive(o.KeepAlive).
		SetTLSConfig(tlsCfg).
		SetProtocolVersion(4).
		SetAutoReconnect(true).
		SetConnectRetry(false).
		SetCleanSession(true)
	opts.SetOnConnectHandler(func(paho.Client) {
		if cb.OnConnect != nil {
			cb.OnConnect(sess, ConnackReason{Code: 0})
		}
	})
	opts.SetConnectionLostHandler(func(_ paho.Client, err error) {
		if cb.OnDisconnect != nil {
			r := ConnackReason{Code: -1}
			if err != nil {
				r = connackFromError(err)
			}
			cb.OnDisconnect(sess, r)
		}
	})
	opts.SetDefaultPublishHandler(func(_ paho.Client, m paho.Message) {
		if cb.OnMessage != nil {
			cb.OnMessage(m.Topic(), m.Payload())
		}
	})
	sess.c = paho.NewClient(opts)
	tok := sess.c.Connect()
	go func() {
		tok.Wait()
		if err := tok.Error(); err != nil && cb.OnConnect != nil {
			cb.OnConnect(sess, connackFromError(err))
		}
	}()
	return sess, nil
}
