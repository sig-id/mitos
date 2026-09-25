#!/usr/bin/env bash
set -euo pipefail
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
output_dir="${1:-$project_dir/dist/wifi-setup-site}"
mkdir -p "$output_dir/wifi-setup"
cp "$project_dir"/internal/wifisetup/assets/* "$output_dir/wifi-setup/"
cp "$project_dir/deploy/wifi-setup/_headers" "$output_dir/_headers"
printf 'Static sender: %s/wifi-setup/\n' "$output_dir"

# Firmware bundle, separate from public web output: /api belongs to the watch.
portal_dir="${output_dir%/}-portal"
mkdir -p "$portal_dir"
cp "$project_dir"/internal/wifisetup/portal/* "$portal_dir/"
printf 'Watch portal firmware assets: %s\n' "$portal_dir"
