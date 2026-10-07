package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stasnowak/Uped/internal/store"
)

type sseEvent struct{ name, data string }

// openStream connects to /api/events and returns parsed events, comments
// included as name ":".
func (e *env) openStream(ctx context.Context) (<-chan sseEvent, *http.Response) {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make(chan sseEvent, 100)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, ":"):
				out <- sseEvent{name: ":", data: strings.TrimSpace(line[1:])}
			case strings.HasPrefix(line, "event: "):
				ev.name = line[len("event: "):]
			case strings.HasPrefix(line, "data: "):
				ev.data = line[len("data: "):]
			case line == "" && ev.name != "":
				out <- ev
				ev = sseEvent{}
			}
		}
	}()
	return out, resp
}

func TestEventStream(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, resp := e.openStream(ctx)
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("stream headers: %v", resp.Header)
	}
	select {
	case ev := <-events:
		if ev.name != ":" || ev.data != "connected" {
			t.Fatalf("first frame = %+v, want the connected comment", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no connected comment")
	}

	go e.upload("trip", "a.jpg", pattern(3000), "")

	sawDone, sawChange := false, false
	timeout := time.After(2 * time.Second)
	for !sawDone || !sawChange {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream closed early")
			}
			var m map[string]any
			_ = json.Unmarshal([]byte(ev.data), &m)
			switch {
			case ev.name == "upload" && m["state"] == "done" && m["path"] == "trip/a.jpg":
				sawDone = true
			case ev.name == "change" && m["dir"] == "trip":
				sawChange = true
			}
		case <-timeout:
			t.Fatalf("within 2s: upload done=%v, change=%v", sawDone, sawChange)
		}
	}
}

func TestHubCoalescesChanges(t *testing.T) {
	events := make(chan store.Event)
	h := newHub(events, 150*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer h.close()
	sub := h.subscribe()

	start := time.Now()
	for i := 0; i < 20; i++ {
		dir := []string{"a", "b"}[i%2]
		events <- store.Event{Kind: store.EventChange, Dir: dir}
	}
	events <- store.Event{Kind: store.EventUpload, Upload: store.UploadInfo{ID: "u1", State: store.StateActive}}

	var frames []string
	deadline := time.After(time.Second)
	for len(frames) < 4 {
		select {
		case f := <-sub:
			frames = append(frames, string(f))
		case <-deadline:
			t.Fatalf("frames after 1s: %q", frames)
		}
	}
	if !strings.Contains(frames[0], `"dir":"a"`) || time.Since(start) < 100*time.Millisecond {
		t.Errorf("first change not sent immediately, or batch came too early: %q", frames)
	}
	if !strings.HasPrefix(frames[1], "event: upload") {
		t.Errorf("upload event was held back behind changes: %q", frames)
	}
	if !strings.Contains(frames[2], `"dir":"a"`) || !strings.Contains(frames[3], `"dir":"b"`) {
		t.Errorf("trailing batch = %q, want one change each for a and b", frames[2:])
	}
	select {
	case f := <-sub:
		t.Errorf("extra frame %q: 20 changes should collapse into 3", f)
	case <-time.After(400 * time.Millisecond):
	}
}

func TestHubDropsSlowClient(t *testing.T) {
	events := make(chan store.Event)
	h := newHub(events, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer h.close()
	slow := h.subscribe()
	for i := 0; i < subBuffer+1; i++ {
		events <- store.Event{Kind: store.EventUpload}
	}
	deadline := time.Now().Add(time.Second)
	for h.subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.subscribers() != 0 {
		t.Fatal("slow subscriber not dropped")
	}
	n := 0
	for range slow {
		n++
	}
	if n != subBuffer {
		t.Errorf("slow subscriber received %d buffered frames before close, want %d", n, subBuffer)
	}
}

func TestStreamsEndOnClose(t *testing.T) {
	e := newEnv(t)
	events, _ := e.openStream(context.Background())
	<-events // connected
	e.srv.Close()
	select {
	case _, ok := <-events:
		for ok {
			_, ok = <-events
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream still open after Close")
	}
	resp, _ := e.do("GET", "/api/events", nil)
	expect(t, resp, http.StatusServiceUnavailable, "events after close")
}
