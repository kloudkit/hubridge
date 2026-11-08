package mask

import (
	"regexp"
	"strings"
)

const Marker = "‹masked›"

const secretKey = `[\w.-]*(?:password|passwd|secret|token|apikey|api_key|auth|credential)[\w.-]*`

var (
	opRef  = regexp.MustCompile(`op://[^\s"'<>]+`)
	pem    = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?(?:-----END [A-Z0-9 ]+-----|$)`)
	jwt    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	urlPw  = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s:/@]+:)[^\s@/]+@`)
	prefix = regexp.MustCompile(`\b(?:ghp_|gho_|ghs_|github_pat_|glpat-|sk_live_|rk_live_|sk-|xox[abpr]-|hubd_)[A-Za-z0-9_-]{8,}|\b(?:AKIA|ASIA)[0-9A-Z]{16}\b|\bAIza[0-9A-Za-z_-]{20,}`)
	scheme = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	flagKV = regexp.MustCompile(`(?i)(--` + secretKey + `)(\s+|=)("[^"]*"|'[^']*'|\S+)`)
	kv     = regexp.MustCompile(`(?i)(["']?` + secretKey + `["']?)(\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&}]+)`)
	run    = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
	plain  = regexp.MustCompile(`^[a-z/_-]+$`)
)

func String(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range opRef.FindAllStringIndex(s, -1) {
		b.WriteString(apply(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(apply(s[last:]))
	return b.String()
}

func apply(s string) string {
	s = pem.ReplaceAllString(s, Marker)
	s = jwt.ReplaceAllString(s, Marker)
	s = urlPw.ReplaceAllString(s, "${1}"+Marker+"@")
	s = prefix.ReplaceAllString(s, Marker)
	s = scheme.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Fields(m)[0] + " " + Marker
	})
	s = flagKV.ReplaceAllString(s, "${1}${2}"+Marker)
	s = kv.ReplaceAllStringFunc(s, func(m string) string {
		g := kv.FindStringSubmatch(m)
		if g[3] == Marker || strings.HasPrefix(g[3], Marker) {
			return m
		}
		return g[1] + g[2] + Marker
	})
	return run.ReplaceAllStringFunc(s, func(m string) string {
		if plain.MatchString(m) {
			return m
		}
		return Marker
	})
}
