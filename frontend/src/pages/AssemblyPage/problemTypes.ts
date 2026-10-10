export const problemTypes = [
  ['insufficient_stock', 'Недостаточно товара'],
  ['missing_in_cell', 'Товар отсутствует в ячейке'],
  ['unknown_sku', 'Неизвестный товар / SKU'],
  ['damaged_product', 'Повреждённый товар'],
  ['invalid_order', 'Ошибка данных заказа'],
  ['other', 'Другая проблема'],
] as const

export const allProblemTypes = [
  ...problemTypes,
  ['cancelled_order', 'Отменённый заказ'],
  ['ozon_error', 'Ошибка Ozon'],
] as const
