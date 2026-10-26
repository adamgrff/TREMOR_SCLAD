import { useEffect, useState } from 'react'
import { API_BASE_URL } from '../../config/api'
import { allProblemTypes } from './problemTypes'
import type { AssemblyPreviewOrder, AssemblyProblemSnapshot } from './ActiveAssembly'
import type { SavedOrderProblem } from './OrderProblemDialog'
import ProblemHistory, { ProblemTime, type HistoryRecord } from './ProblemHistory'
import './ProblemArchive.css'

type ArchiveRow = { problemId: string; number: string; source: string; type: string; outcome: string; createdAt: string; closedAt: string | null; eventCount: number; positions: Array<{ sku: string; name: string | null }> }
type ArchivePage = { problems: ArchiveRow[]; total: number; page: number; pageCount: number }
type StockCheck = { id: string; recordedAt: string; result: { enough: boolean; positions: Array<{ sku: string; name: string; required: number; available: number; missing: number; error?: string }> } }
type OzonJournal = { id: string; purpose: string; attempts: Array<{ requestId: string; kind: string; recordedAt: string }>; observations: Array<{ id: string; outcome: string; observedAt: string; recordedAt: string }> }
type ReturnJournal = { id: string; status: string; returns: Array<{ id: string; operationId: string; sku: string; cell: string; cellId: number; quantity: number; occurredAt: string }> }
type ArchiveDetail = {
  entry: { problemId: string; order: AssemblyPreviewOrder; problem: SavedOrderProblem; assembly?: AssemblyProblemSnapshot }
  originalOrder: AssemblyPreviewOrder
  originalProblem: { comment?: string; type: string }
  historyRecords: HistoryRecord[]
  relatedProblems: Array<{ problemId: string; type: string; resolutionStatus: string; disposition: string; closedOutcome: string | null; createdAt: string }>
  journals: { stockChecks?: StockCheck[]; ozon?: OzonJournal[]; disassembly?: ReturnJournal[] }
}
const outcomes: Record<string, string> = { returned: 'Возвращена в сборку', completed: 'Заказ завершён', cancelled: 'Заказ отменён', disassembled: 'Заказ разобран' }
const ozonResults: Record<string, string> = { unknown: 'Результат неизвестен', not_applied: 'Выполнение не подтверждено', applied: 'Выполнение подтверждено', rejected: 'Отказ', cancelled: 'Отмена подтверждена' }
const reason = (type: string) => allProblemTypes.find(([key]) => key === type)?.[1] ?? type
function numberOrUnknown(value?: number) { return value === undefined || value === null ? '—' : value }

