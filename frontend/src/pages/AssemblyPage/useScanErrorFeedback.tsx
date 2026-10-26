import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

export default function useScanErrorFeedback() {
  const [flash, setFlash] = useState(0)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const audio = useRef<AudioContext | null>(null)
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
    if (audio.current) void audio.current.close().catch(() => {})
  }, [])

  function signalError() {
    setFlash((value) => value + 1)
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => setFlash(0), 650)
    try {
      const context = audio.current ?? new AudioContext()
      audio.current = context
      void context.resume().then(() => {
        if (context.state !== 'running') return
        const oscillator = context.createOscillator()
        const gain = context.createGain()
        const start = context.currentTime
        oscillator.type = 'sine'
        oscillator.frequency.setValueAtTime(440, start)
        oscillator.frequency.exponentialRampToValueAtTime(220, start + .16)
        gain.gain.setValueAtTime(.0001, start)
        gain.gain.exponentialRampToValueAtTime(.12, start + .015)
        gain.gain.exponentialRampToValueAtTime(.0001, start + .18)
        oscillator.connect(gain)
        gain.connect(context.destination)
        oscillator.start(start)
        oscillator.stop(start + .19)
        oscillator.onended = () => { oscillator.disconnect(); gain.disconnect() }
      }).catch(() => {})
    } catch { /* Visual feedback remains available when audio is unsupported. */ }
  }

  const overlay = flash > 0 ? createPortal(<div key={flash} className="assemblyScanErrorFlash" aria-hidden="true" />, document.body) : null
  return { signalError, overlay }
}
