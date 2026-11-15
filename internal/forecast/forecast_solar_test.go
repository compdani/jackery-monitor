package forecast

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSolarFetchCacheAndRateLimit(t *testing.T) {
	calls := 0
	limited := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if limited {
			w.Header().Set("X-Ratelimit-Retry-At", time.Now().Add(time.Hour).Format(time.RFC3339))
			w.WriteHeader(429)
			fmt.Fprint(w, `{"message":{}}`)
			return
		}
		fmt.Fprint(w, `{"result":{"watts":{"2024-07-01 08:15:00":100,"2024-07-01 08:45:00":300,"2024-07-01 09:00:00":50,"bad":1,"2024-07-01 10:00:00":"bad"}}}`)
	}))
	defer srv.Close()
	c := &SolarClient{BaseURL: srv.URL}
	fetch := func() SolarEstimate { return c.Fetch(context.Background(), 37, -122, 20, 0, 2, -7*3600, "") }
	out := fetch()
	if out.Error != "" || len(out.Hourly) != 2 || Number(out.Hourly[0], "watts") != 200 {
		t.Fatal(out)
	}
	want := time.Date(2024, 7, 1, 15, 0, 0, 0, time.UTC).Unix()
	if int64(Number(out.Hourly[0], "ts")) != want {
		t.Fatal(out)
	}
	fetch()
	if calls != 1 {
		t.Fatal(calls)
	}
	for k, v := range c.cache {
		v.FetchedAt -= 7200
		c.cache[k] = v
	}
	limited = true
	out = fetch()
	if !out.Stale || out.Error != "rate limited" || calls != 2 {
		t.Fatal(out, calls)
	}
	out = fetch()
	if !out.Stale || calls != 2 {
		t.Fatal(out, calls)
	}
	c.cache = nil
	out = fetch()
	if len(out.Hourly) != 0 || out.Error == "" || calls != 2 {
		t.Fatal(out, calls)
	}
}
func TestSolarErrorsAndKeyURL(t *testing.T) {
	if got := EstimateURL(52, 12, 37, 0, 5.67, "secret-key"); got != "https://api.forecast.solar/secret-key/estimate/52/12/37/0/5.67" {
		t.Fatal(got)
	}
	c := &SolarClient{HTTP: &http.Client{Transport: weatherFailure{}}}
	out := c.Fetch(context.Background(), 37, -122, 20, 0, 2, 0, "secret")
	if out.Error == "" || len(out.Hourly) != 0 {
		t.Fatal(out)
	}
}
