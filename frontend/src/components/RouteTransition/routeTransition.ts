import type { Location } from 'react-router'

export function navigationId(location: Pick<Location, 'key' | 'pathname' | 'search' | 'hash'>) {
  return JSON.stringify([location.key, location.pathname, location.search, location.hash])
}

// Only browser-rendered snapshots animate. No retained route trees, cloned
// controls, transforms on live main, timers or interaction locks are needed.
export function createRouteTransition(doc = document, win = window) {
  let active: ViewTransition | undefined
  let revision = 0
  const root = doc.documentElement
  const properties = ['--route-old-x', '--route-old-y', '--route-old-width',
    '--route-new-x', '--route-new-y', '--route-new-width']
  function clear() {
    root.removeAttribute('data-route-transition')
    properties.forEach(property => root.style.removeProperty(property))
  }
  function geometry(side: 'old' | 'new') {
    const rect = doc.querySelector<HTMLElement>('[data-route-content] > main')?.getBoundingClientRect()
    if (!rect) return false
    for (const [key, value] of Object.entries({ x: rect.left, y: rect.top, width: rect.width })) {
      root.style.setProperty(`--route-${side}-${key}`, `${value}px`)
    }
    return true
  }
  return {
    navigate(commit: () => void, animate: boolean) {
      const current = ++revision
      function applyRoute() {
        commit()
        // flushSync also drains mount effects (including existing hash scroll
        // handlers). Reset after them, before the incoming snapshot is taken.
        win.scrollTo({ top: 0, left: 0, behavior: 'instant' })
      }
      active?.skipTransition()
      active = undefined
      if (!animate || !doc.startViewTransition || doc.querySelector('.pageIntro') ||
        win.matchMedia('(prefers-reduced-motion: reduce)').matches || !geometry('old')) {
        clear()
        applyRoute()
        return
      }
      root.setAttribute('data-route-transition', '')
      try {
        const transition = doc.startViewTransition(() => {
          // skipTransition still runs its update callback: superseded callbacks
          // must never commit an older URL after a rapid subsequent navigation.
          if (current !== revision) return
          applyRoute()
          geometry('new')
        })
        active = transition
        void transition.ready.catch(() => {})
        void transition.finished.catch(() => {}).then(() => {
          if (current !== revision) return
          active = undefined
          clear()
        })
      } catch {
        clear()
        applyRoute()
      }
    },
    dispose() {
      revision++
      active?.skipTransition()
      active = undefined
      clear()
    },
  }
}
