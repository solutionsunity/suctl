# suctl

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platforms](https://img.shields.io/badge/platforms-linux%20%C2%B7%20macOS%20%C2%B7%20windows-555)](#install)
[![Release](https://img.shields.io/github/v/release/solutionsunity/suctl?sort=semver)](https://github.com/solutionsunity/suctl/releases)

> **Dead simple. Solid as a mountain.**

suctl is a single-binary server control tool. It opens a REPL that reads your
server's live state, never caches it, and lets you act on what is actually
there — the same loop on every surface: **survey → focus → act**.

**Reality is always the source of truth.** Never store what can be read
directly. Never assume what can be verified. That invariant holds across every
component and every module.

## Install

suctl is a single binary — no runtime, no dependencies.

```sh
# linux · macos · arm
curl -fsSL https://suctl.com/install | sh
```

```powershell
# windows
irm https://suctl.com/install.ps1 | iex
```

Both installers resolve the latest release, verify its sha256, and hand off to
`suctl install` (which owns the on-disk layout). Pin a version with
`SUCTL_VERSION=v0.5.0` or point at a fork with `SUCTL_REPO=owner/name`.

## Upgrade

Once installed, suctl updates itself in place — same resolve-and-verify flow as
the installer, no script needed:

```sh
sudo suctl upgrade        # -y to skip the confirmation prompt
```

It compares the latest release against the running build, and when newer
downloads the matching archive, verifies its checksum, swaps the binary (atomic
on Unix; a self-rename on Windows, with the old image cleared on next run), and
refreshes the bundled modules. Module setup/upgrade hooks run on your next
`suctl` invocation. Honours the same `SUCTL_VERSION` / `SUCTL_REPO` overrides.

## First session

```sh
suctl
```

You land on the home page. On a fresh install nothing is active yet, so it
reads `0 active · M ready` — suctl has *found* modules but started none. Tab to
**inventory**, focus a module (`⏎`), and `activate` it; its surface then
appears as a row on the home page.

Everything is a row: `↑↓` selects, `←→`/`tab` step across a row's fields and
actions, `⏎` enters, `esc` goes back, `Alt+q` quits. Start typing on any survey
to filter rows instantly. The loop is the same on every surface —
**survey → focus → act** — and the full walkthrough lives at
[suctl.com](https://suctl.com).

## Modules

Capabilities are provided by modules, discovered at runtime and activated only
when you ask. Modules can be written in any language that speaks the wire
protocol. The bundled set:

| Module | Purpose |
|---|---|
| `suctl-mod-nginx` | nginx domains, server blocks, SSL status |
| `suctl-mod-certbot` | Let's Encrypt certificate lifecycle |
| `suctl-mod-os` | OS services and units |
| `suctl-mod-fail2ban` | fail2ban jails and filters |
| `suctl-mod-odoo` | Odoo instance management |

The bundled modules are Linux-only today. On macOS and Windows suctl runs and
the REPL works — there are simply no bundled modules to activate until you add
your own.

## Building from source

Requires Go and (on Linux, for journald/CGo) `libsystemd-dev`.

```sh
make all          # host platform → bin/<os>-<arch>/
make check        # vet + Go tests + Python module tests
```

Artifacts land in `bin/<os>-<arch>/`: the `suctl` binary plus a `modules/`
tree. Cross-build by overriding `GOOS`/`GOARCH`, e.g.
`make all GOOS=windows GOARCH=amd64`.

## Documentation

Full documentation — concepts, guides, and module-author reference — lives at
[suctl.com](https://suctl.com).

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) and
[NOTICE](NOTICE). Contributions are accepted under the
[Developer Certificate of Origin](DCO) — sign off your commits with `git commit -s`.
