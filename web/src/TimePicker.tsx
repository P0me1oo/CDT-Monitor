import { useCallback, useId, useLayoutEffect, useRef, useState } from 'react'
import type { CSSProperties, KeyboardEvent as ReactKeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronDown, Clock3, X } from 'lucide-react'
import './time-picker.css'

type Time = { hour: number; minute: number }
type TimePart = 'hour' | 'minute'

type TimePickerProps = {
  label: string
  value: string
  onChange: (value: string) => void
}

const HOURS = Array.from({ length: 24 }, (_, index) => index)
const MINUTES = Array.from({ length: 60 }, (_, index) => index)
const pad = (value: number) => String(value).padStart(2, '0')

function parseTime(value: string): Time | null {
  const match = /^(\d{2}):(\d{2})$/.exec(value)
  if (!match) return null
  const hour = Number(match[1])
  const minute = Number(match[2])
  return hour < 24 && minute < 60 ? { hour, minute } : null
}

function formatTime({ hour, minute }: Time) {
  return `${pad(hour)}:${pad(minute)}`
}

function centerOption(list: HTMLDivElement | null, value: number) {
  const option = list?.querySelector<HTMLButtonElement>(`[data-time-value="${pad(value)}"]`)
  if (!list || !option) return
  list.scrollTop = option.offsetTop - (list.clientHeight - option.clientHeight) / 2
}

