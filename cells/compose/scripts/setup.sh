#!/bin/sh
# Schema setup for a laug cell. Postgres always (persistence and SQL visibility); the Elasticsearch
# index too when ENABLE_ES=true. Runs inside temporalio/admin-tools.
set -eu

sh /scripts/setup-postgres.sh

if [ "${ENABLE_ES:-false}" = "true" ]; then
  ep="${ES_SCHEME:-http}://${ES_HOST:-elasticsearch}:${ES_PORT:-9200}"
  attempt=0
  # No depends_on for elasticsearch: it is behind a compose profile, so wait here instead.
  until temporal-elasticsearch-tool --ep "$ep" setup-schema \
     && temporal-elasticsearch-tool --ep "$ep" create-index --index "${ES_VISIBILITY_INDEX:-temporal_visibility_v1_dev}"; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 30 ]; then
      echo "elasticsearch at $ep not ready after $attempt attempts" >&2
      exit 1
    fi
    echo "elasticsearch not ready yet (attempt $attempt/30)"
    sleep 5
  done
  echo 'Elasticsearch setup complete'
fi
