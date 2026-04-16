# LSS Backup — Threat Model

_Status: v2, 2026-04-16. Scope: lss-backup-cli v2.5+ and management server v1.14+. Shared between both sessions._

---

## What this document is

A one-page written record of **what we defend against, what we don't, and where the assumptions live**. Updated when those change. Not a checklist — a statement of intent future contributors can use to judge whether a proposed feature fits or bypasses the security model.

## What we're defending

1. **Backup data integrity at rest** — the restic/rsync destinations hold the only safe copy. If it's silently deleted or corrupted, the product has failed.
2. **Backup data confidentiality at rest** — restic repos are encrypted by a per-job password the operator chose. Rsync destinations are not encrypted; operators who need encryption must use restic.
3. **Audit trail integrity against accidental or clumsy tampering** — the operational history of what happened on each node must survive crashes, reboots, and upgrades.
4. **Operational signal** — when a backup fails, the node's local state + the management server both know within one scheduled heartbeat cycle.
5. **Attack observability for three specific classes:** source wipe / ransomware encryption, repository tampering (`restic forget`), and unauthorized configuration changes. The anomaly detectors fire on these.

## What we're NOT defending against

These are explicit non-goals for the current version. If any become in-scope, this document gets updated first.

1. **A root-compromised node.** If an attacker has root on a backup node, they can:
   - Stop the daemon and silently skip backups.
   - Forge any audit event with any `actor` string (HMAC chain would close this — parked).
   - Read `secrets.env` (mode 0o600 is the only protection).
   - Overwrite `audit.jsonl` with anything.
   - Spoof heartbeats using the node's PSK.
   The server-side 1-hour stale-gap sweeper and "node went silent" alarm (planned) bound the detection window, but they don't prevent these actions.

2. **A compromised management server.** If the central server is owned, the attacker sees every node's backup metadata, can push tunnel commands to every node, and can mark anomalies as resolved. We do not defend against this.

3. **Weak operator passwords.** Restic repo passwords and the encryption password for `secrets.env` are user-chosen. Weak passwords undermine the entire confidentiality model.

4. **Offline cryptanalysis of a stolen restic repository.** We rely on restic's crypto. Anyone with a copy of the repo and time can attempt to crack the password.

5. **Supply-chain compromise of restic / rsync / Go toolchain / OS.** Out of scope.

6. **Multi-tenancy.** The server is single-tenant. One PSK realm, one admin surface. Multi-customer deployment is not supported.

7. **Real-time alerting.** The SMTP / webhook / escalation stack is deferred. Until it ships, alerting is pull-based (operators check the dashboard) or via healthchecks.io pings.

8. **Forensics under active attack.** The audit log is now integrity-verified via HMAC chain (v2.5.0). Tamper-evident relative to a trusted CLI: if an attacker modifies events on the node before shipping, the chain breaks and the server refuses to advance the ack pointer. Does NOT protect against a compromised CLI binary that computes valid HMACs for forged events — that requires binary attestation (out of scope).

## Trust boundaries

- **Operator machine ⇌ node**: the operator is root on the node. Trust is absolute; no privilege separation.
- **Node ⇌ management server**: PSK (128-hex-char per node, in encrypted secrets.env). AES-256-GCM on every payload. Per-node monotonic seq + server-side UNIQUE constraint for ingest. Tunnel auth via HMAC-SHA256 over a per-node ed25519 key pair.
- **Management server ⇌ operator browser**: session auth, HTTPS. Out of scope for this doc.
- **Node ⇌ backup destination**: for local, filesystem perms. For S3, credentials in secrets.env. For SMB/NFS, credentials in secrets.env. For rsync destinations on the same host, standard Unix perms.

## PSK leak attack tree

_Source: server-side session, merged 2026-04-16._

What the attacker gets with a leaked PSK:

1. **Forge heartbeats** — send fake NodeStatus payloads. Server trusts them (AES-256-GCM decrypts cleanly). Can inject false job states.
2. **Forge audit events** — inject arbitrary audit rows with any actor including `"user:admin"`. Appears legitimate on /audit. Mitigation (planned): HMAC chain. Until shipped, audit is tamper-evident only relative to a trusted CLI.
3. **Open reverse tunnels** — connect to `/ws/ssh-tunnel`, HMAC auth succeeds. Gets a reverse TCP forward on loopback. Still needs the node's SSH private key to do anything useful. Mitigation (shipped v1.12.0): per-UID exponential backoff rate limiter (1s → 10min cap).
4. **Replay old heartbeats** — resend captured payload. Mitigation (shipped v1.0): ±10min freshness window on `reported_at`.

What the attacker does NOT get: database access, web session, other nodes' data, server-side secret key.

Mitigation chain: PSK rotation via `HandleNodeRegeneratePSK` (manual, audited). No auto-rotation (acceptable single-tenant; flag for multi-tenant).

## Compromised management server

_Source: server-side session, merged 2026-04-16._

**If attacker has root on the server:** MySQL data, `secret.key`, `.cast` session recordings, web sessions all fully exposed. Attacker can decrypt all node PSKs. We do NOT defend against this today. Hardening path: off-host syslog mirror, HMAC chain (makes event forgery detectable post-facto), HSM for `secret.key`. All roadmap, none shipped.

**If attacker has stolen superadmin web session:** can view everything, ack anomalies, create/delete users, regen PSKs, open terminals, modify permissions, change tuning thresholds. All audited. Key risk: raising anomaly thresholds to suppress detection. Mitigation (not shipped): alert on `tuning_saved` with large-magnitude threshold changes.

## Defensive posture summary

| Concern | Status |
|---|---|
| Source wipe / ransomware | ✅ Detected (bytes_drop + files_drop anomaly) |
| Repo tampering (restic forget) | ✅ Detected (snapshot_drop anomaly) |
| Snapshot ID set tracking | ✅ Server ready (migration 036), CLI shipping `snapshot_ids` in v2.4.7 |
| Config drift (unauthorized edits) | ✅ Audit trail (`audit.jsonl` + server /audit) |
| Audit log tampering | ✅ HMAC-SHA256 chain on every event (v2.5.0 CLI + v1.14.0 server). Chain break → CRITICAL + ack freeze. |
| Permanent client-side gap | ✅ 1-hour stale-gap sweeper on server prevents pipeline freeze |
| Node went silent (attacker stopped daemon) | ✅ Silent-node alarm (7min default, tunable). Shipped v1.12.0. |
| Tunnel brute-force via leaked PSK | ✅ Per-UID exponential backoff rate limiter (1s → 10min). Shipped v1.12.0. |
| Host audit (sshd/sudo/systemd) | ✅ Host audit worker with journal cursor. Shipped v1.12.0. |
| Full server backup/restore | ✅ /settings/backup UI. Shipped v1.13.1. |
| PSK rotation | ⚠️ Manual. Acceptable while single-operator. |
| Real-time alerting | ❌ Deferred (last-ever milestone). |
| Integration tests | ✅ CLI: 25 tests + CI. Server: ~50 tests + CI. |

## When this document changes

Any PR that:
- Introduces a new attack-observability detector
- Changes a trust boundary
- Closes or widens a "NOT defending against" item
- Adds a new authentication or encryption step

must update this file in the same commit. If you catch yourself arguing about whether a feature is defensible, the argument belongs in this file.
