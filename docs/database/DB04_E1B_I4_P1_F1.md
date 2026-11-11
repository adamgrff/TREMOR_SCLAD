# DB-04 / Э1Б-И4-P1-F1 — целостность истории ledgers

## A. Причина и исходное воспроизведение

Исходный HEAD: `8bb92afedbba35d5c14c6dab80d17cdf4b9cdb81`.
Main: `63efe4bddc3aa8c7303b1bfa4f7276f3191f00f7`, tree
`53d259004b87eef5ed6a39cf544385ca635be23d`.

Manifest/prefix validation подтверждали version/name/checksum/profile/ordinal.
Collector получал hash полных строк, но protected verification после transition
не сравнивала исторические timestamps с закреплённым per-deployment эталоном.
Каталожный readiness и security metadata не заменяют целостность данных ledger.

На старом коде отдельно изменены `applied_at` в `schema_migrations` и
`schema_extensions`: hash строк изменился, OID, manifest prefix и legacy readiness
сохранились; direct verifier принял состояние, реальный mTLS вернул READY,
реальный API — HTTP 200. Оба отрицательных теста завершились FAIL, как ожидалось.
Проект: `e1b-i4-de32340416524ed2b874e073ff1eeabd`, PostgreSQL 18.6,
system identifier `7706187770368446508`. Fault injection — только TEST administrator;
обход ACL этим воспроизведением не доказан.

## B. Изменённые компоненты

- `security_ledger_history.go`: типы, канонизация, hash chain, read-only проверки,
  закрытый writer и инертный versioned control DDL.
- `security_transition_state.go`: historical evidence в snapshot; committed
  comparator требует history, отдельная private pre-receipt проверка effects.
- `security_collector.go`: чтение и проверка history в той же RR READ ONLY
  транзакции; control rows проверяются отдельно от business-data digest.
- `security_execution.go`: обязательный approved history scope, запись root
  вместе с transition receipt; recovery не переутверждает историю.
- `security_protected_apply.go`: preflight history, immutable prefix, append
  checkpoint вместе с migration receipt, postflight и reconnect verification.
- `security_ledger_history*_test.go`: unit, transition и постоянные PostgreSQL/
  mTLS/HTTP регрессии P1.
- Существующие execution/protected-apply/security/verifier TEST fixtures:
  approved control DDL, проверка отказа в доступе и rollback/recovery.
- `cmd/api/protected_integration_test.go`: только TEST-счётчик SQL,
  `/internal/test-sql-count`; в production routing такого endpoint нет.
- `test/e1b/run-readiness.ps1`: опция `-HistoryRegression` существующего harness.

## C. Доверенный anchor и атомарность

Добавлена таблица `tremor_deployment.ledger_history_v1`. Её создание должно быть
явно включено в независимо approved transition SQL, FinalScope и metadata deltas.
Runner не создаёт таблицу скрыто. Versioned protocol: `e1b-ledger-history-v1`.

Step 0 закрепляет полную историю source, ранее проверенную и target-bound,
в первой доверенной approved transition. Запись находится в одной транзакции с
ownership/ACL changes и существующим transition receipt, до одного COMMIT.
Это подтверждение истории с момента закрепления, а не доказательство
неизменности legacy timestamps до этого момента.

Каждая разрешённая migration дописывает step N: предыдущий checkpoint SHA,
operation approval SHA, target/database OID, transition/bound SHA, точный frontier,
baseline и extension history. Предыдущий SHA берётся из проверенного preflight
до migration SQL. Старый prefix, включая timestamps, должен совпадать точно;
разрешено добавление ровно одной новой extension-строки. Новая history запись,
business effects и обычный migration receipt коммитятся вместе.

Root связан с исходным bound snapshot и неизменённым transition receipt.
Конечная запись связана с sealed approval защищённой migration. Verification,
recovery и idempotent repeat не имеют writer callback и не создают anchors.
INSERT writer private; UPDATE/автоматического backfill или repair нет.

Таблица принадлежит `tremor_ledger_owner`; PUBLIC/API/executor не получают прав,
migrator получает только SELECT. Проверена невозможность API читать её, а API и
migrator — изменять. Trusted deployer сохраняет утверждённую модель варианта A.
Против администратора, способного одновременно переписать все доверенные
receipts и anchors, независимой защиты не заявляется.

## D. Канонический формат

Каждая строка содержит version, name, checksum, profile, ordinal и
`applied_at_pg_micros`. Последнее — точное int64 число PostgreSQL-микросекунд от
2000-01-01 UTC, вычисленное numeric SQL:

```sql
((extract(epoch FROM applied_at) - 946684800) * 1000000)::bigint
```

