# Install

setu is a single static binary. You need it on the machine you share from and
on any machine you want to join from a terminal. Joining from a browser needs
nothing installed.

## One line

Linux and macOS:

```sh
curl -fsSL https://dmdhrumilmistry.github.io/setu/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/dmdhrumilmistry/setu/main/install.ps1 | iex
```

The installer picks the right release for your system, checks it against the
release's `SHA256SUMS` and, where OpenSSL 3 is available, the signature of that
file, makes sure it runs, and installs it without administrator rights: to
`~/.local/bin` (or `/usr/local/bin` when run as root) on Linux and macOS, and
to `%LOCALAPPDATA%\Programs\setu` on Windows, which it adds to your `PATH`.
Because the binary stays writable by you, setu keeps itself up to date (see
[Updates](#updates)). Read the scripts first if you like:
[install.sh](https://github.com/dmdhrumilmistry/setu/blob/main/install.sh),
[install.ps1](https://github.com/dmdhrumilmistry/setu/blob/main/install.ps1).

| Variable | Meaning |
| --- | --- |
| `SETU_VERSION` | Install this release (for example `v0.1.0`) instead of the latest. |
| `SETU_INSTALL_DIR` | Install into this directory. |
| `SETU_BASE_URL` | Download from a release mirror with GitHub's layout. |
| `SETU_REQUIRE_SIGNATURE=1` | (install.sh) Fail instead of warning when the signature cannot be checked. |

## With Go

```sh
go install github.com/dmdhrumilmistry/setu/cmd/setu@latest
```

This needs Go 1.27.2 or newer. The binary lands in `$(go env GOPATH)/bin`
(usually `~/go/bin`); make sure that directory is on your `PATH`.

Check the installation:

```sh
setu version
```

`setu version` prints the version, the commit it was built from and your
platform, for example `setu v0.0.0-... (1a2b3c4d5e6f 2026-10-14T12:00:00Z) linux/amd64`.

## Release binaries

Tagged releases publish binaries for Linux, macOS and Windows (amd64 and
arm64) on the [releases page](https://github.com/dmdhrumilmistry/setu/releases),
named `setu-<version>-<os>-<arch>` (`.exe` on Windows), together with a
`SHA256SUMS` file.

```sh
# Linux / macOS example
curl -LO https://github.com/dmdhrumilmistry/setu/releases/download/<version>/setu-<version>-linux-amd64
curl -LO https://github.com/dmdhrumilmistry/setu/releases/download/<version>/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
chmod +x setu-<version>-linux-amd64
sudo mv setu-<version>-linux-amd64 /usr/local/bin/setu
```

Every release is signed (`SHA256SUMS.sig`, checked by `setu upgrade`) and
carries a build provenance attestation, so you can check that a binary was
built from this repository by its release workflow:

```sh
gh attestation verify setu-<version>-linux-amd64 --repo dmdhrumilmistry/setu
```

On macOS, a downloaded binary may be quarantined; clear it with
`xattr -d com.apple.quarantine /usr/local/bin/setu`.

## From source

```sh
git clone https://github.com/dmdhrumilmistry/setu
cd setu
go build -o setu ./cmd/setu      # setu.exe on Windows
```

Cross-compile with `GOOS` and `GOARCH`, for example
`GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/setu`.

## Updates

setu keeps itself up to date. The always-on service checks for a new release
about every 6 hours and installs it by itself; you can also run
`setu upgrade` at any time. Updating never interrupts anyone:

* The download is checked against the release's `SHA256SUMS` (and, in builds
  that carry the release signing key, against an ed25519 signature of it),
  then run once to make sure it starts and reports the right version.
* Only then is it swapped in, with a single rename: the file is never half
  written, and a failed update leaves the old binary in place.
* Shares that are already running keep the version they started with until
  they end.
* The service switches to the new version only when nobody has been
  connected for two minutes and no session can still be resumed.

`setu upgrade auto off` turns automatic updates off (`on` turns them back on,
`status` shows the state). A development build (built from source) is never
updated automatically; `setu upgrade --force` replaces it with the latest
release. If setu was installed by an administrator in a directory you cannot
write to, they update it. Organizations can pin a version, use an internal
mirror, require automatic updates or turn them off with
[managed policy](mdm.md).

## Platform notes

**Windows.** Hosting uses the ConPTY pseudo console, so it needs Windows 10
1809 / Server 2019 or newer. With no command, setu shares PowerShell 7
(`pwsh`), then Windows PowerShell, then `cmd.exe`. npm shims such as
`claude.cmd` work. The shared command runs in a job object, so it cannot
outlive setu.

**macOS and Linux.** With no command, setu shares `$SHELL` (or `/bin/sh`).

**Networks.** setu needs outbound access to the nostr relays (`wss://`, port
443) and to a STUN server (UDP 19302 for the default Google STUN). On networks
that block peer-to-peer UDP, add a TURN server; see
[Strict networks](sharing.md#strict-networks-stun-and-turn).

## Next

Continue with the [Quick start](quickstart.md).
