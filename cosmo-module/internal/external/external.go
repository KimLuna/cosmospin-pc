// Package external launches the user's real media tools: a URL/file opener,
// mpv for viewing, and ffmpeg for muxing downloaded replay tracks. These are
// the "external viewers" the app uses instead of rendering media in-terminal. A
// link-handler script configured via the config file overrides the openers (see
// SetLinkHandler).
package external

import (
	"os"
	"os/exec"
	"runtime"
)

// linkHandler, when non-empty, replaces the per-OS opener in OpenURL. Set
// once at startup, before the TUI runs, so no locking is needed.
var linkHandler string

// SetLinkHandler routes subsequent OpenURL calls through the given
// executable, invoked detached as `handler <url>`. Empty restores the
// default per-OS opener.
func SetLinkHandler(path string) { linkHandler = path }

// LinkHandlerSet reports whether a link-handler is configured, for call
// sites whose default isn't OpenURL (live's in-terminal mpv).
func LinkHandlerSet() bool { return linkHandler != "" }

// OpenURL opens a URL or file path in the user's link-handler if one is
// configured, else the OS default handler (browser, image viewer, etc). It
// returns quickly; the handler runs detached.
//
// The started process is reaped in the background. Nothing here waits on the
// handler's exit, but a started child still has to be waited on to release it:
// openers exit almost immediately after handing the URL off, and an unreaped one
// stays a zombie for the life of the TUI — one per open/view keypress, in a
// session that can run for hours.
func OpenURL(target string) error {
	var cmd *exec.Cmd
	switch {
	case linkHandler != "":
		cmd = exec.Command(linkHandler, target)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", target)
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// LocalOrURL returns localPath if a file exists there, else rawURL. Call sites
// pass the path their download flow would have written to, so opening media
// that is already saved reuses the local copy instead of re-fetching it from
// the CDN.
func LocalOrURL(localPath, rawURL string) string {
	if localPath == "" {
		return rawURL
	}
	if _, err := os.Stat(localPath); err != nil {
		return rawURL
	}
	return localPath
}

// Available reports whether an executable is on PATH, for pre-flight checks.
func Available(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// MPVCommand builds an mpv invocation for a video URL. Hand it to
// tea.ExecProcess so mpv gets the terminal and the TUI restores on exit.
func MPVCommand(url string) *exec.Cmd {
	return exec.Command("mpv", "--quiet", url)
}

// FFmpegMuxCommand builds an ffmpeg invocation that muxes a separate video and
// audio track into outPath by stream copy (no re-encode), so it is near-instant.
// Cosmo replays are demuxed HLS; the hls package downloads each track and this
// combines them into one playable mp4.
func FFmpegMuxCommand(videoPath, audioPath, outPath string) *exec.Cmd {
	return exec.Command("ffmpeg",
		"-y",
		"-loglevel", "error",
		"-i", videoPath,
		"-i", audioPath,
		"-c", "copy",
		"-movflags", "+faststart",
		outPath,
	)
}
