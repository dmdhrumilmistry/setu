# Managing setu with MDM

setu reads a **managed policy** that an organization deploys through its MDM
(Intune, Jamf, Kandji, Mosyle, VaanarSena, Workspace ONE, ...), Group Policy, or
configuration management (Ansible, Puppet, Chef, Salt). The policy restricts
what users can share and forces audit logging to your OpenTelemetry collector.
Users see the effective policy with `setu policy`.

| OS | Where setu reads it (first found wins) | Use |
| --- | --- | --- |
| Windows | `HKLM\SOFTWARE\Policies\setu` | [`windows/setu.admx`](windows/setu.admx) (Group Policy, Intune, VaanarSena), or [`windows/setu-policy.reg`](windows/setu-policy.reg) |
| Windows | `%ProgramData%\setu\policy.json` (owned by Administrators or SYSTEM) | a file deployed by a script |
| macOS | `/Library/Managed Preferences/io.github.dmdhrumilmistry.setu.plist` | [`macos/setu.mobileconfig`](macos/setu.mobileconfig), installed as a **device** profile |
| macOS | `/Library/Application Support/setu/policy.json` (root-owned) | a file deployed by a script |
| Linux | `/etc/setu/policy.json` (root-owned, not group/world writable) | [`linux/install-policy.sh`](linux/install-policy.sh) |

All the examples here hold the same policy as
[`policy.example.json`](policy.example.json), and a test keeps them in sync.

## Settings

Every setting is optional. Without a policy, setu behaves as it does
unmanaged.

| Key | Type | Effect |
| --- | --- | --- |
| `DisableShare` | bool | `setu share` is refused. |
| `DisableService` | bool | `setu service` cannot add profiles, and a running service stops serving. |
| `DisableManual` | bool | `setu share --manual` is refused, so all signaling uses the allowed relays. |
| `DisableDesktop` | bool | `setu desktop` (remote desktop) is refused. |
| `MaxIdleTime` | duration | Control clients are disconnected after this long without input (watching is not input; view-only clients are not affected). Without `--idle-timeout`, shares and profiles get this value; turning it off or a longer value is refused. |
| `DisableFileTransfer` | bool | Clients cannot upload files to this device (shares, remote desktop and service profiles). |
| `DisableAudio` | bool | Remote desktop cannot share this device's sound: `setu desktop --audio` is refused, and service profiles with sound share only the screen. |
| `RequirePassword` | bool | Every share and profile needs a password. `setu share` prompts even without `--password`. |
| `MinPasswordLength` | int | Minimum password length. setu's own floors (8 for shares, 12 for profiles) still apply. |
| `RequirePersonalKeys` | bool | Service profiles cannot have a shared control link: each person gets their own key, so every audited command names a person. |
| `MaxClients` | int | Cap on concurrent clients. An unset `--max-clients` becomes the cap, and a higher explicit value is refused. |
| `MaxShareDuration` | duration | `setu share` must expire within this time. Without `--expire`, it gets this value. |
| `MaxKeyValidity` | duration | Service keys must expire within this time. Profiles that exceed it are not served until their keys are rotated. |
| `PasswordEveryJoin` | bool | Hosts ask for the password on every join. By default setu remembers a device by its device key once it has given the password (`setu share --password` and password-mode service profiles); with this set, no device is remembered. |
| `RequireDevicePairing` | bool | Service profiles must admit only paired devices (`setu service add --auth device`, the default): each new device enters a one-time code from `setu service pair` once and is then recognised by its device key. Cannot be combined with `RequirePassword`. |
| `MaxPairingTime` | duration | Pairing codes must expire within this time. |
| `MaxDevicesPerProfile` | int | A service profile may admit at most this many devices. |
| `MaxDeviceIdle` | duration | Service profiles forget a paired or remembered device that has not connected for this long, whatever the profile says. |
| `DisableUpdate` | bool | The organization updates setu itself: `setu upgrade` is refused and the service never installs updates. Cannot be combined with `AutoUpdate` or `UpdateVersion`. |
| `AutoUpdate` | bool | The always-on service installs updates automatically. They are on by default; this stops users turning them off with `setu upgrade auto off`. |
| `UpdateVersion` | string | Pin the release (for example `v1.4.0`) that the service and `setu upgrade` install. It may be older than the installed one, to roll back. |
| `UpdateURL` | string | An `https://` release mirror with GitHub's layout (`<base>/latest`, `<base>/download/<version>/<file>`). |
| `AllowedCommands` | list | Only these programs may be shared. A bare name (`claude`) matches the program name on any OS; an absolute path matches exactly. Remote desktop counts as `@desktop`, so a list without it also refuses `setu desktop`. |
| `AllowedRelays` | list | Signaling may only use these relays, and they become the default. |
| `AllowedWebURLs` | list | Invite links may only point at these web client base URLs, for example a self-hosted copy. |
| `AuditEndpoint` | string | Forces audit logging on, to this OTLP/HTTP collector. Users cannot turn it off, switch it to connections only, or redirect it. |
| `AuditHeaders` | map | Headers sent to the collector, for example `Authorization`. |
| `AuditCAFile`, `AuditCertFile`, `AuditKeyFile` | path | Pin the collector's CA, and use mutual TLS. |
| `AuditRequired` | bool | Fail closed: nothing is shared unless the collector accepts a test record. |
| `AuditInput` | `lines` / `off` | Record commands, or connections only. |
| `AuditInsecure` | bool | Allow plain `http://` to a remote collector. |
| `AuditRedact` | list | Extra regular expressions to redact from recorded commands. |
| `ResourceAttributes` | map | Added to every audit record, for example an asset tag or team, so records join your inventory. setu also adds `host.id` (machine-id, IOPlatformUUID or MachineGuid), `setu.policy.source` and `setu.policy.hash`. |

