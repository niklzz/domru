package controllers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/moleus/domru/pkg/videoclip"
)

// liveClient fetches the signed streamer URL; no Timeout, the body is read
// for as long as the viewer watches.
var liveClient = &http.Client{}

// archiveDepth is how far back the cloud archive goes (camera Quota 259200 s);
// older TS make the streamer hang instead of answering.
// ponytail: a constant, read Quota from /forpost/cameras if cameras ever differ.
const archiveDepth = 72 * time.Hour

// archiveTS parses ?ts=<unix seconds>; 0 means live.
func archiveTS(r *http.Request) (int64, error) {
	v := r.URL.Query().Get("ts")
	if v == "" {
		return 0, nil
	}
	ts, err := strconv.ParseInt(v, 10, 64)
	now := time.Now()
	if err != nil || ts > now.Unix() || ts < now.Add(-archiveDepth).Unix() {
		return 0, errors.New("ts must be unix seconds within the last 3 days")
	}
	return ts, nil
}

// PlayerPageController serves the <video> page for a camera; the page picks
// native HLS (Safari) or our fragmented MP4 (everyone else).
// GET /player/{cameraId}[?light=1][&ts=<unix>] — light picks the 960×528
// stream, ts plays the archive from that moment with time-jump controls.
func (h *Handler) PlayerPageController(w http.ResponseWriter, r *http.Request) {
	camera, err := strconv.Atoi(r.PathValue("cameraId"))
	if err != nil || camera <= 0 {
		http.Error(w, "cameraId must be a number", http.StatusBadRequest)
		return
	}
	ts, err := archiveTS(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data := struct {
		Camera int
		Light  bool
		AAC    bool  // ffmpeg present: audio is AAC, which iPhones play
		TS     int64 // archive start, 0 = live
		Depth  int64 // archive depth in seconds
	}{camera, r.URL.Query().Get("light") == "1", videoclip.FFmpeg != "", ts, int64(archiveDepth / time.Second)}
	if err := h.renderTemplate(w, "player", data); err != nil {
		h.Logger.Error("player page", "error", err)
	}
}

// PlayerController streams a camera as fragmented MP4 for the player page
// (MSE, or a plain <video> src), unlike the FLV behind /stream/{cameraId}.
// With ffmpeg the audio is AAC, otherwise the original MP3.
// GET /player/{cameraId}/mp4[?light=1][&ts=<unix>]
func (h *Handler) PlayerController(w http.ResponseWriter, r *http.Request) {
	h.Logger.Debug("PlayerController: %s %s", r.Method, r.URL.Path)
	cameraID := r.PathValue("cameraId")
	if cameraID == "" {
		http.Error(w, "cameraId is required", http.StatusBadRequest)
		return
	}
	light := "0"
	if r.URL.Query().Get("light") == "1" {
		light = "1"
	}
	ts, err := archiveTS(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query := url.Values{"LightStream": {light}, "Format": {"H264"}}
	if ts != 0 {
		query.Set("TS", strconv.FormatInt(ts, 10)) // the archive also plays in real time
	}
	streamURL, err := h.domruAPI.GetStreamURL(cameraID, query)
	if err != nil {
		h.Logger.Warn("player: cannot get stream url", "error", err)
		http.Error(w, "failed to get stream url", http.StatusBadGateway)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, streamURL, nil)
	if err != nil {
		http.Error(w, "failed to build stream request", http.StatusBadGateway)
		return
	}
	res, err := liveClient.Do(req)
	if err != nil {
		http.Error(w, "stream request failed", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		http.Error(w, "streamer HTTP "+res.Status, http.StatusBadGateway)
		return
	}
	// The server's WriteTimeout would cut the stream after 30 s.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Accept-Ranges", "none")
	if videoclip.FFmpeg != "" {
		err = videoclip.Transcode(r.Context(), w, res.Body)
	} else {
		err = videoclip.Play(w, res.Body)
	}
	if err != nil && r.Context().Err() == nil {
		h.Logger.Debug("player: stream stopped", "camera", cameraID, "error", err)
	}
}
