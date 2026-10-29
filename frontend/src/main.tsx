import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import NavigationRouter from './components/RouteTransition/NavigationRouter'

import './index.css'
import App from './App.tsx'
import { shouldShowWelcomeIntro } from './components/PageIntro/welcomeIntroSession'

// Consume the first opening once, before StrictMode can replay React rendering.
const showWelcomeIntro = shouldShowWelcomeIntro(window.location.pathname)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <NavigationRouter>
      <App showWelcomeIntro={showWelcomeIntro} />
    </NavigationRouter>
  </StrictMode>,
)
