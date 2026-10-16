# Audit logging

setu can keep an audit trail of every shared session: who connected, from
where and on what device, and every command they typed. Records are exported as
[OpenTelemetry](https://opentelemetry.io) log records over OTLP/HTTP, so they go
to any collector and from there to Loki, Elasticsearch, Splunk, Datadog or any
other OTLP-capable backend.

## Turn it on

For one share:

```sh
export OTEL_EXPORTER_OTLP_HEADERS='Authorization=Bearer%20<ingest-token>'
setu share --audit-endpoint https://otel.example.com:4318 --audit-required -- claude
```

For the [always-on service](service.md):

```sh
setu service audit --endpoint https://otel.example.com:4318
```

On managed devices, an [MDM policy](mdm.md) can force auditing on and users
cannot turn it off or redirect it.

| Flag (`setu share`) | Meaning |
| --- | --- |
| `--audit` | Enable, using `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` or `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| `--audit-endpoint URL` | Collector URL; `/v1/logs` is added to a bare origin. Implies `--audit`. |
| `--audit-required` | Fail closed: refuse to share unless the collector accepts a test record. |
| `--audit-ca`, `--audit-cert`, `--audit-key` | Pin the collector's CA; use mutual TLS. |
| `--audit-insecure` | Allow plain `http://` to a collector that is not on this machine. |
| `--audit-input lines` or `off` | Record commands, or connections only. |
| `--audit-redact REGEX` | Extra pattern to redact from recorded commands (repeatable). |

Credentials (such as `Authorization`) are read only from
`OTEL_EXPORTER_OTLP_HEADERS` or `OTEL_EXPORTER_OTLP_LOGS_HEADERS`, never from
flags (which other users can see in the process list), and are removed from the
environment before the shared command starts.

## What a command record contains

Every `setu.command` record answers who ran it, from where, on what device,
when, and what it was:

| | Attributes |
| --- | --- |
| **Who** | `setu.actor` (a readable summary), `setu.actor.name` (the personal key's owner, `host-terminal`, or `shared-control-link`), `setu.client.id`, `setu.client.role`, `setu.client.verification_code`, `setu.client.dtls_fingerprint`, and the account and machine the command ran as: `user.name`, `host.name`, `host.id` |
| **From where** | `client.address` and `client.port` (the selected network path), `network.transport`, `setu.client.candidate_type`, `setu.client.public_addresses` (the client's public IP and port behind its router, which stays correct when the path goes through a TURN relay) and `setu.client.local_addresses` |
| **On what device** | `setu.client.device.*`: `app`, `version`, `id`, `os`, `os_version`, `arch`, `model`, `hostname`, `user`, `language`, `timezone`, `screen`, `mobile`, and `user_agent.original`; all flagged `setu.client.device.self_reported=true` |
| **When** | the record timestamp (when Enter was pressed), `setu.command.submitted_at` and `setu.command.started_at` (first keystroke), RFC 3339 UTC |
| **What** | the record body and `setu.command.text`, plus `setu.session.command` (the program it was typed into) |

An example `setu.actor`:

```
alice (client #2 from 203.0.113.7:51234, device alice-laptop (linux/amd64, setu-cli), verification code 769 168)
```

`setu.actor.name` can name a person only when each person has a
[personal key](service.md#concepts). With a shared link every holder looks the
same.

## All events

| Event (`event.name`) | When |
| --- | --- |
| `setu.session.start`, `setu.session.end` | A share starts or ends (command, password on or off, exit code). |
| `setu.client.connect`, `setu.client.disconnect` | A client joins or leaves. |
| `setu.client.rejected`, `setu.client.auth_failure`, `setu.client.approval` | Admission decisions. |
| `setu.command` | A submitted command line. |
| `setu.input.control` | ^C, ^D or ^Z. |
| `setu.desktop.clipboard` | Clipboard text crossed between a client and the host ([remote desktop](desktop.md)): direction (`setu.clipboard.direction` `to_host` or `to_client`) and size (`setu.clipboard.bytes`), never the text. |
| `setu.desktop.display` | A client switched the shared display. |
| `setu.desktop.input` | When a control client leaves: how many clicks, key presses, scrolls and typed characters it sent. Keystrokes themselves are not recorded. |
| `setu.audit.start` | The startup probe sent by `--audit-required`. |

Every record carries `setu.session.id`, and `setu.session.start` records
whether the session shares a `terminal` or a `desktop` (`setu.session.kind`);
service records also carry
`setu.profile`. The resource identifies the host (`service.name=setu`,
`service.version`, `host.name`, `host.id`, `user.name`) and, on managed devices,
`setu.policy.source`, `setu.policy.hash` and any attributes from the policy.

## How commands are recorded, and the limits

setu sees keystrokes, not what the shell finally ran. It rebuilds each line the
way a line editor would (backspace, Ctrl+U, Ctrl+W, bracketed paste, Enter) and
attributes it to the client that typed it. Lines that used arrow keys, history
or tab completion are flagged `setu.input.edited=true`, because the shell may
have run something slightly different. Text typed but never submitted is
recorded with `setu.input.submitted=false` when the client leaves.

Commands an agent runs by itself are not keystrokes, so setu does not see them.
Claude Code's own OpenTelemetry export covers those.

## Secrets stay out

* On Linux and macOS, a line typed while the terminal has echo off (sudo,
  ssh, other password prompts) is replaced by
  `[REDACTED: typed with terminal echo off]`. Windows ConPTY does not expose
  this state, so there only pattern redaction applies.
* Known secret formats are redacted: AWS keys, GitHub and Slack tokens,
  Anthropic and OpenAI keys, JWTs, bearer tokens, `password=`-style
  assignments, credentials in URLs, private keys and setu invite keys. Add your
  own with `--audit-redact`.
* `--audit-input off` records connections only.

## Delivery guarantees

Records are batched and sent every second through a bounded queue (8192
records; the oldest are dropped if the collector is down for long). The
terminal never waits for the network. Because records leave the machine
within about a second, someone who later gains control of the host cannot erase
what was already exported. Export errors are logged at most once a minute.

## A collector to send to

Any OTLP/HTTP endpoint works. Here is an example configuration for the
[OpenTelemetry Collector](https://opentelemetry.io/docs/collector/)
*contrib* distribution (`otelcol-contrib`) that accepts setu records over TLS
with a bearer token and writes them to a file. Replace the file exporter with
your backend's exporter. Adjust and test it for your environment.

```yaml
extensions:
  bearertokenauth:
    token: ${env:SETU_INGEST_TOKEN}

receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
        tls:
          cert_file: /etc/otelcol/tls/server.crt
          key_file: /etc/otelcol/tls/server.key
        auth:
          authenticator: bearertokenauth

processors:
  batch: {}

exporters:
  file:
    path: /var/log/setu/audit.jsonl

service:
  extensions: [bearertokenauth]
  pipelines:
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [file]
```

Then point setu at it:

```sh
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer%20$SETU_INGEST_TOKEN"
setu share --audit-endpoint https://collector.example.com:4318 --audit-required -- claude
```

For stronger device identity, use mutual TLS instead of a shared token: add
`client_ca_file` under the receiver's `tls`, issue each device a client
certificate, and pass `--audit-cert` and `--audit-key` to setu.

<div class="callout warn">

**Give setu an ingest-only credential.** setu runs as the user, so the user
can read whatever credential setu uses. Make sure it can write logs but not
read or delete them.

</div>
