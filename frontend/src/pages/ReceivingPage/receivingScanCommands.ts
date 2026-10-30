import { API_BASE_URL } from '../../config/api'

type ScanCommand = { requestId: string; sessionId: number; sku: string; quantity: 1 }
const prefix = 'tremor.receiving.scan.'
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const uncertain = 'Результат операции приёмки не подтверждён. Новые действия заблокированы. Нажмите «Повторить»: будет проверена та же команда.'
const ambiguousOrder = 'Обнаружено несколько незавершённых команд приёмки. Их порядок неизвестен; автоматическое восстановление и новые складские действия заблокированы. Все команды сохранены. Требуется безопасное восстановление.'

export type ReceivingAction =
  | { kind: 'create-session' }
  | { kind: 'place'; sessionId: number; cellCode: string; items: Array<{ sku: string; quantity: number }> }
  | { kind: 'undo-placement'; sessionId: number; placementId: number }
  | { kind: 'finish'; sessionId: number; placementIds: number[] }
  | { kind: 'delete-item'; sessionId: number; sku: string; expectedRevision: string }

type OperationCommand = { requestId: string; action: ReceivingAction }
type Command = ScanCommand | OperationCommand
export class ReceivingCommandRejected extends Error {}
export class ReceivingRecoveryOrderUnknown extends Error {}
export class ReceivingJournalUnavailable extends Error {}
const positive = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0

function validAction(action: ReceivingAction) {
  if (!action || typeof action !== 'object') return false
  if (action.kind === 'create-session') return true
  if (!positive(action.sessionId)) return false
  switch (action.kind) {
    case 'place': return typeof action.cellCode === 'string' && !!action.cellCode.trim()
      && Array.isArray(action.items) && action.items.length > 0 && action.items.every(item => typeof item?.sku === 'string' && !!item.sku.trim() && positive(item.quantity))
    case 'undo-placement': return positive(action.placementId)
    case 'finish': return Array.isArray(action.placementIds) && action.placementIds.length > 0 && action.placementIds.every(positive)
    case 'delete-item': return typeof action.sku === 'string' && !!action.sku.trim() && typeof action.expectedRevision === 'string' && /^(0|[1-9][0-9]*)$/.test(action.expectedRevision)
    default: return false
  }
}

function delivery(command: Command) {
  if (!('action' in command)) return {
    path: `/receiving/sessions/${command.sessionId}/items`, method: 'POST',
    body: { requestId: command.requestId, sku: command.sku, quantity: command.quantity }, sessionId: command.sessionId,
  }
  const { requestId, action } = command
  switch (action.kind) {
    case 'create-session': return { path: '/receiving/sessions', method: 'POST', body: { requestId }, sessionId: null }
    case 'place': return { path: '/receiving/placements', method: 'POST', body: { requestId, sessionId: action.sessionId, cellCode: action.cellCode, items: action.items }, sessionId: action.sessionId }
    case 'undo-placement': return { path: `/receiving/placements/${action.placementId}/undo`, method: 'POST', body: { requestId }, sessionId: action.sessionId }
    case 'finish': return { path: '/receiving/finish', method: 'POST', body: { requestId, sessionId: action.sessionId, placementIds: action.placementIds }, sessionId: action.sessionId }
    case 'delete-item': return { path: `/receiving/sessions/${action.sessionId}/items/${encodeURIComponent(action.sku)}`, method: 'DELETE', body: { requestId, expectedRevision: action.expectedRevision }, sessionId: action.sessionId }
  }
}

// The existing DB-02.1 journal also stores all receiving actions. Legacy scan
// records stay readable with their original UUID and identical delivery body.
export class ReceivingScanCommands {
  private pending = new Map<string, Command>()
  private inFlight = false
  private ready = false
  private storage: () => Storage
  private request: typeof fetch

  constructor(storage: () => Storage = () => window.localStorage, request: typeof fetch = (...args) => fetch(...args)) {
    this.storage = storage; this.request = request
  }

  private readPending() {
    let entries: Array<[string, string | null]>
    try {
      const storage = this.storage()
      entries = []
      for (let index = 0; index < storage.length; index++) {
        const key = storage.key(index)
        if (key?.startsWith(prefix)) entries.push([key, storage.getItem(key)])
      }
    } catch { throw new ReceivingJournalUnavailable('Журнал команд недоступен. Разрешён только просмотр; складские действия заблокированы.') }
    const commands: Command[] = []
    for (const [key, value] of entries) {
      const command = JSON.parse(value ?? 'null') as Command | null
      if (!command || !uuid.test(command.requestId) || key !== prefix + command.requestId
        || ('action' in command ? !validAction(command.action) : !positive(command.sessionId)
          || typeof command.sku !== 'string' || !command.sku.trim() || command.sku !== command.sku.trim().toUpperCase()
          || command.quantity !== 1)) throw new Error('Сохранённая команда приёмки повреждена. Новые действия заблокированы; требуется проверка.')
      commands.push(command)
    }
    for (const command of commands) {
      const previous = this.pending.get(command.requestId)
      if (previous && JSON.stringify(previous) !== JSON.stringify(command)) throw new Error(uncertain)
      this.pending.set(command.requestId, command)
    }
    // Journal enumeration does not establish business order, including when
    // separate tabs have left commands behind. Do not replay any of them.
    if (this.pending.size > 1) throw new ReceivingRecoveryOrderUnknown(ambiguousOrder)
  }

