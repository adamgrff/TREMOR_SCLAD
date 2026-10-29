export type StatisticsPeriod = 'today' | '7d' | '30d'
export const statisticsPeriodValues = { 'Сегодня': 'today', '7 дней': '7d', '30 дней': '30d' } as const

export type HomeStatistics = {
  receiving: { sessions: number | null; receivedQuantity: number | null; placedQuantity: number | null; pendingQuantity: number | null }
  stock: {
    totalQuantity: number | null; occupiedCells: number | null; totalCells: number | null
    skuCount: number | null; lowStockCount: number | null
    lowStockItems: { sku: string; quantity: number | null }[] | null
  }
  assembly: { completedOrders: number | null; queuedOrders: number | null; activeOrders: number | null; problemOrders: number | null; averageAssemblySeconds: number | null }
}

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid statistics response')
  return value as Record<string, unknown>
}

function metric(value: unknown, fractional = false): number | null {
  if (value === null) return null
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || (!fractional && !Number.isSafeInteger(value))) throw new Error('Invalid statistics metric')
  return value
}

export function parseHomeStatistics(value: unknown): HomeStatistics {
  const data = record(value)
  const receiving = record(data.receiving), stock = record(data.stock), assembly = record(data.assembly)
  if (stock.lowStockItems !== null && !Array.isArray(stock.lowStockItems)) throw new Error('Invalid low-stock items')
  return {
    receiving: {
      sessions: metric(receiving.sessions), receivedQuantity: metric(receiving.receivedQuantity),
      placedQuantity: metric(receiving.placedQuantity), pendingQuantity: metric(receiving.pendingQuantity),
    },
    stock: {
      totalQuantity: metric(stock.totalQuantity), occupiedCells: metric(stock.occupiedCells), totalCells: metric(stock.totalCells),
      skuCount: metric(stock.skuCount), lowStockCount: metric(stock.lowStockCount),
      lowStockItems: stock.lowStockItems === null ? null : (stock.lowStockItems as unknown[]).map(value => {
        const item = record(value)
        if (typeof item.sku !== 'string') throw new Error('Invalid low-stock SKU')
        return { sku: item.sku, quantity: metric(item.quantity) }
      }),
    },
    assembly: {
      completedOrders: metric(assembly.completedOrders), queuedOrders: metric(assembly.queuedOrders),
      activeOrders: metric(assembly.activeOrders), problemOrders: metric(assembly.problemOrders),
      averageAssemblySeconds: metric(assembly.averageAssemblySeconds, true),
    },
  }
}

export function formatStatistic(value: number | null | undefined, unit = ''): string {
  return value == null ? '—' : `${value.toLocaleString('ru-RU')}${unit ? ` ${unit}` : ''}`
}

export function formatAssemblyDuration(value: number | null | undefined): string {
  if (value == null) return '—'
  const seconds = Math.round(value)
  const minutes = Math.floor(seconds / 60), remainder = seconds % 60
  return minutes ? `${minutes} мин${remainder ? ` ${remainder} с` : ''}` : `${remainder} с`
}