export function ArchiveProblemDetail({ data, onSelect }: { data: ArchiveDetail; onSelect: (id: string) => void }) {
  const { entry, originalOrder, originalProblem, historyRecords, relatedProblems, journals } = data
  const context = entry.problem.creationContext
  const picks = entry.assembly?.picks ?? []
  const outcome = entry.problem.resolutionStatus === 'returned' ? 'returned' : entry.problem.closedOutcome
  return <article className="problemArchive__detail" aria-label={`История проблемы заказа ${entry.order.number}`}>
    <div className="problemArchive__detailTitle"><h3 className="ds-v1 ui-v1-type-column">Заказ {entry.order.number} · {reason(entry.problem.type)}</h3><span>{outcome ? outcomes[outcome] ?? outcome : 'Открытая проблема'}</span></div>
    <p className="problemOrders__muted ds-v1 ui-v1-type-secondary">Просмотр истории. Из архива нельзя изменить статус, повторить операцию или вернуть товар.</p>
    <div className="problemArchive__context">
      <div><h3 className="ds-v1 ui-v1-type-section">Контекст при возникновении</h3><p className="ds-v1 ui-v1-type-secondary">{context?.source === 'assembly' ? 'Во время сборки' : context?.source === 'queue' ? 'До начала сборки' : entry.assembly ? 'Сохранённый снимок сборки' : 'Источник возникновения неизвестен'} · {entry.order.source === 'ozon' ? 'Реальный заказ' : 'Исторический пример'}</p>
        {!context?.complete && <p className="problemArchive__legacy">Старый снимок неполный. Точное время старых сканов неизвестно; время записи не заменяет время скана.</p>}
        <div className="problemOrders__tableScroll"><table><thead><tr><th>Товар / SKU</th><th>Нужно</th><th>Отобрано</th><th>Доступно</th><th>Не хватает</th></tr></thead><tbody>{originalOrder.items.map(item => {
          const position = context?.positions.find(p => p.sku === item.sku)
          return <tr key={item.sku}><td><strong>{item.name}</strong><small>{item.sku}</small></td><td>{item.quantity}</td><td>{numberOrUnknown(position?.picked)}</td><td>{numberOrUnknown(position?.available)}</td><td>{numberOrUnknown(position?.missing)}</td></tr>
        })}</tbody></table></div>
        <h3 className="ds-v1 ui-v1-type-section">Комментарий при создании</h3><p className="problemOrders__comment ds-v1 ui-v1-type-secondary">{originalProblem.comment || 'Комментарий не добавлен'}</p>
        <h3 className="ds-v1 ui-v1-type-section">Отборы на момент проблемы</h3>{picks.length ? <div className="problemOrders__tableScroll"><table><thead><tr><th>Товар</th><th>Исходная ячейка</th><th>Время скана</th></tr></thead><tbody>{picks.map((pick, index) => <tr key={pick.operationId ?? index}><td>{originalOrder.items.find(p => p.sku === pick.sku)?.name ?? pick.sku}<small>{pick.sku}</small></td><td>{pick.cell}{pick.cellId && <small>CELL:{pick.cellId}</small>}</td><td>{pick.occurredAt ? <ProblemTime value={pick.occurredAt}/> : 'Время скана неизвестно'}</td></tr>)}</tbody></table></div> : <p className="problemOrders__muted ds-v1 ui-v1-type-secondary">На момент проблемы отборов не было.</p>}
      </div>
      <div><h3 className="ds-v1 ui-v1-type-section">Другие проблемы этого заказа</h3><ul className="problemArchive__related">{relatedProblems.map(p => <li key={p.problemId}><button type="button" className="receivingHistory__button ds-v1 ui-v1-button" disabled={p.problemId === entry.problemId} onClick={() => onSelect(p.problemId)}>{reason(p.type)}</button><span>{p.disposition === 'returned' ? outcomes.returned : p.closedOutcome ? outcomes[p.closedOutcome] ?? p.closedOutcome : p.resolutionStatus === 'resolved' ? 'Решена, ожидает дальнейшего действия' : 'Ожидает решения'}</span><ProblemTime value={p.createdAt}/></li>)}</ul>
        {!!journals.stockChecks?.length && <details><summary>Проверки остатков · {journals.stockChecks.length}</summary>{journals.stockChecks.map(check => <div key={check.id} className="problemArchive__journal"><ProblemTime value={check.recordedAt}/>{check.result.positions.map(position => <p className="ds-v1 ui-v1-type-primary" key={position.sku}>{position.name} · {position.sku}: требуется добрать {position.required}, доступно {position.available}, не хватает {position.missing}{position.error && ` · ${position.error}`}</p>)}</div>)}</details>}
        {!!journals.ozon?.length && <details><summary>Журнал Ozon</summary>{journals.ozon.map(op => <div key={op.id} className="problemArchive__journal"><strong>{op.purpose === 'confirm_shipment' ? 'Подтверждение отправления' : 'Проверка данных заказа'}</strong>{op.attempts.map(attempt => <p className="ds-v1 ui-v1-type-primary" key={attempt.requestId}><ProblemTime value={attempt.recordedAt}/>{attempt.kind === 'unavailable' ? 'Сверка недоступна; эта попытка не отправляла бизнес-операцию.' : 'Запрошена сверка исходной операции.'}</p>)}{op.observations.map(observation => <p className="ds-v1 ui-v1-type-primary" key={observation.id}><ProblemTime value={observation.observedAt}/>{ozonResults[observation.outcome] ?? observation.outcome}</p>)}</div>)}</details>}
        {!!journals.disassembly?.length && <details><summary>Физические возвраты</summary>{journals.disassembly.map(session => <div className="problemArchive__journal" key={session.id}><strong>{session.status === 'completed' ? 'Разбор завершён' : session.status === 'active' ? 'Разбор активен' : 'Ожидает разбора'}</strong>{session.returns.map(r => <p className="ds-v1 ui-v1-type-primary" key={r.id}><ProblemTime value={r.occurredAt}/>{r.sku}: {r.quantity} шт. → исходная ячейка {r.cell} [CELL:{r.cellId}]<small>Возврат {r.id} · исходный отбор {r.operationId}</small></p>)}</div>)}</details>}
      </div>
    </div>
    <h3 className="ds-v1 ui-v1-type-section">История проблемы</h3><div className="problemArchive__history"><ProblemHistory key={entry.problemId} events={historyRecords} fullByDefault/></div>
  </article>
}

