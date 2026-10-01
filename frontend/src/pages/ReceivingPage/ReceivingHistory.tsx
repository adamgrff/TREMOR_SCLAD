import { useEffect, useRef, useState } from 'react'
import './ReceivingHistory.css'
import { API_BASE_URL as API } from '../../config/api'

type Summary = {
  id: number
  completedAt: string
  productCount: number
  cellCount: number
  quantity: number
}

type HistoryPage = { sessions: Summary[]; nextCursor: number | null }
type HistoryItem = {
  placementId: number
  cellCode: string
  name: string
  sku: string
  quantity: number
}

const HISTORY_ORDER_STORAGE_KEY = 'tremor.receiving.historyTimeOrder'
const dateFormat = new Intl.DateTimeFormat('ru-RU', {
  day: '2-digit', month: '2-digit', year: 'numeric',
  hour: '2-digit', minute: '2-digit',
})

function DeleteReceivingDialog({ session, onCancel, onDeleted }: {
  session: Summary
  onCancel: () => void
  onDeleted: (id: number) => void
}) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const requestInFlight = useRef(false)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const dialog = dialogRef.current
    dialog?.showModal()
    return () => dialog?.close()
  }, [])

  async function confirmDeletion() {
    if (requestInFlight.current) return
    requestInFlight.current = true
    setDeleting(true)
    setError('')
    try {
      const response = await fetch(`${API}/receiving/sessions/${session.id}`, { method: 'DELETE' })
      if (!response.ok && response.status !== 404) {
        throw new Error(response.status === 409
          ? 'Активную приёмку удалить нельзя. Обновите историю.'
          : 'Не удалось удалить запись. Проверьте подключение и повторите попытку.')
      }
      onDeleted(session.id)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Связь прервалась. Повторите попытку — остатки товаров не изменятся.')
      requestInFlight.current = false
      setDeleting(false)
    }
  }

  return (
    <dialog ref={dialogRef} className="receivingHistory__dialog" aria-labelledby="delete-receiving-title" aria-describedby="delete-receiving-description" onCancel={(event) => { event.preventDefault(); if (!deleting) onCancel() }}>
      <h2 id="delete-receiving-title">Удалить приёмку №{session.id} из истории?</h2>
      <p>Завершена {dateFormat.format(new Date(session.completedAt))}. Всего: {session.quantity} ед.</p>
      <p id="delete-receiving-description">Запись исчезнет из истории приёмок. Товары останутся в своих ячейках, остатки на складе не изменятся.</p>
      {error && <p role="alert" className="receivingHistory__deleteError">{error}</p>}
      <div className="receivingHistory__dialogActions">
        <button type="button" className="receivingHistory__button" disabled={deleting} autoFocus onClick={onCancel}>Отмена</button>
        <button type="button" className="receivingHistory__button receivingHistory__deleteButton" disabled={deleting} onClick={() => void confirmDeletion()}>{deleting ? 'Удаляю…' : 'Удалить из истории'}</button>
      </div>
    </dialog>
  )
}

