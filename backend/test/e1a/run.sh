#!/bin/bash
set -euo pipefail
test "${E1A_T1_APPROVED:-}" = yes || { echo 'T1 not approved'; exit 2; }
export MIGRATION_TEST_HOST=127.0.0.1 MIGRATION_TEST_PORT=5432
export MIGRATION_TEST_DATABASE=tremor_e1a_test MIGRATION_TEST_USER=e1a_test_admin
export MIGRATION_TEST_SERVER_ADDRESS=127.0.0.1
export MIGRATION_TEST_PASSWORD="$PGPASSWORD"
export MIGRATION_TEST_SERVER_ID
MIGRATION_TEST_SERVER_ID=$(psql -X -At -h 127.0.0.1 -p 5432 -U e1a_test_admin -d tremor_e1a_test -v ON_ERROR_STOP=1 -c 'SELECT system_identifier FROM pg_control_system()')
export EXTENSION_TEST_DISPOSABLE_APPROVED=yes
export EXTENSION_TEST_ALTERNATE_OWNER=e1a_alternate
export E1A_TEST_CLI=/opt/e1a/migrate-extension
report=/tmp/e1a-report
mkdir -p "$report"
/opt/e1a/migrations.test -test.list='^TestExtensionIntegration' > "$report/expected.txt"
# Mandatory read-only auth/HBA gate. A nonzero exit blocks ALL migrations.
if /opt/e1a/migrations.test -test.v -test.count=1 -test.timeout=30s -test.run='^TestExtensionAuthPreflight$'; then
  echo 'AUTH_PREFLIGHT_EXIT=0'
else
  rc=$?
  echo "AUTH_PREFLIGHT_EXIT=$rc; ALL_INTEGRATION_SCENARIOS=NOT_RUN"
  while read -r name; do echo "RESULT NOT_RUN $name"; done < "$report/expected.txt"
  exit "$rc"
fi
# No broad go test invocation. No host .env or PG service files are mounted.
exec /bin/bash /opt/e1a/run-suite.sh /opt/e1a/migrations.test "$report" '^TestExtensionIntegration'
