package main

import (
	"log"
	"os"
	"path/filepath"

	"jackery-monitor/internal/api"

	"jackery-monitor/internal/live"

	_ "jackery-monitor/pb_migrations"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
)

func main() {
	root := os.Getenv("JACKERY_DATA_DIR")
	if root == "" {
		root = "data"
	}
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: filepath.Join(root, "pb_data")})
	var public string
	app.RootCmd.PersistentFlags().StringVar(&public, "publicDir", "pb_public", "dashboard build directory")
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{Automigrate: true})
	mock := os.Getenv("JACKERY_MOCK") == "1"
	app.RootCmd.PersistentFlags().BoolVar(&mock, "mock", mock, "use simulated devices without cloud access")
	// Parse the custom flag before binding the runtime configuration.
	if err := app.RootCmd.ParseFlags(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
	api.Register(app, &public, live.Options{Mock: mock})
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
