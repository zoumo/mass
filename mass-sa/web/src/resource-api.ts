import type { InitialConfig } from './api'
export interface Association { projectId: string; role: string; ticketId?: string; assignmentId?: string; current: boolean; requested?: InitialConfig }
export interface ResourceSession { sessionId: string; namespace: string; name: string; runtimeRef: string; state: string; initialConfig?: InitialConfig; associations: Association[]; providerModel?: string; providerModelEvidence: string }
export interface Runtime { name: string; namespace: string; type: string; state: string; config: InitialConfig; configSource: string }
export interface SessionEvent { eventId: string; sessionId: string; itemId?: string; type: string; time: string; text: string; payload: Record<string, unknown>; preview: boolean }
export interface History { events: SessionEvent[]; next?: string; cursor: string; earlierCursors: string[]; syncedEventId?: string; observedAt: string; source: string }
