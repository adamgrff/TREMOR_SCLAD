#!/bin/bash
# Test-only helper, also exercised offline with a real intentionally failing
# child Go test binary. The Docker caller always supplies fixed /opt paths.
set -euo pipefail
binary=$1
report=$2
filter=$3
mkdir -p "$report"
"$binary" -test.list="$filter" > "$report/expected.txt"
set +e
"$binary" -test.v -test.count=1 -test.failfast -test.timeout=10m -test.run="$filter" 2>&1 | tee "$report/suite.log"
codes=("${PIPESTATUS[@]}")
set -e
echo "INTEGRATION_EXIT=${codes[0]} LOG_TEE_EXIT=${codes[1]}"
# No cancellation claim: failfast stops NEW tests after failure registration.
set +e
awk '
  NR==FNR { if ($1 ~ /^Test/) expected[$1]=1; next }
  $1=="===" && $2=="RUN" && ($3 in expected) { started[$3]=1 }
  $1=="---" && ($2=="PASS:" || $2=="FAIL:" || $2=="SKIP:") {
    status=$2; sub(/:$/, "", status); if ($3 in expected) result[$3]=status
  }
  END { for (name in expected) {
    if ((name in started) && !(name in result)) {
      print "UNFINISHED", name
      result[name]="FAIL"
    }
    print "RESULT", (name in result ? result[name] : "NOT_RUN"), name
  } }
' "$report/expected.txt" "$report/suite.log" | sort
report_codes=("${PIPESTATUS[@]}")
set -e
echo "REPORT_AWK_EXIT=${report_codes[0]} REPORT_SORT_EXIT=${report_codes[1]}"
if (( codes[0] != 0 )); then exit "${codes[0]}"; fi
if (( codes[1] != 0 )); then exit "${codes[1]}"; fi
if (( report_codes[0] != 0 )); then exit "${report_codes[0]}"; fi
if (( report_codes[1] != 0 )); then exit "${report_codes[1]}"; fi
