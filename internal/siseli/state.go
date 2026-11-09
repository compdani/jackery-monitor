package siseli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// StateStore persists decoded readings only. Liveness timestamps are never restored.
type StateStore interface {
	Load() (Values, error)
	Save(Values) error
}
type FileStateStore struct{ Path string }

func (s FileStateStore) Load() (Values, error) {
	b, e := os.ReadFile(s.Path)
	if e != nil {
		return nil, e
	}
	v := Values{}
	e = json.Unmarshal(b, &v)
	return v, e
}
func (s FileStateStore) Save(v Values) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	dir := filepath.Dir(s.Path)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".siseli-state-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, s.Path)
}
func (l *LocalClient) Restore(store StateStore) error {
	v, e := store.Load()
	if e != nil {
		return e
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, x := range v {
		if SensorRegistered(k) {
			l.state[k] = x
		}
	}
	return nil
}
func (l *LocalClient) Save(store StateStore) error { return store.Save(l.Snapshot()) }

// Runner owns a single cancellable local transport. Stop waits for transport cleanup.
// The supplied transport must obey MQTTTransport's cancellation contract.
type Runner struct {
	mu     sync.Mutex
	client *LocalClient
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func NewRunner(c *LocalClient) *Runner { return &Runner{client: c} }
func (r *Runner) Start(ctx context.Context, transport MQTTTransport) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return false
	}
	ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	done := r.done
	r.err = nil
	go func() {
		err := r.client.Run(ctx, transport)
		r.mu.Lock()
		r.err = err
		r.cancel = nil
		close(done)
		r.mu.Unlock()
	}()
	return true
}
func (r *Runner) Stop(ctx context.Context) error {
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
func (r *Runner) Err() error { r.mu.Lock(); defer r.mu.Unlock(); return r.err }
