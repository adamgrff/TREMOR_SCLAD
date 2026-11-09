# DB04_E1B_I4_ACCEPTANCE

Продолжение И4 после P1-F1, 2026-11-09. Production admission **disabled**.
Ни push, ни PR, ни merge, ни production provisioning не выполняются.

## A. P1-F1 и исходное состояние

Проверяемая feature branch: `codex/e1b-i1-offline-contracts`.
Входной HEAD: `7825ccdf6f062b705a65a7d5cb05ce8d3af5bec2` (tracked worktree чистый).
Main: `63efe4bddc3aa8c7303b1bfa4f7276f3191f00f7`, tree
`53d259004b87eef5ed6a39cf544385ca635be23d`.

История: И3 `8bb92af` → P1-F1 `7825ccd` → отдельный локальный commit И4.
Первоначальный BLOCKED/P1 отчёт сохранён ниже как историческое приложение;
подробности исправления — [P1-F1](DB04_E1B_I4_P1_F1.md).

В новых реальных PostgreSQL/mTLS/HTTP прогонах отдельно изменяются timestamps
обоих ledgers. При прежних manifest prefix и legacy readiness изменённый RowsSHA
вызывает отказ Go verification, отсутствие READY, HTTP 503
`protected_not_ready` и неизменный TEST SQL counter. Checkpoints не дописываются.
После точного восстановления строк корректные verification/apply/repeat проходят.
Исторические anchors не переутверждаются verification или recovery.

## B. Конкурентный HTTP 500

До исправления проведены два диагностических прогона: один успешный,
второй воспроизвёл 500 в серии восьми одновременных `GET /api/racks`.
Запланированный третий диагностический прогон не запускался после воспроизведения.
Evidence: `e1b-readiness-12d01e9727d941a3aa8522b47c8383bf` и
`e1b-readiness-2d549e79118a4c9e87ee6658633aa5b3`.

Зафиксировано: `BeforeAcquire` отказал при `context=<nil>`, замещающее
`AfterConnect` вернуло `readiness unavailable`, затем handler вернул 500.
Middleware уже получил READY и вошёл в handler. Повторные verifier RPC внутри
pool acquisition могли отказать после admission; `racksHandler` отображал
ошибку `db.Query` как общий 500. Дальнейшие canceled contexts наблюдались уже
после завершения неуспешного TEST service. В этот момент deployment/drift/fault
SQL ещё не запускались: контроллер ожидал окончания начальной HTTP проверки.

Состояние отдельного отклонённого RPC различено лишь как `unavailable`:
перегрузка/таймаут/конкретный HTTP status сервером не диагностированы отдельно.
Эта более узкая причина не выдаётся за доказанную. Доказан дефект повторного
readiness admission в пределах одного запроса и его преобразования в handler 500.
Этот маршрут содержит только SELECT; DML/DDL и ledger mutations запрещены самой
ролью. Подтверждения ложного READY, обхода ACL или частичной записи не получено.

Минимальное исправление: свежая успешная mTLS проверка выдаёт внутренний,
неэкспортируемый request admission, привязанный к точному Client и активному
handler. Pool сохраняет SQL identity checks, но не повторяет тот же admission
RPC внутри этого запроса. Без такого контекста (startup, самостоятельный pool
call, другое соединение/запрос вне handler) выполняется настоящая новая проверка.
При завершении handler capability атомарно отзывается; TTL-кеша READY нет.
Следующий HTTP запрос всегда получает новый nonce и новый действительный probe.
Никаких retries, превращения ошибки в HTTP 200 или административного fallback нет.

Постоянный unit regression проверяет единственный fresh probe внутри операции,
невозможность использовать capability другим Client или после handler и HTTP 503
до следующего handler при недоступном verifier. Реальный TEST API сохраняет
диагностику acquire/connect и выполняет три независимые серии по восемь запросов;
допустимы только 200 или 503, прежний assertion против 500 не ослаблен.

## C. Настоящий server crash

Использован отдельный TEST supervisor: PID 1 сохраняет namespace, postmaster —
его дочерний процесс. Host controller перед каждым fault проверяет обе project
labels, точный database/system identifier, читает postmaster PID и отправляет
**SIGKILL самому postmaster**, не backend и не graceful shutdown.
Docker socket в TEST контейнеры не монтируется. PGDATA находится в отдельном
именованном volume; он удаляется лишь после recovery и restore assertions.

Точки fault:

1. Approved transition уже изменил ownership/ACL и создал control objects в
   открытой транзакции, но ещё не вставил transition receipt (`before_receipt`).
2. Transition и protected migration подтверждённо committed, receipts/checkpoints
   и business effect проверены; затем выполняется второй SIGKILL.

Логи PostgreSQL содержат `Killed`, `database system was interrupted`,
`automatic recovery in progress`, `redo starts`, `redo done`, end-of-recovery
checkpoint и новый `ready to accept connections`. `fsync=on`,
`synchronous_commit=on`. Все проверки после crash используют новые соединения.

