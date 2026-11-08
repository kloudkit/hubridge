package mask

import (
	"strings"
	"testing"
)

const M = Marker

func TestRules(t *testing.T) {
	cases := []struct{ rule, in, want string }{
		{"github ghp_", "use ghp_abcdEFGH1234abcdEFGH1234abcdEFGH1234 now", "use " + M + " now"},
		{"github gho_", "gho_abcdefgh12345678", M},
		{"github ghs_", "ghs_abcdefgh12345678", M},
		{"github_pat_", "github_pat_11ABCDEFG0abcdefgh_xyz", M},
		{"anthropic sk-", "key sk-ant-api03-abcdefgh", "key " + M},
		{"slack xox", "xoxb-1234-5678-abcdefgh", M},
		{"gitlab glpat-", "glpat-abcdefgh12345678", M},
		{"aws AKIA", "id AKIAIOSFODNN7EXAMPLE end", "id " + M + " end"},
		{"aws ASIA", "ASIAIOSFODNN7EXAMPLE", M},
		{"google AIza", "AIzaSyA-abcdefghijklmnopqrstuvwxyz12345", M},
		{"stripe sk_live_", "sk_live_abcdefgh1234", M},
		{"stripe rk_live_", "rk_live_abcdefgh1234", M},
		{"hubd token", "hubd_abcdefgh12345678", M},
		{"jwt", "jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl ok", "jwt " + M + " ok"},
		{"pem", "a -----BEGIN RSA PRIVATE KEY-----\nMIIE\nabc\n-----END RSA PRIVATE KEY----- b", "a " + M + " b"},
		{"pem unterminated", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbn", M},
		{"key=value", "DB_PASSWORD=hunter2 next", "DB_PASSWORD=" + M + " next"},
		{"key: value", "client_secret: s3cr3t", "client_secret: " + M},
		{"json key", `{"api_key": "abc"}`, `{"api_key": ` + M + `}`},
		{"--key value", "cli --auth-token abc123 --verbose", "cli --auth-token " + M + " --verbose"},
		{"--key=value", "cli --credential=abc", "cli --credential=" + M},
		{"query param", "https://x.io/cb?token=abc&next=1", "https://x.io/cb?token=" + M + "&next=1"},
		{"bearer", "Authorization: Bearer abcdefgh12345", "Authorization: " + M + " " + M},
		{"url password", "git clone https://bob:pa55w0rd@git.example.com/r.git", "git clone https://bob:" + M + "@git.example.com/r.git"},
		{"long base64", "blob QmFzZTY0IGVuY29kZWQgc2VjcmV0IHZhbHVlIGhlcmU= end", "blob " + M + " end"},
		{"long hex", "sha 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "sha " + M},
		{"op reference kept", "read op://Private/github-token-1234567890abcdefghijklmnop/credential", "read op://Private/github-token-1234567890abcdefghijklmnop/credential"},
		{"op reference as value kept", "PASSWORD=op://vault/item/password", "PASSWORD=op://vault/item/password"},
		{"plain path kept", "edit /workspace/kloudkit/hubridge/internal/sessions", "edit /workspace/kloudkit/hubridge/internal/sessions"},
		{"prose kept", "Fix the sk-learn import in the dashboard", "Fix the sk-learn import in the dashboard"},
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.rule, got, c.want)
		}
	}
}

func TestNothingLeaks(t *testing.T) {
	secrets := []string{"ghp_abcdEFGH1234abcdEFGH1234abcdEFGH1234", "hunter2", "pa55w0rd", "AKIAIOSFODNN7EXAMPLE"}
	in := "ghp_abcdEFGH1234abcdEFGH1234abcdEFGH1234 password=hunter2 https://a:pa55w0rd@h AKIAIOSFODNN7EXAMPLE"
	out := String(in)
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("%q leaked in %q", s, out)
		}
	}
}
