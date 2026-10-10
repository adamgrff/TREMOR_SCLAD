import { API_BASE_URL } from '../../config/api'
import type { AssemblyPreviewOrder, AssemblyProblemSnapshot } from './ActiveAssembly'
import type { SavedOrderProblem, ProblemHistoryEvent } from './OrderProblemDialog'

export default async function checkProblemStock(order: AssemblyPreviewOrder, problem: SavedOrderProblem, assembly?: AssemblyProblemSnapshot) {
  const checkEvent: ProblemHistoryEvent = { createdAt: new Date().toISOString(), kind: 'check', title: 'Выполнена проверка остатков', message: 'Проверка доступного количества по всем позициям заказа.', outcome: 'neutral' }
  const results = await Promise.allSettled(order.items.map(async (item) => {
    const response = await fetch(`${API_BASE_URL}/products/${encodeURIComponent(item.sku)}/stock`, { cache: 'no-store', signal: AbortSignal.timeout(15000) })
    if (!response.ok) throw new Error(response.status === 404 ? `SKU ${item.sku} не найден на складе. Проверьте сопоставление товара.` : `Не удалось проверить остаток ${item.name}.`)
    const data = await response.json() as { sku: string; cells: Array<{ cellCode: string; quantity: number }> }
    if (data.sku !== item.sku || !Array.isArray(data.cells) || data.cells.some((cell) => !cell.cellCode || !Number.isSafeInteger(cell.quantity) || cell.quantity < 0)) throw new Error(`Некорректный ответ склада для ${item.name}.`)
    const available = data.cells.reduce((sum, cell) => sum + cell.quantity, 0)
    const picked = assembly?.picks.filter((pick) => pick.sku === item.sku).length ?? 0
    const required = Math.max(0, item.quantity - picked)
    if (required > 0 && new Set(data.cells.map((cell) => cell.cellCode)).size !== data.cells.length) throw new Error(`Для ${item.name} найдены одинаковые коды ячеек. Требуется уточнить ячейки перед возвратом в сборку.`)
    const plan = new Map<string, number>()
    for (const pick of assembly?.picks ?? []) if (pick.sku === item.sku) plan.set(pick.cell, (plan.get(pick.cell) ?? 0) + 1)
    const cellStocks = Object.fromEntries(data.cells.map((cell) => [cell.cellCode, cell.quantity]))
    for (const [cell, quantity] of plan) cellStocks[cell] = (cellStocks[cell] ?? 0) + quantity
    let remaining = required
    for (const cell of [...data.cells].sort((a, b) => a.quantity - b.quantity || a.cellCode.localeCompare(b.cellCode, 'ru', { numeric: true }))) {
      const take = Math.min(remaining, cell.quantity)
      if (take > 0) plan.set(cell.cellCode, (plan.get(cell.cellCode) ?? 0) + take)
      remaining -= take
    }
    return { item, available, required, missing: Math.max(0, required - available), cellStocks, cells: [...plan].map(([cell, quantity]) => `${cell} ×${quantity}`).join(' · ') }
  }))
  const failed = results.find((result) => result.status === 'rejected')
  const timestamp = new Date().toISOString()
  if (failed?.status === 'rejected') {
    const message = failed.reason instanceof Error ? failed.reason.message : 'Проверка остатков не выполнена. Повторите попытку.'
    const event: ProblemHistoryEvent = { createdAt: timestamp, title: 'Ошибка проверки остатков', message, outcome: 'error' }
    return { order, problem: { ...problem, resolutionStatus: 'awaiting' as const, history: [...(problem.history ?? []), checkEvent, event] }, message }
  }
  const stocks = results.flatMap((result) => result.status === 'fulfilled' ? [result.value] : [])
  const shortages = stocks.filter((stock) => stock.missing > 0).map(({ item, available, required, missing }) => ({ sku: item.sku, name: item.name, cells: item.cells, required, available, missing }))
  const enough = shortages.length === 0
  const missing = shortages.reduce((sum, item) => sum + item.missing, 0)
  const message = enough ? '✓ Остатков достаточно. Проблема устранена!' : `Товара всё ещё недостаточно. Не хватает: ${missing} шт.`
  const event: ProblemHistoryEvent = { createdAt: timestamp, kind: 'result', title: enough ? '✓ Остатков достаточно.' : 'Товара всё ещё недостаточно', message: enough ? `Проблема устранена! Доступно: ${stocks.map(({ item, available }) => `${item.name} — ${available} шт.`).join('; ')}` : `Не хватает: ${missing} шт. ${shortages.map((item) => `${item.name}: ${item.missing} шт.`).join('; ')}`, outcome: enough ? 'success' : 'failure' }
  const resolution: ProblemHistoryEvent[] = enough && problem.resolutionStatus !== 'resolved' ? [{ createdAt: timestamp, kind: 'resolution', title: 'Проблема решена', message: 'Остатков достаточно для продолжения сборки.', outcome: 'success' }] : []
  return {
    order: { ...order, items: order.items.map((item) => {
      const stock = stocks.find((stock) => stock.item.sku === item.sku)!
      return { ...item, stock: stock.available, ...(enough ? { cells: stock.cells, cellStocks: stock.cellStocks } : {}) }
    }) },
    problem: { ...problem, shortages, resolutionStatus: enough ? 'resolved' as const : 'awaiting' as const, history: [...(problem.history ?? []), checkEvent, event, ...resolution] },
    message,
  }
}
