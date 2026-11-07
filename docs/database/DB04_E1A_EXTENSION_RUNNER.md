# DB-04.2 / E1A extension runner — R1–R6 revision

Status: **ACCEPT — limited E1A contract, E1A-T3.3 audit (2026-11-07).**
**Production deployment is not authorized; production readiness is not established.**
No business extension SQL, roles, grants or production deployment approvals ship
in E1A. The 22 clean-v2 SQL files, its manifest and original runner are unchanged.

## Current acceptance evidence: E1A-T3.2 / T3.3

T3.2 ran one controlled regression suite on a newly built TEST image and a new
disposable Docker PostgreSQL 18.6 cluster, linux/amd64, builtin C.UTF-8:
**17 main integration tests PASS, 0 FAIL, 0 NOT_RUN, 2 expected child-entry SKIP**.
Auth/HBA preflight passed before migrations: the correct TEST password succeeded,
all TCP HBA rules required SCRAM, a SCRAM verifier was present, and the wrong
password failed with typed SQLSTATE **28P01**. No password/verifier is recorded here.
Suite, Compose, tee and report exit codes were 0; both containers exited 0 with
OOMKilled=false. The two SKIP entries are RestartChild and CrashChild when found
without their parent flags; their parent restart/hard-crash scenarios passed.

| T3.2 check group | Result / boundary |
|---|---|
| Main PostgreSQL suite | 17 PASS, 0 FAIL, 0 NOT_RUN |
| Child-entry discovery | 2 expected SKIP; parent scenarios PASS |
| Auth/HBA preflight | PASS, including wrong-password SQLSTATE 28P01 |
| R4 literal-only CHECK regression | Both checksum/profile cases PASS |
| Advisory-lock concurrency | Observed blocked waiter; exactly-once receipt/effect PASS |
| Packaged offline unit/contract tests | 17 PASS, separate from the PostgreSQL suite |
| Genuine network lost-successful-COMMIT-response / remote TLS / extra server variants | NOT RUN |

Both R4 literal-only cases passed: ledger_checksum_hex and ledger_profile_clean.
The fixtures compared actual pg_get_constraintdef/pg_get_expr and retained the
CHECK name, type and count. Substituting only the regex literal caused ErrUnsafe,
with unchanged rows/receipts; restoring the correct CHECK allowed 026 with one
additional receipt and one non-idempotent effect. ConcurrentApply observed a
granted holder and distinct blocked waiter on the same advisory key, using
pg_locks, pg_stat_activity and pg_blocking_pids, within the unchanged 5-second
limit; after release both runners completed with one receipt and one SQL effect.

T3.3 independently reviewed the code and archived T3.2 evidence and accepted
R1-R6 within the documented E1A boundaries. P0=0, P1=0; its remaining documentation
P2 is addressed by T4.1. This is mechanism acceptance, not E1B completion or
permission to deploy. Restricted PostgreSQL privileges and separation of owners
remain E1B work; the production approval allowlist intentionally remains [].

