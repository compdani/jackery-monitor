package main

import (
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase"
	"github.com/spf13/cobra"
	"os"
)

func importConfigCommand(app *pocketbase.PocketBase) *cobra.Command {
	var dry bool
	var manifest string
	cmd := &cobra.Command{Use: "import-config LEGACY_DIRECTORY", Short: "Insert legacy JSON config/accounts using an existing key; sources remain read-only", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		paths := map[string]string{}
		if manifest != "" {
			data, err := os.ReadFile(manifest)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(data, &paths); err != nil {
				return errors.New("manifest must map source names to paths")
			}
		}
		known := map[string]bool{}
		for _, src := range store.ConfigSources {
			known[src.Name] = true
		}
		for name := range paths {
			if !known[name] {
				return errors.New("manifest contains an unknown source name")
			}
		}
		for _, src := range store.ConfigSources {
			if paths[src.Name] == "" && os.Getenv(src.Env) != "" {
				paths[src.Name] = os.Getenv(src.Env)
			}
		}
		// Validate the existing key before PocketBase bootstrap creates any files.
		keyPath := os.Getenv("JACKERY_AT_REST_KEY_FILE")
		if _, err := store.ReadExistingKey(keyPath); err != nil {
			return err
		}
		var st *store.Store
		if !dry {
			if err := app.Bootstrap(); err != nil {
				return err
			}
			defer app.ResetBootstrapState()
			if err := app.RunAllMigrations(); err != nil {
				return err
			}
			st = &store.Store{App: app}
		}
		reports, err := st.ImportConfig(cmd.Context(), store.ConfigImportOptions{Directory: args[0], Paths: paths, KeyPath: keyPath, DryRun: dry})
		if e := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"dry_run": dry, "sources": reports}); e != nil {
			return e
		}
		return err
	}}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Validate sources without opening or modifying destination")
	cmd.Flags().StringVar(&manifest, "manifest", "", "JSON map of source names to explicit input paths (overrides source environment variables)")
	return cmd
}
