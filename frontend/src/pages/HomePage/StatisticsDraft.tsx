import { useEffect, useRef, useState, type CSSProperties } from 'react'
import './StatisticsDraft.css'

const periods = ['Сегодня', '7 дней', '30 дней'] as const
const examples = {
  'Сегодня': { receipts: 9, received: 268, placed: 256, assembled: 34, problems: 2 },
  '7 дней': { receipts: 52, received: 1640, placed: 1628, assembled: 218, problems: 9 },
  '30 дней': { receipts: 206, received: 6824, placed: 6812, assembled: 946, problems: 27 },
}
const actions = [
  { time: '14:42', process: 'СБОРКА', event: 'Заказ собран', description: 'Сборка завершена, заказ готов к отгрузке', references: ['№1048-0032'] },
  { time: '14:38', process: 'ПРИЁМКА', event: 'Товар размещён', description: '24 ед. размещены в ячейке A3', references: ['SKU TR-042', 'A3'] },
  { time: '14:35', process: 'СБОРКА', event: 'Начата сборка', description: 'Заказ передан в активную сборку', references: ['№1048-0033'] },
  { time: '14:31', process: 'СБОРКА', event: 'Проблемный заказ', description: 'Не хватает 2 ед. товара для сборки', references: ['№1048-0029'] },
  { time: '14:26', process: 'ПРИЁМКА', event: 'Товар принят', description: 'Принято 36 ед., ожидают размещения', references: ['SKU TR-018'] },
  { time: '14:18', process: 'ЯЧЕЙКИ', event: 'Товар перемещён', description: '8 ед. перемещены из A1 в B2', references: ['SKU TR-007', 'A1', 'B2'] },
  { time: '14:10', process: 'OZON', event: 'Синхронизация завершена', description: 'Очередь заказов обновлена: 6 новых заказов', references: [] },
]
const number = (value: number) => value.toLocaleString('ru-RU')

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
  const data = examples[period]
  const sortedActions = [...actions].sort((left, right) => timeOrder === 'asc' ? left.time.localeCompare(right.time) : right.time.localeCompare(left.time))

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
          { label: 'Приёмок', value: number(data.receipts) },
          { label: 'Принято', value: `${number(data.received)} ед.` },
          { label: 'Размещено', value: `${number(data.placed)} ед.` },
          { label: 'Ожидает размещения сейчас', value: '12 ед.' },
        ]} />
      </section>
      <section className="statisticsDraft__section" aria-labelledby="statistics-stock">
        <h3 id="statistics-stock" className="ds-v1 ui-v1-type-section">ЯЧЕЙКИ</h3>
        <p className="statisticsDraft__context">Текущее состояние · не зависит от периода</p>
        <Metrics items={[
          { label: 'Товара на складе', value: '1 248 ед.' },
          { label: 'Занято ячеек', value: '42 из 60' },
          { label: 'Разных SKU', value: '86' },
          { label: 'Позиций с низким остатком', value: '4' },
        ]} />
        <div className="statisticsDraft__lowStock">
          <span className="statisticsDraft__process">НИЗКИЙ ОСТАТОК</span>
          <ul>{[{ sku: 'TR-007', quantity: 3 }, { sku: 'TR-018', quantity: 2 }, { sku: 'TR-024', quantity: 4 }, { sku: 'TR-063', quantity: 1 }].map(item => <li key={item.sku}><span>SKU {item.sku}</span><span>{item.quantity} ед.</span></li>)}</ul>
        </div>
      </section>
      <section className="statisticsDraft__section" aria-labelledby="statistics-assembly">
        <h3 id="statistics-assembly" className="ds-v1 ui-v1-type-section">СБОРКА</h3>
        <p className="statisticsDraft__context">Результаты за период и текущая очередь</p>
        <Metrics items={[
          { label: 'Собрано за период', value: number(data.assembled) },
          { label: 'Сейчас в очереди', value: '8' },
          { label: 'Активных сейчас', value: '3' },
          { label: 'Проблемных за период', value: number(data.problems) },
          { label: 'Среднее время сборки', value: '6 мин' },
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
          <tbody>{sortedActions.map(action => <tr key={action.time}>
            <td><time dateTime={`2026-10-26T${action.time}:00+07:00`}>{action.time}</time></td>
            <td><div className="statisticsDraft__event"><span className="statisticsDraft__process">{action.process}</span><span>{action.event}</span></div></td>
            <td>{action.description}</td><td><div className="statisticsDraft__references">{action.references.map(value => <CopyBadge key={value} value={value} />)}</div></td>
          </tr>)}</tbody>
        </table>
      </div>
    </section>
  </div>
}