Evidence project: `e1a-t32-0d0e3bd9b37941e8a778f5813249fa24`. The external archive
contains summary.json, test-results.csv, tests.log, build/runtime logs, auth/HBA,
identity/image metadata, source/evidence SHA256 inventories and verified cleanup
records. Hashes establish consistency, not independent provenance. Successful
CHECK definitions/full lock rows were asserted by tests, not printed in full.
See the [current test matrix](../../backend/test/e1a/README.md#current-t32-test-results)
for all 17 main entries and the two child SKIP entries.

At T4.1 the E1A files remain untracked: current Git HEAD does not contain E1A.
The tested source tree is identified by its file inventory over the base HEAD,
not by a commit containing these additions. A later separately authorized Git
checkpoint must bind its commit SHA to those build inputs and archived results.
No new tests or runtime actions are part of this documentation-only update.

## Acceptance history and E1A-T3.1 correction (historical)

E1A-T2.2 ran successfully on disposable Docker PostgreSQL 18.6, linux/amd64:
auth/HBA preflight PASS (wrong password SQLSTATE 28P01); 16 main integration
tests PASS, 0 FAIL, 0 NOT_RUN, and 2 expected child-entry SKIP. Parent restart and
both hard-crash phases passed. Suite/Compose exit codes were 0; no OOM occurred.
Evidence project: `e1a-t22-ac90aa941c5c427d85859fa4a745e552`, system identifier
`7705152489884045356`; logs, test-results.csv, summary and SHA256 inventory are
outside the repository in the Codex visualization workspace for that project.

The subsequent E1A-T3 source audit rejected acceptance: CHECK normalization used
token.text from the shared SQL envelope lexer, which intentionally omits quoted
contents. Therefore a literal-only regex substitution could pass ledger-shape
validation. Passing T2.2 did not exercise this case and does not prove R4.

T3.1 fixes ONLY the extension CHECK normalizer: quoted tokens are represented by
their exact original bytes (quotes included), with token kinds/boundaries retained.
ASCII unquoted word case and outside ASCII whitespace normalize; quoted contents
never normalize. Comments, E/dollar/Unicode quote syntax, invalid encoding and
unsupported symbols fail closed. The global SQL lexer remains unchanged. This
is a narrow catalogue comparison, not a semantic SQL equivalence engine.

New unit regressions cover checksum/profile regex literals, quoted case/content,
doubled quotes and identifiers, whitespace, unsupported forms and lexical errors.
An old-algorithm test witness demonstrates the original collision. New prepared
PG fixtures change ONLY the literal while checking actual pg_get_constraintdef
and pg_get_expr output, retaining name/type/count; rejected apply must leave all
business rows and receipts intact. Restoring the correct CHECK permits pending
026 with exactly one additional effect. They were NOT RUN at T3.1; T3.2 subsequently
passed both cases as recorded above.

The concurrent-apply fixture now observes the exact bigint advisory key in
pg_locks (classid/objid/objsubid), its granted holder and a distinct ungranted
waiter, pg_stat_activity Lock/advisory state and pg_blocking_pids. Observation is
bounded to 5 seconds; absence fails. Only then is the first runner released.
Two concurrent PostgreSQL backend processes (two Go runner goroutines) must then
complete with one receipt and exactly one non-idempotent SQL effect. This
strengthened PG fixture was NOT RUN at T3.1 and passed at T3.2.

The shared test-only run-suite.sh helper is exercised offline using a separate
Go test process: first probe intentionally fails; failfast must prevent a second
probe, report it NOT_RUN, preserve cause through tee, and return Go exit 1 with
tee exit 0. Probes are outside the PostgreSQL suite's test-name filter. This
checks the actual harness without connecting to PostgreSQL. Docker packaging
copies the same helper. T3.2 rebuilt the image and checked the packaged scripts
and test list before its suite. The negative offline harness passed at T3.1;
it was not rerun as a negative Docker scenario at T3.2.

Still unproved: genuine network loss of a successful COMMIT response, remote TLS,
additional server variants, restricted-role deployment, sequence-value preservation
and arbitrary business histories beyond the populated representative fixtures.
Owner/superuser SQL remains trusted; E1B privilege separation and release review
remain mandatory production gates. No production allowlist entries are added.

## R1: release approval and SQL trust

`extension_approvals.json` is embedded into the reviewed migration executable.
It currently contains `[]`: the shipped tool refuses every `apply`, before any
connection. There is no CLI/environment/external-file override for this list.
Changing it requires a reviewed release/build, not an operator-supplied checksum.
Protecting the executable and its distribution remains an operational trust gate.

Offline `plan` validates the separate extension manifest and SQL files and emits
`plan` plus `manifest_sha256`. This value identifies content; it grants no approval.
`--approved-manifest-sha256` is removed and rejected as an unknown CLI flag.
`status`/`verify` can inspect an unapproved plan with explicit validated target
credentials; they cannot apply it. Only test binaries contain the private fixture
approval helper; its dynamic approval never ships in the ordinary executable.

Only named local SQL files in the validated manifest are retained in memory.
No SQL/DSN argument, automatic discovery, `.env` or DATABASE_URL is used. Migration
SQL is trusted deployment code and must undergo independent safety/data-preservation
review before its manifest enters a release. The BEGIN/COMMIT lexer and checksum
are not a semantic SQL security verifier or sandbox: UPDATE/DELETE, dynamic SQL,
function calls and external/session effects cannot be declared safe by text filters.

## Manifest contract

JSON format 1: `profile=db04-extension-v1`, `canonicalization=utf8-lf-v1`,
`readiness_format=pg18-catalog-v2`, `baseline_sha256`,
`baseline_readiness_sha256`, and explicit `migrations` array (empty is valid).
Each entry has `version`, `name`, `sha256`, `order`, `readiness_sha256`.
Versions strictly increase above 24; ordinals start at 1 without gaps. Names have
`025_description.sql` form without path components. `baseline_sha256` hashes Go
JSON encoding of the validated original clean-v2 Plan. Per-entry cumulative hashes
bind the baseline, readiness format, SQL and readiness hashes of the whole prefix.
Appending is supported; changing an applied prefix is rejected.

Duplicate JSON keys (including nested objects, escaped names and case-fold aliases),
unknown fields, trailing JSON and excessive nesting are rejected. Semantic manifest
hashes normalize JSON layout, not arbitrary SQL or catalogue semantics.

## Explicit target, ownership and observed privileges

Online modes require all existing explicit Target flags: host, port, database,
user, schema, server-address, server-id, sslmode and explicit CA for verify-full,
plus deployment-id, baseline-owner and ledger-owner. Password comes only from
MIGRATION_PASSWORD. Base manifest defaults to migrations/manifest.clean-v2.json;
extension manifest has no default. Protected databases including tremor_sklad are
rejected before connection. Existing PG18/cluster/database/user/address/port/
locale/TLS inspection is retained. Startup is read-only, pg_catalog search_path.
Readiness inspection uses pg_catalog first; reviewed SQL executes with explicit
transaction-local target schema search_path. There is no SET ROLE/provisioning.

Baseline ledger owner must equal the explicit expected baseline owner. Extension
ledger owner must currently equal the authenticated user. `status` exposes both
ledger owners and effective INSERT/UPDATE/DELETE/TRUNCATE/column-UPDATE privileges
of that migration user. These facts are observed only on an explicitly inspected
target. T3.2 exercised these observations only in its disposable TEST cluster;
no existing deployment database was inspected.
The extension ledger is created by the migration user, so that user owns it.
E1A does not revoke its owner capabilities or provision role separation.

Every successful inspection includes the unresolved security gate
`E1B-ledger-privilege-separation-required`. Production admission requires E1B to
define owners, role inheritance/SET ROLE, DML/DDL permissions and protected ledger
access. A snapshot does NOT constrain malicious SQL with owner/superuser authority
that can alter and restore state, manipulate session behaviour or cause other
side effects. Do not claim absolute ledger immutability before that security gate.

## R2: full snapshots and atomicity

Before extension SQL, capture all baseline and previously applied extension rows:
version, name, checksum, profile/chain, ordinal and applied_at. Capture relation OIDs,
owners, table ACL and column ACL too. After SQL and the new receipt, but before
COMMIT, compare every immutable row/metadata field and require exactly one new
extension entry. A mismatch rolls back the transaction. Postflight also verifies
baseline lineage, deployment and approved readiness. No business-row emptiness
check, base-ledger rewrite, automatic data cleanup or destructive migration ships.

A separate schema_extensions ledger stores extension receipts. Initialization and
the first SQL step commit together; every later SQL step/receipt is atomic too.
The original ledger is never an output target of runner-generated SQL. Full-row
comparison detects persistent migration changes; it is not an adversarial-owner
security boundary. An attacker changing records outside a serialized run cannot
be defeated by a snapshot taken only afterwards; privileges remain essential.

## R4: ledger definitions

Validate ordinary table identity/owner/marker, shape, constraints, valid indexes,
defaults, RLS, inheritance, triggers and rules. CHECK validation is a one-to-one
mapping of name → contype=c → normalized token expression. Duplicate names,
missing/extra checks, wrong types and expression substitution under another valid
name fail. Normalization handles lexical spacing/case, not semantic equivalence
of different SQL expressions; quoted bytes are retained exactly. Unsupported or
unexpected server renderings fail closed. The live reader uses pg_get_expr;
new fixtures also inspect pg_get_constraintdef without stripping quoted contents.

## R3: bounded pg18-catalog-v2 readiness

Per-prefix catalogue hashes replace the old exact table/trigger-count requirement
after extension. Initial admission still runs the original bounded warehouse
object validator without testing business-table emptiness. Versioned readiness
supports schema changes only when the new catalogue is reviewed and release-approved.

Supported metadata projections:

- schemas: names, owners and ACL;
- ordinary tables/indexes/views/composite relations: inventory, persistence, RLS,
  options, owners/ACL, replica identity, inheritance; index definitions/validity;
- columns: names/types/nullability/identity/generated/defaults, attacl, collation,
  options and missing-default metadata;
- table constraints: definitions, validation/enforcement; user triggers/rules:
  definitions and trigger enabled state; view queries through rewrite rules;
- functions/procedures/window functions: definitions (including security/config),
  owners and ACL; policies: command/permissiveness, normalized role names from
  polroles, USING and WITH CHECK;
- domains: base type, nullability, default expression, owner/ACL, named domain
  constraint definitions and validation/enforcement; enum labels/order; composite
  and array type inventory/owner/ACL;
- sequence parameters (not current values); default ACL including grantor role;
  namespace-dependent object inventory.

Unsupported relation kinds (partitions, foreign/materialized tables/views), user
aggregates/ranges/custom scalar types, additional extensions, user casts and
unrecognized namespace-dependent classes are rejected by the v2 admission query.
They require a new reviewed metadata contract, not an accept-current-hash switch.
Even this bounded contract is not a universal pg_dump/security drift comparison:
role memberships/attributes, global cluster/database settings and arbitrary code
semantics/dependencies are outside its proof. Unknown gaps need explicit review.

Business data, sequence current values and the separately validated extension
ledger are excluded from the catalogue hash. Baseline ledger metadata remains
included. Digests are schema/owner/ACL/search-path/PostgreSQL-rendering sensitive;
expected fingerprints must be captured/reviewed on the approved disposable PG18
fixture. No automatic production-current-catalogue approval exists.

## R6: unknown COMMIT and verification

Apply serializes with base runner/bootstrap through their session advisory lock.
External administrator SQL must be excluded operationally; this advisory lock does
not stop unrelated sessions. status/verify use read-only repeatable-read snapshots;
verify fails on pending extensions. Errors never become silent apply success.

On unknown COMMIT, close the old connection and use a fresh target-inspected,
read-only snapshot. Compare the full expected committed prefix (timestamps and
ledger metadata included), baseline and readiness. Confirmed incomplete progress
returns ErrResume; complete confirmed progress succeeds. Missing records,
reconnect errors, identity/readiness failures or snapshot mismatch return
ErrCommitUnknown with the underlying error preserved. The recovery capability
has no execution/retry callback. A later explicit apply rechecks the durable
prefix under the deployment lock; it does not trust an in-memory step counter.

Offline tests cover approval substitution, snapshots/metadata, CHECK mapping,
JSON ambiguity, recovery classification/failures and fresh manifest reload.
SQL projection contract tests alone are static; T2.2 and T3.2 additionally exercised
the prepared PG18.6 metadata cases, not every possible catalogue state.
Prepared integration tests additionally require EXTENSION_TEST_DISPOSABLE_APPROVED=yes
and the existing explicit MIGRATION_TEST_* identity guards. They only use a newly
created fixture schema in the separately approved disposable TEST database.
The original T2.2 set and the strengthened T3.2 set ran as recorded above.
Any further run needs separate permission; an environment flag is not itself
user permission.

Prepared cases include rollback and ledger tampering, committed lost reply,
absent commit, failed recovery validation, concurrent apply, actual test-process
restart, named CHECK substitution and metadata mutations. Ownership mutation
requires an existing explicitly supplied EXTENSION_TEST_ALTERNATE_OWNER with the
necessary fixture memberships/privileges; tests create no roles. Network failure
classification is tested offline. T3.2 also closed the client TCP socket before
COMMIT, but did not drop a successful COMMIT response in flight. Genuine network
lost-successful-COMMIT-response, remote TLS/CA/hostname and additional server
variants remain NOT RUN and need a separately agreed harness/target. No legacy
destructive CLI is invoked.
