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

## И2.1-F2: catalog session defaults

The collector previously fingerprinted `pg_roles.rolconfig`, which exposes only
role-wide settings (`setdatabase=0`). It missed database-wide and role+database
rows, as well as global all-role/all-database defaults. The previous 19 PG cases
never changed these settings. Consequently ordinary binding and recovery received
incomplete evidence despite structurally complete object inventories.

### Contract and scope

Every present scoped role now has mandatory `session_defaults` metadata containing
exactly four ordered `TransitionSessionLayer` values: `global`, `database`, `role`,
`role_database`. Each has an explicit `present` and non-null, ordered, unique list
of typed `TransitionSessionSetting{name,value}`. Absent and unreadable differ:
confirmed absence has `present=false, settings=[]`; missing/null layers fail.
Non-role metadata cannot carry nonempty session defaults.

The enclosing role and target specify logical identities. Observed database OID
and role `ObjectIdentities` specify physical identities; release payloads do not
pin target-specific role OIDs. The fixed query selects these exact OID pairs:

| Layer | setdatabase | setrole |
| --- | --- | --- |
| global | 0 | 0 |
| database | target DatabaseOID | 0 |
| role | 0 | scoped role OID |
| role_database | target DatabaseOID | scoped role OID |

All applicable rows are retained, even if overridden. Other databases/roles are
excluded. Unexpected or duplicate OID pairs, null catalog arrays, missing roles,
invalid references and query errors fail collection. `pg_roles.rolconfig` is read
again inside the same REPEATABLE READ read-only transaction and reconciled exactly
with each role-wide layer, including NULL/absence. Shared global/database layers
must agree across every scoped role.

The typed layers live in release-approved `Before`/`After`, so their changes need
explicit metadata deltas and independent release approval, just like other role
metadata. They enter role `DefinitionSHA`, metadata digests, descriptor digest,
target-bound snapshot digest, collector seal, binding, comparison and recovery.
No legacy fingerprint path silently accepts a descriptor missing the field.
The existing JSON decoder enforces mandatory fields, exact types, duplicate and
unknown field rejection for these nested structures too. Existing test fixtures
now explicitly specify all four absent layers; checks were not relaxed.
Production release approval remains empty; the collector cannot approve policy.

### Parameter policy and boundaries

The reviewed v1 subset accepts only these settings and values:

- `session_replication_role=origin`; `row_security=on`.
- `check_function_bodies`, `standard_conforming_strings`, `escape_string_warning`:
  `on`; `transform_null_equals`: `off`; `client_encoding`: `UTF8`.
- `backslash_quote`: `off` or `safe_encoding`.
- `search_path`: a restricted unquoted list beginning with `pg_catalog`, ending
  with `pg_temp`, without duplicates; other entries must be protected scoped schemas.
- `default_transaction_read_only`, `default_transaction_deferrable`: `on`/`off`;
  isolation: `read committed`, `repeatable read`, `serializable`.
- `statement_timeout`, `lock_timeout`, `idle_in_transaction_session_timeout`,
  `idle_session_timeout`, `transaction_timeout`: bounded nonnegative decimal
  durations with optional `ms`, `s`, `min` units.

This is a deliberate fail-closed subset, not a classification of every PG GUC.
Unknown/custom settings and all unsupported values fail, even if an operator
computes a new digest. Supporting another setting requires reviewed code/policy.
Within this subset, a value is still accepted only when it exactly matches the
independently approved source/final metadata. Values are never included in error
messages or test logs; formatting an individual setting redacts its value.
Canonical JSON contains policy data and must not be used as an operational log.

`CatalogLoginOverrides` derives only catalog overrides in increasing precedence:
global < database < role < role_database. It is not proof of the complete effective
runtime state. Server configuration, connection startup parameters and later
session SET are separate sources, and are not certified here. There is no new
runtime-setting collector. TEST-only fresh connections compare the selected
catalog-derived override with actual `pg_settings`; SET ROLE does not reapply
role login defaults.

