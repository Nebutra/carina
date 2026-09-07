import type { ReactNode } from 'react'

export type PageHeaderProps = {
  eyebrow: string
  title: string
  lead: string
  actions?: ReactNode
}

/** Stable page heading grammar shared by operational surfaces. */
export function PageHeader({ eyebrow, title, lead, actions }: PageHeaderProps) {
  return <header className="react-surface-heading">
    <div>
      <span className="eyebrow">{eyebrow}</span>
      <h2>{title}</h2>
      <p>{lead}</p>
    </div>
    {actions ? <div className="react-page-actions">{actions}</div> : null}
  </header>
}
