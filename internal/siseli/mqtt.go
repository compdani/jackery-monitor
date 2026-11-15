package siseli

import (
	"context"
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

// MQTTStream reassembles MQTT packets across arbitrary TCP read boundaries.
// A stream belongs to one ordered TCP connection; packet acquisition/reordering
// must be handled by the transport before Feed is called.
type MQTTStream struct {
	buffer         []byte
	MaxPacketBytes int
}

func DecodeRemainingLength(b []byte, start int) (value, end int, complete bool) {
	mult := 1
	for i := 0; i < 4; i++ {
		if start+i >= len(b) {
			return 0, 0, false
		}
		x := b[start+i]
		value += int(x&127) * mult
		if x&128 == 0 {
			if i > 0 && x == 0 {
				return 0, -1, false
			}
			return value, start + i + 1, true
		}
		mult *= 128
	}
	return 0, -1, false
}
func ExtractPublish(packet []byte) (string, []byte, error) {
	if len(packet) < 4 || packet[0]>>4 != 3 {
		return "", nil, errors.New("not MQTT PUBLISH")
	}
	n, pos, ok := DecodeRemainingLength(packet, 1)
	if !ok || pos+n != len(packet) {
		return "", nil, errors.New("invalid MQTT length")
	}
	qos := (packet[0] >> 1) & 3
	if qos == 3 || pos+2 > len(packet) {
		return "", nil, errors.New("invalid MQTT header")
	}
	size := int(binary.BigEndian.Uint16(packet[pos:]))
	pos += 2
	if size == 0 || pos+size > len(packet) {
		return "", nil, errors.New("invalid MQTT topic")
	}
	topic := string(packet[pos : pos+size])
	pos += size
	if !utf8.ValidString(topic) {
		return "", nil, errors.New("invalid MQTT topic UTF-8")
	}
	for _, c := range topic {
		if c == 0 || c == '+' || c == '#' {
			return "", nil, errors.New("invalid MQTT topic")
		}
	}
	if qos > 0 {
		if pos+2 > len(packet) || binary.BigEndian.Uint16(packet[pos:]) == 0 {
			return "", nil, errors.New("invalid MQTT packet id")
		}
		pos += 2
	}
	return topic, packet[pos:], nil
}
func (s *MQTTStream) Feed(data []byte) ([]Message, error) {
	max := s.MaxPacketBytes
	if max <= 0 {
		max = 4 << 20
	}
	if len(s.buffer)+len(data) > max {
		s.buffer = nil
		return nil, errors.New("MQTT stream exceeds buffer limit")
	}
	s.buffer = append(s.buffer, data...)
	out := []Message{}
	for len(s.buffer) > 1 {
		n, pos, ok := DecodeRemainingLength(s.buffer, 1)
		if !ok {
			if pos < 0 {
				s.buffer = s.buffer[1:]
				continue
			}
			break
		}
		if n+pos > max {
			s.buffer = nil
			return nil, errors.New("MQTT packet exceeds limit")
		}
		if n+pos > len(s.buffer) {
			break
		}
		packet := s.buffer[:n+pos]
		if packet[0]>>4 == 3 {
			if topic, payload, e := ExtractPublish(packet); e == nil {
				out = append(out, Message{Topic: topic, Payload: append([]byte(nil), payload...)})
			}
		}
		s.buffer = s.buffer[n+pos:]
	}
	return out, nil
}

// ChannelTransport is useful for adapters and fakes. Closing Messages ends Run.
type ChannelTransport struct{ Messages <-chan Message }

func (t ChannelTransport) Run(ctx context.Context, deliver func(Message) error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-t.Messages:
			if !ok {
				return nil
			}
			if err := deliver(m); err != nil {
				return err
			}
		}
	}
}
