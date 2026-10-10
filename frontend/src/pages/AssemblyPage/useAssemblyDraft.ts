import { useEffect, useRef, useState } from 'react'
import { API_BASE_URL } from '../../config/api'

type Response<T> = { version: number; state: T | null }
export default function useAssemblyDraft<T>(initialState: T) {
  const [state, setState] = useState(initialState)
  const [ready, setReady] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const version = useRef(0)
  const inFlight = useRef(false)
  const pending = useRef<{ requestId: string; version: number; state: T } | null>(null)

  async function load(signal?: AbortSignal) {
    const response = await fetch(`${API_BASE_URL}/assembly-preview/draft`, { signal, cache: 'no-store' })
    if (!response.ok) throw new Error('Не удалось загрузить сборку. Проверьте backend и миграцию 011.')
    const data = await response.json() as Response<T>
    if (signal?.aborted) return
    version.current = data.version
    setState(data.state ?? initialState)
    setReady(true)
  }
  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal).catch((reason: unknown) => {
      if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : 'Не удалось загрузить сборку.')
    })
    return () => controller.abort()
    // The initial fixture is used only when PostgreSQL has no saved draft.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function save(next: T): Promise<boolean> {
    if (!ready || inFlight.current || pending.current) return false
    pending.current = { requestId: crypto.randomUUID(), version: version.current, state: next }
    return retry()
  }
  async function retry(): Promise<boolean> {
    if (inFlight.current) return false
    if (!pending.current) {
      try { setError(''); await load(); return true }
      catch (reason) { setError(reason instanceof Error ? reason.message : 'Не удалось загрузить сборку.'); return false }
    }
    inFlight.current = true
    setSaving(true)
    setError('')
    try {
      const response = await fetch(`${API_BASE_URL}/assembly-preview/draft`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(pending.current) })
      if (response.status === 409) {
        pending.current = null
        await load()
        throw new Error('Сборка изменена в другом окне. Загружено актуальное состояние; повторите действие.')
      }
      if (!response.ok) throw new Error('Сборка не сохранена. Проверьте backend и нажмите «Повторить».')
      const data = await response.json() as Response<T>
      version.current = data.version
      setState(data.state ?? initialState)
      pending.current = null
      return true
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Связь с backend прервалась. Нажмите «Повторить».')
      return false
    } finally { inFlight.current = false; setSaving(false) }
  }
  return { state, save, ready, saving, error, retry, blocked: !ready || saving || error !== '' }
}
