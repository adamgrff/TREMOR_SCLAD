import { useNavigate } from 'react-router'
import '../../App.css'

import Header from '../../components/Header/Header'
import { useTheme } from '../../hooks/useTheme'
import { useHomeStatistics } from './useHomeStatistics'
import { useHomeSummary } from './useHomeSummary'
import StatisticsDraft from './StatisticsDraft'

import { mainActions } from '../../config/mainActions'

function HomePage() {
  const navigate = useNavigate()
  const { theme, toggleTheme } = useTheme('dark')
  const statistics = useHomeStatistics()
  const summary = useHomeSummary()
  const summaryItems = [
    { title: 'ПРИЁМКА', value: summary.pending, description: 'ожидают размещения' },
    { title: 'ЯЧЕЙКИ', value: summary.stock, description: 'ед. на складе' },
    { title: 'СБОРКА', value: summary.queue, description: 'заказов в очереди' },
  ]

  const isLightTheme = theme === 'light'

  return (
    <main className={`homePage homePage--${theme}`}>
      <div className="homePage__screen" ref={statistics.screenRef}>
      <Header logoMode="static" theme={theme} onToggleTheme={toggleTheme} />

      <section className="hero" aria-labelledby="home-title">
        <p className="hero__eyebrow">СКЛАДСКАЯ СИСТЕМА</p>

        <h1 className="hero__title" id="home-title">
          TREMOR SCLAD
        </h1>

        <div className="mainActions" aria-label="Основные действия">
          {mainActions.map((action) => (
            <button
              className="mainActionButton"
              type="button"
              key={action.title}
              onClick={() => {
                if (action.path) {
                  navigate(action.path)
                }
              }}
            >
              <img
                className="mainActionButton__icon"
                src={isLightTheme ? action.lightIcon : action.icon}
                alt=""
                aria-hidden="true"
              />
              <span className="mainActionButton__title">{action.title}</span>
              <span className="mainActionButton__description">
                {action.description}
              </span>
            </button>
          ))}
        </div>

      </section>

      <div className="homeSummary">
        <svg ref={statistics.surfaceRef} className="homeSummary__surface" viewBox="0 0 1000 152" preserveAspectRatio="none" aria-hidden="true">
          <defs>
            <radialGradient id="homeSummaryLocalGlow" gradientUnits="userSpaceOnUse" r="65">
              <stop stopColor="white" stopOpacity=".055" />
              <stop offset="1" stopColor="white" stopOpacity="0" />
            </radialGradient>
            <radialGradient id="homeSummaryEdgeGlow" gradientUnits="userSpaceOnUse" r="100">
              <stop stopColor="white" />
              <stop offset="1" stopColor="white" stopOpacity="0" />
            </radialGradient>
            <radialGradient id="homeSummaryAccent" cx="0" cy="0" r="1" gradientTransform="translate(500 152) scale(340 80)" gradientUnits="userSpaceOnUse">
              <stop offset="0" stopColor="white" stopOpacity=".9" />
              <stop offset="1" stopColor="white" stopOpacity="0" />
            </radialGradient>
          </defs>
          <path className="homeSummary__shape" d="M28 0H972Q1000 0 1000 28V100Q1000 128 972 128H526Q520 128 516 134L500 152L484 134Q480 128 474 128H28Q0 128 0 100V28Q0 0 28 0Z" />
          <path className="homeSummary__hover" fill="url(#homeSummaryLocalGlow)" stroke="url(#homeSummaryEdgeGlow)" d="M28 0H972Q1000 0 1000 28V100Q1000 128 972 128H526Q520 128 516 134L500 152L484 134Q480 128 474 128H28Q0 128 0 100V28Q0 0 28 0Z" />
          <path className="homeSummary__accent homeEdgeHalo" d="M180 128H474Q480 128 484 134L500 152L516 134Q520 128 526 128H820" />
          <path className="homeSummary__accent" d="M180 128H474Q480 128 484 134L500 152L516 134Q520 128 526 128H820" />
        </svg>
        <dl className="homeSummary__items">
          {summaryItems.map((item) => (
            <div className="homeSummary__item" key={item.title}>
              <dt className="homeSummary__title">{item.title}</dt>
              <dd className="homeSummary__data">{item.value ?? '—'} {item.description}</dd>
            </div>
          ))}
        </dl>
        <button className="homeSummary__link" type="button" aria-label="Открыть общее окно статистики" aria-controls="home-statistics" onClick={statistics.scrollToStatistics} onPointerMove={statistics.moveGlow} />
      </div>
      </div>
      <div className="homeStatisticsConnection" ref={statistics.bridgeRef} aria-hidden="true">
        <span className="homeStatisticsConnection__light" />
      </div>
      <section id="home-statistics" className="homeStatistics ds-v1 ui-v1-window" ref={statistics.windowRef} aria-labelledby="home-statistics-title">
        <svg className="homeStatistics__connection" viewBox="0 0 1000 48" preserveAspectRatio="none" aria-hidden="true">
          <defs>
            <radialGradient id="homeStatisticsArrival" cx="0" cy="0" r="1" gradientTransform="translate(500 -156) scale(320 80)" gradientUnits="userSpaceOnUse">
              <stop stopColor="white" stopOpacity=".9" />
              <stop offset="1" stopColor="white" stopOpacity="0" />
            </radialGradient>
          </defs>
          <path className="homeStatistics__tail" d="M474 24Q480 24 484 18L500 0L516 18Q520 24 526 24Z" />
          <path className="homeStatistics__arrival homeEdgeHalo" d="M180 24H474Q480 24 484 18L500 0L516 18Q520 24 526 24H820" />
          <path className="homeStatistics__arrival" d="M180 24H474Q480 24 484 18L500 0L516 18Q520 24 526 24H820" />
        </svg>
        <StatisticsDraft />
      </section>
    </main>
  )
}

export default HomePage
