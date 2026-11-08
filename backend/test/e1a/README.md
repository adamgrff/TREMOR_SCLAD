# E1A: disposable Docker acceptance fixtures

## Current status: E1A-T3.3 ACCEPT (limited contract)

**Production deployment is not authorized; production readiness is not established.**
T3.3 (2026-11-07) accepted R1-R6 within the bounded E1A mechanism contract after
reviewing T3.2 code/evidence. E1B is not implemented or accepted: restricted
PostgreSQL privileges and separation of owners remain E1B work. The ordinary
CLI's production approval allowlist intentionally remains []; fixture approvals
exist only in the test binary.

T3.2 used a newly built TEST image and a new disposable PostgreSQL 18.6 cluster,
linux/amd64, builtin C.UTF-8. Results: **17 main integration tests PASS, 0 FAIL,
0 NOT_RUN, 2 expected child-entry SKIP**. Auth/HBA preflight passed before
migrations: correct password accepted, all TCP HBA rules SCRAM, SCRAM verifier
present, wrong password rejected with typed SQLSTATE **28P01**. Suite/Compose,
tee/report and both container exit codes were 0; OOMKilled=false.

Both literal-only CHECK substitutions (ledger_checksum_hex, ledger_profile_clean)
caused ErrUnsafe with unchanged rows/receipts, retaining CHECK name/type/count.
After restoration, 026 added exactly one receipt and one non-idempotent effect.
ConcurrentApply observed the same advisory key's granted holder and distinct
blocked waiter via pg_locks, pg_stat_activity and pg_blocking_pids within
5 seconds; both runners then completed with one receipt and one effect.

Evidence project: `e1a-t32-0d0e3bd9b37941e8a778f5813249fa24`. Keep its external
summary.json, test-results.csv, tests.log, build/runtime logs, auth/HBA and
identity/image metadata, SHA256 inventories and cleanup records together.
SHA256 consistency is not independent provenance. Successful raw CHECK/lock
catalogue values were asserted in fixtures, not logged in full.

At T4.1 the E1A additions are still untracked; current Git HEAD does not contain
them. The tested source inventory identifies the additions over the base HEAD.
A future separately authorized Git checkpoint must bind its commit SHA to the
tested build inputs and archived results. T4.1 changes documentation only.
The complete mechanism contract is in
[DB04_E1A_EXTENSION_RUNNER.md](../../../docs/database/DB04_E1A_EXTENSION_RUNNER.md).

## History: E1A-T3.1 correction and earlier runs

T1.2: 14 main PASS, 2 FAIL, 2 child-entry SKIP. T2.2: auth/HBA PASS,
16 main PASS, 0 FAIL, 0 NOT_RUN, 2 expected child-entry SKIP. T3 subsequently
rejected acceptance because the then-green suite did not expose the R4 defect.
These historical totals are separate from the current 17-test T3.2 result.

The T2.2 run passed, but T3 found a literal-loss defect in extension CHECK
normalization. T3.1 preserves exact quoted bytes and adds checksum/profile
literal-only negative fixtures plus positive 025/026 cases. The concurrent test
now requires a real granted holder / blocked waiter on the exact advisory key
before release, then one durable row effect. These cases were NOT RUN at T3.1;
their T3.2 results are PASS as recorded above and in the current matrix below.

The suite runner is now run-suite.sh, invoked by run.sh AFTER auth admission.
Dockerfile and its ignore allowlist include it. T3.2 rebuilt and checked a new
TEST image; the retained T2.2 image does not contain these changes. Any future
authorized run must build its own unique image from the intended source tree.

Offline TestExtensionOfflineFailfastHarness exercises this same helper with a
real child Go test binary: synthetic first FAIL, exact exit 1, tee exit 0,
preserved error, second test NOT_RUN and no overall PASS. These harness probes
have names outside ^TestExtensionIntegration and cannot fail the PG suite.
On Windows the offline test uses Git Bash with explicit MSYS paths; no DB or
Docker execution was involved in that T3.1 negative harness check. T3.2 checked
the packaged helper and ran the positive suite; it did not repeat the negative
harness inside Docker. Resource limits and pinned images remain unchanged.