export default function TimePicker({ label, value, onChange }: TimePickerProps) {
  const triggerId = useId()
  const valueId = useId()
  const dialogId = useId()
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const hourListRef = useRef<HTMLDivElement>(null)
  const minuteListRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<Time>(() => parseTime(value) ?? { hour: 0, minute: 0 })
  const [position, setPosition] = useState({ top: 0, left: 0, width: 392 })
  const selected = parseTime(value)

  const dismiss = useCallback(() => {
    setOpen(false)
    requestAnimationFrame(() => triggerRef.current?.focus())
  }, [])

  const openPicker = () => {
    setDraft(parseTime(value) ?? { hour: 0, minute: 0 })
    setOpen(true)
  }

  const save = () => {
    onChange(formatTime(draft))
    dismiss()
  }

  useLayoutEffect(() => {
    if (!open) return

    const updatePosition = (event?: Event) => {
      if (event?.target instanceof Node && panelRef.current?.contains(event.target)) return
      const trigger = triggerRef.current
      if (!trigger) return
      const rect = trigger.getBoundingClientRect()
      const width = Math.min(392, window.innerWidth - 24)
      const height = panelRef.current?.offsetHeight ?? 480
      const below = window.innerHeight - rect.bottom - 12
      const above = rect.top - 12
      const placeBelow = below >= height || below >= above
      const proposedTop = placeBelow ? rect.bottom + 8 : rect.top - height - 8
      setPosition({
        top: Math.max(12, Math.min(proposedTop, window.innerHeight - height - 12)),
        left: Math.max(12, Math.min(rect.left, window.innerWidth - width - 12)),
        width,
      })
    }

    updatePosition()
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
  }, [open])

  useLayoutEffect(() => {
    if (!open) return
    centerOption(hourListRef.current, draft.hour)
    centerOption(minuteListRef.current, draft.minute)
  }, [open, draft.hour, draft.minute])

  useLayoutEffect(() => {
    if (!open) return
    hourListRef.current?.querySelector<HTMLButtonElement>(`[data-time-value="${pad(draft.hour)}"]`)?.focus()
  // Focus only when the panel first opens; selection changes keep focus in their own column.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  useLayoutEffect(() => {
    if (!open) return
    const onEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      event.stopPropagation()
      dismiss()
    }
    document.addEventListener('keydown', onEscape, true)
    return () => document.removeEventListener('keydown', onEscape, true)
  }, [open, dismiss])

  useLayoutEffect(() => {
    if (!open || !window.matchMedia('(max-width: 640px)').matches) return
    const previous = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => { document.body.style.overflow = previous }
  }, [open])

  const selectPart = (part: TimePart, next: number, moveFocus = false) => {
    setDraft((current) => ({ ...current, [part]: next }))
    if (moveFocus) {
      const list = part === 'hour' ? hourListRef.current : minuteListRef.current
      list?.querySelector<HTMLButtonElement>(`[data-time-value="${pad(next)}"]`)?.focus()
    }
  }

  const handleOptionKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>, part: TimePart, current: number) => {
    const maximum = part === 'hour' ? 24 : 60
    let next: number | null = null
    switch (event.key) {
      case 'ArrowDown': next = (current + 1) % maximum; break
      case 'ArrowUp': next = (current - 1 + maximum) % maximum; break
      case 'PageDown': next = Math.min(maximum - 1, current + 5); break
      case 'PageUp': next = Math.max(0, current - 5); break
      case 'Home': next = 0; break
      case 'End': next = maximum - 1; break
      case 'ArrowRight':
        if (part === 'hour') {
          event.preventDefault()
          minuteListRef.current?.querySelector<HTMLButtonElement>(`[data-time-value="${pad(draft.minute)}"]`)?.focus()
        }
        return
      case 'ArrowLeft':
        if (part === 'minute') {
          event.preventDefault()
          hourListRef.current?.querySelector<HTMLButtonElement>(`[data-time-value="${pad(draft.hour)}"]`)?.focus()
        }
        return
      default: return
    }
    event.preventDefault()
    selectPart(part, next, true)
  }

  const trapTab = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'Tab') return
    const focusable = Array.from(panelRef.current?.querySelectorAll<HTMLButtonElement>('button:not([disabled]):not([tabindex="-1"])') ?? [])
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (!first || !last) return
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }

  const panelStyle = {
    '--time-picker-top': `${position.top}px`,
    '--time-picker-left': `${position.left}px`,
    '--time-picker-width': `${position.width}px`,
  } as CSSProperties

  return <div className="field time-picker">
    <label htmlFor={triggerId}>{label}</label>
    <button
      ref={triggerRef}
      id={triggerId}
      type="button"
      className={`time-picker__trigger${open ? ' is-open' : ''}`}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-controls={open ? dialogId : undefined}
      aria-describedby={valueId}
      onClick={openPicker}
    >
      <Clock3 size={17} aria-hidden="true" />
      <span id={valueId} className={`time-picker__value${selected ? '' : ' is-empty'}`}>{selected ? formatTime(selected) : '选择时间'}</span>
      <ChevronDown size={17} className="time-picker__chevron" aria-hidden="true" />
    </button>
    {open && createPortal(<div className="time-picker__overlay">
      <div className="time-picker__scrim" aria-hidden="true" onPointerDown={dismiss} />
      <div ref={panelRef} id={dialogId} className="time-picker__panel" style={panelStyle} role="dialog" aria-modal="true" aria-label={`${label}，选择时间`} onKeyDown={trapTab}>
        <div className="time-picker__handle" aria-hidden="true" />
        <header className="time-picker__header">
          <div className="time-picker__heading"><Clock3 size={19} aria-hidden="true" /><span>{label}</span></div>
          <button type="button" className="time-picker__close" aria-label="关闭时间选择器" onClick={dismiss}><X size={18} aria-hidden="true" /></button>
        </header>
        <div className="time-picker__selection">
          <span>所选时间 <small>24 小时制</small></span>
          <output>{pad(draft.hour)}<em>:</em>{pad(draft.minute)}</output>
        </div>
        <div className="time-picker__columns">
          <div className="time-picker__column">
            <div className="time-picker__column-heading"><span>小时</span><small>00–23</small></div>
            <div ref={hourListRef} className="time-picker__options" role="group" aria-label="小时">
              {HOURS.map((hour) => <button key={hour} type="button" data-time-value={pad(hour)} className={`time-picker__option${draft.hour === hour ? ' is-selected' : ''}`} tabIndex={draft.hour === hour ? 0 : -1} aria-label={`${pad(hour)} 时`} aria-pressed={draft.hour === hour} onClick={() => selectPart('hour', hour)} onKeyDown={(event) => handleOptionKeyDown(event, 'hour', hour)}>{pad(hour)}{draft.hour === hour && <Check size={14} aria-hidden="true" />}</button>)}
            </div>
          </div>
          <div className="time-picker__column">
            <div className="time-picker__column-heading"><span>分钟</span><small>00–59</small></div>
            <div ref={minuteListRef} className="time-picker__options" role="group" aria-label="分钟">
              {MINUTES.map((minute) => <button key={minute} type="button" data-time-value={pad(minute)} className={`time-picker__option${draft.minute === minute ? ' is-selected' : ''}`} tabIndex={draft.minute === minute ? 0 : -1} aria-label={`${pad(minute)} 分`} aria-pressed={draft.minute === minute} onClick={() => selectPart('minute', minute)} onKeyDown={(event) => handleOptionKeyDown(event, 'minute', minute)}>{pad(minute)}{draft.minute === minute && <Check size={14} aria-hidden="true" />}</button>)}
            </div>
          </div>
        </div>
        <footer className="time-picker__footer">
          <button type="button" className="time-picker__cancel" onClick={dismiss}>取消</button>
          <button type="button" className="time-picker__confirm" onClick={save}>确定</button>
        </footer>
      </div>
    </div>, document.body)}
  </div>
}
