# Э1Б-И3-F1: isolated Go readiness verifier

Status: foundation / disposable TEST only. Production release allowlists remain
empty. This change does not enable production or install existing API routes.

## Processes and trust

`cmd/readiness-verifier` loads one private fixed configuration and its own password
file. It accepts only `tremor_migrator`, validates the existing migration Target,
loads release-approved transition/protected artifacts, binds the archived source,
and invokes the existing `VerifyProtectedExtension` for every request. There is
no apply, provisioning, transition, caller-selected SQL, descriptor, or target
operation in its HTTP interface. Initial verification also must succeed.

The existing `tremor_migrator` is sufficient; no new DB grants or approved metadata
are introduced. It retains only the I2.2 approved reads (including ledgers and
receipts) and the separately provisioned exact pg_control_system() EXECUTE grant.
No pg_monitor / pg_read_all_data / business DML / DDL / owner memberships are
added. Real TEST connections verify the role and rejected mutations independently
of READ ONLY transactions. The existing verifier performs its catalog collection
inside REPEATABLE READ, READ ONLY; foundationSecurity() remains unchanged.

The API receives only its tremor_api password and client TLS key. RuntimePool
rejects administrative users, removes fallback targets and inherited pool hooks,
checks actual user/database/OID and obtains a fresh verdict on connection and pool
acquisition. Require obtains another fresh verdict before a future protected HTTP
action and maps failure to safe HTTP 503. These adapters are exercised by the TEST
API container; existing cmd/api routes are deliberately not installed/refactored.
The deployment operator must configure the API's fixed PostgreSQL endpoint and
expected database OID together with the verifier's target. The database OID alone
is not a cluster identity proof; the verifier checks system identity separately.
The TEST services share only the isolated PG network namespace (loopback), with
separate PID namespaces, filesystem mounts and credentials, no host ports, no
Docker socket, read-only service filesystems and dropped capabilities.

## Wire contract

TLS 1.3, dedicated trusted CA, standard chain/validity/EKU checks plus exactly one
expected URI SAN for each peer. The client also checks the configured server DNS
name; no InsecureSkipVerify, redirects or ambient HTTP proxy.

Only POST /internal/readiness, no query string. Strict bounded JSON request:
`{"nonce":"<fresh 32 random bytes as lowercase hex>"}`. Response contains only
READY/NOT_READY, the identical nonce and exact expected logical target
(cluster/database/deployment/schema). No hashes, receipt contents or SQL errors.
Unknown/duplicate/case-alias fields, oversized payload and trailing content are
rejected. A new random nonce and verification are required for every call; old
responses cannot authorize a new request. No READY cache or legacy fallback.
Verification failure/timeout and saturation return HTTP 503. Two concurrent
checks maximum in the executable; per-check timeout 5s, bounded server I/O.
The database probe must honor its context; the existing Go verifier does so.

## Evidence reused on every verification

| Invariant | Existing evidence/check | Regression |
|---|---|---|
| Physical cluster/database/deployment binding | Target.connect/inspect, database OID and deployment_identity | I2.2; real migrator service; wrong target channel test |
| Approved exact frontier and legacy readiness | strict extension manifest/prefix, checkProtectedCheckpoint, E1A-context fingerprint | retained collector readiness and I2.2 suites |
| Transition receipt and approved anchor | BoundSecurityTransition.ExpectedReceipt and protected checkpoint | real missing receipt / changed descriptor anchor refused |
| Owners, schema/database/object/column ACL, SD context, defaults | full scoped Go collector + exact approved Metadata comparison | real PUBLIC grant and SD drift refused; retained collector suite |
| Role attributes and membership paths | scoped role graph and effective privilege collection | real unexpected owner membership refused |
| Session defaults | all applicable pg_db_role_setting layers | retained F2 regressions |
| Parameter ACL/effective privileges | pg_parameter_acl, has_parameter_privilege + foundation policy | real session_replication_role grant refused; retained F3 regressions |
| API cannot bypass database boundaries | unchanged foundationSecurity + PostgreSQL ACL | real ledger read, DML/DDL, EXECUTE and SET ROLE denials |

## TEST approval and reproducibility

Production executable cannot obtain an approval from an operator-supplied digest:
public approval loaders remain empty and fail closed. For actual TEST READY, a
TEST-only entry point in the existing migrations test executable receives sealed
artifacts exported by the independently approved I2.2 fixture. Its descriptor,
source, protected plan and manifest originate from pre-apply approved fixtures;
no post-apply fingerprint is promoted to an approval. It calls the exact same
VerifyProtectedExtension and readiness channel. This TEST approval mechanism is
not compiled into the production executable. Production-approved startup is
NOT_TESTED and remains disabled, as required.

