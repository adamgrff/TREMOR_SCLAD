const SESSION_KEY = 'tremor-site-opened'

// A direct internal-route opening consumes the welcome too. Storage denial
// skips the optional intro rather than repeating it on every reload.
export function shouldShowWelcomeIntro(
  pathname: string,
  getStorage: () => Pick<Storage, 'getItem' | 'setItem'> = () => window.sessionStorage,
): boolean {
  try {
    const storage = getStorage()
    const alreadyOpened = storage.getItem(SESSION_KEY) !== null
    storage.setItem(SESSION_KEY, '1')
    return !alreadyOpened && pathname === '/'
  } catch {
    return false
  }
}
