# Claude Code from your phone

setu was built for this: start Claude Code (or another agent) on your
workstation, then follow along and answer its questions from your phone.

## The quick way

```sh
cd ~/projects/my-app
setu share --password -- claude
```

Scan the QR code, enter the password, check the verification code, and you see
Claude Code exactly as it looks on your computer. Use the compose box (pencil
key) to write prompts: multi-line text arrives as one paste, so Claude Code
treats it as a single message.

## Keep it running while you are away

Run the share in the background so you can close the terminal:

```sh
setu share -d --password --expire 8h -- claude
setu ps          # later: is it still running?
setu stop <id>   # done for the day
```

Or make it permanent with the [Always-on service](service.md), which restarts
Claude Code if it exits and keeps the same link across reboots:

```sh
setu service add --person me --dir ~/projects/my-app claude -- claude
setu service install
```

## A safer setup

An agent with a shell on your machine is powerful, and a setu link gives that
power to whoever holds it.

* **Always set a password** for anything that lives longer than a few minutes.
* **Prefer short lifetimes**: `--expire 8h` for ad-hoc shares,
  `--valid-for 168h` for service profiles.
* **Use `--max-clients 1`** so a second device cannot join unnoticed.
* **Check the verification code** every time you connect.
* **Run the agent with the permissions you would grant it locally**, for
  example in a container, a separate user account, or with Claude Code's own
  permission settings.
* **Turn on [audit logging](audit.md)** to keep a record of what was typed and
  from where. setu records the prompts and commands people type; Claude Code's
  own OpenTelemetry export records what the agent then ran.

## Tips

* Lost the link? `setu link <id>` for background shares, or
  `setu service link <profile>` for the service.
* The web client keeps the terminal size of your phone; the latest control
  client to resize wins.
* If your phone sleeps, reopen the link: the replay buffer shows you the last
  256 KiB of output.
