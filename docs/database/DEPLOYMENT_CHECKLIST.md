# Операторский checklist clean-v2

Исполнять совместно с [runbook](CLEAN_DEPLOYMENT.md) и [safety](DEPLOYMENT_SAFETY.md). Это checklist нового чистого контура, не разрешение production rollout.

## Перед подключением

- [ ] Владелец согласовал назначение TEST-контура и карточку target: host/IP/port/database/user/schema, cluster system identifier, data directory, доступы, warehouse name, API bind, frontend origin и API URL.
- [ ] Для production получено отдельное решение после DB-04 и закрытия release gates; сам DB-03.6 такого решения не даёт.
- [ ] Ветка/commit и полный комплект исходников/SQL/manifest зафиксированы; незакоммиченные изменения не потеряны. Артефакт получен из доверенного reviewed release.
- [ ] PostgreSQL server/tools 18.x; Go toolchain из backend не ниже 1.26.9; Node не ниже 22.22.0, согласованный npm; lockfile сохранён. Рекомендован проверенный набор из release review.
- [ ] Новый data directory отсутствует; каталог и порты не принадлежат существующему серверу/процессу. Никакие чужие процессы не завершаются.
- [ ] Параметры подключения заданы явно, старый backend/.env не загружается. Секреты не находятся в argv/history/report/source.
- [ ] Целевая база не `tremor_sklad` (любой регистр), не postgres/template0/template1; имя не используется для обхода защиты старой базы.
- [ ] Сверены независимый pg_controldata/администраторская карточка и фактический READ ONLY SQL: server ID, адрес/порт, data directory, user, version, recovery=false.
- [ ] Для нового отдельного кластера до создания БД присутствуют только стандартные базы. Целевой БД ещё нет.

**STOP:** несовпадение target, неожиданная существующая БД, неизвестный кластер, нет прав доказать identity, не доказано разделение TEST/рабочих процессов. Не продолжать созданием/миграцией «на удачу».

## Пустая база и миграции

- [ ] Создание выполнено только из template0, encoding UTF8, provider builtin (`b`), locale C.UTF-8; повтор создания существующей БД не используется как reset.
- [ ] Фактическая новая БД/owner/server подтверждены; public не содержит прикладных объектов, business rows отсутствуют.
- [ ] Offline plan явно использует `--profile clean-v2 --manifest migrations/manifest.clean-v2.json`: 22 migration entries, 024 последняя, 002 excluded, 006 gap.
- [ ] `status` пустой БД: initialized=false, 22 pending. `verify` до apply ожидаемо неуспешен только из-за неполной схемы, а не из-за другой ошибки.
- [ ] После apply: initialized=true, applied=22, pending=0; status/verify успешны.
- [ ] Журнал содержит точные версии/name/checksum/profile/ordinal; нет 002/006, все profile=clean-v2. Повтор apply — no-op.
- [ ] `deployment_identity` содержит ровно один ненулевой UUID, ограничения и защитные триггеры проходят validator. UUID записан в карточку deployment.

**STOP:** неизвестная непустая схема, checksum mismatch, несовместимый/чужой ledger, неизвестная версия, неправильный порядок или профиль. Не редактировать старые SQL/manifest/ledger для прохождения проверки. Не выдавать migration verify за полный schema-diff.

## Bootstrap

- [ ] До bootstrap `warehouse validate` возвращает schema-ready; используется явно согласованное название, без пробелов по краям/контрольных символов, 1–200 Unicode characters.
- [ ] Bootstrap создал ровно один склад; повтор с тем же точным именем — no-op. Различие имени/более одного склада — конфликт, не автоматическое исправление.
- [ ] Validator возвращает bootstrap-ready; остальные 43 бизнес-таблицы пусты; ledger/identity — технические строки.
- [ ] Контрольные показатели и UUID сохранены **до** создания первого стеллажа.

**STOP:** неподтверждённая identity или неготовая bounded schema. Не выполнять bootstrap после начала бизнес-заполнения и не удалять данные для возврата к bootstrap-ready.

## Перед frontend и API

- [ ] DATABASE_URL задан безопасным внешним способом для новой БД; подтверждены фактические параметры отдельно от сообщения «connection established».
- [ ] TREMOR_SKIP_DOTENV=1; API_ADDR и FRONTEND_ORIGIN заданы явно; новые свободные порты. В TEST TREMOR_DISABLE_OUTBOUND_HTTP=1 и нет OZON_* credentials.
- [ ] После старта read-only pg_stat_activity подтверждает, что новый API подключён именно к утверждённой БД; /health не используется как доказательство target.
- [ ] GET /api/deployment совпадает с SQL UUID, не кешируется; mismatch/ошибка — STOP для записи.
- [ ] Frontend VITE_API_BASE_URL задан до dev/build; фактические browser request URL не указывают на старый API. FRONTEND_ORIGIN равен origin страницы без path.
- [ ] Для независимого контура выделен новый origin/profile. До загрузки приложения проверено отсутствие старых active/pending-команд; при их наличии не очищать storage.
- [ ] Защищённый journal работоспособен; если legacy/foreign/multiple pending или storage/identity недоступны — только просмотр, ручное разрешение по safety.
- [ ] UI загружен с сервера и принадлежит проверенному deployment; старые формы A не используются после смены на B.

**STOP:** неизвестные pending-команды, непроверенный UI context, невозможно доказать разделение процессов/портов/API URLs. Смена API URL сама по себе не изолирует storage прежнего browser origin.

## Первый запуск и фиксация

- [ ] До действий нет demo products, stock, orders или фиктивной истории; склад один. Каталог/история показывают корректные пустые состояния.
- [ ] По явному решению создан только первый согласованный стеллаж через «Ячейки»; название и числа ячеек проверены. Никакие товары/остатки не придуманы.
- [ ] Перечитаны стеллаж/ячейки и каталог; состояние реально сохранено в PostgreSQL, нет скрытой зависимости от 002.
- [ ] После стеллажа ожидаемо нельзя требовать bootstrap-ready: бизнес-таблицы уже непусты. Выполнены migration status/verify и целевые read-only проверки данных вместо повторного bootstrap.
- [ ] Зафиксированы версии, commit/source hashes, карточка target, 22 ledger-row, deployment UUID, warehouse, вывод проверок, browser origin/API URL, скриншоты/ошибки и решение владельца.
- [ ] При завершении TEST-прогона остановлены только собственные процессы по сохранённым PID/data directory, порты освобождены. Нет автоматического удаления БД/данных.
- [ ] Открытые release gates из [DB03_RELEASE_REVIEW.md](DB03_RELEASE_REVIEW.md) явно приняты/закрыты. Production нельзя объявить готовым только на основании TEST PASS.
