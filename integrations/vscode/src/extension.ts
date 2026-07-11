import * as vscode from 'vscode'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { RpcClient } from './rpc'

type Agent = { session_id: string; task_id?: string; title?: string; status: string; needs_input?: boolean; workspace_root?: string }
class AgentItem extends vscode.TreeItem { constructor(readonly agent: Agent) { super(agent.title || agent.session_id, vscode.TreeItemCollapsibleState.None); this.description = agent.needs_input ? 'Needs input' : agent.status; this.tooltip = `${agent.workspace_root || ''}\n${agent.session_id}`; this.contextValue = 'carinaAgent'; this.command = { command: 'carina.attach', title: 'Attach', arguments: [agent] } } }
class Agents implements vscode.TreeDataProvider<AgentItem> {
  private changed = new vscode.EventEmitter<void>(); readonly onDidChangeTreeData = this.changed.event
  constructor(readonly rpc: RpcClient, readonly status: vscode.StatusBarItem) {}
  refresh(): void { this.changed.fire() }
  getTreeItem(i: AgentItem): vscode.TreeItem { return i }
  async getChildren(): Promise<AgentItem[]> { try { const result = await this.rpc.call<{ agents?: Agent[] }>('agent.list'); const agents = Array.isArray(result) ? result as Agent[] : result.agents || []; this.status.text = `$(hubot) Carina ${agents.length}`; this.status.tooltip = 'carina-daemon connected'; return agents.map(a => new AgentItem(a)) } catch (e) { this.status.text = '$(debug-disconnect) Carina offline'; this.status.tooltip = e instanceof Error ? e.message : String(e); return [new AgentItem({ session_id: 'Daemon unavailable', status: 'Start carina-daemon or check carina.socketPath' })] } }
}
export function activate(ctx: vscode.ExtensionContext): void {
  const configured = vscode.workspace.getConfiguration('carina').get<string>('socketPath') || ''
  const rpc = new RpcClient(configured || join(homedir(), '.carina', 'daemon.sock'))
  const status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left); status.show()
  const agents = new Agents(rpc, status); ctx.subscriptions.push(rpc, status, vscode.window.registerTreeDataProvider('carina.agents', agents))
  const command = (name: string, fn: (...a: any[]) => unknown) => ctx.subscriptions.push(vscode.commands.registerCommand(name, fn))
  command('carina.refresh', () => agents.refresh())
  command('carina.attach', async (a: Agent) => { if (a.session_id === 'Daemon unavailable') return; const value = await rpc.call('session.attach', { session_id: a.session_id, since: 0 }); const doc = await vscode.workspace.openTextDocument({ language: 'json', content: JSON.stringify(value, null, 2) }); await vscode.window.showTextDocument(doc) })
  command('carina.steer', async (a: Agent) => { if (!a.task_id) return vscode.window.showWarningMessage('This agent has no active task.'); const message = await vscode.window.showInputBox({ prompt: 'Steering message', ignoreFocusOut: true }); if (message) await rpc.call('task.steer', { task_id: a.task_id, message }) })
  command('carina.approve', async () => { const id = await vscode.window.showInputBox({ prompt: 'Decision ID' }); if (!id) return; const pick = await vscode.window.showWarningMessage(`Resolve approval ${id}?`, { modal: true }, 'Allow once', 'Deny'); if (pick) await rpc.call('task.approval.resolve', { decision_id: id, allow: pick === 'Allow once', scope: 'once', approver: 'vscode' }) })
  command('carina.answer', async () => { const id = await vscode.window.showInputBox({ prompt: 'Question ID' }); const value = id && await vscode.window.showInputBox({ prompt: 'Answer', ignoreFocusOut: true }); if (id && value !== undefined) await rpc.call('task.user.answer', { question_id: id, value }) })
  command('carina.previewPatch', async () => { const id = await vscode.window.showInputBox({ prompt: 'Session ID' }); if (!id) return; const events = await rpc.call<any[]>('session.replay', { session_id: id }); const diff = events.map(e => e.payload?.diff).filter(Boolean).join('\n'); const doc = await vscode.workspace.openTextDocument({ language: 'diff', content: diff || 'No patch diff is available.' }); await vscode.window.showTextDocument(doc, { preview: true }) })
  const interval = Math.max(1000, vscode.workspace.getConfiguration('carina').get<number>('refreshIntervalMs') || 3000); const timer = setInterval(() => agents.refresh(), interval); ctx.subscriptions.push({ dispose: () => clearInterval(timer) })
}
