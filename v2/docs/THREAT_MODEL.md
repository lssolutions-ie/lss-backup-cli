# LSS Backup — Threat Model

_Status: v1 draft, 2026-04-15. Scope: lss-backup-cli v2.4+ and the management server at similar maturity. Shared between both sessions._

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

8. **Forensics under active attack.** The audit log is a history, not an integrity-verified evidence chain. When HMAC chain lands, it becomes tamper-evident relative to a trusted CLI; until then, relative to a trusted node.

## Trust boundaries

- **Operator machine ⇌ node**: the operator is root on the node. Trust is absolute; no privilege separation.
- **Node ⇌ management server**: PSK (128-hex-char per node, in encrypted secrets.env). AES-256-GCM on every payload. Per-node monotonic seq + server-side UNIQUE constraint for ingest. Tunnel auth via HMAC-SHA256 over a per-node ed25519 key pair.
- **Management server ⇌ operator browser**: session auth, HTTPS. Out of scope for this doc.
- **Node ⇌ backup destination**: for local, filesystem perms. For S3, credentials in secrets.env. For SMB/NFS, credentials in secrets.env. For rsync destinations on the same host, standard Unix perms.

## Defensive posture summary

| Concern | Status |
|---|---|
| Source wipe / ransomware | ✅ Detected (bytes_drop + files_drop anomaly) |
| Repo tampering (restic forget) | ✅ Detected (snapshot_drop anomaly) |
| Config drift (unauthorized edits) | ✅ Audit trail (`audit.jsonl` + server /audit) |
| Audit log tampering | ⚠️ Detected only relative to a trusted CLI. HMAC chain would close this. |
| Permanent client-side gap | ✅ 1-hour stale-gap sweeper on server prevents pipeline freeze |
| Node went silent (attacker stopped daemon) | ⚠️ 10-minute threshold, too loose. "Aggressive silence alarm" (1-2 missed heartbeats) is planned. |
| Tunnel brute-force via leaked PSK | ⚠️ No rate limit on `/ws/ssh-tunnel` today. P0 hardening planned. |
| PSK rotation | ⚠️ Manual. Acceptable while single-operator. |
| Real-time alerting | ❌ Deferred (last-ever milestone). |
| MySQL backup of server itself | ❌ Gap. Planned. |
| Staging environment | ❌ Gap. First fresh-VM install doubles as staging. |
| Integration tests | 🛠 In progress (CLI side landed v2.4.0; server-side still pending). |

## When this document changes

Any PR that:
- Introduces a new attack-observability detector
- Changes a trust boundary
- Closes or widens a "NOT defending against" item
- Adds a new authentication or encryption step

must update this file in the same commit. If you catch yourself arguing about whether a feature is defensible, the argument belongs in this file.
