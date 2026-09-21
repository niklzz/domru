package videoclip

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
)

// FFmpeg is the ffmpeg binary found on PATH at startup, or "" when absent:
// then the player falls back to Play (MP3 audio, which iOS does not play).
var FFmpeg, _ = exec.LookPath("ffmpeg")

// Transcode remuxes a live FLV stream into fragmented MP4 like Play, but
// through ffmpeg: video is copied, MP3 audio is re-encoded to AAC so Safari
// and iPhones get sound. Fragments every 0.5 s keep the latency low. It
// returns when r ends, ctx is cancelled or w stops accepting data.
func Transcode(ctx context.Context, w io.Writer, r io.Reader) error {
	if FFmpeg == "" {
		return errors.New("ffmpeg not installed")
	}
	cmd := exec.CommandContext(ctx, FFmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		// Live input: probe 0.5 s instead of the default 5 s. Audio follows the
		// first (200 KB) keyframe, so probesize must cover more than that.
		"-fflags", "+nobuffer", "-analyzeduration", "500000", "-probesize", "2000000",
		"-f", "flv", "-i", "pipe:0",
		"-c:v", "copy",
		"-c:a", "aac", "-b:a", "96k",
		"-f", "mp4", "-movflags", "+empty_moov+default_base_moof", "-frag_duration", "500000",
		"pipe:1")
	cmd.Stdin = r
	cmd.Stdout = &streamWriter{w: w}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			// ffmpeg errors name codecs and streams, never the signed URL: it reads stdin.
			return errors.New("ffmpeg: " + lastLine(msg))
		}
		return errors.New("ffmpeg failed")
	}
	return errors.New("live stream ended")
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// limitedWriter keeps the first n bytes and drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		if len(p) > l.n {
			p = p[:l.n]
		}
		l.n -= len(p)
		_, _ = l.w.Write(p)
	}
	return len(p), nil
}
