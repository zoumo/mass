import type { RouterRecord } from './api'
import type { TriggerDelivery } from './trigger-types'
export interface TicketRecord {
 id: string; projectId: string; title: string; businessState: string;
 parentTicketId?: string; rootTicketId: string; depth: number;
 currentAssignmentId?: string; currentAssignmentStatus: string; currentSessionId?: string; createdAt: string; updatedAt: string;
}
export interface TicketAssignment {
 id: string; current: boolean; workerName: string; sessionId?: string; runtimeRef: string;
 requested: { model?: string; effort?: string }; status: string; executionId?: string; executionState?: string;
 awaitingResumedInput: boolean; reconciliationRequired: boolean; reconciliationReason?: string;
 createdAt: string; updatedAt: string; stoppedAt?: string; observation: RouterRecord['observation'];
}
export interface TicketReport {
 id: string; body: string;
 assignmentId: string; sessionId?: string; sourceEventId: string; sourceItemId?: string; sourceTurnId?: string;
 structured: boolean; parseError?: string; reportedAt: string;
}
export interface TicketDetail extends TicketRecord {
 objective: string; acceptance: string; createdByAssignment?: string; createdBySessionId?: string;
 parent?: TicketRecord; parentUnavailable: boolean; children: TicketRecord[]; descendants: TicketRecord[];
 assignments: TicketAssignment[]; reports: TicketReport[];
 directTriggers: { triggerId: string; summary: string; source: string; threadId: string; deliveries: TriggerDelivery[] }[];
 internalInputs: { inboxId: string; kind: string; summary: string; childTicketId?: string; childState?: string; revision: number;
 state: string; targetResolution: string; assignmentId?: string; sessionId?: string; executionId?: string; acceptedAt: string; batches: TriggerDelivery['batches'] }[];
 observedAt: string; source: string;
}
