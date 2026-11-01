# Чистое развёртывание TREMOR_SCLAD — clean-v2

DB-03.6, 2026-11-01. Основной поддерживаемый путь: **новая изолированная база → clean-v2 → один склад → явный запуск клиентов**. Документ содержит будущие действия оператора; при подготовке документации они не выполнялись. Это не разрешение создавать production-БД или переключать действующее приложение.

Сначала прочитать [safety](DEPLOYMENT_SAFETY.md), работать по [checklist](DEPLOYMENT_CHECKLIST.md), учитывать [release review](DB03_RELEASE_REVIEW.md). Для production сначала закрыть DB-04 и остальные release gates. Приведённый исполняемый пример ограничен новым локальным TEST-кластером; удалённое/production-развёртывание требует собственной согласованной карточки и проверок.

## 0. Что подготовить

Утверждённая карточка: назначение контура, новый data directory, host/port/database/user/schema, ожидаемый system identifier из независимого источника, warehouse name, API address, frontend origin/API URL, ответственный, artifact directory. Нельзя принимать identity произвольного endpoint за ожидаемую без независимой сверки.

Требования:

- PostgreSQL **18.x** server и утилиты initdb/pg_ctl/pg_controldata/createdb/psql. Проверен 18.4 Windows. Runner откажет другой major.
- Go **не ниже 1.26.9** (`backend/go.mod`). Проверять `go env GOVERSION GOTOOLCHAIN` именно из backend. Для offline-компьютера заранее установить нужный toolchain: auto-download не является offline provisioning.
- Node **>=22.22.0** по текущему lockfile (react-router 8.3.0); для воспроизведения проверенного стенда Node 24.18.0 и npm 12.2.0. npm engine/pin в проекте не задан, lockfileVersion=3; другой набор отдельно проверить.
- Полный reviewed checkout: cmd/migrate, cmd/warehouse, internal/migrations с embedded warehouse_contract.json, SQL-файлы и оба manifest, frontend package-lock.json. Одного старого HEAD без незакоммиченных файлов DB-03 недостаточно.
- Отдельные свободные порты и browser origin/profile. Не останавливать чужие процессы. В dedicated terminal не загружать backend/.env, PG service или рабочие credentials.

Фиксировать `git status`, commit и hashes reviewed файлов. Не делать reset/checkout/commit автоматически ради установки. Изменение checksum — STOP, не повод переписать manifest.

## 1–4. Новый PostgreSQL 18 и пустая база

### PowerShell: явные параметры и fail-fast

Запускать в отдельном терминале оператора. Все пути абсолютные, значения вводятся явно; пароль не вводится как текст команды.

```powershell
$ErrorActionPreference = 'Stop'
function Invoke-Native {
    param([string]$File, [string[]]$Arguments)
    & $File @Arguments
    if ($LASTEXITCODE -ne 0) { throw 'STOP: native command failed; inspect output' }
}
function Read-ProcessSecret {
    param([string]$Prompt)
    $secretValue = Read-Host $Prompt -AsSecureString
    $secretPointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secretValue)
    try { [Runtime.InteropServices.Marshal]::PtrToStringBSTR($secretPointer) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($secretPointer) }
}
$RepoRoot = Read-Host 'Absolute path of reviewed repository'
$PgBin = Read-Host 'Absolute path of PostgreSQL 18 bin directory'
$PgData = Read-Host 'Absolute NEW TEST data directory (must not exist)'
$Artifacts = Read-Host 'Absolute evidence directory OUTSIDE repository'
$DbHost = '127.0.0.1'
$DbPort = [int](Read-Host 'NEW unused PostgreSQL TCP port')
$DbName = Read-Host 'NEW TEST database name ending _test'
$DbUser = Read-Host 'Initial user for this NEW TEST cluster'
$WarehouseName = Read-Host 'Exact warehouse name, no default'
if ($DbPort -lt 1 -or $DbPort -gt 65535) { throw 'STOP: invalid port' }
if ($DbName -cnotmatch '^[A-Za-z_][A-Za-z0-9_-]{0,57}_test$' -or
    $DbName -ieq 'tremor_sklad' -or
    $DbUser -cnotmatch '^[A-Za-z_][A-Za-z0-9_-]{0,62}$') { throw 'STOP: invalid TEST target' }
foreach ($path in @($RepoRoot, $PgBin, $PgData, $Artifacts)) {
    if (![IO.Path]::IsPathRooted($path)) { throw 'STOP: absolute paths required' }
}
if (Test-Path -LiteralPath $PgData) { throw 'STOP: data directory exists; no reset allowed' }
if (!(Test-Path -LiteralPath (Join-Path $RepoRoot 'backend/go.mod'))) { throw 'STOP: wrong repository' }
$RepoCanonical = [IO.Path]::GetFullPath($RepoRoot).TrimEnd([IO.Path]::DirectorySeparatorChar)
$ArtifactsCanonical = [IO.Path]::GetFullPath($Artifacts).TrimEnd([IO.Path]::DirectorySeparatorChar)
if ($ArtifactsCanonical.StartsWith($RepoCanonical + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or
    $ArtifactsCanonical -eq $RepoCanonical) { throw 'STOP: evidence must be outside checkout' }
New-Item -ItemType Directory -Path $Artifacts -Force | Out-Null
Get-ChildItem Env: | Where-Object { $_.Name.StartsWith('PG') } | ForEach-Object { Remove-Item -LiteralPath ('Env:' + $_.Name) }
$env:PGCLIENTENCODING = 'UTF8'
$env:PGCONNECT_TIMEOUT = '5'
$env:PGSSLMODE = 'disable' # only this exact loopback TEST path
$Psql = Join-Path $PgBin 'psql.exe'
$Createdb = Join-Path $PgBin 'createdb.exe'
$PgCtl = Join-Path $PgBin 'pg_ctl.exe'
```

