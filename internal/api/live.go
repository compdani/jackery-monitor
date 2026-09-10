package api

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"jackery-monitor/internal/energy"

	"jackery-monitor/internal/forecast"

	"github.com/pocketbase/pocketbase/core"

	"jackery-monitor/internal/live"

	"github.com/pocketbase/pocketbase/apis"

	"jackery-monitor/internal/migrate"
	"jackery-monitor/internal/secrets"
	"jackery-monitor/internal/site"
	"jackery-monitor/internal/tunables"
	"jackery-monitor/internal/wsjson"

	"github.com/coder/websocket"
)

func registerLive(e *core.ServeEvent, options live.Options, gate func(*core.RequestEvent) error) error {
	if !options.Mock {
		if err := migrate.Run(e.App, filepath.Dir(e.App.DataDir())); err != nil {
			return err
		}
	}
	if err := site.Upgrade(e.App); err != nil {
		return err
	}
	store := energy.New(e.App)
	service := live.New(filepath.Dir(e.App.DataDir()), options, func(key string) int { return tunables.Value(e.App, key) })
	fcast := &forecaster{store: store, live: service, weather: forecast.NewWeather(e.App)}
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	var lifecycle sync.Mutex
	closing := false
	var persistenceMu sync.RWMutex
	persistenceError := ""
	snapshotFor := func(view string) map[string]any {
		snap := decorateEnergy(store, service.Snapshot(view))
		persistenceMu.RLock()
		if persistenceError != "" {
			snap["energy_error"] = persistenceError
		}
		persistenceMu.RUnlock()
		return snap
	}
	if !options.Disabled {
		workers.Add(3)
		go func() { defer workers.Done(); fcast.Run(ctx) }()
		go func() { defer workers.Done(); service.Run(ctx) }()
		go func() {
			defer workers.Done()
			persistEnergy(ctx, store, service, func(err error) {
				persistenceMu.Lock()
				defer persistenceMu.Unlock()
				persistenceError = ""
				if err != nil {
					persistenceError = err.Error()
				}
			})
		}()
	}
	e.App.OnTerminate().BindFunc(func(event *core.TerminateEvent) error {
		lifecycle.Lock()
		closing = true
		cancel()
		lifecycle.Unlock()
		workers.Wait()
		return event.Next()
	})
	route := func(method, path string, handler func(*core.RequestEvent) error) {
		e.Router.Route(method, "/japi"+path, handler).BindFunc(gate).Bind(apis.RequireAuth("users"))
	}
	registerEnergy(store, service, route)
	registerForecast(fcast, route)
	failure := func(r *core.RequestEvent, err error) error {
		return r.JSON(400, map[string]string{"detail": err.Error()})
	}
	route("GET", "/status", func(r *core.RequestEvent) error {
		return r.JSON(200, snapshotFor(r.Request.URL.Query().Get("view_device_id")))
	})
	route("GET", "/devices", func(r *core.RequestEvent) error {
		snapshot := service.Snapshot("")
		return r.JSON(200, snapshot["cloud"])
	})
	route("GET", "/auth/status", func(r *core.RequestEvent) error { return r.JSON(200, service.AuthStatus()) })
	route("POST", "/auth/credentials", func(r *core.RequestEvent) error {
		var body secrets.Credentials
		if err := r.BindBody(&body); err != nil {
			return failure(r, err)
		}
		body.Email = strings.TrimSpace(body.Email)
		body.Region = strings.ToUpper(strings.TrimSpace(body.Region))
		if body.Region == "" {
			body.Region = "US"
		}
		if body.Email == "" || body.Password == "" {
			return r.JSON(400, map[string]string{"detail": "email and password are required"})
		}
		if body.Region != "US" {
			return r.JSON(400, map[string]string{"detail": "Only the US cloud region is currently supported"})
		}
		if err := service.SetCredentials(r.Request.Context(), body); err != nil {
			return failure(r, err)
		}
		return r.JSON(200, map[string]bool{"ok": true})
	})
	route("POST", "/auth/forget", func(r *core.RequestEvent) error {
		if err := service.Forget(); err != nil {
			return failure(r, err)
		}
		return r.JSON(200, map[string]bool{"ok": true})
	})
	route("POST", "/set_output", func(r *core.RequestEvent) error {
		var body struct {
			Port string `json:"port"`
			On   *bool  `json:"on"`
			SN   string `json:"device_sn"`
		}
		if err := r.BindBody(&body); err != nil {
			return failure(r, err)
		}
		if body.On == nil {
			return r.JSON(400, map[string]string{"detail": "on must be a boolean"})
		}
		if err := service.SetOutput(r.Request.Context(), body.SN, body.Port, *body.On); err != nil {
			return failure(r, err)
		}
		return r.JSON(200, map[string]any{"ok": true, "port": body.Port, "on": *body.On})
	})
	route("POST", "/pause_polling", func(r *core.RequestEvent) error {
		body := struct {
			Seconds int `json:"seconds"`
		}{600}
		if r.Request.ContentLength != 0 {
			if err := r.BindBody(&body); err != nil {
				return failure(r, err)
			}
		}
		return r.JSON(200, service.Pause(body.Seconds))
	})
	for _, path := range []string{"/resume_polling", "/reconnect"} {
		route("POST", path, func(r *core.RequestEvent) error { return r.JSON(200, service.Resume()) })
	}
	e.Router.GET("/ws", func(r *core.RequestEvent) error {
		query := r.Request.URL.Query()
		token := query.Get("token")
		view := query.Get("view_device_id")
		// Never persist the bearer token as part of PocketBase's activity URL.
		query.Del("token")
		r.Request.URL.RawQuery = query.Encode()
		r.Request.RequestURI = r.Request.URL.RequestURI()
		lifecycle.Lock()
		if closing {
			lifecycle.Unlock()
			return r.JSON(503, map[string]string{"detail": "shutting_down"})
		}
		workers.Add(1)
		lifecycle.Unlock()
		defer workers.Done()
		if token == "" {
			token = strings.TrimPrefix(r.Request.Header.Get("Authorization"), "Bearer ")
		}
		user, err := r.App.FindAuthRecordByToken(token, core.TokenTypeAuth)
		if err != nil || user.Collection().Name != "users" {
			return r.JSON(http.StatusUnauthorized, map[string]string{"detail": "unauthorized"})
		}
		r.Auth = user
		conn, err := websocket.Accept(r.Response, r.Request, nil)
		if err != nil {
			return nil
		}
		defer conn.CloseNow()
		conn.SetReadLimit(4096)
		socketCtx, stop := context.WithCancel(ctx)
		defer stop()
		// Read/discard legacy ping text as well as control frames; cancellation stops
		// the writer on disconnect. Clients never mutate runtime state over /ws.
		go func() {
			defer stop()
			for {
				if _, _, err := conn.Read(socketCtx); err != nil {
					return
				}
			}
		}()
		updates, unsubscribe := service.Subscribe()
		defer unsubscribe()
		send := func(kind string) error {
			writeCtx, done := context.WithTimeout(socketCtx, 5*time.Second)
			defer done()
			return wsjson.Write(writeCtx, conn, map[string]any{"type": kind, "data": snapshotFor(view)})
		}
		if err = send("snapshot"); err != nil {
			return nil
		}
		ticker := time.NewTicker(time.Duration(tunables.Value(r.App, "poll_interval_s")) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-socketCtx.Done():
				return nil
			case <-updates:
			case <-ticker.C:
			}
			// Expiry, account deletion and password changes also revoke open sockets.
			fresh, err := r.App.FindAuthRecordByToken(token, core.TokenTypeAuth)
			if err != nil || fresh.Collection().Name != "users" {
				_ = conn.Close(websocket.StatusPolicyViolation, "Session expired")
				return nil
			}
			if err = send("status"); err != nil {
				return nil
			}
		}
	}).Bind(apis.SkipSuccessActivityLog())
	return nil
}
