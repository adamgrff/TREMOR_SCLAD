# DB-03 Release Readiness

Дата сверки: 2026-11-01. Ветка feature/db03-clean-database, исходный HEAD 1f562bbbc30796aa7c77c587c493d1203d415061. Реализация пока находится в незакоммиченном рабочем дереве; один HEAD не идентифицирует комплект DB-03. Перед выпуском необходимы отдельное разрешение на фиксацию и идентификация полного reviewed artifact.

## Разделение готовности

| Контур | Оценка |
|---|---|
| Новый изолированный Windows TEST, clean-v2 | Путь реализован, миграции/bootstrap/API/UI и identity E2E подтверждены |
| Перенос на другой Windows-компьютер | Документирован; каждый target требует новой проверки, identity и runtime |
| Linux runtime | Команды документированы, фактический прогон отсутствует |
| Удалённый PostgreSQL с TLS | Guard verify-full/CA реализован, реальный remote TLS не проверен |
| Production | **Не готов к разрешению запуска по одному DB-03**: DB-04 и операционные gates открыты |
| Upgrade существующей БД | Не реализован/не утверждён; отдельный проект |

## Реализовано

- DB-03.1: аудит старых миграций и зависимостей чистой схемы.
- DB-03.2: versioned manifest, utf8-lf-v1 checksum, transactional ledger, offline plan/status/verify/apply, строгий target и session advisory lock, продолжение после сбоя и разрешение неопределённого COMMIT по ledger.
- DB-03.3: отдельный PostgreSQL 18, template0/UTF8/builtin C.UTF-8, чистая цепочка, 44 пустые бизнес-таблицы, проверка Unicode и негативных сценариев.
- DB-03.4: initial warehouse bootstrap, конкурентный no-op/conflict/rollback; bounded read-only readiness validator. Никаких demo товаров/остатков.
- DB-03.5: отдельные API/frontend, явные env/address/origin, outbound HTTP guard и функциональная регрессия нового склада.
- DB-03.5.1: migration 024, постоянная identity, новый fresh-only clean-v2 из 22 миграций, серверная транзакционная проверка и durable journal.
- DB-03.5.2: привязка действий к UI snapshots; stale A нельзя перепривязать к B новым handshake; вторичные формы и каталог без активной сессии.
- DB-03.5.3: настоящие browser/HTTP/PostgreSQL проверки stale UI, безопасного нового контекста и pending после COMMIT.
- DB-03.6: [runbook](CLEAN_DEPLOYMENT.md), [safety](DEPLOYMENT_SAFETY.md), [checklist](DEPLOYMENT_CHECKLIST.md), этот review; новых runtime/DB-изменений нет.

## Версии: требования и фактическая проверка

| Компонент | Контракт кода | Проверенный стенд |
|---|---|---|
| PostgreSQL | Только major 18; UTF8, provider builtin, datlocale=C.UTF-8, primary/non-template | 18.4 Windows |
| Go | backend/go.mod: go 1.26.9 | Из backend `go env GOVERSION`=go1.26.9, GOTOOLCHAIN=auto |
| Node | По текущему package-lock пересечение engine-требований: >=22.22.0; Node 20 не подходит из-за react-router 8.3.0 | v24.18.0 |
| npm | В package.json нет engines/packageManager pin; lockfileVersion=3 | 12.2.0; для воспроизведения использовать этот согласованный набор |
| Vite | package-lock фиксирует 8.1.3, package.json range ^8.1.1 | 8.1.3 |

На этом компьютере базовый `go version` из корня выдаёт go1.26.4, но в backend автоматически выбирается go1.26.9. Для другого/offline компьютера установить 1.26.9 заранее или согласовать доступ к toolchain; нельзя считать старую bundled version достаточной. Версии выше проверенного набора требуют своей валидации.

Старые README cmd/migrate и cmd/warehouse, часть help/comment strings описывают исторический clean-v1. Фактические manifest/Target/Runner поддерживают v2, но defaults не переключены. Для нового install каноническая последовательность здесь — с двумя явными v2-флагами. Старые файлы на этапе документации не редактировались.

## Проверенные результаты и границы доказательств

Исторические числа ниже относятся к указанным прогонам, не суммируются и не объявляются повторно выполненными в DB-03.6.

| Этап | Фактические результаты |
|---|---|
| DB-03.3 | Clean-v1, 21 миграция; 12 migration integration tests и unit/CLI, 0 FAIL/SKIP; Unicode lower/upper/search |
| DB-03.4 | 12 прежних migration + 5 новых warehouse integration tests, unit/CLI, 0 FAIL/SKIP; конкурентность, no-op/conflict/rollback, первый rack SQL fixture |
| DB-03.5 | Полный Go: 155 верхнеуровневых/293 с subtests; frontend 22 checks, vet/lint/build; реальный UI базового склада |
| DB-03.5.1 | Полный Go: 158/331, 0 FAIL/SKIP; frontend 23; две независимые clean-v2 identity, API restart/switch, guards |
| DB-03.5.2 | Полный Go: 159/332, 0 FAIL/SKIP; frontend 24; vet/lint/build; stale UI VM/TSX + реальные API/SQL |
| DB-03.5.3 | Настоящий Chromium, реальные frontend/Go/PostgreSQL: пять stale действий, безопасный B edit без активной сессии, pending A → B → A; 86 browser HTTP-запросов и полные снимки 46 таблиц |
| DB-03.6 | Только read-only аудит исходников/артефактов, локальные offline plans и документационные проверки; БД/приложения/тесты не запускались |

