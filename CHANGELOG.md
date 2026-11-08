# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.0] - 2026-11-08

### Added

- 🚦 Keep `~/.claude/hubd/status.json` saying whether the daemon is connected to hubd, since when and, when it is not, why, for the hubd footer mod to read; it is rewritten atomically when the daemon starts or stops, on every `welcome`, and when a connection ends or an attempt fails
- 🩺 Report the status file's state, age and reason in `hubridge doctor`

## [0.3.0] - 2026-11-08

### Changed

- 📜 Write only the instructions hubd sends into `~/.claude/rules/hubd/`, and ignore the `builtins` key older hubd versions still send
- 💾 Keep each file's body in `rules.json`, and rewrite any listed file that is missing or changed when the daemon starts, before it reaches hubd; a v0.2.0 manifest is read, and rewritten at the next fetch
- 🔁 Retry a failed fetch, after a connect or on `sync`, from 5 seconds up to every 5 minutes, until it succeeds or the connection closes
- 🩺 Say in `hubridge doctor` whether the instructions are restorable offline

### Removed

- 🔥 The built-in rules shipped in the binary (the estate manual, secrets, shared git checkouts); the operator brings their own instructions through hubd

### Fixed

- 🐛 Keep the files when hubd answers 404, as a reverse proxy does during a deploy, instead of replacing them with the built-ins

## [0.2.0] - 2026-11-08

### Added

- 📜 Mirror hubd's session rules into `~/.claude/rules/hubd/`: three built-ins shipped in the binary (the estate manual, secrets, shared git checkouts) and the claims the operator pins, fetched on start, after every connect and on hubd's `sync`
- 🩺 List the mirrored rules in `hubridge doctor`

### Removed

- 🔥 Reading estate claim files to find a session's topics; hubd now takes them from the files a session reads through its MCP

## [0.1.0] - 2026-11-08

### Added

- 🌉 Report Claude Code sessions, live state and usage to hubd over its client protocol, version 1
- 📦 Install from the build hubd bundles, or from GitHub with `--version latest` or a release tag
- 🔄 Update with `hubridge update`, and in the background on the saved channel
- 🔏 Check every update against the release's signed manifest and the protocol hubd speaks

[Unreleased]: https://github.com/kloudkit/hubridge/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/kloudkit/hubridge/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/kloudkit/hubridge/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/kloudkit/hubridge/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/kloudkit/hubridge/releases/tag/v0.1.0
