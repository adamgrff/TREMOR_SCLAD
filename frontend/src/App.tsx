import { Navigate, Route, Routes } from 'react-router'

import HomePage from './pages/HomePage/HomePage'
import CellsPage from './pages/CellsPage/CellsPage'
import ReceivingPage from './pages/ReceivingPage/ReceivingPage'
import IssuingPage from './pages/IssuingPage/IssuingPage'
import PageIntro from './components/PageIntro/PageIntro'
import { useCallback, useState } from 'react'
import { useLocation } from 'react-router'

function App() {
  const location = useLocation()
  const [displayedLocation, setDisplayedLocation] = useState(location)
  const switchPage = useCallback(() => {
    window.scrollTo({ top: 0, left: 0, behavior: 'instant' })
    setDisplayedLocation(location)
  }, [location])
  return (
    <>
      <PageIntro key={location.key} closing={displayedLocation.key !== location.key} onClosed={switchPage} />
      <Routes location={displayedLocation}>
        <Route path="/" element={<HomePage />} />
        <Route path="/cells" element={<CellsPage />} />
        <Route path="/receiving" element={<ReceivingPage />} />
        <Route path="/issuing" element={<IssuingPage />} />

        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </>
  )
}

export default App