Build a fresh Linux/amd64 migrations test binary using existing Go tooling:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go test -c -o <external-artifact-directory>/collector.test ./internal/migrations
# From repository root; Docker Engine access required:
./backend/test/e1b/run-readiness.ps1 -EvidenceDirectory <existing-external-directory> -TestBinary <absolute-test-binary>
```

This is an overlay of the existing E1A/I2.2 harness, not a new CI framework. Uses
immutable local image digest 475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2
(PostgreSQL 18.6), internal network and no host ports. The setup/controller alone
has synthetic administrator credentials. It rotates API and migrator to separate
random passwords and generates ephemeral mTLS identities. The verifier gets only
its private read-only mount; API gets only its separate runtime mount. CA signing
key is never persisted. Cleanup validates unique project labels, removes only
its containers/network/volumes and its exact generated secret directory. Logs
exclude environment/passwords/certificate private keys.

## Limits

Fresh verdict and subsequent API work are different PostgreSQL transactions.
No claim of eliminating races with a trusted privileged administrator; actual DB
ACL remain independently enforced. Pool acquisition does not prove every SQL
statement on a previously acquired connection: callers must use Require before
each protected action and avoid retaining connections across requests. Complete
endpoint protection, read allowlists, frontend and business commands remain I3.
No production deployment, new DB migration or new runtime EXECUTE grant is made.
Actual loss of a successful COMMIT response is outside this substage and is not
promoted from prior simulated evidence to PASS. This is not production-ready API.

References checked: https://pkg.go.dev/crypto/tls#Config and
https://www.postgresql.org/docs/18/sql-set-role.html .

## Acceptance evidence (2026-11-09)

- PASS: `go test ./...`; 64 selected existing offline migrations tests (includes
  the earlier 58 and subsequent I2.2 tests); seven new channel/runtime/executable
  top-level unit tests. `go vet ./...`, gofmt and diff whitespace checks PASS.
- PASS: all 47 existing PostgreSQL collector subtests, project
  `e1b-collector-a891cc84e8fa49f1b16ef3dad9d0e684`, system identifier
  `7706154908324835371`.
- PASS: I2.2 fresh fixture within the readiness project; I2.2 populated upgrade
  project `e1b-execution-3c2d9de953614e1fb260715ca86a7648`, including atomic rollback,
  protected apply/repeat, independent recovery and actual PostgreSQL denials.
- PASS: final real cross-container readiness project
  `e1b-readiness-aa961630e85f4eb691f04d44247fd2c2`, PostgreSQL 18.6, system identifier
  `7706155763694215211`. Actual tremor_migrator verification, tremor_api pool and
  protected HTTP adapter, separate passwords/mounts, forbidden ledger reads and
  role/DML/DDL/EXECUTE operations; missing receipt, wrong receipt anchor, PUBLIC
  grant, membership, elevated parameter grant, SD context drift all refused.
  After stopping the verifier, the API adapter returns HTTP 503 without calling
  the protected handler. NOT_READY never becomes READY through a cached verdict.
- PASS: real local TLS unit tests additionally exercise wrong SAN, untrusted CA
  on both sides, absent client certificate, replay/wrong nonce/target, malformed
  payload, timeout and concurrency saturation. These are distinct from the actual
  successful cross-container mTLS/Go/PostgreSQL chain, not mocked database proof.
- PASS: all own projects removed with zero containers/networks/volumes; generated
  secret directories removed. Evidence logs retained outside Git; no credentials
  or PEM private-key material committed.
- NOT_TESTED: production-approved executable startup (production disabled), full
  warehouse API route integration/read allowlists, races with privileged admin,
  genuine loss of successful COMMIT response. No claim of passing these cases.

main remains `63efe4bddc3aa8c7303b1bfa4f7276f3191f00f7`, tree
`53d259004b87eef5ed6a39cf544385ca635be23d`. Every file already present in main
remains byte-identical. The only existing feature file changed is the I2.2 TEST
helper, to invoke the optional TEST-only exporter.

Seven main-worktree user files were rehashed unchanged:

| File | SHA256 |
|---|---|
| .agents/skills/code-review-pro/SKILL.md | B96E3BB390F5803DF8C012D09C5F138B28CE478FEE806287F747571586F24AA4 |
| .agents/skills/database-schema-designer/SKILL.md | A1CA262A12833DCE2FC7BEFDE5F54371F0CE9FE0F139659D2D48267709B39A9B |
| .agents/skills/database-schema-designer/references/best-practices.md | A900AB42A0F07DBD411FE2B1DCCBCD6150B5FF755AF80D14507D08B69A6FC08E |
| .agents/skills/database-schema-designer/references/output-format.md | 07D3130198903349DF763AFDBA2829B273A2E51D3EA2F8ACA6D504FD542292D8 |
| .agents/skills/database-schema-designer/references/schema-templates.md | 097EC84FDCCDEDC803ADFB381DA161E59DC861D076078E809E041802F44B1D77 |
| .agents/skills/git-pr-reviewer/SKILL.md | 1FB97CBF966B8E97A2342AE72B99F411D640E2D706AB334F23159F8642623989 |
| TREMOR_SCLAD_v1.0_ROADMAP.md | 6D6DBC66F9323BF9DEFDA89C81BE9134B8D08F26D3AB5FFA4943E735FBA3D55F |
