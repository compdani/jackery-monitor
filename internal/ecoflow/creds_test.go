package ecoflow

import "testing"

func TestCredsNormalizeAndPublicView(t *testing.T) {
	c := Credentials{Email: " user@example.com ", Password: "hunter2", APIHost: "API-E.ecoflow.com", Token: "t"}.Normalize()
	if c.Email != "user@example.com" || c.APIHost != "api-e.ecoflow.com" {
		t.Fatalf("%+v", c)
	}
	if (Credentials{APIHost: "evil.example.com"}).Normalize().APIHost != DefaultAPIHost {
		t.Fatal("disallowed host must fall back")
	}
	v := c.Public()
	if v.Password != "" || v.Token != "" || !v.HasPassword || v.Email != "user@example.com" {
		t.Fatalf("%+v", v)
	}
}

func TestCredsEmptyPasswordKeepsPrevious(t *testing.T) {
	ex := &Credentials{Email: "user@example.com", Password: "hunter2"}
	got, ok := MergeSave(ex, Credentials{Email: "user@example.com", APIHost: "api.ecoflow.com"}, false)
	if !ok || got.Password != "hunter2" {
		t.Fatalf("%+v ok=%v", got, ok)
	}
	if _, ok := MergeSave(nil, Credentials{Email: "a@b.c"}, false); ok {
		t.Fatal("no password must fail")
	}
}

func TestCredsMQTTClientIDRoundTrip(t *testing.T) {
	first, ok := MergeSave(nil, Credentials{Email: "user@example.com", Password: "hunter2",
		MQTTClientID: "ANDROID_ABCDEF0123456789ABCDEF0123456789_99"}, true)
	if !ok || first.MQTTClientID != "ANDROID_ABCDEF0123456789ABCDEF0123456789_99" {
		t.Fatalf("%+v", first)
	}
	id := "ANDROID_FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF_uid-1"
	upd := first.WithSession("tok", "uid-1", &id)
	if upd.Token != "tok" || upd.UserID != "uid-1" || upd.MQTTClientID != id {
		t.Fatalf("%+v", upd)
	}
	// save without mqtt_client_id keeps the previous value
	again, _ := MergeSave(&upd, Credentials{Email: "user@example.com", Password: "hunter2"}, false)
	if again.MQTTClientID != id || again.Token != "tok" {
		t.Fatalf("%+v", again)
	}
}
