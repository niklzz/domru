package videoclip

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Settings is the clip configuration chosen on the home page; it survives
// restarts through Switch.Save.
type Settings struct {
	Mode  string `json:"mode"`  // off, archive or live
	Light bool   `json:"light"` // live buffer on the 960×528 stream instead of 1080p
}

// State is what the home page shows: the settings plus what is possible.
type State struct {
	Settings
	Archive bool   `json:"archive"`          // the tariff has cloud recording (last probe)
	Buffer  string `json:"buffer,omitempty"` // live connection status while Mode is live
}

var ErrNoArchive = errors.New("archive unavailable: the tariff has no cloud recording")
var errBadMode = errors.New("mode must be off, archive or live")

// Switch routes clips to the archive or the live Buffer and runs the buffer
// only while it is the selected source. An archive that turns out to have no
// recording (errLive) flips the mode to live for good; buying a subscription
// later needs one explicit switch back, which re-probes the archive.
type Switch struct {
	Archive *Source
	Buffer  *Buffer
	Ctx     context.Context      // lifetime of the buffer
	Save    func(Settings) error // optional persistence, called on every accepted change

	mu      sync.Mutex
	s       Settings
	archive bool
	stop    context.CancelFunc // running buffer, nil when stopped
	done    chan struct{}
}

// Start probes the archive once and applies s; archive falls back to live
// (without saving) when the probe finds no recording. Blocks for a few
// seconds, run it in a goroutine.
func (w *Switch) Start(ctx context.Context, s Settings) {
	if s.Mode != "archive" && s.Mode != "live" {
		s.Mode = "off"
	}
	w.mu.Lock()
	w.s = s
	w.mu.Unlock()
	ok := w.probe(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.archive = ok
	if s.Mode == "archive" && !ok {
		s.Mode = "live"
	}
	w.apply(s)
}

// Set applies a change from the UI; archive is accepted only when a probe
// finds recordings.
func (w *Switch) Set(ctx context.Context, s Settings) error {
	if s.Mode != "off" && s.Mode != "archive" && s.Mode != "live" {
		return errBadMode
	}
	if s.Mode == "archive" {
		ok := w.probe(ctx)
		w.mu.Lock()
		w.archive = ok
		w.mu.Unlock()
		if !ok {
			return ErrNoArchive
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.apply(s)
	return w.save()
}

func (w *Switch) State() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := State{Settings: w.s, Archive: w.archive}
	if w.stop != nil {
		st.Buffer = w.Buffer.Status()
	}
	return st
}

// Clip implements the telegram.Bot Video hook; nil, nil means video is off.
func (w *Switch) Clip(ctx context.Context, start time.Time, d time.Duration) ([]byte, error) {
	w.mu.Lock()
	s := w.s
	w.mu.Unlock()
	switch s.Mode {
	case "archive":
		clip, err := w.Archive.Clip(ctx, start, d)
		if err != errLive {
			return clip, err
		}
		// The subscription is gone: switch to live inside this call so it
		// still gets the seconds after the ring.
		w.mu.Lock()
		w.archive = false
		if w.s.Mode == "archive" {
			s.Mode = "live"
			w.apply(s)
			_ = w.save()
		}
		w.mu.Unlock()
		return w.Buffer.Clip(ctx, start, d)
	case "live":
		return w.Buffer.Clip(ctx, start, d)
	}
	return nil, nil
}

// probe asks the archive for one second from half a minute ago; only a
// real recording answers with archive timestamps (live video is errLive).
func (w *Switch) probe(ctx context.Context) bool {
	_, err := w.Archive.Clip(ctx, time.Now().Add(-30*time.Second), time.Second)
	return err == nil
}

// apply starts or stops the buffer to match s; a quality change restarts
// it, because frames of two resolutions cannot share one MP4. Caller holds mu.
func (w *Switch) apply(s Settings) {
	live := s.Mode == "live"
	if w.stop != nil && (!live || s.Light != w.s.Light) {
		w.stop()
		<-w.done // Run has returned and reset the buffer: no stale frames
		w.stop = nil
	}
	if live && w.stop == nil {
		ctx := w.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		w.stop, w.done = cancel, done
		w.Buffer.Light.Store(s.Light)
		go func() {
			w.Buffer.Run(ctx)
			w.Buffer.reset()
			close(done)
		}()
	}
	w.s = s
}

func (w *Switch) save() error {
	if w.Save == nil {
		return nil
	}
	return w.Save(w.s)
}