Нефинитные timestamps отвергаются. Float, timezone-dependent text и округление
через наносекунды Go не используются. PostgreSQL timestamp имеет точность
[одна микросекунда](https://www.postgresql.org/docs/18/datatype-datetime.html);
семантика [EXTRACT EPOCH](https://www.postgresql.org/docs/18/functions-datetime.html)
для timestamptz привязана к UTC.

Порядок строк — ordinal, последовательные ordinals и возрастающие versions
проверяются. Сохраняются present/absent, физический OID и прежний полный RowsSHA.
Payload — детерминированный JSON типизированной структуры, TEXT в БД; чтение
отклоняет duplicate/unknown fields, неверную форму, неканонический JSON,
неверный digest, цепочку, target, OID, frontier или prefix. Пустая история,
пропущенный root/step и посторонние transition rows не принимаются.

Существующий `schema_migrations`, `schema_extensions`, их OID, строки и форматы
receipts не пересоздаются. Существующая 5-column transition receipt не меняется.

## E. P1 и дополнительные проверки

На исправленном коде обе исходные подмены независимо дают:

| Проверка | Результат |
|---|---|
| Hash исторических строк меняется, OID сохраняется | PASS |
| Manifest prefix валиден, legacy readiness прежний | PASS |
| Полный collector отклоняет повреждённую history | PASS |
| `VerifyProtectedExtension` отказывает | PASS |
| Реальный межконтейнерный mTLS не возвращает READY | PASS |
| API `503`, code `protected_not_ready` | PASS |
| TEST SQL counter не меняется — handler не выполняется | PASS |
| Повторный protected apply не принимает повреждение | PASS |
| После восстановления точных строк READY возвращается | PASS |

Также проверены несколько baseline timestamps, изменение одной микросекунды
extension timestamp, отсутствующий и malformed checkpoint, неполные offline
evidence, неверная цепочка, OID, target, старый timestamp в новом checkpoint,
неизменная повторная verification и timezone/DateStyle independence с
восстановлением контекста.

## F. Transition, apply и recovery

Approved TEST transition и последующий approved extension проходят с независимым
metadata approval до применения; legacy manifest/readiness SHA не переписываются.
Легитимный append extension не считается подменой prefix.
Повторный apply дважды оставляет ровно две history записи, один extension receipt
и один business effect. Во время fault recovery новых checkpoints не появляется.

Ошибки до receipt, после receipt и до COMMIT откатывают control/history/ACL changes
вместе. При неопределённом результате success подтверждается только новым
соединением с полным конечным evidence. Известное противоречие истории — отказ;
недоступность чтения не является подтверждённым COMMIT. Автоматического SQL replay
нет. В тесте ошибки без отправки COMMIT восстановлен исходный state, новая таблица
отсутствует. Существующий тест потери backend после подтверждённого COMMIT также
перепроверяет durable state.

Metadata-only TEST rehearsal очищает текущие control/history receipts только
в откатываемой административной транзакции перед расчётом проектируемого metadata:
это не verification PROTECTED и не утверждение timestamps по результату apply.
При реальном apply эта очистка не выполняется. Старые assertions сохранены.

## G. Регрессии и evidence

Evidence сохраняется вне Git в `e1b-i4-p1-f1` рядом с feature worktree.
Изначальное FAIL evidence — в `e1b-i4-acceptance`; приватные TEST credentials и
TLS keys после каждого прогона удаляются, в отчёт не включены.

Проверены fresh/upgrade, исходные I3/F1 и новые P1 сценарии через существующий
Compose harness, API/collector Linux test binaries. PostgreSQL 18.6;
прикреплённый TEST image digest:
`sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2`.
Внутренняя сеть, без host ports, синтетические данные, отдельные credentials.

Подтверждённые прогоны P1: `e1b-i4-0618d3aab7de4667b13d02db6e14dcc7`
(system identifier `7706191752143650860`), fresh
`e1b-readiness-d8af37f8c13d4407b880ce88c4d6f3d1`
(`7706193141502353451`) и upgrade
`e1b-readiness-67d19bf5a4824ac4b73f4847d862a9d0`.
Collector: `e1b-collector-437beef3818c40a685856298990f4744`, identifier
`7706194771382882348`, все 47 сценариев PASS.
API/legacy: `e1b-api-legacy-aec75fdc53ff4fc59d98e2d0b44f5946`,
129 top-level тестов PASS.

Неуспешные development прогоны не объявляются PASS. Удалённый первоначальный FK
новой таблицы давал OID-dependent RI trigger names между rehearsal/apply; вместо
ослабления catalog comparator связь с receipt проверяется Go. Исправлена TEST
fixture baseline profile (`clean-v2`, а не extension chain SHA).

Отдельно при общих HTTP регрессиях в старом и новом коде наблюдался конкурентный
HTTP 500 (`concurrent status 500`), после чего TEST service не выставлял marker
готовности. Assertions и runtime routing не ослаблены; сбой и повторные прогоны
учитываются отдельно от результата P1. Это остаётся диагностическим ограничением
приёмки, даже если повторный прогон успешен.

## H. Совместимость и границы

Э1А, 22 baseline SQL, manifests, release approvals, production allowlist и
legacy readiness algorithms не изменены. Новая history — самостоятельная
проверка; она не заменяет manifest, catalog readiness или security fingerprint.
Новый runtime transition требует history-enabled FinalScope и отсутствие control
таблицы в SourceScope. Старые архивные offline v1 fixtures остаются читаемыми,
но не дают admission реальному незафиксированному protected deployment.

Автоматического upgrading существующей protected БД без history нет: такие
состояния fail closed. Новая процедура закрепления уже существующего protected
deployment потребует отдельного утверждения, не фиктивного восстановления
старых timestamps. V1 table предназначена для одной исходной history-enabled
foundation binding; текущая область protected apply (один approved next step)
не расширяется этой правкой.

## I. Неиспытанное и verdict

Реальная потеря успешного COMMIT-response на транспортном уровне, crash всего
PostgreSQL-сервера, оставшиеся concurrency и backup/restore acceptance —
**NOT_TESTED в этом исправлении**. Backend termination/commit hook не выдаются
за такие доказательства. Readiness и API SQL по-прежнему находятся в разных
транзакциях. Production admission disabled; business write commands отсутствуют.

P1 исправление готовит возобновление И4, но **не означает И4 ACCEPTED**.
Main и семь пользовательских untracked файлов должны остаться неизменными;
их SHA256 проверяются отдельно при завершении. Push/PR/merge не выполняются.

## J. Финальные результаты

| Проверка | Статус / evidence |
|---|---|
| Все Go packages, `go test ./... -count=1` | PASS |
| `go vet ./...`, gofmt, PowerShell parser, `git diff --check` | PASS |
| Collector, прежние 47 PostgreSQL сценариев | PASS |
| API/legacy, прежние 129 top-level тестов | PASS |
| И2.2 fresh/upgrade, rollback и protected apply/repeat | PASS |
| И3-F1 без API read extension | PASS, `e1b-readiness-005cccd7837a4ef499a866ff840cb6b1`, system identifier `7706195843901308971` |
| И3 51 route cases, прежние drift/receipt/offline tests | PASS в финальном последовательном прогоне `e1b-readiness-35e610f1fca44761b5d3db43a5792fe7`, identifier `7706195416257749036` |
| Финальные P1/mTLS/HTTP tests | PASS, `e1b-readiness-e1463e7163154bbcafda0a936984bbad`, identifier `7706196107456086059` |
| Frontend tests/build (исходники frontend не менялись) | PASS, 25 проверок и build |
| Secret-pattern scan и ручной просмотр diff | PASS, секреты/artifacts не добавлены |
| Все собственные Docker resources | PASS, повторно подтверждены 0 containers/networks/volumes для всех 14 TEST projects этого исправления, включая неуспешные; private dirs 0 |
| Main SHA/tree, baseline/manifests/Э1А | PASS, неизменны |
| Реальный server crash / потеря COMMIT-response / backup-restore | NOT_TESTED, остаются И4 |

Последний P1 TEST подтверждает `fsync=on`, `synchronous_commit=on`.
Предыдущий общий HTTP прогон с конкурентным 500 — FAIL; успешный повтор не
отменяет факт нестабильности. Это не ложный READY и не подмена результата P1,
но дальнейшая И4 acceptance должна учитывать этот отдельный недостаток.

## K. Сохранность пользовательских файлов

В основном checkout ровно прежние семь untracked файлов, SHA256 совпадают:

| Файл | SHA256 |
|---|---|
| `.agents/skills/code-review-pro/SKILL.md` | `B96E3BB390F5803DF8C012D09C5F138B28CE478FEE806287F747571586F24AA4` |
| `.agents/skills/database-schema-designer/SKILL.md` | `A1CA262A12833DCE2FC7BEFDE5F54371F0CE9FE0F139659D2D48267709B39A9B` |
| `.agents/skills/database-schema-designer/references/best-practices.md` | `A900AB42A0F07DBD411FE2B1DCCBCD6150B5FF755AF80D14507D08B69A6FC08E` |
| `.agents/skills/database-schema-designer/references/output-format.md` | `07D3130198903349DF763AFDBA2829B273A2E51D3EA2F8ACA6D504FD542292D8` |
| `.agents/skills/database-schema-designer/references/schema-templates.md` | `097EC84FDCCDEDC803ADFB381DA161E59DC861D076078E809E041802F44B1D77` |
| `.agents/skills/git-pr-reviewer/SKILL.md` | `1FB97CBF966B8E97A2342AE72B99F411D640E2D706AB334F23159F8642623989` |
| `TREMOR_SCLAD_v1.0_ROADMAP.md` | `6D6DBC66F9323BF9DEFDA89C81BE9134B8D08F26D3AB5FFA4943E735FBA3D55F` |

## L. Решение

Исправление P1 проверено, готово возобновление И4. История до первого доверенного
checkpoint не получает вымышленного доказательства неизменности. Existing
protected deployments без checkpoint не переутверждаются автоматически.
Production admission остаётся disabled. Итог И4 остаётся BLOCKED до завершения
оставшихся acceptance-сценариев и оценки наблюдаемой HTTP нестабильности.
Локальный commit создаётся отдельно поверх И3; его SHA сообщается в итоговом
ответе, история не переписывается.
