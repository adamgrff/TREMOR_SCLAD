# План безопасного внедрения v1.0

Это план, не разрешение на выполнение. Коммиты и push не выполняются автоматически. Каждый номер ниже — отдельная небольшая задача с проверкой и остановкой до следующего разрешения. Числа при подключении не перекалибровывать; реальное противоречие оформлять отдельно.

Актуализация25.10.2026: **I1–I12.7 завершены и согласованы**, включая I12.3.1–I12.3.2. Page Title реализован28/36/700, capsule4×12,r999, контекст X12/gap10; Compact-варианта нет. Описания исходных этапов ниже — исторический план и критерии, а не незавершённые работы или повторное разрешение.

## Общие проверки

Работать от актуальной локальной ветки со всеми её изменениями, а не от GitHub main. Перед разрешённым изменением сохранить свежую файловую точку отката, включая tracked/untracked файлы. Не делать reset/stash/commit ради чистого Git и не терять существующие изменения Разбора.

Снять baseline рабочих окон и исключённых областей в dark/light: фиксированные данные и время, одинаковые браузер, шрифт, DPR, viewport, zoom0.7. Для изменённых элементов ожидается новый дизайн; для исключённых — отсутствие визуального изменения. Не требовать полного pixel-equality старой и новой типографики на мигрированной странице.

Целевые desktop:1280/1440/1920. Для modal дополнительно900×500. Это проверки переполнения, не новый мобильный дизайн. Никаких destructive запросов к рабочему складу ради скриншотов: использовать fixtures или безопасные тестовые данные.

Проверки пропорциональны риску: CSS/токены — lint/build и точечная визуальная проверка; компоненты — профильные тесты/состояния; логика/API/сканирование — полная профильная регрессия. Для финализации I12.8: один заключительный `npm run lint`, `npm run build`, `npm test` из frontend и компактный Chrome smoke1440px/zoom0.7 в dark/light. Если обычный npm недоступен — bundled Node/npm-cli.js. Текущие scripts/test-*.mjs частично проверяют исходный контракт; они не заменяют реальное поведение в браузере. Не скрывать падения и не переписывать ожидания без выяснения причины.

## Этапы и конкретные файлы

### D0. Принять и разместить документацию

D0 выполнен24.10.2026 по разрешению владельца. Актуальный комплект перечислен в [README.md](README.md); прежний утверждённый документ сохранён в archive, исторический DESIGN_SYSTEM.md — с предупреждением о legacy. Ссылки, приоритеты и реестр проверены. CSS/TSX не изменены; I1 не начат.

### I1. Пассивный фундамент — выполнено

Файл: `frontend/src/styles/tokens.css`.

Добавить только foundation-токены v1.0 в отдельном неиспользуемом пространстве имён `--ds-v1-*` и opt-in scope: шрифт как значение, шкала, палитра, поверхности, радиусы, отступы. Не менять существующие `--ui-*`, ui.css, TSX, импорты, font-face или потребителей. Не добавлять marker на страницы.

Риск: даже новая переменная может совпасть с текущим потребителем; изменение :root или наследования может повлиять на исключения. До записи проверить отсутствие ссылок на namespace. Критерий: computed styles и изображения страниц до/после идентичны; build/lint/test проходят. Остановиться до разрешения I2.

### I2. Изолированный Noto и один заголовочный эталон

Файлы: `frontend/src/assets/fonts/` — Noto и лицензия; `frontend/src/styles/ui.css`; `frontend/src/pages/ReceivingPage/ReceivingHistory.tsx`.

Подключить файл шрифта только к явно выбранной области и оформить один Page Title истории. Не переключать весь body панели на новые классы в том же diff. Технический font-family alias для того же Noto допустим, если глобальная @font-face с именем Noto Sans поменяет legacy fallback.

Риски: внезапное изменение шрифта Разбора и других старых страниц; длинный заголовок; смещение контекста. Проверки: фактические400/600/700, 28/36/700, capsule4×12, context-X12 и вертикальный title-context10; исключённые области и light неизменны; `test-theme-storage.mjs`.

### I3a. Neutral, Icon и Sort на истории

Файлы: `styles/ui.css`, `ReceivingPage/ReceivingHistory.tsx/.css`.

Существующим кнопкам и SVG дать opt-in классы. Учесть ширину action-column до установки48px; сортировку оставить24px. Обработчики не менять.

