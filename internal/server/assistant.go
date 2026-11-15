package server

import (
	"context"
	"database/sql"
	"errors"
	"github.com/compdani/jackery-monitor/internal/assistant"
	"net/http"
	"os"
	"strings"
	"sync"
)

type textProvider interface {
	Complete(context.Context, []assistant.Message) (string, error)
}
type assistantRuntime struct{ mu sync.Mutex }

func (s *Server) aiRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p := r.URL.Path
	if s.modelRoutes(w, r) {
		return true
	}
	for _, provider := range []string{"anthropic", "openai"} {
		if p != "/api/"+provider+"/key" {
			continue
		}
		switch r.Method {
		case "GET":
			m, e := s.Store.Secret(s.Key, provider)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				storageError(w, e)
			} else {
				var source any
				if str(m, "api_key") != "" {
					source = "saved"
				} else if os.Getenv(strings.ToUpper(provider)+"_API_KEY") != "" {
					source = "env"
				}
				jsonOut(w, 200, map[string]any{"has_key": source != nil, "source": source})
			}
		case "POST":
			key := str(b, "api_key")
			if key == "" {
				key = str(b, "key")
			}
			key = strings.TrimSpace(key)
			if key == "" {
				fail(w, 400, "api_key required")
			} else if e := s.validateKey(r.Context(), provider, key); e != nil {
				fail(w, 400, "key validation failed: "+e.Error())
			} else if e := s.Store.SaveSecret(s.Key, provider, map[string]any{"api_key": key}); e != nil {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"ok": true, "has_key": true})
			}
		case "DELETE":
			e := s.Store.Delete("credentials", provider)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				storageError(w, e)
			} else {
				jsonOut(w, 200, map[string]any{"ok": true})
			}
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	return false
}
