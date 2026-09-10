package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"jackery-monitor/internal/live"

	"jackery-monitor/internal/tunables"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// Register installs the application hooks before PocketBase starts serving.
func Register(app core.App, public *string, options ...live.Options) {
	var config live.Options
	if len(options) > 0 {
		config = options[0]
	}
	var setupMu sync.Mutex
	app.OnRecordCreateRequest("users").BindFunc(func(e *core.RecordRequestEvent) error {
		setupMu.Lock()
		defer setupMu.Unlock()
		info, err := e.RequestInfo()
		if err != nil {
			return err
		}
		password, _ := info.Body["password"].(string)
		original := e.App
		defer func() { e.App = original }()
		return original.RunInTransaction(func(tx core.App) error {
			count, err := tx.CountRecords("users")
			if err != nil {
				return err
			}
			if count != 0 {
				return e.ForbiddenError("Signups are closed.", nil)
			}
			e.App = tx
			// The dashboard never receives an admin token. The matching admin account
			// is created in the same transaction, so invalid setup cannot strand it.
			email := e.Record.Email()
			if email == "" {
				email = e.Record.GetString("username") + "@local.invalid"
				e.Record.SetEmail(email)
			}
			admins, err := tx.FindCollectionByNameOrId(core.CollectionNameSuperusers)
			if err != nil {
				return err
			}
			admin := core.NewRecord(admins)
			admin.SetEmail(email)
			admin.SetPassword(password)
			if err := tx.Save(admin); err != nil {
				return e.BadRequestError("Could not create administrator.", err)
			}
			return e.Next()
		})
	})
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// This middleware must precede RequireAuth to preserve setup_required.
		gate := func(r *core.RequestEvent) error {
			r.Response.Header().Set("Cache-Control", "no-store")
			if r.Auth == nil {
				count, err := r.App.CountRecords("users")
				if err != nil {
					return err
				}
				if count == 0 {
					return r.JSON(http.StatusUnauthorized, map[string]any{"detail": "setup_required", "username": legacyUsername(r.App)})
				}
			}
			return r.Next()
		}
		e.Router.GET("/japi/settings", tunables.Get).BindFunc(gate).Bind(apis.RequireAuth("users"))
		e.Router.POST("/japi/settings", tunables.Post).BindFunc(gate).Bind(apis.RequireAuth("users"))
		if err := registerLive(e, config, gate); err != nil {
			return err
		}
		// Unknown feature routes return JSON and can never fall through to SPA HTML.
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			e.Router.Route(method, "/japi/{path...}", func(r *core.RequestEvent) error {
				return r.JSON(http.StatusNotFound, map[string]string{"detail": "not_found"})
			}).BindFunc(gate).Bind(apis.RequireAuth("users"))
		}
		e.Router.GET("/{path...}", apis.Static(os.DirFS(*public), true))
		return e.Next()
	})
}

// Only expose the old identity, never its password hash. Legacy files are read
// without modification; the full data migration is a later milestone.
func legacyUsername(app core.App) string {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(app.DataDir()), "auth.json"))
	if err != nil {
		return ""
	}
	var old struct {
		Username string `json:"username"`
	}
	if json.Unmarshal(raw, &old) != nil {
		return ""
	}
	return old.Username
}
