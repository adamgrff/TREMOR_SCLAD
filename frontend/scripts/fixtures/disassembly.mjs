// Isolated fixtures only: no API, order imports or warehouse writes.
const item = (id, cellId, returned = false) => ({ id, sku: 'OZON', warehouseSku: 'A', name: 'Тестовый товар', cell: 'A1', cellId, warehouse: 'Склад', operationId: `pick-${id}`, returned, returnable: true })
const session = { id: 'session', number: 'REAL-123', reason: 'cancelled_order', status: 'active', pendingItemId: 'item2', items: [item('item1', 1, true), item('item2', 2)], lastScan: 'A', log: 'Ожидается ячейка', error: false }
export function disassemblyFixtures() {
  return structuredClone({
    empty: { sessions: [], blocked: false },
    waiting: { sessions: [{ ...session, status: 'waiting', pendingItemId: null, items: session.items.map(i => ({ ...i, returned: false })) }], blocked: false },
    paused: { sessions: [{ ...session, status: 'waiting', startedAt: '2026-10-14T10:00:00Z', pendingItemId: null }], blocked: false },
    product: { sessions: [{ ...session, pendingItemId: null }], blocked: false },
    cell: { sessions: [session], blocked: false },
    error: { sessions: [{ ...session, error: true, log: 'Неверная ячейка' }], blocked: false },
    blocked: { sessions: [session], blocked: true },
    complete: { sessions: [{ ...session, pendingItemId: null, items: session.items.map(i => ({ ...i, returned: true })) }], blocked: false },
  })
}
