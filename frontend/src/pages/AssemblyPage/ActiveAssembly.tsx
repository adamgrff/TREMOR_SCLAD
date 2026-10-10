const previewAssembly = {
  number: '024831-0001',
  items: [
    { name: 'TREMOR Classic', sku: 'TR-CLASSIC', required: 3, collected: 1 },
    { name: 'TREMOR Black', sku: 'TR-BLACK', required: 2, collected: 0 },
    { name: 'TREMOR White', sku: 'TR-WHITE', required: 1, collected: 0 },
  ],
}

export default function ActiveAssembly() {
  const required = previewAssembly.items.reduce((sum, item) => sum + item.required, 0)
  const collected = previewAssembly.items.reduce((sum, item) => sum + item.collected, 0)
  const progress = required > 0 ? Math.min(100, collected / required * 100) : 0

  return <section className="receivingPage__panel activeAssembly" aria-labelledby="active-assembly-title">
    <div className="activeAssembly__header">
      <h2 className="receivingPage__panelTitle" id="active-assembly-title">ЗАКАЗ № {previewAssembly.number}</h2>
      <div className={`activeAssembly__progress${progress === 100 ? ' activeAssembly__progress--complete' : ''}`} role="progressbar" aria-label="Прогресс сборки заказа" aria-valuemin={0} aria-valuemax={required} aria-valuenow={collected} aria-valuetext={`Собрано ${collected} из ${required} единиц`}>
        <div className="activeAssembly__progressFill" style={{ width: `${progress}%` }} />
      </div>
    </div>
    <div className="activeAssembly__workspace">
      <div className="activeAssembly__tableScroll" role="region" aria-label="Товары активной сборки" tabIndex={0}>
        <table className="activeAssembly__table">
          <colgroup><col /><col style={{ width: '160px' }} /><col style={{ width: '160px' }} /></colgroup>
          <thead><tr><th>Товар / SKU</th><th>Нужно</th><th>Собрано</th></tr></thead>
          <tbody>{previewAssembly.items.map((item) => <tr key={item.sku}>
            <td><strong>{item.name}</strong><span>{item.sku}</span></td><td>{item.required} шт.</td><td>{item.collected} шт.</td>
          </tr>)}</tbody>
        </table>
      </div>
      <div className="activeAssembly__scanPanel">
        <label className="receivingPage__panelTitle" htmlFor="active-assembly-scan">СКАНИРУЙТЕ ЯЧЕЙКУ</label>
        <input id="active-assembly-scan" className="receivingPage__scanInput activeAssembly__scanInput" placeholder="Код ячейки" autoComplete="off" />
        <div className="activeAssembly__lastScan">
          <h3>Последний успешный скан</h3>
          <p>TR-CLASSIC</p>
        </div>
        <div className="activeAssembly__scanLog" role="status">
          <p>TREMOR Classic: собрана 1 единица из A1.</p>
          <p>Здесь будут отображаться результат последнего скана и возможные ошибки.</p>
        </div>
        <div className="activeAssembly__actions">
          <button className="assemblyQueue__problem" type="button" aria-disabled="true">Проблема</button>
          <button className="receivingPage__finishButton assemblyQueue__start" type="button" disabled>Завершить сборку</button>
        </div>
      </div>
    </div>
  </section>
}
