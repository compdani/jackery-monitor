package siseli

import (
	"encoding/json"
	"testing"
)

func TestCredentialValues(t *testing.T) {
	c, ok := MergeCredentials(nil, Values{"user_id": " alice ", "password": "hunter2", "station_id": "12345"})
	if !ok || c.LocalRead || c.HybridPull {
		t.Fatal(c)
	}
	for _, fields := range []Values{{"user_id": "", "password": "p", "station_id": "1"}, {"user_id": "u", "password": "", "station_id": "1"}, {"user_id": "u", "password": "p", "station_id": ""}} {
		if _, ok := MergeCredentials(nil, fields); ok {
			t.Fatal(fields)
		}
	}
	c, ok = MergeCredentials(&c, Values{"password": "", "time_zone": "America/Chicago", "local_read": true, "inverter_ip": "192.168.1.50", "router_ip": "192.168.1.1"})
	if !ok || c.Password != "hunter2" || c.TimeZone != "America/Chicago" || !c.LocalRead || c.MQTTBrokerIP != "" {
		t.Fatal(c)
	}
	c, ok = MergeCredentials(&c, Values{"mqtt_broker_ip": "203.0.113.10", "hybrid_pull": true})
	if !ok || !c.HybridPull || c.Public().MQTTBrokerIP != "203.0.113.10" {
		t.Fatal(c)
	}
	c = c.WithTokens("a", "r", "exp-a", "exp-r")
	p := c.Public()
	if !p.HasPassword || p.Password != "" || p.AccessToken != "" || p.RefreshToken != "" || c.AccessTokenExpires != "exp-a" {
		t.Fatal(c, p)
	}
	b, _ := json.Marshal(c)
	var d Credentials
	if e := json.Unmarshal(b, &d); e != nil || d != c {
		t.Fatal(d, e)
	}
}
func TestSNHelpers(t *testing.T) {
	if !IsSiseliSN("siseli:abc") || IsSiseliSN("JK-123") {
		t.Fatal("prefix")
	}
	for _, s := range []string{"abc", "siseli:abc"} {
		got, e := MakeSN(s)
		if e != nil || got != "siseli:abc" {
			t.Fatal(got, e)
		}
	}
	if _, e := MakeSN(""); e == nil {
		t.Fatal("empty id")
	}
	if PortalIDFromSN("siseli:abc") != "abc" {
		t.Fatal("id")
	}
}
