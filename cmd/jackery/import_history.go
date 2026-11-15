package main

import (
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase"
	"github.com/spf13/cobra"
)

func importHistoryCommand(app *pocketbase.PocketBase) *cobra.Command {
	var dry bool
	var attachments string
	cmd := &cobra.Command{Use: "import-legacy DATABASE", Short: "Convert a checkpointed legacy SQLite copy with reconciliation (source immutable)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		report, err := st.ImportHistory(cmd.Context(), args[0], store.HistoryImportOptions{DryRun: dry, AttachmentDir: attachments})
		if outputErr := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"dry_run": dry, "reconciliation": report}); outputErr != nil {
			return outputErr
		}
		return err
	}}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Validate and total sources without opening destination or copying attachments")
	cmd.Flags().StringVar(&attachments, "attachments", "", "Copied legacy upload root (containing conversation directories)")
	return cmd
}
