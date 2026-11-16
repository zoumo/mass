export interface Project { id: string; name: string }
export interface Page<T> { items: T[]; next?: string; observedAt: string; source: string }
export interface InitialConfig { model?: string; effort?: string }
export interface RouterRecord {
  project: Project; name: string; namespace: string; runtimeRef: string;
  requested: InitialConfig; cwd: string; instructions: string; configSource: string;
  worker: InitialConfig & { runtimeRefs: string[] };
  inbox: { mergeWindowMs: number; maxBatchSize: number; maxPromptBytes: number };
  persisted?: { sessionId?: string; namespace?: string; name?: string; observedState: string; updatedAt: string };
  observation: {
    status: string; attemptedAt: string; lastSuccessAt?: string; stale: boolean; error?: string;
    session?: { sessionId: string; namespace: string; name: string; runtimeRef: string; state: string; initialConfig?: InitialConfig };
  };
}

export async function readAPI<T>(resource: string, params: Record<string, string> = {}, signal?: AbortSignal): Promise<T> {
  const query = new URLSearchParams(Object.entries(params).filter(([, v]) => v !== ''))
  const response = await fetch(`/observer/api/${resource}?${query}`, { signal, credentials: 'same-origin' })
  if (!response.ok) throw new Error(`观测读取失败（HTTP ${response.status}）`)
  return response.json() as Promise<T>
}

export async function readProjects(signal?: AbortSignal): Promise<Project[]> {
  const result: Project[] = []
  let cursor = ''
  do {
    const page = await readAPI<Page<Project>>('projects', { limit: '200', cursor }, signal)
    result.push(...page.items)
    cursor = page.next ?? ''
  } while (cursor)
  return result
}