До COMMIT ownership/ACL, business/ledger rows, OID и receipts откатились точно.
В заполненном target изменился только `SequenceStateSHA`: PostgreSQL может
пропускать sequence values после crash. Это соответствует
[документированной семантике sequences](https://www.postgresql.org/docs/18/functions-sequence.html).
Старый source binding поэтому безопасно возвращает REJECTED, а не автоматически
принимает новый sequence hash. Comparator не менялся. Это операционное ограничение
доступности recovery, не частично применённая миграция и не доказательство
повреждения business data. Для явно следующей TEST операции создан новый
target-bound source; старый runner SQL не повторял.

После committed crash полная verification, свежий mTLS и API 200 восстановлены.
Hashes всех scoped tables (включая оба ledgers, control receipts/checkpoints),
sequence state, owner/ACL/role/function metadata сверены до и после. READY не
основывается на сохранённом флаге.

## D. Unknown-COMMIT

TEST-only proxy применяется к уже аутентифицированному transaction socket через
существующий commit hook. Используется настоящий loopback TCP client/proxy и
реальный PostgreSQL wire protocol; production transport/Target/E1А не меняются.

| Сценарий | Доказательство |
|---|---|
| A: COMMIT не произошёл | TCP Query COMMIT принят proxy, не передан серверу; клиент получил EOF, server socket закрыт, новое соединение подтверждает исходное состояние без receipts/effect |
| B: COMMIT и ответ получены | Обычный `pgx.Tx.Commit` protected migration успешно завершён, новое соединение подтверждает receipt и один business effect |
| C: COMMIT произошёл, успешный ответ потерян | Proxy переслал COMMIT и получил PostgreSQL `CommandComplete(COMMIT)`, но закрыл TCP client до передачи даже первого байта ответа; клиент получил EOF; существующий recovery подтвердил committed состояние на независимом соединении |

Для C блокируется и CommandComplete, и ReadyForQuery. Это реальная потеря
ответа на TEST TCP сегменте, не mock, не backend termination и не after-ack hook.
Проверено ровно по одной transition receipt/history root; повторный вызов runner
является observation-only. После protected apply — одна extension receipt,
две history записи, один `protected_probe` effect. Неполные/противоречивые
historical evidence отказывают; target binding сохранён. SQL автоматически
не повторяется, повреждённая история не исправляется.

## E. Конкурентность и drift

На одном настоящем advisory key удерживается административный TEST lock.
Две deployment attempts заканчивают source collection и видны как два
`pg_locks` advisory waiters. После unlock одна commits, другая отклоняет
изменённый source; новая receipt/root ровно одна. Аналогично два protected apply
waiters: одна migration, второй apply только подтверждает уже завершённое
состояние, эффект и receipt не дублируются. Используется прежний ключ Э1А.

Проверены GRANT, memberships, parameter GRANT, SD context, отсутствие receipt,
неправильный anchor, historical drift, foreign target и остановка verifier.
Ранее полученный READY не разрешает следующий запрос после drift/offline.
Ответы fail closed, legacy fallback отсутствует.
Readiness verifier и последующий API SQL находятся в **разных транзакциях**;
request admission не делает их атомарными и не исключает race с trusted admin.
DB ACL остаются самостоятельным ограничителем.

## F. Backup/restore

На синтетическом committed target выполнены `pg_dump -Fc` и отдельно
`pg_dumpall --globals-only --no-role-passwords`. Восстановление — в новый собственный
PostgreSQL кластер с другим system identifier и независимой bootstrap identity.
Globals восстанавливаются с ON_ERROR_STOP; DB — pg_restore --exit-on-error.
Пароли dump не переносит; TEST проверочная identity настраивается отдельно.

Известные шаги TEST restore procedure:

- Закреплённый E1А TEST image создаёт `e1a_alternate` при initdb: только эта
  неиспользуемая fixture role удаляется в новом restore target до точного globals
  restore. Ошибки дублирования ролей не игнорируются.
- Минимальный EXECUTE на `pg_catalog.pg_control_system()` для migrator
  восстанавливается явно: обычный DB dump не переносит эту system-function ACL.
  Широкие `pg_monitor`/`pg_read_all_data` не выдаются.
- Exact row hashes всех scoped tables, ledger timestamps/history/checkpoints,
  receipts и identity сравниваются с source backup evidence; sequence values
  и semantic owner/ACL/role attributes/memberships/function context совпадают.
  Для table/view definition digest отдельно не заявляется переносимость:
  внутренние FK trigger names/OID после logical restore могут отличаться.

Старый cluster binding отвергается при действительных admin credentials нового
кластера; соединение с явно новым system identifier работоспособно.
Даже смена только ServerID при старом bound transition/approval не допускает
PROTECTED под восстановленным ограниченным migrator. Старые checkpoint payloads
с исходными OID сохраняются, не переписываются под новый кластер.

Автоматического restore admission нет. Для будущего разрешения restored target
потребуется отдельно reviewed/approved restore attestation: доказанная связь с
backup/старой историей, новая physical identity/OID, проверенные ACL/lineage и
новый независимый anchor с сохранением старых receipts. Эта процедура не
реализуется в acceptance и не подменяется вычислением hash текущей БД.

## G. End-to-end и HTTP/frontend

Единый новый контур: baseline → synthetic stock/history/receipts → versioned TEST
roles → independently approved transition/history root → approved protected
extension/receipt → отдельный Go migrator verifier → mTLS → API tremor_api.
Полный список маршрутов — [API protection](DB04_E1B_API_PROTECTION.md).

В новых HTTP прогонах проверены все 51 маршрута: 17 разрешённых GET, 29 writes
и 5 GET сборки с 501 до SQL. `/health` остаётся документированным local health,
не подтверждением PROTECTED. Разрешённые data GET требуют fresh readiness;
NOT_READY/offline/wrong target/history drift дают 503 без SQL. Форматы чтения
сохранены. Никакого фиктивного write success или автоматического legacy режима.
SQL denials и secret mounts проверяются между настоящими отдельными контейнерами.

Frontend проверяется существующими 25 React/TS тестами и build: 501/503,
сохранение local/journal state, отсутствие false success и бесконечного retry.
Ручной browser smoke **NOT_TESTED**: браузер к приватному контуру не подключался;
автоматизированный React тест не объявляется браузерным E2E.

## H. Сводная матрица

| Проверка | Результат |
|---|---|
| Оба первоначальных P1 timestamp faults + missing/malformed history | PASS: verifier/mTLS отказ, HTTP 503 до SQL |
| Unit request admission: fresh, Client-bound, revoked | PASS |
| Все Go packages (`go test ./... -count=1`), `go vet ./...` | PASS |
| Collector regressions | PASS, все 47 PostgreSQL сценариев |
| API/legacy regressions | PASS, все 129 top-level тестов |
| И2.2 fresh и synthetic upgrade | PASS, точные approved metadata, receipts/history/data |
| 51 HTTP routes и отсутствие write SQL | PASS: 17 reads, 29 writes + 5 GET blocked |
| Конкурентные HTTP после исправления | PASS: 72 запроса, 9 серий по 8 в 3 независимых контурах, без 500/retries |
| Реальный SIGKILL до COMMIT, WAL restart | PASS: rollback ownership/ACL/rows/OID/receipt; для synthetic sequence drift — безопасный REJECTED, отдельное ограничение |
| Реальный SIGKILL после COMMIT | PASS: durable state, receipts/history, новая verification/mTLS/API 200 |
| Unknown-COMMIT A/B/C | PASS: request dropped / response delivered / response dropped до client, независимые persistent evidence |
| Два deployment advisory waiters | PASS: только один transition/receipt/root |
| Два protected apply advisory waiters | PASS: только одна migration receipt/business effect, idempotent observation |
| Drift/membership/parameter/SD/receipt/anchor/target | PASS, fail closed |
| Verifier offline и старый READY | PASS: новый запрос 503, без fallback |
| Backup, separate globals, новый restore cluster | PASS: rows/sequences/roles/ACL; старый target и старые anchors не допускаются |
| Frontend tests/build | PASS, 25 проверок и build; frontend source не изменялся |
| Formatting, PowerShell parser, diff/secret checks | PASS |
| Main/Э1А/baseline/manifests/seven user SHA256 | PASS |
| Own Docker containers/networks/volumes и private keys/dumps | PASS: повторная независимая проверка нулевых остатков |
| Ручной browser smoke | NOT_TESTED, автоматизированные React проверки не заменяют браузер |
| Race detector | NOT_TESTED: native CGO compiler отсутствует; закреплённый Go Docker image локально отсутствует, новый образ ради проверки не скачивался |

Исторические FAIL не скрыты: исходный P1, повторённый concurrent 500 и неуспешные
TEST harness development runs сохранены отдельно. Они не засчитываются как PASS.
Критические crash/unknown-COMMIT сценарии согласованного плана не остались
NOT_TESTED; ручной UI и race detector не выдаются за выполненные проверки.

## I. Findings

- P0/P1: новых подтверждённых дефектов нет. Исходный P1 historical integrity
  исправлен P1-F1 и повторно проверен.
- P2 fixed: повторная readiness admission после входа в read handler приводила
  к 500; исправлена request-scoped capability, permanent regression и реальные
  конкурентные HTTP проверки.
- P2 operational: после crash sequence WAL drift может дать консервативный
  REJECTED по старому source binding. Автоматической reapproval/replay нет.
- Nonblocking acceptance limitation: ручной browser smoke не выполнялся.
- Production blocker вне merge admission: отдельная approved restore-rebinding
  процедура пока отсутствует; restored cluster автоматически не принимается.

## J. Остаточные границы

Threat model не защищает от trusted admin, переписывающего одновременно все
доверенные receipts/anchors. История legacy timestamps до первого доверенного
checkpoint не имеет вымышленного доказательства неизменности. Между readiness и
API SQL остаётся межтранзакционное окно; SQL ACL действуют независимо от READY.
Business write commands не реализованы, production approvals пусты.
Оснований объявлять foundation полноценным production API нет.

## K. PostgreSQL и evidence

PostgreSQL 18.6. Pinned derived TEST image:
`tremor-e1a-test@sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2`;
PostgreSQL base digest:
`sha256:02fb3245e038f33536cf13bc6c4e59f21d07b0cea3c1fe1c07c6e8aa8f083bcc`.
Только internal network, ни одного host DB/verifier/API port, отдельные TEST
credentials, без Docker socket и доступа к рабочему `tremor_sklad`.

Успешный полный upgrade project:
`e1b-readiness-a127005bc8fb4800a6443c89b2c647d6`.
Source system identifier `7706204062879993900`, restore `7706204476015874091`.
Успешный полный fresh project:
`e1b-readiness-a38affdc62b6453dbb632894d279a700`, source
`7706204933980409900`, restore `7706205343913697323`.
Полные regressions: `e1b-collector-46e1f774d8da452ebb713344096df120` и
`e1b-api-legacy-09b8c377d2224b38bb919815965cce64`.
Evidence: setup, crash.log, postgres.log, api-http, faults, history,
crash-durable, restore, offline, runtime, cleanup/result в локальном каталоге
`e1b-i4-final` рядом с feature worktree, без Git artifacts/credentials.

Неуспешные development прогоны сохранены отдельно и не засчитываются PASS:
стартовая гонка supervisor/Compose; слишком строгий sequence expectation после
crash; конфликт bootstrap fixture role при globals restore. Исправлены TEST
процедуры, production comparators не ослаблялись.

## L. Cleanup и сохранность

Независимая финальная проверка всех девяти собственных TEST projects дала
0 containers/networks/volumes; private directories (credentials/keys/dumps) — 0.
Main SHA/tree и все исходные файлы Э1А, включая baseline/manifests, byte-identical.
Production approvals/loaders не менялись. Семь исходных пользовательских файлов
проверены по SHA256 и совпадают с эталоном в
[P1-F1, раздел K](DB04_E1B_I4_P1_F1.md); их исходные hashes остаются эталоном.
Временные credentials, keys, dumps и control requests удаляются вместе с
адресно проверенным TEST private directory; глобальный cleanup не используется.

## M. Финальный Git state

Единственный локальный commit поверх `7825ccdf6f062b705a65a7d5cb05ce8d3af5bec2`.
Итоговый HEAD — commit, содержащий этот отчёт; его SHA и состояние worktree
фиксируются в заключительном сообщении и внешнем `final-state.json` evidence.
Переписывание истории/push/PR/merge не выполняются.

## N. Verdict

**ACCEPTED WITH NONBLOCKING LIMITATIONS — только для merge неактивного foundation.**

Подтверждённый P1 закрыт, ошибка concurrent 500 исправлена и регрессии пройдены.
Реальные server crash, WAL recovery, unknown-COMMIT A/B/C, concurrency и
backup/restore выполнены. Открытых P0/P1 в проверенной области не обнаружено.
Ограничения: browser/race NOT_TESTED, консервативная source recovery при sequence
WAL drift, отсутствие approved процедуры admission нового restored target,
межтранзакционное окно readiness/API. Они не дают обхода полномочий или
автоматического replay и не разрешают production.

Merge допустим исключительно с сохранением disabled production admission,
пустых approvals и блокировки неподдерживаемых business writes. Это решение
не является production approval, включением runtime production или разрешением
выполнить merge сейчас. Push/PR/merge по этому этапу не выполняются.

Production code change ограничен `internal/readiness/channel.go` и `runtime.go`:
operation-scoped admission, без новых DB grants. Остальные изменения — постоянные
tests, TEST supervisor/Compose overlay, расширение существующего harness и этот
отчёт. Локальный `test/e1b/.gitattributes` сохраняет LF только для нового напрямую
монтируемого Linux script при Windows checkout; правила/исходники Э1А не меняются.

## Историческое приложение: первоначальная приёмка И4

Ниже сохраняется исходный отчёт BLOCKED до исправления P1. Его прежние
FAIL/NOT_TESTED и HEAD относятся к тому прогону, а не к результатам продолжения.

# DB04_E1B_I4_ACCEPTANCE

Дата: 2026-11-09. Verdict: **BLOCKED**.

Приёмка остановлена после двух воспроизведений одного P1: историческая строка
migration ledger изменена, но действительная повторная Go verification и mTLS
readiness возвращают успех, а существующий Go API разрешает GET. Исправления
production-кода не выполнялись. Неиспытанные критические сценарии не засчитаны
по результатам предыдущих этапов.

## A. Git history и проверенные компоненты

- Feature: codex/e1b-i1-offline-contracts.
- Проверяемый HEAD: 8bb92afedbba35d5c14c6dab80d17cdf4b9cdb81.
- main: 63efe4bddc3aa8c7303b1bfa4f7276f3191f00f7.
- main tree: 53d259004b87eef5ed6a39cf544385ca635be23d.
- Входной tracked worktree чистый. Полный diff инвентаризирован: 51 файл,
  9870 insertions / 168 deletions. Из существовавших в main файлов изменены
  только backend/cmd/api/main.go и два frontend TS-файла; остальные файлы новые.
- Линейная история: 6606a97 → 5d80b1b → 91f5f9c → c1d7e31 → 9bbe711 →
  5b71787 → d214079 → 9dd9446 → 8bb92af.
- Проверены границы contracts/collector → transition/protected apply → Go
  verifier → mTLS → API routing; воспроизведение использует существующие
  компоненты без замены collector/verifier на mocks.
- Э1А, Target.inspect, baseline/extension runner, sql/manifest validators,
  каталог readiness и весь backend/migrations byte-identical относительно main.
  Все 22 baseline migration сохранены, существующие manifests не менялись.
  Production approval loaders/guards не изменены acceptance-работой.
- Полная security-подпись всех компонентов не выдана: углублённая приёмка
  прекращена на воспроизводимом P1, как требуется критериями И4.

## B. Совместный end-to-end

Новый project: e1b-i4-566d96030c2d4f029d23fefc2109bfb7.
Использован существующий E1A/I2.2/I3 harness с отдельным TEST-only regression,
без изменения зарегистрированных маршрутов или production transport.

| Шаг | Результат И4 | Evidence |
|---|---|---|
| Чистая baseline | PASS | существующий execution fixture; TEST database только tremor_e1b_collector_test |
| Синтетические stock/history/receipts | PASS | UPGRADE synthetic stock/history/receipts seeded |
| Целевые роли | PASS | существующий versioned TEST provisioning |
| Approved TEST security transition | PASS | rollback/source/recovery и commit проверки execution fixture |
| Сохранность ledger/data после перехода | PASS | старые guards и before/after evidence |
| Одна approved TEST extension | PASS | version 25 fixture + inert API read-policy, approval до применения |
| Migration receipt + business effect | PASS | protected apply/repeat, один protected_probe row, атомарный receipt |
| Отдельный verifier | PASS | REAL_VERIFIER_IDENTITY=tremor_migrator INITIAL_PROTECTED=PASS |
| Отдельный API | PASS | tremor_api; нет административного password/policy mount |
| Все 51 HTTP routes | PASS в исходном корректном состоянии | API_HTTP_CASES_PASS routes=51 reads=17 identity=tremor_api blocked_SQL=0 |
| 29 writes и 5 legacy/function GET | PASS | 501 до handler/SQL; query tracer |
| Frontend errors | PASS автоматизированно | все 25 frontend tests; реальные hook/journal 501/503 regression |
| Повторная verification без изменений | PASS positive control | direct verifier + свежий nonce/mTLS |
| Повторная verification после изменения ledger timestamp | **FAIL / P1** | оба ledgers; подробности M |
| Возврат исходного состояния | PASS | восстановлены точные timestamps, новый Go/mTLS positive control, полное integrity comparison |

Тестовый frontend проверялся существующими автоматизированными TS/React-тестами
и build на хосте. Интерактивный браузерный frontend к этому контуру не подключался.
Это не объявляется браузерным E2E.

## C. Business data и ledgers

Перед fault injection полный collector подтвердил совпадение business data,
identity, business receipts, sequences и ledger evidence с сохранённым TEST
checkpoint. После двух отдельных faults timestamps были восстановлены точно;
повторная полная сверка и Go/mTLS verification прошли.

В каждом fault изменялся только applied_at строки ordinal=1 выбранного ledger.
Новое независимое REPEATABLE READ / READ ONLY наблюдение установило:
RowsSHA изменён, OID прежний, Metadata прежние, legacy readiness прежний,
frontier прежний. Ни source, ни descriptor, ни approvals, ни anchors не менялись.
Поэтому исходное положительное состояние нельзя считать доказательством
целостности ledger после этих faults: readiness при них ошибочно остаётся READY.

Сохранность business effects при первоначальном transition/apply подтверждена;
полнота длительного контроля неизменности ledger history — опровергнута.
OID replacement отдельно не выполнялся и не объявляется воспроизведённым.

## D. Security permissions matrix

| Identity | Полномочия в TEST / граница | Подтверждение И4 |
|---|---|---|
| TEST administrator | Только setup/fault controller; synthetic credentials | Проверены system id/database OID перед fault SQL |
| tremor_deployer | Доверенный deployment; approved provisioning/transition/apply | существующий execution fixture; не runtime identity |
| tremor_owner | NOLOGIN, business ownership | approved collector/checkpoint + исполнение SD под этим owner |
| tremor_ledger_owner | NOLOGIN, migration ledger ownership | metadata/lineage проверены; API/MIG изменения ledger запрещены |
| tremor_migrator | LOGIN, status/verify; scoped READ и отдельный минимальный pg_control_system() EXECUTE | настоящий отдельный Go verifier; нет deployer credentials |
| tremor_executor | NOLOGIN, без foundation business commands | реальная executor-ledger denial в execution fixture |
| tremor_api | LOGIN, только column SELECT allowlist 20 таблиц, CONNECT/USAGE; без бизнес DML/DDL/user-function EXECUTE | реальные SQL denials, роль API, закрытые ledger/receipt SELECT, owner SET ROLE denied |

Доказаны реальные PostgreSQL ограничения API/migrator/executor из существующих
execution/F1 helpers; READ ONLY транзакция не используется как замена ACL.
No elevated attributes/memberships/parameter privileges подтверждены исходным
полным collector и точным approved metadata comparison. Отдельные новые
конкурентные SET/INHERIT/ADMIN сценарии И4 не запускались.

У API нет административного HTTP route, private verifier policy/password mount,
credentials migrator/deployer или Docker socket. Go admission guards не разрешают
runtime API вызывать административную verification/transition/apply; это исходный
Go guard/source check, а не утверждение о наличии соответствующей SQL-функции.
PUBLIC/user-function EXECUTE и SD owner/context проверяются существующим
foundation/metadata путём; этот путь не расширялся ради acceptance.

Положительный READY не даёт дополнительных PostgreSQL прав. P1 не является
обходом SQL ACL: timestamp faults выполнял исключительно TEST administrator.
Проверяется обещанный отказ на изменённой истории, а не защита от злонамеренного
доверенного administrator, способного переписать весь deployment.

## E. Реальный crash PostgreSQL

**NOT_TESTED**: server crash до COMMIT, server crash после подтверждённого COMMIT,
WAL-backed recovery и restart protected readiness. До этих сценариев работа
остановлена на P1. PostgreSQL server не убивался и не перезапускался для crash.

Execution fixture повторил backend termination до COMMIT и после уже полученного
успешного COMMIT; это не crash всего PostgreSQL и не подмена этого требования.
Cleanup был обычным завершением собственных контейнеров, а не fault injection.

Измеренные settings: fsync=on, synchronous_commit=on. Они не являются результатом
crash-теста. Использованный для узкого reproduction harness хранит PGDATA в
изолированном tmpfs; durable-volume / crash / WAL / backup acceptance ещё предстоит.
Никакой TEST volume не удалялся до незавершённой recovery verification: такая
crash/recovery процедура вообще не начиналась.

Официальная семантика settings сверена с
[PostgreSQL 18 WAL](https://www.postgresql.org/docs/18/runtime-config-wal.html).
Она не заменяет экспериментальное evidence durability.

## F. Unknown-COMMIT evidence

| Ситуация | Результат И4 |
|---|---|
| A. SQL/процедура прервана до COMMIT, клиент получает ошибку | PASS в прежних fault hooks, rollback/source recovery; не server crash |
| B. COMMIT подтверждён, затем новое подключение проверяет state | PASS в commit-and-independent-recovery fixture |
| C. COMMIT выполнен, успешный wire response намеренно потерян | **NOT_TESTED** |

В C transport proxy не создавался и достоверная потеря ответа не выполнялась.
Backend termination after acknowledged COMMIT из fixture не зачтён за C.
В существующих повторных apply/run тестах SQL replay/двойной business effect не
обнаружены; это не сертификат корректности настоящего unknown-COMMIT transport
сценария. Новые contradictory-evidence/transport тесты отложены после P1.

## G. Конкурентность и security drift

- Повторный существующий transition Runner после bound observation — PASS в
  fixture; повтор protected apply не даёт второго business effect/receipt.
- Свежие mTLS вызовы, wrong-target HTTP rejection и конкурентные GET — PASS в
  текущем API HTTP fixture до timestamp faults.
- Drift исторического applied_at после положительного READY — FAIL, два ledger.
  Старая успешная проверка не использовалась: каждый c.Check создаёт новый nonce,
  direct VerifyProtectedExtension открывает новые подключения.
- Два одновременных deployment attempts и реальная contention advisory locks:
  NOT_TESTED в И4. Исходный advisoryKey/Э1А lock код не изменён.
- Остановка verifier во время запросов, отдельный lost-READY transport/replay
  сценарий, повтор всех ACL/membership/parameter drift HTTP tests: NOT_TESTED в
  этом И4; предыдущие И3 результаты не повышены до И4 PASS.
- Unit TLS/nonce/replay/timeout тесты входят в go test ./... и прошли.

Verifier и последующая API SQL транзакция различны. Абсолютная атомарность verdict
и бизнес-запроса или отсутствие race с trusted administrator не заявляются.

## H. Backup/restore

**NOT_TESTED**: синтетический backup, role globals, restore в тот же/другой cluster,
проверка data/history/receipts/ACL и отказ старого target binding в новом cluster.
После P1 backup/restore не начинался; реальных dumps/credentials не использовалось.

После исправления: cluster-wide roles восстанавливать отдельной процедурой,
не объявляя её атомарной с DB restore; сравнить ownership/ACL/defaults/parameter
privileges и ledger/data; новый system identifier не должен автоматически
получать старый anchor или approval.

## I. HTTP и frontend acceptance

Полная таблица 51 routes сохранена в репозитории в
`docs/database/DB04_E1B_API_PROTECTION.md`; маршруты и middleware не менялись.
В текущем отдельном TEST API все 51 routes проверены реальными HTTP запросами:
17 GET admitted (health только liveness), 29 writes и 5 GET сборки — 501 с
protected_operation_unsupported, zero SQL для blocked routes. Проверены
wrong target, concurrent GET, HEAD/canonical path variants, 400/404/JSON contracts
и положительный stock DTO=3 на синтетической БД.

/health не объявляется protected readiness. После каждого timestamp fault
/api/racks ошибочно отвечает 200, так как verifier возвращает свежий READY.
Это FAIL защищённой readiness-интеграции, несмотря на корректный routing отказов.

25 frontend tests и npm run build — PASS в текущей приёмке. Проверены реальные
receiving journal/assembly hook: 501/503, сохранение UUID/локальных данных,
отсутствие ложного успеха и автоматических бесконечных повторов. SQL/credentials/
anchors в публичных HTTP errors не раскрывались при выполненных проверках.

Browser smoke/manual UI — **NOT_TESTED**: остановка acceptance после P1; доступное
средство управления браузером не применялось. Новый E2E framework не создавался.

## J. Regression results

| Группа | Результат этой приёмки |
|---|---|
| Все Go unit/offline packages без подключения к рабочей БД | PASS; go-tests.log |
| go vet ./... | PASS; go-vet.log |
| gofmt по всему feature Go diff и git diff --check | PASS |
| Исходные 47 collector PostgreSQL regressions | NOT_TESTED повторно в И4; И3 PASS — только исторический результат |
| И2.2 execution/rollback/protected apply synthetic fixture | PASS в setup нового project |
| И3-F1 реальный mTLS / roles / secret isolation positive path | PASS |
| И3 все 51 HTTP routes и integrity positive controls | PASS до fault injection |
| И4 исторический timestamp drift | FAIL два subtests / один P1 |
| И3 129 API/legacy PostgreSQL tests | NOT_TESTED повторно в И4; обычные unit tests прошли, DB suite не запускался |
| 25 frontend scripts и frontend build | PASS, текущие tool outputs |
| Secret/artifact review | В Git нет новых TEST credentials/keys/binaries/logs/dumps; temporary secrets удалены |

Неизменность main SHA/tree, baseline и исходников Э1А проверена git diff. Все семь
пользовательских untracked файлов основной main worktree сохранены по SHA256:

| Файл | SHA256 |
|---|---|
| .agents/skills/code-review-pro/SKILL.md | B96E3BB390F5803DF8C012D09C5F138B28CE478FEE806287F747571586F24AA4 |
| .agents/skills/database-schema-designer/SKILL.md | A1CA262A12833DCE2FC7BEFDE5F54371F0CE9FE0F139659D2D48267709B39A9B |
| .agents/skills/database-schema-designer/references/best-practices.md | A900AB42A0F07DBD411FE2B1DCCBCD6150B5FF755AF80D14507D08B69A6FC08E |
| .agents/skills/database-schema-designer/references/output-format.md | 07D3130198903349DF763AFDBA2829B273A2E51D3EA2F8ACA6D504FD542292D8 |
| .agents/skills/database-schema-designer/references/schema-templates.md | 097EC84FDCCDEDC803ADFB381DA161E59DC861D076078E809E041802F44B1D77 |
| .agents/skills/git-pr-reviewer/SKILL.md | 1FB97CBF966B8E97A2342AE72B99F411D640E2D706AB334F23159F8642623989 |
| TREMOR_SCLAD_v1.0_ROADMAP.md | 6D6DBC66F9323BF9DEFDA89C81BE9134B8D08F26D3AB5FFA4943E735FBA3D55F |

## K. PostgreSQL / image / target

- PostgreSQL 18.6 (Debian 18.6-1.pgdg12+2), linux/amd64.
- Image: tremor-e1a-test@sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2.
- System identifier: 7706178219834581035.
- Database: tremor_e1b_collector_test; schema public.
- fsync=on; synchronous_commit=on.
- Internal network, no host ports, separate processes/PID/filesystem mounts,
  dropped capabilities, no Docker socket in API/verifier.
- Новый случайный TEST admin password и отдельные случайные API/migrator passwords;
  private mTLS keys и policy mounts разделены. API не получает private controller
  bundle или verifier password. Никаких tremor_sklad/production connections.

## L. Docker cleanup

PASS: собственные containers=0, networks=0, volumes=0 после адресного compose down
с проверкой labels; затем отдельно повторено read-only перечисление ресурсов
исключительно собственного project. Generated `*-private` directory удалена.
Чужие Docker ресурсы не инспектировались и не менялись; глобальные prune отсутствуют.

Runner завершился nonzero вследствие ожидаемого regression FAIL; cleanup завершён.
Поле test_exit=-1 в generic runner result означает, что fault command выбросил
ошибку до присвоения обычного runExit, а не неизвестный результат DB COMMIT.

## M. Findings — только подтверждённые проблемы

### P1 — protected readiness принимает изменённую историческую строку ledger

**Root cause.** `backend/internal/migrations/security_protected_apply.go:50`
checkProtectedCheckpoint собирает полный snapshot, но строка 63 сравнивает лишь
transition receipt fields, Metadata и ReadinessSHA. `Ledgers` (в том числе RowsSHA),
собранные `security_collector.go:218`, не сравниваются с trusted immutable evidence.
`VerifyProtectedExtension` (:210) вызывает этот checkpoint; mTLS probe и API используют
этот же путь. Полноценный CompareSecurityTransition (:143/:154) проверяет immutable
ledger evidence для первоначального transition, но не применяется в этом
post-extension verification path.

**Реальное воспроизведение.** В корректном PROTECTED + approved extension состоянии,
без изменения descriptor/anchors/manifest, TEST administrator отдельно выполняет:

```sql
UPDATE public.schema_migrations
SET applied_at = applied_at + interval '1 second'
WHERE ordinal = 1;
```

Затем то же отдельно для public.schema_extensions. После каждого fault новый
collector фиксирует изменённый RowsSHA, при неизменных OID/Metadata/legacy readiness/
frontier. Новые реальные direct verifier/mTLS/API вызовы дают:

```text
ledger=schema_migrations rows_hash_changed=true oid_same=true
metadata_same=true readiness_same=true frontier_same=true
direct_verifier_accepted=true mtls_READY=true real_API_HTTP=200

ledger=schema_extensions rows_hash_changed=true oid_same=true
metadata_same=true readiness_same=true frontier_same=true
direct_verifier_accepted=true mtls_READY=true real_API_HTTP=200
```

Expected: ошибка immutable-ledger verification, NOT_READY и HTTP 503. Оба
негативных regression subtests падают на исходном неизменённом HEAD. После
восстановления исходного timestamp положительная verification снова проходит.

**Граница вывода.** Это не доказательство возможности API/migrator изменять ledger;
ACL по-прежнему запрещают запись. Administrator используется только как TEST fault
injector. Не заявляется защита от злонамеренного доверенного administrator.
Дефект — false READY при отличающейся уже сохранённой истории, то есть нарушение
объявленной проверки ledger integrity. Каталожный readiness Э1А сам по себе не
обязан включать данные и не должен переписываться ради исправления.

**Минимальный план исправления (не выполнялся).**

1. В общий protected verification path добавить отдельное target-bound immutable
   ledger evidence: OID, исторические полные rows/receipt timestamps, marker и
   constraints; проверять его наряду с metadata/readiness, не вместо них.
2. Исторический baseline/существующий prefix сверять с уже bound source. Для
   впервые созданного extension ledger и нового generated applied_at нужен
   доверенно сформированный durable checkpoint/receipt binding с тем же COMMIT.
   Операторский новый hash или post-hoc approval недопустим. Это может потребовать
   небольшой версионированной доработки новых Э1Б contracts/control receipt, а не
   однострочного ослабления guards или изменения Э1А.
3. Не замораживать все изменяемые business data как способ исправить ledger:
   ledger-history integrity и будущие разрешённые business effects различаются.
4. Добавить постоянные отрицательные тесты обоих timestamps, ledger OID replacement,
   исторических receipts и всех публичных verification/recovery entry points.
   OID replacement пока только необходимая регрессия, не воспроизведённый факт.
5. Повторить этот реальный mTLS/HTTP test; затем продолжить crash, genuine
   unknown-COMMIT, concurrency и restore acceptance. Не принимать старые approvals
   автоматически для изменённого контракта и не менять production allowlist.

P0: не обнаружены. Другие P1/P2 не подтверждены; это не утверждение о завершённом
безусловном аудите всей ветки.

## N. Неиспытанные критические сценарии

- Реальный crash всего PostgreSQL до/после COMMIT и WAL-backed restart recovery.
- Настоящая потеря успешного COMMIT-response через transport fault.
- Два конкурирующих deployment attempts / lock contention.
- Backup/restore, cluster-wide roles и отказ прежнего binding в другом cluster.
- Полный повтор 47 collector DB и 129 API/legacy DB suites в рамках И4.
- Новая проверка ledger OID replacement и остальные неисполненные drift faults.
- Браузерный UI smoke.

Причина остановки — воспроизводимый P1; PASS других групп эти пробелы не закрывает.

## O. Production limitations

Production admission disabled. Нет production approvals/provisioning, push/PR/merge,
реальных business write commands или обращения к рабочей БД. 29 write routes и пять
GET сборки закрыты. Trusted deployment administrator остаётся доверенным в v1.0.
Новый отчёт не разрешает production или объединение ветки.

## P. Итоговый verdict

**BLOCKED.** Объединение в main сейчас не рекомендовано. Необходим исправленный
immutable ledger verification contract и повторная приёмка с закрытием критических
NOT_TESTED сценариев. Автоматическое принятие новых hashes/anchors запрещено.

## Q. Финальный HEAD / worktree / artifacts

HEAD неизменён: 8bb92afedbba35d5c14c6dab80d17cdf4b9cdb81. Feature worktree чистый,
main SHA/tree прежние, пользовательские семь untracked файлов основной main worktree
не затронуты. Commit не создавался: production-код не исправлялся, отчёт сохранён
вне Git согласно запрету commit только ради acceptance-отчёта.

Артефакты рядом с этим файлом:

- security_i4_regression_test.go — точный TEST-only regression, SHA256
  093590DDE1DA37A429E80665A5C20AC0E93E76DB714785273D66144C924F5AAC.
- run-i4-repro.ps1 — адаптация существующего readiness harness только для выбора
  нового negative regression; без production source changes.
- collector.test и api.test — собственные Linux TEST binaries, не добавлены в Git.
- e1b-i4-566d96030c2d4f029d23fefc2109bfb7-{setup,api,api-http,faults,verifier,cleanup}.log.
- соответствующие image/runtime/result files; go-tests.log, go-vet.log.

Для повторения: временно поместить точную regression-копию в
backend/internal/migrations/security_i4_regression_test.go, собрать Linux test
binaries через существующий Go tooling и запустить run-i4-repro.ps1 с явными
EvidenceDirectory/TestBinary/ApiTestBinary и -PopulateTestData. Ожидается FAIL двух
subtests при успешном cleanup. Затем убрать только эту точную временную TEST-копию
после сверки SHA256. Исходные тесты/validators/fixtures менять не требуется.
