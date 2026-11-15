package siseli

import (
	"encoding/json"
	"strings"
)

// Credentials are pure values. Encryption and durable storage belong to the caller.
type Credentials struct {
	UserID              string `json:"user_id"`
	Password            string `json:"password"`
	StationID           string `json:"station_id"`
	DeviceID            string `json:"device_id"`
	TimeZone            string `json:"time_zone"`
	AccessToken         string `json:"access_token"`
	RefreshToken        string `json:"refresh_token"`
	AccessTokenExpires  string `json:"access_token_expires"`
	RefreshTokenExpires string `json:"refresh_token_expires"`
	LocalRead           bool   `json:"local_read"`
	HybridPull          bool   `json:"hybrid_pull"`
	InverterIP          string `json:"inverter_ip"`
	RouterIP            string `json:"router_ip"`
	SniffIface          string `json:"sniff_iface"`
	InverterMAC         string `json:"inverter_mac"`
	RouterMAC           string `json:"router_mac"`
	MQTTBrokerIP        string `json:"mqtt_broker_ip"`
}

func (c Credentials) Normalize() Credentials {
	for _, p := range []*string{&c.UserID, &c.StationID, &c.DeviceID, &c.TimeZone, &c.InverterIP, &c.RouterIP, &c.SniffIface, &c.InverterMAC, &c.RouterMAC, &c.MQTTBrokerIP} {
		*p = strings.TrimSpace(*p)
	}
	c.InverterMAC = strings.ToLower(c.InverterMAC)
	c.RouterMAC = strings.ToLower(c.RouterMAC)
	return c
}
func (c Credentials) Valid() bool { return c.UserID != "" && c.Password != "" && c.StationID != "" }

type PublicCredentials struct {
	Credentials
	HasPassword bool `json:"has_password"`
}

func (c Credentials) Public() PublicCredentials {
	p := PublicCredentials{Credentials: c, HasPassword: c.Password != ""}
	p.Password, p.AccessToken, p.RefreshToken = "", "", ""
	return p
}
func (c Credentials) WithTokens(a, r, ae, re string) Credentials {
	c.AccessToken, c.RefreshToken, c.AccessTokenExpires, c.RefreshTokenExpires = a, r, ae, re
	return c
}

// MergeCredentials accepts partial updates, ignores nulls, and retains an empty password.
func MergeCredentials(existing *Credentials, fields Values) (Credentials, bool) {
	v := Values{}
	if existing != nil {
		b, _ := json.Marshal(existing)
		_ = json.Unmarshal(b, &v)
	}
	for k, x := range fields {
		if x != nil {
			v[k] = x
		}
	}
	if text(fields["password"]) == "" && existing != nil {
		v["password"] = existing.Password
	}
	for _, k := range []string{"local_read", "hybrid_pull"} {
		if b, ok := v[k].(bool); !ok {
			s := strings.ToLower(strings.TrimSpace(text(v[k])))
			v[k] = s == "1" || s == "true" || s == "yes" || s == "on"
		} else {
			v[k] = b
		}
	}
	b, _ := json.Marshal(v)
	var out Credentials
	if json.Unmarshal(b, &out) != nil {
		return out, false
	}
	out = out.Normalize()
	return out, out.Valid()
}
