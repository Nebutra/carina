import { Button, cn } from '../design-system'

export type ConversationTab = 'chat' | 'trajectory' | 'context'

export type ConversationTabsProps = {
  value: ConversationTab
  onChange: (value: ConversationTab) => void
}

const tabs: Array<{ value: ConversationTab; label: string }> = [
  { value: 'chat', label: 'Chat' },
  { value: 'trajectory', label: 'Trajectory' },
  { value: 'context', label: 'Context' },
]

export function ConversationTabs({ value, onChange }: ConversationTabsProps) {
  return <nav className="react-conversation-tabs" aria-label="Conversation views">
    {tabs.map((tab) => <Button key={tab.value} variant="ghost" size="compact" className={cn('react-conversation-tab', value === tab.value && 'active')} onClick={() => onChange(tab.value)} aria-current={value === tab.value ? 'page' : undefined}>{tab.label}</Button>)}
  </nav>
}
