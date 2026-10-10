import { useState } from 'react'
import type { AssemblyPreviewOrder, AssemblyProblemSnapshot } from './ActiveAssembly'
import type { SavedOrderProblem } from './OrderProblemDialog'
import { problemTypes } from './problemTypes'
import './ProblemOrders.css'

type ProblemOrder = { order: AssemblyPreviewOrder; problem: SavedOrderProblem; status: 'problem'; assembly?: AssemblyProblemSnapshot }
const date = (value: string) => new Date(value).toLocaleString('ru-RU', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })

export default function ProblemOrders({ orders }: { orders: ProblemOrder[] }) {
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState('')
  const [notice, setNotice] = useState('')
  const search = query.trim().toLocaleLowerCase('ru-RU')
  const visible = orders.filter(({ order, problem }) => (!filter || problem.type === filter) && (!search || [order.number, ...order.items.flatMap((item) => [item.name, item.sku])].some((value) => value.toLocaleLowerCase('ru-RU').includes(search))))

  return <section className="receivingPage__panel problemOrders" aria-labelledby="problem-orders-title">
    <div className="problemOrders__header">
      <div><h2 className="receivingPage__panelTitle" id="problem-orders-title">ПРОБЛЕМНЫЕ ЗАКАЗЫ <span className="problemOrders__count">{orders.length}</span></h2><p className="assemblyQueue__subtitle">Разберите причину и продолжите работу с заказом</p></div>
      <div className="problemOrders__tools">
        <label className="problemOrders__search"><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true"><circle cx="10" cy="10" r="6"/><path d="m15 15 6 6"/></svg><input type="search" aria-label="Поиск проблемного заказа, SKU или товара" placeholder="Заказ, SKU или товар" value={query} onChange={(event) => setQuery(event.target.value)}/></label>
        <select aria-label="Фильтр типа проблемы" value={filter} onChange={(event) => setFilter(event.target.value)}><option value="">Все причины</option>{problemTypes.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
      </div>
    </div>
    <div className="problemOrders__columns" aria-hidden="true"><span>Заказ</span><span>Причина</span><span>Затронутый товар</span><span>Решение</span><span>Время</span><span/></div>
    <div className="problemOrders__list">
      {visible.map(({ order, problem, assembly }) => {
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
        const actions = [
          ...(['insufficient_stock', 'missing_in_cell', 'damaged_product'].includes(problem.type) ? ['Обновить остатки'] : []),
          ...(problem.type === 'unknown_sku' ? ['Сопоставить SKU'] : []),
          ...(problem.type === 'invalid_order' || assembly?.shipmentConfirmed ? ['Повторить подтверждение Ozon'] : []),
          'Вернуть в сборку', 'Передать в разбор',
        ]
        return <details className="problemOrders__order" key={order.number}>
          <summary className="problemOrders__row">
            <span><strong>{order.number}</strong><small>{assembly ? 'Из активной сборки' : 'Из очереди заказов'}</small></span>
            <span className="problemOrders__reason">{reason}</span>
            <span>{affected.length ? <><strong>{affected[0].name}</strong><small>{affected[0].sku}{affected.length > 1 && ` · ещё ${affected.length - 1}`}</small></> : <><strong>Заказ целиком</strong><small>{order.items.length} поз. · {required} шт.</small></>}</span>
            <span><span className="problemOrders__status">Ожидает решения</span></span>
            <time dateTime={problem.createdAt}>{date(problem.createdAt)}</time><span className="problemOrders__chevron" aria-hidden="true">⌄</span>
          </summary>
          <div className="problemOrders__details">
            <div className="problemOrders__picked">
              <div className="problemOrders__sectionTitle"><h3>Сохранённый прогресс</h3><span>{picks.length} / {required} шт.</span></div>
              <progress value={picks.length} max={required || 1} aria-label="Сохранённый прогресс сборки"/>
              <p className="problemOrders__muted">{assembly ? (assembly.shipmentConfirmed ? 'Отправление подтверждено' : assembly.cell ? `Подтверждена ячейка ${assembly.cell}` : 'Ожидается сканирование ячейки или отправления') : 'Сборка ещё не начата'}</p>
              {groups.length ? <table><thead><tr><th>Уже собрано</th><th>Исходная ячейка</th><th>Шт.</th></tr></thead><tbody>{groups.map((item) => <tr key={`${item.sku}-${item.cell}`}><td><strong>{order.items.find((product) => product.sku === item.sku)?.name ?? item.sku}</strong><small>{item.sku}</small></td><td><span className="problemOrders__cell">{item.cell}</span></td><td>{item.quantity}</td></tr>)}</tbody></table> : <p className="problemOrders__muted">Отобранных товаров нет</p>}
            </div>
            <div className="problemOrders__context">
              <h3>Проблема и комментарий</h3>
              {problem.shortages.map((item) => <p className="problemOrders__shortage" key={item.sku}>{item.name}: нужно {item.required}, доступно {item.available}, не хватает {item.missing} шт.</p>)}
              <p className="problemOrders__comment">{problem.comment || 'Комментарий не добавлен'}</p>
              <h3>История проблемы</h3>
              <ol className="problemOrders__history"><li><time dateTime={problem.createdAt}>{date(problem.createdAt)}</time><span>Заказ передан из {assembly ? 'сборки' : 'очереди'}.<br/>{reason}{problem.positionSku && ` · ${problem.positionSku}`}</span></li></ol>
            </div>
            <div className="problemOrders__actions">{actions.map((action) => <button type="button" key={action} className={`receivingHistory__button${action === 'Вернуть в сборку' ? ' problemOrders__resume' : ''}`} onClick={() => setNotice(`${order.number}: «${action}» — действие будет подключено на этапе функционала.`)}>{action}</button>)}</div>
          </div>
        </details>
      })}
      {!visible.length && <div className="problemOrders__empty"><strong>{orders.length ? 'Ничего не найдено' : 'Проблемных заказов пока нет'}</strong><p>{orders.length ? 'Измените поисковый запрос или тип проблемы.' : 'Здесь появятся заказы, отправленные из очереди или активной сборки.'}</p>{orders.length > 0 && <button className="receivingHistory__button" type="button" onClick={() => { setQuery(''); setFilter('') }}>Сбросить фильтры</button>}</div>}
    </div>
    {notice && <p className="problemOrders__notice" role="status">{notice}</p>}
  </section>
}
