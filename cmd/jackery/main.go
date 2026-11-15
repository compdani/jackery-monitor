package main

import (
	"context"
	"github.com/compdani/jackery-monitor/internal/ble/blehw"
	"github.com/compdani/jackery-monitor/internal/bridge"
	"github.com/compdani/jackery-monitor/internal/server"
	"github.com/compdani/jackery-monitor/internal/store"
	_ "github.com/compdani/jackery-monitor/migrations"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"log"
	"os"
	"time"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	app := pocketbase.New()
	app.RootCmd.AddCommand(importConfigCommand(app))
	app.RootCmd.AddCommand(importHistoryCommand(app))
	app.RootCmd.AddCommand(diagnoseCommand(app))

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		key, err := store.LoadKey(env("JACKERY_AT_REST_KEY_FILE", app.DataDir()+"/.jackery-creds.key"))
		if err != nil {
			return err
		}
		public := env("JACKERY_UI_DIR", "pb_public")
		if _, err := os.Stat(public + "/index.html"); err != nil {
			for _, p := range []string{"ui/build", "web/next"} {
				if _, err := os.Stat(p + "/index.html"); err == nil {
					public = p
					break
				}
			}
		}
		s := &server.Server{Store: &store.Store{App: app}, Key: key, PublicDir: public, Bridge: &bridge.RPC{Address: env("BRIDGE_URL", "127.0.0.1:8766")}}
		s.BLEAdapter = blehw.New()
		s.CFAccess = server.CFAccessFromEnv()
		if env("BACKEND", "native") == "native" {
			s.UseNativeCloud()
		}
		s.Register(e)
		ctx, cancel := context.WithCancel(context.Background())
		app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			cancel()
			if closer, ok := s.Bridge.(interface{ Close() }); ok {
				closer.Close()
			}
			return e.Next()
		})
		go s.RescueLoop(ctx)
		go s.SmartChargeLoop(ctx)
		go s.SolarChargeLoop(ctx)
		go s.ForecastRecorderLoop(ctx)
		go s.KasaReconcileLoop(ctx)
		go s.SiseliLoop(ctx)
		go s.EcoFlowLoop(ctx)
		go s.BMSLoop(ctx)
		go s.AdvisorLoop(ctx)
		go s.RunNativeRealtime(ctx)
		go s.Poll(ctx, 10*time.Second)
		go s.PollTesla(ctx)
		return e.Next()
	})
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
