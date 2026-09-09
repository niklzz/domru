package videoclip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStreamer answers archive requests with live timestamps (no recording)
// while live is set, and counts live connections by LightStream value.
func fakeStreamer(t *testing.T, live *atomic.Bool, lightHits *atomic.Int32) (*Source, *httptest.Server) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		if r.URL.Query().Has("TS") {
			base := uint32(0)
			if live.Load() {
				base = 433696720
			}
			_, _ = w.Write(fakeFLV(t, 2*25, base))
			return
		}
		if r.URL.Query().Get("LightStream") == "1" {
			lightHits.Add(1)
		}
		_, _ = w.Write(fakeFLV(t, 25, 433696720))
	}))
	src := &Source{
		Camera: func(context.Context) (string, error) { return "cam", nil },
		URL:    func(_ string, q url.Values) (string, error) { return srv.URL + "/stream?" + q.Encode(), nil },
		Client: srv.Client(),
	}
	return src, srv
}

func TestSwitchModes(t *testing.T) {
	var live atomic.Bool
	var lightHits atomic.Int32
	live.Store(true)
	src, srv := fakeStreamer(t, &live, &lightHits)
	defer srv.Close()
	var saved []Settings
	w := &Switch{Archive: src, Buffer: &Buffer{Src: src, Client: srv.Client(), Keep: 20 * time.Second}, Save: func(s Settings) error { saved = append(saved, s); return nil }}
	ctx := context.Background()

	w.Start(ctx, Settings{Mode: "archive"}) // no recording: falls back to live without saving
	if st := w.State(); st.Mode != "live" || st.Archive || len(saved) != 0 {
		t.Fatalf("after start: %+v, saved %v", st, saved)
	}
	if err := w.Set(ctx, Settings{Mode: "archive"}); err != ErrNoArchive {
		t.Fatalf("archive without subscription: %v", err)
	}
	if err := w.Set(ctx, Settings{Mode: "bogus"}); err != errBadMode {
		t.Fatalf("bogus mode: %v", err)
	}

	if err := w.Set(ctx, Settings{Mode: "live", Light: true}); err != nil { // quality change restarts the buffer
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for lightHits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if lightHits.Load() == 0 {
		t.Fatal("buffer did not reconnect with LightStream=1")
	}

	if err := w.Set(ctx, Settings{Mode: "off"}); err != nil {
		t.Fatal(err)
	}
	if w.stop != nil || len(w.Buffer.frames) != 0 {
		t.Fatalf("buffer still running or holds %d frames after off", len(w.Buffer.frames))
	}
	if clip, err := w.Clip(ctx, time.Now(), 0); clip != nil || err != nil {
		t.Fatalf("off should yield nil, nil: %v %v", clip, err)
	}

	live.Store(false) // subscription bought: archive becomes selectable
	if err := w.Set(ctx, Settings{Mode: "archive"}); err != nil {
		t.Fatal(err)
	}
	if st := w.State(); !st.Archive || st.Mode != "archive" || w.stop != nil {
		t.Fatalf("archive mode: %+v", st)
	}
	if got := saved[len(saved)-1]; got.Mode != "archive" {
		t.Fatalf("last saved %+v", got)
	}

	live.Store(true) // subscription gone: a call flips to live for good
	if _, err := w.Clip(ctx, time.Now().Add(-time.Second), 0); err == nil {
		t.Fatal("cold buffer should not yield a clip yet")
	}
	if st := w.State(); st.Mode != "live" || st.Archive || saved[len(saved)-1].Mode != "live" {
		t.Fatalf("after errLive: %+v, saved %+v", st, saved[len(saved)-1])
	}
	_ = w.Set(ctx, Settings{Mode: "off"})
}