References: [pg_db_role_setting](https://www.postgresql.org/docs/18/catalog-pg-db-role-setting.html),
[ALTER ROLE precedence and login behavior](https://www.postgresql.org/docs/18/sql-alterrole.html).

### Reproduction and verification

Original collector plus permanent new regression failed on a new isolated PG18.6:
a TEST API role+database `session_replication_role=replica` took effect on a fresh
login, but evidence was unchanged and recovery returned SOURCE_CONFIRMED. This
proves false source confirmation, **not** an observed false PROTECTED or COMMIT.
Old-code cluster system identifier: `7705865891418869803`.

Fixed-code PostgreSQL run: 27/27 scenarios PASS (all previous 19 plus 8 new).
Covered four catalog layers, new-login precedence, SET ROLE distinction, stable
snapshots, unrelated-database exclusion, unchanged rolconfig with changed target
settings, unsafe replication/search_path/RLS settings, unsupported custom GUC,
unapproved database defaults, ACL/membership/ledger/receipt regressions and real
runtime permission denials. Contradictory evidence cannot confirm recovery.
Offline tests cover missing layers, duplicate fields/settings, wrong types/nulls,
unknown role/database references, incomplete reads, rolconfig disagreement, shared
layer contradictions, approved final-state defaults and rejection of mutated
settings even with a matching receipt. No PostgreSQL transition was performed.

PostgreSQL: 18.6 (Debian 18.6-1.pgdg12+2), system identifier
`7705869010502365227` for the expanded passing run. Pinned local image:
`tremor-e1a-test@sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2`.
Unique project: `e1b-collector-eda18c47649845e4a906f8e8c01f8fd6`.
No host ports, internal network, synthetic TEST database and temporary credentials.
All three F2 disposable projects were cleaned, each with zero remaining project
containers, networks and volumes. External evidence logs are kept outside Git.

No baseline, manifest, E1A runner, API, production allowlist, ownership transition
or production connection changed. This fixes the entry blocker for resuming the
separately authorized И2.2 work; it does not implement that work or claim
production readiness.

Final offline verification: **45/45 top-level tests PASS** (all previous 41 plus
four new groups), 268 passing test/subtest events; `go vet` PASS; `gofmt` reports
zero unformatted files; `git diff --check` PASS. Synthetic query-boundary fault
injection is labelled separately from the real PostgreSQL evidence.

## И2.1-F3: PostgreSQL parameter privileges

### Root cause and actual reproduction

F2 collected login defaults but omitted the distinct cluster-wide
`pg_parameter_acl` privilege catalog. Role attributes, memberships, `rolconfig`
and `pg_db_role_setting` do not encode a parameter GRANT. The E1A readiness
catalog does not cover it either; that comparator remains unchanged.

A permanent PostgreSQL regression was first run against F2 code before the fix.
In a new isolated PG18.6 cluster, TEST administrator granted SET on
`session_replication_role` to `tremor_api`; `has_parameter_privilege` confirmed the
permission. The old collector returned the identical snapshot and recovery
returned SOURCE_CONFIRMED. The regression failed as intended. This proves false
source confirmation, not a real false PROTECTED or successful transition COMMIT.
Original-code system identifier: `7706069491071791147`.

### Mandatory approved scope and typed evidence

`TransitionScope.Parameters` is an independently approved, ordered, unique list
for each source/final phase. `row_security`, `search_path` and
`session_replication_role` are mandatory and cannot be removed. The current
reviewed vocabulary additionally permits `statement_timeout` and `lock_timeout`.
Both optional names must be explicitly listed to include their confirmed absent
state. Unknown names/contexts fail until reviewed code and policy support them.

Every present scoped role has mandatory `ParameterPrivileges`, containing the
exact approved names. Each record contains canonical name, PG context, bootstrap
ACL owner, catalog-row presence, ACL mode and resolved grants, intrinsic USERSET
status, effective SET / ALTER SYSTEM, and both effective grant options. Raw grant
records retain grantor/grantee/PUBLIC, privilege and grant option. Shared catalog
facts must be identical across roles; effective rights differ by actor.

`TransitionSnapshot.ParameterIdentities` separately contains each name and
observed `pg_parameter_acl` OID. OID zero means confirmed absent row, never a failed
read. Physical OIDs are not put in release descriptors. Existing nonzero OIDs are
preserved when the row remains present across a transition comparison; explicitly
approved creation/removal has distinct presence metadata.

The collector reads **all** rows of the shared catalog and unions the approved
names to represent absence. Thus a new grant cannot hide by omitting its parameter
from operator-supplied scope. An extra unapproved name or unknown grantor/grantee
causes refusal. All ACL references must resolve within approved role coverage;
PUBLIC is the one pseudo-role exception. A pre-existing grant to another cluster
role requires explicitly approved coverage of that reference, rather than silent
filtering. This is not a request for all cluster roles to be inventoried.

Queries are fixed SELECT statements, executed inside the existing single
REPEATABLE READ read-only transaction. The collector also calls
`has_parameter_privilege` for each actor/name and SET / ALTER SYSTEM, with and
without WITH GRANT OPTION. Complete actor/name coverage is required; duplicate,
unknown, omitted or unreadable results fail. The validator independently checks
ACL/effective agreement using direct and inherited privileges, PUBLIC, actual
superuser attributes and PostgreSQL's implicit bootstrap-owner grant options.
NULL ACL on an existing row is resolved using PG parameter default ACL semantics;
absence of a row is represented separately.

`has_parameter_privilege(SET)` is not a test of every ordinary session SET:
PostgreSQL USERSET parameters can be changed without any delegated parameter
privilege. `IntrinsicUserSet` is therefore separate and context-validated. The
real TEST API can SET `row_security=on` without such a GRANT, while privileged SET
of `session_replication_role` is denied before a grant. No blanket SET ban exists.

These records enter role DefinitionSHA, metadata/descriptor digests, ScopeSHA,
bound snapshot digest and collector seal. Binding, source/final comparison and
both recovery/state classifiers all use the new mandatory evidence. Unexpected
supported grants/options change fingerprints and reject the previous approval.
Unsupported or unreadable evidence cannot produce a usable sealed collection.
No code path converts a catalog error into empty ACL. Missing fields in older
JSON descriptors fail strict decoding; rebuilding a digest does not independently
approve the replacement descriptor. Production release approval remains empty.

This data certifies catalog permissions, not server configuration or every actual
runtime setting. Session defaults, connection options and subsequent session SET
remain distinct categories. Parameter queries read names/context/permissions,
never GUC values, and diagnostics do not publish settings values.

References: [parameter ACL catalog](https://www.postgresql.org/docs/18/catalog-pg-parameter-acl.html),
[parameter GRANT](https://www.postgresql.org/docs/18/sql-grant.html),
[effective privilege inquiries](https://www.postgresql.org/docs/18/functions-info.html),
[PG18 ACL-mask implementation](https://github.com/postgres/postgres/blob/REL_18_STABLE/src/backend/catalog/aclchk.c).

### One-time coverage of agreed requirements

This table covers the agreed contract and approved objects/creators, not every
possible PostgreSQL object or unobserved runtime action. No additional proven
critical omission was identified within these categories during this check.

| Security requirement | PostgreSQL source | Collector field | Validator | Regression |
| --- | --- | --- | --- | --- |
| Role attributes and memberships | pg_roles, pg_auth_members, pg_has_role MEMBER/USAGE/SET | Metadata.RoleFlags/Memberships/DefinitionSHA; RolePaths | validateTransitionScope, exact metadata comparison | missing-role, dangerous-set-role, complete-repeatable-source; offline role/membership negatives |
| Database/schema privileges | pg_database.datacl/owner, pg_namespace.nspacl, has_database_privilege/has_schema_privilege | target schema DefinitionSHA binds database security; schema ACL/owner; role DefinitionSHA and privilege report bind effective rights | scope/reference validation and approved metadata comparison | unexpected-database-CREATE, missing-schema, real-runtime-denials |
| Object ownership and ACL | pg_class, pg_attribute, aclexplode, has_table/column/sequence_privilege | Metadata.Owner/ACL, separate column objects, ObjectIdentities, effective privilege report | transitionMetadataIndex, validateTransitionScope/Identities, CompareSecurityTransition | changed-owner, unexpected-grant, column SELECT positive and runtime denials |
| Functions and SECURITY DEFINER | pg_proc, pg_get_functiondef, dependencies, has_function_privilege | Owner, SecurityDefiner, SearchPath, ACL, DefinitionSHA | execution-context delta requirement and exact metadata comparison | changed-SD, public-execute, missing-function; offline SD-context negatives |
| Default privileges | pg_default_acl, acldefault, approved creators and global/local scopes | default_acl metadata, Mode/Grants/DefinitionSHA | required global/schema-local creator coverage and exact deltas | complete-repeatable-source global/local assertions; offline default_acl mutation |
| Session defaults | pg_db_role_setting, pg_roles.rolconfig | Metadata.SessionDefaults, CatalogLoginOverrides | validateSessionLayers, fixed approved scope and binding/recovery | prior F2 precedence/new-login, role+DB drift, RLS/search_path negatives, incomplete-read tests |
| Parameter privileges | all pg_parameter_acl, pg_settings context, aclexplode, has_parameter_privilege | Scope.Parameters, Metadata.ParameterPrivileges, ParameterIdentities | validateParameterScope/Privileges/Identities and exact comparison/recovery | parameter-grant-drift-regression, PUBLIC, inheritance, grant option, ALTER SYSTEM privilege, unknown grantee, catalog-read-denied; offline completeness/JSON/effective contradictions |
| Ledger identity and integrity | pg_class, baseline/extension ledger rows, marker, constraints, indexes, scoped data/receipts/sequence reads | Ledgers, ObjectIdentities, Frontier, readiness and immutable hashes | unchanged E1A shape/prefix checks; cross-inventory validation; sameTransitionLedgers/CompareSecurityTransition | ledger-and-identity-preserved, historical-transition-receipt, contradictory-offline-evidence; offline OID/receipt/frontier negatives |

### Test boundaries

New PostgreSQL cases cover absent grants, intrinsic USERSET, TEST-approved explicit
USERSET grant, direct SET drift, ALTER SYSTEM privilege, PUBLIC, inherited grant,
WITH GRANT OPTION, an additional reviewed parameter, unknown grantee, unsupported
parameter and real catalog SELECT denial for TEST observer. No actual ALTER SYSTEM
command was executed. Error/partial-stream injection is labelled offline, while
catalog permission denial is a real PostgreSQL test.

Offline tests also reject missing/substituted scope, omitted parameter inventory,
unknown references, duplicate grants/results, contradictory effective permissions,
malformed JSON, physical-identity mismatches, identical incomplete source/final
and contradictory recovery with a matching synthetic receipt. The approved
metadata-delta positive is an offline contract test, not a real security transition.

No roles/ownership/ledger transition, migration apply, API work, production
connection, new CI framework or E1A modification belongs to this fix.

### Final F3 verification and limits

- 52/52 offline top-level tests PASS (all previous 45 plus seven new groups),
  295 passing test/subtest events. Old/new test-name comparison found zero missing
  previous offline cases.
- 38/38 PostgreSQL scenarios PASS, including all previous 27 plus 11 new cases.
  Old/new scenario-name comparison found zero missing previous PG cases.
- go vet PASS; gofmt reports zero unformatted files; git diff --check PASS;
  secret-pattern scan reports zero hits, followed by manual review of TEST-only
  credentials handling (no new literal credentials).
- Final run: PostgreSQL 18.6 (Debian 18.6-1.pgdg12+2), system identifier
  `7706074271493242924`, project
  `e1b-collector-90d3eeddf4db4ffb827df698c85be474`.
- Fixed local image digest:
  `tremor-e1a-test@sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2`.
  Runtime inspection confirms no host ports and an internal network.
- All five F3 projects (original failing regression, one Compose startup failure,
  and three passing runs) were removed; post-cleanup label-filtered queries found
  zero project containers, networks and volumes. The Compose healthcheck startup
  failure did not run Go tests and is not counted as PASS; fresh later runs passed.
- Main SHA/tree and every main-tracked file remain unchanged. All seven user
  untracked-file SHA-256 values match the saved fingerprints. Feature changes are
  confined to 11 E1B collector/contracts/tests/docs files.

The reviewed parameter-name/context vocabulary is intentionally small. Broader
names need reviewed policy/code support. Pure effective-ACL reconciliation handles
the explicitly scoped inheritance graph, superuser attributes and bootstrap-owner
rights; any additional implicit-right case it cannot reconcile is rejected, not
silently accepted. Shared-catalog references outside approved role coverage also
fail closed. Actual runtime configuration and session actions remain separate
from these catalog facts. Offline DTOs provide structural consistency, not DB
origin proof; only a completed collector result has its private collection seal.
No security transition, protected migration apply or actual ALTER SYSTEM command
was executed. The fix permits resuming the separate И2.2 entry checks; it is not
production admission or proof that И2.2 is already implemented.

## И2.1-F4: legacy readiness context compatibility

### Root cause and unchanged E1A contract

E1A `ExtensionRunner.inspect` executes `SET LOCAL search_path TO pg_catalog,` plus
its safely quoted `Target.Schema` before calling `extensionCatalog`. E1B had
called the same function with the connection's `pg_catalog` path. The SQL is the
same, but its text projection uses visibility-sensitive PostgreSQL deparsers:
`pg_get_constraintdef`, `pg_get_expr`, `pg_get_viewdef`, `pg_get_indexdef`,
`pg_get_functiondef`, `pg_get_triggerdef`, `pg_get_ruledef`, `format_type` and
`pg_describe_object`. No text replacement can safely reconcile these definitions.

The original FAIL was reproduced again on HEAD 9bbe711 before changing code, in
one REPEATABLE READ READ ONLY transaction on an unchanged clean-v2 baseline:

- collector path `pg_catalog`:
  `a5fb566fbaaebceffefab307678c3b5e06c71573974ce32ccf5784c24b9bc489`;
- E1A path `pg_catalog,public`:
  `e648ad5a6084d251016ed325bc306fe214f0d460be119cb834c4903a890270f7`.

The same FK was rendered as `REFERENCES public.warehouses(id)` versus
`REFERENCES warehouses(id)`. Original-failure system identifier:
`7706102551411097643`. This was a compatibility failure; no false PROTECTED,
transition COMMIT or privilege escalation was claimed.

### Narrow E1B adapter

`collectorLegacyReadiness` validates the schema using the existing target schema
rules, captures the current path, sets the exact E1A path using
`pgx.Identifier{Target.Schema}.Sanitize()`, calls the **unchanged**
`extensionCatalog`, then restores the exact saved path via parameterized
transaction-local `set_config`. Setting, reading or restoring failure returns an
error and no successful readiness hash. Caller SQL or descriptor SQL is never
executed. There is no independent copy of extensionCatalogSQL.

At the internal collector transaction boundary, metadata/data collection now
uses canonical `pg_catalog`, UTC and row_security=off regardless of caller state.
The wrapper saves all three settings and restores them on every return; a failed
restoration discards the result. The public collector seals a result only after
successful collection/context restoration and completion of its read-only
transaction. This also makes the internal path safe to reuse after an E1A-style
inspection has changed search_path. No default role/database settings are changed.

Target.Schema is not hardcoded to public. A real `warehouse_scope` schema and a
real quoted reserved-word schema `"order"` were tested. Mixed-case, embedded-quote
or otherwise unsupported logical schema names remain rejected under the existing
E1A Target.Validate rules; those guards were not broadened.

### Two separate fingerprints and approval boundaries

- `TransitionSnapshot.ReadinessSHA` remains the legacy `pg18-catalog-v2` projection
  in E1A's visibility context. It is not replaced with a security-metadata hash.
- `CollectedSecuritySnapshot.SecurityFingerprint()` exposes a versioned,
  deterministic E1B metadata fingerprint binding target/database OID, approved
  scope, metadata, object identities and parameter identities. Metadata already
  includes roles/memberships/effective rights, ACL, SD context, defaults and
  parameter privileges. This fingerprint excludes legacy readiness and business
  rows; the existing bound snapshot digest separately binds the complete evidence.
- Neither fingerprint approves a release or creates a transition anchor. Existing
  binding/recovery checks remain unchanged and require independently approved
  source/final state. A pre-F4 collector-context hash cannot silently become an
  approved legacy checkpoint. The production allowlists remain empty.

Ownership, ACL or other catalog changes can legitimately change legacy readiness.
A previous manifest hash must not be treated as valid after such changes. A future
security transition needs its separately approved exact-frontier anchor; F4 does
not implement that transition or anchor activation.

### Immutable manifest evidence

There is no production extension readiness manifest shipped in this checkout;
the production extension approval list is empty. The accepted baseline manifest
contains migration checksums, not a legacy catalog SHA. Tests therefore use a
fixed TEST release manifest literal in `security_readiness_integration_test.go`:
its baseline digest is
`f4f65a9dd08e5f3357671294cf344808f821e191bb2612e2e3f2a28bc63a32d5`
and its readiness SHA is the pre-F4 E1A value `e648ad5a...` above. These values are
not computed from the current collector output. The literal is loaded through
unchanged LoadExtension, accepted by unchanged ExtensionRunner.verify, compared
with E1B output, and byte-checked unchanged afterward. The malicious-hash negative
uses a separate temporary TEST document, never rewrites this fixed artifact, and
is rejected by the unchanged comparator/production apply allowlist.

This is TEST approval evidence, not a production-release authorization claim.
No baseline SQL, accepted manifest, E1A code, comparator, or approval list changed.

### Results

- 56 offline top-level tests PASS: all previous 52 plus four new groups. Name-set
  comparison found zero missing previous cases. go vet PASS; formatting clean;
  git diff --check PASS; secret-pattern scan plus manual review performed.
- 47 PostgreSQL scenarios PASS: all previous 38 plus nine new readiness cases.
  Name-set comparison found zero missing previous scenarios.
- New cases prove raw deparser divergence; exact E1A/E1B/fixed-manifest equality;
  catalog-drift refusal; refusal of the old incorrect hash; precise non-public
  and quoted-schema context; full snapshot and fingerprint stability across caller
  path/timezone/RLS settings; restoration of caller context; and legacy-readiness
  stability versus changed security fingerprint after a parameter GRANT, with
  binding/recovery refusal of that security drift.
- SET/read/restore/catalog-error paths are covered by offline driver faults.
  Nested legacy-restore and outer caller-restore faults are also injected into
  otherwise real PostgreSQL collection: no valid/bindable result is returned.
  Those faults are explicitly driver injection, not a claimed real server crash
  or network failure. Real PostgreSQL context restoration was checked directly.

Passing run: PostgreSQL 18.6 (Debian 18.6-1.pgdg12+2), system identifier
`7706105037277929515`; project
`e1b-collector-ba5523eb23cd43f19f24ca2960274607`.
Pinned image:
`tremor-e1a-test@sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2`.
The existing harness used an internal network, no host ports, new TEST credentials,
synthetic data and resource limits. Both F4 projects (original FAIL and fixed PASS)
were removed, with no project containers/networks/volumes left.

The remaining boundaries are unchanged: catalog evidence does not certify every
server/connection/runtime setting; offline DTOs are not PostgreSQL-origin proof;
unsupported metadata fails closed; and I2.2 must still implement its own approved
transition/apply protocol. F4 permits resuming I2.2 entry checks, not production
admission. No security transition, protected apply, API work or new CI framework
was implemented.

Sources checked:
[PG18 deparser source](https://github.com/postgres/postgres/blob/REL_18_STABLE/src/backend/utils/adt/ruleutils.c),
[PG18 catalog information functions](https://www.postgresql.org/docs/18/functions-info.html).

F4 changed files: new `security_readiness.go`, `security_readiness_test.go` and
`security_readiness_integration_test.go`; integration updates in
`security_collector.go` / `security_collector_integration_test.go`; this collector
report and `DB04_E1B_SECURITY_PROTOCOL.md`. All are E1B components. Main SHA/tree,
all main-tracked files and the seven user-file SHA-256 values were verified
unchanged before the single local commit.
