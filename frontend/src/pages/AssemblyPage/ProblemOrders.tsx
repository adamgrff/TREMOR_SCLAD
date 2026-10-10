import { useState } from 'react'
import { Link } from 'react-router'
import type { AssemblyPreviewOrder, AssemblyProblemSnapshot } from './ActiveAssembly'
import type { SavedOrderProblem } from './OrderProblemDialog'
import { problemTypes } from './problemTypes'
import arrowDown from '../../assets/home/arrow-down.svg'
import arrowUp from '../../assets/home/arrow-up.svg'
import './ProblemOrders.css'

type ProblemOrder = { order: AssemblyPreviewOrder; problem: SavedOrderProblem; status: 'problem'; assembly?: AssemblyProblemSnapshot }
function ProblemTime({ value }: { value: string }) {
  const timestamp = new Date(value)
  return <time className="problemOrders__time" dateTime={value}><span className="assemblyQueue__time">{timestamp.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })}</span><span className="assemblyQueue__date">{timestamp.toLocaleDateString('ru-RU')}</span></time>
}

export default function ProblemOrders({ orders, blocked, assemblyBusy, onReturn }: { orders: ProblemOrder[]; blocked: boolean; assemblyBusy: boolean; onReturn: (number: string) => Promise<boolean> }) {
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState('')
  const [notice, setNotice] = useState('')
  const [page, setPage] = useState(1)
  const search = query.trim().toLocaleLowerCase('ru-RU')
  const pendingOrders = orders.filter(({ problem }) => problem.resolutionStatus !== 'returned')
  const visible = pendingOrders.filter(({ order, problem }) => (!filter || problem.type === filter) && (!search || [order.number, ...order.items.flatMap((item) => [item.name, item.sku])].some((value) => value.toLocaleLowerCase('ru-RU').includes(search))))
  const pageSize = 5
  const pageCount = Math.max(1, Math.ceil(visible.length / pageSize))
  const currentPage = Math.min(page, pageCount)
  const pageOrders = visible.slice((currentPage - 1) * pageSize, currentPage * pageSize)

  return <section className="receivingPage__panel problemOrders" aria-labelledby="problem-orders-title">
    <div className="problemOrders__header">
      <div><div className="problemOrders__title"><h2 className="receivingPage__panelTitle" id="problem-orders-title">ПРОБЛЕМНЫЕ ЗАКАЗЫ</h2><span className="problemOrders__count">{pendingOrders.length}</span></div><p className="assemblyQueue__subtitle">Разберите причину и продолжите работу с заказом</p></div>
      <div className="problemOrders__tools">
        <label className="problemOrders__search"><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true"><circle cx="10" cy="10" r="6"/><path d="m15 15 6 6"/></svg><input type="search" aria-label="Поиск проблемного заказа, SKU или товара" placeholder="Заказ, SKU или товар" value={query} onChange={(event) => { setQuery(event.target.value); setPage(1) }}/></label>
        <select aria-label="Фильтр типа проблемы" value={filter} onChange={(event) => { setFilter(event.target.value); setPage(1) }}><option value="">Все причины</option>{problemTypes.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
      </div>
    </div>
    <div className="problemOrders__columns" aria-hidden="true"><span>Заказ / статус</span><span>Причина</span><span>Затронутый товар</span><span>Решение</span><span>Время</span><span/></div>
    <div className="problemOrders__list" key={`${currentPage}:${query}:${filter}`}>
      {pageOrders.map(({ order, problem, assembly }) => {
        const affected = order.items.filter((item) => item.sku === problem.positionSku || problem.shortages.some((shortage) => shortage.sku === item.sku))
        const picks = assembly?.picks ?? []
        const required = order.items.reduce((sum, item) => sum + item.quantity, 0)
        const groups = picks.reduce<Array<{ sku: string; cell: string; quantity: number }>>((result, pick) => {
          const group = result.find((item) => item.sku === pick.sku && item.cell === pick.cell)
          if (group) group.quantity += 1
          else result.push({ ...pick, quantity: 1 })
          return result
        }, [])
        const reason = problemTypes.find(([type]) => type === problem.type)?.[1] ?? 'Другая проблема'
        const resolved = problem.resolutionStatus === 'resolved'
        const returnHint = !resolved ? 'Сначала устраните причину проблемы' : assemblyBusy ? 'Сначала завершите текущую сборку' : blocked ? 'Дождитесь сохранения и восстановления связи' : 'Продолжить сборку с сохранённым прогрессом'
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
        }[problem.type]
        const actions = [
          ...(['insufficient_stock', 'missing_in_cell', 'damaged_product'].includes(problem.type) ? ['Обновить остатки'] : []),
          ...(problem.type === 'unknown_sku' ? ['Сопоставить SKU'] : []),
          ...(problem.type === 'invalid_order' || assembly?.shipmentConfirmed ? ['Повторить подтверждение Ozon'] : []),
          'Вернуть в сборку', 'Передать в разбор',
        ]
        return <details className="problemOrders__order" key={order.number}>
          <summary className="problemOrders__row">
            <span><strong className="problemOrders__number">{order.number}</strong><small>{assembly ? 'Из активной сборки' : 'Из очереди заказов'}</small></span>
            <span className="problemOrders__reason">{reason}</span>
            <span>{affected.length ? <><strong>{affected[0].name}</strong><small>{affected[0].sku}{affected.length > 1 && ` · ещё ${affected.length - 1}`}</small></> : <><strong>Заказ целиком</strong><small>{order.items.length} поз. · {required} шт.</small></>}</span>
            <span><span className={`problemOrders__status${resolved ? ' problemOrders__status--resolved' : ''}`}>{resolved ? 'Решена' : 'Ожидает решения'}</span></span>
            <ProblemTime value={problem.createdAt}/><span className="problemOrders__chevron" aria-hidden="true"><img className="problemOrders__arrowDown" src={arrowDown} alt=""/><img className="problemOrders__arrowUp" src={arrowUp} alt=""/></span>
          </summary>
          <div className="problemOrders__details">
            <div className="problemOrders__picked">
              <div className="problemOrders__sectionTitle"><h3>Сохранённый прогресс</h3><span>{picks.length} / {required} шт.</span></div>
              <progress value={picks.length} max={required || 1} aria-label="Сохранённый прогресс сборки"/>
              <p className="problemOrders__muted">{assembly ? (assembly.shipmentConfirmed ? 'Отправление подтверждено' : assembly.cell ? `Подтверждена ячейка ${assembly.cell}` : 'Ожидается сканирование ячейки или отправления') : 'Сборка ещё не начата'}</p>
              {groups.length ? <table><thead><tr><th>Уже собрано</th><th>Исходная ячейка</th><th>Шт.</th></tr></thead><tbody>{groups.map((item) => <tr key={`${item.sku}-${item.cell}`}><td><strong>{order.items.find((product) => product.sku === item.sku)?.name ?? item.sku}</strong><small>{item.sku}</small></td><td><span className="problemOrders__cell">{item.cell}</span></td><td>{item.quantity}</td></tr>)}</tbody></table> : <p className="problemOrders__muted">Отобранных товаров нет</p>}
              <h3 className="problemOrders__problemTitle">Проблема и комментарий</h3>
              {problem.shortages.length > 0 && <div className="problemOrders__tableScroll"><table className="problemOrders__shortages"><thead><tr><th>Товар</th><th>Нужно</th><th>В наличии</th><th>Не хватает</th></tr></thead><tbody>{problem.shortages.map((item) => <tr key={item.sku}><td><strong>{item.name}</strong><small>{item.sku}</small></td><td>{item.required} шт.</td><td>{item.available} шт.</td><td className="problemOrders__missing">{item.missing} шт.</td></tr>)}</tbody></table></div>}
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
              <ol className="problemOrders__history"><li><ProblemTime value={problem.createdAt}/><span>Заказ передан из {assembly ? 'сборки' : 'очереди'}.<br/>{reason}{problem.positionSku && ` · ${problem.positionSku}`}</span></li></ol>
            </div>
            <div className="problemOrders__actions">{actions.map((action) => <button type="button" key={action} className={`receivingHistory__button${action === 'Вернуть в сборку' ? ' problemOrders__resume' : ''}`} disabled={action === 'Вернуть в сборку' && (!resolved || assemblyBusy || blocked)} title={action === 'Вернуть в сборку' ? returnHint : undefined} onClick={() => {
              if (action === 'Вернуть в сборку') void onReturn(order.number).then((saved) => { if (saved) setNotice(`${order.number}: заказ возвращён в сборку.`) })
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
    {notice && <p className="problemOrders__notice" role="status">{notice}</p>}
  </section>
}
