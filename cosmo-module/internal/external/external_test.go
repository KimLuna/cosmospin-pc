package external

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFFmpegMuxCommand checks the mux invocation stream-copies the two inputs
// into the output (no re-encode) and enables faststart.
func TestFFmpegMuxCommand(t *testing.T) {
	cmd := FFmpegMuxCommand("v.mp4", "a.mp4", "out.mp4")
	got := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"ffmpeg", "-i v.mp4", "-i a.mp4", "-c copy", "-movflags +faststart", "out.mp4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ffmpeg args %q missing %q", got, want)
		}
	}
}

// TestOpenURLReapsHandler checks that the detached handler is waited on. An
// opener exits as soon as it has handed the URL off, and a started child that is
// never waited on stays a zombie until the whole app exits — one per open/view
// keypress. Linux-only: it reads the child states out of /proc.
func TestOpenURLReapsHandler(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc to see child process states")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "handler")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	SetLinkHandler(script)
	defer SetLinkHandler("")

	for i := 0; i < 5; i++ {
		if err := OpenURL("https://example.com/" + strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}

	// The handlers exit on their own schedule, so poll for the reap.
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := zombieChildren(t)
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d handler process(es) left unreaped", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// zombieChildren counts this process's children sitting in the zombie state.
func zombieChildren(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	me := strconv.Itoa(os.Getpid())
	n := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue // not a pid directory
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // exited from under us
		}
		// "pid (comm) state ppid …"; comm can hold spaces, so scan past its ')'.
		rest := string(data)
		if i := strings.LastIndex(rest, ") "); i >= 0 {
			rest = rest[i+2:]
		}
		fields := strings.Fields(rest)
		if len(fields) >= 2 && fields[0] == "Z" && fields[1] == me {
			n++
		}
	}
	return n
}

// TestOpenURLLinkHandler routes OpenURL through a stub handler and checks the
// URL arrives as argv[1].
func TestOpenURLLinkHandler(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := filepath.Join(dir, "handler")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$1\" > "+log+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	SetLinkHandler(script)
	defer SetLinkHandler("")
	if !LinkHandlerSet() {
		t.Fatal("LinkHandlerSet should report true")
	}

	const url = "https://example.com/media/clip.m3u8"
	if err := OpenURL(url); err != nil {
		t.Fatal(err)
	}

	// OpenURL starts the handler detached; poll briefly for its output.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(log); err == nil {
			if got := strings.TrimSpace(string(data)); got != url {
				t.Fatalf("handler argv[1] = %q, want %q", got, url)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("handler never wrote its log")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
