package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/bridge"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase"
	"github.com/spf13/cobra"
	"os"
	"time"
)

func diagnoseCommand(app *pocketbase.PocketBase) *cobra.Command {
	return diagnosticCommand(app, func(creds map[string]any) *bridge.Cloud {
		return &bridge.Cloud{Email: fmtString(creds["email"]), Password: fmtString(creds["password"]), Region: fmtString(creds["region"])}
	})
}
func diagnosticCommand(app *pocketbase.PocketBase, factory func(map[string]any) *bridge.Cloud) *cobra.Command {
	var devices bool
	cmd := &cobra.Command{Use: "diagnose", Short: "Explicit Jackery login/device diagnostic using saved encrypted credentials", RunE: func(cmd *cobra.Command, args []string) error {
		key, err := store.ReadExistingKey(os.Getenv("JACKERY_AT_REST_KEY_FILE"))
		if err != nil {
			return err
		}
		if err = app.Bootstrap(); err != nil {
			return err
		}
		defer app.ResetBootstrapState()
		creds, err := (&store.Store{App: app}).Secret(key, "jackery")
		if err != nil {
			return errors.New("saved Jackery credentials unavailable")
		}
		c := factory(creds)
		defer c.Close()
		c.TLSConfig, err = bridge.TLSFromCA(os.Getenv("JACKERY_MQTT_CA_FILE"))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		if err = c.Login(ctx); err != nil {
			return err
		}
		out := map[string]any{"login": "ok"}
		if devices {
			list, err := c.Devices(ctx)
			if err != nil {
				return err
			}
			out["devices"] = list
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}}
	cmd.Flags().BoolVar(&devices, "devices", false, "Also list bound devices (never issue control commands)")
	return cmd
}
func fmtString(v any) string { s, _ := v.(string); return s }
