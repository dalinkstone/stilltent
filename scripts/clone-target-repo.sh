#!/usr/bin/env bash
set -euo pipefail

# This script runs INSIDE the openclaw-gateway container.
# It's called by the bootstrap process and can be re-run to refresh the clone.

REPO_DIR="/workspace/repo"
GITHUB_TOKEN="${GITHUB_TOKEN:?GITHUB_TOKEN must be set}"
TARGET_REPO="${TARGET_REPO:?TARGET_REPO must be set}"

if [ -d "$REPO_DIR/.git" ]; then
    echo "Repository already cloned at $REPO_DIR"
    echo "Pulling latest changes..."
    cd "$REPO_DIR"
    git checkout main
    git pull origin main
    echo "Updated to $(git rev-parse --short HEAD)"
else
    echo "Cloning $TARGET_REPO into $REPO_DIR..."
    git clone "https://${GITHUB_TOKEN}@github.com/${TARGET_REPO}.git" "$REPO_DIR"
    cd "$REPO_DIR"
    echo "Cloned at $(git rev-parse --short HEAD)"
fi

# Configure git credentials for future pushes
cd "$REPO_DIR"
git remote set-url origin "https://${GITHUB_TOKEN}@github.com/${TARGET_REPO}.git"

echo "Target repository ready."
