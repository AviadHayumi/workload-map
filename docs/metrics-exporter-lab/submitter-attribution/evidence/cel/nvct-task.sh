# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
set -eu
for attempt in $(seq 1 60); do
  [ -d "$NVCT_RESULTS_DIR" ] && break
  sleep 1
done
[ -d "$NVCT_RESULTS_DIR" ]
sleep 1
printf '{"taskId":"%s","percentComplete":1,"name":"running","lastUpdatedAt":"%s"}\n' "$NVCT_TASK_ID" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$NVCT_PROGRESS_FILE_PATH.tmp"
mv "$NVCT_PROGRESS_FILE_PATH.tmp" "$NVCT_PROGRESS_FILE_PATH"
sleep 180
printf '{"taskId":"%s","percentComplete":100,"name":"completed","lastUpdatedAt":"%s"}\n' "$NVCT_TASK_ID" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$NVCT_PROGRESS_FILE_PATH.tmp"
mv "$NVCT_PROGRESS_FILE_PATH.tmp" "$NVCT_PROGRESS_FILE_PATH"
sleep 10
