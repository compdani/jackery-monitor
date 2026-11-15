package main

import (
	"context"
	"fmt"
	"github.com/compdani/jackery-monitor/internal/bridge"
	"github.com/compdani/jackery-monitor/internal/server"
	"github.com/compdani/jackery-monitor/internal/store"
	_ "github.com/compdani/jackery-monitor/migrations"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/spf13/cobra"
	"log"
	"os"
	"path/filepath"
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
	app.RootCmd.AddCommand(&cobra.Command{Use: "import-legacy DATABASE", Short: "Archive a legacy SQLite database into PocketBase collections (source stays read-only)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := app.Bootstrap(); err != nil {
			return err
		}
		defer app.ResetBootstrapState()
		if err := app.RunAllMigrations(); err != nil {
			return err
		}
		path, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		counts, err := (&store.Store{App: app}).ImportLegacy(cmd.Context(), path)
		if err != nil {
			return err
		}
		fmt.Println(counts)
		return nil
	}})
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
		if env("BACKEND", "native") == "native" {
			s.UseNativeCloud()
		}
		s.Register(e)
		ctx, cancel := context.WithCancel(context.Background())
		app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error { cancel(); return e.Next() })
		go s.Poll(ctx, 10*time.Second)
		go s.PollTesla(ctx)
		return e.Next()
	})
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
