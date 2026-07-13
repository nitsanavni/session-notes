#!/usr/bin/env bash
# Nightly consistent backup of the session-notes SQLite database with a
# retention ladder, pushed to a SECOND provider (independent of litestream's
# continuous S3 replication — defense in depth: two providers, two mechanisms).
#
# Provider-agnostic: set BACKUP_TOOL=restic or BACKUP_TOOL=rclone and the
# matching env. Intended to run from cron (see deploy/README.md), e.g.:
#   15 3 * * *  /opt/session-notes/deploy/backup.sh >> /var/log/sn-backup.log 2>&1
#
# Required env:
#   SN_DB            path to the live database (default /var/lib/session-notes/server.db)
#   BACKUP_TOOL      restic | rclone
#   BACKUP_DIR       local staging dir for the VACUUM snapshot (default /var/backups/session-notes)
#
# restic:  RESTIC_REPOSITORY, RESTIC_PASSWORD (+ provider creds, e.g. B2/S3 keys)
# rclone:  RCLONE_REMOTE  (e.g. "b2:session-notes-backups")
set -euo pipefail

SN_DB="${SN_DB:-/var/lib/session-notes/server.db}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/session-notes}"
BACKUP_TOOL="${BACKUP_TOOL:-restic}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
SNAP="${BACKUP_DIR}/server-${STAMP}.db"

mkdir -p "$BACKUP_DIR"

# VACUUM INTO takes a transactionally consistent snapshot even while the server
# is writing (no need to stop it) and compacts the file in one step.
echo "[$(date -u)] VACUUM INTO ${SNAP}"
sqlite3 "$SN_DB" "VACUUM INTO '${SNAP}'"
gzip -f "$SNAP"
SNAP_GZ="${SNAP}.gz"

case "$BACKUP_TOOL" in
  restic)
    : "${RESTIC_REPOSITORY:?set RESTIC_REPOSITORY}"
    : "${RESTIC_PASSWORD:?set RESTIC_PASSWORD}"
    restic backup "$SNAP_GZ" --tag session-notes
    # Retention ladder: keep 7 daily, 4 weekly, 6 monthly snapshots.
    restic forget --tag session-notes \
      --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune
    ;;
  rclone)
    : "${RCLONE_REMOTE:?set RCLONE_REMOTE, e.g. b2:session-notes-backups}"
    rclone copy "$SNAP_GZ" "${RCLONE_REMOTE}/daily/"
    # Weekly (Mondays) and monthly (1st) copies as the retention ladder.
    if [ "$(date -u +%u)" = "1" ]; then
      rclone copy "$SNAP_GZ" "${RCLONE_REMOTE}/weekly/"
    fi
    if [ "$(date -u +%d)" = "01" ]; then
      rclone copy "$SNAP_GZ" "${RCLONE_REMOTE}/monthly/"
    fi
    # Prune local daily copies in the remote older than 8 days.
    rclone delete --min-age 8d "${RCLONE_REMOTE}/daily/" || true
    ;;
  *)
    echo "unknown BACKUP_TOOL=${BACKUP_TOOL} (want restic|rclone)" >&2
    exit 2
    ;;
esac

# Keep only the last 3 local staging snapshots.
ls -1t "${BACKUP_DIR}"/server-*.db.gz 2>/dev/null | tail -n +4 | xargs -r rm -f

echo "[$(date -u)] backup complete via ${BACKUP_TOOL}"
