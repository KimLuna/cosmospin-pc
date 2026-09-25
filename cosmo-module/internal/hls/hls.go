// Package hls downloads a Cloudflare Stream VOD from its HLS master playlist.
// Cosmo replays are demuxed HLS: video-only variants plus a separate audio
// rendition, both fragmented MP4. Fetch grabs the best video track and the
// audio track in parallel (many fragments at once — the single knob that makes
// Cloudflare Stream downloads fast) and concatenates each into a single-track
// file. Muxing the two tracks into one playable mp4 is the caller's job (see
// external.FFmpegMuxCommand); the package itself has no external dependencies.
package hls

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultConcurrency is the fragment-fetch parallelism used when Options leaves
// it unset; it mirrors the -N value the app previously passed to yt-dlp.
const DefaultConcurrency = 32

const (
	maxRetries    = 5
	retryBackoff  = 500 * time.Millisecond
	videoTrackIdx = 0
	audioTrackIdx = 1
)

// Options configures a Fetch. The zero value is usable: Concurrency falls back
// to DefaultConcurrency and Client to a tuned default.
type Options struct {
	Concurrency int          // parallel fragment fetches
	UserAgent   string       // sent on every request (Cosmo's CDN wants okhttp's UA)
	Client      *http.Client // nil → a keep-alive-friendly default
}

// Fetch resolves masterURL, downloads the best video rendition and the audio
// rendition in parallel into workDir, and returns the concatenated per-track
// file paths (video.mp4, audio.mp4). progress, if non-nil, is called as each
// fragment lands with the running count across both tracks. When the stream
// carries no separate audio rendition (already-muxed variant), audioPath is ""
// and the video file is a complete playable mp4.
func Fetch(ctx context.Context, masterURL, workDir string, opts Options, progress func(done, total int)) (videoPath, audioPath string, err error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultConcurrency
	}
	d := &downloader{
		client: opts.Client,
		ua:     opts.UserAgent,
	}
	if d.client == nil {
		d.client = defaultClient(opts.Concurrency)
	}

	masterBody, err := d.get(ctx, masterURL)
	if err != nil {
		return "", "", fmt.Errorf("fetch master playlist: %w", err)
	}
	videoPL, audioPL, err := parseMaster(masterURL, string(masterBody))
	if err != nil {
		return "", "", err
	}

	// Resolve each track's ordered segment list (init prepended).
	tracks := make([][]string, 1, 2)
	body, err := d.get(ctx, videoPL)
	if err != nil {
		return "", "", fmt.Errorf("fetch video playlist: %w", err)
	}
	if tracks[videoTrackIdx], err = parseMedia(videoPL, string(body)); err != nil {
		return "", "", fmt.Errorf("video playlist: %w", err)
	}
	if audioPL != "" {
		body, err := d.get(ctx, audioPL)
		if err != nil {
			return "", "", fmt.Errorf("fetch audio playlist: %w", err)
		}
		segs, err := parseMedia(audioPL, string(body))
		if err != nil {
			return "", "", fmt.Errorf("audio playlist: %w", err)
		}
		tracks = append(tracks, segs)
	}

	if err := d.downloadAll(ctx, workDir, tracks, opts.Concurrency, progress); err != nil {
		return "", "", err
	}

	videoPath = filepath.Join(workDir, "video.mp4")
	if err := writeConcat(videoPath, workDir, videoTrackIdx, len(tracks[videoTrackIdx])); err != nil {
		return "", "", err
	}
	if len(tracks) > 1 {
		audioPath = filepath.Join(workDir, "audio.mp4")
		if err := writeConcat(audioPath, workDir, audioTrackIdx, len(tracks[audioTrackIdx])); err != nil {
			return "", "", err
		}
	}
	return videoPath, audioPath, nil
}

// downloader holds the shared HTTP state for one Fetch.
type downloader struct {
	client *http.Client
	ua     string
}

// job identifies one fragment to fetch within the tracks slice.
type job struct{ track, seg int }

// fragPath names track t's seg-th fragment file inside workDir. Fragments are
// staged as individual files and assembled by writeConcat, so memory holds at
// most one in-flight fragment per worker instead of the whole video.
func fragPath(workDir string, track, seg int) string {
	return filepath.Join(workDir, fmt.Sprintf("t%d_%06d.frag", track, seg))
}

// downloadAll fetches every fragment of every track using a bounded worker
// pool, writing each to its own fragPath file under workDir. The first failing
// fragment (fetch or write) cancels the rest.
func (d *downloader) downloadAll(ctx context.Context, workDir string, tracks [][]string, workers int, progress func(done, total int)) error {
	var jobs []job
	for t, segs := range tracks {
		for s := range segs {
			jobs = append(jobs, job{t, s})
		}
	}
	total := len(jobs)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobCh := make(chan job)
	var errOnce sync.Once
	var firstErr error
	var wg sync.WaitGroup
	fail := func(err error) { errOnce.Do(func() { firstErr = err; cancel() }) }

	// One mutex covers the count and the callback so reports arrive in order;
	// bare atomics let two workers race past each other between incrementing
	// and reporting, and the final report could then be total-1 of total.
	var progressMu sync.Mutex
	done := 0
	report := func() {
		progressMu.Lock()
		defer progressMu.Unlock()
		done++
		if progress != nil {
			progress(done, total)
		}
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				b, err := d.fetchWithRetry(ctx, tracks[j.track][j.seg])
				if err != nil {
					fail(err)
					return
				}
				if err := os.WriteFile(fragPath(workDir, j.track, j.seg), b, 0o644); err != nil {
					fail(err)
					return
				}
				report()
			}
		}()
	}

	for _, j := range jobs {
		select {
		case <-ctx.Done():
			// A worker failed (or the caller cancelled); stop feeding.
			goto wait
		case jobCh <- j:
		}
	}
