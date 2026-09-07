import type { ReactNode } from 'react'

export type Metric = { label: string; value: ReactNode }

export function MetricGroup({ metrics, className = 'react-stat-grid' }: { metrics: Metric[]; className?: string }) {
  return <div className={className}>{metrics.map((metric) => <div className="react-stat" key={metric.label}><span>{metric.label}</span><strong>{metric.value}</strong></div>)}</div>
}
