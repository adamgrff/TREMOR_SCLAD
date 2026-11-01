# Explicit-target migration runner

Запускать из `backend`. Инструмент не создаёт базу/схему, не делает warehouse-bootstrap, не запускает API, не загружает `.env` и не использует `DATABASE_URL` как target. Старые частные migration CLI не нужны для этого профиля.

## Manifest и переносимый checksum

`migrations/manifest.clean-v1.json` — утверждённая версия профиля `clean-v1`: 21 schema-миграция, номер/имя/SHA-256/порядок. `002_seed.sql` явно исключён как demo, `006` — исторический gap. Эти номера никогда не записываются как applied. Текущая policy намеренно ограничена проверенной цепочкой 001–023; расширение профиля требует ревью manifest/policy, а не автоматического применения неизвестных файлов.

`utf8-lf-v1`:

1. Только валидный UTF-8, без BOM и NUL.
2. CRLF преобразуется в LF; одиночный CR запрещён.
3. Остальные байты сохраняются: нет trim, изменения пробелов, Unicode normalization или добавления финального LF.
4. SHA-256 считается по этим каноническим байтам **всего** SQL-файла, включая комментарии и внешнюю рамку. Исходники на диске не переписываются.

Векторы для LF/CRLF и всех 21 миграций проверяются Go-тестами. Фактический Linux-прогон требует Linux-стенда; cross-build не заменяет выполнение на Linux.

## Режимы

```powershell
go run ./cmd/migrate plan --profile clean-v1
```

`plan` читает/проверяет только локальные файлы, хеши и SQL-границы; соединения не открывает. Default manifest path — `migrations/manifest.clean-v1.json`; можно явно передать `--manifest`.

`status`, `verify`, `apply` требуют **все** параметры target. Образец ниже содержит placeholders и не является командой подключения к существующей рабочей БД:

```text
go run ./cmd/migrate status --profile clean-v1 --host <HOST> --port <PORT> --database <NEW_DATABASE> --user <EXISTING_USER> --schema public --server-address <EXPECTED_SERVER_IP> --server-id <APPROVED_SYSTEM_IDENTIFIER> --sslmode verify-full --sslrootcert <CA_FILE>
```

- `status`: read-only проверка журнала/метаданных и список applied/pending. Пустая БД без ledger возвращает initialized=false; неизвестная непустая БД — ошибку.
- `verify`: те же проверки плюс требование полного совпадающего applied-prefix. Неполный deployment — ошибка, не запись.
- `apply`: один session-level advisory lock, повторная проверка состояния под lock, и применение недостающих миграций. Полный совпадающий журнал — no-op.
- Общий deadline — `--timeout`, по умолчанию 5 минут, включая ожидание lock. Положительный deadline обязателен.
- stdout — JSON plan/state; stderr — безопасная диагностика. Код 0 — успех; ненулевой код требует проверки сообщения/state перед продолжением.

Пароль при необходимости передаётся **только** через `MIGRATION_PASSWORD`, установленный безопасным внешним способом. Не передавать пароль через CLI/DSN/командную историю. DSN/драйверные ошибки с параметрами не печатаются. Содержимое `PGOPTIONS`/fallback target и неявный пароль не наследуются для выбора подключения.

## Safety contract

- host/port/database/user/profile обязательны; схема существует заранее (`public` по умолчанию). Системные схемы запрещены.
- `tremor_sklad` запрещена независимо от регистра; также запрещены maintenance/template targets postgres/template0/template1. Нет auto-adoption, DROP/RESET/TRUNCATE, down или reset-mode.
- Ожидаются PostgreSQL 18.x, UTF8, builtin `C.UTF-8`, primary/non-template БД. Другой профиль параметров отвергается, а не исправляется автоматически.
- Сверяются actual current_database/current_user, inet_server_addr/port и PostgreSQL `system_identifier`. Последний должен быть получен из **заранее одобренной** информации о кластере (например, администратором через `pg_controldata`), а не принят на доверии от случайного endpoint.
- Проверки выполняются в read-only startup session с `search_path=pg_catalog`. Имена системных функций/каталогов qualified: одноимённая функция неизвестной схемы не может подменить identity check.
- Нужна возможность читать `pg_control_system()`. Если существующей роли этого не разрешено, runner отказывает. Инструмент не создаёт роли и не выдаёт права; DB-04 здесь не реализуется.
- `verify-full` требует явного CA. `disable` допустим только для точного loopback IP; это режим изолированных локальных тестов, не рекомендация для удалённого сервера.
- Без собственного marker/валидной формы ledger отказывается работать с непустой БД; проверяются namespace dependencies/расширения, включая объекты за пределами target schema.
- Пока есть pending-миграции, `clean-v1` отказывается продолжать при бизнес-строках, RLS или неожиданных таблицах вне target. Это fresh/resume-construction, не перенос существующей рабочей схемы. Полностью applied target с бизнес-данными допускает read-only status/verify и apply no-op.

