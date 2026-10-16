<div class="hero">

# Your terminal, anywhere

<p class="lead">setu shares a terminal on your machine with a browser or another terminal, peer-to-peer and end-to-end encrypted, with no server of your own. Built for driving Claude Code and other AI agents from your phone.</p>

</div>

<div class="actions">
<a class="button primary" href="quickstart.html">Quick start</a>
<a class="button" href="install.html">Install</a>
<a class="button" href="https://github.com/dmdhrumilmistry/setu">View on GitHub</a>
</div>

```sh
go install github.com/dmdhrumilmistry/setu/cmd/setu@latest
setu share --password -- claude
```

setu prints a link and a QR code. Open it on your phone (or any browser), enter
the password, check that the verification code matches, and you are in the same
terminal session as your computer.

*setu* means "bridge" in Sanskrit.

## What you can do

<div class="cards">
<a class="card" href="sharing.html"><h3>Share a terminal</h3><p>Any command or your shell, with passwords, view-only links, single-use links, expiry and approval prompts.</p></a>
<a class="card" href="desktop.html"><h3>Remote desktop</h3><p>Share the whole screen, mouse, keyboard and clipboard of a Windows, Mac or Linux computer with any browser.</p></a>
<a class="card" href="joining.html"><h3>Join from anywhere</h3><p>A mobile-friendly web client with a key bar and a compose box, or <code>setu join</code> from another terminal.</p></a>
<a class="card" href="claude-code.html"><h3>Drive Claude Code</h3><p>Start an agent on your workstation and steer it from your phone, safely.</p></a>
<a class="card" href="service.html"><h3>Always-on service</h3><p>Persistent links that survive reboots, a personal key per person, autostart on Linux, macOS and Windows.</p></a>
<a class="card" href="audit.html"><h3>Audit logging</h3><p>Who ran which command, when, from where and on what device, exported over OpenTelemetry.</p></a>
<a class="card" href="mdm.html"><h3>Managed devices</h3><p>Org policy through Intune, Jamf, Kandji, VaanarSena or Group Policy, enforced on every device.</p></a>
</div>

## How it works

```
 setu share --(encrypted hello / offer / answer over nostr relays)-- browser / setu join
      |                                                                   |
      +------------- WebRTC data channel (DTLS, peer-to-peer) -------------+
```

1. `setu share` starts your command in a pseudo terminal and creates a random
   256-bit secret. The secret lives only in the link's `#fragment`, which
   browsers never send to any server.
2. The joiner and the host exchange a few small, encrypted messages through
   public [nostr](https://nostr.com) relays to set up a WebRTC connection.
   Relays only ever see ciphertext. With `--manual` you copy and paste these
   messages instead and no relay is involved.
3. The peers connect directly. Terminal bytes travel only over that DTLS
   encrypted channel. Both sides show a 6-digit verification code; if they
   match, nobody is in the middle.

The full protocol is in [Design and protocol](DESIGN.md).

## Platforms

| | Share (host) | Join |
| --- | --- | --- |
| Linux | yes | terminal or browser |
| macOS | yes | terminal or browser |
| Windows 10 1809+ / Server 2019+ | yes (ConPTY; default shell PowerShell) | terminal or browser |
| Phones and tablets | no | browser |

[Remote desktop](desktop.md) hosting (`setu desktop`) works on Windows, macOS 13+ and Linux or BSD with an X11 session.

## Where to next

* New here? Follow the [Quick start](quickstart.md).
* Setting setu up for a team? Read [Always-on service](service.md),
  [Audit logging](audit.md) and [MDM and managed policy](mdm.md).
* Want to know what protects you? See the [Security model](security.md).
