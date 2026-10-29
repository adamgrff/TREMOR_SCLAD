import { getQueueOrders, type QueueOrder, type QueueState } from '../AssemblyPage/assemblyQueue'

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid summary data')
  return value as Record<string, unknown>
}

function quantity(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) throw new Error('Invalid summary quantity')
  return value
}

function order(value: unknown): QueueOrder {
  const item = record(value)
  if (typeof item.number !== 'string' || (item.source !== undefined && item.source !== 'ozon' && item.source !== 'preview')) throw new Error('Invalid queue order')
  return { number: item.number, source: item.source }
}

export function pendingQuantity(data: unknown): number {
  return quantity(record(data).pendingQuantity)
}

export function stockQuantity(data: unknown): number {
  const products = record(data).products
  if (!Array.isArray(products)) throw new Error('Invalid product catalog')
  return quantity(products.reduce((sum, product: unknown) => sum + quantity(record(product).quantity), 0))
}

export function queueQuantity(ordersData: unknown, draftData: unknown): number {
  const orders = record(ordersData).orders
  if (!Array.isArray(orders)) throw new Error('Invalid queue data')
  const raw = record(draftData).state
  let state: QueueState | null = null
  if (raw !== null) {
    const workspace = record(raw)
    if (!Array.isArray(workspace.completedOrders) || !Array.isArray(workspace.problemOrders)) throw new Error('Invalid queue state')
    state = {
      activeOrder: workspace.activeOrder === null ? null : order(workspace.activeOrder),
      completedOrders: workspace.completedOrders.map(order),
      problemOrders: workspace.problemOrders.map((value: unknown) => {
        const item = record(value)
        const problem = record(item.problem)
        if (problem.resolutionStatus !== undefined && typeof problem.resolutionStatus !== 'string') throw new Error('Invalid problem state')
        return { order: order(item.order), problem: { resolutionStatus: problem.resolutionStatus as string | undefined } }
      }),
    }
  }
  return getQueueOrders(orders.map(order), state).length
}
