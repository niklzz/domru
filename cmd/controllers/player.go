package controllers

import (
	"errors"
	"io"
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

// maxDownload caps an archive download: it arrives in real time, so this is
// also how long the browser waits for it.
const maxDownload = 30 * time.Minute

// archiveTS parses ?ts=<unix seconds>; 0 means live.
func archiveTS(r *http.Request) (int64, error) {
	return archiveTime(r, "ts")
}

// archiveTime parses a unix-seconds query parameter inside the archive
// window; a missing one is 0.
func archiveTime(r *http.Request, name string) (int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	ts, err := strconv.ParseInt(v, 10, 64)
	now := time.Now()
	if err != nil || ts > now.Unix() || ts < now.Add(-archiveDepth).Unix() {
		return 0, errors.New(name + " must be unix seconds within the last 3 days")
	}
	return ts, nil
}

// downloadRange parses ?from=&to= of an archive download.
func downloadRange(r *http.Request) (from int64, d time.Duration, err error) {
	from, err = archiveTime(r, "from")
	if err != nil {
		return 0, 0, err
	}
	to, err := archiveTime(r, "to")
	if err != nil {
		return 0, 0, err
	}
	d = time.Duration(to-from) * time.Second
	if from == 0 || d <= 0 || d > maxDownload {
		return 0, 0, errors.New("from and to must give 1 s to 30 min")
	}
	return from, d, nil
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
	body := h.openStream(w, r, cameraID, light, ts)
	if body == nil {
		return
	}
	defer body.Close()
	if videoclip.FFmpeg != "" {
		err = videoclip.Transcode(r.Context(), w, body, 0)
	} else {
		err = videoclip.Play(w, body)
	}
	if err != nil && r.Context().Err() == nil {
		h.Logger.Debug("player: stream stopped", "camera", cameraID, "error", err)
	}
}

// DownloadController saves an archive interval as an MP4 file. The archive
// arrives in real time, so the browser download takes about as long as the
// interval; the file is fragmented MP4 (AAC), written as it comes.
// GET /player/{cameraId}/download?from=<unix>&to=<unix>
func (h *Handler) DownloadController(w http.ResponseWriter, r *http.Request) {
	cameraID := r.PathValue("cameraId")
	if _, err := strconv.Atoi(cameraID); err != nil {
		http.Error(w, "cameraId must be a number", http.StatusBadRequest)
		return
	}
	from, d, err := downloadRange(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if videoclip.FFmpeg == "" { // ponytail: Play cannot stop after d, the Dockerfile image has ffmpeg
		http.Error(w, "download needs ffmpeg in the image", http.StatusNotImplemented)
		return
	}
	body := h.openStream(w, r, cameraID, "0", from)
	if body == nil {
		return
	}
	defer body.Close()
	name := "domru-" + cameraID + "-" + time.Unix(from, 0).Format("20060102-150405") + ".mp4"
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if err := videoclip.Transcode(r.Context(), w, body, d); err != nil && r.Context().Err() == nil {
		h.Logger.Warn("download: stopped", "camera", cameraID, "error", err)
	}
}

// openStream requests the camera stream (the archive from ts unless ts is 0)
// and prepares w for endless MP4; on failure it answers w and returns nil.
func (h *Handler) openStream(w http.ResponseWriter, r *http.Request, cameraID, light string, ts int64) io.ReadCloser {
	query := url.Values{"LightStream": {light}, "Format": {"H264"}}
	if ts != 0 {
		query.Set("TS", strconv.FormatInt(ts, 10)) // the archive also plays in real time
	}
	streamURL, err := h.domruAPI.GetStreamURL(cameraID, query)
	if err != nil {
		h.Logger.Warn("player: cannot get stream url", "error", err)
		http.Error(w, "failed to get stream url", http.StatusBadGateway)
		return nil
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, streamURL, nil)
	if err != nil {
		http.Error(w, "failed to build stream request", http.StatusBadGateway)
		return nil
	}
	res, err := liveClient.Do(req)
	if err != nil {
		http.Error(w, "stream request failed", http.StatusBadGateway)
		return nil
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		http.Error(w, "streamer HTTP "+res.Status, http.StatusBadGateway)
		return nil
	}
	// The server's WriteTimeout would cut the stream after 30 s.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Accept-Ranges", "none")
	return res.Body
}