## SQL framing и атомарность

Лексер проверяет один внешний **bare `BEGIN; ... COMMIT;`**. Пропускаются корректные line/nested block comments, обычные/E-строки, quoted identifiers и dollar quotes. Неполная кавычка/комментарий/рамка или transaction/session control внутри тела — ошибка до подключения. Top-level DROP/TRUNCATE также запрещены; существующие ALTER DROP CONSTRAINT не удаляют данные и остаются частью утверждённых исходников.

После проверки берётся точный byte slice **между** внешними statements. Глобальной замены BEGIN/COMMIT и разбиения SQL по `;` нет. Runner начинает READ WRITE/READ COMMITTED transaction, выбирает target через SET LOCAL search_path и передаёт тело целиком через pgx simple protocol. INSERT ledger находится в той же транзакции. Комментарии/PLpgSQL bodies не пересериализуются; нормализация CRLF описана отдельно и входит в checksum-контракт.

`schema_migrations` хранит version/name/checksum/profile/ordinal/applied_at. PK/UNIQUE/CHECK, проверка формы таблицы и marker отличают ledger этого runner от случайной одноимённой таблицы. Пользовательские triggers/rules на ledger отвергаются. INSERT обязан затронуть ровно одну строку; её совпадение проверяется до COMMIT, а продвижение prefix — после. Валидируется **точный prefix** manifest: изменение имени, hash, профиля, порядка, пропуск или неизвестная версия — отказ. Повтор не меняет applied_at.

Session lock удерживается одним соединением через всю пачку, включая создание ledger и отдельные commits. При закрытии соединения lock освобождается. Ledger initialization тоже транзакционная; бизнес-миграции не начинаются до успешного commit её структуры.

### Неопределённый COMMIT

Никакого автоматического повтора SQL после ошибки COMMIT. Старое соединение закрывается; новое **read-only** соединение повторно проверяет target/system ID/database OID и журнал.

- Строка ожидаемой миграции доказанно присутствует: commit подтверждён. Если всё применено — успех; иначе `commit_resolved=true` и ошибка `ErrResume`: повторить **команду apply**, которая заново возьмёт lock и применит только отсутствующий suffix.
- Строка отсутствует либо проверка недоступна: `ErrCommitUnknown`. Отсутствие может ещё гоняться с завершением старого backend, поэтому оно не объявляется rollback. Повторный запуск после проверки target возьмёт session lock и прочитает фактический журнал до любых новых записей.

Предыдущие успешные миграции остаются committed. Частичного applied-row для незафиксированного SQL нет. Автоматического rollback всей пачки/down нет.

## Проверки

```powershell
go test ./internal/migrations ./cmd/migrate -count=1 -v
go vet ./...
```

Без явно подготовленных `MIGRATION_TEST_*` PostgreSQL-тесты пропускаются; это не доказательство прохождения интеграций. Требуются HOST/PORT/DATABASE/USER/SERVER_ADDRESS/SERVER_ID, опциональный PASSWORD; только loopback и имя `tremor_*_test`. Fixture проверяет реальное подключение read-only до создания собственного случайного schema. Test-only cleanup удаляет только созданную fixture схему с тем же OID, на повторно проверенном TEST-target; production runner такого cleanup-кода не имеет.

`verify` проверяет metadata/checksum/параметры, не является полным pg_catalog schema-diff и не защищает от владельца БД, подделывающего журнал/DDL. Система управления ролями — DB-04. Файл manifest и бинарник должны приходить из доверенного reviewed release.
