package hls

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseAttrs(t *testing.T) {
	// A quoted value containing a comma (CODECS) must not split the list.
	attrs := parseAttrs(`RESOLUTION=720x1280,CODECS="avc1.4d401f,mp4a.40.2",BANDWIDTH=149020,AUDIO="group_audio"`)
	want := map[string]string{
		"RESOLUTION": "720x1280",
		"CODECS":     "avc1.4d401f,mp4a.40.2",
		"BANDWIDTH":  "149020",
		"AUDIO":      "group_audio",
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("attr %s = %q, want %q", k, attrs[k], v)
		}
	}
}

func TestParseMaster(t *testing.T) {
	const master = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="group_audio",NAME="original",DEFAULT=YES,URI="audio.m3u8"
#EXT-X-STREAM-INF:RESOLUTION=480x852,CODECS="avc1.4d401f,mp4a.40.2",BANDWIDTH=143142,AUDIO="group_audio"
low.m3u8
#EXT-X-STREAM-INF:RESOLUTION=720x1280,CODECS="avc1.4d401f,mp4a.40.2",BANDWIDTH=149020,AUDIO="group_audio"
high.m3u8
`
	video, audio, err := parseMaster("https://cdn.example/manifest/video.m3u8", master)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://cdn.example/manifest/high.m3u8"; video != want {
		t.Errorf("video = %q, want %q (highest bandwidth)", video, want)
	}
	if want := "https://cdn.example/manifest/audio.m3u8"; audio != want {
		t.Errorf("audio = %q, want %q", audio, want)
	}
}

func TestParseMasterNoAudio(t *testing.T) {
	const master = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=149020
only.m3u8
`
	video, audio, err := parseMaster("https://cdn.example/manifest/video.m3u8", master)
	if err != nil {
		t.Fatal(err)
	}
	if audio != "" {
		t.Errorf("audio = %q, want empty (no audio group)", audio)
	}
	if want := "https://cdn.example/manifest/only.m3u8"; video != want {
		t.Errorf("video = %q, want %q", video, want)
	}
}

func TestParseMedia(t *testing.T) {
	// Cloudflare uses ../../-relative URIs resolved against the media playlist.
	const media = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-MAP:URI="../../vid/1280/init.mp4?p=abc"
#EXTINF:4.0,
../../vid/1280/seg_1.mp4?p=abc
#EXTINF:4.0,
../../vid/1280/seg_2.mp4?p=abc
#EXT-X-ENDLIST
`
	segs, err := parseMedia("https://cdn.example/manifest/stream_x.m3u8", media)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://cdn.example/vid/1280/init.mp4?p=abc",
		"https://cdn.example/vid/1280/seg_1.mp4?p=abc",
		"https://cdn.example/vid/1280/seg_2.mp4?p=abc",
	}
	if len(segs) != len(want) {
		t.Fatalf("got %d segments, want %d", len(segs), len(want))
	}
	for i, s := range segs {
		if s != want[i] {
			t.Errorf("segment %d = %q, want %q", i, s, want[i])
		}
	}
}

func TestParseMediaRejectsUnsupported(t *testing.T) {
	for _, tag := range []string{
		`#EXT-X-KEY:METHOD=AES-128,URI="k"`,
		`#EXT-X-BYTERANGE:1000@0`,
	} {
		body := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n" + tag + "\nseg_1.mp4\n"
		if _, err := parseMedia("https://cdn.example/x.m3u8", body); err == nil {
			t.Errorf("parseMedia accepted unsupported tag %q, want error", tag)
		}
	}
}

