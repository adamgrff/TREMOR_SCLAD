import { useEffect, useState } from 'react'
import refreshArrow from '../../assets/home/refresh-arrow.svg'

export type OzonSyncState = {
  status: 'connected' | 'disconnected' | 'syncing' | 'error'
  lastSyncedAt?: string
}

type Props = {
  state?: OzonSyncState
  onSync?: () => Promise<void>
}

export default function OzonSyncStatus({ state = { status: 'disconnected' }, onSync }: Props) {
  const [expanded, setExpanded] = useState(false)
  const [pending, setPending] = useState(false)
  const [failed, setFailed] = useState(false)
  const [revealAttempt, setRevealAttempt] = useState(0)
  const status = pending || state.status === 'syncing' ? 'syncing' : failed ? 'error' : state.status
  const showFullStatus = expanded || status === 'syncing' || status === 'error'
  const timestamp = state.lastSyncedAt ? new Date(state.lastSyncedAt) : null
  const time = timestamp && !Number.isNaN(timestamp.getTime())
    ? timestamp.toLocaleTimeString('ru-RU', { timeZone: 'Asia/Krasnoyarsk', hour: '2-digit', minute: '2-digit' })
    : '—'
  const label = status === 'connected' ? 'Ozon подключён · обновлено'
    : status === 'syncing' ? 'Синхронизация...'
      : status === 'error' ? 'Ошибка синхронизации' : 'Ozon не подключён'

  useEffect(() => {
    if (!expanded || status === 'syncing') return
    const timer = window.setTimeout(() => setExpanded(false), 10000)
    return () => window.clearTimeout(timer)
  }, [expanded, status, state.lastSyncedAt, revealAttempt])

  async function refresh() {
    setExpanded(true)
    setRevealAttempt((value) => value + 1)
    if (!onSync || pending || status === 'syncing') return
    setFailed(false)
    setPending(true)
    try { await onSync() }
    catch { setFailed(true) }
    finally { setPending(false) }
  }

  return <div className={`ozonSync ozonSync--${status}`}>
    <button className="headerIconButton ozonSync__button" type="button" aria-label="Обновить заказы Ozon" title={onSync ? 'Обновить заказы Ozon' : 'Синхронизация Ozon пока не подключена'} aria-busy={status === 'syncing'} disabled={status === 'syncing'} onClick={() => void refresh()}>
      <img className="headerIcon ozonSync__icon" src={refreshArrow} alt="" aria-hidden="true" />
      {status === 'disconnected' && <span className="ozonSync__disconnectedDot" aria-hidden="true" />}
    </button>
    <span className={`ozonSync__text${showFullStatus ? ' ozonSync__text--expanded' : ''}`} role="status" aria-label={status === 'connected' ? `${label} ${time}` : label}>
      <span className="ozonSync__label"><span>{label}</span></span>
      {status === 'connected' && <time dateTime={timestamp && !Number.isNaN(timestamp.getTime()) ? timestamp.toISOString() : undefined}>{time}</time>}
    </span>
  </div>
}
