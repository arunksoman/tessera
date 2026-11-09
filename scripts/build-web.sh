#!/usr/bin/env bash
# Builds the web app into web/build/app, where tess embeds it. Run before go build.
set -euo pipefail
cd "$(dirname "$0")/../web"
npm ci --ignore-scripts
npm run check
npm run build
