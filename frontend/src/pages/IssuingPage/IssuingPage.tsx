import { type FormEvent, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import Header from '../../components/Header/Header'
import { useTheme } from '../../hooks/useTheme'
import './IssuingPage.css'

type Cell = { cellId: number; cellCode: string; warehouse: string; quantity: number }
type Product = { sku: string; name: string; cells: Cell[] }
type Issue = {
  id: number; requestId: string; sku: string; name: string; cellCode: string
  warehouse: string; quantity: number; status: 'active' | 'undone'
  createdAt: string; undoneAt: string | null
}
type Command = { requestId: string; sku: string; cellId: number; quantity: number }
type History = { issues: Issue[]; undoableId: number | null }

const API = 'http://127.0.0.1:8080/api'
const PENDING_KEY = 'tremor.issuing.pendingCommand'
const dateFormat = new Intl.DateTimeFormat('ru-RU', {
  day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit',
})

function restoreCommand(): Command | null {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(PENDING_KEY) ?? 'null')
    if (value && typeof value === 'object' && 'requestId' in value && 'sku' in value && 'cellId' in value && 'quantity' in value) {
      const command = value as Command
      if (typeof command.requestId === 'string' && typeof command.sku === 'string' && Number.isSafeInteger(command.cellId) && command.cellId > 0 && Number.isSafeInteger(command.quantity) && command.quantity > 0) return command
    }
  } catch { /* A malformed saved draft must not be sent to the API. */ }
  return null
}

