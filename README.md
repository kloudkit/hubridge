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