function HistoryRow({ session, loading, onDelete }: {
  session: Summary; loading: boolean
  onDelete: () => void
}) {
  const [expanded, setExpanded] = useState(false)
  const [items, setItems] = useState<HistoryItem[] | null>(null)
  const [requested, setRequested] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!requested) return
    const controller = new AbortController()
    void fetch(`${API}/receiving/sessions/${session.id}`, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error('Не удалось загрузить состав приёмки')
        const data = await response.json() as { placedItems: HistoryItem[] }
        if (!controller.signal.aborted) {
          setItems(data.placedItems)
          setExpanded(true)
        }
      })
      .catch(() => { if (!controller.signal.aborted) setError('Не удалось загрузить состав. Нажмите глаз, чтобы повторить.') })
      .finally(() => { if (!controller.signal.aborted) setRequested(false) })
    return () => controller.abort()
  }, [requested, session.id])

  const label = `${expanded ? 'Скрыть' : 'Показать'} состав приёмки №${session.id}`
  return (
    <tr>
      <td>№{session.id}</td>
      <td colSpan={3} className="receivingHistory__composition">
        <div className={`receivingHistory__compositionGrid receivingHistory__compositionHeading${expanded ? ' receivingHistory__compositionHeading--expanded' : ''}`}>
          <span>{expanded ? 'Товар / артикул' : session.productCount}</span>
          <span>{expanded ? 'Ячейка' : session.cellCount}</span>
          <span>{expanded ? 'Кол-во' : session.quantity}</span>
        </div>
        <div id={`receiving-composition-${session.id}`} className={`receivingHistory__reveal${expanded ? ' receivingHistory__reveal--open' : ''}`} aria-hidden={!expanded}>
          <div>{(items ?? []).map((item) => (
            <div className="receivingHistory__compositionGrid receivingHistory__compositionItem" key={`${item.placementId}-${item.sku}`}>
              <span>{item.name}<span className="receivingHistory__sku">{item.sku}</span></span>
              <span>{item.cellCode}</span><span>{item.quantity}</span>
            </div>
          ))}{items?.length === 0 && <p className="receivingHistory__emptyComposition">В приёмке нет размещений.</p>}</div>
        </div>
      </td>
      <td>{dateFormat.format(new Date(session.completedAt))}</td>
      <td><div className="receivingHistory__rowActions">
        <button type="button" className="receivingHistory__button receivingHistory__iconButton" disabled={loading || requested} aria-busy={requested} aria-label={label} title={requested ? 'Загружаю состав…' : label} aria-expanded={expanded} aria-controls={`receiving-composition-${session.id}`} onClick={() => {
          setError('')
          if (items !== null) setExpanded((value) => !value)
          else setRequested(true)
        }}>
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z" /><circle cx="12" cy="12" r="3" />
            {expanded && <path d="m3 3 18 18" />}
          </svg>
        </button>
        <button type="button" disabled={loading} className="receivingHistory__button receivingHistory__deleteButton receivingHistory__iconButton" aria-label={`Удалить приёмку №${session.id} из истории`} title={`Удалить приёмку №${session.id} из истории`} onClick={onDelete}>
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3 6h18M9 6V4h6v2M5 6l1 14h12l1-14M10 10v6M14 10v6" /></svg>
        </button>
      </div>{error && <p role="alert" className="receivingHistory__rowError">{error}</p>}</td>
    </tr>
  )
}

function CloseHistoryDialog({ onClose, onConfirm }: { onClose: () => void; onConfirm: () => void }) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const dialog = dialogRef.current
    dialog?.showModal()
    return () => dialog?.close()
  }, [])
  return <dialog ref={dialogRef} className="receivingHistory__dialog" aria-labelledby="close-history-title" aria-describedby="close-history-description" onCancel={(event) => { event.preventDefault(); onClose() }}>
    <h2 id="close-history-title">Завершить историю приёмок за день?</h2>
    <p id="close-history-description">После подключения экспорта приёмки будут сохранены в Excel, а окно истории подготовлено к следующему дню. Сейчас сохранение и очистка ещё не подключены — записи останутся на месте.</p>
    <div className="receivingHistory__dialogActions">
      <button type="button" className="receivingHistory__button" autoFocus onClick={onClose}>Отмена</button>
      <button type="button" className="receivingHistory__button" onClick={onConfirm}>Подтвердить</button>
    </div>
  </dialog>
}

