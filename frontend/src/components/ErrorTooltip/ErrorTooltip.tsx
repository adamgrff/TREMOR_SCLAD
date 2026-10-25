import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

export default function ErrorTooltip({ text, label, className }: { text: string; label: string; className: string }) {
  const trigger = useRef<HTMLElement>(null)
  const tip = useRef<HTMLDivElement>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const fade = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [dark, setDark] = useState(() => typeof document !== 'undefined' && document.documentElement.dataset.theme === 'dark')
  const [visible, setVisible] = useState(false)
  const [closing, setClosing] = useState(false)
  const id = useId()
  function clear() { if (timer.current) clearTimeout(timer.current); if (fade.current) clearTimeout(fade.current) }
  function open(delay = 0) {
    clear()
    if (!dark) return
    const show = () => { setClosing(false); setVisible(true) }
    if (delay) timer.current = setTimeout(show, delay)
    else show()
  }
  function leave() {
    clear()
    if (!dark) return
    timer.current = setTimeout(() => {
      setClosing(true)
      fade.current = setTimeout(() => setVisible(false), matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 150)
    }, 150)
  }
  useEffect(() => {
    const observer = new MutationObserver(() => { setDark(document.documentElement.dataset.theme === 'dark'); clear(); setVisible(false) })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => { clear(); observer.disconnect() }
  }, [])
  useLayoutEffect(() => {
    if (!visible || !dark) return
    function position() {
      if (!trigger.current || !tip.current) return
      const anchor = trigger.current.getBoundingClientRect(), box = tip.current.getBoundingClientRect()
      const scale = parseFloat(getComputedStyle(document.documentElement).zoom) || 1
      const inset = 12 * scale, gap = 8 * scale
      const x = Math.max(inset, Math.min(innerWidth - box.width - inset, anchor.left + (anchor.width - box.width) / 2))
      const above = anchor.top - box.height - gap
      const y = Math.max(inset, Math.min(innerHeight - box.height - inset, above >= inset ? above : anchor.bottom + gap))
      tip.current.style.left = `${x / scale}px`
      tip.current.style.top = `${y / scale}px`
    }
    function escape(event: KeyboardEvent) {
      if (event.key !== 'Escape') return
      event.preventDefault(); event.stopPropagation(); clear(); setVisible(false)
    }
    const resize = new ResizeObserver(position)
    if (tip.current) resize.observe(tip.current)
    if (trigger.current) resize.observe(trigger.current)
    position()
    window.addEventListener('resize', position)
    window.addEventListener('scroll', position, true)
    document.addEventListener('keydown', escape, true)
    return () => { resize.disconnect(); window.removeEventListener('resize', position); window.removeEventListener('scroll', position, true); document.removeEventListener('keydown', escape, true) }
  }, [visible, dark])
  return <><i ref={trigger} className={`${className} ui-v1-error-tooltip-trigger`} aria-label={label} title={dark ? undefined : text} tabIndex={dark ? 0 : undefined} aria-describedby={visible && dark ? id : undefined} onMouseEnter={() => open(220)} onMouseLeave={leave} onFocus={() => open()} onBlur={leave}>!</i>{visible && dark && createPortal(<div ref={tip} id={id} role="tooltip" className="ds-v1 ui-v1-error-tooltip" data-closing={closing} onMouseEnter={() => open()} onMouseLeave={leave}>{text}</div>, document.body)}</>
}
