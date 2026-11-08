# DB-04 / E1B-I2.1 security collector

Scope: collect security evidence from an explicitly approved PostgreSQL 18 target.
No security transition, provisioning, API, E1A runner change, production admission,
dispatcher or migration-step functions. Production transition approvals remain empty.
Existing deployed PostgreSQL roles/ACL remain UNKNOWN; only disposable TEST ran.

## Admission and API

Entry review of I1-F1 at `5d80b1ba03feecc1cebe3ceb4d6fbd194970e414`:
39 offline tests passed, including both original incomplete-inventory reproductions.
No blocking regression found. The bounded metadata vocabulary now includes views
and their column identities, required for the existing baseline read view. Ledger
requirements remain ordinary tables; views cannot masquerade as ledgers.

`CollectSecuritySnapshot(ctx, Target, ApprovedSecurityTransition, phase,
ExtensionManifest)` accepts only sealed release approval, a source/final phase,
and the matching original manifests. No DSN, raw SQL, approval hash override,
operator-provided fingerprint or verified flag. Descriptor payload is never executed.

`CollectedSecuritySnapshot` has private fields. Only a completed, validated read-only
observation is sealed. Errors return a zero result, never usable partial evidence.
Snapshot/privilege/path accessors copy data. Returning an untrusted DTO does not
transfer the collected-result provenance. `BindCollectedSecurityTransition` combines
collected source evidence with offline binding. `ClassifyCollectedTransitionRecovery`
uses the receipt actually read from PostgreSQL, not `ExpectedReceipt()` supplied by
the caller. Pure offline APIs remain pure and do not prove PostgreSQL origin.

The collector does not create approval. Synthetic dynamic approvals exist only in
test helpers. A truthful collected snapshot may differ from approved metadata:
collection proves coverage/origin, while binding/comparison decides conformity.

## Connection and consistency

Uses unchanged `Target.connect/inspect`: explicit host, port, database, user,
server address/system identifier, PG18, locale/encoding/TLS guards, read-only startup
and no ambient PG fallbacks. Database/user/system identifier/OID are rechecked
inside one REPEATABLE READ READ ONLY transaction; deployment identity must be an
unambiguous singleton matching the approved UUID. No other connection contributes
to the observation. UTC normalizes row evidence. `row_security=off` makes silently
filtered table reads fail, without granting BYPASSRLS to the connection role.

Missing `pg_control_system()` or required table/sequence/catalog privileges is an
error. TEST observer received EXECUTE only on that exact function plus explicit
TEST reads. Runtime API received no monitor role or identity-inspection escalation.
Callers must supply a bounded context and collect during quiesced deployment.

Sequence last_value/is_called are not MVCC: read twice on the same connection,
reject observed movement. This does not prove absence of concurrent ABA or external
effects. A read-only transaction is not a lock against a malicious administrator.

## Evidence collected

- Cluster/database/deployment/schema identity and physical OID/column attnum.
- Database owner/ACL/connection settings are bound into the target-schema
  definition digest; the owner must resolve inside approved role scope. Effective
  CONNECT/CREATE/TEMPORARY are collected too: database CREATE must not evade
  detection merely because existing schema ACL did not change.
- Full baseline lineage using unchanged E1A reader, explicit extension presence,
  extension shape/CHECKs/marker and exact manifest prefix; no baseline emptiness gate.
- All required scoped roles, attributes, configuration digests, direct membership
  edges with grantor/ADMIN/INHERIT/SET, and server-inquired transitive role paths.
- Owners and NULL/default/explicit ACL for schemas, ordinary tables, views,
  sequences, columns and exact function/procedure signatures.
- SECURITY DEFINER, full function definition/config/dependency digest and parsed
  search_path (unsupported quoted/path syntax fails rather than being guessed).
- Global and schema-local defaults by approved creator; explicit resolved default
  ACL semantics are fingerprinted even when pg_default_acl has no row.
- Relation definitions, columns/defaults, constraints, index validity, triggers,
  rules, RLS policies, sequence parameters and described dependencies.
- Ledger owner/ACL/OID/shape/marker/constraints and every historical row field.
- Full scoped ordinary-table rows, identity, business receipts and sequence state.
  Historical transition receipts are preserved; only the current transition ID is
  excluded from the immutable history hash and read separately for classification.

