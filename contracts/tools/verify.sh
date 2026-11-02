#!/usr/bin/env bash
# Verifikasi kontrak: OpenAPI valid, WS vector lulus, tabel endpoint tidak basi.
# Prasyarat: pip install openapi-spec-validator jsonschema pyyaml
set -euo pipefail
cd "$(dirname "$0")/../.."
python3 -c "
import yaml
from openapi_spec_validator import validate
validate(yaml.safe_load(open('contracts/openapi.yaml'))); print('OpenAPI valid')"
python3 contracts/tools/validate_vectors.py | tail -n 3
python3 contracts/tools/gen_api_ref.py >/dev/null
git diff --exit-code -- docs/spec/generated/api-endpoints.md >/dev/null \
  || { echo "api-endpoints.md basi: commit hasil gen_api_ref.py"; exit 1; }
if [ -n "${SMSC_TEST_DATABASE_URL:-}" ]; then
  bash server/tests/schema_sync.sh
else
  echo "(lewati sinkron skema: set SMSC_TEST_DATABASE_URL untuk membandingkan schema.sql dengan migrasi)"
fi
echo "Kontrak OK"