// TestFetch exercises the full download against a fake CDN: it asserts the two
// track files are the init+segments concatenated in order, that progress
// reaches the full count, and that concurrency stays within the configured cap.
func TestFetch(t *testing.T) {
	const nSegs = 12
	seg := func(track string, i int) []byte {
		return []byte(fmt.Sprintf("%s-seg-%02d;", track, i))
	}
	initSeg := func(track string) []byte { return []byte(track + "-init;") }

	var inFlight, maxInFlight atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n"+
			"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",DEFAULT=YES,URI=\"audio.m3u8\"\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=100,AUDIO=\"a\"\nvideo.m3u8\n")
	})
	writeMedia := func(w http.ResponseWriter, track string) {
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-MAP:URI=\"%s/init.mp4\"\n", track)
		for i := 1; i <= nSegs; i++ {
			fmt.Fprintf(w, "#EXTINF:4.0,\n%s/seg_%d.mp4\n", track, i)
		}
		fmt.Fprint(w, "#EXT-X-ENDLIST\n")
	}
	mux.HandleFunc("/video.m3u8", func(w http.ResponseWriter, r *http.Request) { writeMedia(w, "video") })
	mux.HandleFunc("/audio.m3u8", func(w http.ResponseWriter, r *http.Request) { writeMedia(w, "audio") })
	serveSeg := func(track string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			cur := inFlight.Add(1)
			for {
				old := maxInFlight.Load()
				if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond) // widen the overlap window
			defer inFlight.Add(-1)
			if r.URL.Path == "/"+track+"/init.mp4" {
				w.Write(initSeg(track))
				return
			}
			var i int
			fmt.Sscanf(r.URL.Path, "/"+track+"/seg_%d.mp4", &i)
			w.Write(seg(track, i))
		}
	}
	mux.HandleFunc("/video/", serveSeg("video"))
	mux.HandleFunc("/audio/", serveSeg("audio"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	const concurrency = 4
	var lastDone, lastTotal atomic.Int64
	videoPath, audioPath, err := Fetch(context.Background(), srv.URL+"/master.m3u8", dir,
		Options{Concurrency: concurrency, UserAgent: "test"},
		func(done, total int) { lastDone.Store(int64(done)); lastTotal.Store(int64(total)) })
	if err != nil {
		t.Fatal(err)
	}

	wantVideo := initSeg("video")
	for i := 1; i <= nSegs; i++ {
		wantVideo = append(wantVideo, seg("video", i)...)
	}
	wantAudio := initSeg("audio")
	for i := 1; i <= nSegs; i++ {
		wantAudio = append(wantAudio, seg("audio", i)...)
	}
	if got, _ := os.ReadFile(videoPath); !bytes.Equal(got, wantVideo) {
		t.Errorf("video file = %q, want %q", got, wantVideo)
	}
	if got, _ := os.ReadFile(audioPath); !bytes.Equal(got, wantAudio) {
		t.Errorf("audio file = %q, want %q", got, wantAudio)
	}
	if want := int64(2 * (nSegs + 1)); lastTotal.Load() != want || lastDone.Load() != want {
		t.Errorf("progress ended at %d/%d, want %d/%d", lastDone.Load(), lastTotal.Load(), want, want)
	}
	if m := maxInFlight.Load(); m > concurrency {
		t.Errorf("max concurrent fetches = %d, exceeds cap %d", m, concurrency)
	}
	if filepath.Dir(videoPath) != dir {
		t.Errorf("video written outside workDir: %s", videoPath)
	}
	// The staged fragment files must be consumed by assembly: leaving them
	// would roughly double the temp dir's peak disk during the later mux.
	if frags, _ := filepath.Glob(filepath.Join(dir, "*.frag")); len(frags) != 0 {
		t.Errorf("%d fragment file(s) left after assembly, want none", len(frags))
	}
}

func TestFetchPropagatesHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nvideo.m3u8\n")
	})
	mux.HandleFunc("/video.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:4.0,\nseg_1.mp4\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "gone", http.StatusNotFound) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, _, err := Fetch(context.Background(), srv.URL+"/master.m3u8", t.TempDir(),
		Options{Concurrency: 2, UserAgent: "test"}, nil); err == nil {
		t.Fatal("Fetch returned nil error despite a failing segment")
	}
}
