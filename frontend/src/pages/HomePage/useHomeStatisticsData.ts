import { useEffect, useState } from 'react'
import { API_BASE_URL } from '../../config/api'
import { parseHomeStatistics, type HomeStatistics, type StatisticsPeriod } from './homeStatisticsData'

export function useHomeStatisticsData(period: StatisticsPeriod): HomeStatistics | null {
  const [loaded, setLoaded] = useState<{ period: StatisticsPeriod; data: HomeStatistics } | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    const { signal } = controller
    setLoaded(null)
    async function load() {
      const response = await fetch(`${API_BASE_URL}/home/statistics?period=${period}`, { signal, cache: 'no-store' })
      if (!response.ok) throw new Error(`Statistics request failed: ${response.status}`)
      const data = parseHomeStatistics(await response.json())
      if (!signal.aborted) setLoaded({ period, data })
    }
    void load().catch(() => {
      if (!signal.aborted) setLoaded(null)
    })
    return () => controller.abort()
  }, [period])

  return loaded?.period === period ? loaded.data : null
}
