import { useEffect, useRef, useState, type FormEvent } from 'react'
import './ProductCatalog.css'

type Category = { id: number; name: string }
type Product = { id: number; categoryId: number; name: string; sku: string; quantity: number; archived?: boolean }
type Catalog = { categories: Category[]; products: Product[] }

export default function ProductCatalog() {
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [query, setQuery] = useState('')
  const [category, setCategory] = useState('all')
  const [showArchived, setShowArchived] = useState(false)
  const [editing, setEditing] = useState<Product | null>(null)
  const [name, setName] = useState('')
  const [sku, setSku] = useState('')
  const [rack, setRack] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [pendingArchive, setPendingArchive] = useState<Product | null>(null)
  const dialogRef = useRef<HTMLDialogElement>(null)
  const nameRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const controller = new AbortController()
    void fetch('http://127.0.0.1:8080/api/product-catalog', { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error('Не удалось загрузить каталог')
        const data = await response.json() as Catalog
        if (!controller.signal.aborted) { setCatalog(data); setRack(String(data.categories[0]?.id ?? '')) }
      })
      .catch(() => { if (!controller.signal.aborted) setError('Не удалось загрузить каталог товаров. Проверьте подключение.') })
    return () => controller.abort()
  }, [attempt])

  useEffect(() => {
    if (!pendingArchive) return
    const dialog = dialogRef.current
    dialog?.showModal()
    return () => dialog?.close()
  }, [pendingArchive])

  function resetForm() { setEditing(null); setName(''); setSku(''); setNotice('') }
  function submit(event: FormEvent) {
    event.preventDefault()
    if (!catalog || !rack) return
    if (catalog.products.some((product) => product.sku.toLocaleLowerCase() === sku.trim().toLocaleLowerCase() && product.id !== editing?.id)) {
      setNotice('Этот код уже используется, в том числе среди архивных товаров.'); return
    }
    const product: Product = { id: editing?.id ?? -Date.now(), name: name.trim(), sku: sku.trim(), categoryId: Number(rack), quantity: editing?.quantity ?? 0 }
    if (!product.name || !product.sku) return
    setCatalog({ ...catalog, products: editing ? catalog.products.map((item) => item.id === editing.id ? product : item) : [...catalog.products, product] })
    setEditing(null); setName(''); setSku(''); setNotice('Предпросмотр обновлён. Изменения не записаны в базу и исчезнут при перезагрузке страницы.')
  }

  const products = (catalog?.products ?? []).filter((product) => Boolean(product.archived) === showArchived
    && (category === 'all' || product.categoryId === Number(category))
    && `${product.name} ${product.sku}`.toLocaleLowerCase().includes(query.toLocaleLowerCase().trim()))

  return <section className="receivingPage__panel productCatalog" aria-labelledby="product-catalog-title">
    <div className="productCatalog__header"><div>
      <h2 className="receivingPage__panelTitle" id="product-catalog-title">ТОВАРЫ И КОДЫ</h2>
      <p className="productCatalog__hint">Справочник товаров, кодов и стеллажей</p>
    </div><span className="productCatalog__badge">ПРЕДПРОСМОТР</span></div>
    <p className="productCatalog__preview">Список загружен из базы. Изменения ниже — только пример работы интерфейса, без сохранения.</p>
    {error && <p role="alert">{error} <button className="receivingHistory__button" onClick={() => { setError(''); setAttempt((value) => value + 1) }}>Повторить</button></p>}
    <div className="productCatalog__layout">
      <div><div className="productCatalog__filters">
        <input aria-label="Поиск товара или кода" placeholder="Найти товар или код…" value={query} onChange={(event) => setQuery(event.target.value)} />
        <select aria-label="Фильтр по стеллажу" value={category} onChange={(event) => setCategory(event.target.value)}><option value="all">Все стеллажи</option>{catalog?.categories.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
      </div><div className="productCatalog__tabs">
        <button className={!showArchived ? 'productCatalog__tab--active' : ''} onClick={() => setShowArchived(false)}>Активные</button>
        <button className={showArchived ? 'productCatalog__tab--active' : ''} onClick={() => setShowArchived(true)}>Архив</button>
        <span>Найдено: {products.length}</span>
      </div>
      <div className="productCatalog__tableScroll"><table className="productCatalog__table"><thead><tr><th>Товар / код</th><th>Стеллаж</th><th>Остаток</th><th aria-label="Действия" /></tr></thead><tbody>
        {products.map((product) => <tr key={product.id}><td>{product.name}<span className="receivingHistory__sku">{product.sku}</span></td><td>{catalog?.categories.find((item) => item.id === product.categoryId)?.name}</td><td>{product.quantity} ед.</td><td><div className="receivingHistory__rowActions">
          {!product.archived && <button className="receivingHistory__button receivingHistory__iconButton" aria-label={`Редактировать ${product.sku}`} title="Редактировать" onClick={() => { setEditing(product); setName(product.name); setSku(product.sku); setRack(String(product.categoryId)); setNotice(''); nameRef.current?.focus() }}><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m16 3 5 5-12 12-6 1 1-6Z M13 6l5 5" /></svg></button>}
          <button className="receivingHistory__button receivingHistory__iconButton" disabled={!product.archived && product.quantity > 0} aria-label={`${product.archived ? 'Восстановить' : 'Архивировать'} ${product.sku}`} title={product.quantity > 0 ? 'Нельзя архивировать товар с остатком' : product.archived ? 'Восстановить' : 'В архив'} onClick={() => {
            if (product.archived && catalog) { setCatalog({ ...catalog, products: catalog.products.map((item) => item.id === product.id ? { ...item, archived: false } : item) }); setNotice('Товар восстановлен в предпросмотре, база не изменена.') }
            else setPendingArchive(product)
          }}><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={product.archived ? 'M4 10a8 8 0 1 1 1 8M4 4v6h6' : 'M3 3h18v5H3zM5 8v13h14V8M10 12h4'} /></svg></button>
        </div></td></tr>)}
        {!products.length && <tr><td colSpan={4}>{catalog ? 'Товары не найдены' : 'Загружаю каталог…'}</td></tr>}
      </tbody></table></div></div>
      <form className="productCatalog__form" onSubmit={submit}><h3>{editing ? 'Редактирование товара' : 'Новый товар'}</h3>
        <label>Название<input ref={nameRef} required maxLength={200} placeholder="Например, Ручка TREMOR Classic" value={name} onChange={(event) => setName(event.target.value)} /></label>
        <label>Код / артикул<input required maxLength={128} placeholder="TREMOR-CLASSIC" value={sku} onChange={(event) => setSku(event.target.value)} /></label>
        <label>Стеллаж<select required value={rack} onChange={(event) => setRack(event.target.value)}><option value="" disabled>Выберите стеллаж</option>{catalog?.categories.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        <p>Код должен быть уникальным. Старые товары можно перенести в архив, если их остаток равен нулю.</p>
        <button className="receivingHistory__button" disabled={!catalog}>{editing ? 'Применить в предпросмотре' : '+ Добавить в предпросмотр'}</button>
        {editing && <button type="button" className="receivingHistory__button" onClick={resetForm}>Отмена</button>}
      </form>
    </div>{notice && <p className="productCatalog__notice" role="status">{notice}</p>}
    {pendingArchive && <dialog ref={dialogRef} className="receivingHistory__dialog" aria-labelledby="archive-product-title" onCancel={(event) => { event.preventDefault(); setPendingArchive(null) }}>
      <h2 id="archive-product-title">Перенести товар в архив?</h2><p>{pendingArchive.name} · {pendingArchive.sku}</p><p>Это предпросмотр. Записи в базе и история приёмок не изменятся.</p>
      <div className="receivingHistory__dialogActions"><button className="receivingHistory__button" autoFocus onClick={() => setPendingArchive(null)}>Отмена</button><button className="receivingHistory__button" onClick={() => { if (catalog) setCatalog({ ...catalog, products: catalog.products.map((item) => item.id === pendingArchive.id ? { ...item, archived: true } : item) }); if (editing?.id === pendingArchive.id) resetForm(); setPendingArchive(null); setNotice('Товар перенесён в архив предпросмотра, база не изменена.') }}>В архив</button></div>
    </dialog>}
  </section>
}
