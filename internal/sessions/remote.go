package sessions

import (
	"net/url"
	"strings"

	"github.com/go-git/go-git/v5"
)

func originURL(root string) string {
	repo, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return ""
	}
	origin, err := repo.Remote("origin")
	if err != nil || len(origin.Config().URLs) == 0 {
		return ""
	}
	return withoutCredentials(origin.Config().URLs[0])
}

func withoutCredentials(remote string) string {
	if !strings.Contains(remote, "://") {
		return remote
	}
	u, err := url.Parse(remote)
	if err != nil || u.User == nil {
		return remote
	}
	u.User = nil
	return u.String()
}