The T3.1 pending status was superseded by T3.2 regression and T3.3 acceptance.
Genuine network lost-successful-COMMIT-response, remote TLS/CA/hostname and
additional server variants remain NOT RUN. Mechanism acceptance does not
authorize another test run, a Git commit, E1B work or production deployment.

## History: E1A-T2.1 corrections (runtime pending then; passed at T2.2/T3.2)

The first T1.2 run built successfully and ended with integration exit 1:
14 top-level PASS, 2 FAIL, 2 child-entry SKIP. That historical run was not a green
complete suite; the current matrix below describes the later T3.2 run. Neither observed failure
had a PostgreSQL SQLSTATE: recovery authenticated successfully with the wrong
password; address validation failed in the Go constructor before connecting.

T2.1 explicitly sets both `--auth-host=scram-sha-256` in POSTGRES_INITDB_ARGS and
POSTGRES_HOST_AUTH_METHOD=scram-sha-256. The first controls initdb's earlier
loopback/replication host rules; the latter controls the image entrypoint's
appended host rule. Setting only the latter would not fix first-match trust.
Locale/encoding and target identity guards are unchanged. Unix socket trust is
retained for the official entrypoint's local initialization only; ALL TCP rules
must be SCRAM. init.sql only creates the alternative NOLOGIN TEST owner and does
not edit HBA. Dockerfile copies updated tests into the test binary automatically.

run.sh now invokes TestExtensionAuthPreflight before any migration test. It:
- connects to the exact approved loopback TEST endpoint with the correct password
  and reuses Target.inspect (read-only startup; no schema creation);
- reads SHOW hba_file and ordered pg_hba_file_rules, rejects parse errors and ANY
  non-SCRAM TCP rule, requires the generated all/all 127.0.0.1/32 SCRAM rule and
  a SCRAM verifier (only a boolean is read, never the verifier itself);
- logs safe HBA metadata without authentication options/passwords;
- attempts the SAME endpoint/user/database with a deliberately wrong password;
  only typed pgconn.PgError SQLSTATE 28P01 passes. A timeout, network error, 28000,
  missing DB or unexpectedly successful connection blocks all migrations.

pg_hba_file_rules describes current files, NOT necessarily the last loaded rules.
The fresh cluster's startup plus actual correct/wrong-password connections
validate effective authentication. No file edit/reload happens in preflight.
Save the HBA metadata and AUTH PASS/28P01 output with the next run's diagnostics.
Neither test passwords nor raw connection errors are printed by this gate.

The address fixture now checks NewExtension directly and never calls Run for
the forbidden target. Production exposes no sentinel/typed address error, so
the test requires its exact concrete errors.New type AND exact guard message;
other constructor errors and unexpected successful construction fail the test.

The suite uses -test.failfast. It prevents starting new tests after a registered
failure; it does not instantly interrupt already running operations. run.sh logs
the original Go and tee exit codes and emits RESULT PASS/FAIL/SKIP/NOT_RUN for
every listed top-level integration test. Unstarted tests are NOT_RUN, not SKIP.
Started tests without a terminal outcome (for example, a timeout/panic) are
reported FAIL with an additional UNFINISHED marker, never mislabeled NOT_RUN.
Preflight failure lists all integration tests NOT_RUN. Subtest outcomes remain
in the verbose suite log. No retries are implemented. Temporary report files are
under /tmp/e1a-report; RESULT lines and HBA output also reach container logs.

T2.2 used separate permission, a fresh project/cluster/password and a rebuilt
unique TEST image; T3.2 did the same for the new regressions. For any future
authorized run follow the pinned recipe below; verify labels/isolation and save
auth gate and fail-fast reports. Do
not reuse stopped T1.2 resources, rerun migrations automatically, or change the
production allowlist. Retain existing resource limits and the same image digests.

References: https://www.postgresql.org/docs/18/view-pg-hba-file-rules.html and
https://www.postgresql.org/docs/18/auth-pg-hba-conf.html

Historical T1.1 was static preparation only: Docker Engine/Compose and registry
metadata were inspected without pulling layers, building or starting containers.
Later separately authorized runs exercised the runtime, including T3.2 above.
Every future runtime run needs its own explicit authorization. This creates no
production approval and proves none of the E1B privilege model.

