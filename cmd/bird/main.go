// Command bird is the Bird reminder daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/bird-app/bird/internal/config"
	"github.com/bird-app/bird/internal/daemon"
	"github.com/bird-app/bird/internal/platform"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

const usage = `Bird — a desktop reminder that flies in when it's time.

Usage:
  bird [flags]                 run the reminder daemon (foreground)
  bird --check-config          validate the configuration and list upcoming reminders
  bird --preview <event-id>    show one event's reminder now (no state is changed)
  bird --version               print the version

Flags:
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bird", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage); fs.PrintDefaults() }

	cfgPath := fs.String("config", platform.ConfigPath(), "path to config.yaml")
	check := fs.Bool("check-config", false, "validate the configuration and exit")
	preview := fs.String("preview", "", "show the reminder for this event `id` now and exit")
	showVersion := fs.Bool("version", false, "print version and exit")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	noTray := fs.Bool("no-tray", false, "disable the tray icon")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}

	switch {
	case *showVersion:
		fmt.Fprintf(stdout, "bird %s\n", version)
		return 0
	case *check:
		return checkConfig(*cfgPath, stdout, stderr)
	}

	log, err := newLogger(*logLevel, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opt := daemon.Options{
		ConfigPath: *cfgPath,
		StateDir:   platform.StateDir(),
		Session:    platform.DetectSession(),
		Log:        log,
		NoTray:     *noTray,
	}

	if *preview != "" {
		chosen, err := daemon.Preview(ctx, opt, *preview)
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "preview failed: %v\n", err)
			return 1
		}
		if chosen == "" {
			chosen = "(none: interrupted)"
		}
		fmt.Fprintf(stdout, "chosen action: %s\n", chosen)
		return 0
	}

	if err := daemon.Run(ctx, opt); err != nil {
		if errors.Is(err, platform.ErrAlreadyRunning) {
			fmt.Fprintln(stderr, "bird is already running (see `systemctl --user status bird`)")
			return 1
		}
		log.Error("fatal", "err", err)
		return 1
	}
	return 0
}

func checkConfig(path string, stdout, stderr io.Writer) int {
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "no configuration at %s\nCreate one from examples/workday.yaml (see bird-docs/CONFIGURATION.md).\n", path)
		} else {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
		}
		return 1
	}
	now := time.Now().In(cfg.Location)
	fmt.Fprintf(stdout, "Configuration OK: %s\n", path)
	fmt.Fprintf(stdout, "Time zone: %s (now %s)\n", cfg.Location, now.Format("Mon 2006-01-02 15:04 MST"))
	fmt.Fprintf(stdout, "Catch-up window: %s\n\n", cfg.App.CatchUp())
	if len(cfg.Events) == 0 {
		fmt.Fprintln(stdout, "No events configured.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "EVENT\tSCHEDULE\tNEXT\tACTIONS")
	for _, ev := range cfg.Events {
		next := "disabled"
		if ev.IsEnabled() {
			next = ev.Compiled.Next(now).Format("Mon 2006-01-02 15:04")
		}
		var actions string
		for i, a := range ev.Actions {
			if i > 0 {
				actions += ", "
			}
			actions += a.ID + ":" + a.Action
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ev.ID, ev.Compiled, next, actions)
	}
	tw.Flush()
	return 0
}

func newLogger(level string, w io.Writer) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid --log-level %q", level)
	}
	opts := &slog.HandlerOptions{Level: l}
	// journald timestamps every line itself.
	if os.Getenv("JOURNAL_STREAM") != "" {
		opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}
	}
	return slog.New(slog.NewTextHandler(w, opts)), nil
}
