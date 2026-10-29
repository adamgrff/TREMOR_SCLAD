import HeaderLogo, { type HeaderLogoMode } from './HeaderLogo'

import './Header.css'

import OzonSyncStatus, { type OzonSyncState } from './OzonSyncStatus'
import iconLogin from '../../assets/home/icon-login.svg'

export type Theme = 'dark' | 'light'

type HeaderProps = {
  logoMode: HeaderLogoMode
  theme: Theme
  onToggleTheme: () => void
  ozonSyncState?: OzonSyncState
  onOzonSync?: () => Promise<void>
}

function Header({ logoMode, theme, onToggleTheme, ozonSyncState, onOzonSync }: HeaderProps) {
  const isLightTheme = theme === 'light'

  return (
    <header className={`header header--${theme}`}>
      <OzonSyncStatus state={ozonSyncState} onSync={onOzonSync} />

      <HeaderLogo mode={logoMode} />

      <div className="headerActions">
        <button
          className="themeToggle"
          type="button"
          aria-label="Переключить тему"
          aria-pressed={isLightTheme}
          onClick={onToggleTheme}
        >
          <span className="themeToggle__thumb">
            {isLightTheme ? '☾' : '☀'}
          </span>
        </button>

        <button
          className="loginButton"
          type="button"
          aria-label="Войти в аккаунт"
        >
          <img
            className="headerIcon"
            src={iconLogin}
            alt=""
            aria-hidden="true"
          />
        </button>
      </div>
    </header>
  )
}

export default Header
