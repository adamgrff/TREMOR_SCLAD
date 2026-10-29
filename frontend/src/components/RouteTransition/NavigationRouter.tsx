import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { flushSync } from 'react-dom'
import { Router, UNSAFE_createBrowserHistory as createBrowserHistory } from 'react-router'
import { createRouteTransition } from './routeTransition'

// Same history implementation as BrowserRouter, with a single listener for
// PUSH/REPLACE/POP. It captures before React replaces main, not after a paint.
export default function NavigationRouter({ children }: { children: ReactNode }) {
  const historyRef = useRef<ReturnType<typeof createBrowserHistory> | null>(null)
  if (!historyRef.current) historyRef.current = createBrowserHistory({ v5Compat: true })
  const history = historyRef.current
  const [state, setState] = useState({ action: history.action, location: history.location })
  const currentPath = useRef(state.location.pathname)

  useLayoutEffect(() => {
    const transitions = createRouteTransition()
    const unlisten = history.listen(({ action, location }) => {
      const animate = currentPath.current !== location.pathname
      currentPath.current = location.pathname
      transitions.navigate(() => flushSync(() => setState({ action, location })), animate)
    })
    return () => { unlisten(); transitions.dispose() }
  }, [history])

  return <Router location={state.location} navigationType={state.action} navigator={history} useTransitions={false}>
    {children}
  </Router>
}
