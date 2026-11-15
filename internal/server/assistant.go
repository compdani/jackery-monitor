package server

import (
	"context"
	"database/sql"
	"errors"
	"github.com/compdani/jackery-monitor/internal/assistant"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/core"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
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
				jsonOut(w, 200, map[string]any{"has_key": str(m, "api_key") != "" || os.Getenv(strings.ToUpper(provider)+"_API_KEY") != ""})
			}
		case "POST":
			key := str(b, "api_key")
			if key == "" {
				key = str(b, "key")
			}
			if strings.TrimSpace(key) == "" {
				fail(w, 400, "api_key required")
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
	if p != "/api/assistant/chat" {
		return false
	}
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return true
	}
	id := str(b, "conversation_id")
	if id == "" {
		fail(w, 400, "conversation_id required")
		return true
	}
	if _, e := s.Store.Get("conversations", id); e != nil {
		storageError(w, e)
		return true
	}
	text := str(b, "message")
	if text == "" {
		text = str(b, "text")
	}
	if strings.TrimSpace(text) == "" {
		fail(w, 400, "message required")
		return true
	}
	if ids, ok := b["attachment_ids"].([]any); ok && len(ids) > 0 {
		fail(w, 501, "assistant attachments are not ported")
		return true
	}
	s.assistant.mu.Lock()
	defer s.assistant.mu.Unlock()
	provider := "anthropic"
	if v, e := s.Store.Get("settings", "ai_provider"); e == nil && str(v, "provider") != "" {
		provider = str(v, "provider")
	}
	prefs, _ := s.Store.Get("settings", "anthropic_prefs")
	secret, e := s.Store.Secret(s.Key, provider)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		storageError(w, e)
		return true
	}
	key := str(secret, "api_key")
	if key == "" {
		key = os.Getenv(strings.ToUpper(provider) + "_API_KEY")
	}
	if key == "" {
		fail(w, 400, "AI provider key required")
		return true
	}
	var client textProvider
	model := str(b, "model")
	if provider == "openai" {
		if model == "" {
			model = str(prefs, "openai_model")
		}
		if model == "" {
			fail(w, 400, "AI model required")
			return true
		}
		client = &assistant.Client{HTTP: s.AIHTTP, APIKey: key, Model: model, BaseURL: assistant.ResolveBaseURL(compatPreset(prefs), str(prefs, "openai_base_url"))}
	} else {
		if model == "" {
			model = str(prefs, "assistant_model")
		}
		if model == "" {
			fail(w, 400, "AI model required")
			return true
		}
		client = &assistant.Anthropic{APIKey: key, Model: model}
	}
	rows, e := s.Store.List("messages", id, 0, 1000)
	if e != nil {
		storageError(w, e)
		return true
	}
	messages := []assistant.Message{{Role: "system", Content: "You are the Solar Pow Monitor AI Assistant. Help the user understand energy usage and forecast SOC. Be concise and concrete. Device changes are unavailable in this chat."}}
	for _, row := range rows {
		role := str(row, "role")
		if role == "user" || role == "assistant" {
			messages = append(messages, assistant.Message{Role: role, Content: str(row, "content")})
		}
	}
	messages = append(messages, assistant.Message{Role: "user", Content: text})
	reply, e := client.Complete(r.Context(), messages)
	if e != nil {
		fail(w, 502, e.Error())
		return true
	}
	uid, aid := store.ID(), store.ID()
	stamp := float64(time.Now().UnixNano()) / 1e9
	// A transaction prevents orphan user messages when persisting a completed turn.
	e = s.Store.App.RunInTransaction(func(app core.App) error {
		st := &store.Store{App: app}
		if e := st.Put("messages", uid, id, map[string]any{"id": uid, "conversation_id": id, "role": "user", "content": text, "ts": stamp}); e != nil {
			return e
		}
		return st.Put("messages", aid, id, map[string]any{"id": aid, "conversation_id": id, "role": "assistant", "content": reply, "ts": stamp + .000001})
	})
	if e != nil {
		storageError(w, e)
		return true
	}
	jsonOut(w, 200, map[string]any{"conversation_id": id, "text": reply, "message": map[string]any{"id": aid, "conversation_id": id, "role": "assistant", "content": reply, "ts": stamp + .000001}, "pending_tools": []any{}, "tool_runs": []any{}})
	return true
}