Durations are Go-style strings (`30m`, `8h`, `720h`) or a number of seconds.

## Guarantees and limits

* **Fail closed.** If a policy source exists but setu cannot use it, setu
  refuses to run rather than ignore the policy. That covers a source that is
  unreadable, malformed, uses an unknown key (a typo such as `DisableShre`
  must not silently allow sharing), or could have been written by a
  non-administrator.
* **Enforced where it runs.** `setu share` checks the policy at start.
  `setu service` checks it when profiles are created, and the running
  service re-checks every profile every 10 seconds before serving it, because
  users can edit their own service configuration file. A policy pushed to a
  device takes effect within seconds, without a restart.
* **A guardrail, not a sandbox.** A local administrator can remove the policy,
  and a user can run a different build of setu. Pair it with egress controls
  if that matters, for example allowing only your relays and collector.
* **Collector credentials are visible to the device user.** setu runs as the
  user, so anything it can read (policy headers, `HKLM\Software\Policies`,
  certificate files), the user can read too. Give the collector an
  **ingest-only** credential, ideally a per-device client certificate (mutual
  TLS), so it cannot read or delete logs.

## Microsoft Intune

1. **Devices > Configuration > Import ADMX**: upload `windows/setu.admx` and
   `windows/en-US/setu.adml`.
2. Create an **Administrative templates (imported)** profile, then configure
   the settings under **setu (terminal sharing)**.

Intune can also push `setu.mobileconfig` to Macs, as a custom macOS
configuration profile at device level.

## Group Policy

Copy `setu.admx` to `\\<domain>\SYSVOL\<domain>\Policies\PolicyDefinitions\`
and `setu.adml` to its `en-US` folder. The settings then appear under
**Computer Configuration > Administrative Templates > setu (terminal sharing)**.

## Jamf Pro, Kandji, Mosyle

Upload `macos/setu.mobileconfig` as a custom profile with **device level**
(System) scope. In Jamf's **Application & Custom Settings** you can instead
set the preference domain to `io.github.dmdhrumilmistry.setu`, and paste the
keys from `policy.example.json`.

## VaanarSena

```sh
vsctl apply -f deploy/mdm/vaanarsena/setu.yaml
```

[`vaanarsena/setu.yaml`](vaanarsena/setu.yaml) applies the policy on every
platform VaanarSena manages:

* **macOS:** an Apple custom payload.
* **Windows:** it ingests the ADMX, then sets each policy by OMA-URI
  (`./Device/Vendor/MSFT/Policy/Config/setu~Policy~Setu/<Policy>`).
* **Linux:** a Blueprint onboarding script for corporate Linux devices
  installs `/etc/setu/policy.json`. That script runs once, when a device
  enters scope, so re-run it after you change the policy.

It is validated with VaanarSena's own manifest validator.

## Linux configuration management

```sh
sudo sh deploy/mdm/linux/install-policy.sh deploy/mdm/policy.example.json
```

With Ansible, use `ansible.builtin.copy` with `owner: root`, `group: root` and
`mode: "0644"` to `/etc/setu/policy.json`.

## Checking a device

```console
$ setu policy
source: /etc/setu/policy.json
hash:   3f1c...

{ "RequirePassword": true, ... }        # header values are shown as [hidden]
$ setu share -- htop
setu: blocked by your organization's policy (AllowedCommands): "/usr/bin/htop" may not be shared (allowed: claude, bash, pwsh)
```

Every audit record carries `setu.policy.hash`, so your SIEM can show which
policy version was in force for each session.
