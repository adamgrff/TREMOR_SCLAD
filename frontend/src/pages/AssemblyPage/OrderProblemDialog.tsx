import { useEffect, useRef, useState, type FormEvent } from 'react'

const problemTypes = [
  ['insufficient_stock', 'Недостаточно товара'],
  ['missing_in_cell', 'Товар отсутствует в ячейке'],
  ['unknown_sku', 'Неизвестный товар / SKU'],
  ['damaged_product', 'Повреждённый товар'],
  ['invalid_order', 'Ошибка данных заказа'],
  ['other', 'Другая проблема'],
] as const

export type OrderProblemType = typeof problemTypes[number][0]
export type OrderProblemDetails = { type: OrderProblemType; comment: string }
export type OrderShortage = { sku: string; name: string; required: number; available: number; missing: number; cells: string }
export type SavedOrderProblem = OrderProblemDetails & { shortages: OrderShortage[]; createdAt: string }

export default function OrderProblemDialog({ initialType = '', onCancel, onConfirm }: {
  initialType?: OrderProblemType | ''
  onCancel: () => void
  onConfirm: (details: OrderProblemDetails) => void
}) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const [type, setType] = useState<OrderProblemType | ''>(initialType)
  const [comment, setComment] = useState('')
  useEffect(() => {
    const dialog = dialogRef.current
    dialog?.showModal()
    return () => dialog?.close()
  }, [])
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!type) return
    onConfirm({ type, comment: type === 'other' ? comment.trim() : '' })
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
      <div className="receivingHistory__dialogActions">
        <button className="receivingHistory__button" type="button" onClick={onCancel}>Отмена</button>
        <button className="receivingHistory__button assemblyProblemDialog__confirm" type="submit" disabled={!type}>Отправить в проблемные</button>
      </div>
    </form>
  </dialog>
}