T1.1 validation: Docker Engine 29.9.2, Compose 5.6.1, linux/x86_64.
`compose --env-file NUL ... config --quiet` passed using validation-only values
and E1A_T1_APPROVED=no. Normalized config confirmed resource limits, platform,
service:pg and no explicit ports/volumes. The password recipe was executed
successfully in Windows PowerShell 5.1 and PowerShell 7. Container/runtime
validation was NOT RUN at T1.1. T3.2 later checked runtime isolation and OOM;
static config validation alone does not exercise tmpfs mounts.

## Architecture

Use Docker Desktop Linux containers with its WSL 2 backend on Windows. Installing
Docker/WSL and enabling virtualization are operator prerequisites, not performed
by this harness. Review the current Docker Desktop Windows requirements first.

The `pg` service has a new internal Compose network, tmpfs PGDATA, no published
ports, and listens only on 127.0.0.1:5432. The test container shares exactly that
service's network namespace (`network_mode: service:pg`). Thus Target.inspect
sees the same loopback address and server port as the client. No runner guard is
weakened. No host source/data/secret directories or engine socket are mounted.
The Dockerfile-specific ignore file allowlists only migration code, CLI, baseline
SQL, module metadata and this test harness. Image build downloads Go modules;
runtime uses an internal network. Image builds are outside the runtime isolation.

The test image contains a compiled test binary and a separately compiled ordinary
CLI. Only the test binary contains private fixture approvals. The CLI's embedded
production list remains []. Test migrations are generated under the test process's
temporary directory. Never add fixture hashes to production approvals.

The disposable bootstrap user is a superuser created only by this container's
entrypoint. This intentionally allows tampering fixtures, pg_control_system(),
owner changes and cleanup; it does NOT test restricted migrator privileges.
The alternative NOLOGIN owner is also confined to this new cluster.

## Pinned official images (verified 2026-11-07)

Registry metadata was obtained with `docker buildx imagetools inspect` (including
`--raw`). These are platform-specific linux/amd64 manifests, not attestations.
No layers were downloaded during that metadata-only check. Subsequent authorized
builds, including T3.2, used these pins. Both versions are Docker Official Images.

| Image tag | linux/amd64 manifest digest |
|---|---|
| docker.io/library/postgres:18.6-bookworm | sha256:02fb3245e038f33536cf13bc6c4e59f21d07b0cea3c1fe1c07c6e8aa8f083bcc |
| docker.io/library/golang:1.27.3-bookworm | sha256:025eefc1e8dbe63201734a72aaa0a55d7a077f2d8410432f8ae2f3e939f32871 |

Observed multi-platform index digests (provenance only, not the build pins):
- PostgreSQL: sha256:f1ea2a1a4fb7b68d4836be1fdcb8a6466a89b072158bf9d97663600538c294c5
- Go: sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0

Both services explicitly select linux/amd64. Go 1.27.3 satisfies go.mod's 1.26.9
minimum; dependencies and go.mod are unchanged. PGDATA matches PostgreSQL 18's
official /var/lib/postgresql/18/docker layout; builtin C.UTF-8 is explicit.

## Runtime resource budget

For the 16 GiB Windows host, PG is capped at 3 GiB and tests at 1 GiB, with no
additional container swap allowance. PGDATA tmpfs is capped at 1536 MiB (not
preallocated), PG /tmp at 128 MiB, tests /tmp at 256 MiB, and PG /dev/shm at
256 MiB. tmpfs/shared memory usage counts toward the corresponding memory cap;
these limits are not additive reservations. The PG cap leaves headroom beyond
small-fixture data/WAL and default shared buffers. Tests run compiled binaries,
including child processes, without a Go compiler at runtime.

Tests also override the inherited /var/lib/postgresql VOLUME with a 16 MiB tmpfs,
so neither service needs an anonymous volume. No named/external volumes exist.
The combined 4 GiB runtime cap leaves headroom in the observed ~7.69 GiB Docker
VM and on Windows. T3.2 ran with these limits and OOMKilled=false on both
containers; peak RAM/tmpfs usage was not measured, so no capacity guarantee is
claimed. Future runs must retain OOM/timing diagnostics; resource failures need
harness diagnosis before attribution to the runner. Image build/BuildKit memory
is separate and is NOT constrained by service mem_limit. Build and runtime are
separate steps.

