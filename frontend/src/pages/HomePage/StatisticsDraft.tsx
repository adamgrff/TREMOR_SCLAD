import { useEffect, useRef, useState, type CSSProperties } from 'react'
import './StatisticsDraft.css'
import { formatAssemblyDuration, formatStatistic, statisticsPeriodValues } from './homeStatisticsData'
import { useHomeStatisticsData } from './useHomeStatisticsData'
import { useHomeHistory } from './useHomeHistory'
import { formatHistoryReference, formatHistoryTime, historyProcessLabels, sortHomeHistory } from './homeHistoryData'

const periods = ['Сегодня', '7 дней', '30 дней'] as const

export function CopyBadge({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const attempt = useRef(0)
  useEffect(() => () => {
    attempt.current++
    if (timer.current !== null) clearTimeout(timer.current)
  }, [])

  async function copy() {
    const current = ++attempt.current
    try {
      await navigator.clipboard.writeText(value)
      if (current !== attempt.current) return
      if (timer.current !== null) clearTimeout(timer.current)
      setFailed(false)
      setCopied(true)
      timer.current = setTimeout(() => { setCopied(false); timer.current = null }, 1400)
    } catch {
      if (current !== attempt.current) return
      setFailed(true)
      setCopied(false)
    }
  }

  return <button type="button" className="statisticsDraft__reference" data-copied={copied} onClick={() => void copy()}
    title={failed ? 'Не удалось скопировать. Повторите попытку' : `Скопировать ${value}`}
    aria-label={copied ? `${value}: скопировано` : failed ? `${value}: не удалось скопировать, повторить` : `Скопировать ${value}`}>
    <span className="statisticsDraft__copyMark" aria-hidden="true"><svg viewBox="0 0 18 18" fill="none"><path d="m4 9 3 3 7-7" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" /></svg></span>
    <span>{value}</span>
  </button>
}

function Metrics({ items }: { items: { label: string; value: string }[] }) {
  return <dl className="statisticsDraft__metrics">
    {items.map(item => <div key={item.label}><dt>{item.label}</dt><dd>{item.value}</dd></div>)}
  </dl>
}

export default function StatisticsDraft() {
  const [period, setPeriod] = useState<(typeof periods)[number]>('Сегодня')
  const [timeOrder, setTimeOrder] = useState<'asc' | 'desc'>('desc')
  const data = useHomeStatisticsData(statisticsPeriodValues[period])
  const lowStockItems = data?.stock.lowStockItems
  const history = useHomeHistory()
  const sortedActions = sortHomeHistory(history.events, timeOrder)

  return <div className="statisticsDraft">
    <header className="statisticsDraft__header">
      <div>
        <h2 id="home-statistics-title" className="homeStatistics__title ds-v1 ui-v1-page-title">СТАТИСТИКА / ИСТОРИЯ</h2>
      </div>
      <div className="statisticsDraft__period" role="group" aria-label="Период статистики" style={{ '--period-index': periods.indexOf(period) } as CSSProperties}>
        <span className="statisticsDraft__periodGlass" aria-hidden="true" />
        {periods.map(item => <button key={item} type="button" aria-pressed={period === item} onClick={() => setPeriod(item)}>{item.toLocaleUpperCase('ru-RU')}</button>)}
      </div>
    </header>

    <div className="statisticsDraft__columns">
      <section className="statisticsDraft__section" aria-labelledby="statistics-receiving">
        <h3 id="statistics-receiving" className="ds-v1 ui-v1-type-section">ПРИЁМКА</h3>
        <p className="statisticsDraft__context">За выбранный период</p>
        <Metrics items={[
          { label: 'Приёмок', value: formatStatistic(data?.receiving.sessions) },
          { label: 'Принято', value: formatStatistic(data?.receiving.receivedQuantity, 'ед.') },
          { label: 'Размещено', value: formatStatistic(data?.receiving.placedQuantity, 'ед.') },
          { label: 'Ожидает размещения сейчас', value: formatStatistic(data?.receiving.pendingQuantity, 'ед.') },
        ]} />
      </section>
      <section className="statisticsDraft__section" aria-labelledby="statistics-stock">
        <h3 id="statistics-stock" className="ds-v1 ui-v1-type-section">ЯЧЕЙКИ</h3>
        <p className="statisticsDraft__context">Текущее состояние · не зависит от периода</p>
        <Metrics items={[
          { label: 'Товара на складе', value: formatStatistic(data?.stock.totalQuantity, 'ед.') },
          { label: 'Занято ячеек', value: data?.stock.occupiedCells == null || data.stock.totalCells == null ? '—' : `${formatStatistic(data.stock.occupiedCells)} из ${formatStatistic(data.stock.totalCells)}` },
          { label: 'Разных SKU', value: formatStatistic(data?.stock.skuCount) },
          { label: 'Позиций с низким остатком', value: formatStatistic(data?.stock.lowStockCount) },
        ]} />
        <div className="statisticsDraft__lowStock">
          <span className="statisticsDraft__process">НИЗКИЙ ОСТАТОК</span>
          <ul>{lowStockItems == null ? <li>—</li> : lowStockItems.length === 0 ? <li>Нет позиций с низким остатком</li> : lowStockItems.map(item => <li key={item.sku}><span>SKU {item.sku}</span><span>{formatStatistic(item.quantity, 'ед.')}</span></li>)}</ul>
        </div>
      </section>
      <section className="statisticsDraft__section" aria-labelledby="statistics-assembly">
        <h3 id="statistics-assembly" className="ds-v1 ui-v1-type-section">СБОРКА</h3>
        <p className="statisticsDraft__context">Результаты за период и текущая очередь</p>
        <Metrics items={[
          { label: 'Собрано за период', value: formatStatistic(data?.assembly.completedOrders) },
          { label: 'Сейчас в очереди', value: formatStatistic(data?.assembly.queuedOrders) },
          { label: 'Активных сейчас', value: formatStatistic(data?.assembly.activeOrders) },
          { label: 'Проблемных за период', value: formatStatistic(data?.assembly.problemOrders) },
          { label: 'Среднее время сборки', value: formatAssemblyDuration(data?.assembly.averageAssemblySeconds) },
        ]} />
      </section>
    </div>

    <section className="statisticsDraft__section statisticsDraft__history" aria-labelledby="statistics-history">
      <div className="statisticsDraft__historyHeading">
        <h3 id="statistics-history" className="ds-v1 ui-v1-type-section">ПОСЛЕДНИЕ ДЕЙСТВИЯ</h3>
      </div>
      <div className="statisticsDraft__tableWrap">
        <table className="statisticsDraft__table">
          <thead><tr><th scope="col" aria-sort={timeOrder === 'asc' ? 'ascending' : 'descending'}><span className="statisticsDraft__timeHeading">Время <button type="button" className="statisticsDraft__sort ds-v1 ui-v1-button ui-v1-sort-button"
            aria-label={`Сортировка по времени: ${timeOrder === 'asc' ? 'от старых к новым' : 'от новых к старым'}. Нажмите, чтобы изменить`}
            title={timeOrder === 'asc' ? 'От старых к новым' : 'От новых к старым'} onClick={() => setTimeOrder(value => value === 'asc' ? 'desc' : 'asc')}>
            <span aria-hidden="true">{timeOrder === 'asc' ? '↑' : '↓'}</span>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={timeOrder === 'asc' ? 'M12 20V4m-7 7 7-7 7 7' : 'M12 4v16m-7-7 7 7 7-7'} /></svg>
          </button></span></th><th scope="col">Событие</th><th scope="col">Описание</th><th scope="col">Связано с</th></tr></thead>
          <tbody>{history.status !== 'ready' || sortedActions.length === 0 ? <tr><td colSpan={4} role="status">{history.status === 'loading' ? 'Загрузка действий…' : history.status === 'error' ? 'История временно недоступна' : 'Действий пока нет'}</td></tr> : sortedActions.map(action => <tr key={action.id}>
            <td><time dateTime={action.occurredAt}>{formatHistoryTime(action.occurredAt)}</time></td>
            <td><div className="statisticsDraft__event"><span className="statisticsDraft__process">{historyProcessLabels[action.process]}</span><span>{action.event}</span></div></td>
            <td>{action.description}</td><td><div className="statisticsDraft__references">{action.references.map(reference => <CopyBadge key={`${reference.type}:${reference.value}`} value={formatHistoryReference(reference)} />)}</div></td>
          </tr>)}</tbody>
        </table>
      </div>
    </section>
  </div>
}