Проверить, что выбранный порт действительно свободен, до старта. Например, `Get-NetTCPConnection -State Listen` и проверка bind. Ошибка bind означает STOP, не выбор чужого процесса для завершения.

```powershell
$probe = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $DbPort)
try { $probe.Start() } finally { $probe.Stop() }
Invoke-Native (Join-Path $PgBin 'initdb.exe') @('--version')
Invoke-Native (Join-Path $PgBin 'initdb.exe') @('-D', $PgData, '-U', $DbUser,
    '--encoding=UTF8', '--locale-provider=builtin', '--builtin-locale=C.UTF-8',
    '--auth=scram-sha-256', '--pwprompt')
Invoke-Native (Join-Path $PgBin 'pg_controldata.exe') @('-D', $PgData)
$ExpectedSystemId = Read-Host 'System identifier verified from this NEW data directory'
if ($ExpectedSystemId -notmatch '^[1-9][0-9]{0,19}$') { throw 'STOP: invalid server ID' }
Invoke-Native $PgCtl @('-D', $PgData, '-l', (Join-Path $Artifacts 'postgres.log'),
    '-o', "-h $DbHost -p $DbPort", '-w', '-t', '30', 'start')
$env:MIGRATION_PASSWORD = Read-ProcessSecret 'Password of the initial TEST user'
$env:PGPASSWORD = $env:MIGRATION_PASSWORD
$PsqlMaintenance = @('-X', '-w', '-q', '-A', '-t', '-v', 'ON_ERROR_STOP=1',
    '--host', $DbHost, '--port', "$DbPort", '--username', $DbUser, '--dbname', 'postgres')
$IdentitySQL = @'
BEGIN READ ONLY;
SELECT json_build_object(
 'database',current_database(),'user',current_user,
 'address',host(inet_server_addr()),'port',inet_server_port(),
 'server_id',(SELECT system_identifier::text FROM pg_control_system()),
 'data_directory',current_setting('data_directory'),
 'version',current_setting('server_version_num')::int,'recovery',pg_is_in_recovery(),
 'databases',(SELECT json_agg(datname ORDER BY datname) FROM pg_database));
COMMIT;
'@
$Observed = (Invoke-Native $Psql ($PsqlMaintenance + @('-c', $IdentitySQL))) | ConvertFrom-Json
if ($Observed.database -ne 'postgres' -or $Observed.user -ne $DbUser -or
    $Observed.address -ne $DbHost -or $Observed.port -ne $DbPort -or
    $Observed.server_id -ne $ExpectedSystemId -or $Observed.recovery -or
    [math]::Floor($Observed.version / 10000) -ne 18 -or
    [IO.Path]::GetFullPath($Observed.data_directory) -ne [IO.Path]::GetFullPath($PgData) -or
    (($Observed.databases -join ',') -ne 'postgres,template0,template1')) {
    throw 'STOP: actual cluster differs; do not create a database'
}
$Observed | ConvertTo-Json -Depth 3 | Set-Content (Join-Path $Artifacts 'cluster-before-write.json')
Invoke-Native $Createdb @('--maintenance-db=postgres', '-w', '--host', $DbHost,
    '--port', "$DbPort", '--username', $DbUser, '--template', 'template0',
    '--encoding=UTF8', '--locale-provider=builtin', '--builtin-locale=C.UTF-8', $DbName)
$PsqlTarget = @('-X', '-w', '-q', '-A', '-t', '-v', 'ON_ERROR_STOP=1',
    '--host', $DbHost, '--port', "$DbPort", '--username', $DbUser, '--dbname', $DbName)
Invoke-Native $Psql ($PsqlTarget + @('-c', @'
BEGIN READ ONLY;
SELECT current_database(),current_user,
       (SELECT system_identifier::text FROM pg_control_system());
SELECT pg_encoding_to_char(encoding),datlocprovider,datlocale,datistemplate
FROM pg_database WHERE datname=current_database();
SELECT count(*) AS public_objects FROM pg_class c
JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public';
COMMIT;
'@))
```

