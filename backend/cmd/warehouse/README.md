# Initial warehouse bootstrap and readiness validator

Run from `backend`. This command neither creates a database nor applies migrations,
starts the API, loads `.env`, imports demo data or creates racks/products.

```powershell
go run ./cmd/warehouse validate --warehouse-name $WarehouseName --profile clean-v1 --host $DbHost --port $DbPort --database $DbName --user $DbUser --server-address $ExpectedIP --server-id $ExpectedSystemID --sslmode verify-full --sslrootcert $ExplicitCA
go run ./cmd/warehouse bootstrap --warehouse-name $WarehouseName --profile clean-v1 --host $DbHost --port $DbPort --database $DbName --user $DbUser --server-address $ExpectedIP --server-id $ExpectedSystemID --sslmode verify-full --sslrootcert $ExplicitCA
```

Variables are explicit operator-supplied values, not defaults. The exact warehouse
name is required for both modes: 1–200 Unicode characters, no leading/trailing
whitespace or control characters. Case is significant; no trimming or Unicode
normalization is silently performed. Password is supplied via `MIGRATION_PASSWORD`,
never a flag. `--manifest` defaults to the reviewed `migrations/manifest.clean-v1.json`;
`--schema` defaults to public. All target checks reuse DB-03.2 `migrations.Target`,
including identity, PostgreSQL 18, UTF8, builtin C.UTF-8, and unconditional rejection
of tremor_sklad. No production connection is inferred from environment files.
Loopback-only `--sslmode disable` is available for explicit isolated TEST targets.

## States and bootstrap

- `schema-ready`: ledger exactly matches all 21 clean-v1 migrations, the bounded
  object/protection contract passes, all 44 application tables are empty.
- `bootstrap-ready`: the same checks pass with exactly one warehouse whose name
  exactly matches the supplied expectation; the other 43 tables remain empty.
- Any other state is an error (nonzero exit), not a partially ready success.

Bootstrap is deliberately restricted to initial empty business state. It refuses
nonempty business tables, including after normal warehouse operation has started.
It acquires the same session advisory lock as migration apply, starts a READ WRITE /
READ COMMITTED transaction, verifies the ledger, locks all 45 tables in deterministic
order with SHARE ROW EXCLUSIVE, then rechecks the complete state. With no warehouse
it inserts exactly one row and rechecks postconditions; an exact existing warehouse
is a no-op preserving ID/created_at. A different name or multiple warehouses is a
conflict, without merging/deleting/updating rows. There is no singleton restriction
added to old migrations: the initial provisioning command enforces this invariant.

Errors roll back rows; PostgreSQL identity sequences can retain gaps after rollback.
A COMMIT transport error is explicitly uncertain: repeat the command with **the
same name** to safely determine the result under the same locks. No blind INSERT
retry occurs inside the command.

## What validation proves

Validator opens one READ ONLY / REPEATABLE READ transaction; all ledger, catalog and
row checks share its snapshot. It writes no rows/receipts and never calls nextval.
Readiness describes that snapshot, not a permanent guarantee against later writes.

`internal/migrations/warehouse_contract.json` pins the reviewed PostgreSQL 18
clean-v1 catalogue from DB-03.3. Checks cover:

- exactly the expected 44 ordinary application tables plus owned ledger; no RLS,
  foreign/partitioned/materialized or unexpected data tables in other user schemas;
- every recorded non-NOT-NULL application constraint's name, type, definition and
  validated flag; validity/readiness of existing indexes and the SKU upper index;
- all 33 recorded user triggers, their table/function/event definitions and enabled
  status; no extra table triggers or rewrite rules;
- existence/signatures of 31 functions; exact definitions of the 11 functions called
  by protective triggers; the effective-returns view definition;
- warehouse id/name/timestamp shape, identity, timestamp default; pending_revision
  bigint/NOT NULL/default zero and its recorded CHECK/trigger;
- ledger shape, exact checksums/profile/order and all 21 rows through the same
  DB-03.2 inspector; actual warehouse count/name and emptiness of other tables.

This is **not a universal schema-diff**: it does not compare every column/default,
sequence state, every nonprotective function body, privileges/owners or arbitrary
additional views/functions. NOT NULL comparison outside the explicitly checked
columns is not exhaustive. A privileged owner can alter objects after the check;
role separation remains DB-04. This installer is intended before application
traffic, not as an online schema upgrade. Strict PostgreSQL deparser comparison can
fail closed on future formatting differences and requires review, not auto-repair.

Tests use explicit MIGRATION_TEST_* identity parameters and disposable TEST schemas,
as documented in `../migrate/README.md`. No test ever adopts an unknown nonempty DB.
