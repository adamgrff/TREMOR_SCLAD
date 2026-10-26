import type { AssemblyPreviewOrder, AssemblyProblemSnapshot } from './ActiveAssembly'
import type { OrderProblemDetails, ProblemHistoryEvent } from './OrderProblemDialog'
import { allProblemTypes } from './problemTypes'

export default function createProblemHistory(order: AssemblyPreviewOrder, details: OrderProblemDetails, createdAt: string, assembly?: AssemblyProblemSnapshot): ProblemHistoryEvent[] {
  const position = order.items.find((item) => item.sku === details.positionSku)
  const lastPick = [...(assembly?.picks ?? [])].reverse().find((pick) => !position || pick.sku === position.sku)
  const picked = position ? assembly?.picks.filter((pick) => pick.sku === position.sku).length ?? 0 : assembly?.picks.length ?? 0
  const required = position?.quantity ?? order.items.reduce((sum, item) => sum + item.quantity, 0)
  const reason = allProblemTypes.find(([type]) => type === details.type)?.[1] ?? 'Другая проблема'
  return [
    { createdAt, kind: 'creation', title: assembly ? 'Проблема создана во время сборки' : 'Обнаружена до начала сборки', message: assembly ? `Ячейка: ${assembly.cell ?? lastPick?.cell ?? 'не зафиксирована'}\nПозиция: ${position ? `${position.name} · ${position.sku}` : 'заказ целиком'}\nНа момент проблемы собрано: ${picked} / ${required}` : 'Отбор товара ещё не выполнялся.', outcome: 'neutral' },
    { createdAt, kind: 'transfer', title: 'Заказ передан в проблемные', message: reason, outcome: 'neutral' },
  ]
}
