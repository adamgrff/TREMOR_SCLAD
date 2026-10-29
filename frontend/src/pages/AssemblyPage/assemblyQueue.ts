export type QueueOrder = { number: string; source?: 'ozon' | 'preview' }
export type QueueState = {
  activeOrder: QueueOrder | null
  completedOrders: QueueOrder[]
  problemOrders: { order: QueueOrder; problem: { resolutionStatus?: string } }[]
}

export function getQueueOrders<T extends QueueOrder>(orders: T[], state: QueueState | null): T[] {
  const { activeOrder = null, completedOrders = [], problemOrders = [] } = state ?? {}
  return orders.filter((order) => !(order.number === activeOrder?.number && activeOrder.source === order.source) && !completedOrders.some((completed) => completed.number === order.number && completed.source === order.source) && !problemOrders.some((problemOrder) => problemOrder.order.number === order.number && problemOrder.order.source === order.source && problemOrder.problem.resolutionStatus !== 'returned'))
}
