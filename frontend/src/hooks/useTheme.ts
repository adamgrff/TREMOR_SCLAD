import { useEffect, useState } from 'react'

export type Theme = 'dark' | 'light'

const THEME_STORAGE_KEY = 'tremor-theme'

export function useTheme(defaultTheme: Theme) {
  const [theme, setTheme] = useState<Theme>(() => {
    try {
      const savedTheme = localStorage.getItem(THEME_STORAGE_KEY)
      if (savedTheme === 'dark' || savedTheme === 'light') return savedTheme
    } catch { /* Theme switching remains usable when browser storage is unavailable. */ }

    return defaultTheme
  })

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    try { localStorage.setItem(THEME_STORAGE_KEY, theme) }
    catch { /* An optional preference must not prevent the page from rendering. */ }
  }, [theme])

  function toggleTheme() {
    setTheme((currentTheme) =>
      currentTheme === 'light' ? 'dark' : 'light',
    )
  }

  return {
    theme,
    toggleTheme,
  }
}
