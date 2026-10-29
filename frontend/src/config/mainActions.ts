import iconReceiving from '../assets/home/icon-receiving.svg'
import iconCells from '../assets/home/icon-cells.svg'
import iconAssembly from '../assets/home/icon-assembly.svg'

import iconReceivingBlack from '../assets/home/icon-receiving-black.svg'
import iconCellsBlack from '../assets/home/icon-cells-black.svg'
import iconAssemblyBlack from '../assets/home/icon-assembly-black.svg'

type MainAction = {
  title: string
  navigationTitle?: string
  description: string
  icon: string
  lightIcon: string
  path: string
}

export const mainActions: MainAction[] = [
  {
    title: 'ПРИЕМКА',
    navigationTitle: 'ПРИЁМКА',
    description: 'Добавление товара на склад',
    icon: iconReceiving,
    lightIcon: iconReceivingBlack,
    path: '/receiving',
  },
  {
    title: 'ЯЧЕЙКИ',
    description: 'Страница схемы ячеек и их содержимого',
    icon: iconCells,
    lightIcon: iconCellsBlack,
    path: '/cells',
  },
  {
    title: 'СБОРКА',
    description: 'Страница с выдачей и сборкой заказов',
    icon: iconAssembly,
    lightIcon: iconAssemblyBlack,
    path: '/assembly',
  },
]
