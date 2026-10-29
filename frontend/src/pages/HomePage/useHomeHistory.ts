import { useEffect, useState } from 'react'
import { API_BASE_URL } from '../../config/api'
import { parseHomeHistory, type HomeHistoryEvent } from './homeHistoryData'

export type HomeHistoryState = { status: 'loading' | 'ready' | 'error'; events: HomeHistoryEvent[] }

export function useHomeHistory(): HomeHistoryState {
  const [state, setState] = useState<HomeHistoryState>({ status: 'loading', events: [] })
  useEffect(() => {
    const controller = new AbortController()
    async function load() {
      try {
        const response = await fetch(`${API_BASE_URL}/home/history?limit=20`, { signal: controller.signal, cache: 'no-store' })
        if (!response.ok) throw new Error('History unavailable')
        const events = parseHomeHistory(await response.json())
        if (!controller.signal.aborted) setState({ status: 'ready', events })
      } catch {
        if (!controller.signal.aborted) setState({ status: 'error', events: [] })
      }
    }
    void load()
    return () => controller.abort()
  }, [])
  return state
}
