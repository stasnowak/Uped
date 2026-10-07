package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/stasnowak/Uped/internal/store"
)

const (
	// changeCooldown limits folder-change broadcasts: the first goes out at
	// once, later ones within the window are merged into one batch at its
	// end. Every change makes every open page re-list the folder.
	changeCooldown = 500 * time.Millisecond
	pingInterval   = 20 * time.Second
	subBuffer      = 64
)

// hub fans store events out to live event streams.
type hub struct {
	log      *slog.Logger
	cooldown time.Duration
	done     chan struct{}

	mu     sync.Mutex
	subs   map[chan []byte]struct{}
	closed bool
}

func newHub(events <-chan store.Event, cooldown time.Duration, log *slog.Logger) *hub {
	h := &hub{log: log, cooldown: cooldown, done: make(chan struct{}), subs: map[chan []byte]struct{}{}}
	go h.run(events)
	return h
}

func (h *hub) run(events <-chan store.Event) {
	pending := map[string]bool{}
	var timer *time.Timer
	var cooling <-chan time.Time
	for {
		select {
		case <-h.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case e := <-events:
			switch e.Kind {
			case store.EventUpload:
				h.broadcast(frame("upload", e.Upload))
			case store.EventChange:
				if cooling != nil {
					pending[e.Dir] = true
					continue
				}
				h.broadcast(frame("change", map[string]string{"dir": e.Dir}))
				timer = time.NewTimer(h.cooldown)
				cooling = timer.C
			}
		case <-cooling:
			if len(pending) == 0 {
				cooling = nil
				continue
			}
			dirs := make([]string, 0, len(pending))
			for d := range pending {
				dirs = append(dirs, d)
			}
			sort.Strings(dirs)
			for _, d := range dirs {
				h.broadcast(frame("change", map[string]string{"dir": d}))
			}
			clear(pending)
			timer.Reset(h.cooldown)
		}
	}
}

func frame(event string, v any) []byte {
	data, _ := json.Marshal(v)
	return []byte("event: " + event + "\ndata: " + string(data) + "\n\n")
}

// subscribe returns a channel of encoded frames, or nil after close.
func (h *hub) subscribe() chan []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	ch := make(chan []byte, subBuffer)
	h.subs[ch] = struct{}{}
	return ch
}

func (h *hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
}

// broadcast never blocks: a client whose buffer is full is dropped and
// reconnects on its own, re-listing as it does.
func (h *hub) broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default:
			delete(h.subs, ch)
			close(ch)
			h.log.Warn("live update client too slow, disconnected")
		}
	}
}

func (h *hub) subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.done)
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

// GET /api/events: server-sent events. "change" carries {"dir": folder};
// "upload" carries an upload's state. A comment line is sent every 20 s so
// idle connections stay open through proxies and dead ones are noticed.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ch := s.hub.subscribe()
	if ch == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "server is shutting down"})
		return
	}
	defer s.hub.unsubscribe(ch)

	rc := http.NewResponseController(w)
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(s.opts.IdleTimeout))
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send([]byte(": connected\nretry: 3000\n\n")) {
		return
	}
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-ch:
			if !ok || !send(b) {
				return
			}
		case <-ping.C:
			if !send([]byte(": ping\n\n")) {
				return
			}
		}
	}
}
