package config

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testSetters builds a registry of one option per supported value kind,
// recording what each setter received so tests can assert on it.
func testSetters(got map[string]any) map[string]option {
	return map[string]option{
		"mybool": {set: func(_ *Options, v string) error {
			b, err := parseBool(v)
			if err != nil {
				return err
			}
			got["mybool"] = b
			return nil
		}},
		"mydir": {set: func(_ *Options, v string) error {
			p, err := expandPath(v)
			if err != nil {
				return err
			}
			got["mydir"] = p
			return nil
		}},
		"mystr": {set: func(_ *Options, v string) error {
			got["mystr"] = v
			return nil
		}},
	}
}

func TestParseOptions(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		in      string
		want    map[string]any
		wantErr string // substring of the expected error; empty means success
	}{
		{
			name: "empty input",
			in:   "",
			want: map[string]any{},
		},
		{
			name: "comments and blank lines skipped",
			in:   "# a comment\n\n   \n  # indented comment\n",
			want: map[string]any{},
		},
		{
			name: "basic values with whitespace",
			in:   "mybool = yes\nmystr =  hello world  \n",
			want: map[string]any{"mybool": true, "mystr": "hello world"},
		},
		{
			name: "inline comment stripped",
			in:   "mystr=value # trailing comment\n",
			want: map[string]any{"mystr": "value"},
		},
		{
			name: "quoted value keeps spaces and hash",
			in:   `mystr=" spaced # value " # real comment`,
			want: map[string]any{"mystr": " spaced # value "},
		},
		{
			name: "empty quoted value",
			in:   `mystr=""`,
			want: map[string]any{"mystr": ""},
		},
		{
			name: "bool variants",
			in:   "mybool=FALSE\n",
			want: map[string]any{"mybool": false},
		},
		{
			name: "tilde expansion",
			in:   "mydir=~/pix/cosmo\n",
			want: map[string]any{"mydir": filepath.Join(home, "pix/cosmo")},
		},
		{
			name: "plain path untouched",
			in:   "mydir=/srv/media\n",
			want: map[string]any{"mydir": "/srv/media"},
		},
		{
			name:    "unknown option",
			in:      "mybool=yes\nnosuch=1\n",
			wantErr: `config line 2: unknown option "nosuch"`,
		},
		{
			name:    "missing equals",
			in:      "\njust some words\n",
			wantErr: "config line 2: expected key=value",
		},
		{
			name:    "missing key",
			in:      "=value\n",
			wantErr: "config line 1: missing key",
		},
		{
			name:    "duplicate option",
			in:      "mybool=yes\nmybool=no\n",
			wantErr: `config line 2: duplicate option "mybool"`,
		},
		{
			name:    "invalid bool",
			in:      "mybool=maybe\n",
			wantErr: `config line 1: mybool: invalid bool "maybe"`,
		},
		{
			name:    "unterminated quote",
			in:      `mystr="oops`,
			wantErr: "config line 1: unterminated quote",
		},
		{
			name:    "junk after closing quote",
			in:      `mystr="ok" junk`,
			wantErr: `config line 1: unexpected text after closing quote: "junk"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]any{}
			_, err := parseOptions(strings.NewReader(tc.in), testSetters(got))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got error %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("%s = %#v, want %#v", k, got[k], want)
				}
			}
		})
	}
}

// TestParseOptionsRealRegistry exercises the actual option set end-to-end.
func TestParseOptionsRealRegistry(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	in := strings.Join([]string{
		"post-dir=~/pix/cosmo # room posts",
		`replay-dir="/srv/media/vods"`,
		"talk-dir=talk",
	}, "\n")
	opts, err := parseOptions(strings.NewReader(in), optionSetters)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Options{
		PostDownloadDir:   filepath.Join(home, "pix/cosmo"),
		ReplayDownloadDir: "/srv/media/vods",
		TalkDownloadDir:   "talk",
	}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("got %+v, want %+v", opts, want)
	}

	// Unset options keep their defaults.
	opts, err = parseOptions(strings.NewReader("talk-dir=~\n"), optionSetters)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.PostDownloadDir != "." || opts.ReplayDownloadDir != "." {
		t.Fatalf("unset options lost defaults: %+v", opts)
	}
	if opts.TalkDownloadDir != home {
		t.Fatalf("bare ~ = %q, want %q", opts.TalkDownloadDir, home)
	}

	// An empty path is rejected.
	_, err = parseOptions(strings.NewReader("post-dir= # nope\n"), optionSetters)
	if err == nil || !strings.Contains(err.Error(), "config line 1: post-dir: empty path") {
		t.Fatalf("got error %v, want empty-path error", err)
	}
}

// TestNicknamesOption checks the bool parse and its default (false).
func TestNicknamesOption(t *testing.T) {
	if DefaultOptions().Nicknames {
		t.Fatal("nicknames should default to false")
	}

	for in, want := range map[string]bool{
		"nicknames=false\n": false,
		"nicknames=no\n":    false,
		"nicknames=TRUE\n":  true,
	} {
		opts, err := parseOptions(strings.NewReader(in), optionSetters)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", in, err)
		}
		if opts.Nicknames != want {
			t.Fatalf("%q: Nicknames = %v, want %v", in, opts.Nicknames, want)
		}
	}

	_, err := parseOptions(strings.NewReader("nicknames=maybe\n"), optionSetters)
	if err == nil || !strings.Contains(err.Error(), "config line 1: nicknames: invalid bool") {
		t.Fatalf("got error %v, want invalid-bool error", err)
	}
}

// TestAutoTranslateOption checks the bool parse and its default (false).
func TestAutoTranslateOption(t *testing.T) {
	if DefaultOptions().AutoTranslate {
		t.Fatal("auto-translate should default to false")
	}

	for in, want := range map[string]bool{
		"auto-translate=true\n":  true,
		"auto-translate=yes\n":   true,
		"auto-translate=FALSE\n": false,
	} {
		opts, err := parseOptions(strings.NewReader(in), optionSetters)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", in, err)
		}
		if opts.AutoTranslate != want {
			t.Fatalf("%q: AutoTranslate = %v, want %v", in, opts.AutoTranslate, want)
		}
	}

	_, err := parseOptions(strings.NewReader("auto-translate=maybe\n"), optionSetters)
	if err == nil || !strings.Contains(err.Error(), "config line 1: auto-translate: invalid bool") {
		t.Fatalf("got error %v, want invalid-bool error", err)
	}
}

// TestLinkHandlerOption checks the executable validation on link-handler.
func TestLinkHandlerOption(t *testing.T) {
	script := filepath.Join(t.TempDir(), "handler")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts, err := parseOptions(strings.NewReader("link-handler="+script+"\n"), optionSetters)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.LinkHandler != script {
		t.Fatalf("LinkHandler = %q, want %q", opts.LinkHandler, script)
	}
	if DefaultOptions().LinkHandler != "" {
		t.Fatal("link-handler should default to unset")
	}

	// A bare command name resolves on PATH.
	if _, err := parseOptions(strings.NewReader("link-handler=sh\n"), optionSetters); err != nil {
		t.Fatalf("PATH lookup failed: %v", err)
	}

	// Nonexistent handlers fail at parse, with the line number.
	_, err = parseOptions(strings.NewReader("\nlink-handler=/no/such/thing\n"), optionSetters)
	if err == nil || !strings.Contains(err.Error(), "config line 2: link-handler: not an executable") {
		t.Fatalf("got error %v, want not-an-executable error", err)
	}
}

// TestLoadOptionsMissingFile points the OS config dir at an empty temp dir
// and expects silent defaults, matching a fresh install.
func TestLoadOptionsMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := os.UserConfigDir(); err != nil {
		t.Skip("XDG_CONFIG_HOME override not honored on this platform")
	}
	if p, err := OptionsPath(); err != nil {
		t.Fatal(err)
	} else if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Skip("XDG_CONFIG_HOME override not honored on this platform")
	}
	opts, err := LoadOptions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(opts, DefaultOptions()) {
		t.Fatalf("got %+v, want defaults", opts)
	}
}

// TestRegisterFlags checks that command-line flags run the same setters as
// the config file and override previously loaded values, while untouched
// options keep theirs.
func TestRegisterFlags(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	opts, err := parseOptions(strings.NewReader(
		"post-dir=/srv/posts\ntalk-dir=/srv/talk\n"), optionSetters)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	RegisterFlags(fs, &opts)
	args := []string{
		"--talk-dir=~/talk",
		"--nicknames=true",
		"--artists=triples",
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Options{
		PostDownloadDir:   "/srv/posts", // from the config file, untouched
		ReplayDownloadDir: ".",          // default, untouched
		TalkDownloadDir:   filepath.Join(home, "talk"),
		Artists:           []string{"tripleS"},
		Nicknames:         true,
	}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("got %+v, want %+v", opts, want)
	}

	// The bool flag also works bare.
	opts = DefaultOptions()
	fs = flag.NewFlagSet("test", flag.ContinueOnError)
	RegisterFlags(fs, &opts)
	if err := fs.Parse([]string{"--nicknames"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opts.Nicknames {
		t.Fatal("bare --nicknames should enable the option")
	}

	// Setter validation applies to flags too.
	opts = DefaultOptions()
	fs = flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	RegisterFlags(fs, &opts)
	err = fs.Parse([]string{"--artists=nosuch"})
	if err == nil || !strings.Contains(err.Error(), "unknown artist") {
		t.Fatalf("got error %v, want unknown-artist error", err)
	}
}

// TestArtistsOption checks the carousel list: canonicalization, ordering, and
// the strict rejections.
func TestArtistsOption(t *testing.T) {
	opts, err := parseOptions(strings.NewReader("artists=idntt, ARTMS\n"), optionSetters)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"idntt", "artms"}; !reflect.DeepEqual(opts.Artists, want) {
		t.Fatalf("Artists = %v, want %v", opts.Artists, want)
	}

	opts, err = parseOptions(strings.NewReader("artists=triples\n"), optionSetters)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"tripleS"}; !reflect.DeepEqual(opts.Artists, want) {
		t.Fatalf("Artists = %v, want %v", opts.Artists, want)
	}

	if DefaultOptions().Artists != nil {
		t.Fatal("artists should default to nil (all groups)")
	}

	for in, wantErr := range map[string]string{
		"artists=tripleS,loona": `config line 1: artists: unknown artist "loona" (valid: tripleS, artms, idntt)`,
		"artists=artms,ARTMS":   `config line 1: artists: duplicate artist "artms"`,
		"artists=idntt,,artms":  "config line 1: artists: empty artist name",
		"artists=":              "config line 1: artists: empty artist name",
	} {
		_, err := parseOptions(strings.NewReader(in+"\n"), optionSetters)
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: got error %v, want one containing %q", in, err, wantErr)
		}
	}
}

func TestTabsOption(t *testing.T) {
	opts, err := parseOptions(strings.NewReader("tabs=talk,room,live,news\n"), optionSetters)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"talk", "room", "live", "news"}; !reflect.DeepEqual(opts.Tabs, want) {
		t.Fatalf("Tabs = %v, want %v", opts.Tabs, want)
	}

	// Names are case-insensitive and normalize to lowercase.
	opts, err = parseOptions(strings.NewReader("tabs=TALK, Info\n"), optionSetters)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"talk", "info"}; !reflect.DeepEqual(opts.Tabs, want) {
		t.Fatalf("Tabs = %v, want %v", opts.Tabs, want)
	}

	if DefaultOptions().Tabs != nil {
		t.Fatal("tabs should default to nil (all tabs)")
	}

	for in, wantErr := range map[string]string{
		"tabs=talk,bogus": `config line 1: tabs: unknown tab "bogus" (valid: info, news, room, live, talk, objekt, gravity, profile)`,
		"tabs=talk,TALK":  `config line 1: tabs: duplicate tab "talk"`,
		"tabs=talk,,room": "config line 1: tabs: empty tab name",
		"tabs=":           "config line 1: tabs: empty tab name",
	} {
		_, err := parseOptions(strings.NewReader(in+"\n"), optionSetters)
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: got error %v, want one containing %q", in, err, wantErr)
		}
	}
}