Ожидание: новая database/user/server ID, `UTF8 | b | C.UTF-8 | f`, public_objects=0. Любое расхождение — STOP. Повтор создания существующей БД должен отказать; не заменять этот отказ удалением/очисткой.

Создание роли initdb здесь относится только к начальному владельцу нового TEST-кластера. Production-роли/права и разрешение pg_control_system() — DB-04; runbook не выдаёт GRANT и не предлагает superuser для рабочего API.

### Linux: эквивалентный новый локальный TEST-кластер

Этот Bash-вариант статически сверен с флагами, но **не исполнялся на Linux**. Запускать от отдельного непривилегированного владельца нового PostgreSQL, не root. Пути утилит и пакеты PostgreSQL 18 определяются ОС; Windows .exe не переносить.

```bash
set -euo pipefail
read -r -p 'Reviewed repo absolute path: ' REPO_ROOT
read -r -p 'PostgreSQL 18 bin absolute path: ' PG_BIN
read -r -p 'NEW TEST data directory: ' PG_DATA
read -r -p 'Evidence directory outside repo: ' ARTIFACTS
read -r -p 'Unused PostgreSQL port: ' DB_PORT
read -r -p 'NEW database ending _test: ' DB_NAME
read -r -p 'Initial TEST cluster user: ' DB_USER
read -r -p 'Exact warehouse name: ' WAREHOUSE_NAME
DB_HOST=127.0.0.1
[[ "$DB_NAME" =~ ^[A-Za-z_][A-Za-z0-9_-]{0,57}_test$ ]]
[[ "$DB_USER" =~ ^[A-Za-z_][A-Za-z0-9_-]{0,62}$ ]]
[[ "$DB_PORT" =~ ^[0-9]+$ ]] && (( DB_PORT > 0 && DB_PORT < 65536 ))
[[ ! -e "$PG_DATA" && -f "$REPO_ROOT/backend/go.mod" ]]
[[ "$REPO_ROOT" == /* && "$PG_BIN" == /* && "$PG_DATA" == /* && "$ARTIFACTS" == /* ]]
[[ "$ARTIFACTS" != "$REPO_ROOT" && "$ARTIFACTS" != "$REPO_ROOT/"* ]]
mkdir -p "$ARTIFACTS"
unset PGHOST PGHOSTADDR PGPORT PGDATABASE PGUSER PGPASSWORD PGPASSFILE PGSERVICE PGSERVICEFILE PGOPTIONS PGSSLMODE PGSSLROOTCERT PGTARGETSESSIONATTRS PGCONNECT_TIMEOUT PGCLIENTENCODING
export PGCLIENTENCODING=UTF8 PGCONNECT_TIMEOUT=5 PGSSLMODE=disable
# Independently inspect listeners (e.g. ss -ltn) and STOP if the chosen port is used.
"$PG_BIN/initdb" --version
"$PG_BIN/initdb" -D "$PG_DATA" -U "$DB_USER" --encoding=UTF8 \
  --locale-provider=builtin --builtin-locale=C.UTF-8 --auth=scram-sha-256 --pwprompt
"$PG_BIN/pg_controldata" -D "$PG_DATA"
read -r -p 'Independently verified system identifier: ' EXPECTED_SYSTEM_ID
[[ "$EXPECTED_SYSTEM_ID" =~ ^[1-9][0-9]{0,19}$ ]]
"$PG_BIN/pg_ctl" -D "$PG_DATA" -l "$ARTIFACTS/postgres.log" \
  -o "-h $DB_HOST -p $DB_PORT" -w -t 30 start
read -r -s -p 'Initial TEST user password: ' MIGRATION_PASSWORD; printf '\n'
export MIGRATION_PASSWORD PGPASSWORD="$MIGRATION_PASSWORD"
PSQL_MAINT=(-X -w -q -A -t -v ON_ERROR_STOP=1 --host "$DB_HOST" --port "$DB_PORT" --username "$DB_USER" --dbname postgres)
"$PG_BIN/psql" "${PSQL_MAINT[@]}" <<'SQL'
BEGIN READ ONLY;
SELECT current_database(),current_user,host(inet_server_addr()),inet_server_port(),
       system_identifier::text,current_setting('data_directory'),
       current_setting('server_version_num'),pg_is_in_recovery()
FROM pg_control_system();
SELECT datname FROM pg_database ORDER BY datname;
COMMIT;
SQL
# STOP until output matches the independent target card, PG_DATA and EXPECTED_SYSTEM_ID,
# version 18, recovery=false and only postgres/template0/template1. Do not auto-adopt output.
read -r -p 'After full comparison, type the approved system identifier again: ' APPROVAL
[[ "$APPROVAL" == "$EXPECTED_SYSTEM_ID" ]]
"$PG_BIN/createdb" --maintenance-db=postgres -w --host "$DB_HOST" --port "$DB_PORT" \
  --username "$DB_USER" --template template0 --encoding=UTF8 \
  --locale-provider=builtin --builtin-locale=C.UTF-8 "$DB_NAME"
PSQL_TARGET=(-X -w -q -A -t -v ON_ERROR_STOP=1 --host "$DB_HOST" --port "$DB_PORT" --username "$DB_USER" --dbname "$DB_NAME")
"$PG_BIN/psql" "${PSQL_TARGET[@]}" <<'SQL'
BEGIN READ ONLY;
SELECT current_database(),current_user,(SELECT system_identifier::text FROM pg_control_system());
SELECT pg_encoding_to_char(encoding),datlocprovider,datlocale,datistemplate
FROM pg_database WHERE datname=current_database();
SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public';
COMMIT;
SQL
# STOP unless database/user/system ID match and parameters are UTF8/b/C.UTF-8/false, count=0.
```