  private persist(command: Command) {
    const storage = this.storage(), key = prefix + command.requestId, value = JSON.stringify(command)
    const previous = storage.getItem(key)
    if (previous !== null && previous !== value) throw new Error(uncertain)
    storage.setItem(key, value)
    if (storage.getItem(key) !== value) throw new Error(uncertain)
  }

  private verifyJournalWritable() {
    // A separate probe never changes a command or its UUID. A readable but
    // full/read-only storage must also leave the page in viewing mode.
    const key = 'tremor.receiving.journalProbe'
    try {
      const storage = this.storage()
      storage.setItem(key, 'available')
      if (storage.getItem(key) !== 'available') throw new Error()
      storage.removeItem(key)
      if (storage.getItem(key) !== null) throw new Error()
    } catch { throw new ReceivingJournalUnavailable('Журнал команд недоступен для записи. Разрешён только просмотр; складские действия заблокированы.') }
  }

  private forget(command: Command) {
    const storage = this.storage()
    storage.removeItem(prefix + command.requestId)
    if (storage.getItem(prefix + command.requestId) !== null) throw new Error(uncertain)
    this.pending.delete(command.requestId)
  }

  private async resolve(command: Command, synchronize: (sessionId: number) => Promise<void>) {
    this.persist(command)
    const target = delivery(command)
    const response = await this.request(API_BASE_URL + target.path, {
      method: target.method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(target.body),
    })
    if (!response.ok) {
      // Only an explicit pre-COMMIT business rejection is safe to resolve as
      // unsuccessful. Network/5xx/identity-conflict responses retain the UUID.
      if ([400, 404, 409].includes(response.status) && response.headers?.get('X-Receiving-Command-Rejected') === 'true') {
        const result = await response.json() as { error?: string }
        if (target.sessionId !== null) await synchronize(target.sessionId)
        this.forget(command)
        throw new ReceivingCommandRejected(result.error || 'Операция отклонена; загружено актуальное состояние.')
      }
      throw new Error(uncertain)
    }
    const result = await response.json() as { status?: string; id?: number; placementCount?: number }
    const kind = 'action' in command ? command.action.kind : 'scan'
    const valid = kind === 'scan' ? result.status === 'saved'
      : kind === 'delete-item' ? result.status === 'deleted'
      : kind === 'create-session' ? positive(result.id) && result.status === 'active'
      : kind === 'undo-placement' ? result.status === 'undone' && result.id === ('action' in command && command.action.kind === kind ? command.action.placementId : null)
      : kind === 'finish' ? result.id === target.sessionId && Number.isSafeInteger(result.placementCount)
      : positive(result.id)
    if (!valid) throw new Error(uncertain)
    await synchronize(target.sessionId ?? result.id!)
    this.forget(command)
  }

  async recover(synchronize: (sessionId: number) => Promise<void>) {
    if (this.inFlight) throw new Error(uncertain)
    this.inFlight = true; this.ready = false
    try {
      this.readPending()
      for (const command of this.pending.values()) {
        try { await this.resolve(command, synchronize) }
        catch (reason) { if (!(reason instanceof ReceivingCommandRejected)) throw reason }
      }
      this.verifyJournalWritable()
      this.ready = true
    } catch (reason) {
      if (reason instanceof ReceivingRecoveryOrderUnknown || reason instanceof ReceivingJournalUnavailable) throw reason
      throw new Error(reason instanceof Error && (reason.message.includes('повреждена') || reason.message === ambiguousOrder) ? reason.message
        : `${uncertain} Если хранилище недоступно, отправка не возобновится до его восстановления.`)
    } finally { this.inFlight = false }
  }

  private async submit(command: Command, synchronize: (sessionId: number) => Promise<void>) {
    if (!this.ready || this.inFlight || this.pending.size) throw new Error(uncertain)
    this.inFlight = true; this.ready = false
    try {
      this.readPending()
      if (this.pending.size) throw new Error(uncertain)
      this.pending.set(command.requestId, command)
      await this.resolve(command, synchronize)
      this.ready = true
    } catch (reason) {
      if (reason instanceof ReceivingCommandRejected) { this.ready = true; throw reason }
      if (reason instanceof Error && reason.message === ambiguousOrder) throw reason
      throw new Error(uncertain)
    } finally { this.inFlight = false }
  }

  async scan(sessionId: number, sku: string, synchronize: (sessionId: number) => Promise<void>) {
    return this.submit({ requestId: crypto.randomUUID(), sessionId, sku: sku.trim().toUpperCase(), quantity: 1 }, synchronize)
  }

  async execute(action: ReceivingAction, synchronize: (sessionId: number) => Promise<void>) {
    if (!validAction(action)) throw new Error('Некорректная команда приёмки')
    return this.submit({ requestId: crypto.randomUUID(), action: structuredClone(action) }, synchronize)
  }
}

export function readActiveReceivingSession() {
  try { return window.localStorage.getItem('tremor.receiving.activeSessionId') }
  catch { return null }
}

export function rememberActiveReceivingSession(id: number | null) {
  try {
    if (id === null) window.localStorage.removeItem('tremor.receiving.activeSessionId')
    else window.localStorage.setItem('tremor.receiving.activeSessionId', String(id))
  } catch { /* Scan commands have their own mandatory durable journal. */ }
}
