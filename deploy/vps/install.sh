#!/usr/bin/env bash
# Run as root with the private directory produced by scripts/prepare_vps.py.
set -euo pipefail
if [[ $EUID -ne 0 || $# -ne 1 ]]; then
    echo 'Usage: sudo bash install.sh /path/to/prepared-bundle' >&2
    exit 1
fi
bundle=$(cd -- "$1" && pwd)
for filename in support-crm crm.env projectdesk.service backup.py projectdesk-backup.service projectdesk-backup.timer; do
    [[ -f "$bundle/$filename" ]] || { echo "Missing $filename" >&2; exit 1; }
done
command -v systemctl >/dev/null
command -v curl >/dev/null
command -v python3 >/dev/null
grep -qx 'SUPPORT_SEED_DEMO=false' "$bundle/crm.env"
grep -qx 'SUPPORT_ADDR=127.0.0.1:8090' "$bundle/crm.env"
grep -qx 'SUPPORT_DB=/var/lib/projectdesk/support.db' "$bundle/crm.env"
# Never replace a live database or silently reuse another deployment's records.
if [[ -e "$bundle/support.db" && -e /var/lib/projectdesk/support.db ]]; then
    echo 'A CRM database already exists; stop and reconcile data before migration.' >&2
    exit 1
fi
if ! id projectdesk >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/projectdesk --no-create-home --shell /usr/sbin/nologin projectdesk
fi
install -d -m 0755 /opt/projectdesk
install -d -m 0700 /etc/projectdesk
install -d -m 0700 -o projectdesk -g projectdesk /var/lib/projectdesk
if systemctl cat projectdesk.service >/dev/null 2>&1; then
    systemctl stop projectdesk.service
fi
install -m 0755 "$bundle/support-crm" /opt/projectdesk/support-crm
install -m 0600 "$bundle/crm.env" /etc/projectdesk/crm.env
install -m 0644 "$bundle/projectdesk.service" /etc/systemd/system/projectdesk.service
install -m 0644 "$bundle/backup.py" /opt/projectdesk/backup.py
install -m 0644 "$bundle/projectdesk-backup.service" /etc/systemd/system/projectdesk-backup.service
install -m 0644 "$bundle/projectdesk-backup.timer" /etc/systemd/system/projectdesk-backup.timer
if [[ -e "$bundle/support.db" ]]; then
    install -m 0600 -o projectdesk -g projectdesk "$bundle/support.db" /var/lib/projectdesk/support.db
fi
systemctl daemon-reload
systemctl enable --now projectdesk.service
systemctl enable --now projectdesk-backup.timer
for attempt in {1..30}; do
    if systemctl is-active --quiet projectdesk.service && curl --fail --silent --max-time 2 http://127.0.0.1:8090/healthz; then
        echo
        echo 'CRM is healthy on localhost:8090. Configure the Mac MCP tunnel and HTTPS next.'
        exit 0
    fi
    sleep 0.5
done
echo 'CRM health check failed; inspect journalctl -u projectdesk.service.' >&2
exit 1
