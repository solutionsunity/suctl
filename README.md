# suctl

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

## First session

```sh
suctl
```

You land on the home page; the title bar reads the inventory at a glance
(`N active · M ready`). Everything is a row: `↑↓` moves the selection, `←→` or
`tab` step across a row's fields and actions, `⏎` enters, `esc` goes back,
`Alt+q` quits. Start typing on any survey to filter rows instantly.

From there the REPL is one loop, three moments:

- **Survey** — the whole domain at once, read live the moment you enter.
- **Focus** — everything the module knows about one subject, read live.
- **Act** — actions computed from the subject's current state, not a fixed
  menu. A suspended domain offers `unsuspend`; `suspend` is simply absent.

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
