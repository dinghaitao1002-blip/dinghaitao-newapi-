#!/bin/bash
# Use this entry point for production edge reloads after configuration changes.
set -euo pipefail
deployment_root=/home/user1/dingjx/newapi
python3 "$deployment_root/scripts/check_frontend_release.py"
docker exec newapi-edge nginx -t
docker exec newapi-edge nginx -s reload
# A graceful reload can briefly leave old workers serving existing connections.
for attempt in 1 2 3; do
  if python3 "$deployment_root/scripts/check_frontend_release.py" --live; then
    exit 0
  fi
  sleep 1
done
echo 'ERROR: edge reloaded, but the custom dashboard failed live verification' >&2
exit 1
