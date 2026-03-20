#!/usr/bin/env bash
set -euo pipefail

echo "=== repokeeper teardown ==="
echo "This will stop all containers, remove volumes, and delete the cloned repo."
read -p "Are you sure? (y/N) " confirm
if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
    echo "Aborted."
    exit 0
fi

docker compose down -v
rm -rf workspace/repo
rm -f workspace/PAUSE
echo "Teardown complete."
