# Реестр компонентов и состояний v1.0

Статус чисел: утверждены1.1–1.7 и корректировка Page Title25.10.2026. I1–I12.7 выполнены и согласованы, включая применение нового Page Title в I12.3.2. Реестр ниже отражает фактическое внедрение первой волны; сознательные исключения не объявляются полностью мигрированными. Имена токенов/компонентов сохраняются.

Page Title всех окон, включая «ОЧЕРЕДЬ ЗАКАЗОВ»: Noto Sans28/36/700uppercase; капсула padding4×12,r999, прежние fillwhite78%/textblack/blur8/border0/shadownone. Контекст Xoffset12 совпадает с текстом капсулы; вертикальный gap10 сохраняется. Отдельного Compact Page Title нет. Section28/36/700 остаётся без капсулы; Modal/Column и исключения не меняются.

## Архитектура без дублирования

Сохранить существующие `frontend/src/styles/tokens.css` и `ui.css`. Не перезаписывать старые `--ui-*`/`.ui-*`, которыми пользуется Разбор. Реализовано отдельное пространство имён **`--ds-v1-*` / `.ui-v1-*`** с явным opt-in для тёмных мигрируемых панелей. Это временная совместимость с черновиком, не второй визуальный канон. Удалять legacy только после отдельной миграции всех его потребителей.

Реализованный scope (проверен на актуальном механизме темы): `html[data-theme="dark"] .ds-v1` на конкретной рабочей панели и её `.ui-v1-*` элементах. Не ставить marker на общий root страницы, содержащий Header/Разбор; не менять глобальное наследование font/color. Native dialog/top-layer должен иметь свой marker или получать только явно выбранные свойства, потому что не всякий overlay будет DOM-потомком панели. Проверять применимость scope после portal/popover.

Локальные Noto400/600/700 и OFL.txt находятся в frontend/src/assets/fonts/noto-sans-v1/. Реализован технический alias `Tremor Noto Sans v1`; старый fallback `Noto Sans`, body/html и фирменные шрифты глобально не переключались.

Базовая геометрия в CSS. React-обёртка нужна только при повторном поведении/измерении или для существующего повторяемого DOM. Не создавать по React-компоненту на каждый цвет текста. Существующие state, refs и обработчики остаются в предметных компонентах.

| Роль | Числовой контракт | Состояния | Текущая реализация / статус |
|---|---|---|---|
| Page/Section/Column/Text | 28/36/700;28/36/700;24/32/600;22/30;20/28;18/24 | Primary600 точечно | Реализованы `.ui-v1-page-title`, `.ui-v1-type-section/column/primary/secondary/meta` и `.ui-v1-type-semibold`; dynamic «ЗАКАЗ №…» — Column24/32/600 без капсулы |
| Page Title counter | 36px;20/28;gap12 | Опциональный, реальное число | CSS `.ui-v1-counter--page-title` реализован; подключать только к существующему счётчику внутри капсулы, без создания новых данных |
| Window/Control/Content | 24×40/20 padding, r24/16; Content без рамки | Темная opt-in поверхность | Существующие section/aside; `.ui-v1-window`, `control-panel`; без универсального Layout-компонента |
| Primary/Negative/Neutral | 52/52/48, r999/999/8 | Normal/hover/pressed/disabled/loading | Существующие button + `.ui-v1-action--primary/negative`, `.ui-v1-button`; не менять handlers |
| Icon/DeleteIcon | 48×48/SVG18 | Neutral; Delete краснеет на hover | Существующие SVG/actions; `.ui-v1-icon-button`, modifierdelete |
| Sort | 24×24/SVG18 | Asc/desc, disabled | Существующая сортировка; исключение из Icon48 |
| Input/Scan | 48px/22/30 | Hover/disabled/focus/error рядом | Существующие input и refs; `.ui-v1-input`; не новый scanner-hook |
| Search | Static48; expandable48+220 | Closed/open/query/error | У очереди существующие handlers/selection сохраняются; CSS-оболочка, не новая логика поиска |
| Select | 48;optionsmin48 | Closed/open/selected/hover | Шесть обычных Select: `.ui-v1-select--customizable`, совместный @supports base-select/::picker(select), max-height:min(432px,70vh), radius8; native fallback. Четыре modal Select: только закрытый control, системное меню сохранено |
| Table/List | Header64,rowmin88,cell12 | Hover/selected/expanded/empty | Существующие table/grid в страницах; `.ui-v1-table`, `row`, `actions`; **не универсальный DataTable** |
| Pagination | 5 основных объектов;buttons48,gap20 | Prev/next disabled | Существующая пагинация, без добавления новых страниц/total |
| StatusBadge | 20/28;padding5×8;r8;alpha16% | Info/waiting/error/success/neutral | Реализован components/StatusBadge/StatusBadge.tsx: одна подложка по longest line, измерение после fonts.ready/resize; существующие тона/данные |
| Counter | Min40square;22/30/600 | Реальное число, расширение квадрата | Отдельный от Page counter/+N |
| Main/CompactProgress | 28border3pad4 /8border0 | Blue/completegreen;0/0неcomplete | Существующие значения/aria; CSS-варианты, не объединение всех progress по высоте |
| Modal | r16,pad24×40,body-scroll | Open/closing/busy, Escape blocked | ReceivingHistory dialogs, QuantityDialog, ProductCatalog, OrderProblemDialog, SkuMappingDialog; native dialog сохраняется |
| Destructive Confirmation | Neutral48 +erroroutline | Normal/hover/disabled/loading | Удаление истории как эталон; CancelNeutral |
| ErrorTooltip | 18/24,pad10×12,max360 | Hover/focus,Escape close | Конкретная доступная причина ошибки; не контейнер истории |
| Feedback | 20/28;Search18/24exception | Actualerror/loading/success/empty | Существующие сообщения и flags, не новый toast-service |
| EventHistoryPopover | Max420,list260,pad16,+N30 | Hover/focus/scroll/Escape | Реализован специализированный EventHistoryPopover внутри ProblemHistory.tsx: portal, clamp/scroll/resize, hover и keyboard, Escape; не универсальный Popover |
| StepIndicator | 46;22/30/600;stroke2 | Active/completed/future, по реальным данным | Реализован AssemblyStepIndicator внутри ActiveAssembly.tsx; вычисляется из существующих step/shipmentConfirmed, без нового business-state |
| WaitMetric | 18/24,min28,pad2×6 | Existinggreen/yellow/orange/red | Реализована .assemblyQueue__wait:18/24,h28,pad2×6,r8,gap8; существующие расчёт, четыре цвета и пороги сохранены |

