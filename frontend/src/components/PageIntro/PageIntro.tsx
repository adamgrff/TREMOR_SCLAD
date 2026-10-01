import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import './PageIntro.css'

const INTRO_DELAY_MS = 1000
const REDUCED_MOTION_DELAY_MS = 300
const LOGO_MOVE_MS = 900
const REVEAL_MS = 1400
const CLOSE_MS = 700
const SOFT_EDGE_MARGIN = 320

function setOpeningRect(rect: SVGRectElement, centerX: number, centerY: number, width: number, height: number) {
  rect.setAttribute('x', String(centerX - width / 2))
  rect.setAttribute('y', String(centerY - height / 2))
  rect.setAttribute('width', String(width))
  rect.setAttribute('height', String(height))
}

export default function PageIntro({ closing, onClosed }: { closing: boolean; onClosed: () => void }) {
  const initialTransition = useRef({ closing, onClosed })
  const [finished, setFinished] = useState(false)
  const logoRef = useRef<HTMLDivElement>(null)
  const svgRef = useRef<SVGSVGElement>(null)
  const openingRef = useRef<SVGRectElement>(null)
  const maskId = useId().replace(/:/g, '')

  useEffect(() => {
    const root = document.getElementById('root')
    const logoElement = logoRef.current
    const opening = openingRef.current
    if(!logoElement || !opening) return
    const logo = logoElement
    const wasInert = root?.inert ?? false
    if(root) root.inert = true
    let frame = 0
    let timer = 0
    let animation: Animation | undefined
    let cancelled = false
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches

    function prepareLogo() {
      const headerLogo = document.querySelector<HTMLElement>('.header .logo')
      const bounds = headerLogo?.getBoundingClientRect()
      const width = window.innerWidth
      const height = window.innerHeight
      svgRef.current?.setAttribute('viewBox', `0 0 ${width} ${height}`)
      if(headerLogo && bounds) {
        const scale = bounds.width / headerLogo.offsetWidth
        logo.style.width = `${bounds.width}px`
        logo.style.height = `${bounds.height}px`
        Array.from(headerLogo.children).forEach((source, index) => {
          const target = logo?.children[index] as HTMLElement | undefined
          if(!target) return
          const style = getComputedStyle(source)
          for(const property of ['font-size', 'padding-bottom', 'margin-bottom', 'letter-spacing'] as const) {
            target.style.setProperty(property, `${parseFloat(style.getPropertyValue(property)) * scale}px`)
          }
        })
      }
      const logoBounds = logo.getBoundingClientRect()
      logo.style.left = `${(width - logoBounds.width) / 2}px`
      logo.style.top = `${(height - logoBounds.height) / 2}px`
      return bounds
    }
    prepareLogo()
    function startIntro() {
      timer = window.setTimeout(async () => {
        const bounds = prepareLogo()
        if(bounds && !reducedMotion) {
          animation = logo.animate([
            { left: logo.style.left, top: logo.style.top },
            { left: `${bounds.left}px`, top: `${bounds.top}px` },
          ], { duration: LOGO_MOVE_MS, easing: 'cubic-bezier(.22, 1, .36, 1)', fill: 'forwards' })
          try { await animation.finished } catch { return }
        }
        if(cancelled) return
        if(reducedMotion) { setFinished(true); return }
        const width = window.innerWidth
        const height = window.innerHeight
        svgRef.current?.setAttribute('viewBox', `0 0 ${width} ${height}`)
        const centerX = bounds ? bounds.left + bounds.width / 2 : width / 2
        const centerY = bounds ? bounds.top + bounds.height / 2 : height / 2
        const maxWidth = Math.max(centerX, width - centerX) * 2 + SOFT_EDGE_MARGIN
        const maxHeight = Math.max(centerY, height - centerY) * 2 + SOFT_EDGE_MARGIN
        const start = performance.now()
        function reveal(now: number) {
          if(cancelled || !opening) return
          const progress = Math.min((now - start) / REVEAL_MS, 1)
          const eased = progress * progress * (3 - 2 * progress)
          const rectWidth = maxWidth * eased
          const rectHeight = maxHeight * eased
          setOpeningRect(opening, centerX, centerY, rectWidth, rectHeight)
          logo.style.opacity = String(1 - Math.min(progress * 3, 1))
          if(progress < 1) frame = requestAnimationFrame(reveal)
          else setFinished(true)
        }
        frame = requestAnimationFrame(reveal)
      }, reducedMotion ? REDUCED_MOTION_DELAY_MS : INTRO_DELAY_MS)
    }
    if(initialTransition.current.closing && !reducedMotion) {
      logo.style.opacity = '0'
      const width = window.innerWidth
      const height = window.innerHeight
      const start = performance.now()
      function closePage(now: number) {
        if(cancelled || !opening) return
        const progress = Math.min((now - start) / CLOSE_MS, 1)
        const remaining = 1 - (progress * progress * (3 - 2 * progress))
        const rectWidth = (width + SOFT_EDGE_MARGIN) * remaining
        const rectHeight = (height + SOFT_EDGE_MARGIN) * remaining
        setOpeningRect(opening, width / 2, height / 2, rectWidth, rectHeight)
        logo.style.opacity = String(Math.max(0, (progress - .5) * 2))
        if(progress < 1) frame = requestAnimationFrame(closePage)
        else { initialTransition.current.onClosed(); startIntro() }
      }
      closePage(start)
    } else {
      if(initialTransition.current.closing) initialTransition.current.onClosed()
      startIntro()
    }
    return () => {
      cancelled = true
      clearTimeout(timer)
      cancelAnimationFrame(frame)
      animation?.cancel()
      if(root) root.inert = wasInert
    }
  }, [])

  useEffect(() => {
    if(finished) {
      const root = document.getElementById('root')
      if(root) root.inert = false
    }
  }, [finished])

  if(finished) return null
  return createPortal(<div className="pageIntro" role="status" aria-label="Открытие страницы TREMOR СКЛАД">
    <svg ref={svgRef} className="pageIntro__curtain" preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <filter id={`${maskId}-soft-edge`} x="-50%" y="-50%" width="200%" height="200%" colorInterpolationFilters="sRGB"><feGaussianBlur stdDeviation="32" /></filter>
        <mask id={maskId} maskUnits="userSpaceOnUse" x="0" y="0" width="100%" height="100%">
          <rect width="100%" height="100%" fill="white" />
          <rect ref={openingRef} width="0" height="0" rx="64" fill="black" filter={`url(#${maskId}-soft-edge)`} />
        </mask>
      </defs>
      <rect width="100%" height="100%" fill="black" mask={`url(#${maskId})`} />
    </svg>
    <div className="pageIntro__logo" ref={logoRef} aria-hidden="true"><span>TREMOR</span><span>СКЛАД</span></div>
  </div>, document.body)
}
