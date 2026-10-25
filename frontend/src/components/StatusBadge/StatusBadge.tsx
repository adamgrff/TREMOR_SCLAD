import { useLayoutEffect, useRef } from 'react'

type Props = { children: string; tone: 'waiting' | 'success' | 'error' | 'active' | 'neutral'; className?: string }

export default function StatusBadge({ children, tone, className = '' }: Props) {
  const ref = useRef<HTMLSpanElement>(null)
  useLayoutEffect(() => {
    const badge = ref.current
    const parent = badge?.parentElement
    if (!badge || !parent) return
    let disposed = false
    let frame = 0
    function measure() {
      if (disposed || !badge) return
      badge.style.removeProperty('width')
      if (document.documentElement.dataset.theme !== 'dark') return
      const style = getComputedStyle(badge)
      const scale = badge.getBoundingClientRect().width / parseFloat(style.width)
      if (!Number.isFinite(scale) || scale <= 0) return
      const range = document.createRange()
      range.selectNodeContents(badge)
      const longest = Math.max(0, ...Array.from(range.getClientRects(), rect => rect.width / scale))
      const padding = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight)
      // Round upward to layout precision so shrinking does not rewrap a line.
      badge.style.width = `${Math.ceil((longest + padding) * 64) / 64}px`
    }
    function schedule() {
      if (!disposed && !frame) frame = requestAnimationFrame(() => { frame = 0; measure() })
    }
    const resize = new ResizeObserver(schedule)
    resize.observe(parent)
    const theme = new MutationObserver(measure)
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    document.fonts.addEventListener('loadingdone', schedule)
    void document.fonts.ready.then(measure)
    measure()
    return () => {
      disposed = true
      cancelAnimationFrame(frame)
      resize.disconnect()
      theme.disconnect()
      document.fonts.removeEventListener('loadingdone', schedule)
    }
  }, [children])
  return <span ref={ref} className={`${className} ds-v1 ui-v1-badge ui-v1-badge--${tone}`}>{children}</span>
}