## Local reproduction recipe (DO NOT run without separate permission)

Run from the repository root in Windows PowerShell 5.1 or PowerShell 7.
The random-password generation below works on both .NET Framework and modern .NET.
Use the verified immutable image references below.
No mutable `latest` defaults are supplied. Use a fresh run ID each time; refuse to
reuse a project if Docker already has containers/networks with that project label.

```powershell
$env:E1A_RUN_ID = 'e1a-' + [guid]::NewGuid().ToString('N')
$env:E1A_TEST_IMAGE = 'tremor-e1a-test:' + $env:E1A_RUN_ID
$env:E1A_GO_IMAGE = 'docker.io/library/golang@sha256:025eefc1e8dbe63201734a72aaa0a55d7a077f2d8410432f8ae2f3e939f32871'
$env:E1A_PG_IMAGE = 'docker.io/library/postgres@sha256:02fb3245e038f33536cf13bc6c4e59f21d07b0cea3c1fe1c07c6e8aa8f083bcc'
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try {
    $passwordBytes = New-Object byte[] 32
    $rng.GetBytes($passwordBytes)
    $env:E1A_TEST_PASSWORD = [BitConverter]::ToString($passwordBytes).Replace('-', '')
} finally { $rng.Dispose() }
$env:E1A_T1_APPROVED = 'yes' # set only after explicit authorization
$composeFile = 'backend/test/e1a/compose.yaml'
$docker = Join-Path $env:LOCALAPPDATA 'Programs\DockerDesktop\resources\bin\docker.exe'
if (-not (Test-Path -LiteralPath $docker)) {
    $docker = (Get-Command docker.exe -ErrorAction Stop).Source
}
& $docker compose --env-file NUL -p $env:E1A_RUN_ID -f $composeFile config --quiet
if ($LASTEXITCODE -ne 0) { throw 'Compose validation failed' }
# ONLY AFTER explicit permission for a new run: downloads pinned layers/modules.
& $docker compose --env-file NUL -p $env:E1A_RUN_ID -f $composeFile build pg
if ($LASTEXITCODE -ne 0) { throw 'Test image build failed' }
# pg and tests use this same unique image; no separate tests build is needed.
& $docker compose --env-file NUL -p $env:E1A_RUN_ID -f $composeFile up --pull never --no-build --abort-on-container-exit --exit-code-from tests
if ($LASTEXITCODE -ne 0) { throw 'Integration run failed; retain resources for diagnosis' }
```

Check the exit code after EVERY command and stop on failure. `--env-file NUL`
prevents Compose from loading the project's .env on Windows. Do not print the
expanded Compose configuration: it contains the disposable password. The script
does not inherit Windows DATABASE_URL/PG service settings. No production password
is needed. The fresh system identifier is read only from the new container over
its loopback interface; database, user, locale, port and schema are rechecked by
the existing target inspector on each connection.

Before teardown, save the selected image digests, project/run label, exact
container IDs, cluster system identifier, test output/exit status and skip list
outside the containers. Child-entry tests skipped in the parent suite are
expected; missing alternate-owner/CLI fixtures or other scenario skips are not.

## Cleanup

Inspect containers/networks with `com.docker.compose.project=$env:E1A_RUN_ID` and
verify their `tremor.e1a.run` labels and recorded IDs. Only after this verification:

```powershell
& $docker compose --env-file NUL -p $env:E1A_RUN_ID -f $composeFile down --volumes
if ($LASTEXITCODE -ne 0) { throw 'Project cleanup failed' }
```

No external volumes/networks are declared. PGDATA is tmpfs. Never use prune,
wildcard deletion, or cleanup against another Compose project. Keep the unique
test image for diagnosis, or remove only its verified image ID separately.
Remove disposable password variables from this PowerShell session afterwards.

## Scenario matrix

The 15 coverage groups below were exercised by the T3.2 suite. They group the
17 main tests and are not 15 additional executions. Manifest/strict-JSON cases
also have packaged offline unit coverage. PASS applies only to these fixtures;
the untested network/TLS/server variants listed below remain NOT RUN.

