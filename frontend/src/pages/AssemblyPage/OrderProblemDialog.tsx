import { useEffect, useRef, useState, type FormEvent } from 'react'
import { problemTypes, allProblemTypes } from './problemTypes'

export type OrderProblemType = typeof allProblemTypes[number][0]
export type OrderProblemDetails = { type: OrderProblemType; comment: string; positionSku?: string }
export type OrderShortage = { sku: string; name: string; required: number; available: number; missing: number; cells: string }
export type ProblemResolutionStatus = 'awaiting' | 'resolved' | 'returned'
export type ProblemCreationContext = { schemaVersion: 1; complete: boolean; source: 'queue' | 'assembly'; positions: Array<{ sku: string; name?: string; required?: number; picked?: number; remaining?: number; available?: number; missing?: number; orderPositionId: string }>; affectedPositions: Array<{ sku: string }>; confirmedCell?: string | null; suggestedCell?: string | null; scanStep?: 'cell' | 'product' | 'shipment'; picked?: number; required?: number }
export type ProblemHistoryEvent = { createdAt: string; title: string; message: string; outcome: 'success' | 'failure' | 'error' | 'neutral'; kind?: 'creation' | 'transfer' | 'check' | 'result' | 'resolution' | 'return' }
export type SavedOrderProblem = OrderProblemDetails & { shortages: OrderShortage[]; createdAt: string; resolutionStatus?: ProblemResolutionStatus; returnedAt?: string; history?: ProblemHistoryEvent[]; requiresDisassembly?: boolean; creationContext?: ProblemCreationContext }

export default function OrderProblemDialog({ initialType = '', positions = [], initialPositionSku = '', blocked = false, onCancel, onConfirm }: {
  initialType?: OrderProblemType | ''
  positions?: Array<{ sku: string; name: string }>
  initialPositionSku?: string
  blocked?: boolean
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
    if (!type || blocked || (type === 'other' && !comment.trim())) return
    if (positions.length > 0 && positionRequired && !positionSku) return
    onConfirm({ type, comment: type === 'other' ? comment.trim() : '', ...(positions.length > 0 && positionSku ? { positionSku } : {}) })
  }
  return <dialog ref={dialogRef} className="receivingHistory__dialog assemblyProblemDialog" aria-labelledby="order-problem-title" aria-describedby="order-problem-description" onCancel={(event) => { event.preventDefault(); onCancel() }}>
    <form onSubmit={submit}>
      <h2 id="order-problem-title">Отправить заказ в проблемные?</h2>
      <p id="order-problem-description">Данный заказ будет отправлен в раздел «Проблемные».</p>
      <label className="assemblyProblemDialog__field">Тип проблемы
        <select value={type} required autoFocus disabled={blocked} onChange={(event) => setType(event.target.value as OrderProblemType | '')}>
          <option value="" disabled>Выбрать</option>
          {problemTypes.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
      </label>
      {type === 'other' && <label className="assemblyProblemDialog__field">Комментарий
        <textarea value={comment} required disabled={blocked} rows={3} maxLength={2000} onChange={(event) => setComment(event.target.value)} />
      </label>}
      {positions.length > 0 && type && <label className="assemblyProblemDialog__field">Позиция{!positionRequired && ' (необязательно)'}
        <select value={positionSku} required={positionRequired} disabled={blocked} onChange={(event) => setPositionSku(event.target.value)}>
          <option value="">{positionRequired ? 'Выбрать позицию' : 'Заказ целиком'}</option>
          {positions.map((position) => <option key={position.sku} value={position.sku}>{position.name} · {position.sku}</option>)}
        </select>
      </label>}
      <div className="receivingHistory__dialogActions">
        <button className="receivingHistory__button" type="button" onClick={onCancel}>Отмена</button>
        <button className="receivingHistory__button assemblyProblemDialog__confirm" type="submit" disabled={blocked || !type || (positionRequired && !positionSku) || (type === 'other' && !comment.trim())}>Отправить в проблемные</button>
      </div>
    </form>
  </dialog>
}
