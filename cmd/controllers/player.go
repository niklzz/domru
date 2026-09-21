package controllers

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/moleus/domru/pkg/videoclip"
)

// liveClient fetches the signed streamer URL; no Timeout, the body is read
// for as long as the viewer watches.
var liveClient = &http.Client{}

// PlayerPageController serves the <video> page for a camera; the page picks
// native HLS (Safari) or our fragmented MP4 (everyone else).
// GET /player/{cameraId}[?light=1] — light picks the 960×528 stream.
func (h *Handler) PlayerPageController(w http.ResponseWriter, r *http.Request) {
	camera, err := strconv.Atoi(r.PathValue("cameraId"))
	if err != nil || camera <= 0 {
		http.Error(w, "cameraId must be a number", http.StatusBadRequest)
		return
	}
	data := struct {
		Camera int
		Light  bool
		AAC    bool // ffmpeg present: audio is AAC, which iPhones play
	}{camera, r.URL.Query().Get("light") == "1", videoclip.FFmpeg != ""}
	if err := h.renderTemplate(w, "player", data); err != nil {
		h.Logger.Error("player page", "error", err)
	}
}

// PlayerController streams a camera as fragmented MP4 for the player page
// (MSE, or a plain <video> src), unlike the FLV behind /stream/{cameraId}.
// With ffmpeg the audio is AAC, otherwise the original MP3.
// GET /player/{cameraId}/mp4[?light=1]
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
	streamURL, err := h.domruAPI.GetStreamURL(cameraID, url.Values{"LightStream": {light}, "Format": {"H264"}})
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
