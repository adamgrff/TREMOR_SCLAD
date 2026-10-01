import { useEffect, useRef, useState, type FormEvent } from 'react'

export default function ReceivingQuantityDialog({ sessionId, product, onClose, onSaved }: {
  sessionId: number; product: { id?: number; sku: string; name: string; quantity?: number }
  onClose: () => void; onSaved: () => Promise<void>
}) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const [quantity, setQuantity] = useState(String(product.quantity ?? 1))
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const inFlight = useRef(false)
  const operation = useRef<{ payload: string; token: string } | null>(null)
  useEffect(() => { const dialog = dialogRef.current; dialog?.showModal(); return () => dialog?.close() }, [])
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (inFlight.current) return
    inFlight.current = true; setSaving(true); setError('')
    try {
      let id = product.id
      if (!id) {
        const lookup = await fetch(`http://127.0.0.1:8080/api/products/${encodeURIComponent(product.sku)}`)
        if (!lookup.ok) throw new Error('Товар недоступен. Обновите приёмку.')
        id = Number((await lookup.json() as { id: string }).id)
      }
      const payload = JSON.stringify({ quantity: Number(quantity), expectedQuantity: product.quantity ?? 0 })
      if (operation.current?.payload !== payload) operation.current = { payload, token: crypto.randomUUID() }
      const response = await fetch(`http://127.0.0.1:8080/api/receiving/sessions/${sessionId}/quantities/${id}`, {
        method: product.quantity === undefined ? 'POST' : 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...JSON.parse(payload), requestId: operation.current.token }),
      })
      if (!response.ok) throw new Error((await response.json() as { error: string }).error)
      await onSaved(); onClose()
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Не удалось сохранить количество. Повторите с теми же данными.') }
    finally { inFlight.current = false; setSaving(false) }
  }
  return <dialog ref={dialogRef} className="receivingHistory__dialog" aria-labelledby="quantity-dialog-title" onCancel={(event) => { event.preventDefault(); if (!saving) onClose() }}>
    <h2 id="quantity-dialog-title">{product.quantity === undefined ? 'Добавить в приёмку' : 'Изменить ожидающее количество'}</h2>
    <p>{product.name} · {product.sku}</p>
    <form onSubmit={(event) => void submit(event)}>
      <label>Количество <input autoFocus type="number" required min="1" max="1000000000" step="1" value={quantity} disabled={saving} onChange={(event) => setQuantity(event.target.value)} /></label>
      <p>Меняется только количество, ожидающее ячейку. Размещённые остатки не изменятся.</p>
      {error && <p role="alert"><span className="productCatalog__errorLabel">ОШИБКА</span>: {error}</p>}
      <div className="receivingHistory__dialogActions"><button type="button" className="receivingHistory__button" disabled={saving} onClick={onClose}>Отмена</button><button className="receivingHistory__button" disabled={saving}>{saving ? 'Сохраняю…' : 'Сохранить'}</button></div>
    </form>
  </dialog>
}
