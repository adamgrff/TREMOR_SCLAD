import { Navigate, Route, Routes } from 'react-router'

import HomePage from './pages/HomePage/HomePage'
import CellsPage from './pages/CellsPage/CellsPage'
import ReceivingPage from './pages/ReceivingPage/ReceivingPage'

function App() {
  return (
    <Routes>
      <Route path="/" element={<HomePage />} />
      <Route path="/cells" element={<CellsPage />} />
      <Route path="/receiving" element={<ReceivingPage />} />

      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

export default App