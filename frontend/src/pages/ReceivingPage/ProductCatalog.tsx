import useModalFocusReturn from '../../components/useModalFocusReturn'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import './ProductCatalog.css'
import ReceivingQuantityDialog from './ReceivingQuantityDialog'
import { API_BASE_URL } from '../../config/api'

type Category = { id: number; name: string }
type Product = { id: number; categoryId: number; name: string; sku: string; quantity: number; archived?: boolean }
type Catalog = { categories: Category[]; products: Product[] }

export default function ProductCatalog({ sessionId, disabled, revision, onReceivingChanged }: { sessionId: number | null; disabled: boolean; revision: number; onReceivingChanged: () => Promise<void> }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [query, setQuery] = useState('')
  const [category, setCategory] = useState('all')
  const [showArchived, setShowArchived] = useState(false)
  const [editing, setEditing] = useState<Product | null>(null)
  const [name, setName] = useState('')
  const [sku, setSku] = useState('')
  const [rack, setRack] = useState('')
  const [receivingQuantity, setReceivingQuantity] = useState('0')
  const [receivingProduct, setReceivingProduct] = useState<Product | null>(null)
  const createOperation = useRef<{ payload: string; token: string } | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [formError, setFormError] = useState('')
  const [archiveError, setArchiveError] = useState('')
  const [saving, setSaving] = useState(false)
  const requestInFlight = useRef(false)
  const [attempt, setAttempt] = useState(0)
  const [pendingArchive, setPendingArchive] = useState<Product | null>(null)
  const dialogRef = useRef<HTMLDialogElement>(null)
  const markArchiveConfirmed = useModalFocusReturn(dialogRef, !!pendingArchive, 'product-catalog-title')
  const nameRef = useRef<HTMLInputElement>(null)
  const formRef = useRef<HTMLFormElement>(null)
  const [formHeight, setFormHeight] = useState<number | undefined>(undefined)
  useEffect(() => {
    const form = formRef.current
    if (!form) return
    const observer = new ResizeObserver(() => setFormHeight(form.offsetHeight))
    observer.observe(form)
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void fetch(`${API_BASE_URL}/product-catalog`, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error('Не удалось загрузить каталог')
        const data = await response.json() as Catalog
        if (!controller.signal.aborted) { setCatalog(data); setError(''); setRack((value) => value || String(data.categories[0]?.id ?? '')) }
      })
      .catch(() => { if (!controller.signal.aborted) setError('Не удалось загрузить каталог товаров. Проверьте подключение.') })
    return () => controller.abort()
  }, [attempt, revision])

  useEffect(() => {
    if (!pendingArchive) return
    const dialog = dialogRef.current
    dialog?.showModal()
    return () => dialog?.close()
  }, [pendingArchive])

  function resetForm() { setEditing(null); setName(''); setSku(''); setReceivingQuantity('0'); setNotice(''); setFormError('') }
  async function saveProduct(product: Product, create = false, errorTarget: 'form' | 'archive' = 'form') {
    if (requestInFlight.current) return false
    requestInFlight.current = true
    setSaving(true)
    setNotice('')
    const setSaveError = errorTarget === 'archive' ? setArchiveError : setFormError
    setSaveError('')
    try {
      const amount = create ? Number(receivingQuantity) : 0
      if (amount > 0 && (sessionId === null || disabled)) throw new Error('Начните новую приёмку перед добавлением количества.')
      const data = { name: product.name, sku: product.sku, categoryId: product.categoryId, archived: Boolean(product.archived), receivingQuantity: amount, sessionId: amount > 0 ? sessionId : 0 }
      const payload = JSON.stringify(data)
      if (createOperation.current?.payload !== payload) createOperation.current = { payload, token: crypto.randomUUID() }
      const response = await fetch(`${API_BASE_URL}/product-catalog${create ? '' : `/${product.id}`}`, {
        method: create ? 'POST' : 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...data, requestId: createOperation.current.token }),
      })
      if (!response.ok) {
        const data = await response.json() as { error?: string }
        throw new Error(data.error || 'Не удалось сохранить товар')
      }
      setAttempt((value) => value + 1)
      setNotice('Изменения сохранены в базе.')
      if (amount > 0) await onReceivingChanged()
      return true
    } catch (reason) { setSaveError(reason instanceof Error ? reason.message : 'Ошибка соединения. Проверьте каталог перед повторной отправкой.'); return false }
    finally { requestInFlight.current = false; setSaving(false) }
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!catalog || !rack) return
    if (!createOperation.current && catalog.products.some((product) => product.sku.toLocaleLowerCase() === sku.trim().toLocaleLowerCase() && product.id !== editing?.id)) {
      setFormError('Этот код уже используется, в том числе среди архивных товаров.'); return
    }
    const product: Product = { id: editing?.id ?? 0, name: name.trim(), sku: sku.trim(), categoryId: Number(rack), quantity: editing?.quantity ?? 0 }
    if (!product.name || !product.sku) return
    if (await saveProduct(product, editing === null)) { setEditing(null); setName(''); setSku(''); setReceivingQuantity('0'); createOperation.current = null }
  }

  const products = (catalog?.products ?? []).filter((product) => Boolean(product.archived) === showArchived
    && (category === 'all' || product.categoryId === Number(category))
    && `${product.name} ${product.sku}`.toLocaleLowerCase().includes(query.toLocaleLowerCase().trim()))

  return <section className="receivingPage__panel productCatalog ds-v1 ui-v1-window" id="receiving-catalog" aria-labelledby="product-catalog-title">
    <div className="productCatalog__header"><div>
      <h2 className="receivingPage__panelTitle ds-v1 ui-v1-page-title" id="product-catalog-title">ТОВАРЫ И КОДЫ</h2>
      <p className="productCatalog__hint ds-v1 ui-v1-type-secondary">Справочник товаров, кодов и стеллажей</p>
    </div></div>
    {error && <p role="alert" className="ds-v1 ui-v1-feedback ui-v1-feedback--error">{error} <button className="receivingHistory__button ds-v1 ui-v1-button" onClick={() => { setError(''); setAttempt((value) => value + 1) }}>Повторить</button></p>}
    <div className="productCatalog__layout">
      <div style={{ height: formHeight }}><div className="productCatalog__filters">
        <input className="ds-v1 ui-v1-input" aria-label="Поиск товара или кода" placeholder="Найти товар или код…" value={query} onChange={(event) => setQuery(event.target.value)} />
        <select className="ds-v1 ui-v1-input ui-v1-select ui-v1-select--customizable" aria-label="Фильтр по стеллажу" value={category} onChange={(event) => setCategory(event.target.value)}><option value="all">Все стеллажи</option>{catalog?.categories.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
      </div><div className="productCatalog__tabs">
          <button className={!showArchived ? 'productCatalog__tab--active' : ''} onClick={() => setShowArchived(false)}>Активные</button>
          <button className={showArchived ? 'productCatalog__tab--active' : ''} onClick={() => setShowArchived(true)}>Архив</button>
          <span className="ds-v1 ui-v1-type-meta">Найдено: {products.length}</span>
        </div>
        <div className="productCatalog__tableScroll"><table className="productCatalog__table ds-v1 ui-v1-table"><thead><tr><th>Товар / код</th><th>Стеллаж</th><th>Остаток</th><th aria-label="Действия" /></tr></thead><tbody>
          {products.map((product) => <tr key={product.id}><td>{product.name}<span className="receivingHistory__sku">{product.sku}</span></td><td>{catalog?.categories.find((item) => item.id === product.categoryId)?.name}</td><td>{product.quantity} ед.</td><td><div className="receivingHistory__rowActions">
            {!product.archived && <button type="button" className="receivingHistory__button receivingHistory__iconButton ds-v1 ui-v1-button ui-v1-icon-button" disabled={disabled || saving || sessionId === null} title="Добавить в приёмку" aria-label={`Добавить ${product.sku} в приёмку`} onClick={() => setReceivingProduct(product)}>+</button>}
            {!product.archived && <button className="receivingHistory__button receivingHistory__iconButton ds-v1 ui-v1-button ui-v1-icon-button" aria-label={`Редактировать ${product.sku}`} title="Редактировать" onClick={() => { setEditing(product); setName(product.name); setSku(product.sku); setRack(String(product.categoryId)); setNotice(''); nameRef.current?.focus() }}><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m16 3 5 5-12 12-6 1 1-6Z M13 6l5 5" /></svg></button>}
            <button className="receivingHistory__button receivingHistory__iconButton ds-v1 ui-v1-button ui-v1-icon-button" disabled={saving} aria-label={`${product.archived ? 'Восстановить' : 'Архивировать'} ${product.sku}`} title={product.archived ? 'Восстановить' : 'В архив'} onClick={() => {
              if (product.archived) { void saveProduct({ ...product, archived: false }) }
              else { setArchiveError(''); setPendingArchive(product) }
            }}><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={product.archived ? 'M4 10a8 8 0 1 1 1 8M4 4v6h6' : 'M3 3h18v5H3zM5 8v13h14V8M10 12h4'} /></svg></button>
          </div></td></tr>)}
          {!products.length && <tr><td className="ds-v1 ui-v1-type-secondary" colSpan={4}>{catalog ? 'Товары не найдены' : 'Загружаю каталог…'}</td></tr>}
        </tbody></table></div></div>
      <form ref={formRef} className="productCatalog__form ds-v1 ui-v1-control-panel" onSubmit={submit}><h3 className="ds-v1 ui-v1-type-section">{editing ? 'РЕДАКТИРОВАНИЕ ТОВАРА' : 'НОВЫЙ ТОВАР'}</h3>
        <label><span className="ds-v1 ui-v1-type-secondary">Название</span><input className="ds-v1 ui-v1-input" ref={nameRef} required maxLength={200} placeholder="Например, Ручка TREMOR Classic" value={name} onChange={(event) => setName(event.target.value)} /></label>
        <label><span className="ds-v1 ui-v1-type-secondary">Код / артикул</span><input className="ds-v1 ui-v1-input" required maxLength={128} placeholder="TREMOR-CLASSIC" value={sku} onChange={(event) => setSku(event.target.value)} /></label>
        <label><span className="ds-v1 ui-v1-type-secondary">Стеллаж</span><select className="ds-v1 ui-v1-input ui-v1-select ui-v1-select--customizable" required value={rack} onChange={(event) => setRack(event.target.value)}><option value="" disabled>Выберите стеллаж</option>{catalog?.categories.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        {!editing && <label><span className="ds-v1 ui-v1-type-secondary">Количество к приёмке</span><input className="ds-v1 ui-v1-input" type="number" required min="0" max="1000000000" step="1" value={receivingQuantity} onChange={(event) => setReceivingQuantity(event.target.value)} /><small className="ds-v1 ui-v1-type-meta">0 — сохранить только код. Больше 0 — добавить в текущую приёмку.</small></label>}
        <div className="productCatalog__formError" aria-live="polite">{formError && <p role="alert" className="ds-v1 ui-v1-feedback ui-v1-feedback--error"><span>ОШИБКА</span>: {formError}</p>}</div>
        <button className="receivingHistory__button ds-v1 ui-v1-action ui-v1-action--primary" disabled={!catalog || saving} aria-busy={saving}>{saving ? 'Сохраняю…' : editing ? 'Сохранить изменения' : '+ Добавить товар'}</button>
        {editing && <button type="button" className="receivingHistory__button ds-v1 ui-v1-action ui-v1-action--negative" onClick={resetForm}>Отмена</button>}
      </form>
    </div>{notice && <p className="productCatalog__notice ds-v1 ui-v1-feedback ui-v1-feedback--success" role="status">{notice}</p>}
    {receivingProduct && sessionId !== null && <ReceivingQuantityDialog sessionId={sessionId} product={{ id: receivingProduct.id, name: receivingProduct.name, sku: receivingProduct.sku }} onClose={() => setReceivingProduct(null)} onSaved={onReceivingChanged} />}
    {pendingArchive && <dialog ref={dialogRef} className="receivingHistory__dialog ds-v1 ui-v1-modal ui-v1-modal--confirmation" aria-labelledby="archive-product-title" onCancel={(event) => { event.preventDefault(); if (document.documentElement.dataset.theme === 'dark') return; setPendingArchive(null) }}>
      <h2 id="archive-product-title">Перенести товар в архив?</h2>
      <div className="ui-v1-modal__body"><p>{pendingArchive.name} · {pendingArchive.sku}</p><p>Остаток: {pendingArchive.quantity} ед. Количество и история сохранятся. Товар будет скрыт из рабочих списков и недоступен для приёмки и сборки до восстановления.</p>
      {archiveError && <p role="alert" className="ds-v1 ui-v1-feedback ui-v1-feedback--error"><span className="productCatalog__errorLabel">ОШИБКА</span>: {archiveError}</p>}
      </div>
      <div className="receivingHistory__dialogActions"><button className="receivingHistory__button ds-v1 ui-v1-button" disabled={saving} autoFocus onClick={() => setPendingArchive(null)}>Отмена</button><button className="receivingHistory__button ds-v1 ui-v1-button ui-v1-button--destructive" disabled={saving} aria-busy={saving} onClick={() => { void saveProduct({ ...pendingArchive, archived: true }, false, 'archive').then((saved) => { if (!saved) return; if (editing?.id === pendingArchive.id) { setEditing(null); setName(''); setSku('') }; markArchiveConfirmed(); setPendingArchive(null) }) }}>В архив</button></div>
    </dialog>}
  </section>
}
