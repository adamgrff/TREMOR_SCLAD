# DB-04 / E1B-I1: offline security-transition contracts

Status: offline implementation only. No PostgreSQL inspection, provisioning,
SQL execution, runner integration, API change, or production admission.
Architecture A: trusted `tremor_deployer` applies reviewed SQL through the runner;
limited `tremor_migrator` is status/verify-only. No dispatcher or migration-step
functions. Actual deployed roles, owners and ACL remain **UNKNOWN**.

F1 corrects admission of incomplete/contradictory evidence. The original I1
descriptor shape is no longer admitted: source_scope/final_scope are required and
included in the release digest. There were and remain no production approvals.

## Trust and public API

`DecodeSecurityTransition` validates a typed descriptor and exact payload bytes;
its digest is content identity, **not permission**. `LoadApprovedSecurityTransition`
has an empty production approval policy and rejects every descriptor in I1.
Only package-private test fixtures supply synthetic release approvals. No caller,
CLI flag, environment variable or external approval file can enable this protocol.
`ApprovedSecurityTransition` and `BoundSecurityTransition` have private fields.
No existing online entry point references these components.

Future release integration must pin descriptor digests in reviewed executable
policy. Protecting executable distribution and the trusted administrator remains
an operational boundary. Protection from malicious trusted administrator SQL is
not claimed. A valid descriptor is not an automatic safety review of its grants.

## Descriptor and canonicalization

Protocol: `e1b-security-transition-v1`. Required fields:

- transition ID; exact payload SHA256;
- cluster system identifier, database, schema, deployment UUID;
- baseline manifest and baseline lineage SHA256; extension manifest SHA256;
- ordered exact applied extension entries (version/name/checksum/chain/ordinal);
- source and final catalog readiness SHA256;
- complete ordered before/after metadata inventories and explicit change list;
- receipt location fixed to `tremor_deployment.security_transitions`, format 1.
- separate source/final scopes defining exact presence/absence, object identities,
  schema/parent references and applicable object-creator default privileges.

The baseline lineage digest binds the complete validated baseline, not merely a
row count. The future collector must calculate it from the original manifest and
validated ledger. Extension entries start above 24 and ordinals start at 1.
Even an empty extension prefix requires an explicit reviewed manifest digest.

Every JSON field is required with exact spelling; null, duplicate keys (including
escaped/case-folded aliases), unknown fields, trailing content, invalid types,
invalid UTF-8 and unpaired surrogate escapes are rejected. Descriptors are limited
to 4 MiB and the existing JSON nesting bound. Canonical descriptor bytes are Go
`encoding/json.Marshal` of the validated typed struct: fixed field order, compact
JSON, UTF-8 and Go JSON escaping. Object key order/input whitespace do not matter.
Arrays are ordered; inventories/deltas/grants/memberships must be sorted and
unique. Ordered function search paths and migration prefixes are never sorted.

Payload checksum covers **exact** nonempty UTF-8/LF bytes. CR, NUL and BOM are
rejected. The component does not normalize, parse, execute or certify SQL. Distinct
accepted payload bytes have distinct checksum identity.

## Snapshots, metadata and delta comparison

Release-approved metadata does not include target-specific ledger OIDs or business
row digests. `BindSecurityTransition` requires a sealed approval, an independently
expected target equal to the approved target, and an observed source snapshot
matching the approved frontier/readiness/metadata. Binding deep-copies evidence.

Metadata classes: schema, table, column, function, sequence, default_acl, role.
Keys must be collector-stable qualified identities: include function argument
types, column name, and creator/schema/object-class for default ACL records.
Catalog collection must use one unambiguous identity convention; I1 compares keys
as exact strings and does not infer SQL identifier equivalence.

Each object records owner, explicit/default ACL mode and grant tuples (grantor,
grantee, privilege, grant option), definition digest, SD flag, ordered search_path,
protected-schema policy flag, role attributes and membership edges including
grantor/ADMIN/INHERIT/SET. All collections are non-null. A default PostgreSQL ACL
is not an empty explicit ACL. Table and column ACL are different inventory objects.
Role and definition digests must include additional catalog properties not modeled
as typed fields; `DefinitionSHA` is not a substitute for a complete collector.

Before/after changes must match an exact per-key before/after digest allowlist.
Absent object digest is SHA256 of `absent-transition-object-v1`. Creation/deletion
therefore needs an explicit delta. No unused or unknown deltas are accepted.
An existing SD function changing owner, SD mode, search_path or definition must
explicitly mark `execution_context_change=true`; unrelated ACL changes cannot
masquerade as execution-context changes.

Bound evidence additionally includes a scope digest and ordered physical object
identities. OIDs belong exclusively to target-bound evidence, not approved
descriptors. Every present physical object needs one nonzero OID; columns use the
parent table OID plus positive attnum. Duplicate catalog identities are rejected.
Definitions/default ACLs are compared as explicit scoped metadata, not physical
objects requiring a synthetic OID.

Ledger observations include owner and ACL, which must equal their table metadata;
their OID must equal the corresponding physical identity. The ledger inventory
must exactly match approved presence policy. F1 permits owner/ACL changes only
through the descriptor's exact metadata deltas; immutable ledger fields remain
unchanged. Existing physical identities remain stable when the object is retained.

Bound immutable evidence includes database OID, ledger OIDs, markers, constraint
digests and complete historical-row digests, business data, deployment identity,
business receipts and sequence state. Ledger row digests must include every field,
including timestamps. Final snapshots must retain this evidence exactly.
An existing empty extension ledger can be present only when explicitly required by
both approved scopes, and cannot disappear. An absent extension ledger must have an
explicit absent requirement, with no metadata, physical identity or ledger row.
Creation
of an absent extension ledger is outside this transition comparator and remains
part of the later first-apply initialization contract.

