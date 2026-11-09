package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kloudkit/hubridge/internal/doctor"
	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/install"
	"github.com/kloudkit/hubridge/internal/mcpproxy"
	"github.com/kloudkit/hubridge/internal/proc"
	"github.com/kloudkit/hubridge/internal/protocol"
	"github.com/kloudkit/hubridge/internal/rules"
	"github.com/kloudkit/hubridge/internal/sessions"
	"github.com/kloudkit/hubridge/internal/status"
	"github.com/kloudkit/hubridge/internal/update"
	"github.com/kloudkit/hubridge/internal/usage"
	"github.com/kloudkit/hubridge/internal/wsclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version, commit = "dev", ""

const help = `usage: hubridge [command] [flags]

commands:
  run          watch transcripts and stream them to hubd, and write the instructions it sends (default)
  enroll       --url <hubd> (--code <code> | --token-file <path>) [--machine <name>]
  setup        add the hubd hooks to Claude Code's settings.json and wrap its status line (--remove undoes both)
  hook         session-start | stop, run by Claude Code
  supervise    restart loop for run (--detach to background it)
  mcp          serve hubd's MCP to one Claude Code session over stdio, forwarding to hubd with the client token
  statusline   run by Claude Code as its status line: keep the usage reading, then run the original
  doctor       check the Claude Code formats the daemon reads against the pinned ones (--days 7), list the instruction files and read the status file
  update       install the newest build of the saved channel (--version bundled|latest|<tag> changes the channel)
  version      print the version and the update channel

every command takes --home <dir> (default $HUBD_HOME, else $XDG_CONFIG_HOME/hubd, else ~/.config/hubd)
`

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run":
		err = run(args)
	case "enroll":
		err = enroll(args)
	case "setup":
		err = setup(args)
	case "hook":
		hook(args)
		return
	case "supervise":
		err = supervise(args)
	case "mcp":
		err = mcpCmd(args)
	case "mcp-headers":
		err = mcpHeaders(args)
	case "statusline":
		os.Exit(statusLine(args))
	case "doctor":
		os.Exit(doctorCmd(args))
	case "update":
		err = updateCmd(args)
	case "version":
		printVersion(args)
	case "help":
		fmt.Print(help)
	default:
		fmt.Fprint(os.Stderr, help)
		os.Exit(2)
	}
	if errors.Is(err, wsclient.ErrRevoked) {
		fmt.Fprintln(os.Stderr, "hubridge:", err)
		os.Exit(install.ExitRevoked)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hubridge "+cmd+":", err)
		os.Exit(1)
	}
}