export default function IssuingPage() {
  const { theme, toggleTheme } = useTheme('light')
  const [code, setCode] = useState('')
  const [product, setProduct] = useState<Product | null>(null)
  const [cellId, setCellId] = useState<number | null>(null)
  const [quantity, setQuantity] = useState('1')
  const [pending, setPending] = useState<Command | null>(restoreCommand)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [history, setHistory] = useState<History | null>(null)
  const [historyError, setHistoryError] = useState('')
  const [revision, setRevision] = useState(0)
  const requestInFlight = useRef(false)
  const selectedCell = product?.cells.find((cell) => cell.cellId === cellId)
  const amount = Number(quantity)
  const validQuantity = Number.isSafeInteger(amount) && amount > 0 && amount <= (selectedCell?.quantity ?? 0)

  useEffect(() => {
    const controller = new AbortController()
    void fetch(`${API}/issuing`, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error('Не удалось загрузить историю выдач')
        const data = await response.json() as History
        if (!controller.signal.aborted) { setHistory(data); setHistoryError('') }
      })
      .catch(() => { if (!controller.signal.aborted) setHistoryError('Не удалось загрузить историю выдач. Попробуйте обновить её.') })
    return () => controller.abort()
  }, [revision])

  async function loadProduct(sku: string): Promise<Product> {
    const response = await fetch(`${API}/products/${encodeURIComponent(sku)}/stock`)
    if (response.status === 404) throw new Error('Товар с таким артикулом не найден')
    if (!response.ok) throw new Error('Не удалось загрузить остатки. Проверьте подключение.')
    return await response.json() as Product
  }

  async function findProduct(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || pending || !code.trim()) return
    setBusy(true)
    setMessage('')
    setProduct(null)
    setCellId(null)
    try {
      const result = await loadProduct(code.trim().toUpperCase())
      setProduct(result)
      setCellId(result.cells[0]?.cellId ?? null)
      setQuantity('1')
      if (result.cells.length === 0) setMessage('Товар найден, но на складе нет доступного остатка.')
    } catch (error) {
      setMessage(error instanceof Error ? error.message : 'Не удалось найти товар')
    } finally { setBusy(false) }
  }

  async function refreshStock(sku: string) {
    try {
      const result = await loadProduct(sku)
      setProduct(result)
      setCellId((previous) => result.cells.some((cell) => cell.cellId === previous) ? previous : result.cells[0]?.cellId ?? null)
    } catch { setProduct(null); setCellId(null) }
  }

  async function issueProduct() {
    if (requestInFlight.current || busy) return
    const command = pending ?? (product && cellId !== null && validQuantity
      ? { requestId: crypto.randomUUID(), sku: product.sku, cellId, quantity: amount }
      : null)
    if (!command) return
    // Save before sending: retry the same command if the response is lost or the page reloads.
    try { localStorage.setItem(PENDING_KEY, JSON.stringify(command)) }
    catch { setMessage('Не удалось сохранить действие в браузере. Выдача не отправлена.'); return }
    requestInFlight.current = true
    setPending(command)
    setBusy(true)
    setMessage('')
    try {
      const response = await fetch(`${API}/issuing`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(command),
      })
      if (!response.ok) {
        if ([400, 404, 409].includes(response.status)) {
          localStorage.removeItem(PENDING_KEY)
          setPending(null)
          await refreshStock(command.sku)
          setRevision((value) => value + 1)
          throw new Error(response.status === 409 ? 'Остаток изменился или действие конфликтует с уже сохранённым. Остатки обновлены — проверьте количество.' : 'Не удалось выдать товар: проверьте артикул, ячейку и количество.')
        }
        throw new Error('Не удалось подтвердить выдачу. Повторите сохранённое действие — повторного списания не будет.')
      }
      const issue = await response.json() as Issue
      localStorage.removeItem(PENDING_KEY)
      setPending(null)
      setMessage(issue.status === 'undone'
        ? `Выдача №${issue.id} уже была отменена. Повторного списания нет.`
        : `Выдача №${issue.id} сохранена: ${issue.name}, ${issue.quantity} шт. из ${issue.cellCode}.`)
      setCode(command.sku)
      setQuantity('1')
      await refreshStock(command.sku)
      setRevision((value) => value + 1)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : 'Связь прервалась. Повторите сохранённое действие — повторного списания не будет.')
    } finally { requestInFlight.current = false; setBusy(false) }
  }

  async function undoLastIssue() {
    const id = history?.undoableId
    if (!id || busy || pending || requestInFlight.current) return
    requestInFlight.current = true
    setBusy(true)
    setMessage('')
    try {
      const response = await fetch(`${API}/issuing/${id}/undo`, { method: 'POST' })
      if (response.status === 409) throw new Error('Последняя выдача изменилась или возврат невозможен. История обновлена — проверьте её.')
      if (!response.ok) throw new Error('Не удалось подтвердить отмену. Обновите историю; повторная отмена не возвращает товар дважды.')
      const issue = await response.json() as Issue
      setMessage(`Выдача №${issue.id} отменена. ${issue.quantity} шт. возвращены в ${issue.cellCode}.`)
      if (product) await refreshStock(product.sku)
    } catch (error) { setMessage(error instanceof Error ? error.message : 'Не удалось отменить выдачу') }
    finally { setRevision((value) => value + 1); requestInFlight.current = false; setBusy(false) }
  }

  return (
    <main className={`issuingPage issuingPage--${theme}`}>
      <Header theme={theme} onToggleTheme={toggleTheme} />
      <section className="issuingPage__content" aria-labelledby="issuing-title">
        <div className="issuingPage__titleRow"><h1 id="issuing-title">ПОДБОР И ВЫДАЧА</h1><Link to="/receiving">Приёмка товаров →</Link></div>
        <p className="issuingPage__intro">Отсканируйте артикул товара, выберите ячейку и количество для выдачи.</p>
        <div className="issuingPage__panel">
          <form onSubmit={findProduct} className="issuingPage__scanForm">
            <label htmlFor="issue-sku">Артикул товара</label>
            <input id="issue-sku" placeholder="Например, TREMOR-CLASSIC" value={code} onChange={(event) => setCode(event.target.value)} disabled={busy || pending !== null} autoComplete="off" />
            <button type="submit" disabled={busy || pending !== null || !code.trim()}>НАЙТИ ТОВАР</button>
          </form>
          {pending && <div className="issuingPage__pending" role="status">
            <p>Есть неподтверждённое действие: {pending.sku}, {pending.quantity} шт. Проверьте его результат повторным запросом.</p>
            <button type="button" disabled={busy} onClick={() => void issueProduct()}>ПОВТОРИТЬ СОХРАНЁННОЕ ДЕЙСТВИЕ</button>
          </div>}
          {product && <section className="issuingPage__product" aria-labelledby="issue-product-title">
            <h2 id="issue-product-title">{product.name}</h2><p>{product.sku}</p>
            {product.cells.length > 0 && <>
              <fieldset disabled={busy || pending !== null}>
                <legend>Откуда забрать товар</legend>
                <div className="issuingPage__cells">{product.cells.map((cell) => (
                  <label className={`issuingPage__cell ${cell.cellId === cellId ? 'issuingPage__cell--selected' : ''}`} key={cell.cellId}>
                    <input type="radio" name="issue-cell" value={cell.cellId} checked={cell.cellId === cellId} onChange={() => setCellId(cell.cellId)} />
                    <span><strong>{cell.cellCode}</strong><small>{cell.warehouse}</small><span>Доступно: {cell.quantity} шт.</span></span>
                  </label>
                ))}</div>
              </fieldset>
              <div className="issuingPage__quantityRow">
                <label htmlFor="issue-quantity">Количество</label>
                <input id="issue-quantity" type="number" min="1" max={selectedCell?.quantity ?? 0} step="1" value={quantity} onChange={(event) => setQuantity(event.target.value)} disabled={busy || pending !== null} />
                <button type="button" disabled={busy || pending !== null || !validQuantity} onClick={() => void issueProduct()}>ВЫДАТЬ ТОВАР</button>
              </div>
              {!validQuantity && <p>Укажите целое количество от 1 до {selectedCell?.quantity ?? 0}.</p>}
            </>}
          </section>}
          {message && <p className="issuingPage__message" role="status">{message}</p>}
        </div>
        <section className="issuingPage__panel" aria-labelledby="issuing-history-title">
          <div className="issuingPage__titleRow"><h2 id="issuing-history-title">ПОСЛЕДНИЕ ВЫДАЧИ</h2>
            <div className="issuingPage__actions">
              <button type="button" disabled={busy} onClick={() => setRevision((value) => value + 1)}>Обновить историю</button>
              <button type="button" disabled={busy || pending !== null || historyError !== '' || !history?.undoableId} onClick={() => void undoLastIssue()}>Отменить последнюю выдачу{history?.undoableId ? ` №${history.undoableId}` : ''}</button>
            </div>
          </div>
          <p className="issuingPage__intro">Последние 20 операций. Отмена возвращает товар в исходную ячейку; запись остаётся в истории.</p>
          {historyError ? <p role="alert">{historyError}</p> : history === null ? <p role="status">Загружаю историю…</p> : history.issues.length === 0 ? <p>Выдач пока нет.</p>
            : <div className="issuingPage__tableScroll"><table>
              <thead><tr><th>№ / время</th><th>Товар</th><th>Ячейка</th><th>Количество</th><th>Статус</th></tr></thead>
              <tbody>{history.issues.map((issue) => <tr key={issue.id}>
                <td>№{issue.id}<small>{dateFormat.format(new Date(issue.createdAt))}</small></td>
                <td>{issue.name}<small>{issue.sku}</small></td>
                <td>{issue.cellCode}<small>{issue.warehouse}</small></td><td>{issue.quantity}</td>
                <td>{issue.status === 'active' ? 'Выдано' : 'Отменено'}{issue.undoneAt && <small>{dateFormat.format(new Date(issue.undoneAt))}</small>}</td>
              </tr>)}</tbody>
            </table></div>}
        </section>
      </section>
    </main>
  )
}
