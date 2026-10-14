import { useState } from 'react'
import SkuMappingDialog from './SkuMappingDialog'
import { Link } from 'react-router'
import type { AssemblyPreviewOrder, AssemblyProblemSnapshot } from './ActiveAssembly'
import type { SavedOrderProblem } from './OrderProblemDialog'
import { allProblemTypes } from './problemTypes'
import ProblemHistory, { ProblemTime } from './ProblemHistory'
import createProblemHistory from './createProblemHistory'
import arrowDown from '../../assets/home/arrow-down.svg'
import arrowUp from '../../assets/home/arrow-up.svg'
import './ProblemOrders.css'

type ProblemOrder = { problemId?: string; order: AssemblyPreviewOrder; problem: SavedOrderProblem; status: 'problem'; assembly?: AssemblyProblemSnapshot }

export default function ProblemOrders({ orders, blocked, assemblyBusy, onReturn, onStockCheck, onMap, onOzonAction, onTransfer, onArchive }: { orders: ProblemOrder[]; blocked: boolean; assemblyBusy: boolean; onReturn: (number: string, problemId?: string) => Promise<boolean>; onStockCheck: (number: string, problemId?: string) => Promise<string>; onMap?: (problemId: string, positionSku: string, productId: number) => Promise<boolean>; onOzonAction?: (problemId: string, action: 'retry-ozon' | 'close-cancelled') => Promise<string>; onTransfer?: (problemId: string) => Promise<string>; onArchive?: () => void }) {
  const [mapping, setMapping] = useState<ProblemOrder | null>(null)
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState('')
  const [notice, setNotice] = useState('')
  const [page, setPage] = useState(1)
  const search = query.trim().toLocaleLowerCase('ru-RU')
  const pendingOrders = orders.filter(({ problem }) => problem.resolutionStatus !== 'returned' && !problem.closedOutcome)
  const visible = pendingOrders.filter(({ order, problem }) => (!filter || problem.type === filter) && (!search || [order.number, ...order.items.flatMap((item) => [item.name, item.sku])].some((value) => value.toLocaleLowerCase('ru-RU').includes(search))))
  const pageSize = 5
  const pageCount = Math.max(1, Math.ceil(visible.length / pageSize))
  const currentPage = Math.min(page, pageCount)
  const pageOrders = visible.slice((currentPage - 1) * pageSize, currentPage * pageSize)

  return <section className="receivingPage__panel problemOrders" aria-labelledby="problem-orders-title">
    <div className="problemOrders__header">
      <div><div className="problemOrders__title"><h2 className="receivingPage__panelTitle" id="problem-orders-title">ПРОБЛЕМНЫЕ ЗАКАЗЫ</h2><span className="problemOrders__count">{pendingOrders.length}</span></div><p className="assemblyQueue__subtitle">Разберите причину и продолжите работу с заказом</p></div>
      <div className="problemOrders__tools">
        {onArchive && <button type="button" className="receivingHistory__button" onClick={onArchive}>Архив закрытых</button>}
        <label className="problemOrders__search"><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true"><circle cx="10" cy="10" r="6"/><path d="m15 15 6 6"/></svg><input type="search" aria-label="Поиск проблемного заказа, SKU или товара" placeholder="Заказ, SKU или товар" value={query} onChange={(event) => { setQuery(event.target.value); setPage(1) }}/></label>
        <select aria-label="Фильтр типа проблемы" value={filter} onChange={(event) => { setFilter(event.target.value); setPage(1) }}><option value="">Все причины</option>{allProblemTypes.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
      </div>
    </div>
    <div className="problemOrders__columns" aria-hidden="true"><span>Заказ / статус</span><span>Причина</span><span>Затронутый товар</span><span>Решение</span><span>Время</span><span/></div>
    <div className="problemOrders__list" key={`${currentPage}:${query}:${filter}`}>
      {pageOrders.map(({ problemId, order, problem, assembly }) => {
        const affected = order.items.filter((item) => item.sku === problem.positionSku || problem.shortages.some((shortage) => shortage.sku === item.sku) || problem.creationContext?.affectedPositions.some((position) => position.sku === item.sku))
        const problemPositions = problem.shortages.length ? problem.shortages : problem.resolutionStatus !== 'resolved' && problem.creationContext?.complete ? problem.creationContext.positions.filter((position) => affected.some((item) => item.sku === position.sku)).map((position) => ({ sku: position.sku, name: position.name ?? position.sku, required: position.remaining ?? 0, available: position.available ?? 0, missing: position.missing ?? 0 })) : []
        const problemQuantity = affected.reduce((sum, item) => {
          const shortage = problem.shortages.find((position) => position.sku === item.sku)
          const picked = assembly?.picks.filter((pick) => pick.sku === item.sku).length ?? 0
          return sum + (shortage ? shortage.missing : Math.max(0, item.quantity - picked))
        }, 0)
        const picks = assembly?.picks ?? []
        const needsDisassembly = picks.length > 0 && (problem.requiresDisassembly ?? problem.type === 'cancelled_order')
        const history = problem.history?.some((event) => event.kind === 'creation') ? problem.history : [...createProblemHistory(order, problem, problem.createdAt, assembly), ...(problem.history ?? [])]
        const required = order.items.reduce((sum, item) => sum + item.quantity, 0)
        const groups = picks.reduce<Array<{ sku: string; quantity: number; cells: Map<string, number> }>>((result, pick) => {
          const group = result.find((item) => item.sku === pick.sku)
          if (group) group.quantity += 1
          else result.push({ sku: pick.sku, quantity: 1, cells: new Map() })
          const position = result.find((item) => item.sku === pick.sku)!
          const cellLabel = pick.cellId ? `${pick.cell} [ID ${pick.cellId}]` : pick.cell
          position.cells.set(cellLabel, (position.cells.get(cellLabel) ?? 0) + 1)
          return result
        }, [])
        const reason = allProblemTypes.find(([type]) => type === problem.type)?.[1] ?? 'Другая проблема'
        const resolved = problem.resolutionStatus === 'resolved'
        const orderProblemsResolved = order.source === 'ozon' && pendingOrders.filter((item) => item.order.number === order.number && item.order.source === order.source).every((item) => item.problem.resolutionStatus === 'resolved' && !item.problem.requiresDisassembly && item.problem.type !== 'cancelled_order' && (item.problem.type !== 'ozon_error' || (item.problem.ozonPurpose === 'refresh_order' && item.problem.ozonOutcome === 'applied')))
        const returnHint = order.source !== 'ozon' ? 'Исторический пример: реальные складские списания отключены' : !orderProblemsResolved ? 'Сначала устраните все проблемы заказа' : assemblyBusy ? 'Сначала завершите текущую сборку' : blocked ? 'Дождитесь сохранения и восстановления связи' : 'Продолжить сборку с сохранённым прогрессом'
        const position = order.items.find((item) => item.sku === problem.positionSku)
        const product = position ? `${position.name} (${position.sku})` : 'товар заказа'
        const explanation = {
          insufficient_stock: {
            happened: problem.shortages.length ? problem.shortages.map((item) => `Для ${item.name} требуется ${item.required} шт., на складе доступно ${item.available} шт.`).join(' ') : `Для ${product} указан недостаточный остаток. Проверьте доступное количество.`,
            todo: problem.shortages.length ? `Перейдите в «Приёмку» и добавьте недостающее количество: ${problem.shortages.map((item) => `${item.name} — минимум ${item.missing} шт.`).join('; ')} После этого нажмите «Обновить остатки».` : 'Проверьте недостающее количество, добавьте товар через «Приёмку», затем нажмите «Обновить остатки».',
          },
          missing_in_cell: { happened: `Отмечено отсутствие ${product} в ячейке при наличии по данным склада.`, todo: 'Проверьте указанную ячейку и фактическое наличие товара. Уточните размещение и остатки перед продолжением сборки.' },
          unknown_sku: { happened: `Код ${position?.sku ?? 'товара заказа'} не сопоставлен с товаром склада.`, todo: 'Сопоставьте SKU с правильным товаром склада и проверьте состав заказа.' },
          damaged_product: { happened: `Для ${product} отмечено повреждение: товар нельзя использовать для сборки.`, todo: 'Отделите повреждённый товар, проверьте количество пригодных единиц и обеспечьте необходимый остаток.' },
          invalid_order: { happened: 'В составе, количестве или других данных заказа отмечена ошибка.', todo: 'Сверьте заказ с данными Ozon и устраните расхождения перед продолжением сборки.' },
          other: { happened: problem.comment || 'Заказ остановлен по причине, указанной как «Другая проблема».', todo: 'Уточните причину по комментарию и проверьте заказ. После устранения причины можно продолжить сборку.' },
          cancelled_order: { happened: 'Заказ отменён. Продолжать сборку этого заказа нельзя.', todo: needsDisassembly ? 'Передайте уже отобранные товары в разбор для возврата в исходные ячейки.' : picks.length > 0 ? 'Для этого заказа физический разбор не требуется.' : 'Отобранных товаров нет. Физический разбор не требуется.' },
          ozon_error: { happened: problem.ozonOutcome === 'applied' ? 'Исходная операция Ozon подтверждена. Остальные причины проверяются отдельно.' : 'Фактическое подтверждение Ozon не получено или ожидает сверки.', todo: 'Повтор сначала проверяет результат исходной операции. При неизвестном результате повторная отправка запрещена.' },
        }[problem.type]
        const actions: string[] = {
          insufficient_stock: ['Обновить остатки', 'Вернуть в сборку'],
          unknown_sku: resolved ? ['Вернуть в сборку'] : ['Сопоставить товар'],
          cancelled_order: !picks.length && problem.ozonOperationId ? ['Закрыть отменённый заказ'] : [],
          ozon_error: resolved && problem.ozonPurpose === 'refresh_order' ? ['Вернуть в сборку'] : ['Повторить запрос Ozon'],
          missing_in_cell: ['Вернуть в сборку'],
          damaged_product: ['Вернуть в сборку'],
          invalid_order: ['Вернуть в сборку'],
          other: ['Вернуть в сборку'],
        }[problem.type]
        if (needsDisassembly) actions.push(problem.disassemblySessionId ? 'Передан в разбор' : 'Передать в разбор')
        return <details className="problemOrders__order" key={problemId ?? `${order.number}:${problem.type}:${problem.createdAt}`}>
          <summary className="problemOrders__row">
            <span><strong className="problemOrders__number">{order.number}</strong><small>{order.source !== 'ozon' && 'Исторический пример · '}{assembly ? 'Из активной сборки' : 'Из очереди заказов'}</small></span>
            <span className="problemOrders__reason">{reason}</span>
            <span>{affected.length > 1 ? <><strong>{affected.length} {({ one: 'позиция', few: 'позиции', many: 'позиций', other: 'позиции', zero: 'позиций', two: 'позиции' })[new Intl.PluralRules('ru').select(affected.length)]}</strong><small>{problemQuantity} шт.</small></> : affected.length === 1 ? <><strong>{affected[0].name}</strong><small>{affected[0].sku}</small></> : <><strong>Заказ целиком</strong><small>{order.items.length} поз. · {required} шт.</small></>}</span>
            <span><span className={`problemOrders__status${resolved ? ' problemOrders__status--resolved' : ''}`}>{resolved ? 'Решена' : 'Ожидает решения'}</span></span>
            <ProblemTime value={problem.createdAt}/><span className="problemOrders__chevron" aria-hidden="true"><img className="problemOrders__arrowDown" src={arrowDown} alt=""/><img className="problemOrders__arrowUp" src={arrowUp} alt=""/></span>
          </summary>
          <div className="problemOrders__details">
            <div className="problemOrders__picked">
              <div className="problemOrders__sectionTitle"><h3>Сохранённый прогресс</h3><span>{picks.length} / {required} шт.</span></div>
              <progress value={picks.length} max={required || 1} aria-label="Сохранённый прогресс сборки"/>
              {groups.length ? <table className="problemOrders__scanned"><thead><tr><th>Товар</th><th>Отсканировано / Нужно</th></tr></thead><tbody>{groups.map((item) => {
                const product = order.items.find((position) => position.sku === item.sku)
                return <tr key={item.sku}><td><strong>{product?.name ?? item.sku}</strong><small>{item.sku}</small><small>Из ячеек: {[...item.cells].map(([cell, quantity]) => `${cell} ×${quantity}`).join(' · ')}</small></td><td>{item.quantity} / {product?.quantity ?? '—'}</td></tr>
              })}</tbody></table> : <p className="problemOrders__muted">Сборка ещё не начата</p>}
              <h3 className="problemOrders__problemTitle">Проблема и комментарий</h3>
              {problemPositions.length > 0 && <div className="problemOrders__tableScroll"><table className="problemOrders__shortages"><thead><tr><th>Товар</th><th>Нужно</th><th>В наличии</th><th>Не хватает</th></tr></thead><tbody>{problemPositions.map((item) => <tr key={item.sku}><td><strong>{item.name}</strong><small>{item.sku}</small></td><td>{item.required} шт.</td><td>{item.available} шт.</td><td className={item.missing > 0 ? 'problemOrders__missing' : undefined}>{item.missing} шт.</td></tr>)}</tbody></table></div>}
              <p className="problemOrders__comment">{problem.comment || 'Комментарий не добавлен'}</p>
            </div>
            <div className="problemOrders__context">
              <div className="problemOrders__guidance">
                <h3>Что произошло</h3>
                <p>{explanation.happened}</p>
                <h3>Что делать</h3>
                <p>{explanation.todo}</p>
                {problem.type === 'insufficient_stock' && <Link className="receivingHistory__button problemOrders__receivingLink" to="/receiving#receiving-scan">В Приёмку</Link>}
              </div>
              <h3>История проблемы</h3>
              <ProblemHistory events={history} />
            </div>
            <div className="problemOrders__actions">{actions.map((action) => action === 'Перейти в приёмку' ? <Link key={action} className="receivingHistory__button problemOrders__receivingLink" to="/receiving#receiving-scan">{action}</Link> : <button type="button" key={action} className={`receivingHistory__button${action === 'Вернуть в сборку' ? ' problemOrders__resume' : ''}`} disabled={blocked || (!!problem.disassemblySessionId && ['Обновить остатки', 'Вернуть в сборку', 'Сопоставить товар', 'Закрыть отменённый заказ'].includes(action)) || action === 'Передан в разбор' || (action === 'Передать в разбор' && (!onTransfer || !problemId || order.source !== 'ozon')) || (['Повторить запрос Ozon', 'Закрыть отменённый заказ'].includes(action) && (!onOzonAction || !problemId || !problem.ozonOperationId || order.source !== 'ozon')) || (action === 'Сопоставить товар' && (order.source !== 'ozon' || !problemId || !onMap)) || (action === 'Вернуть в сборку' && (!orderProblemsResolved || assemblyBusy))} title={action === 'Вернуть в сборку' ? returnHint : undefined} onClick={() => {
              if (action === 'Вернуть в сборку') void onReturn(order.number, problemId).then((saved) => { if (saved) setNotice(`${order.number}: заказ возвращён в сборку.`) })
              else if (action === 'Обновить остатки' && problem.type === 'insufficient_stock') { setNotice(`${order.number}: проверка остатков…`); void onStockCheck(order.number, problemId).then(setNotice).catch(() => setNotice('Не удалось выполнить проверку остатков. Повторите попытку.')) }
              else if (['Повторить запрос Ozon', 'Закрыть отменённый заказ'].includes(action) && problemId && onOzonAction) { setNotice(`${order.number}: проверка исходной операции…`); void onOzonAction(problemId, action === 'Повторить запрос Ozon' ? 'retry-ozon' : 'close-cancelled').then(setNotice).catch(() => setNotice('Не удалось проверить операцию. Повторите тот же запрос.')) }
              else if (action === 'Передать в разбор' && problemId && onTransfer) void onTransfer(problemId).then(setNotice)
              else if (action === 'Сопоставить товар') setMapping({ problemId, order, problem, status: 'problem', assembly })
              else setNotice(`${order.number}: «${action}» — действие будет подключено на этапе функционала.`)
            }}>{action}</button>)}</div>
          </div>
        </details>
      })}
      {pageOrders.length > 0 && pageOrders.length < pageSize && <div aria-hidden="true" style={{ minHeight: `calc(${pageSize - pageOrders.length} * var(--problem-row-height))` }} />}
      {!visible.length && <div className="problemOrders__empty"><strong>{pendingOrders.length ? 'Ничего не найдено' : 'Проблемных заказов пока нет'}</strong><p>{pendingOrders.length ? 'Измените поисковый запрос или тип проблемы.' : 'Здесь появятся заказы, отправленные из очереди или активной сборки.'}</p>{pendingOrders.length > 0 && <button className="receivingHistory__button" type="button" onClick={() => { setQuery(''); setFilter(''); setPage(1) }}>Сбросить фильтры</button>}</div>}
    </div>
    <nav className="receivingHistory__pagination" aria-label="Страницы проблемных заказов">
      <button type="button" className="receivingHistory__button" disabled={currentPage === 1} onClick={() => { setPage(currentPage - 1); setNotice('') }}>Предыдущая</button>
      <span aria-live="polite">Страница {currentPage} из {pageCount}</span>
      <button type="button" className="receivingHistory__button" disabled={currentPage === pageCount} onClick={() => { setPage(currentPage + 1); setNotice('') }}>Следующая</button>
    </nav>
    {mapping?.problemId && onMap && <SkuMappingDialog order={mapping.order} problem={mapping.problem} blocked={blocked} onCancel={() => setMapping(null)} onConfirm={async (sku, productId) => {
      const saved = await onMap(mapping.problemId!, sku, productId)
      if (saved) setMapping(null)
      return saved
    }} />}
    {notice && <p className="problemOrders__notice" role="status">{notice}</p>}
  </section>
}
