import type { ReactNode, RefObject } from 'react'
import { Button, Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, Icon, Input, Label, LayoutPanelLeft, Monitor, Moon, Plug, Settings2, ShieldCheck, Sun, Switch, X, cn } from '../design-system'
import type { LucideIcon } from '../design-system'
import type { GatewayRole, ThemePreference } from '../lib/harness-preferences'

export type SettingsSection = 'general' | 'interface' | 'gateway'

export type SettingsDialogProps = {
  autoConnect: boolean
  connected: boolean
  connecting: boolean
  connectionLabel: string
  filesOpen: boolean
  gatewayUrl: string
  initialSection: SettingsSection
  onAutoConnectChange: (value: boolean) => void
  onClose: () => void
  restoreFocusRef: RefObject<HTMLElement | null>
  onConnect: () => void
  onFilesOpenChange: (value: boolean) => void
  onGatewayUrlChange: (value: string) => void
  onRoleChange: (value: GatewayRole) => void
  onSectionChange: (value: SettingsSection) => void
  onTerminalOpenChange: (value: boolean) => void
  onThemeChange: (value: ThemePreference) => void
  onTokenChange: (value: string) => void
  open: boolean
  role: GatewayRole
  terminalOpen: boolean
  theme: ThemePreference
  token: string
  urlError?: string
}

const sections: Array<{ id: SettingsSection; label: string; icon: LucideIcon }> = [
  { id: 'general', label: 'General', icon: Settings2 },
  { id: 'interface', label: 'Interface', icon: LayoutPanelLeft },
  { id: 'gateway', label: 'Gateway', icon: Plug },
]

export function SettingsDialog(props: SettingsDialogProps) {
  const {
    autoConnect, connected, connecting, connectionLabel, filesOpen, gatewayUrl,
    initialSection: section, onAutoConnectChange, onClose, onConnect,
    onFilesOpenChange, onGatewayUrlChange, onRoleChange, onSectionChange,
    onTerminalOpenChange, onThemeChange, onTokenChange, open, role,
    terminalOpen, theme, token, urlError, restoreFocusRef,
  } = props

  return <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onClose() }}>
    <DialogContent className="react-settings-dialog" closeOnOverlayClick={!connecting} aria-busy={connecting} onCloseAutoFocus={(event) => {
      event.preventDefault()
      restoreFocusRef.current?.focus({ preventScroll: true })
    }}>
      <DialogHeader className="react-settings-header">
        <div><DialogTitle>Settings</DialogTitle><DialogDescription>Configure the Harness without leaving your current session.</DialogDescription></div>
        <Button variant="ghost" size="icon" type="button" onClick={onClose} aria-label="Close settings" title="Close settings"><Icon icon={X} /></Button>
      </DialogHeader>
      <div className="react-settings-layout">
        <nav className="react-settings-nav" aria-label="Settings sections">
          {sections.map((entry) => <Button key={entry.id} className={cn('react-settings-nav-item', section === entry.id && 'active')} variant="ghost" type="button" aria-current={section === entry.id ? 'page' : undefined} onClick={() => onSectionChange(entry.id)}><Icon icon={entry.icon} /><span>{entry.label}</span></Button>)}
        </nav>
        <div className="react-settings-content">
          {section === 'general' && <GeneralSettings autoConnect={autoConnect} role={role} onAutoConnectChange={onAutoConnectChange} onRoleChange={onRoleChange} />}
          {section === 'interface' && <InterfaceSettings theme={theme} filesOpen={filesOpen} terminalOpen={terminalOpen} onThemeChange={onThemeChange} onFilesOpenChange={onFilesOpenChange} onTerminalOpenChange={onTerminalOpenChange} />}
          {section === 'gateway' && <GatewaySettings connected={connected} connecting={connecting} connectionLabel={connectionLabel} gatewayUrl={gatewayUrl} onGatewayUrlChange={onGatewayUrlChange} token={token} onTokenChange={onTokenChange} role={role} onRoleChange={onRoleChange} urlError={urlError} onConnect={onConnect} />}
        </div>
      </div>
      <footer className={cn('react-settings-footer', section === 'gateway' && 'gateway')}>
        <span>Changes are saved automatically.</span>
        <div className="react-settings-footer-actions">
          <Button variant="outline" type="button" onClick={onClose}>Done</Button>
          {section === 'gateway' && <Button variant="primary" type="button" onClick={onConnect} disabled={connecting || Boolean(urlError)}>{connecting ? 'Connecting...' : connected ? `Reconnect as ${role === 'operator' ? 'Operator' : 'Observer'}` : 'Connect Gateway'}</Button>}
        </div>
      </footer>
    </DialogContent>
  </Dialog>
}

type GeneralSettingsProps = Pick<SettingsDialogProps, 'autoConnect' | 'role' | 'onAutoConnectChange' | 'onRoleChange'>

