# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/kloudkit/hubridge/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/kloudkit/hubridge/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/kloudkit/hubridge/releases/tag/v0.1.0
