import useModalFocusReturn from '../../components/useModalFocusReturn'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { API_BASE_URL } from '../../config/api'
import type { AssemblyPreviewOrder } from './ActiveAssembly'
import type { SavedOrderProblem } from './OrderProblemDialog'

type Product = { id: number; sku: string; name: string }
export default function SkuMappingDialog({ order, problem, blocked, onCancel, onConfirm }: {
  order: AssemblyPreviewOrder; problem: SavedOrderProblem; blocked: boolean
  onCancel: () => void; onConfirm: (sku: string, productId: number) => Promise<boolean>
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useModalFocusReturn(ref)
  const positions = order.items.filter(item => problem.creationContext?.affectedPositions.some(p => p.sku === item.sku))
  const [sku, setSku] = useState(positions.find(item => !item.productId)?.sku ?? positions[0]?.sku ?? '')
  const [products, setProducts] = useState<Product[]>([])
  const [productId, setProductId] = useState('')
  const [query, setQuery] = useState('')
  const [confirmed, setConfirmed] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const product = products.find(p => p.id === Number(productId))
  const search = query.trim().toLocaleLowerCase('ru-RU')
  const filtered = products.filter(p => !search || `${p.name} ${p.sku}`.toLocaleLowerCase('ru-RU').includes(search) || p.id === Number(productId))
  useEffect(() => {
    ref.current?.showModal()
    const controller = new AbortController()
    void fetch(`${API_BASE_URL}/assembly/mapping-products`, { signal: controller.signal, cache: 'no-store' }).then(async response => {
      if (!response.ok) throw new Error('Не удалось загрузить складские товары. Закройте окно и повторите попытку.')
      const data = await response.json() as { products: Product[] }
      if (!controller.signal.aborted) setProducts(data.products)
    }).catch((reason: unknown) => { if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : 'Не удалось загрузить товары.') })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    const dialog = ref.current
    return () => { controller.abort(); dialog?.close() }
  }, [])
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (blocked || loading || !product || !sku || !confirmed) return
    setError('')
    if (!await onConfirm(sku, product.id)) setError('Сопоставление не подтверждено. Проверьте сообщение сервера над окнами страницы; при обрыве связи нажмите «Повторить» там же.')
  }
  return <dialog ref={ref} className="receivingHistory__dialog assemblyProblemDialog ds-v1 ui-v1-modal ui-v1-modal--form" aria-labelledby="sku-mapping-title" onCancel={event => { event.preventDefault(); if (document.documentElement.dataset.theme === 'dark') return; if (!blocked) onCancel() }}>
    <form onSubmit={event => void submit(event)}>
      <h2 id="sku-mapping-title">Сопоставить товар</h2>
      <div className="ui-v1-modal__body">
      <p>Заказ {order.number}. Выберите соответствующий складской товар. После сохранения сервер проверит остатки и остальные проблемы заказа.</p>
      <label className="assemblyProblemDialog__field">Позиция Ozon
        <select className="ds-v1 ui-v1-input ui-v1-select" value={sku} disabled={blocked} required onChange={event => { setSku(event.target.value); setConfirmed(false) }}>
          {positions.map(item => <option value={item.sku} key={item.sku}>{item.name} · {item.sku}{item.ozonSku ? ` · SKU Ozon: ${item.ozonSku}` : ''}</option>)}
        </select>
      </label>
      <label className="assemblyProblemDialog__field">Поиск складского товара
        <input className="ds-v1 ui-v1-input" type="search" autoFocus value={query} disabled={blocked || loading} placeholder="Название или SKU" onChange={event => setQuery(event.target.value)} />
      </label>
      <label className="assemblyProblemDialog__field">Складской товар
        <select className="ds-v1 ui-v1-input ui-v1-select" value={productId} required disabled={blocked || loading} onChange={event => { setProductId(event.target.value); setConfirmed(false) }}>
          <option value="">{loading ? 'Загрузка…' : 'Выбрать товар'}</option>
          {filtered.map(p => <option value={p.id} key={p.id}>{p.name} · {p.sku}</option>)}
        </select>
      </label>
      {product && <p>{product.name} · {product.sku}</p>}
      <label className="skuMapping__conformity"><input type="checkbox" checked={confirmed} disabled={blocked || !product} onChange={event => setConfirmed(event.target.checked)} /> Я проверил соответствие позиции Ozon выбранному товару.</label>
      {error && <p role="alert" className="ds-v1 ui-v1-feedback ui-v1-feedback--error">{error}</p>}
      </div>
      <div className="receivingHistory__dialogActions">
        <button type="button" className="receivingHistory__button ds-v1 ui-v1-button" disabled={blocked} onClick={onCancel}>Отмена</button>
        <button type="submit" className="receivingHistory__button skuMapping__confirm ds-v1 ui-v1-button" disabled={blocked || loading || !product || !sku || !confirmed}>Сохранить и проверить</button>
      </div>
    </form>
  </dialog>
}
