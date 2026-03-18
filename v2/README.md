# LSS Backup CLI v2

This directory contains the in-progress Go rewrite of LSS Backup CLI.

Current scaffold goals:

- establish the v2 project layout
- preserve the "independent job" model
- support both `restic` and `rsync` in the architecture from the start
- keep `job.toml` as the durable job definition
- keep `secrets.env` separate from general config

Current status:

- interactive menu created
- job storage layout created
- create/manage/list flows are menu-driven
- engine registry added for `restic` and `rsync`
- structured config parsing added for the current supported shape

Expected layout:

```text
v2/
  cmd/lss-backup/
  internal/
  jobs/
  state/
  docs/
```

The first real execution slice should be:

- local source
- local destination
- `restic`
- `rsync`

before expanding into scheduling, retention, notifications, and migration.
