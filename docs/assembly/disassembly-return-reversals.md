# R4.2A — append-only модель компенсации возврата

Этот этап подготавливает только схему и текущую read-семантику. Пользовательского
action/API отмены, кнопки, новых состояний интерфейса и новых видов операций
`assembly_warehouse_operations` нет. Миграции 001–018 не изменяются.

## Миграция 019

`019_assembly_disassembly_return_reversals.sql` добавляет:

- `assembly_disassembly_return_reversals`: UUID `id`, уникальный `request_id`,
  уникальный `return_id`, время `occurred_at`;
- FK `request_id` к прежнему `assembly_preview_requests`, deferred до COMMIT,
  аналогично физическому возврату;
- FK `return_id` к конкретной неизменяемой записи физического возврата;
- запрет UPDATE/DELETE reversal тем же immutable trigger, что у других событий;
- view `assembly_disassembly_effective_returns`;
- индекс по `assembly_disassembly_returns.item_id` вместо прежнего UNIQUE;
- актуальные validation/read functions, без изменения прежних миграций.

Действующий возврат определяется только во view: физический return, у которого
отсутствует reversal с тем же `return_id`. Исторический return не удаляется.
`UNIQUE(return_id)` запрещает повторную компенсацию одной и той же записи.
После компенсации допускается новый return той же исходной единицы.

## Не более одного действующего возврата

Validation trigger нового return сохраняет прежние проверки активной сессии,
допустимости исходной единицы, pending item, product/cell исходного pick и
отсутствия обычного `undo_pick`. Дополнительно он блокирует исходную единицу
`FOR UPDATE`, а затем проверяет отсутствие действующего return во view.
Запись reversal блокирует ту же исходную единицу; историческая связь с return
и однократность обеспечены FK/UNIQUE.

Штатные handlers уже работают в READ COMMITTED и под общим workspace/session
lock. Конкурентные INSERT одной единицы сериализуются на строке источника:
проигравшая попытка после ожидания видит действующий возврат и отклоняется.
SERIALIZABLE также безопасен: проигравшая запись отклоняется constraint либо
serialization failure. Прямые записи в REPEATABLE READ запрещены validation
trigger: фиксированный snapshot мог бы пропустить конкурента после ожидания.
Это ограничение только записи ledger; исторические read-only транзакции в
REPEATABLE READ продолжают работать.

Пользовательские условия отмены — active/pending/последний возврат, складское
списание и событие отмены — не реализуются здесь; это следующий этап R4.2B.
Сам INSERT reversal не выполняет складское списание. В тестах обе записи и
компенсация количества подготавливаются только внутри изолированной фикстуры.

## Аудит обращений к assembly_disassembly_returns

### Current-state queries — переведены на effective returns

| Место | Семантика после 019 |
|---|---|
| `assembly_load_disassembly` | LEFT JOIN view: `returned`, `returnId`, `returnedAt` относятся только к действующему return; исходная единица не дублируется при повторных возвратах |
| `applyDisassembly`: scan товара | Поиск единицы без действующего return; компенсированная единица снова выбирается обычным процессом |
| `applyDisassembly`: finish | Остались ли единицы без действующего return |
| `verifyWarehousePicks` | Из действующих picks исключаются только действительно возвращённые, а не исторически возвращавшиеся единицы |
| `assembly_validate_disassembly_return` | Запрет второго действующего return; старый компенсированный return не мешает новому |
| `assembly_disassembly_no_undo_returned` | Запрет обычного undo для действующего физического возврата; сама операция undo_pick и её ledger не меняются |
| `assertDisassemblyFinishPreconditions` в тестах | Прямые проверки remaining/outstanding используют тот же view |

До 019 reversals не существуют, поэтому `disassemblyReturnsRelation` возвращает
прежнюю таблицу целиком. После 019 выбирается только общий effective view.
Это обратная совместимость при развёртывании, а не второе определение reversal.

### Historical/audit queries — намеренно оставлены полными

| Место | Причина |
|---|---|
| `assembly_problem_archive.go`, `journals.disassembly[].returns` | История всех физических возвратов, включая позже компенсированные |
| Число записей return в прежних integration tests | Проверяется число созданных складских событий, а не текущий прогресс |
| Снимки ledger в finish-blocker/rollback тестах | Должны замечать удаление/переписывание любой исторической записи; добавлен также снимок reversals |
| Новые model tests: исходные строки, времена, история и архив | Подтверждают сохранность return #1 → reversal #1 → return #2 |

Исторический endpoint не переделывается и не получает новую UI-функциональность.
Сами reversal-записи доступны в отдельном неизменяемом реестре БД.

### Обращения, которые менять не потребовалось

- Production `INSERT INTO assembly_disassembly_returns`: новая физическая
  операция всегда добавляется в полный ledger, а не во view.
- FK reversal и исходных записей, immutable triggers, DDL и индекс: ссылаются
  на реальные записи, независимо от их текущего действия.
- Исходная таблица внутри определения effective view и validation reversal:
  нужны все физические возвраты, в том числе компенсированные.
- Миграция 018 содержит прежние определения как исторический шаг; 019 заменяет
  функции через CREATE OR REPLACE. Старые миграции не редактируются.
- Проверки существования сессии в problem/Ozon-коде не определяют returned и
  продолжают использовать сессии. Ledger сборки остаётся pick/undo_pick.
- Прямые INSERT/снимки полной таблицы в тестовых фикстурах остаются полными;
  проверка текущего количества использует effective view отдельно.

## Проверки

Все проверки PostgreSQL выполняются в автоматически удаляемых временных схемах
с отдельным search_path. Миграционный runner подготовлен к 019, но на рабочей
БД не запускается.

Модельные тесты проверяют обновление 018 → 019 с уже существующим возвратом без
изменения workspace/ledger/history; повторный физический возврат через прежний
scan handler; обнуление возвращённого состояния после reversal; отсутствие
размножения items; новые returnId/returnedAt; finish и reconciliation; полную
историю; FK/UNIQUE/immutability; конкурентные INSERT до и после reversal в
READ COMMITTED и SERIALIZABLE; запрет записей из REPEATABLE READ; защиту от
повторного возврата уже отменённого обычным undo_pick исходного отбора.

Из backend: `go test ./cmd/api -run '^TestDisassembly' -count=1 -v`,
`go test ./... -count=1`, `go vet ./...` с локальным TEST_DATABASE_URL.
Из frontend: `npm test`, `npm run lint -- src scripts vite.config.ts`,
`npm run build`. Frontend, дизайн и visual baseline этим этапом не меняются.
