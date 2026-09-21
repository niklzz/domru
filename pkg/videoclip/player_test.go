package videoclip

import (
	"bytes"
	"context"
	"encoding/binary"
	"os/exec"
	"testing"

	flv "github.com/yapingcat/gomedia/go-flv"
)

// fakeLiveFLV is the live stream as the streamer sends it: timestamps in the
// hundreds of millions, `lead` non-key frames before the first keyframe, then
// `gops` GOPs of 25 frames with an MP3 frame per video frame.
func fakeLiveFLV(t *testing.T, lead, gops int) []byte {
	var out bytes.Buffer
	w := flv.CreateFlvWriter(&out)
	if err := w.WriteFlvHeader(); err != nil {
		t.Fatal(err)
	}
	start := []byte{0, 0, 0, 1}
	mp3 := make([]byte, 417) // MPEG-1 layer III, 128 kbit/s, 44.1 kHz, mono
	copy(mp3, []byte{0xff, 0xfb, 0x90, 0xc0})
	const base = uint32(433696720)
	n := 0
	write := func(key bool) {
		var frame []byte
		if key {
			frame = append(frame, start...)
			frame = append(frame, sps...)
			frame = append(frame, start...)
			frame = append(frame, pps...)
			frame = append(frame, start...)
			frame = append(frame, 0x65, 0x88, 0x84, byte(n), 1, 2, 3)
		} else {
			frame = append(frame, start...)
			frame = append(frame, 0x41, 0x9a, byte(n), 4, 5, 6)
		}
		ts := base + uint32(n)*40
		if err := w.WriteH264(frame, ts, ts); err != nil {
			t.Fatal(err)
		}
		// FlvWriter.WriteMp3 emits a stray empty tag (gomedia bug), so the
		// audio tag is framed by hand: 11-byte tag header, payload, previous size.
		payload := flv.WriteAudioTag(mp3, flv.FLV_MP3, 44100, 1, false)
		tag := []byte{8, byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload)),
			byte(ts >> 16), byte(ts >> 8), byte(ts), byte(ts >> 24), 0, 0, 0}
		tag = append(tag, payload...)
		out.Write(tag)
		out.Write(binary.BigEndian.AppendUint32(nil, uint32(len(tag))))
		n++
	}
	for i := 0; i < lead; i++ {
		write(false)
	}
	for g := 0; g < gops; g++ {
		write(true)
		for i := 1; i < 25; i++ {
			write(false)
		}
	}
	return out.Bytes()
}

func TestPlayStreamsFragmentedMP4(t *testing.T) {
	var out bytes.Buffer
	err := Play(&out, bytes.NewReader(fakeLiveFLV(t, 7, 3)))
	if err == nil || err.Error() != "live stream ended" {
		t.Fatalf("err = %v", err)
	}
	b := out.Bytes()
	if !bytes.Equal(b[4:8], []byte("ftyp")) || !bytes.Contains(b, []byte("iso5")) {
		t.Fatalf("not fragmented MP4: %x", b[:16])
	}
	for _, box := range []string{"moov", "mvex", "avc1", "mp4a", "moof"} {
		if !bytes.Contains(b, []byte(box)) {
			t.Fatalf("no %s box", box)
		}
	}
	// One fragment per GOP: the third GOP stays in the muxer (no trailer for live).
	if n := bytes.Count(b, []byte("moof")); n != 2 {
		t.Fatalf("fragments = %d, want 2", n)
	}
	// stsz in a fragmented file is empty: the lead frames must not be sampled anywhere.
	if !bytes.Contains(b, []byte("tfdt")) {
		t.Fatal("no tfdt: fragments not timestamped")
	}
}

func TestPlaySkipsUntilKeyframe(t *testing.T) {
	var out bytes.Buffer
	_ = Play(&out, bytes.NewReader(fakeLiveFLV(t, 30, 0)))
	if out.Len() != 0 {
		t.Fatalf("wrote %d bytes without a keyframe", out.Len())
	}
	if err := Play(&out, bytes.NewReader([]byte("HTTP/1.0 404 Not Found"))); err == nil {
		t.Fatal("garbage accepted")
	}
}

// realFLV renders 3 s of H.264 + MP3 with ffmpeg itself: Transcode parses
// the video while probing, so the junk slices of fakeLiveFLV will not do.
func realFLV(t *testing.T) []byte {
	out, err := exec.Command(FFmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-t", "3", "-c:v", "libx264", "-preset", "ultrafast", "-g", "25",
		"-c:a", "libmp3lame", "-ac", "1", "-f", "flv", "pipe:1").Output()
	if err != nil {
		t.Skipf("ffmpeg cannot render the fixture: %v", err)
	}
	return out
}

func TestTranscodeReencodesAudioToAAC(t *testing.T) {
	if FFmpeg == "" {
		t.Skip("ffmpeg not installed")
	}
	in := realFLV(t)
	var out bytes.Buffer
	err := Transcode(context.Background(), &out, bytes.NewReader(in))
	if err == nil || err.Error() != "live stream ended" {
		t.Fatalf("err = %v", err)
	}
	b := out.Bytes()
	if !bytes.Contains(b, []byte("mp4a")) || !bytes.Contains(b, []byte("avc1")) || bytes.Count(b, []byte("moof")) < 3 {
		t.Fatalf("no AAC fragmented MP4: mp4a=%d avc1=%d moof=%d", bytes.Count(b, []byte("mp4a")), bytes.Count(b, []byte("avc1")), bytes.Count(b, []byte("moof")))
	}
	// esds objectTypeIndication 0x40 = MPEG-4 audio (AAC); the MP3 source would be 0x6b.
	i := bytes.Index(b, []byte("esds"))
	if i < 0 || bytes.Contains(b[i:i+64], []byte{0x6b, 0x15}) || !bytes.Contains(b[i:i+64], []byte{0x40, 0x15}) {
		t.Fatalf("esds does not declare AAC")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Transcode(ctx, &out, bytes.NewReader(in)); err != context.Canceled {
		t.Fatalf("cancelled: %v", err)
	}
}
