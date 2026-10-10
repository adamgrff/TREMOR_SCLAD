import { useEffect, useRef, useState, type FormEvent } from 'react'
import { problemTypes } from './problemTypes'

export type OrderProblemType = typeof problemTypes[number][0]
export type OrderProblemDetails = { type: OrderProblemType; comment: string; positionSku?: string }
export type OrderShortage = { sku: string; name: string; required: number; available: number; missing: number; cells: string }
export type SavedOrderProblem = OrderProblemDetails & { shortages: OrderShortage[]; createdAt: string }

export default function OrderProblemDialog({ initialType = '', positions = [], initialPositionSku = '', onCancel, onConfirm }: {
  initialType?: OrderProblemType | ''
  positions?: Array<{ sku: string; name: string }>
  initialPositionSku?: string
  onCancel: () => void
  onConfirm: (details: OrderProblemDetails) => void
}) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const [type, setType] = useState<OrderProblemType | ''>(initialType)
  const [comment, setComment] = useState('')
  const [positionSku, setPositionSku] = useState(initialPositionSku)
  const positionRequired = ['insufficient_stock', 'missing_in_cell', 'unknown_sku', 'damaged_product'].includes(type)
  useEffect(() => {
    const dialog = dialogRef.current
    dialog?.showModal()
    return () => dialog?.close()
  }, [])
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!type) return
    if (positions.length > 0 && positionRequired && !positionSku) return
    onConfirm({ type, comment: type === 'other' ? comment.trim() : '', ...(positions.length > 0 && positionSku ? { positionSku } : {}) })
  }
  return <dialog ref={dialogRef} className="receivingHistory__dialog assemblyProblemDialog" aria-labelledby="order-problem-title" aria-describedby="order-problem-description" onCancel={(event) => { event.preventDefault(); onCancel() }}>
    <form onSubmit={submit}>
      <h2 id="order-problem-title">Отправить заказ в проблемные?</h2>
      <p id="order-problem-description">Данный заказ будет отправлен в раздел «Проблемные».</p>
      <label className="assemblyProblemDialog__field">Тип проблемы
        <select value={type} required autoFocus onChange={(event) => setType(event.target.value as OrderProblemType | '')}>
          <option value="" disabled>Выбрать</option>
          {problemTypes.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
      </label>
      {type === 'other' && <label className="assemblyProblemDialog__field">Комментарий
        <textarea value={comment} rows={3} maxLength={2000} onChange={(event) => setComment(event.target.value)} />
      </label>}
      {positions.length > 0 && type && <label className="assemblyProblemDialog__field">Позиция{!positionRequired && ' (необязательно)'}
        <select value={positionSku} required={positionRequired} onChange={(event) => setPositionSku(event.target.value)}>
          <option value="">{positionRequired ? 'Выбрать позицию' : 'Заказ целиком'}</option>
          {positions.map((position) => <option key={position.sku} value={position.sku}>{position.name} · {position.sku}</option>)}
        </select>
      </label>}
      <div className="receivingHistory__dialogActions">
        <button className="receivingHistory__button" type="button" onClick={onCancel}>Отмена</button>
        <button className="receivingHistory__button assemblyProblemDialog__confirm" type="submit" disabled={!type}>Отправить в проблемные</button>
      </div>
    </form>
  </dialog>
}