Риски: вытеснение данных; выбор строки при нажатии action; заголовок «Действия» остаётся видимым. Проверки: группы1/2/3, gap12, SVG18, sort-center, disabled/loading/focus, отдельные row/action клики, длинные данные.

### I3b. Primary и Negative на одном footer

Файлы: `styles/ui.css`, `AssemblyPage/ActiveAssembly.tsx`, при необходимости scoped-правила в `AssemblyPage.css`.

Оформить существующие действия, сохранив onComplete/onProblem/blocked. Проверить52px, radius999 включая hover, pressed2/80, отсутствие hover у disabled, длинный локальный loading label и стабильность ширины. Регрессии: `test-warehouse-assembly.mjs`, `test-assembly-recovery.mjs`; визуальная проверка без реальных складских операций.

### I4a. Обычный Input и Scan Input

Файлы: `styles/ui.css`, `ActiveAssembly.tsx`, затем отдельной задачей `ReceivingPage.tsx`; локальные CSS только при необходимости.

Перенести только визуальный контракт48/22/30. Сохранить refs, значение, нормализацию кода, Enter, autofocus, restoreScanFocus, guards. Риск: один Enter создаёт два запроса или фокус уходит после обновления. Проверить правильный/неправильный код, blocked/loading, возврат фокуса и восстановление draft; существующий scan flash/звук не переписывать.

### I4b. Search по одному варианту

Файлы: `AssemblyPage.tsx/.css`; затем `ProductCatalog.tsx/.css` и `SkuMappingDialog.tsx` отдельно.

Риски: query/selection теряются, внутренний input получает отдельную рамку, раскрытие раздвигает layout. Проверки:220+48, раскрытие влево, click outside/Escape, Enter/повторная лупа, выделение старого запроса, промах сохраняет selected, прокрутка нужного списка, local error18/24/gap6, focus всей static-оболочки.

### I4c. Select: сначала проверить native-путь

Файлы: `OrderProblemDialog.tsx`, `SkuMappingDialog.tsx`, `ProductCatalog.tsx` и scoped CSS, по одному контексту.

Нативное меню может не соответствовать всем пиксельным требованиям. Если нужен custom combobox/listbox — отдельное техническое решение и разрешение, не скрытая замена в CSS-задаче. Проверить ArrowUp/Down, Home/End, Enter, Escape, Tab, поиск по символам, выбранное значение, роли, возврат фокуса и native dialog top layer.

### I5. Window и Control Panel по одной поверхности

Файлы: `styles/ui.css`, выбранный section в `ReceivingHistory.tsx/.css`; затем ProductCatalog, ProblemOrders и ActiveAssembly отдельными задачами.

Риск: общий `.receivingPage__panel` используется несколькими окнами. Подключать только opt-in; не менять глобальный класс и grid. Проверки: Window без border, padding24×40, Control border/padding20, blur8, фон темы сохранён, light/Разбор/Header неизменны.

### I6a. Таблица истории

Файлы: `ReceivingHistory.tsx/.css`, `styles/ui.css`.

Подключить header64, row-min88, padding12, gap4, dividers1/2, action48, selected14/20, expanded4%. Сохранить сортировку, selection, раскрытие и существующую пагинацию. Проверить длинные SKU/даты/названия, рост строки, вертикальные оси, кнопки справа. API total и число страниц не придумывать.

### I6b. Сложные таблицы и sticky по одной

Файлы: `ProductCatalog.tsx/.css`, затем `ProblemOrders.tsx/.css`.

Риск: через прозрачный sticky-header просвечивают строки. Сначала доказать локальное решение общей геометрии header/body; если нужен DOM split/scroll-sync — отдельная маленькая структурная задача. Не строить DataTable. Проверять семантику, горизонтальную прокрутку, общие оси; у каталога — три кнопки и192px action-резерв рядом с формой. Не ужимать шрифт ради места.

### I7a. Badge и два вида Counter

Файлы: `styles/ui.css`, существующие узлы в `ProblemOrders.tsx`, `ProblemArchive.tsx`, `ReceivingPage.tsx`, по одной группе.

Риски: Badge растягивается по колонке; контур неверен после загрузки Noto; цифры обрезаются. Проверки:1/2/3 строки, симметричный padding по longest line, font/resize-sync, zero/large values, обычный Counter40square, PageCounter36. Не присоединять сюда +N истории.