function GeneralSettings({ autoConnect, role, onAutoConnectChange, onRoleChange }: GeneralSettingsProps) {
  return <section className="react-settings-section" aria-labelledby="settings-general-title">
    <header><span className="eyebrow">HARNESS</span><h2 id="settings-general-title">General</h2><p>Choose how Carina starts and which capability boundary new connections request.</p></header>
    <SettingRow title="Connect on launch" description="Connect silently when the Harness opens and retry transient network disconnects."><Switch checked={autoConnect} onCheckedChange={onAutoConnectChange} aria-label="Connect on launch" /></SettingRow>
    <SettingRow title="Default permission" description="Observer requests read-only access; Operator requests governed workspace writes."><div className="react-settings-segment" role="group" aria-label="Default permission"><Button variant="ghost" className={cn(role === 'observer' && 'active')} aria-pressed={role === 'observer'} onClick={() => onRoleChange('observer')}>Observer</Button><Button variant="ghost" className={cn(role === 'operator' && 'active')} aria-pressed={role === 'operator'} onClick={() => onRoleChange('operator')}>Operator</Button></div></SettingRow>
  </section>
}

type InterfaceSettingsProps = Pick<SettingsDialogProps, 'theme' | 'filesOpen' | 'terminalOpen' | 'onThemeChange' | 'onFilesOpenChange' | 'onTerminalOpenChange'>

function InterfaceSettings({ theme, filesOpen, terminalOpen, onThemeChange, onFilesOpenChange, onTerminalOpenChange }: InterfaceSettingsProps) {
  const themes: Array<{ id: ThemePreference; label: string; icon: LucideIcon }> = [
    { id: 'light', label: 'Light', icon: Sun },
    { id: 'dark', label: 'Dark', icon: Moon },
    { id: 'system', label: 'System', icon: Monitor },
  ]
  return <section className="react-settings-section" aria-labelledby="settings-interface-title">
    <header><span className="eyebrow">DISPLAY</span><h2 id="settings-interface-title">Interface</h2><p>Keep the workspace legible without changing the underlying session.</p></header>
    <div className="react-settings-block"><span>Appearance</span><div className="react-theme-options" role="group" aria-label="Appearance">{themes.map((entry) => <Button key={entry.id} variant="outline" className={cn('react-theme-option', theme === entry.id && 'active')} aria-pressed={theme === entry.id} onClick={() => onThemeChange(entry.id)}><Icon icon={entry.icon} /><span>{entry.label}</span></Button>)}</div></div>
    <SettingRow title="Files panel" description="Restore the workspace file tree when Carina opens."><Switch checked={filesOpen} onCheckedChange={onFilesOpenChange} aria-label="Open files panel on launch" /></SettingRow>
    <SettingRow title="Terminal panel" description="Restore the terminal dock when Carina opens."><Switch checked={terminalOpen} onCheckedChange={onTerminalOpenChange} aria-label="Open terminal panel on launch" /></SettingRow>
  </section>
}

type GatewaySettingsProps = Pick<SettingsDialogProps, 'connected' | 'connecting' | 'connectionLabel' | 'gatewayUrl' | 'onGatewayUrlChange' | 'token' | 'onTokenChange' | 'role' | 'onRoleChange' | 'urlError' | 'onConnect'>

function GatewaySettings({ connected, connecting, connectionLabel, gatewayUrl, onGatewayUrlChange, token, onTokenChange, role, onRoleChange, urlError, onConnect }: GatewaySettingsProps) {
  return <section className="react-settings-section" aria-labelledby="settings-gateway-title">
    <header><span className="eyebrow">CONTROL PLANE</span><h2 id="settings-gateway-title">Gateway</h2><p>Connection details stay separate from everyday session work.</p></header>
    <div className="react-settings-status"><span className={cn('status-dot', connected && 'online')} aria-hidden="true" /><span><strong>{connected ? 'Connected' : connecting ? 'Connecting' : 'Not connected'}</strong><small>{connectionLabel}</small></span></div>
    <SettingRow title="Connection permission" description="Observer is read-only; Operator requests governed workspace writes."><div className="react-settings-segment" role="group" aria-label="Connection permission"><Button variant="ghost" className={cn(role === 'observer' && 'active')} aria-pressed={role === 'observer'} disabled={connecting} onClick={() => onRoleChange('observer')}>Observer</Button><Button variant="ghost" className={cn(role === 'operator' && 'active')} aria-pressed={role === 'operator'} disabled={connecting} onClick={() => onRoleChange('operator')}>Operator</Button></div></SettingRow>
    <Label htmlFor="gateway-url">WebSocket URL<Input id="gateway-url" value={gatewayUrl} onChange={(event) => onGatewayUrlChange(event.target.value)} disabled={connecting} aria-invalid={Boolean(urlError)} aria-describedby={urlError ? 'gateway-url-error' : 'gateway-url-hint'} /></Label>
    <small id={urlError ? 'gateway-url-error' : 'gateway-url-hint'} className={cn('react-settings-field-note', urlError && 'error')} role={urlError ? 'alert' : undefined}>{urlError || 'Secure wss:// endpoints are supported; ws:// is restricted to explicit loopback IPs.'}</small>
    <Label htmlFor="gateway-token">{role === 'operator' ? 'Operator token' : 'Observer token'}<Input id="gateway-token" type="password" value={token} onChange={(event) => onTokenChange(event.target.value)} disabled={connecting} autoComplete="off" /></Label>
    <div className="react-settings-security"><Icon icon={ShieldCheck} /><span>Token storage is limited to this browser tab and cleared when the tab closes.</span></div>
  </section>
}

function SettingRow({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return <div className="react-settings-row"><span><strong>{title}</strong><small>{description}</small></span><div>{children}</div></div>
}
