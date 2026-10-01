import { useEffect, useState } from 'react'
import './ReceivingScrollProgress.css'

const sections = [
  { id: 'receiving-scan', label: 'СКАНИРУЙТЕ ТОВАР' },
  { id: 'receiving-placed', label: 'УЖЕ РАЗМЕЩЕНО' },
  { id: 'receiving-history', label: 'ИСТОРИЯ ПРИЕМОК' },
  { id: 'receiving-catalog', label: 'ТОВАРЫ И КОДЫ' },
]

export default function ReceivingScrollProgress() {
  const [active, setActive] = useState(0)
  const [highlighted, setHighlighted] = useState<number | null>(null)
  const [hovered, setHovered] = useState(false)

  useEffect(() => {
    let frame = 0
    function update() {
      frame = 0
      let closest = 0
      let distance = Infinity
      sections.forEach((section, index) => {
        const bounds = document.getElementById(section.id)?.getBoundingClientRect()
        if (!bounds) return
        // Match the same viewport edge used when navigating to this section.
        const next = section.id === 'receiving-catalog'
          ? Math.abs(bounds.bottom - (window.innerHeight - 24))
          : Math.abs(bounds.top - 24)
        if (next < distance) { distance = next; closest = index }
      })
      setActive(closest)
    }
    function schedule() { if (!frame) frame = requestAnimationFrame(update) }
    const observer = new ResizeObserver(schedule)
    sections.forEach((section) => {
      const element = document.getElementById(section.id)
      if (element) observer.observe(element)
    })
    update()
    window.addEventListener('scroll', schedule, { passive: true })
    window.addEventListener('resize', schedule)
    return () => {
      cancelAnimationFrame(frame)
      observer.disconnect()
      window.removeEventListener('scroll', schedule)
      window.removeEventListener('resize', schedule)
    }
  }, [])

  return <>
    <div className={`receivingScroll__shade${hovered || highlighted !== null ? ' receivingScroll__shade--visible' : ''}`} aria-hidden="true" />
    <nav className="receivingScroll" aria-label="Разделы приёмки" onMouseEnter={() => setHovered(true)} onMouseLeave={() => { setHovered(false); setHighlighted(null) }} onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) setHighlighted(null) }}>
      {sections.map((section, index) => <button
        key={section.id}
        type="button"
        className={`receivingScroll__step${index === active ? ' receivingScroll__step--active' : ''}${index === highlighted ? ' receivingScroll__step--highlighted' : ''}${highlighted !== null && Math.abs(index - highlighted) === 1 ? ' receivingScroll__step--neighbor' : ''}`}
        aria-label={section.label}
        aria-current={index === active ? 'location' : undefined}
        onMouseEnter={() => setHighlighted(index)}
        onFocus={() => setHighlighted(index)}
        onClick={() => document.getElementById(section.id)?.scrollIntoView({ behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth', block: section.id === 'receiving-catalog' ? 'end' : 'start' })}
      >
        <span className="receivingScroll__line" aria-hidden="true" />
        <span className="receivingScroll__label" aria-hidden="true">{section.label}</span>
      </button>)}
    </nav>
  </>
}
