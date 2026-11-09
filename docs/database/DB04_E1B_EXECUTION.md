# DB04 E1B I2.2 execution

Variant A. Production admission remains disabled: role, transition and protected
migration public loaders have no release approvals. TEST approvals are private
package fixtures, not operator flags.

Components: security_execution.go (separate role provisioning, transition,
anchor, recovery and foundation capability checks); security_lineage.go (separate
lineage inspection retaining E1A shape/prefix checks); security_protected_apply.go
(one approved next migration, readonly verify, receipt and reconnect recovery).
Tests are security_execution*_test.go and security_protected_apply_integration_test.go.
compose.execution.yaml extends the existing disposable harness, not CI.

Provisioning is separately committed. Roles: trusted SUPERUSER LOGIN deployer;
NOLOGIN owner, ledger_owner, executor; LOGIN restricted api/migrator. All have
NOINHERIT, no memberships; limited roles have no administrative attributes.
New LOGIN roles have no password; temporary credentials are installed only by
TEST administrator. Minimal pg_control_system EXECUTE for migrator is a separate
provisioning permission, never pg_monitor. Migrator has explicit SELECT on the
current approved inventory for complete verification, no future blanket grants.

Transition validates release approval/payload/target/frontier and unchanged legacy
source readiness; captures source; takes the shared advisory lock and ACCESS
EXCLUSIVE locks on approved tables before transaction snapshot reads. It repeats
source validation, executes the approved transaction envelope, validates exact
metadata and immutable data/ledger/sequence evidence, writes one receipt, validates
again, COMMITs once and reconnects independently. Advisory locking does not fence
uncooperative clients. Maintenance/quiescence for DDL/shared role changes remains
an operational precondition; table locks cover approved business/ledger tables.
Trusted deployment administrator is outside the malicious-code threat model.

The precomputed approved anchor binds descriptor, frontier, final legacy readiness,
metadata digest and scope. Final ownership/ACL changes legitimately alter legacy
readiness. No postflight-observed hash is promoted to approval. The source archive
can be exported with Bound.SourceSnapshot and rebound with the same approved
descriptor; its digest must match the protected durable receipt during recovery.
A repeated transition Run is observation-only, never automatic replay.

Protected apply uses an independently approved one-step sidecar and exact protected
checkpoints. PriorManifest must hash to the original anchor manifest; its entries
and baseline readiness cannot be rewritten. Old prefix/checksum/chain and ledger
comparators are reused unchanged. Protected metadata replaces the old warehouse
privilege contract only in this separately admitted path. No invalid legacy runner
is constructed. First extension ledger creation is explicitly represented by the
before/after scopes and uses the original shape/marker with ledger_owner ownership.
SQL and its migration receipt share one COMMIT. Subsequent calls verify the durable
post-checkpoint without executing SQL. Full verify is read-only and available to
migrator; apply requires deployer. Each next step needs its own reviewed sidecar;
this implementation accepts a manifest ending at that one next step.

Evidence (all new isolated PostgreSQL 18.6, fixed image digest
sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2):

| Scenario | Result |
|---|---|
| Fresh provisioning, transition, independent confirmation | PASS |
| Populated synthetic stock/history/receipts upgrade; immutable comparisons | PASS |
| API/migrator DML, DDL, TRUNCATE, ledger/receipt writes, SET ROLE denied | PASS |
| Executor ledger write denied; elevated parameter drift rejected | PASS |
| Rollback after changes, before receipt, after receipt | PASS |
| Partial ownership SQL error rollback | PASS |
| Actual backend termination before COMMIT, source confirmed | PASS |
| Actual backend termination after acknowledged COMMIT, fresh final confirmed | PASS |
| Real loss of successful COMMIT response | NOT_TESTED |
| Whole-server/power-loss crash | NOT_TESTED |
| Protected approved migration and repeated call without duplicate effects | PASS |
| Protected foreign anchor / catalog drift refusal / SQL error rollback | PASS |
| New SD function effective owner and unapproved SD drift | PASS |
| Limited migrator full readonly verify | PASS |
| Existing 47 PostgreSQL security regressions | PASS |

58 offline tests (previous 56 plus admission/capability groups), vet and formatting
passed. Fresh acceptance system identifier 7706115469461569579; populated acceptance
7706115566302769195; existing-regression cluster 7706114707318612011. Additional final
anchor run is logged separately in external i22-anchor-final.log. Logs live outside
Git under e1b-i21-audit. Every harness invocation cleans only its labelled project;
no host ports, internal network, new temporary credentials, synthetic data only.

No E1A/baseline/manifest/API changes. Production allowlists stay empty. This is
foundation execution, not a working production warehouse API. I3 may be developed
separately after review; production admission requires adapters and remaining
recovery testing. No push/PR/merge.