### I7b. Main и Compact Progress

Файлы: `ActiveAssembly.tsx`, `ProblemOrders.tsx` и их CSS.

Сохранить расчёт и aria. Проверить28/8, border/padding, один track, blue/green, 0/0 не завершён, caption22/30/600 обычного регистра и value20/28;220ms/reduced0. Риск: общий класс вернёт подписи compact роль Section Title.

### I8a. Modal — внешность по одному существующему диалогу

Файлы: диалоги в `ReceivingHistory.tsx`, `ReceivingQuantityDialog.tsx`, `ProductCatalog.tsx`, `OrderProblemDialog.tsx`, `SkuMappingDialog.tsx`; соответствующие scoped CSS.

Сохранить состав формы/валидацию/autofocus. Проверить padding24×40, content-aware размеры с uppercaps640/960, footer вне scroll-body, viewport900×500, checkbox20, outline по мыши/Tab, зазор scrollbar24. Риск: общий `.receivingHistory__dialog` изменит немигрированные окна.

### I8b. Modal — Escape и Destructive Confirmation

Те же файлы, но отдельный diff обработчиков конкретного диалога. Текущий callback после preventDefault надо исключить из cancel-события, не меняя Cancel-кнопку и операцию.

Проверки: Escape/backdrop не закрывают; кнопки работают по прежнему сценарию; busy/inFlight блокируют повторы; фокус возвращается; title/description доступны. Проверить `test-sku-mapping.mjs`, `test-problem-creation.mjs`, `test-ozon-problems.mjs` и соответствующие реальные browser-сценарии. UI-поведение не смешивать с CSS или изменением API.

### I8c. Error Tooltip и Feedback

Файлы: реальные сообщения в `ProblemOrders.tsx`, `SkuMappingDialog.tsx`, `AssemblyPage.tsx`; scoped CSS. Не создавать глобальный toast-service.

Риски: alert-spam, Tooltip под modal, невидимые focus-stops, распознавание error по тексту. Проверки: фактические state/flags, hover/focus persistence, Escape закрывает только Tooltip, viewport-fit, retry guards, полное сообщение. Историю повторов не заменять маленькой подсказкой.

### I9. Event History Popover

Файлы: **`ProblemHistory.tsx` и `ProblemOrders.css`**. При необходимости допустим небольшой внутренний слой истории, не универсальный Popover. `groupProblemHistory.ts` не редактировать.

Проверки: `test-problem-history.mjs`; разные kind/operationId не объединяются; latest/previous и +N точны; даты/ID/режим полного списка сохранены. Затем1/18/много событий, длинный текст, Tab/End/Escape/возврат фокуса, pointer-переход500ms, scroll-container/viewport/native dialog. Риски: потеря данных, непригодная tooltip-семантика и clipping.

### I10a. Step Indicator

Файлы: `ActiveAssembly.tsx`, `AssemblyPage.css`, `styles/ui.css`. Малый presentation-only `components/ui/ScanStepIndicator.tsx` возможен только после проверки отсутствия подходящего компонента; предметный state остаётся в ActiveAssembly.

Проверки: cell→product→cell→shipment, undo/restore, error/blocked, тот же step, shipmentConfirmed, empty. Проверить46px, padding14, fonts.ready/resize, отсутствие сдвига текста,150/300ms и reduced0. Регрессии: `test-warehouse-assembly.mjs`, `test-assembly-recovery.mjs`. Риск: монотонный счётчик анимации покажет ложное завершение реального цикла.

### I10b. Таймер ожидания

Файлы: `AssemblyPage.tsx/.css`, только представление. Не менять formula/clock/пороги.

Проверки:0/10/11/20/21/30/31 минут, большие цифры, отрицательная разница времени как сейчас, visibility restore;18/24,min28,r8. Существующие четыре цвета не заменять общими Waiting/Success Badge.

### I11. Остальные панели первой волны

Файлы: ReceivingPage, ProductCatalog, AssemblyPage, ProblemOrders, ProblemArchive и их CSS — по одному небольшому набору узлов. Использовать уже принятые эталоны, не повторять калибровку. Проверять предметные сценарии конкретной страницы и light/excluded. Не превращать «второй проход» в массовую замену селекторов.

### I12. Подтверждённая уборка и реестр реализации

