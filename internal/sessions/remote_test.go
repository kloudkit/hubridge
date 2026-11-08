package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
)

func initRepo(t *testing.T, dir, origin string) {
	t.Helper()
	os.RemoveAll(filepath.Join(dir, ".git"))
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if origin == "" {
		return
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{origin}}); err != nil {
		t.Fatal(err)
	}
}

func TestOriginIsReadWithoutCredentials(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir, "https://user:secret@github.com/kloudkit/ws-cli.git")
	if got := originURL(dir); got != "https://github.com/kloudkit/ws-cli.git" {
		t.Fatalf("origin %q", got)
	}
	scp := t.TempDir()
	initRepo(t, scp, "git@github.com:kloudkit/ws-cli.git")
	if got := originURL(scp); got != "git@github.com:kloudkit/ws-cli.git" {
		t.Fatalf("scp origin %q", got)
	}
	none := t.TempDir()
	initRepo(t, none, "")
	if got := originURL(none); got != "" {
		t.Fatalf("no origin gave %q", got)
	}
	if got := originURL(t.TempDir()); got != "" {
		t.Fatalf("not a repo gave %q", got)
	}
}

func TestOriginOfAWorktreeComesFromTheMainRepo(t *testing.T) {
	main := t.TempDir()
	initRepo(t, main, "git@github.com:kloudkit/hubridge.git")
	admin := filepath.Join(main, ".git", "worktrees", "wt")
	os.MkdirAll(admin, 0o700)
	os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o600)
	os.WriteFile(filepath.Join(admin, "HEAD"), []byte("ref: refs/heads/wt\n"), 0o600)
	wt := t.TempDir()
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o600)
	if got := originURL(wt); got != "git@github.com:kloudkit/hubridge.git" {
		t.Fatalf("worktree origin %q", got)
	}
}

func TestSummaryCarriesRootAndRemote(t *testing.T) {
	fx := setup(t)
	initRepo(t, fx.repo, "https://github.com/kloudkit/hubridge")
	var out sink
	fx.tracker.Emit(&out, fx.tracker.Scan())
	found := false
	for _, s := range out.sessions {
		if s.ID == idWorking {
			found = true
			if s.Root != fx.repo || s.Remote != "https://github.com/kloudkit/hubridge" {
				t.Fatalf("working session root %q remote %q", s.Root, s.Remote)
			}
		}
		if s.ID == idEnded && s.Remote != "" {
			t.Fatalf("a session outside git got remote %q", s.Remote)
		}
	}
	if !found {
		t.Fatal("the working session was not emitted")
	}
}
