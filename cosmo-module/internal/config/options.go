package config

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"codeberg.org/djvu/cosmo-tui/internal/hls"
	"codeberg.org/djvu/cosmo-tui/internal/members"
)

// Options holds the user's runtime configuration, read from the plaintext
// "config" file that lives next to auth.json and overridable per run via
// command-line flags of the same name (see RegisterFlags). Each option is one
// field here, with its default in DefaultOptions and its key registered in
// optionSetters.
//
// The file is `key=value` lines with `#` comments (whole-line or inline).
// Values may be double-quoted to preserve spaces or `#`, and paths may start
// with `~/` to mean the user's home directory. Unknown keys, duplicates, and
// malformed lines are errors so typos surface at startup instead of being
// silently ignored.
type Options struct {
	// Base directories for downloads; media lands in
	// <dir>/cosmo-<group>/<member> beneath them. Each defaults to ".",
	// keeping the historical behavior of downloading relative to the
	// working directory.
	PostDownloadDir   string // room-post media
	ReplayDownloadDir string // replay vods (yt-dlp)
	TalkDownloadDir   string // talk media

	// ReplayFragments is the number of replay-vod fragments fetched in
	// parallel during a download; 0 uses hls.DefaultConcurrency.
	ReplayFragments int

	// LinkHandler is a user script/program that replaces the default
	// openers (xdg-open, in-terminal mpv): every view/open keybind runs
	// it detached with the URL as its single argument. Empty = unset.
	LinkHandler string

	// Artists filters and orders the group carousel (the A key); the
	// first entry is the startup group. Canonical ids, in the user's
	// order. Nil = every group, canonical order.
	Artists []string

	// Tabs selects and orders the visible UI tabs; the first entry is the
	// startup tab. Canonical ids from Tabs, in the user's order. Nil = all
	// tabs, default order.
	Tabs []string

	// Nicknames shows artist-set nicknames on the talk page (matching
	// the app). Off by default, which shows members' real names instead.
	Nicknames bool

	// AutoTranslate turns on auto-translation by default: the talk tab's
	// toggle starts on, and room posts translate on open.
	AutoTranslate bool
}

// Tabs is the full set of UI tabs in default display order; it is also the
// set of valid values for the "tabs" option.
var Tabs = []string{"info", "news", "room", "live", "talk", "objekt", "gravity", "profile"}

// DefaultOptions returns the configuration used when the config file is
// absent or leaves an option unset.
func DefaultOptions() Options {
	return Options{
		PostDownloadDir:   ".",
		ReplayDownloadDir: ".",
		TalkDownloadDir:   ".",
	}
}

// setter validates one option's raw value and applies it to opts.
type setter func(opts *Options, value string) error

// option is one registry entry: the setter shared by the config file and the
// command line, plus the metadata needed to expose it as a flag.
type option struct {
	set     setter
	usage   string // one-line help text for --help
	boolean bool   // registered via flag.BoolFunc so bare --key works
}

// optionSetters maps config keys to their options.
var optionSetters = map[string]option{
	"post-dir": {
		set:   pathSetter(func(o *Options) *string { return &o.PostDownloadDir }),
		usage: "base directory for room-post media downloads",
	},
	"replay-dir": {
		set:   pathSetter(func(o *Options) *string { return &o.ReplayDownloadDir }),
		usage: "base directory for replay vod downloads",
	},
	"talk-dir": {
		set:   pathSetter(func(o *Options) *string { return &o.TalkDownloadDir }),
		usage: "base directory for talk media downloads",
	},
	"fragments": {
		set:   setReplayFragments,
		usage: fmt.Sprintf("number of replay-vod fragments to download in parallel (default %d)", hls.DefaultConcurrency),
	},
	"link-handler": {
		set:   setLinkHandler,
		usage: "program run with a URL argument instead of the default openers",
	},
	"artists": {
		set:   setArtists,
		usage: "comma-separated groups to show, in carousel order",
	},
	"tabs": {
		set:   setTabs,
		usage: "comma-separated tabs to show, in display order (first is the startup tab)",
	},
	"nicknames": {
		set:     boolSetter(func(o *Options) *bool { return &o.Nicknames }),
		usage:   "show artist-set nicknames on the talk page instead of real names",
		boolean: true,
	},
	"auto-translate": {
		set:     boolSetter(func(o *Options) *bool { return &o.AutoTranslate }),
		usage:   "translate incoming talk messages and opened room posts by default",
		boolean: true,
	},
}

// RegisterFlags registers every config option as a flag on fs, applying
// values to opts through the same setters the config file uses, so flags
// given on the command line override the loaded config.
func RegisterFlags(fs *flag.FlagSet, opts *Options) {
	for key, o := range optionSetters {
		apply := func(v string) error { return o.set(opts, v) }
		if o.boolean {
			fs.BoolFunc(key, o.usage, apply)
		} else {
			fs.Func(key, o.usage, apply)
		}
	}
}

