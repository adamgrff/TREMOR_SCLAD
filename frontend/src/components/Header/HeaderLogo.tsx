import { useId, useRef, useState } from 'react'
import { Link } from 'react-router'
import { mainActions } from '../../config/mainActions'
import './HeaderLogo.css'

export type HeaderLogoMode = 'static' | 'navigation'

export default function HeaderLogo({ mode }: { mode: HeaderLogoMode }) {
  const [open, setOpen] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)
  const dismissed = useRef(false)
  const hovered = useRef(false)
  const id = useId()
  const interactive = mode === 'navigation'

  return <div className={`headerLogo${open ? ' headerLogo--open' : ''}`}
    onMouseLeave={event => {
      hovered.current = false
      dismissed.current = false
      if (!event.currentTarget.contains(document.activeElement)) setOpen(false)
    }}
    onBlur={event => {
      if (!event.currentTarget.contains(event.relatedTarget)) {
        dismissed.current = false
        if (!hovered.current) setOpen(false)
      }
    }}
    onKeyDown={event => {
      if (event.key === 'Escape' && open) {
        event.preventDefault()
        event.stopPropagation()
        dismissed.current = true
        trigger.current?.focus()
        setOpen(false)
      }
    }}>
    <div className="headerLogo__brand" data-page-intro-logo-target>
      <span className="headerLogo__top" data-brand-top>
        {interactive ? <Link className="headerLogo__home" to="/" aria-label="Главная страница">TREMOR</Link> : 'TREMOR'}
      </span>
      <span className="headerLogo__wordViewport"><span className="headerLogo__word" data-brand-bottom>СКЛАД</span></span>
    </div>
    {interactive && <div className="headerLogo__navigation"
      onMouseEnter={() => { hovered.current = true; dismissed.current = false; setOpen(true) }}
      onFocus={() => { if (!dismissed.current) setOpen(true) }}>
      <button ref={trigger} className="headerLogo__trigger" type="button"
        aria-label="Открыть разделы склада" aria-expanded={open} aria-controls={`${id}-links`}
        onClick={() => { dismissed.current = false; setOpen(true) }} />
      <div className="headerLogo__iconsViewport">
        <nav id={`${id}-links`} className="headerLogo__links" aria-label="Разделы склада" inert={!open}>
          {mainActions.map((action, index) => <Link key={action.path} to={action.path}
            className="headerLogo__link" aria-label={action.navigationTitle ?? action.title}
            aria-describedby={`${id}-tip-${index}`} tabIndex={open ? 0 : -1}
            style={{ transitionDelay: `${open ? 150 + index * 40 : index * 40}ms` }}>
            <img src={action.icon} alt="" aria-hidden="true" />
          </Link>)}
        </nav>
      </div>
      <div className="headerLogo__tooltips">
        {mainActions.map((action, index) => <span key={action.path} id={`${id}-tip-${index}`}
          role="tooltip" className={`headerLogo__tooltip headerLogo__tooltip--${index}`}>
          {action.navigationTitle ?? action.title}
        </span>)}
      </div>
    </div>}
  </div>
}