ACL tuples are sorted in Go using the contract ordering. Empty ACL arrays are
handled without treating NULL as no privileges. Server `has_*_privilege` inquiries
include column/table interactions, grant options, PUBLIC, superuser and implicit
membership semantics. Effective privileges and transitive paths are bound into
compared role fingerprints, not left as an ignorable auxiliary report.

Scoped schemas are discovered independently for extra/missing relations, columns
and routines. Supported scoped objects and their dependencies must match the
approved scope; unsupported catalog classes fail. Existing pg18-catalog-v2 readiness
is collected as well. Data hashes stream length-framed canonical JSON rows sorted
with C collation; a table exceeding 128 MiB evidence is refused rather than truncated.
This is a bounded collector, not a general PostgreSQL dump or SQL safety verifier.

## Disposable verification

Uses existing `test/e1a/compose.yaml` with `test/e1b/compose.collector.yaml`; no new CI
framework and no E1A utility edits. Linux/amd64 test binary is freshly compiled from
this checkout (`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c`). Mount only that
binary read-only; existing image contains the unchanged baseline files.

Pinned local TEST image:
`tremor-e1a-test@sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2`.
It contains PostgreSQL 18.6 (Debian 18.6-1.pgdg12+2), linux/amd64. Each run has its
own random Compose project, new password, tmpfs PGDATA and fresh system identifier.
No host port, internal network, shared network namespace with the TEST server only,
PostgreSQL listens on 127.0.0.1. `--env-file NUL`, `--no-build`, pull_policy never;
no project secrets. Inspect only own labeled container/network IDs before down.
Retained evidence records exact image, system identifier, cases, exit codes and
zero remaining own containers/networks/volumes. Existing images/resources untouched.

Run only `TestSecurityCollectorIntegrationDisposable` with the explicitly approved
TEST environment. The tests create synthetic roles/data/ACL under their new TEST
administrator; the collector itself stays read-only. No ledger ownership transition.

Positive scenarios cover repeatability, precise target/owner/signature, direct and
transitive memberships including ADMIN/SET/INHERIT, column privileges, defaults and
explicit extension-ledger absence. Negative scenarios cover missing role/schema/
function, unexpected object/GRANT/PUBLIC EXECUTE, dangerous SET ROLE, changed owner/
SD context, unreadable/RLS-filtered data, foreign deployment and conflicting DTOs.
Ledger OID and one-inventory-only negatives perturb copies of real observations;
they do not replace the real baseline ledger. A TEST receipt without the actual
transition is rejected, not classified as a successful COMMIT.

Actual API-login SELECT/function calls succeed as allowed; column reads, DML, DDL,
SET ROLE and ledger writes are checked for typed SQLSTATE 42501. End-of-suite checks
prove unchanged baseline ledger evidence, deployment identity and sequence state.

Final I2.1 run: `e1b-collector-b59026c474364ce593cbe8e33048d4e3`, system identifier
`7705824172199755819`. **19 PostgreSQL subscenarios PASS**, parent PASS, no SKIP;
Compose/test exit 0, PG exit 0, OOMKilled=false for both containers. Runtime
inspection confirmed internal network and no host ports. All eight own TEST
projects from development were independently checked: zero containers, networks
and volumes remain. Earlier failed development runs are retained in evidence;
their resources were also removed. Offline suite: **41 PASS**, Go vet and formatting
PASS using Go 1.27.3. TEST binary SHA256:
`0c2913300cbf4761b9df8a1774ea00c078c5c09085715e6cb53e308bdfd5dd62`.

## Boundaries for I2.2

Only the collector/contract integration is tested. No real protected transition,
final protected deployment, transition crash/unknown-COMMIT transport experiment,
remote TLS or production warehouse history acceptance is established. I2.2 needs
separate authorization, independent review and a disposable transition transaction.
Release approval remains a distinct reviewed release operation. Do not enable API
writes or production deployment on the strength of this collector suite.

PostgreSQL references: [privilege inquiries](https://www.postgresql.org/docs/18/functions-info.html),
[membership catalog](https://www.postgresql.org/docs/18/catalog-pg-auth-members.html),
[default ACL semantics](https://www.postgresql.org/docs/18/catalog-pg-default-acl.html).
