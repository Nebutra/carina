import { useEffect, useRef } from 'react'
import { Button, Icon, IconButton, PanelBottomClose, PanelBottomOpen, Plus, Terminal, X, cn } from '../design-system'

export type TerminalPanelProps = {
  workspace: string
  open: boolean
  onToggle: () => void
}

function basename(value: string) {
  return value.split(/[\\/]/).filter(Boolean).pop() || 'workspace'
}

/** A quiet DSH-style terminal dock reserved for Gateway-backed commands. */
export function TerminalPanel({ workspace, open, onToggle }: TerminalPanelProps) {
  const previousOpen = useRef(open)
  const closeButtonRef = useRef<HTMLButtonElement>(null)
  const openButtonRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (previousOpen.current === open) return
    previousOpen.current = open
    const frame = requestAnimationFrame(() => (open ? closeButtonRef.current : openButtonRef.current)?.focus())
    return () => cancelAnimationFrame(frame)
  }, [open])

  return <section className={cn('react-terminal-panel', !open && 'is-collapsed')} aria-label="Terminal">
    <div className="react-terminal-expanded" aria-hidden={!open}>
      <header className="react-terminal-header"><div className="react-terminal-tabs"><Button variant="ghost" size="compact" className="react-terminal-tab active"><Icon icon={Terminal} /><span>zsh</span><Icon icon={X} /></Button><Button variant="ghost" size="icon-sm" aria-label="New terminal tab" title="New terminal tab"><Icon icon={Plus} /></Button></div><IconButton ref={closeButtonRef} icon={PanelBottomClose} label="Collapse terminal" onClick={onToggle} /></header>
      <div className="react-terminal-body"><strong>~/{basename(workspace)}</strong><span>on Gateway</span><div aria-hidden="true">›</div></div>
    </div>
    <div className="react-terminal-collapsed" aria-hidden={open}><Button ref={openButtonRef} variant="ghost" size="compact" onClick={onToggle} tabIndex={open ? -1 : undefined}><Icon icon={PanelBottomOpen} /><span>Open terminal</span></Button></div>
  </section>
}
