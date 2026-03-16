# lss-backup-cli

More information coming soon. This is an early-stage release candidate.

## Documentation
- User guide: `docs/user-guide.txt`
- Exit codes: `docs/exit-codes.txt`

## Install
Make sure you are downloading the latest release.
```
cd /etc
wget https://github.com/lssolutions-ie/lss-backup-cli/archive/refs/tags/v1.0.1.tar.gz
tar -xvf v1.0.1.tar.gz
rm v1.0.1.tar.gz
mv lss-backup-cli-1.0.1 lss-backup
cd lss-backup
chmod +x *.sh
chmod +x functions/*.sh prep-dependencies/*.sh.prep
bash install-lss-backup.sh
```
