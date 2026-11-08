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

## Session rules

hubridge mirrors hubd's session rules into `~/.claude/rules/hubd/` (under `CLAUDE_CONFIG_DIR`
when it is set), where Claude Code loads them as personal rules: the built-ins `estate.md`,
`secrets.md` and `git.md` unless hubd turns one off, and the files hubd adds. On its first
start the daemon writes the built-ins, so a new machine has them before it reaches hubd; it
then refreshes them after every connect and on hubd's `sync` command. It removes only files it
wrote, which `rules.json` in its home lists, and leaves anything else in the folder alone.
