// Command cosmo-tui is a terminal UI for the Cosmo phone app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/config"
	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/external"
	"codeberg.org/djvu/cosmo-tui/internal/members"
	"codeberg.org/djvu/cosmo-tui/internal/tui"
	"codeberg.org/djvu/cosmo-tui/internal/tui/login"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	tea "charm.land/bubbletea/v2"
)

// version is the build version, set via -ldflags "-X main.version=..." by
// packaging that knows a better name for the build than the source tree does.
// Left empty, buildVersion falls back to what the toolchain stamped in.
var version = ""

// buildVersion describes the running binary. Preference goes to a
// linker-supplied version, then the VCS revision the toolchain records for
// builds made inside a git checkout, then the module version, which is the
// only one of the three present after "go install pkg@version".
func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var revision, date string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			date, _, _ = strings.Cut(s.Value, "T")
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		return "dev"
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if modified {
		revision += "-dirty"
	}
	if date != "" {
		return revision + " (" + date + ")"
	}
	return revision
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// Options resolve in three layers: defaults, then the config file
	// (skipped with --no-config), then command-line flags. Flags are
	// parsed first, against the defaults, because --no-config decides
	// whether the file is read at all; when it is, the flags are parsed
	// again on top so they override the file for this run. All of this
	// happens before any login flow or network activity, so a bad option
	// fails fast.
	opts := config.DefaultOptions()
	fs := flag.NewFlagSet("cosmo-tui", flag.ExitOnError)
	config.RegisterFlags(fs, &opts)
	noConfig := fs.Bool("no-config", false, "ignore the config file")
	noAuth := fs.Bool("no-auth", false, "ignore stored credentials and log in again")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Parse(os.Args[1:]) // ExitOnError: a bad flag prints usage and exits
	if *showVersion {
		fmt.Println("cosmo-tui", buildVersion())
		return nil
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if !*noConfig {
		fileOpts, err := config.LoadOptions()
		if err != nil {
			return err
		}
		// The registered flags close over &opts, so reparsing applies
		// the command line on top of the file's values.
		opts = fileOpts
		fs.Parse(os.Args[1:])
	}
	external.SetLinkHandler(opts.LinkHandler)

	// Load stored credentials; on first use - or when what is stored is
	// unusable - run the sign-in flow. --no-auth skips the stored
	// credentials entirely, forcing a fresh sign-in (which overwrites
	// auth.json as usual).
	var creds config.Credentials
	var err error
	if *noAuth {
		creds, err = login.Run()
	} else {
		creds, err = config.Load()
		if errors.Is(err, config.ErrNoCredentials) {
			creds, err = login.Run()
		}
	}
	if err != nil {
		return err
	}

	// Build the client and confirm we can produce a fresh access token
	// (refreshing + persisting if the stored one is stale).
	client := cosmo.New(creds, config.Save)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.EnsureToken(ctx); err != nil {
		return err
	}
	cancel() // startup token check done; the app manages its own timeouts

	// Load the objekt-send signing key if one was provisioned at login. Its
	// absence just disables sending; everything else works without it.
	var w *wallet.Wallet
	if keyBytes, err := config.LoadWalletKey(); err == nil {
		w, _ = wallet.FromKeyBytes(keyBytes)
	}

	// Launch the TUI app shell on the configured artists (or all of them),
	// opening on the first.
	groups := opts.Artists
	if len(groups) == 0 {
		groups = members.Groups
	}
	p := tea.NewProgram(tui.New(client, groups, opts, w))
	_, err = p.Run()
	return err
}