func parse(name string, args []string, mode flag.ErrorHandling, open bool, define func(*flag.FlagSet)) (*home.Home, error) {
	fs := flag.NewFlagSet(name, mode)
	fs.Usage = func() { fmt.Fprint(os.Stderr, help) }
	homeFlag := fs.String("home", "", "client home")
	if define != nil {
		define(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	dir, err := home.Resolve(*homeFlag)
	if err != nil || !open {
		return &home.Home{Dir: dir}, err
	}
	return home.Open(dir)
}

func self() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return os.Args[0]
}

func enroll(args []string) error {
	var o install.EnrollOptions
	h, err := parse("enroll", args, flag.ExitOnError, true, func(fs *flag.FlagSet) {
		fs.StringVar(&o.URL, "url", "", "hubd origin")
		fs.StringVar(&o.Code, "code", "", "one-time install code")
		fs.StringVar(&o.TokenFile, "token-file", "", "file holding a token")
		fs.StringVar(&o.Machine, "machine", "", "this machine's name")
	})
	if err != nil {
		return err
	}
	if err := install.Enroll(h, o); err != nil {
		return err
	}
	cfg, _ := h.Config()
	fmt.Printf("enrolled as %s; token and config are in %s\n", cfg.Machine, h.Dir)
	return nil
}

func setup(args []string) error {
	var remove bool
	h, err := parse("setup", args, flag.ExitOnError, false, func(fs *flag.FlagSet) {
		fs.BoolVar(&remove, "remove", false, "take the hubd hooks out")
	})
	if err != nil {
		return err
	}
	path, err := install.SettingsPath()
	if err != nil {
		return err
	}
	if err := install.Setup(path, h.Dir, remove); err != nil {
		return err
	}
	fmt.Println("updated", path)
	if remove {
		if err := install.UnwrapStatusLine(path, h.Dir); err != nil {
			return err
		}
		return install.UnregisterMCP(os.Stdout)
	}
	wrapped, err := install.WrapStatusLine(path, h.Dir)
	if err != nil {
		return err
	}
	if wrapped {
		fmt.Println("wrapped the status line, to read Claude usage")
	}
	if h, err = home.Open(h.Dir); err != nil {
		return err
	}
	return install.RegisterMCP(h.Dir, os.Stdout)
}

func statusLine(args []string) int {
	h, err := parse("statusline", args, flag.ContinueOnError, false, nil)
	if err != nil {
		return 0
	}
	return install.RunStatusLine(context.Background(), h, os.Stdin, os.Stdout, os.Stderr, time.Now())
}

func mcpHeaders(args []string) error {
	h, err := parse("mcp-headers", args, flag.ExitOnError, false, nil)
	if err != nil {
		return err
	}
	return install.Headers(h, os.Stdout)
}

func mcpCmd(args []string) error {
	h, err := parse("mcp", args, flag.ExitOnError, true, nil)
	if err != nil {
		return err
	}
	cfg, err := h.Config()
	if err != nil {
		return err
	}
	path, err := status.McpPath()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	p := mcpproxy.New(h, strings.TrimRight(cfg.URL, "/")+"/mcp", version, status.New(path, cfg.Machine, cfg.URL, version, log), log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return p.Serve(ctx, &mcp.StdioTransport{})
}

func hook(args []string) {
	if len(args) == 0 {
		return
	}
	event := args[0]
	h, err := parse("hook", args[1:], flag.ContinueOnError, true, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hubridge hook:", err)
		return
	}
	wait := install.StdinWait
	if event == "stop" {
		wait = install.StopWait
	}
	in := install.ReadInputWait(os.Stdin, wait)
	if in.SessionID == "" {
		return
	}
	switch event {
	case "session-start":
		err = install.SessionStart(h, func() error { return install.Detach(h, self()) })
	case "stop":
		err = install.Stop(h, in, time.Now())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hubridge hook:", err)
	}
}

func supervise(args []string) error {
	var detach bool
	h, err := parse("supervise", args, flag.ExitOnError, true, func(fs *flag.FlagSet) {
		fs.BoolVar(&detach, "detach", false, "run in the background and return")
	})
	if err != nil {
		return err
	}
	if detach {
		if h.Held("supervisor.lock") {
			return nil
		}
		return install.Detach(h, self())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = install.NewSupervisor(h, self(), slog.New(slog.NewTextHandler(os.Stderr, nil))).Run(ctx)
	if errors.Is(err, install.ErrUpgraded) {
		bin := install.Binary(h.Dir)
		return syscall.Exec(bin, []string{bin, "supervise", "--home", h.Dir}, os.Environ())
	}
	return err
}

func run(args []string) error {
	var projects string
	h, err := parse("run", args, flag.ExitOnError, true, func(fs *flag.FlagSet) {
		fs.StringVar(&projects, "projects", projectsDir(), "Claude Code transcripts root")
	})
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	lock, err := h.LockFile("daemon.lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	cfg, err := h.Config()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tracker := sessions.New(projects, log)
	tracker.Background, tracker.ClearBackground = h.Backgrounds, h.ClearBackground
	if tracker.Registry.Claude == "" {
		log.Warn("claude not found on PATH: live state comes from the registry files alone, with no waiting reasons or background sessions")
	}
	client := wsclient.New(cfg.URL, cfg.Machine, version, cfg.Channel, h, tracker.Snapshot, log)
	if path, err := status.Path(); err != nil {
		log.Warn("no Claude config dir, so the status file is not written", "err", err)
	} else {
		client.Status = status.New(path, cfg.Machine, cfg.URL, version, log)
	}
	if dir, err := rules.Dir(); err != nil {
		log.Warn("no Claude config dir, so hubd's instructions are not written", "err", err)
	} else {
		mirror := rules.New(cfg.URL, cfg.Machine, h, dir, log)
		if err := mirror.Restore(); err != nil {
			log.Warn("restore the instructions from the manifest", "err", err)
		}
		client.Rules = mirror.Refresh
	}
	watchErr := make(chan error, 1)
	go func() { watchErr <- tracker.Watch(ctx, client, sessions.DefaultIntervals) }()
	go usage.Watch(ctx, h, client.Usage, tracker.AnyLive, probe(h.Dir), log, usage.DefaultIntervals)
	go rewrap(ctx, h, log)
	kick := make(chan struct{}, 1)
	go autoUpdate(ctx, h, cfg.Channel, kick, log)

	log.Info("hubridge started", "version", version, "channel", cfg.Channel, "home", h.Dir, "projects", projects, "machine", cfg.Machine)
	if err = client.Run(ctx); errors.Is(err, wsclient.ErrRefused) {
		log.Error("not reconnecting until the daemon is updated")
		kick <- struct{}{}
		select {
		case <-ctx.Done():
			err = nil
			client.Status.Offline("stopped")
		case err = <-watchErr:
			return err
		}
	}
	stop()
	return errors.Join(err, <-watchErr)
}

func autoUpdate(ctx context.Context, h *home.Home, channel string, kick <-chan struct{}, log *slog.Logger) {
	if version == "dev" || os.Getenv("HUBRIDGE_NO_AUTOUPDATE") == "1" {
		return
	}
	update.New(h, version).Watch(ctx, channel, 6*time.Hour, kick, log)
}

func updateCmd(args []string) error {
	var to string
	h, err := parse("update", args, flag.ExitOnError, true, func(fs *flag.FlagSet) {
		fs.StringVar(&to, "version", "", "bundled, latest or a release tag, saved as the channel")
	})
	if err != nil {
		return err
	}
	cfg, err := h.Config()
	if err != nil {
		return err
	}
	channel := cmp.Or(to, cfg.Channel)
	res, err := update.New(h, version).Run(context.Background(), channel, true)
	if err != nil {
		return err
	}
	if channel != cfg.Channel {
		cfg.Channel = channel
		if err := h.WriteConfig(cfg); err != nil {
			return err
		}
	}
	switch {
	case !res.Changed:
		fmt.Printf("hubridge is up to date (%s, %s)\n", res.From, channel)
		return nil
	case res.Downgrade:
		fmt.Printf("downgraded %s → %s (%s)\n", res.From, res.To, channel)
	default:
		fmt.Printf("updated %s → %s (%s)\n", res.From, res.To, channel)
	}
	if h.Held("supervisor.lock") {
		fmt.Println("the daemon restarts on it within 15 seconds")
	}
	return nil
}

func printVersion(args []string) {
	out := strings.TrimSpace(version + " " + commit)
	if h, err := parse("version", args, flag.ExitOnError, false, nil); err == nil {
		if cfg, err := h.Config(); err == nil {
			out += " (" + cfg.Channel + ")"
		}
	}
	fmt.Println(out)
}

func probe(dir string) usage.Prober {
	return func(ctx context.Context) (protocol.Reading, error) {
		claude, err := proc.Claude()
		if err != nil {
			return protocol.Reading{}, err
		}
		return usage.Probe(ctx, claude, dir)
	}
}

func rewrap(ctx context.Context, h *home.Home, log *slog.Logger) {
	path, err := install.SettingsPath()
	if err != nil {
		return
	}
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		if _, err := os.Stat(h.Path(install.StatusLineFile)); err == nil {
			if again, err := install.WrapStatusLine(path, h.Dir); err != nil {
				log.Warn("re-wrap the status line", "err", err)
			} else if again {
				log.Info("the status line was rewritten by another tool; wrapped it again")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func doctorCmd(args []string) int {
	var projects string
	var days int
	h, _ := parse("doctor", args, flag.ExitOnError, false, func(fs *flag.FlagSet) {
		fs.StringVar(&projects, "projects", projectsDir(), "Claude Code transcripts root")
		fs.IntVar(&days, "days", 7, "how many days of transcripts to read")
	})
	rep := doctor.Run(context.Background(), projects, days)
	doctor.CheckRules(rep, h.Dir)
	doctor.CheckStatus(rep, time.Now())
	rep.Write(os.Stdout)
	switch rep.Worst() {
	case doctor.Fail:
		return 2
	case doctor.Drift:
		return 1
	}
	return 0
}

func projectsDir() string {
	dir, _ := install.ClaudeDir()
	return filepath.Join(dir, "projects")
}
