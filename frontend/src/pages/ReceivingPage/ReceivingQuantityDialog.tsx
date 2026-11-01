import useModalFocusReturn from '../../components/useModalFocusReturn'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { isReceivingDeployment, ReceivingDeploymentBlocked, sendReceivingAction } from './receivingScanCommands'
import { API_BASE_URL } from '../../config/api'

export default function ReceivingQuantityDialog({ sessionId, deploymentId, product, onClose, onSaved }: {
  sessionId: number; product: { id?: number; sku: string; name: string; quantity?: number }
  deploymentId: string | null
  onClose: () => void; onSaved: () => Promise<void>
}) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  useModalFocusReturn(dialogRef)
  const [quantity, setQuantity] = useState(String(product.quantity ?? 1))
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const inFlight = useRef(false)
  useEffect(() => { const dialog = dialogRef.current; dialog?.showModal(); return () => dialog?.close() }, [])
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (inFlight.current) return
    inFlight.current = true; setSaving(true); setError('')
    try {
      if (!isReceivingDeployment(deploymentId)) throw new ReceivingDeploymentBlocked('Источник данных не подтверждён. Обновите приёмку; разрешён только просмотр.')
      let id = product.id
      if (!id) {
        const lookup = await fetch(`${API_BASE_URL}/product-catalog`, { cache: 'no-store' })
        if (!lookup.ok) throw new Error('Товар недоступен. Обновите приёмку.')
        const catalog = await lookup.json() as { deploymentId?: string; products: Array<{ id: number; sku: string }> }
        if (catalog.deploymentId !== deploymentId) throw new ReceivingDeploymentBlocked('Развёртывание изменилось. Операция не отправлена; обновите приёмку.')
        id = catalog.products.find(item => item.sku === product.sku)?.id
        if (!id) throw new Error('Товар недоступен. Обновите приёмку.')
      }
      const data = { quantity: Number(quantity), expectedQuantity: product.quantity ?? 0 }
      await sendReceivingAction({ kind: 'quantity', sessionId, productId: id!, method: product.quantity === undefined ? 'POST' : 'PUT', ...data }, deploymentId)
      await onSaved(); onClose()
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Не удалось сохранить количество. Повторите с теми же данными.') }
    finally { inFlight.current = false; setSaving(false) }
  }
  return <dialog ref={dialogRef} className="receivingHistory__dialog ds-v1 ui-v1-modal ui-v1-modal--form" aria-labelledby="quantity-dialog-title" onCancel={(event) => { event.preventDefault(); if (document.documentElement.dataset.theme === 'dark') return; if (!saving) onClose() }}>
    <form onSubmit={(event) => void submit(event)}>
    <h2 id="quantity-dialog-title">{product.quantity === undefined ? 'Добавить в приёмку' : 'Изменить ожидающее количество'}</h2>
      <div className="ui-v1-modal__body">
    <p>{product.name} · {product.sku}</p>

      <label>Количество <input className="ds-v1 ui-v1-input" autoFocus type="number" required min="1" max="1000000000" step="1" value={quantity} disabled={saving} onChange={(event) => setQuantity(event.target.value)} /></label>
      <p>Меняется только количество, ожидающее ячейку. Размещённые остатки не изменятся.</p>
      {error && <p role="alert" className="ds-v1 ui-v1-feedback ui-v1-feedback--error"><span className="productCatalog__errorLabel">ОШИБКА</span>: {error}</p>}
      </div>
      <div className="receivingHistory__dialogActions"><button type="button" className="receivingHistory__button ds-v1 ui-v1-button" disabled={saving} onClick={onClose}>Отмена</button><button className="receivingHistory__button ds-v1 ui-v1-button" disabled={saving || !isReceivingDeployment(deploymentId)} aria-busy={saving}>{saving ? 'Сохраняю…' : 'Сохранить'}</button></div>
    </form>
  </dialog>
}