| # | Scenario | Entry point / remaining limitation |
|---|---|---|
| 1 | Fresh 22 clean-v2 migrations | Every deployment fixture calls base apply |
| 2 | Baseline integrity | Resume snapshots; BusinessPreservation; AdmissionT0 |
| 3 | Approved test schema extension | Resume; BusinessPreservation creates new table |
| 4 | Repeat apply | Resume; BusinessPreservation; HardCrash effect exactly once |
| 5 | Baseline rollback | Resume timestamp; LedgerMetadataT0 |
| 6 | Old extension rollback | Resume name/timestamp; LedgerMetadataT0 |
| 7 | SQL/receipt fault | Resume afterSQL; ReceiptRollbackT0 after receipt |
| 8 | Unknown COMMIT | Resume hook after actual commit; UnconfirmedCommit; RecoveryValidation; RecoveryConnectFailureT0; TransportCloseT0 |
| 9 | Concurrent apply | ConcurrentApply |
| 10 | Actual process death/resume | HardCrash before receipt COMMIT and after successful COMMIT; parent kills child, fresh child resumes |
| 11 | Metadata/readiness | MetadataV2 + TargetGuardsT0 |
| 12 | Unsupported objects | UnsupportedT0: materialized, partitioned, range, aggregate, foreign, cast |
| 13 | Upgrade with business data/receipts | BusinessPreservation: full JSONB rows of every baseline ordinary table; exact multiset comparison |
| 14 | Manifest/identity/CHECK guards | AdmissionT0; CHECKMapping; CHECKLiteralRegression; TargetGuardsT0; offline manifest/strict-JSON tests |
| 15 | Ordinary executable | OrdinaryCLIT0, wrong password must still produce release-approval rejection |

### Current T3.2 test results

Entry names below omit the common `TestExtensionIntegration` prefix.

| Entry | T3.2 result |
|---|---|
| AdmissionT0 | PASS |
| BusinessPreservation | PASS |
| CHECKLiteralRegression (both CHECK cases) | PASS |
| CHECKMapping | PASS |
| ConcurrentApply (observed lock wait) | PASS |
| HardCrash (before_commit and after_commit) | PASS |
| LedgerMetadataT0 | PASS |
| MetadataV2 | PASS |
| OrdinaryCLIT0 | PASS |
| ReceiptRollbackT0 | PASS |
| RecoveryConnectFailureT0 | PASS |
| RecoveryValidation | PASS |
| Resume | PASS |
| TargetGuardsT0 | PASS |
| TransportCloseT0 | PASS |
| UnconfirmedCommit | PASS |
| UnsupportedT0 | PASS |
| RestartChild | Expected SKIP when independently discovered; parent scenarios PASS |
| CrashChild | Expected SKIP when independently discovered; HardCrash parent PASS |

Outside that integration filter, TestExtensionAuthPreflight passed first and
17 packaged offline unit/contract tests passed. The negative offline fail-fast
harness passed at T3.1, not as a repeated T3.2 Docker negative scenario.

Known scope: closing the client's TCP socket before COMMIT is an actual transport
disconnect, but NOT a network packet-drop or a lost successful COMMIT-response
experiment. Lost-success response is still injected AFTER actual tx.Commit().
A true in-flight packet-loss/TLS/remote identity harness needs separate design
and permission. Wrong locale/major require additional disposable server variants;
the base inspector is unchanged. No T0 claim covers these variants.

Synthetic data links warehouse/category/cell/product, stock, active receiving
session/pending items, placement/items, immutable intake journal, quantity receipt
and typed operation receipt. Full-row comparison retains timestamps, IDs, status,
revision, JSON payload/response and all existing rows, including baseline ledger
and deployment identity. Only the explicitly added empty upgrade table and the
separately validated extension ledger are outside that baseline row comparison.

The hard-crash fixture increments a durable synthetic warehouse-name suffix; the
new process must leave exactly one suffix after both crash phases. Before restart
the parent observes only read-only status. Recovery never automatically reapplies
SQL; retry is an explicit new test process.

References:
- https://docs.docker.com/desktop/setup/install/windows-install/
- https://docs.docker.com/desktop/features/wsl/
- https://docs.docker.com/reference/compose-file/services/
- https://www.postgresql.org/docs/18/app-initdb.html
