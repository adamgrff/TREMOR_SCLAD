# Deployment identity and clean-v2

`clean-v1` remains the reviewed 21-migration profile. Its manifest and SQL 001–023
are unchanged. The new **fresh-install** `clean-v2` profile applies the same 21 SQL
files followed by 024, with its own ledger marker and all 22 ledger rows labelled
clean-v2. The CLI requires matching explicit profile/manifest. It never relabels
an existing clean-v1 ledger, even if the database is otherwise empty.

From `backend`, supply the same explicit verified connection flags as DB-03.2:

```powershell
go run ./cmd/migrate plan --profile clean-v2 --manifest migrations/manifest.clean-v2.json
go run ./cmd/migrate apply --profile clean-v2 --manifest migrations/manifest.clean-v2.json <explicit-target-flags>
go run ./cmd/migrate verify --profile clean-v2 --manifest migrations/manifest.clean-v2.json <explicit-target-flags>
go run ./cmd/warehouse bootstrap --profile clean-v2 --manifest migrations/manifest.clean-v2.json --warehouse-name $WarehouseName <explicit-target-flags>
go run ./cmd/warehouse validate --profile clean-v2 --manifest migrations/manifest.clean-v2.json --warehouse-name $WarehouseName <explicit-target-flags>
```

Angle-bracket items are explanatory placeholders, not executable literal flags.
Passwords stay in MIGRATION_PASSWORD. No working `.env` is loaded by these tools.
Protected targets, actual server identity, advisory lock, checksums and atomic
DDL+ledger rules are unchanged. apply cannot adopt a nonempty/unknown database.

## Creation, API and journal

024 atomically creates one deployment_identity row with PostgreSQL
gen_random_uuid() (random UUID v4), not a value derived from server coordinates or
warehouse IDs. The migration transaction also records its ledger row. Repeating
apply or restarting API never regenerates it. A singleton PK/CHECK, unique UUID,
NOT NULL fields and rewrite/truncate guards protect the identity. Validator adds
explicit identity-table/constraint/trigger/function checks for clean-v2; this one
technical row is excluded from business-emptiness checks.

GET /api/deployment reads it without cache or repair. Every receiving mutation,
including quantity corrections, catalog writes and history deletion, requires
`X-Tremor-Deployment-Id`. The actual row is checked FOR SHARE **inside that mutation's
transaction before request/receipt/session/stock processing**. A missing/invalid
header is 400; mismatch is 409 with code deployment_identity_mismatch; unavailable
identity is 503. Identity failures never carry X-Receiving-Command-Rejected=true.
Even an existing successful receipt is inaccessible with a foreign deployment ID.

This public identifier is a context boundary, not authentication or a secret.
Roles/auth remain DB-04. CORS permits the explicit header.

Frontend obtains identity before journal recovery. New durable commands store the
exact requestId, payload and deploymentId before sending, and send the same ID on
every attempt. Mismatch/legacy/unavailable identity retains the command and blocks
mutations. The session snapshot carries its deploymentId; local activeSession.v2
metadata is only written for a matching confirmed snapshot. The old unbound
activeSessionId key is ignored and preserved. Commands and session metadata from
another deployment are not silently rebound, removed or adopted.

Quantity dialogs, catalog edits and history deletion use the same durable journal.
Quantity/history and catalog-with-intake recovery reuse their original command.
Pure catalog writes without intake lack a backend receipt in the existing model:
an unresolved such command is preserved for manual inspection rather than replayed
automatically (a repeated PUT could overwrite a later edit). This deliberately
does not invent an unsafe generic retry mechanism.

## Existing clean-v1: no automatic upgrade

The supported path in this change is **new empty database → clean-v2**. Running v2
against v1 fails on ledger ownership/lineage before modifying SQL. v1's default
CLI path is not silently changed. Applying 024 manually does not turn a v1 ledger
into clean-v2, and must not be represented as an approved upgrade.

A future existing-DB upgrade needs a separately reviewed explicit upgrade lineage:
quiesce all clients/API writers, take/verify backup, prove destination identity and
existing v1 checksums/catalog, acquire the deployment lock, and atomically register
the new migration under a policy preserving the original 21 ledger rows. That
requires an explicit mixed-lineage policy or separate upgrade journal/marker,
not blanket UPDATE of profile/checksum/marker. Then verify all invariants, deploy
API/frontend together and resolve legacy client commands manually. No such adoption
or upgrade mode is implemented or executed here.

## Backup/restore and independent clones

Restoring **the same logical warehouse** preserves deployment_identity, ledger,
receipts and events. Do not generate a new identity simply because host/port/name
changed. Restore all these records consistently before allowing clients back.
Restoring an older backup can lose committed receipts/effects after the backup's
recovery point: reconcile those operations before enabling automatic recovery.
Identity alone cannot recover transaction evidence omitted by the backup.

An **independent clone** is a different deployment, even if copied from a backup.
The safest supported creation path is an empty clean-v2 install. A data-bearing
clone requires an explicitly approved offline re-identification procedure, never
API startup or a normal migration retry:

1. Stop clone API/clients and prove it is the separate destination (actual cluster,
   DB OID, explicit target); independently verify the expected copied old UUID.
2. Acquire the deployment lock and exclusive identity-table lock. Under one
   transaction, an authorized maintenance procedure would temporarily disable only
   its two immutable guards, assign a new random UUID once, restore guards, check
   exactly one row/new UUID/protections and commit. Keep source warehouse and all
   historical UUID/receipt data untouched. Record the assignment for audit.
3. Validate the clone, start its API, and use a separate client origin/profile.
   Old/source client journals must remain mismatched and blocked; never relabel
   them automatically with the clone UUID.

This describes the required maintenance gate; no rekey/adoption CLI or executable
trigger-disabling recipe is shipped here, and no existing database was rekeyed.
Normal API/runner paths cannot rotate identity. Direct owner/superuser tampering
is outside this application's context-identity guarantee and needs DB-04 controls.