Точная палитра/геометрия/тайминги каждого состояния в DESIGN_SYSTEM_APPROVED.md и TOKENS.v1.json. Новые компоненты `Modal`, `Search`, `Select` не создавать параллельно готовым предметным диалогам только ради названия. Сначала извлечь визуальный контракт; повторное извлечение поведения — отдельный минимальный diff с тестами.

## Сохранение функциональности

- Routing/useTheme/theme storage/PageIntro не меняются.
- Scanner: входной код, нормализация, Enter, autoFocus, restoreScanFocus, blocked/saving, guards, подтверждение сервером, undo, восстановление draft не переносятся в CSS/анимацию.
- Tables: selection, expansion, sort direction, фильтры, query, refresh, текущая пагинация и число реальных записей сохраняются.
- History: groupProblemHistory не редактируется; key включает kind и operationId, не только title/message/outcome. Все даты/повторы/ID остаются доступны по существующему режиму.
- Dialog: валидация, обязательность полей, maxLength/rows/min/max/step, autofocusing и операции не меняются; запрет Escape и возврат фокуса реализованы в I8, а не добавлены как новая бизнес-логика.
- Никаких новых «Готово», дополнительных блоков, повсеместных helper-text, повторных уведомлений или скрытия реальных данных ради компактности.



## Проверка механизма темы — D0,24.10.2026

[useTheme.ts](../../frontend/src/hooks/useTheme.ts) записывает тему в useEffect через `document.documentElement.dataset.theme = theme`: фактический атрибут — `data-theme="dark"` или `data-theme="light"` у элемента `html`. Начальное значение читается из существующего ключа localStorage `tremor-theme`, при недоступном storage используется текущий fallback. Поэтому `html[data-theme="dark"] .ds-v1` соответствует работающему механизму; менять useTheme или хранение настройки не требуется.

Это проверка исходного кода, а не изменение пользовательской темы. На момент D0 marker, переменные и классы ещё не подключались. После I1–I12.7 они реализованы на перечисленных рабочих компонентах первой волны. Для native dialog/portal по-прежнему необходим собственный opt-in marker; перенос в top layer сам по себе не меняет DOM-наследование.

## Завершение точечных исправлений I12

I12.2: переносы вложенных таблиц и защита двух колонок. I12.3: адресные типографические роли; I12.3.1–I12.3.2: единый Page Title и выравнивание подписи. I12.4: оставшиеся семейства кнопок и состояния. I12.5: Input/Search Каталога, Проблемных и Архива. I12.6: заключительный аудит. I12.7: Table v1 для отсканированных товаров, Secondary-пагинация Проблемных, success Feedback подтверждённой коробки и hover/disabled/resize:none у многострочной textarea. Высота textarea остаётся по rows/форме, не48px.
