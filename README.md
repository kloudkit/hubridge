# KloudKIT • hubridge

> 🌉 Your bridge to mission control

Install it with the command in hubd's Settings › Access:

```sh
curl -fsSL '<hubd>/install?code=<code>' | sh                          # hubd's bundled build
curl -fsSL '<hubd>/install?code=<code>' | sh -s -- --version latest   # or a tag, such as v0.1.0
```

`hubridge update [--version bundled|latest|<tag>]` updates it and saves the channel. Outside a
pinned tag it also updates itself every 6 hours; `HUBRIDGE_NO_AUTOUPDATE=1` turns that off.
`hubridge help` lists the commands. The protocol it speaks is in
[kloudkit/hubd](https://github.com/kloudkit/hubd/tree/main/protocol).

## Instructions

hubridge writes the instructions the operator keeps in hubd into `~/.claude/rules/hubd/`
(under `CLAUDE_CONFIG_DIR` when it is set), where Claude Code loads them as personal rules. It
writes the files hubd sends and none of its own. It fetches them after every connect and on
hubd's `sync` command, and retries a failed fetch after 5 seconds, then at doubling intervals
up to 5 minutes, until it succeeds or the connection closes. A 404, which a reverse proxy
answers during a deploy, keeps the files as they are.

`rules.json` in its home lists the files hubridge wrote, with their contents. When the daemon
starts, before it reaches hubd, it rewrites any of them that is missing or changed, so a
recreated container gets its instructions back while hubd is down. A new machine gets them at
its first connect. When hubd stops sending a file, hubridge removes it; it removes only files
it wrote and leaves anything else in the folder alone.
