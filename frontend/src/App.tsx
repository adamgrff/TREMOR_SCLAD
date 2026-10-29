import { Navigate, Route, Routes } from 'react-router'

import HomePage from './pages/HomePage/HomePage'
import CellsPage from './pages/CellsPage/CellsPage'
import ReceivingPage from './pages/ReceivingPage/ReceivingPage'
import AssemblyPage from './pages/AssemblyPage/AssemblyPage'
import PageIntro from './components/PageIntro/PageIntro'
import { useLayoutEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router'
import Header from './components/Header/Header'
import { useTheme } from './hooks/useTheme'
import { navigationId } from './components/RouteTransition/routeTransition'
import './components/RouteTransition/RouteTransition.css'

function App({ showWelcomeIntro }: { showWelcomeIntro: boolean }) {
  const location = useLocation()
  const routeId = navigationId(location)
  const initialLocation = useRef(routeId)
  const { theme, toggleTheme } = useTheme(location.pathname === '/cells' || location.pathname === '/receiving' ? 'light' : 'dark')
  const [introEnabled, setIntroEnabled] = useState(showWelcomeIntro)
  useLayoutEffect(() => {
    const previous = window.history.scrollRestoration
    window.history.scrollRestoration = 'manual'
    return () => { window.history.scrollRestoration = previous }
  }, [])
  useLayoutEffect(() => {
    window.scrollTo({ top: 0, left: 0, behavior: 'instant' })
    if (routeId !== initialLocation.current) setIntroEnabled(false)
  }, [routeId])
  const showIntro = introEnabled && routeId === initialLocation.current && location.pathname === '/'
  return (
    <>
      {showIntro && <PageIntro />}
      <div className={`appShell${location.pathname === '/' ? ' appShell--home' : ''}`}>
        <div className="appHeaderFrame">
          <Header logoMode={location.pathname === '/' ? 'static' : 'navigation'} theme={theme} onToggleTheme={toggleTheme} />
        </div>
        <div data-route-content>
          <Routes>
            <Route path="/" element={<HomePage theme={theme} />} />
            <Route path="/cells" element={<CellsPage theme={theme} />} />
            <Route path="/receiving" element={<ReceivingPage theme={theme} />} />
            <Route path="/assembly" element={<AssemblyPage theme={theme} />} />

            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </div>
      </div>
    </>
  )
}

export default App