function HistoryList({ onDeleted }: { onDeleted: (id: number) => void }) {
  const [order, setOrder] = useState<'asc' | 'desc'>(() => {
    try { return localStorage.getItem(HISTORY_ORDER_STORAGE_KEY) === 'asc' ? 'asc' : 'desc' }
    catch { return 'desc' }
  })
  const [cursors, setCursors] = useState<number[]>([0])
  const [page, setPage] = useState<HistoryPage | null>(null)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [pendingDelete, setPendingDelete] = useState<Summary | null>(null)
  const [pendingClose, setPendingClose] = useState(false)
  const [notice, setNotice] = useState('')
  const [loading, setLoading] = useState(true)
  const cursor = cursors[cursors.length - 1]

  useEffect(() => {
    const controller = new AbortController()
    void fetch(`${API}/receiving/sessions?order=${order}${cursor ? `&before=${cursor}` : ''}`, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error('Не удалось загрузить историю. Проверьте, что backend обновлён и запущен')
        const data = await response.json() as HistoryPage
        if (!controller.signal.aborted) {
          setPage(data)
        }
      })
      .catch((reason: unknown) => {
        if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : 'Не удалось загрузить историю')
      })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [cursor, attempt, order])

  function resetView() {
    setLoading(true)
    setError('')
  }

  function toggleOrder() {
    const value = order === 'asc' ? 'desc' : 'asc'
    resetView()
    setCursors([0])
    setOrder(value)
    try { localStorage.setItem(HISTORY_ORDER_STORAGE_KEY, value) } catch { /* Sorting still works without browser storage. */ }
  }

  function refreshHistory() {
    resetView()
    setCursors([0])
    setAttempt((value) => value + 1)
  }

  return (
    <>
      <div className="receivingHistory__toolbar">
        <p className="receivingHistory__hint">Завершённые приёмки сохраняются здесь. Нажмите на глаз, чтобы посмотреть размещения.</p>
        <div className="receivingHistory__tools">
          <button type="button" className="receivingHistory__button receivingHistory__iconButton" disabled={loading || !page?.sessions.length} aria-label="Завершить историю приёмок за день" title="Завершить историю приёмок за день" onClick={() => setPendingClose(true)}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m5 12 4 4L19 6" /></svg>
          </button>
          <button type="button" className="receivingHistory__button receivingHistory__iconButton" disabled={loading} aria-label="Обновить историю" title="Обновить историю" onClick={refreshHistory}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M20 7v5h-5M20 12a8 8 0 1 0-2.3 5.7" /></svg>
          </button>
        </div>
      </div>
      {notice && <p role="status">{notice}</p>}
      {pendingClose && <CloseHistoryDialog onClose={() => setPendingClose(false)} onConfirm={() => { setPendingClose(false); setNotice('Экспорт в Excel пока не подключён. История сохранена без изменений.') }} />}
      {pendingDelete && <DeleteReceivingDialog session={pendingDelete} onCancel={() => setPendingDelete(null)} onDeleted={(id) => {
        setPendingDelete(null)
        setNotice(`Приёмка №${id} удалена из истории. Остатки товаров сохранены.`)
        refreshHistory()
        onDeleted(id)
      }} />}
      {error && <p role="alert">{error}. <button className="receivingHistory__button" disabled={loading} onClick={() => { resetView(); setAttempt((value) => value + 1) }}>Повторить</button></p>}
      {page === null ? (!error && <p role="status">Загружаю историю…</p>)
        : page.sessions.length === 0 ? <p>Завершённых приёмок пока нет.</p>
          : <>
            <div className="receivingHistory__tableScroll" aria-busy={loading}>
              <table className="receivingHistory__table receivingHistory__summaryTable">
                <colgroup><col className="receivingHistory__receiptCol" /><col /><col className="receivingHistory__cellCol" /><col className="receivingHistory__quantityCol" /><col className="receivingHistory__dateCol" /><col className="receivingHistory__actionsCol" /></colgroup>
                <thead><tr><th>Приёмка</th><th>Позиций</th><th>Ячейки</th><th>Кол-во</th><th aria-sort={order === 'asc' ? 'ascending' : 'descending'}><span className="receivingHistory__columnHeading">Завершена <button type="button" disabled={loading} className="receivingHistory__button receivingHistory__sort" aria-label={`Сортировка по времени завершения: ${order === 'asc' ? 'от старых к новым' : 'от новых к старым'}. Нажмите, чтобы изменить`} title={order === 'asc' ? 'От старых к новым' : 'От новых к старым'} onClick={toggleOrder}>{order === 'asc' ? '↑' : '↓'}</button></span></th><th>Действия</th></tr></thead>
                <tbody>{page.sessions.map((session) => (
                  <HistoryRow key={session.id} session={session} loading={loading} onDelete={() => setPendingDelete(session)} />
                ))}</tbody>
              </table>
            </div>
            <nav className="receivingHistory__pagination" aria-label="Страницы истории приёмок">
              <button className="receivingHistory__button" disabled={loading || cursors.length === 1} onClick={() => { resetView(); setCursors(cursors.slice(0, -1)) }}>Предыдущая</button>
              <span>Страница {cursors.length}</span>
              <button className="receivingHistory__button" disabled={loading || page.nextCursor === null} onClick={() => { if (page.nextCursor !== null) { resetView(); setCursors([...cursors, page.nextCursor]) } }}>Следующая</button>
            </nav>
          </>}
    </>
  )
}

export default function ReceivingHistory({ revision, onDeleted }: { revision: number; onDeleted: (id: number) => void }) {
  return (
    <section className="receivingPage__panel receivingHistory" id="receiving-history" aria-labelledby="receiving-history-title">
      <h2 className="receivingPage__panelTitle" id="receiving-history-title">ИСТОРИЯ ПРИЁМОК</h2>
      <HistoryList key={revision} onDeleted={onDeleted} />
    </section>
  )
}
