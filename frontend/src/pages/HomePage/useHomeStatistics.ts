import { useEffect, useRef, type PointerEvent } from 'react'

export function useHomeStatistics() {
  const screenRef = useRef<HTMLDivElement>(null)
  const windowRef = useRef<HTMLElement>(null)
  const bridgeRef = useRef<HTMLDivElement>(null)
  const surfaceRef = useRef<SVGSVGElement>(null)
  const cancelScroll = useRef<() => void>(() => {})

  useEffect(() => {
    let frame = 0
    const update = () => {
      frame = 0
      const screen = screenRef.current
      const panel = windowRef.current
      const bridge = bridgeRef.current
      if (!screen || !panel || !bridge) return
      const start = screen.getBoundingClientRect().top + window.scrollY
      const end = panel.getBoundingClientRect().top + window.scrollY - 24
      const progress = Math.max(0, Math.min(1, (window.scrollY - start) / Math.max(1, end - start)))
      // Both native and programmed scrolling feed this single visual position.
      // The receiving tail projects 24px above the window: stop at its tip.
      const distance = Math.max(0, bridge.offsetHeight - 24)
      const travel = progress * distance
      bridge.style.setProperty('--light-travel', `${travel}px`)
      screen.style.setProperty('--summary-light', String(Math.max(0, 1 - travel / 80)))
      // Project the same source into each surface's SVG coordinates. Light
      // disappears by distance, rather than by an independent scroll fade.
      const accent = surfaceRef.current?.querySelector('#homeSummaryAccent')
      accent?.setAttribute('gradientTransform', `translate(500 ${152 + travel}) scale(340 80)`)
      panel.querySelector('#homeStatisticsArrival')?.setAttribute('gradientTransform', `translate(500 ${travel - distance}) scale(320 80)`)
    }
    const schedule = () => { if (!frame) frame = requestAnimationFrame(update) }
    const cancel = () => cancelScroll.current()
    const cancelKey = (event: KeyboardEvent) => {
      if (['ArrowDown', 'ArrowUp', 'PageDown', 'PageUp', 'Home', 'End', ' '].includes(event.key)) cancel()
    }
    const observer = new ResizeObserver(schedule)
    if (screenRef.current) observer.observe(screenRef.current)
    if (windowRef.current) observer.observe(windowRef.current)
    window.addEventListener('scroll', schedule, { passive: true })
    window.addEventListener('resize', schedule)
    window.addEventListener('wheel', cancel, { passive: true })
    window.addEventListener('touchstart', cancel, { passive: true })
    window.addEventListener('pointerdown', cancel, { passive: true })
    window.addEventListener('keydown', cancelKey)
    update()
    return () => {
      cancel()
      cancelAnimationFrame(frame)
      observer.disconnect()
      window.removeEventListener('scroll', schedule)
      window.removeEventListener('resize', schedule)
      window.removeEventListener('wheel', cancel)
      window.removeEventListener('touchstart', cancel)
      window.removeEventListener('pointerdown', cancel)
      window.removeEventListener('keydown', cancelKey)
    }
  }, [])

  const scrollToStatistics = () => {
    cancelScroll.current()
    const panel = windowRef.current
    if (!panel) return
    const start = window.scrollY
    const target = start + panel.getBoundingClientRect().top - 24
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      window.scrollTo({ top: target, behavior: 'instant' })
      return
    }
    const started = performance.now()
    let frame = 0
    const tick = (now: number) => {
      const progress = Math.min(1, (now - started) / 460)
      const eased = 1 - (1 - progress) ** 3
      window.scrollTo({ top: start + (target - start) * eased, behavior: 'instant' })
      if (progress < 1) frame = requestAnimationFrame(tick)
    }
    frame = requestAnimationFrame(tick)
    cancelScroll.current = () => cancelAnimationFrame(frame)
  }

  const moveGlow = (event: PointerEvent<HTMLButtonElement>) => {
    const svg = surfaceRef.current
    const shape = svg?.querySelector<SVGPathElement>('.homeSummary__shape')
    if (!svg || !shape) return
    const bounds = svg.getBoundingClientRect()
    const x = (event.clientX - bounds.left) * 1000 / bounds.width
    const y = (event.clientY - bounds.top) * 152 / bounds.height
    svg.querySelectorAll('#homeSummaryLocalGlow, #homeSummaryEdgeGlow').forEach(gradient => {
      gradient.setAttribute('cx', String(x))
      gradient.setAttribute('cy', String(y))
    })
    let distance = Infinity
    const length = shape.getTotalLength()
    for (let index = 0; index <= 200; index++) {
      const point = shape.getPointAtLength(length * index / 200)
      distance = Math.min(distance, Math.hypot((point.x - x) * bounds.width / 1000, (point.y - y) * bounds.height / 152))
    }
    const proximity = Math.max(0, 1 - distance / 48)
    svg.style.setProperty('--edge-glow', String(.9 * proximity * proximity))
  }

  return { screenRef, windowRef, bridgeRef, surfaceRef, scrollToStatistics, moveGlow }
}