export default function ProblemArchive({ refreshToken, onClose }: { refreshToken: unknown; onClose: () => void }) {
  const [q, setQuery] = useState(''), [type, setType] = useState(''), [outcome, setOutcome] = useState(''), [source, setSource] = useState('')
  const [page, setPage] = useState(1), [refresh, setRefresh] = useState(0)
  const [data, setData] = useState<ArchivePage | null>(null), [loading, setLoading] = useState(true), [error, setError] = useState('')
  const [selected, setSelected] = useState(''), [detail, setDetail] = useState<ArchiveDetail | null>(null), [detailError, setDetailError] = useState(''), [detailLoading, setDetailLoading] = useState(false)
  useEffect(() => {
    const controller = new AbortController(); setLoading(true); setError(''); setData(null)
    const params = new URLSearchParams({ page: String(page), pageSize: '5', q: q.trim(), type, outcome, source })
    void fetch(`${API_BASE_URL}/assembly/problems/archive?${params}`, { signal: controller.signal, cache: 'no-store' }).then(async response => {
      if (!response.ok) throw new Error('Не удалось загрузить архив.')
      const next = await response.json() as ArchivePage
      if (!controller.signal.aborted) setData(next)
    }).catch((reason: unknown) => { if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : 'Архив недоступен.') }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [q, type, outcome, source, page, refresh, refreshToken])
  useEffect(() => {
    const controller = new AbortController(); setDetail(null); setDetailError('')
    if (!selected) { setDetailLoading(false); return () => controller.abort() }
    setDetailLoading(true)
    void fetch(`${API_BASE_URL}/assembly/problems/${encodeURIComponent(selected)}/history`, { signal: controller.signal, cache: 'no-store' }).then(async response => {
      if (!response.ok) throw new Error(response.status === 404 ? 'Запись не найдена.' : 'Не удалось загрузить историю.')
      const next = await response.json() as ArchiveDetail
      if (!controller.signal.aborted) setDetail(next)
    }).catch((reason: unknown) => { if (!controller.signal.aborted) setDetailError(reason instanceof Error ? reason.message : 'История недоступна.') }).finally(() => { if (!controller.signal.aborted) setDetailLoading(false) })
    return () => controller.abort()
  }, [selected, refresh, refreshToken])
  const filterChanged = () => { setPage(1); setSelected('') }
  return <section className="receivingPage__panel problemOrders problemArchive ds-v1 ui-v1-window" aria-labelledby="problem-archive-title">
    <div className="problemOrders__header"><div><h2 className="receivingPage__panelTitle ds-v1 ui-v1-page-title" id="problem-archive-title">АРХИВ ПРОБЛЕМ</h2><p className="assemblyQueue__subtitle ds-v1 ui-v1-type-secondary">Закрытые записи и исходная история · {data?.total ?? '—'}</p></div><div className="problemOrders__tools"><button className="receivingHistory__button ds-v1 ui-v1-button" type="button" onClick={() => setRefresh(v => v + 1)} disabled={loading} aria-busy={loading}>Обновить архив</button><button className="receivingHistory__button ds-v1 ui-v1-button" type="button" onClick={onClose}>Закрыть архив</button></div></div>
    <div className="problemArchive__filters"><input className="ds-v1 ui-v1-input" type="search" maxLength={200} value={q} aria-label="Поиск в архиве по заказу, товару или SKU" placeholder="Заказ, товар или SKU" onChange={e => { setQuery(e.target.value); filterChanged() }}/><select className="ds-v1 ui-v1-input ui-v1-select ui-v1-select--customizable problemArchive__select--reason" aria-label="Тип архивной проблемы" value={type} onChange={e => { setType(e.target.value); filterChanged() }}><option value="">Все причины</option>{allProblemTypes.map(([key, label]) => <option value={key} key={key}>{label}</option>)}</select><select className="ds-v1 ui-v1-input ui-v1-select ui-v1-select--customizable problemArchive__select--outcome" aria-label="Исход закрытой проблемы" value={outcome} onChange={e => { setOutcome(e.target.value); filterChanged() }}><option value="">Все исходы</option>{Object.entries(outcomes).map(([key, label]) => <option value={key} key={key}>{label}</option>)}</select><select className="ds-v1 ui-v1-input ui-v1-select ui-v1-select--customizable problemArchive__select--source" aria-label="Источник архивного заказа" value={source} onChange={e => { setSource(e.target.value); filterChanged() }}><option value="">Все источники</option><option value="ozon">Реальные заказы</option><option value="preview">Исторические примеры</option></select></div>
    {loading && <p role="status" className="ds-v1 ui-v1-feedback">Загрузка архива…</p>}{error && <p role="alert" className="ds-v1 ui-v1-feedback ui-v1-feedback--error">{error} <button type="button" className="receivingHistory__button ds-v1 ui-v1-button" onClick={() => setRefresh(v => v + 1)}>Повторить загрузку</button></p>}
    {data && <><div className="problemArchive__table"><table className="ds-v1 ui-v1-table"><thead><tr><th>Заказ / причина</th><th>Затронутые позиции</th><th>Исход</th><th>Создана</th><th>Закрыта</th><th/></tr></thead><tbody>{data.problems.map(row => <tr key={row.problemId} className={selected === row.problemId ? 'problemArchive__selected' : ''}><td><strong>{row.number}</strong><small>{reason(row.type)}</small>{row.source !== 'ozon' && <small>Исторический пример</small>}</td><td>{row.positions.map(p => <span className="problemArchive__position" key={p.sku}>{p.name ?? p.sku}<small>{p.sku}</small></span>)}</td><td>{outcomes[row.outcome] ?? row.outcome}<small>{row.eventCount} событий</small></td><td><ProblemTime value={row.createdAt}/></td><td>{row.closedAt ? <ProblemTime value={row.closedAt}/> : <small>Точное время закрытия не сохранено</small>}</td><td><button type="button" className="receivingHistory__button ds-v1 ui-v1-button" onClick={() => setSelected(selected === row.problemId ? '' : row.problemId)}>{selected === row.problemId ? 'Скрыть' : 'История'}</button></td></tr>)}</tbody></table></div>{!data.problems.length && <p className="problemOrders__empty ds-v1 ui-v1-feedback ui-v1-feedback--empty">{q || type || outcome || source ? 'По этим фильтрам ничего не найдено.' : 'Закрытых проблем пока нет.'}</p>}
      <nav className="receivingHistory__pagination" aria-label="Страницы архива проблем"><button className="receivingHistory__button ds-v1 ui-v1-button" type="button" disabled={loading || data.page <= 1} aria-busy={loading} onClick={() => { setPage(data.page - 1); setSelected('') }}>Предыдущая</button><span className="ds-v1 ui-v1-type-secondary">Страница {data.page} из {data.pageCount}</span><button className="receivingHistory__button ds-v1 ui-v1-button" type="button" disabled={loading || data.page >= data.pageCount} aria-busy={loading} onClick={() => { setPage(data.page + 1); setSelected('') }}>Следующая</button></nav></>}
    {detailLoading && <p role="status" className="ds-v1 ui-v1-feedback">Загрузка истории…</p>}{detailError && <p role="alert" className="ds-v1 ui-v1-feedback ui-v1-feedback--error">{detailError} <button className="receivingHistory__button ds-v1 ui-v1-button" type="button" onClick={() => setRefresh(v => v + 1)}>Повторить загрузку</button></p>}
    {detail && <ArchiveProblemDetail data={detail} onSelect={setSelected}/>}
  </section>
}