## F1 mandatory scope and admission

Scope is release-approved policy, not a list inferred from the observed snapshot.
`legacy-v1` permits explicit absence of not-yet-provisioned target roles and command/
deployment schemas. `prepared-v1` requires all six target roles and three protected
schemas present. Final scope must be prepared. Neither profile admits an absent
baseline ledger; an applied extension prefix requires the extension ledger present.

Fixed requirements cannot be removed by shrinking or rehashing a descriptor:

- role entries for tremor_api, tremor_deployer, tremor_executor,
  tremor_ledger_owner, tremor_migrator, tremor_owner;
- protected target schema plus explicit presence policy for tremor_commands and
  tremor_deployment;
- schema_migrations present, schema_extensions explicitly present or absent;
- nonempty approved creator list; prepared scope includes tremor_deployer;
- functions/sequences/tables global AND schema-local default-privilege evidence for
  every listed creator in every present in-scope schema, including resolved defaults when no
  pg_default_acl row exists.

Scope keys are canonical within a bounded identifier subset: role:name,
schema:name, schema.table, schema.table.column, schema.function(args), and
default_acl:schema:creator:class. Quoted identifiers/unsupported function signature
syntax are refused by this version rather than guessed. Function OID identity
also prevents alternate spellings from aliasing one physical object.
An empty schema segment in a default_acl key denotes global defaults, not a missing
schema observation. These records still require their creator role in scope.

Present scope requirements and metadata are a bijection. Explicitly absent entries
cannot appear in metadata/identities/ledgers. Missing required functions, schemas,
roles, ownership/ACL data or physical identities fail admission. Owners, grantors,
grantees (except PUBLIC), membership targets and membership grantors must resolve
to present in-scope roles. Columns resolve to their table/schema and share the
table owner/OID. Default privilege creators resolve to their role. Membership
dependencies are closed within scope, with self/cyclic memberships rejected.
Prepared target roles except deployer cannot have dangerous role flags or role
memberships; owner/executor/ledger-owner remain NOLOGIN. Additional roles and
objects are governed by the exact approved inventories. This is not an inventory
of unrelated cluster objects and not a semantic safety review of approved SQL.

Binding validates the descriptor and its source scope. All bound-consuming public
paths (comparison, expected receipt, source/final recovery and state classification)
revalidate the sealed descriptor/source: source equality cannot skip admission.
Unavailable evidence remains unknown; supplied inconsistent evidence is rejected.
There is no caller-provided validated flag or scope override.

Structural completeness and consistency are now enforced **within the approved
scope**. They do not prove truth, transactional consistency or PostgreSQL origin.
I2 needs a trusted collector, effective privilege/default
ACL expansion, full membership graph, unsupported-object rejection and target
inspection. The collector must discover all relevant objects/role dependencies,
check coverage against policy, and refuse incomplete reads. It must not mint release
approval. A future separately sealed collected-evidence interface can enforce this
boundary; F1 does not invent or issue a PostgreSQL-provenance token.

## Receipt, states and recovery

Expected receipt binds transition ID, descriptor/payload digest, full bound source
snapshot digest and exact frontier digest. Future storage is ledger-owner owned,
with no API/executor/migrator write or membership path. The expected receipt here
is a value, not evidence that PostgreSQL committed it.

`ClassifyTransitionRecovery` returns SOURCE_CONFIRMED, FINAL_CONFIRMED,
OUTCOME_UNKNOWN or EVIDENCE_REJECTED. Source requires exact baseline and no receipt;
final requires complete approved final state and exact receipt. Missing evidence
is unknown; contradictory supplied evidence is rejected. It has no SQL, connection,
callback or retry interface.

`ClassifySecurityTransition` exposes LEGACY_VERIFIED, PREPARED, TRANSITIONING,
PROTECTED, UNKNOWN_COMMIT, REJECTED. PREPARED is local preparation only. A verified
preflight or full postflight can describe TRANSITIONING, but no partial arbitrary
catalog is admitted. In-transaction final evidence is never PROTECTED. The caller
must supply a fresh post-COMMIT observation for committed classification; I1 cannot
authenticate observation provenance. Uncertain commit plus an exact source does
not assert success; explicit later execution remains an operator decision.

The transition anchor binds only the exact approved frontier. It cannot authorize
an extra/missing/reordered entry, different manifest or unknown readiness. Later
migration receipts and catalog readiness remain the existing append-only contract.

## I2 execution contract (not implemented)

Keep existing business objects and both ledgers in explicitly verified target
schema S; no move or recreation. Target schema/ledgers and deployment-control
schema belong to `tremor_ledger_owner`. Business objects belong to `tremor_owner`;
business commands in `tremor_commands` belong to NOLOGIN `tremor_executor`. No
permanent CREATE for business owner in S is required by architecture A.

Administrative preparation is separate from the target transition transaction;
it is not claimed atomic with it. API remains gated until complete admission.
Within the target transaction: lock, recheck source, apply exact approved metadata
payload, compare allowed deltas/invariants, insert transition receipt, validate final
state, one COMMIT. Unknown COMMIT requires fresh target-inspected read-only evidence;
no automatic SQL replay. Ordinary migrations keep SQL effects and extension receipt
in one transaction. Existing E1A comparator must not be relaxed for normal apply.

I1 intentionally does not change baseline SQL/manifests, Target.inspect, runner,
release approvals, API, Docker or CI. Foundation is not production-ready. Next:
review offline contracts, implement target collection/protocol only with separate
authorization, then disposable PostgreSQL 18 fresh/upgrade/ACL/crash tests using
the existing E1A harness, followed by protected API gates and adapters.