На Linux сравнение карточки в этом примере ручное и обязательное до createdb. Runner после этого всё равно повторно доказывает actual target перед своими записями. Не продолжать shell блоки как безусловный one-shot скрипт через незакрытый STOP.

Официальные флаги: [initdb 18](https://www.postgresql.org/docs/18/app-initdb.html), [createdb 18](https://www.postgresql.org/docs/18/app-createdb.html). Builtin C.UTF-8 обеспечивает нужное Unicode case mapping, но не обещает русскую лингвистическую сортировку. Это часть принятого контракта, не locale операционной системы по умолчанию.

## 5–11. Offline plan, миграции, identity и склад

### PowerShell (из backend)

Продолжение с переменными предыдущего блока. Создать инструменты из проверенного source в каталог evidence, не в Git index:

```powershell
Set-Location (Join-Path $RepoRoot 'backend')
Invoke-Native 'go' @('env', 'GOVERSION', 'GOTOOLCHAIN')
$Migrate = Join-Path $Artifacts 'migrate.exe'
$Warehouse = Join-Path $Artifacts 'warehouse.exe'
Invoke-Native 'go' @('build', '-o', $Migrate, './cmd/migrate')
Invoke-Native 'go' @('build', '-o', $Warehouse, './cmd/warehouse')
$Profile = @('--profile', 'clean-v2', '--manifest', 'migrations/manifest.clean-v2.json')
$Target = $Profile + @('--host', $DbHost, '--port', "$DbPort", '--database', $DbName,
    '--user', $DbUser, '--schema', 'public', '--server-address', $DbHost,
    '--server-id', $ExpectedSystemId, '--sslmode', 'disable')
Invoke-Native $Migrate (@('plan') + $Profile) | Tee-Object (Join-Path $Artifacts 'plan.json')
Invoke-Native $Migrate (@('status') + $Target)
# Expected negative check only: do not use Invoke-Native, which intentionally stops on error.
$EmptyVerifyOutput = & $Migrate verify @Target 2>&1
$EmptyVerifyExit = $LASTEXITCODE
$EmptyVerifyOutput | Write-Output
if ($EmptyVerifyExit -ne 1 -or ($EmptyVerifyOutput -join "`n") -notmatch '(?m)^schema is not fully migrated\r?$') {
    throw 'STOP: expected only incomplete schema, not target/ledger/checksum failure'
}
Invoke-Native $Migrate (@('apply') + $Target)
Invoke-Native $Migrate (@('status') + $Target) | Tee-Object (Join-Path $Artifacts 'status-after.json')
Invoke-Native $Migrate (@('verify') + $Target)
Invoke-Native $Migrate (@('apply') + $Target) # verified no-op
Invoke-Native $Psql ($PsqlTarget + @('-c', @'
BEGIN READ ONLY;
SELECT version,name,checksum,profile,ordinal,applied_at FROM public.schema_migrations ORDER BY ordinal;
SELECT singleton,deployment_id,created_at FROM public.deployment_identity;
SELECT lower(U&'\0410\0411\0412')=U&'\0430\0431\0432' AS unicode_lower,
       upper(U&'\0430\0431\0432')=U&'\0410\0411\0412' AS unicode_upper,
       strpos(lower(U&'\0421\041A\041B\0410\0414'),lower(U&'\041A\041B\0410\0414'))=2 AS cyrillic_search;
COMMIT;
'@))
Invoke-Native $Warehouse (@('validate', '--warehouse-name', $WarehouseName) + $Target)
Invoke-Native $Warehouse (@('bootstrap', '--warehouse-name', $WarehouseName) + $Target)
Invoke-Native $Warehouse (@('validate', '--warehouse-name', $WarehouseName) + $Target) |
    Tee-Object (Join-Path $Artifacts 'bootstrap-ready.json')
Invoke-Native $Warehouse (@('bootstrap', '--warehouse-name', $WarehouseName) + $Target) # no-op, same name
Remove-Item Env:PGPASSWORD, Env:MIGRATION_PASSWORD
```

Каждый обязательный ненулевой exit — STOP. Не запускать apply после произвольной ошибки предварительного verify. Проверить план: 22 entries, 024 последняя; 002 excluded/demo, 006 historical-gap, ни одна не числится applied. До apply status initialized=false/22 pending. После — initialized=true/22 applied/0 pending, профиль clean-v2, точные hash/order/name. Canon UTF8/LF нормализует CRLF без trim/Unicode normalization; старые SQL не переписываются.

Read-only SQL UUID — ровно одна ненулевая случайная identity, записать её как `$ExpectedDeploymentId` в карточку, не генерировать свою. Unicode проверки должны дать t/t/t. Validator до bootstrap: schema-ready (44 пустые прикладные таблицы); после: bootstrap-ready (один точный warehouse, остальные 43 пусты). Identity и ledger — технические записи, не demo data. Название: 1–200 Unicode characters, без внешних пробелов/контрольных символов, регистр значим.

Migration verify не является полным schema-diff. Warehouse validator добавляет bounded объектный контракт, guards 022/023/024, но не доказывает все права/owners/defaults/произвольные view/function. После начала бизнес-заполнения ожидать bootstrap-ready нельзя.

### Bash: те же команды CLI

Использовать проверенные переменные предыдущего Linux-блока, тот же password environment. До каждого продолжения закрыть ручные STOP-проверки.

```bash
cd "$REPO_ROOT/backend"
go env GOVERSION GOTOOLCHAIN
go build -o "$ARTIFACTS/migrate" ./cmd/migrate
go build -o "$ARTIFACTS/warehouse" ./cmd/warehouse
PROFILE=(--profile clean-v2 --manifest migrations/manifest.clean-v2.json)
TARGET=("${PROFILE[@]}" --host "$DB_HOST" --port "$DB_PORT" --database "$DB_NAME" --user "$DB_USER" --schema public --server-address "$DB_HOST" --server-id "$EXPECTED_SYSTEM_ID" --sslmode disable)
"$ARTIFACTS/migrate" plan "${PROFILE[@]}" | tee "$ARTIFACTS/plan.json"
"$ARTIFACTS/migrate" status "${TARGET[@]}"
if "$ARTIFACTS/migrate" verify "${TARGET[@]}"; then
  printf '%s\n' 'STOP: empty DB unexpectedly verifies'; exit 1
fi
# STOP here unless the ONLY failure is incomplete schema, not target/ledger/checksum.
read -r -p 'Type incomplete-schema after reviewing this expected error: ' REASON
[[ "$REASON" == incomplete-schema ]]
"$ARTIFACTS/migrate" apply "${TARGET[@]}"
"$ARTIFACTS/migrate" status "${TARGET[@]}" | tee "$ARTIFACTS/status-after.json"
"$ARTIFACTS/migrate" verify "${TARGET[@]}"
"$ARTIFACTS/migrate" apply "${TARGET[@]}"
"$PG_BIN/psql" "${PSQL_TARGET[@]}" <<'SQL'
BEGIN READ ONLY;
SELECT version,name,checksum,profile,ordinal,applied_at FROM public.schema_migrations ORDER BY ordinal;
SELECT singleton,deployment_id,created_at FROM public.deployment_identity;
SELECT lower(U&'\0410\0411\0412')=U&'\0430\0431\0432',upper(U&'\0430\0431\0432')=U&'\0410\0411\0412';
COMMIT;
SQL
"$ARTIFACTS/warehouse" validate --warehouse-name "$WAREHOUSE_NAME" "${TARGET[@]}"
"$ARTIFACTS/warehouse" bootstrap --warehouse-name "$WAREHOUSE_NAME" "${TARGET[@]}"
"$ARTIFACTS/warehouse" validate --warehouse-name "$WAREHOUSE_NAME" "${TARGET[@]}" | tee "$ARTIFACTS/bootstrap-ready.json"
"$ARTIFACTS/warehouse" bootstrap --warehouse-name "$WAREHOUSE_NAME" "${TARGET[@]}"
unset PGPASSWORD MIGRATION_PASSWORD
```

Для удалённого target runner допускает только `--sslmode verify-full --sslrootcert <явный CA path>` с явными host/port/server-address/server-id. Literal `<...>` не копировать как значение. Удалённый TLS не проверен, не заменять его disable и не использовать localhost вместо literal loopback в локальном disable-профиле. До отдельного remote readiness review удалённый путь не выполнять.

## 12–14. Явный TEST API/frontend, первый безопасный просмотр

Работать в двух новых терминалах с отдельными свободными портами. Нельзя занимать старый API или менять backend/.env. Перед запуском сохранить target card и сравнить все параметры DATABASE_URL без вывода секрета. В URI нужны явные host, port, database, user, sslmode и `application_name=clean-deployment-api`; URL-encoded пароль поступает из защищённого внешнего источника. Не собирать URI строковой конкатенацией неэкранированного пароля.

PowerShell API (повторно определить функции/несекретные переменные из начала документа либо использовать утверждённую карточку; не загружать весь чужой shell profile):

```powershell
Set-Location (Join-Path $RepoRoot 'backend')
$ApiPort = [int](Read-Host 'NEW unused API port')
$FrontendPort = [int](Read-Host 'NEW unused frontend port')
foreach ($port in @($ApiPort, $FrontendPort)) {
    if ($port -lt 1 -or $port -gt 65535 -or $port -eq $DbPort) { throw 'STOP: invalid/overlapping port' }
    $probe = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $port)
    try { $probe.Start() } finally { $probe.Stop() }
}
if ($ApiPort -eq $FrontendPort) { throw 'STOP: ports overlap' }
$env:TREMOR_SKIP_DOTENV = '1'
$env:TREMOR_DISABLE_OUTBOUND_HTTP = '1'
Get-ChildItem Env: | Where-Object { $_.Name.StartsWith('OZON_') } | ForEach-Object { Remove-Item -LiteralPath ('Env:' + $_.Name) }
$env:API_ADDR = "127.0.0.1:$ApiPort"
$env:FRONTEND_ORIGIN = "http://127.0.0.1:$FrontendPort"
$env:DATABASE_URL = Read-ProcessSecret 'Approved NEW TEST DATABASE_URL (never print it)'
try { $apiDatabaseUri = [Uri]$env:DATABASE_URL }
catch { throw 'STOP: malformed secret URI; details suppressed' }
if ($apiDatabaseUri.Scheme -notin @('postgres','postgresql') -or
    $apiDatabaseUri.Host -ne $DbHost -or $apiDatabaseUri.Port -ne $DbPort -or
    [Uri]::UnescapeDataString($apiDatabaseUri.AbsolutePath.TrimStart('/')) -cne $DbName -or
    [Uri]::UnescapeDataString(($apiDatabaseUri.UserInfo -split ':',2)[0]) -cne $DbUser -or
    $apiDatabaseUri.Query -cnotin @('?sslmode=disable&application_name=clean-deployment-api',
        '?application_name=clean-deployment-api&sslmode=disable')) {
    throw 'STOP: API URI differs from approved local TEST card'
}
# No additional URI options/fallback targets are accepted in this local TEST recipe.
$ApiBinary = Join-Path $Artifacts 'tremor-api.exe'
Invoke-Native 'go' @('build', '-o', $ApiBinary, './cmd/api')
& $ApiBinary # foreground; preserve own PID, stop only this process when finished
```

API не имеет migration runner target guard и может подключиться к DATABASE_URL другого контура: **предварительная проверка и pg_stat_activity обязательны**. Для нового терминала Go-сборка завершается до запуска; оставлять migration credentials в API environment не нужно. После остановки удалить process-local DATABASE_URL из environment.

В независимом проверочном терминале, используя временные psql credentials только нужного target, выполнить READ ONLY:

```sql
BEGIN READ ONLY;
SELECT current_database(),current_user,(SELECT system_identifier::text FROM pg_control_system());
SELECT datname,usename,application_name,client_addr
FROM pg_stat_activity WHERE application_name='clean-deployment-api';
SELECT deployment_id FROM public.deployment_identity WHERE singleton;
COMMIT;
```

Ожидается только утверждённая новая БД/user. Затем `GET http://127.0.0.1:<явный API port>/health` и `GET .../api/deployment`; сравнить deploymentId с SQL/card, проверить no-store headers. Пример будущей read-only проверки PS:

```powershell
$ExpectedDeploymentId = Read-Host 'UUID already recorded from SQL/card'
$ApiBase = "http://127.0.0.1:$ApiPort/api"
$apiIdentity = Invoke-WebRequest -Uri ($ApiBase + '/deployment')
if (($apiIdentity.Content | ConvertFrom-Json).deploymentId -ne $ExpectedDeploymentId -or
    $apiIdentity.Headers['Cache-Control'] -notmatch 'no-store') { throw 'STOP: API identity unconfirmed' }
```

В frontend-терминале явно восстановить из карточки RepoRoot/ApiPort/FrontendPort; никакой подстановки старых адресов по умолчанию:

```powershell
Set-Location (Join-Path $RepoRoot 'frontend')
$env:TREMOR_SKIP_DOTENV = '1'
$env:VITE_API_BASE_URL = "http://127.0.0.1:$ApiPort/api"
Remove-Item Env:TREMOR_TEST_CSP -ErrorAction SilentlyContinue
Invoke-Native 'npm' @('ci')
Invoke-Native 'npm' @('run', 'build')
Invoke-Native 'node' @('node_modules/vite/bin/vite.js', '--host', '127.0.0.1',
    '--port', "$FrontendPort", '--strictPort')
```

`npm ci` нужен на новой машине для точного lockfile; он переустанавливает node_modules, не правит lockfile. VITE_API_BASE_URL задаётся **до build** и встраивается в JS; изменение env на static-host после build URL не меняет. В примере затем запускается TEST dev-server, а не production hosting. `npm run dev` без переопределения использовал бы 5173, API без env — 8080: для отдельного контура эти defaults не принимать.

Флаг TREMOR_TEST_CSP=1 в текущем vite.config.ts дополнительно фиксирует websocket 5174. Он предназначен для ранее проверенного тестового случая, не универсален для произвольного порта. Для нового порта нужен согласованный CSP на TEST/hosting слое с точными origin; нельзя обещать автоматическую сетевую изоляцию одним env-флагом. Проверять browser network на отсутствие старого API, не нажимать Ozon sync. Outbound guard блокирует default transport, но не заменяет OS/network policy.

Bash API/frontend — те же явно сверенные карточкой значения; функции PS здесь не используются:

```bash
# API terminal, after matching the secret URI to the approved target card:
cd "$REPO_ROOT/backend"
read -r -p 'NEW free API port: ' API_PORT
read -r -p 'NEW free frontend port: ' FRONTEND_PORT
[[ "$API_PORT" =~ ^[0-9]+$ && "$FRONTEND_PORT" =~ ^[0-9]+$ ]]
(( API_PORT > 0 && API_PORT < 65536 && FRONTEND_PORT > 0 && FRONTEND_PORT < 65536 ))
[[ "$API_PORT" != "$FRONTEND_PORT" && "$API_PORT" != "$DB_PORT" && "$FRONTEND_PORT" != "$DB_PORT" ]]
# Inspect free listeners and STOP on conflict before running the API.
for variable in ${!OZON_@}; do unset "$variable"; done
export TREMOR_SKIP_DOTENV=1 TREMOR_DISABLE_OUTBOUND_HTTP=1
export API_ADDR="127.0.0.1:$API_PORT" FRONTEND_ORIGIN="http://127.0.0.1:$FRONTEND_PORT"
read -r -s -p 'Approved URL-encoded TEST DATABASE_URL: ' DATABASE_URL; printf '\n'
export DATABASE_URL
export DB_HOST DB_PORT DB_NAME DB_USER
node --input-type=module <<'JS'
try {
  const u = new URL(process.env.DATABASE_URL)
  const query = u.search
  if (!['postgres:', 'postgresql:'].includes(u.protocol) ||
      u.hostname !== process.env.DB_HOST || u.port !== process.env.DB_PORT ||
      decodeURIComponent(u.pathname.slice(1)) !== process.env.DB_NAME ||
      decodeURIComponent(u.username) !== process.env.DB_USER ||
      !['?sslmode=disable&application_name=clean-deployment-api',
        '?application_name=clean-deployment-api&sslmode=disable'].includes(query)) throw new Error()
} catch {
  process.stderr.write('STOP: secret API URI differs from the approved local TEST card\n')
  process.exit(1)
}
JS
go build -o "$ARTIFACTS/tremor-api" ./cmd/api
"$ARTIFACTS/tremor-api"
# After this own process stops: unset DATABASE_URL
```

```bash
# Separate frontend terminal; explicitly restore REPO_ROOT/API_PORT/FRONTEND_PORT from card.
cd "$REPO_ROOT/frontend"
export TREMOR_SKIP_DOTENV=1 VITE_API_BASE_URL="http://127.0.0.1:$API_PORT/api"
unset TREMOR_TEST_CSP
npm ci
npm run build
node node_modules/vite/bin/vite.js --host 127.0.0.1 --port "$FRONTEND_PORT" --strictPort
```

Для production нет готового service/static-host/TLS шаблона: путь согласуется отдельно. Вне localhost/loopback нужен безопасный browser context (обычно HTTPS) для crypto.randomUUID; открытый HTTP LAN не является утверждённой заменой.

Перед приложением открыть **новый согласованный origin/profile**, проверить storage без изменения. Если есть неизвестные UUID/pending/activeSession — STOP, не очищать. Первая загрузка identity и серверных данных должна пройти без блокировки. Нельзя мигрировать старые команды в новую БД.

## 15–17. Первый стеллаж, чтение и финальная фиксация

1. До изменений проверить пустые «Товары и коды»/«История приёмок», отсутствие active sessions и demo products/stock/orders/events. Должен быть только один явно созданный склад и технические ledger/identity строки.
2. По явному решению оператора открыть «Ячейки» → создание стеллажа, ввести согласованное название и числа ячеек рядов A/B/C. Никаких реальных товаров/остатков или demo seed. Для TEST использовать синтетическое название. POST /api/racks использует существующий единственный warehouse, сам склад не создаёт.
3. Обновить список/стеллаж, проверить созданные ячейки, пустое содержимое. Перечитать каталог: появился rack/category, товаров всё ещё нет. Перезагрузка/новый просмотр не должны создавать вторую запись. Не повторять создание при неизвестном исходе без проверки существующего стеллажа: это не receiving receipt-команда.
4. После стеллажа bounded initial validator ожидаемо отвергает непустые бизнес-таблицы. **Не удалять их, чтобы снова получить bootstrap-ready.** Сохранить исходный bootstrap-ready и выполнить migration status/verify, целевые read-only SELECT warehouses/categories/cells/products/cell_stock и сравнение с утверждёнными входными данными.
5. Зафиксировать target/card, source hashes/версии, план, 22 ledger-row, UUID, warehouse, вывод проверок, API actual connection/origin/URL, отсутствие demo data, результаты UI и ограничения. Секреты/полный DATABASE_URL в пакет доказательств не включать.
6. При завершении TEST остановить только собственные процессы по сохранённым PID и проверенному data directory. `pg_ctl -D <этот проверенный data directory> -m fast -w stop` — поясняющий шаблон, не команда для случайного каталога. Не удалять кластер/БД автоматически. Убрать process-local credentials, сохранить артефакты.

На другом компьютере повторяется **новый** provisioning с собственным system identifier и случайным deployment UUID, а не копирование TEST data directory/браузерных команд. Если переносится тот же логический склад через backup — это другой согласуемый процесс по safety, не fresh-install.

## Переменные и границы

| Переменная/флаг | Обязательность/назначение |
|---|---|
| MIGRATION_PASSWORD | Пароль явно выбранного migration/warehouse user; не DATABASE_URL, не .env |
| --profile/--manifest | Для нового install всегда оба clean-v2; CLI default manifest остаётся v1 |
| --host/--port/--database/--user/--server-address/--server-id/--sslmode | Обязательный полный проверенный target для status/verify/apply/warehouse |
| --schema public | Явный проверенный application layout; другие схемы для приложения здесь не квалифицированы |
| --sslrootcert | Обязателен с verify-full; remote path требует отдельного теста |
| --timeout | Опциональный положительный deadline, default 5m, включая ожидание lock |
| --warehouse-name | Обязателен validate/bootstrap, без default |
| DATABASE_URL | Только API; секретный явный URI новой БД, не target мигратора |
| API_ADDR | Явный bind API; default 127.0.0.1:8080 не использовать автоматически |
| FRONTEND_ORIGIN | Exact scheme://host:port без path, для нового origin обязателен CORS |
| VITE_API_BASE_URL | Frontend build/dev URL с /api; default указывает 8080, задавать явно |
| TREMOR_SKIP_DOTENV=1 | Для API и Vite, исключает рабочие env-файлы |
| TREMOR_DISABLE_OUTBOUND_HTTP=1 | TEST API: блокировка default outbound transport |
| TREMOR_TEST_CSP | Специальный Vite TEST режим с websocket 5174, не универсальный production CSP |

Совместимость v1/v2, неизвестный COMMIT, clone/restore и ручное разрешение команд описаны в [DEPLOYMENT_SAFETY.md](DEPLOYMENT_SAFETY.md). Нельзя использовать clean-v2 как неявный upgrade старой БД.

Официальные справки: [Go toolchains](https://go.dev/doc/toolchain), [npm ci](https://docs.npmjs.com/cli/v12/commands/npm-ci/). Linux/runtime/TLS и production gates — в release review.
