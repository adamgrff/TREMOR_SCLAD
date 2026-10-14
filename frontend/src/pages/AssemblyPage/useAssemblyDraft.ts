import { useEffect, useRef, useState } from 'react'
import { API_BASE_URL } from '../../config/api'
import type { AssemblyPreviewOrder } from './ActiveAssembly'
import type { OrderProblemDetails } from './OrderProblemDialog'

type Response<T> = { version: number; state: T | null; message?: string; applied?: boolean }
export default function useAssemblyDraft<T>(initialState: T) {
  const [state, setState] = useState(initialState)
  const [ready, setReady] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [actionNotice, setActionNotice] = useState('')
  const actionMessage = useRef('')
  const version = useRef(0)
  const inFlight = useRef(false)
  const pending = useRef<{ path: string; method: string; body: object; uncertain?: boolean } | null>(null)

  async function load(signal?: AbortSignal) {
    const response = await fetch(`${API_BASE_URL}/assembly-preview/draft`, { signal, cache: 'no-store' })
    if (!response.ok) throw new Error('Не удалось загрузить сборку. Проверьте backend и миграции 011–018.')
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
    pending.current = { path: '/assembly-preview/draft', method: 'PUT', body: { requestId: crypto.randomUUID(), version: version.current, state: next } }
    return retry()
  }
  async function createProblem(intent: { source: 'queue' | 'assembly'; orderNumber: string; order?: AssemblyPreviewOrder; details: OrderProblemDetails }): Promise<boolean> {
    if (!ready || inFlight.current || pending.current) return false
    pending.current = { path: '/assembly/problems', method: 'POST', body: { ...intent, requestId: crypto.randomUUID(), version: version.current } }
    return retry()
  }
  async function problemAction(problemId: string, action: 'check-stock' | 'return' | 'retry-ozon' | 'close-cancelled' | 'transfer-disassembly'): Promise<boolean> {
    if (!ready || inFlight.current || pending.current) return false
    pending.current = { path: `/assembly/problems/${encodeURIComponent(problemId)}/${action}`, method: 'POST', body: { requestId: crypto.randomUUID(), version: version.current } }
    return retry()
  }
  async function assemblyAction(action: 'start' | 'scan' | 'undo' | 'finish', params: { orderNumber?: string; code?: string } = {}): Promise<boolean> {
    if (!ready || inFlight.current || pending.current) return false
    pending.current = { path: `/assembly/${action}`, method: 'POST', body: { ...params, requestId: crypto.randomUUID(), version: version.current } }
    return retry()
  }
  async function mapProduct(problemId: string, positionSku: string, productId: number): Promise<boolean> {
    if (!ready || inFlight.current || pending.current) return false
    pending.current = { path: `/assembly/problems/${encodeURIComponent(problemId)}/map-product`, method: 'POST', body: { requestId: crypto.randomUUID(), version: version.current, positionSku, productId, confirmed: true } }
    return retry()
  }
  async function disassemblyAction(sessionId: string, action: 'start' | 'scan' | 'finish' | 'pause', code?: string): Promise<boolean> {
    if (!ready || inFlight.current || pending.current) return false
    pending.current = { path: `/assembly/disassembly/${encodeURIComponent(sessionId)}/${action}`, method: 'POST', body: { requestId: crypto.randomUUID(), version: version.current, ...(code ? { code } : {}) } }
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
    setActionNotice('')
    actionMessage.current = ''
    const reconcile = pending.current.uncertain === true
    try {
      const response = await fetch(`${API_BASE_URL}${pending.current.path}`, { method: pending.current.method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(pending.current.body) })
      if (response.status === 409) {
        const conflict = await response.json() as { error?: string }
        pending.current = null
        await load()
        throw new Error(conflict.error ?? 'Сборка изменена в другом окне. Загружено актуальное состояние; повторите действие.')
      }
      if (response.status === 400) {
        const rejected = await response.json() as { error?: string }
        pending.current = null
        throw new Error(rejected.error ?? 'Проверьте параметры проблемы.')
      }
      if (!response.ok) throw new Error('Сборка не сохранена. Проверьте backend и нажмите «Повторить».')
      const data = await response.json() as Response<T>
      // A replay returns the original receipt, which can predate another tab's work.
      // Reconcile an uncertain request before unblocking further warehouse actions.
      if (reconcile) await load()
      else {
        version.current = data.version
        setState(data.state ?? initialState)
      }
      pending.current = null
      actionMessage.current = data.message ?? ''
      setActionNotice(actionMessage.current)
      return data.applied !== false
    } catch (reason) {
      if (pending.current) pending.current.uncertain = true
      setError(reason instanceof Error ? reason.message : 'Связь с backend прервалась. Нажмите «Повторить».')
      return false
    } finally { inFlight.current = false; setSaving(false) }
  }
  return { state, save, createProblem, problemAction, assemblyAction, mapProduct, disassemblyAction, actionNotice, getActionMessage: () => actionMessage.current, ready, saving, error, retry, blocked: !ready || saving || error !== '' }
}
