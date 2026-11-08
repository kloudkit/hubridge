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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kloudkit/hubridge/internal/doctor"
	"github.com/kloudkit/hubridge/internal/home"
	"github.com/kloudkit/hubridge/internal/install"
	"github.com/kloudkit/hubridge/internal/proc"
	"github.com/kloudkit/hubridge/internal/protocol"
	"github.com/kloudkit/hubridge/internal/registry"
	"github.com/kloudkit/hubridge/internal/sessions"
	"github.com/kloudkit/hubridge/internal/usage"
	"github.com/kloudkit/hubridge/internal/wsclient"
)

var version, commit = "dev", ""

const help = `usage: hubridge [command] [flags]

commands:
  run          watch transcripts and stream them to hubd (default)
  enroll       --url <hubd> (--code <code> | --token-file <path>) [--machine <name>]
  setup        add the hubd hooks to Claude Code's settings.json and wrap its status line (--remove undoes both)
  hook         session-start | notification | stop, run by Claude Code
  supervise    restart loop for run (--detach to background it)
  mcp-headers  print the Authorization header for Claude Code's MCP headersHelper
  statusline   run by Claude Code as its status line: keep the usage reading, then run the original
  doctor       check the Claude Code formats the daemon reads against the pinned ones (--days 7)
  version      print the version

every command takes --home <dir> (default $HUBD_HOME, else ~/.config/hubd)
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
	case "mcp-headers":
		err = mcpHeaders(args)
	case "statusline":
		os.Exit(statusLine(args))
	case "doctor":
		os.Exit(doctorCmd(args))
	case "version":
		fmt.Println(strings.TrimSpace(version + " " + commit))
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

func flags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, help) }
	return fs, fs.String("home", "", "client home")
}

func openHome(flagValue string) (*home.Home, error) {
	dir, err := home.Resolve(flagValue)
	if err != nil {
		return nil, err
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
	fs, homeFlag := flags("enroll")
	var o install.EnrollOptions
	fs.StringVar(&o.URL, "url", "", "hubd origin")
	fs.StringVar(&o.Code, "code", "", "one-time install code")
	fs.StringVar(&o.TokenFile, "token-file", "", "file holding a token")
	fs.StringVar(&o.Machine, "machine", "", "this machine's name")
	fs.Parse(args)
	h, err := openHome(*homeFlag)
	if err != nil {
		return err
	}
	if err := install.Enroll(h, o); err != nil {
		return err
	}
	fmt.Println("enrolled; token and config are in", h.Dir)
	return nil
}

func setup(args []string) error {
	fs, homeFlag := flags("setup")
	remove := fs.Bool("remove", false, "take the hubd hooks out")
	fs.Parse(args)
	dir, err := home.Resolve(*homeFlag)
	if err != nil {
		return err
	}
	path, err := install.SettingsPath()
	if err != nil {
		return err
	}
	if err := install.Setup(path, dir, *remove); err != nil {
		return err
	}
	fmt.Println("updated", path)
	if *remove {
		if err := install.UnwrapStatusLine(path, dir); err != nil {
			return err
		}
		return install.UnregisterMCP(os.Stdout)
	}
	wrapped, err := install.WrapStatusLine(path, dir)
	if err != nil {
		return err
	}
	if wrapped {
		fmt.Println("wrapped the status line, to read Claude usage")
	}
	h, err := home.Open(dir)
	if err != nil {
		return err
	}
	cfg, _ := h.Config()
	if cfg.URL == "" {
		fmt.Println("no hubd url in config.json yet, so hubd's MCP server was not registered; run enroll, then setup again")
		return nil
	}
	return install.RegisterMCP(dir, cfg.URL, os.Stdout)
}

func statusLine(args []string) int {
	fs, homeFlag := flags("statusline")
	fs.Init("statusline", flag.ContinueOnError)
	if fs.Parse(args) != nil {
		return 0
	}
	dir, err := home.Resolve(*homeFlag)
	if err != nil {
		return 0
	}
	return install.RunStatusLine(context.Background(), &home.Home{Dir: dir}, os.Stdin, os.Stdout, os.Stderr, time.Now())
}

func mcpHeaders(args []string) error {
	fs, homeFlag := flags("mcp-headers")
	fs.Parse(args)
	dir, err := home.Resolve(*homeFlag)
	if err != nil {
		return err
	}
	return install.Headers(&home.Home{Dir: dir}, os.Stdout)
}

func hook(args []string) {
	if len(args) == 0 {
		return
	}
	event := args[0]
	fs, homeFlag := flags("hook")
	fs.Init("hook", flag.ContinueOnError)
	if fs.Parse(args[1:]) != nil {
		return
	}
	h, err := openHome(*homeFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hubridge hook:", err)
		return
	}
	wait := install.StdinWait
	if event == "stop" {
		wait = install.StopWait
	}
	in := install.ReadInputWait(os.Stdin, wait)
	switch event {
	case "session-start":
		err = install.SessionStart(h, in, install.Parent(), proc.Lookup, func() error { return install.Detach(h, self()) })
	case "notification":
		err = install.Notification(h, in, time.Now())
	case "stop":
		err = install.Stop(h, in, time.Now())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hubridge hook:", err)
	}
}

func supervise(args []string) error {
	fs, homeFlag := flags("supervise")
	detach := fs.Bool("detach", false, "run in the background and return")
	fs.Parse(args)
	h, err := openHome(*homeFlag)
	if err != nil {
		return err
	}
	if *detach {
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
	fs, homeFlag := flags("run")
	projects := fs.String("projects", "", "Claude Code transcripts root (default ~/.claude/projects)")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	h, err := openHome(*homeFlag)
	if err != nil {
		return err
	}
	if *projects == "" {
		*projects = projectsDir()
	}
	lock, err := h.Lock()
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

	tracker := sessions.New(*projects, h.Handovers, log)
	tracker.Waiting, tracker.Clear = h.Waiting, h.ClearWaiting
	tracker.Background, tracker.ClearBackground = h.Backgrounds, h.ClearBackground
	tracker.Registry = registry.New(registryDir(*projects))
	if tracker.Registry.Claude == "" {
		log.Warn("claude not found on PATH: live state comes from the registry files alone, with no waiting reasons or background sessions")
	}
	client := wsclient.New(cfg.URL, cfg.Machine, version, cfg.Channel, h, tracker.Snapshot, log)
	watchErr := make(chan error, 1)
	go func() { watchErr <- tracker.Watch(ctx, client, sessions.DefaultIntervals) }()
	go usage.Watch(ctx, h, client.Usage, tracker.AnyLive, probe(h.Dir), log, usage.DefaultIntervals, time.Now)
	go rewrap(ctx, h, log)

	log.Info("hubridge started", "version", version, "home", h.Dir, "projects", *projects, "machine", cfg.Machine)
	err = client.Run(ctx)
	if errors.Is(err, wsclient.ErrRevoked) {
		log.Error(err.Error())
		stop()
		<-watchErr
		return err
	}
	if errors.Is(err, wsclient.ErrRefused) {
		log.Error("not reconnecting until the daemon is updated")
		select {
		case <-ctx.Done():
		case err = <-watchErr:
			return err
		}
		return nil
	}
	stop()
	if werr := <-watchErr; werr != nil {
		return werr
	}
	return err
}

func probe(dir string) usage.Prober {
	return func(ctx context.Context) (protocol.Reading, error) {
		claude, err := proc.Claude()
		if err != nil {
			return protocol.Reading{}, err
		}
		return usage.Probe(ctx, claude, dir, time.Now)
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
	fs, _ := flags("doctor")
	projects := fs.String("projects", "", "Claude Code transcripts root (default ~/.claude/projects)")
	days := fs.Int("days", 7, "how many days of transcripts to read")
	fs.Parse(args)
	if *projects == "" {
		*projects = projectsDir()
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rep := doctor.Run(context.Background(), doctor.Options{
		Projects: *projects, Registry: registry.New(registryDir(*projects)), Days: *days, Now: time.Now, Log: log,
	})
	rep.Write(os.Stdout)
	switch rep.Worst() {
	case doctor.Fail:
		return 2
	case doctor.Drift:
		return 1
	}
	return 0
}

func registryDir(projects string) string { return filepath.Join(filepath.Dir(projects), "sessions") }

func projectsDir() string {
	dir, _ := install.ClaudeDir()
	return filepath.Join(dir, "projects")
}