// setArtists parses a comma-separated group list, resolving each name
// case-insensitively to its canonical id and rejecting unknowns, duplicates,
// and empty entries.
func setArtists(o *Options, v string) error {
	var list []string
	seen := make(map[string]bool)
	for _, raw := range strings.Split(v, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			return errors.New("empty artist name")
		}
		canonical, ok := members.NormalizeGroup(name)
		if !ok {
			return fmt.Errorf("unknown artist %q (valid: %s)",
				name, strings.Join(members.Groups, ", "))
		}
		if seen[canonical] {
			return fmt.Errorf("duplicate artist %q", canonical)
		}
		seen[canonical] = true
		list = append(list, canonical)
	}
	o.Artists = list
	return nil
}

// setTabs parses a comma-separated tab list, lowercasing each name and
// validating it against Tabs, rejecting unknowns, duplicates, and empty
// entries. The result preserves the user's order; the first entry is the
// startup tab.
func setTabs(o *Options, v string) error {
	var list []string
	seen := make(map[string]bool)
	for _, raw := range strings.Split(v, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			return errors.New("empty tab name")
		}
		if !slices.Contains(Tabs, name) {
			return fmt.Errorf("unknown tab %q (valid: %s)",
				name, strings.Join(Tabs, ", "))
		}
		if seen[name] {
			return fmt.Errorf("duplicate tab %q", name)
		}
		seen[name] = true
		list = append(list, name)
	}
	o.Tabs = list
	return nil
}

// setReplayFragments parses the parallel-fragment count, requiring a positive
// integer so a typo (or 0) fails at startup instead of silently disabling
// downloads.
func setReplayFragments(o *Options, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("invalid number %q", v)
	}
	if n < 1 {
		return fmt.Errorf("must be at least 1, got %d", n)
	}
	o.ReplayFragments = n
	return nil
}

// setLinkHandler stores the link-handler executable, verifying up front that
// it exists (a path or a name on PATH) so a typo fails at startup rather than
// silently doing nothing on every keypress.
func setLinkHandler(o *Options, v string) error {
	if v == "" {
		return errors.New("empty path")
	}
	p, err := expandPath(v)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(p); err != nil {
		return fmt.Errorf("not an executable: %w", err)
	}
	o.LinkHandler = p
	return nil
}

// boolSetter builds a setter that parses a bool into the field selected by
// field.
func boolSetter(field func(*Options) *bool) setter {
	return func(o *Options, v string) error {
		b, err := parseBool(v)
		if err != nil {
			return err
		}
		*field(o) = b
		return nil
	}
}

// pathSetter builds a setter that ~-expands the value into the string field
// selected by field.
func pathSetter(field func(*Options) *string) setter {
	return func(o *Options, v string) error {
		if v == "" {
			return errors.New("empty path")
		}
		p, err := expandPath(v)
		if err != nil {
			return err
		}
		*field(o) = p
		return nil
	}
}

// OptionsPath returns the config file location under the OS config dir.
func OptionsPath() (string, error) {
	dir, err := appDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config"), nil
}

// LoadOptions reads the config file, returning defaults if it doesn't exist.
func LoadOptions() (Options, error) {
	p, err := OptionsPath()
	if err != nil {
		return Options{}, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultOptions(), nil
	}
	if err != nil {
		return Options{}, err
	}
	defer f.Close()
	return parseOptions(f, optionSetters)
}

// parseOptions parses `key=value` lines from r against the given setter
// registry (a parameter so tests can supply their own options). Any invalid
// line fails the whole parse with its line number.
func parseOptions(r io.Reader, setters map[string]option) (Options, error) {
	opts := DefaultOptions()
	seen := make(map[string]bool)
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, found := strings.Cut(line, "=")
		if !found {
			return Options{}, fmt.Errorf("config line %d: expected key=value", n)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return Options{}, fmt.Errorf("config line %d: missing key before %q", n, "=")
		}
		value, err := parseValue(strings.TrimSpace(raw))
		if err != nil {
			return Options{}, fmt.Errorf("config line %d: %w", n, err)
		}
		o, known := setters[key]
		if !known {
			return Options{}, fmt.Errorf("config line %d: unknown option %q", n, key)
		}
		if seen[key] {
			return Options{}, fmt.Errorf("config line %d: duplicate option %q", n, key)
		}
		seen[key] = true
		if err := o.set(&opts, value); err != nil {
			return Options{}, fmt.Errorf("config line %d: %s: %w", n, key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// parseValue strips an inline `#` comment and optional surrounding double
// quotes from raw (already whitespace-trimmed). Quoted values are taken
// verbatim, so they can contain `#` and leading/trailing spaces.
func parseValue(raw string) (string, error) {
	if strings.HasPrefix(raw, `"`) {
		rest := raw[1:]
		i := strings.Index(rest, `"`)
		if i < 0 {
			return "", errors.New("unterminated quote")
		}
		if after := strings.TrimSpace(rest[i+1:]); after != "" && !strings.HasPrefix(after, "#") {
			return "", fmt.Errorf("unexpected text after closing quote: %q", after)
		}
		return rest[:i], nil
	}
	if i := strings.Index(raw, "#"); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(raw), nil
}

// parseBool accepts true/yes and false/no, case-insensitively.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "yes":
		return true, nil
	case "false", "no":
		return false, nil
	}
	return false, fmt.Errorf("invalid bool %q (want true/yes or false/no)", s)
}

// expandPath resolves a leading ~ or ~/ to the user's home directory.
func expandPath(s string) (string, error) {
	if s != "~" && !strings.HasPrefix(s, "~/") {
		return s, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(s, "~")), nil
}
