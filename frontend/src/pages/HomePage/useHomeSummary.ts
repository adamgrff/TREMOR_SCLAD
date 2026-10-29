import { useEffect, useState } from 'react'
import { API_BASE_URL } from '../../config/api'
import { pendingQuantity, queueQuantity, stockQuantity } from './homeSummaryData'

type Summary = { pending: number | null; stock: number | null; queue: number | null }

export function useHomeSummary() {
  const [summary, setSummary] = useState<Summary>({ pending: null, stock: null, queue: null })

  useEffect(() => {
    const controller = new AbortController()
    const { signal } = controller
    async function read(path: string): Promise<unknown> {
      const response = await fetch(`${API_BASE_URL}${path}`, { signal, cache: 'no-store' })
      if (!response.ok) throw new Error(`Summary request failed: ${response.status}`)
      return response.json()
    }
    function publish(key: keyof Summary, value: number) {
      if (!signal.aborted) setSummary(current => ({ ...current, [key]: value }))
    }
    // Each indicator fails independently; null stays visible as an em dash.
    void read('/receiving/pending-summary').then(pendingQuantity).then(value => publish('pending', value)).catch(() => {})
    void read('/product-catalog').then(stockQuantity).then(value => publish('stock', value)).catch(() => {})
    void Promise.all([read('/assembly/orders'), read('/assembly-preview/draft')])
      .then(([orders, draft]) => queueQuantity(orders, draft)).then(value => publish('queue', value)).catch(() => {})
    return () => controller.abort()
  }, [])

  return summary
}