wait:
	close(jobCh)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// fetchWithRetry fetches one fragment, retrying transient failures a bounded
// number of times. yt-dlp retried forever; a cap avoids hanging on a dead URL.
func (d *downloader) fetchWithRetry(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryBackoff):
			}
		}
		b, err := d.get(ctx, rawURL)
		if err == nil {
			return b, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// get performs a single GET and returns the body. Media is public CDN content,
// so no auth header is sent — only the User-Agent, matching cosmo.Download.
func (d *downloader) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if d.ua != "" {
		req.Header.Set("User-Agent", d.ua)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// defaultClient builds an HTTP client whose transport keeps enough idle
// connections per host to reuse keep-alives across the parallel fetches.
func defaultClient(concurrency int) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = concurrency * 2
	tr.MaxIdleConnsPerHost = concurrency
	return &http.Client{Transport: tr}
}

// writeConcat assembles a track's n staged fragment files, in playlist order
// (init first), into one file at path. Each fragment is deleted as it is
// consumed: the freed space roughly offsets the growing output, keeping peak
// disk near one copy of the track instead of two.
func writeConcat(path, workDir string, track, n int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	for seg := 0; seg < n; seg++ {
		fp := fragPath(workDir, track, seg)
		if err := appendFile(f, fp); err != nil {
			f.Close()
			return err
		}
		_ = os.Remove(fp)
	}
	return f.Close()
}

// appendFile copies src's contents onto the end of dst.
func appendFile(dst *os.File, src string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	_, err = io.Copy(dst, s)
	return err
}

// parseMaster picks the highest-bandwidth video variant and the default audio
// rendition from a master playlist, returning their absolute URLs. audioURL is
// "" when the master has no separate audio group (an already-muxed variant).
func parseMaster(masterURL, body string) (videoURL, audioURL string, err error) {
	base, err := url.Parse(masterURL)
	if err != nil {
		return "", "", err
	}
	var bestBandwidth int
	var audioDefault, audioFirst string

	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			attrs := parseAttrs(line[len("#EXT-X-MEDIA:"):])
			if attrs["TYPE"] != "AUDIO" || attrs["URI"] == "" {
				continue
			}
			u, err := resolve(base, attrs["URI"])
			if err != nil {
				return "", "", err
			}
			if audioFirst == "" {
				audioFirst = u
			}
			if strings.EqualFold(attrs["DEFAULT"], "YES") {
				audioDefault = u
			}
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			attrs := parseAttrs(line[len("#EXT-X-STREAM-INF:"):])
			bw, _ := strconv.Atoi(attrs["BANDWIDTH"])
			// The URI is the next non-comment, non-blank line.
			uri := nextURI(lines, &i)
			if uri == "" {
				continue
			}
			if videoURL == "" || bw > bestBandwidth {
				u, err := resolve(base, uri)
				if err != nil {
					return "", "", err
				}
				videoURL, bestBandwidth = u, bw
			}
		}
	}
	if videoURL == "" {
		return "", "", fmt.Errorf("no video variant in master playlist")
	}
	if audioDefault != "" {
		return videoURL, audioDefault, nil
	}
	return videoURL, audioFirst, nil
}

// nextURI advances *i past comment/blank lines to the following URI line,
// leaving *i on that line, and returns it (or "" if none remains).
func nextURI(lines []string, i *int) string {
	for *i++; *i < len(lines); *i++ {
		line := strings.TrimSpace(lines[*i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

// parseMedia returns a media playlist's fragments as absolute URLs, the
// #EXT-X-MAP init segment first. Encrypted or byte-range playlists are rejected
// rather than silently mis-assembled.
func parseMedia(mediaURL, body string) ([]string, error) {
	base, err := url.Parse(mediaURL)
	if err != nil {
		return nil, err
	}
	var segs []string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs := parseAttrs(line[len("#EXT-X-MAP:"):])
			if attrs["URI"] == "" {
				return nil, fmt.Errorf("EXT-X-MAP without URI")
			}
			u, err := resolve(base, attrs["URI"])
			if err != nil {
				return nil, err
			}
			segs = append(segs, u) // init segment leads the track
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			return nil, fmt.Errorf("encrypted playlist (EXT-X-KEY) not supported")
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			return nil, fmt.Errorf("byte-range playlist (EXT-X-BYTERANGE) not supported")
		case strings.HasPrefix(line, "#"):
			continue
		default:
			u, err := resolve(base, line)
			if err != nil {
				return nil, err
			}
			segs = append(segs, u)
		}
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("no segments in media playlist")
	}
	return segs, nil
}

// resolve turns a possibly-relative playlist URI into an absolute URL against
// base (the playlist's own URL); Cloudflare's segment URIs are "../../"-relative.
func resolve(base *url.URL, ref string) (string, error) {
	u, err := base.Parse(ref)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// parseAttrs splits an HLS attribute list (KEY=VALUE, comma-separated) into a
// map, honoring double-quoted values that may themselves contain commas (e.g.
// CODECS="avc1.4d401f,mp4a.40.2") and stripping the surrounding quotes.
func parseAttrs(s string) map[string]string {
	attrs := make(map[string]string)
	var key, val strings.Builder
	inKey, inQuote := true, false
	flush := func() {
		if key.Len() > 0 {
			attrs[strings.TrimSpace(key.String())] = val.String()
		}
		key.Reset()
		val.Reset()
		inKey = true
	}
	for _, r := range s {
		switch {
		case inKey && r == '=':
			inKey = false
		case inKey:
			key.WriteRune(r)
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			flush()
		default:
			val.WriteRune(r)
		}
	}
	flush()
	return attrs
}