Последние артефакты доступны во внешнем каталоге checkpoints рядом с checkout:

- `db03-5-2-20261101-f19afc1b0b/results.json`, `backend-full-tests.stdout`, `statistics-repeat-tests.stdout`;
- `db03-5-2-frontend-checks.log`;
- `db03-5-3-20261101-aa2c508411/browser-verification.json`, `browser-network.jsonl`, `*-snapshot.json`, `*-browser.json`, JPEG и `ports-stopped.json`;
- `db03-2-report.md` … `db03-5-3-report.md`.

Эти локальные paths — provenance, не переносимая поставка всех доказательств. Перед публикацией release владелец должен решить, где хранить проверенный набор артефактов без секретов.

### Что доказал реальный browser E2E

Удаление истории, редактирование/архивирование/создание каталога и изменение количества из старого A UI после смены API на B: **0 изменяющих HTTP-запросов**, нет новой journal-entry, storage и все 46 таблиц A/B неизменны. Для первых четырёх сценариев нет activeSession/pending, поэтому исходный stale binding действительно проверен.

После явного чтения B допустимый PUT меняет только имя товара B; A не меняется; активной сессии для чистого catalog edit не нужно. Потеря ответа после COMMIT оставляет pending A; загрузка против B не повторяет/не изменяет её; возврат A повторяет тот же UUID/body/identity и не увеличивает количество второй раз (1 → 2 → 2).

Это не доказывает все браузеры, все устройства сканеров или аппаратные отказы. Browser network наблюдался HTTP-прокси и Resource Timing; не заявляется DevTools HAR. Synthetic storage принадлежит отдельному TEST-origin.

### Cached reload и UX

В связке Vite/внешний proxy/IAB часть cached reload оставляла пустой документ/неактуальную iframe binding; точная причина не установлена, дефект приложения не доказан. Pending проверен новой загрузкой с query-маркером при том же origin/storage/API URL, а stale-попытки — без перезагрузки A перед действием. Обычный cached reload стенда не считается надёжно проверенным. Требуется отдельная проверка на конечной static-hosting/cache-конфигурации; query-маркеры не являются production-решением.

На скриншотах blocking notice слабо контрастирует с фоном; видимость/расположение сообщений при разных viewport/scroll и отсутствие бесполезного «Повторить» для ручного разрешения требуют UX-ревью. Защитные disabled состояния/отсутствие записи проверены, дизайн здесь не исправлялся. Ручного оператора разрешения journal пока нет.

## Открытые production gates

1. **DB-04:** роли, наименьшие права, владельцы schema/ledger/защитных объектов, service credentials; текущие TEST owner-guards не заменяют это.
2. Реальный Linux runtime (если выбран Linux), remote TLS verify-full, CA/hostname validation, permissions к pg_control_system(), сетевые границы.
3. Утверждённые host/database/storage/service user/API bind/frontend origin, HTTPS/secure browser context. crypto.randomUUID требует подходящего браузерного контекста; HTTP loopback проверен, произвольный HTTP LAN не утверждён.
4. Production service management, static SPA hosting/cache headers/fallback, logs/secrets policy, monitoring, backup/restore drill, RPO/RTO; эти инструменты DB-03 не поставляет.
5. Upgrade/перенос данных старой БД, сохранение receipts/events/ledger lineage и разрешение реальных pending: отдельный проект. Не обходить запрет target переименованием.
6. Самостоятельный data-clone/rekey, recovery после старого backup и отложенных команд: отдельная процедура, не automatic migration.
7. Ozon credentials/реальные операции, сборка и её бизнес-правила не расширялись; outbound guard процесса — не универсальный firewall для будущего custom transport.

## Решения владельца и отдельное разрешение

Владелец утверждает: название склада и бизнес-справочники; deployment target и локальный/удалённый режим; runtime/ОС и TLS; секреты и роли; origin/profile и способ хранения journal evidence; оператора ручного разрешения; backup/RPO/RTO; cache/UX замечания; место публикации proof artifacts.

Создание production-БД, переключение живого приложения/пользователей, применение миграций к существующей БД, импорт данных, ротация identity/clone, ручное изменение пользовательского journal, включение реального Ozon и commit/push/merge требуют отдельного разрешения. DB-03.6 не выполняет и не разрешает эти действия. Чистый TEST-путь готов к воспроизведению по checklist; production readiness остаётся отдельным решением после закрытия gates.