Файлы: только достоверно неиспользуемые локальные правила мигрированных областей и `docs/design/DESIGN_SYSTEM.md`.

Перед удалением — поиск всех потребителей и визуальная проверка покрытых экранов. Не удалять старые `.ui-*`/`--ui-*`, пока ими пользуется исключённый Разбор. Реестр отмечает фактически реализованные роли, а не все утверждённые требования.

## Отдельная функциональная очередь

Не выполнять под видом стилевой миграции:

- API total/pageCount истории приёмок; серверное изменение отдельно.
- Новая пагинация каталога/очереди/ожидающих/размещённых и правила перехода к найденному/отсканированному объекту.
- Замена native select, если нужен новый combobox/listbox.
- Структурный DOM split для сложного sticky, если CSS-способ не удовлетворяет требованиям.
- Разбор, фирменные области, светлая палитра, новые workflow/Ozon/API.

I1–I12.7 завершены. Перечисленные отложенные задачи не входят в готовый визуальный объём и требуют нового разрешения.


## Выполненное уточнение I12.3.1–I12.3.2 — Page Title

В I12.3.2 синхронизированы --ds-v1-page-title-font-size/line-height в frontend/src/styles/tokens.css с28/36; в frontend/src/styles/ui.css капсула обновлена до padding4×12, отступ контекста —12 и вертикальный gap —10. Имена классов/токенов не менять; не менять значения общих --ds-v1-space-16/12 ради одного компонента. Отдельный Compact-класс не создавать.

Проверить все уже подключённые Page Title, включая Историю и Очередь, caption alignment по тексту, длинные названия и счётчики, шапку с открытым/закрытым поиском, SEARCH-SCROLL-01,1280/1440/1920,zoom.7,dark/light. Section/Modal/Column и исключённые страницы не переопределять. Проверки выполнены при реализации; I12.8 не повторяет массовую матрицу.

## Реестр завершённых этапов

| Этапы | Завершённый объём |
|---|---|
| D0, I1–I2 | Документация, изолированные токены, локальный Noto и пилот Page Title |
| I3a–I3b | Neutral/Icon/Sort и Primary/Negative |
| I4a–I4a.2.1 | Scan Input Активной сборки и Приёмки, локальная ширина364px |
| I4b–I4b.1, I4c.1–I4c.4 | Раскрывающийся поиск, корректная прокрутка; шесть ordinary Select, native fallback |
| I5a–I5d, I5b.1, I5c.1 | Window/Control Panel/прозрачные Content Sections; код ячейки без CELL:ID |
| I6a–I6f, I6e.1 | Основные и вложенные таблицы; sticky-исключения; гонка SEARCH-SCROLL-01 устранена |
| I7–I10b | Badge/Counter/Progress, Modal/Tooltip/Feedback, Event History, Step и Wait Metric |
| I11 | Оставшиеся панели и внутренние Content Sections |
| I12.1–I12.2 | Финальный аудит, переносы и защита вложенных таблиц |
| I12.3, I12.3.1–I12.3.2 | Типографика, подписи капсул, окончательный Page Title28/36/padding4×12/X12 |
| I12.4–I12.5 | Оставшиеся кнопки/состояния и Input/Search |
| I12.6–I12.7 | Заключительный аудит; четыре локальных замечания исправлены |

## I12.8 — финальная проверка и подготовка Git

Синхронизировать только текущие документы/статусы, сохранить архив и числовые параметры. Выполнить один lint/build/full frontend suite и компактный Chrome smoke основных окон/модалок при1440px/zoom0.7,dark/light. SEARCH-SCROLL-01 — существующий регрессионный тест. Отделить изменения v1.0 от предшествовавших изменений Разбора и служебных материалов, подготовить список файлов/сообщение коммита. Не выполнять commit/merge/push и не удалять чужую работу. Новых этапов внедрения без разрешения нет.

Итог I12.8,25.10.2026: выполнено. JSON и77 foundation-привязок проверены, числовые значения реестра сохранены, внутренние ссылки корректны, архив/исторический аудит неизменны. Пройдены один lint/build/full frontend suite (11 проверок, включая SEARCH-SCROLL-01) и компактный Chrome smoke1440px/zoom0.7,dark/light на fixtures. Изменения исходников I12.8 не выполнялись. Перечень будущего коммита отделяет51 файл дизайн-системы от4 прежних посторонних файлов; staging/commit/merge/push не выполнялись.
