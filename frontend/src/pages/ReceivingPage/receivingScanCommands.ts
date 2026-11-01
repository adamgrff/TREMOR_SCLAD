import { API_BASE_URL } from '../../config/api'

type ScanCommand = { deploymentId?: string; requestId: string; sessionId: number; sku: string; quantity: 1 }
const prefix = 'tremor.receiving.scan.'
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const uncertain = 'Результат операции приёмки не подтверждён. Новые действия заблокированы. Нажмите «Повторить»: будет проверена та же команда.'
const ambiguousOrder = 'Обнаружено несколько незавершённых команд приёмки. Их порядок неизвестен; автоматическое восстановление и новые складские действия заблокированы. Все команды сохранены. Требуется безопасное восстановление.'

export type ReceivingAction =
  | { kind: 'create-session' }
  | { kind: 'quantity'; sessionId: number; productId: number; method: 'POST' | 'PUT'; quantity: number; expectedQuantity: number }
  | { kind: 'delete-history'; sessionId: number }
  | { kind: 'catalog-save'; productId?: number; method: 'POST' | 'PUT'; data: { name: string; sku: string; categoryId: number; archived: boolean; receivingQuantity: number; sessionId: number } }
  | { kind: 'place'; sessionId: number; cellCode: string; items: Array<{ sku: string; quantity: number }> }
  | { kind: 'undo-placement'; sessionId: number; placementId: number }
  | { kind: 'finish'; sessionId: number; placementIds: number[] }
  | { kind: 'delete-item'; sessionId: number; sku: string; expectedRevision: string }

type OperationCommand = { deploymentId?: string; requestId: string; action: ReceivingAction }
type Command = ScanCommand | OperationCommand
export class ReceivingCommandRejected extends Error {}
export class ReceivingRecoveryOrderUnknown extends Error {}
export class ReceivingJournalUnavailable extends Error {}
export class ReceivingDeploymentBlocked extends ReceivingJournalUnavailable {}
let confirmedDeployment: string | null = null
const identityBlocked = 'Идентичность развёртывания не подтверждена или изменилась. Команды сохранены без изменений. Разрешён только просмотр; требуется безопасное восстановление.'
const staleContext = 'Развёртывание изменилось или источник данных не подтверждён. Операция не отправлена. Безопасно перезагрузите данные перед новым действием; сохранённые команды не изменены.'

export function isReceivingDeployment(value: unknown): value is string {
  return typeof value === 'string' && uuid.test(value)
}

async function loadDeployment(request: typeof fetch) {
  try {
    const response = await request(API_BASE_URL + '/deployment', { cache: 'no-store' })
    if (!response.ok) throw new Error()
    const value = await response.json() as { deploymentId?: string }
    if (typeof value.deploymentId !== 'string' || !uuid.test(value.deploymentId)) throw new Error()
    return value.deploymentId
  } catch { confirmedDeployment = null; throw new ReceivingDeploymentBlocked(identityBlocked) }
}
const positive = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0

