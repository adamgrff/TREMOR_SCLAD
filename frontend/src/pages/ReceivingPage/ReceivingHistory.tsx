import { useEffect, useState } from 'react'
import './ReceivingHistory.css'

type Summary = {
  id: number
  completedAt: string
  placementCount: number
  productCount: number
  cellCount: number
  quantity: number
}

type HistoryPage = { sessions: Summary[]; nextCursor: number | null }
type HistoryItem = {
  placementId: number
  cellCode: string
  createdAt: string
  name: string
  sku: string
  quantity: number
}

const API = 'http://127.0.0.1:8080/api'
const dateFormat = new Intl.DateTimeFormat('ru-RU', {
  day: '2-digit', month: '2-digit', year: 'numeric',
  hour: '2-digit', minute: '2-digit',
})

function HistoryDetails({ sessionId }: { sessionId: number }) {
  const [items, setItems] = useState<HistoryItem[] | null>(null)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    void fetch(`${API}/receiving/sessions/${sessionId}`, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error('Не удалось загрузить товары приёмки')
        const session = await response.json() as { status: string; placedItems: HistoryItem[] }
        if (session.status !== 'completed') throw new Error('Приёмка ещё не завершена')
        if (!controller.signal.aborted) setItems(session.placedItems)
      })
      .catch((reason: unknown) => {
        if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : 'Не удалось загрузить товары')
      })
    return () => controller.abort()
  }, [sessionId, attempt])

  if (error) return <p role="alert">{error}. <button className="receivingHistory__button" onClick={() => { setError(''); setAttempt(attempt + 1) }}>Повторить</button></p>
  if (items === null) return <p role="status">Загружаю товары…</p>
  if (items.length === 0) return <p>В сохранённой приёмке нет размещений.</p>

  return (
    <div className="receivingHistory__tableScroll">
      <table className="receivingHistory__table">
        <caption className="receivingHistory__caption">Размещения приёмки №{sessionId}</caption>
        <thead><tr><th>Товар / артикул</th><th>Количество</th><th>Ячейка</th><th>Дата и время</th></tr></thead>
        <tbody>{items.map((item) => (
          <tr key={`${item.placementId}-${item.sku}`}>
            <td>{item.name}<span className="receivingHistory__sku">{item.sku}</span></td>
            <td>{item.quantity}</td><td>{item.cellCode}</td>
            <td>{dateFormat.format(new Date(item.createdAt))}</td>
          </tr>
        ))}</tbody>
      </table>
    </div>
  )
}

function HistoryList() {
  const [cursors, setCursors] = useState<number[]>([0])
  const [page, setPage] = useState<HistoryPage | null>(null)
  const [selected, setSelected] = useState<number | null>(null)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const cursor = cursors[cursors.length - 1]

  useEffect(() => {
    const controller = new AbortController()
    void fetch(`${API}/receiving/sessions${cursor ? `?before=${cursor}` : ''}`, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error('Не удалось загрузить историю. Проверьте, что backend обновлён и запущен')
        const data = await response.json() as HistoryPage
        if (!controller.signal.aborted) setPage(data)
      })
      .catch((reason: unknown) => {
        if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : 'Не удалось загрузить историю')
      })
    return () => controller.abort()
  }, [cursor, attempt])

  function resetView() {
    setPage(null)
    setSelected(null)
    setError('')
  }

  return (
    <>
      <div className="receivingHistory__toolbar">
        <p className="receivingHistory__hint">Завершённые приёмки сохраняются здесь. Выберите номер, чтобы посмотреть размещения.</p>
        <button className="receivingHistory__button" onClick={() => { resetView(); setCursors([0]); setAttempt(attempt + 1) }}>Обновить историю</button>
      </div>
      {error ? <p role="alert">{error}. <button className="receivingHistory__button" onClick={() => { resetView(); setAttempt(attempt + 1) }}>Повторить</button></p>
        : page === null ? <p role="status">Загружаю историю…</p>
        : page.sessions.length === 0 ? <p>Завершённых приёмок пока нет.</p>
        : <>
          <div className="receivingHistory__tableScroll">
            <table className="receivingHistory__table">
              <thead><tr><th>Приёмка</th><th>Завершена</th><th>Позиций</th><th>Единиц</th><th>Ячеек</th></tr></thead>
              <tbody>{page.sessions.map((session) => (
                <tr key={session.id} className={selected === session.id ? 'receivingHistory__selected' : ''}>
                  <td><button className="receivingHistory__button" aria-expanded={selected === session.id} aria-controls="receiving-history-details" onClick={() => setSelected(selected === session.id ? null : session.id)}>№{session.id} {selected === session.id ? '▴' : '▾'}</button></td>
                  <td>{dateFormat.format(new Date(session.completedAt))}</td>
                  <td>{session.productCount}</td><td>{session.quantity}</td><td>{session.cellCount}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
          <div id="receiving-history-details" className="receivingHistory__details">
            {selected !== null && <HistoryDetails key={selected} sessionId={selected} />}
          </div>
          <nav className="receivingHistory__pagination" aria-label="Страницы истории приёмок">
            <button className="receivingHistory__button" disabled={cursors.length === 1} onClick={() => { resetView(); setCursors(cursors.slice(0, -1)) }}>Новее</button>
            <span>Страница {cursors.length}</span>
            <button className="receivingHistory__button" disabled={page.nextCursor === null} onClick={() => { if (page.nextCursor !== null) { resetView(); setCursors([...cursors, page.nextCursor]) } }}>Старее</button>
          </nav>
        </>}
    </>
  )
}

export default function ReceivingHistory({ revision }: { revision: number }) {
  return (
    <section className="receivingPage__panel receivingHistory" aria-labelledby="receiving-history-title">
      <h2 className="receivingPage__panelTitle" id="receiving-history-title">ИСТОРИЯ ПРИЁМОК</h2>
      <HistoryList key={revision} />
    </section>
  )
}
