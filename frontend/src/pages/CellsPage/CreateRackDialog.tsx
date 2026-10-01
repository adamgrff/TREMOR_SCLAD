import { useEffect, useRef, useState, type FormEvent } from 'react'

type Rack = { id: number; name: string; rows: string[][] }

export default function CreateRackDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (rack: Rack) => void }) {
  const dialogRef = useRef<HTMLDialogElement>(null)
  const inFlight = useRef(false)
  const [name, setName] = useState('')
  const [counts, setCounts] = useState(['3', '3', '3'])
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    const dialog = dialogRef.current
    dialog?.showModal()
    return () => dialog?.close()
  }, [])
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (inFlight.current) return
    const quantities = counts.map(Number)
    if (!name.trim() || counts.some((value) => value === '') || quantities.some((value) => !Number.isInteger(value) || value < 0 || value > 100) || quantities.every((value) => value === 0)) {
      setError('Укажите название и от 0 до 100 ячеек в каждой строке. Нужна хотя бы одна ячейка.')
      return
    }
    inFlight.current = true
    setSaving(true)
    setError('')
    try {
      const response = await fetch('http://127.0.0.1:8080/api/racks', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: name.trim(), counts: quantities }) })
      if (!response.ok) {
        const data = await response.json() as { error?: string }
        throw new Error(data.error || 'Не удалось создать стеллаж')
      }
      onCreated(await response.json() as Rack)
    } catch (reason) { setError(reason instanceof Error ? reason.message : 'Ошибка соединения. Проверьте список перед повторной отправкой.') }
    finally { inFlight.current = false; setSaving(false) }
  }
  return <dialog ref={dialogRef} className="cellsPage__rackDialog" aria-labelledby="create-rack-title" onCancel={(event) => { event.preventDefault(); if (!saving) onClose() }}>
    <form onSubmit={submit}>
      <h2 id="create-rack-title">НОВЫЙ СТЕЛЛАЖ</h2>
      <label>Название стеллажа<input autoFocus required maxLength={50} value={name} disabled={saving} onChange={(event) => setName(event.target.value)} /></label>
      <div className="cellsPage__rackCounts">{['A', 'B', 'C'].map((row, index) => <label key={row}>Ячеек в строке {row}<input type="number" required min={0} max={100} step={1} value={counts[index]} disabled={saving} onChange={(event) => setCounts((values) => values.map((value, i) => i === index ? event.target.value : value))} /></label>)}</div>
      <p className="cellsPage__rackHint">Коды ячеек: A1, A2… / B1, B2… / C1, C2… Ноль оставит строку пустой.</p>
      <p className="cellsPage__rackError" role="alert">{error && <><span>ОШИБКА: </span>{error}</>}</p>
      <div className="cellsPage__rackDialogActions"><button type="button" disabled={saving} onClick={onClose}>ОТМЕНА</button><button type="submit" disabled={saving}>{saving ? 'СОХРАНЯЮ…' : 'СОЗДАТЬ'}</button></div>
    </form>
  </dialog>
}