function validAction(action: ReceivingAction) {
  if (!action || typeof action !== 'object') return false
  if (action.kind === 'create-session') return true
  if (action.kind === 'catalog-save') return ['POST', 'PUT'].includes(action.method) && !!action.data?.sku && !!action.data?.name && positive(action.data?.categoryId) && Number.isSafeInteger(action.data?.receivingQuantity) && action.data.receivingQuantity >= 0 && (action.method === 'POST' || positive(action.productId)) && (action.data.receivingQuantity === 0 || positive(action.data.sessionId))
  if (!positive(action.sessionId)) return false
  switch (action.kind) {
    case 'quantity': return positive(action.productId) && ['POST', 'PUT'].includes(action.method) && positive(action.quantity) && Number.isSafeInteger(action.expectedQuantity) && action.expectedQuantity >= 0
    case 'delete-history': return true
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
    case 'quantity': return { path: `/receiving/sessions/${action.sessionId}/quantities/${action.productId}`, method: action.method, body: { requestId, quantity: action.quantity, expectedQuantity: action.expectedQuantity }, sessionId: action.sessionId }
    case 'catalog-save': return { path: `/product-catalog${action.method === 'POST' ? '' : `/${action.productId}`}`, method: action.method, body: { ...action.data, requestId }, sessionId: action.data.receivingQuantity > 0 ? action.data.sessionId : null }
    case 'delete-history': return { path: `/receiving/sessions/${action.sessionId}`, method: 'DELETE', body: { requestId }, sessionId: null }
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
  private deploymentId: string | null = null
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
    try {
      const raw = this.storage().getItem('tremor.receiving.activeSession.v2')
      if (raw && (JSON.parse(raw) as { deploymentId?: string }).deploymentId !== this.deploymentId) throw new ReceivingDeploymentBlocked(identityBlocked)
    } catch (reason) { if (reason instanceof ReceivingDeploymentBlocked) throw reason; throw new ReceivingJournalUnavailable('Привязка локальной сессии недоступна. Разрешён только просмотр.') }
    for (const [key, value] of entries) {
      const command = JSON.parse(value ?? 'null') as Command | null
      if (!command || !uuid.test(command.requestId) || (command.deploymentId !== undefined && !uuid.test(command.deploymentId)) || key !== prefix + command.requestId
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
    if ([...this.pending.values()].some(command => !this.deploymentId || command.deploymentId !== this.deploymentId)) throw new ReceivingDeploymentBlocked(identityBlocked)
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
    if (!this.deploymentId || command.deploymentId !== this.deploymentId) throw new ReceivingDeploymentBlocked(identityBlocked)
    this.persist(command)
    const target = delivery(command)
    const response = await this.request(API_BASE_URL + target.path, {
      method: target.method, headers: { 'Content-Type': 'application/json', 'X-Tremor-Deployment-Id': command.deploymentId! }, body: JSON.stringify(target.body),
    })
    if (!response.ok) {
      if ([400, 409, 503].includes(response.status)) {
        const failure = await response.clone?.().json().catch(() => ({})) ?? {} as { code?: string }
        if (failure.code?.startsWith('deployment_identity_')) { confirmedDeployment = null; this.ready = false; throw new ReceivingDeploymentBlocked(identityBlocked) }
      }
      // Only an explicit pre-COMMIT business rejection is safe to resolve as
      // unsuccessful. Network/5xx/identity-conflict responses retain the UUID.
      if ([400, 404, 409].includes(response.status) && response.headers?.get('X-Receiving-Command-Rejected') === 'true') {
        const result = response.status === 204 ? {} : await response.json() as { error?: string }
        if (target.sessionId !== null) await synchronize(target.sessionId)
        this.forget(command)
        throw new ReceivingCommandRejected(result.error || 'Операция отклонена; загружено актуальное состояние.')
      }
      throw new Error(uncertain)
    }
    const result = response.status === 204 ? {} : await response.json() as { status?: string; id?: number; placementCount?: number }
    const kind = 'action' in command ? command.action.kind : 'scan'
    const valid = (kind === 'quantity' || kind === 'delete-history') ? response.status === 204
      : kind === 'catalog-save' ? positive((result as { id?: number }).id)
      : kind === 'scan' ? result.status === 'saved'
      : kind === 'delete-item' ? result.status === 'deleted'
      : kind === 'create-session' ? positive(result.id) && result.status === 'active'
      : kind === 'undo-placement' ? result.status === 'undone' && result.id === ('action' in command && command.action.kind === kind ? command.action.placementId : null)
      : kind === 'finish' ? result.id === target.sessionId && Number.isSafeInteger(result.placementCount)
      : positive(result.id)
    if (!valid) throw new Error(uncertain)
    if (target.sessionId !== null || kind === 'create-session') await synchronize(target.sessionId ?? result.id!)
    this.forget(command)
  }

  async prepare(expectedDeploymentId: string) {
    this.ready = false
    try {
      if (!isReceivingDeployment(expectedDeploymentId)) throw new ReceivingDeploymentBlocked(staleContext)
      const actual = await loadDeployment(this.request)
      if (actual !== expectedDeploymentId) throw new ReceivingDeploymentBlocked(staleContext)
      this.deploymentId = expectedDeploymentId
      confirmedDeployment = this.deploymentId
      this.readPending()
      if (this.pending.size) throw new ReceivingDeploymentBlocked('Сначала разрешите сохранённую команду приёмки. Новые действия заблокированы.')
      this.verifyJournalWritable()
      this.ready = true
    } catch (reason) { confirmedDeployment = null; this.deploymentId = null; throw reason }
  }

  async recover(synchronize: (sessionId: number) => Promise<void>) {
    if (this.inFlight) throw new Error(uncertain)
    this.inFlight = true; this.ready = false
    try {
      this.deploymentId = await loadDeployment(this.request)
      confirmedDeployment = this.deploymentId
      this.readPending()
      for (const command of this.pending.values()) {
        if ('action' in command && command.action.kind === 'catalog-save' && command.action.data.receivingQuantity === 0) throw new ReceivingDeploymentBlocked('Незавершённое изменение каталога требует ручной проверки. Команда сохранена; новые действия заблокированы.')
        try { await this.resolve(command, synchronize) }
        catch (reason) { if (!(reason instanceof ReceivingCommandRejected)) throw reason }
      }
      this.verifyJournalWritable()
      this.ready = true
    } catch (reason) {
      confirmedDeployment = null; this.deploymentId = null
      if (reason instanceof ReceivingRecoveryOrderUnknown || reason instanceof ReceivingJournalUnavailable) throw reason
      throw new Error(reason instanceof Error && (reason.message.includes('повреждена') || reason.message === ambiguousOrder) ? reason.message
        : `${uncertain} Если хранилище недоступно, отправка не возобновится до его восстановления.`)
    } finally { this.inFlight = false }
  }

  private async submit(command: Command, synchronize: (sessionId: number) => Promise<void>) {
    if (!this.deploymentId || confirmedDeployment !== this.deploymentId) throw new ReceivingDeploymentBlocked(identityBlocked)
    if (!this.ready || this.inFlight || this.pending.size) throw new Error(uncertain)
    this.inFlight = true; this.ready = false
    try {
      this.readPending()
      if (this.pending.size) throw new Error(uncertain)
      this.pending.set(command.requestId, command)
      await this.resolve(command, synchronize)
      this.ready = true
    } catch (reason) {
      if (reason instanceof ReceivingDeploymentBlocked) throw reason
      if (reason instanceof ReceivingCommandRejected) { this.ready = true; throw reason }
      if (reason instanceof Error && reason.message === ambiguousOrder) throw reason
      throw new Error(uncertain)
    } finally { this.inFlight = false }
  }

  async scan(sessionId: number, sku: string, synchronize: (sessionId: number) => Promise<void>) {
    return this.submit({ deploymentId: this.deploymentId ?? undefined, requestId: crypto.randomUUID(), sessionId, sku: sku.trim().toUpperCase(), quantity: 1 }, synchronize)
  }

  acceptsSnapshot(deploymentId: string | undefined) {
    return isReceivingDeployment(deploymentId) && deploymentId === this.deploymentId && deploymentId === confirmedDeployment
  }

  async execute(action: ReceivingAction, synchronize: (sessionId: number) => Promise<void>) {
    if (!validAction(action)) throw new Error('Некорректная команда приёмки')
    return this.submit({ deploymentId: this.deploymentId ?? undefined, requestId: crypto.randomUUID(), action: structuredClone(action) }, synchronize)
  }
}

export async function sendReceivingAction(action: ReceivingAction, expectedDeploymentId: string | null | undefined, synchronize: (sessionId: number) => Promise<void> = async () => {}) {
  if (!isReceivingDeployment(expectedDeploymentId)) throw new ReceivingDeploymentBlocked(staleContext)
  const journal = new ReceivingScanCommands()
  await journal.prepare(expectedDeploymentId)
  await journal.execute(action, synchronize)
}

// Session IDs are only remembered when the read snapshot belongs to the same
// confirmed deployment. A GET racing a DB switch must not bind B's ID to A.
export function confirmReceivingSnapshot(deploymentId: string | undefined) {
  if (deploymentId && deploymentId === confirmedDeployment) return true
  confirmedDeployment = null
  return false
}

export function readActiveReceivingSession() {
  try {
    const raw = window.localStorage.getItem('tremor.receiving.activeSession.v2')
    const value = raw ? JSON.parse(raw) as { id: number; deploymentId: string } : null
    return value && positive(value.id) && confirmedDeployment && value.deploymentId === confirmedDeployment ? String(value.id) : null
  } catch { return null }
}

export function rememberActiveReceivingSession(id: number | null) {
  if (!confirmedDeployment) return
  try {
    const key = 'tremor.receiving.activeSession.v2'
    if (id === null) {
      const raw = window.localStorage.getItem(key)
      if (raw && (JSON.parse(raw) as { deploymentId?: string }).deploymentId === confirmedDeployment) window.localStorage.removeItem(key)
    } else window.localStorage.setItem(key, JSON.stringify({ id, deploymentId: confirmedDeployment }))
  } catch { /* Read-only viewing remains possible; journal durability is mandatory. */ }
}
