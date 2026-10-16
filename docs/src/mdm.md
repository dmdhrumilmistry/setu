# MDM and managed policy

Organizations can manage setu through their MDM (Microsoft Intune, Jamf Pro,
Kandji, Mosyle, VaanarSena, Workspace ONE, ...), Group Policy, or configuration
management (Ansible, Puppet, Chef, Salt). An administrator deploys a **managed
policy**; setu enforces it on every device. Users see the policy in force with:

```sh
setu policy
```

Ready-made templates are in
[`deploy/mdm`](https://github.com/dmdhrumilmistry/setu/tree/main/deploy/mdm).

## What a policy can do

* turn off ad-hoc sharing, the background service, or manual mode;
* require passwords (with a minimum length) and personal keys;
* cap concurrent clients, share duration and service key lifetime;
* allow only certain programs, relays and web client URLs;
* force audit logging to the organization's collector, and add device
  attributes (such as an asset tag) to every record.

## Where setu reads it

The first source found wins. All of them are writable only by administrators.

| OS | Source | Deploy with |
| --- | --- | --- |
| Windows | registry `HKLM\SOFTWARE\Policies\setu` | the ADMX template (Group Policy, Intune, VaanarSena) or the `.reg` file |
| Windows | `%ProgramData%\setu\policy.json`, owned by Administrators or SYSTEM | a script |
| macOS | managed preferences for `io.github.dmdhrumilmistry.setu` | the `.mobileconfig` profile, installed at **device** level |
| macOS | `/Library/Application Support/setu/policy.json`, root-owned | a script |
| Linux | `/etc/setu/policy.json`, root-owned, not group or world writable | `install-policy.sh`, Ansible, Puppet, ... |

## Settings

Every setting is optional; without a policy setu behaves as unmanaged.

| Key | Type | Effect |
| --- | --- | --- |
| `DisableShare` | bool | `setu share` is refused. |
| `DisableService` | bool | `setu service` cannot add profiles, and a running service stops serving. |
| `DisableManual` | bool | `setu share --manual` is refused, so all signaling uses the allowed relays. |
| `DisableDesktop` | bool | `setu desktop` (remote desktop) is refused. |
| `RequirePassword` | bool | Every share and profile needs a password; `setu share` prompts even without `--password`. |
| `MinPasswordLength` | int | Minimum password length (setu's own floors of 8 and 12 still apply). |
| `RequirePersonalKeys` | bool | Service profiles cannot have a shared control link, so every audited command names a person. |
| `MaxClients` | int | Cap on concurrent clients. An unset `--max-clients` becomes the cap; a larger explicit value is refused. |
| `MaxShareDuration` | duration | `setu share` must expire within this time; without `--expire` it gets this value. |
| `MaxKeyValidity` | duration | Service keys must expire within this time; profiles that exceed it are not served until rotated. |
| `AllowedCommands` | list | Only these programs may be shared. A bare name (`claude`) matches the program name on any OS; an absolute path matches exactly. Remote desktop counts as `@desktop`, so a list without it also refuses `setu desktop`. |
| `AllowedRelays` | list | Only these relays may be used; they also become the default. |
| `AllowedWebURLs` | list | Invite links may only point at these web client URLs. |
| `AuditEndpoint` | string | Force audit logging on, to this OTLP/HTTP collector. Users cannot turn it off, reduce it or redirect it. |
| `AuditHeaders` | map | Headers for the collector, such as `Authorization`. |
| `AuditCAFile`, `AuditCertFile`, `AuditKeyFile` | path | Pin the collector's CA; mutual TLS. |
| `AuditRequired` | bool | Fail closed: nothing is shared unless the collector accepts a test record. |
| `AuditInput` | `lines` or `off` | Record commands, or connections only. |
| `AuditInsecure` | bool | Allow plain `http://` to a remote collector. |
| `AuditRedact` | list | Extra regular expressions to redact. |
| `ResourceAttributes` | map | Added to every audit record (for example `device.asset_tag`). |

Durations are written like `30m`, `8h` or `720h`, or as a number of seconds.
Example policy:

```json
{
  "DisableManual": true,
  "RequirePassword": true,
  "MinPasswordLength": 14,
  "RequirePersonalKeys": true,
  "MaxClients": 2,
  "MaxShareDuration": "8h",
  "MaxKeyValidity": "720h",
  "AllowedCommands": ["claude", "bash", "pwsh"],
  "AuditEndpoint": "https://otel.example.com:4318/v1/logs",
  "AuditHeaders": {"Authorization": "Bearer REPLACE_WITH_INGEST_ONLY_TOKEN"},
  "AuditRequired": true,
  "ResourceAttributes": {"deployment.environment": "corp"}
}
```

## Deploy it

### Microsoft Intune

1. In **Devices > Configuration > Import ADMX**, upload
   [`setu.admx`](https://github.com/dmdhrumilmistry/setu/blob/main/deploy/mdm/windows/setu.admx)
   and
   [`en-US/setu.adml`](https://github.com/dmdhrumilmistry/setu/blob/main/deploy/mdm/windows/en-US/setu.adml).
2. Create an **Administrative templates (imported)** profile and configure the
   settings under **setu (terminal sharing)**.

For Macs, upload the `.mobileconfig` as a custom device-level profile.

### Group Policy

Copy `setu.admx` to `\\<domain>\SYSVOL\<domain>\Policies\PolicyDefinitions\`
and `setu.adml` to its `en-US` folder. The settings appear under
**Computer Configuration > Administrative Templates > setu (terminal sharing)**.
To test on one machine, import
[`setu-policy.reg`](https://github.com/dmdhrumilmistry/setu/blob/main/deploy/mdm/windows/setu-policy.reg)
as an administrator.

### Jamf Pro, Kandji, Mosyle

Upload
[`setu.mobileconfig`](https://github.com/dmdhrumilmistry/setu/blob/main/deploy/mdm/macos/setu.mobileconfig)
as a custom profile with device-level (System) scope. In Jamf's
**Application & Custom Settings** you can instead set the preference domain to
`io.github.dmdhrumilmistry.setu` and paste the keys from the example policy.

### VaanarSena

```sh
vsctl apply -f deploy/mdm/vaanarsena/setu.yaml
```

The [manifest](https://github.com/dmdhrumilmistry/setu/blob/main/deploy/mdm/vaanarsena/setu.yaml)
applies the policy on macOS (custom payload), Windows (ADMX ingestion plus
OMA-URI settings) and Linux (an onboarding script that installs
`/etc/setu/policy.json`; it runs once per device, so re-run it after changing
the policy).

### Linux configuration management

```sh
sudo sh deploy/mdm/linux/install-policy.sh policy.json
```

With Ansible, copy the file to `/etc/setu/policy.json` with `owner: root`,
`group: root` and `mode: "0644"`.

## Guarantees and limits

* **Fail closed.** If a policy source exists but is unreadable, malformed, has
  an unknown key (a typo such as `DisableShre` must not silently allow
  sharing), or could have been written by a non-administrator, setu refuses to
  run rather than ignoring it.
* **Enforced where it runs.** `setu share` checks the policy at start; the
  service checks profiles when they are created and re-checks every profile
  every 10 seconds before serving it, because users can edit their own service
  configuration. A policy pushed to a device applies within seconds.
* **A guardrail, not a sandbox.** A local administrator can remove the policy,
  and a user can run a different build of setu. Combine it with egress controls
  (allow only your relays and collector) if that matters.
* **Collector credentials are visible to the device's user**, because setu
  runs as that user. Use an ingest-only credential, ideally per-device mutual
  TLS.

## Checking a device

```console
$ setu policy
source: /etc/setu/policy.json
hash:   7637e6a00b7572df

{
  "DisableManual": true,
  ...
}

$ setu share -- htop
setu: blocked by your organization's policy (AllowedCommands): "htop" may not be shared (allowed: claude, bash, pwsh)
```

Header values are shown as `[hidden]`. Every audit record carries
`setu.policy.hash`, so your SIEM can show which policy version applied to each
session.
