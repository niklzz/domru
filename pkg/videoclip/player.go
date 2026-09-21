package videoclip

import (
	"errors"
	"io"
	"net/http"

	codec "github.com/yapingcat/gomedia/go-codec"
	mp4 "github.com/yapingcat/gomedia/go-mp4"
)

// Play remuxes a live FLV stream into fragmented MP4 as it arrives, so a
// browser plays it with a plain <video> tag: init segment plus one fragment
// per GOP (the streamer keys every 2 s), starting at the first keyframe with
// timestamps rebased to zero, H.264 and MP3 audio as they are. It returns
// when r ends or w stops accepting data; the error never contains URLs.
func Play(w io.Writer, r io.Reader) error {
	muxer, err := mp4.CreateMp4Muxer(&streamWriter{w: w}, mp4.WithMp4Flag(mp4.MP4_FLAG_FRAGMENT))
	if err != nil {
		return errors.New("cannot create MP4 muxer")
	}
	var video, audio uint32
	var hasVideo, hasAudio bool
	var base uint32
	rel := func(ts uint32) uint64 {
		if ts < base {
			return 0
		}
		return uint64(ts - base)
	}
	err = parseFLV(r, func(f frame) error {
		switch f.cid {
		case codec.CODECID_VIDEO_H264:
			if !hasVideo {
				if !f.key() {
					return nil
				}
				video = muxer.AddVideoTrack(mp4.MP4_CODEC_H264)
				hasVideo, base = true, f.dts
			}
			err = muxer.Write(video, f.data, rel(f.pts), rel(f.dts))
		case codec.CODECID_AUDIO_MP3:
			if !hasVideo {
				return nil
			}
			if !hasAudio {
				// ponytail: the track must exist before the first fragment writes moov
				// (2 s in); audio frames arrive every 26 ms, so it always does.
				audio = muxer.AddAudioTrack(mp4.MP4_CODEC_MP3)
				hasAudio = true
			}
			err = muxer.Write(audio, f.data, rel(f.pts), rel(f.dts))
		default:
			return nil
		}
		if err != nil {
			return errors.New("cannot write MP4")
		}
		return nil
	})
	if err == nil {
		err = errors.New("live stream ended")
	}
	return err
}

// streamWriter feeds the fragment muxer straight into the response: it
// counts bytes for the muxer's Seek(0, SeekCurrent) and flushes every write
// so the browser gets each fragment as soon as it is complete.
type streamWriter struct {
	w io.Writer
	n int64
}

func (s *streamWriter) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	s.n += int64(n)
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

func (s *streamWriter) Seek(offset int64, whence int) (int64, error) {
	if offset != 0 || whence != io.SeekCurrent {
		return 0, errors.New("stream is not seekable")
	}
	return s.n, nil
}
