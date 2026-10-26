import { useLayoutEffect, useRef, type RefObject } from 'react'

export default function useModalFocusReturn(ref: RefObject<HTMLDialogElement | null>, open = true, confirmedTargetId?: string) {
  const confirmed = useRef(false)
  useLayoutEffect(() => {
    if (!open) return
    confirmed.current = false
    const dialog = ref.current
    const opener = document.activeElement
    return () => {
      if (document.documentElement.dataset.theme !== 'dark') return
      const ownsFocus = dialog?.contains(document.activeElement) || document.activeElement === document.body
      dialog?.close()
      const target = confirmed.current && confirmedTargetId ? document.getElementById(confirmedTargetId) : opener
      if (ownsFocus && target instanceof HTMLElement && target.isConnected) {
        if (confirmed.current && confirmedTargetId && !target.hasAttribute('tabindex')) {
          target.setAttribute('tabindex', '-1')
          target.addEventListener('blur', () => target.removeAttribute('tabindex'), { once: true })
        }
        target.focus({ preventScroll: true })
      }
    }
  }, [ref, open, confirmedTargetId])
  return () => { confirmed.current = true }
}
