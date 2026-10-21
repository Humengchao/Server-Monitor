import type { AIAction, AIEvent, AITaskDetail } from '../api/ai';

export interface AIConversationItem { key: string; event?: AIEvent; action?: AIAction }
export interface AIConversationTurn {
  key: string;
  user?: AIEvent;
  process: AIConversationItem[];
  results: AIEvent[];
  latest: boolean;
}

// Event IDs are the server's ordering contract. Timestamps may be identical,
// including when a tool result and the next user turn were saved together.
export function conversationTurns(detail: AITaskDetail): AIConversationTurn[] {
  const actions = new Map(detail.actions.map(action => [action.id, action]));
  const rendered = new Set<string>();
  const items: AIConversationItem[] = [];
  for (const event of [...detail.events].sort((a, b) => a.id - b.id)) {
    const action = event.action_id ? actions.get(event.action_id) : undefined;
    if (action) {
      if (!rendered.has(action.id)) {
        items.push({ key: `action-${action.id}`, action });
        rendered.add(action.id);
      }
    } else items.push({ key: `event-${event.id}`, event });
  }
  // A detail read can see a newly saved action before its accompanying event.
  // Place these by time without changing the relative order of stored events.
  for (const action of detail.actions) {
    if (rendered.has(action.id)) continue;
    const at = items.findIndex(item => Date.parse(item.event?.created_at || item.action!.created_at) > Date.parse(action.created_at));
    items.splice(at < 0 ? items.length : at, 0, { key: `action-${action.id}`, action });
  }
  const turns: AIConversationTurn[] = [];
  for (const item of items) {
    if (item.event?.kind === 'user') {
      turns.push({ key: item.key, user: item.event, process: [], results: [], latest: false });
    } else {
      if (!turns.length) turns.push({ key: 'initial', process: [], results: [], latest: false });
      turns[turns.length - 1].process.push(item);
    }
  }
  if (!turns.length) turns.push({ key: 'initial', process: [], results: [], latest: true });
  turns.forEach((turn, index) => {
    turn.latest = index === turns.length - 1;
    const explicitResults = turn.process.filter(item => item.event?.kind === 'assistant' && item.event.phase === 'final');
    let resultKeys = new Set(explicitResults.map(item => item.key));
    if (!resultKeys.size && (!turn.latest || ['completed', 'awaiting_input'].includes(detail.task.status))) {
      const candidateIndex = turn.process.findLastIndex(item => item.event?.kind === 'assistant');
      const candidate = turn.process[candidateIndex];
      // Legacy events have no phase. Do not promote a known progress message,
      // or one followed by an operation in event order, into a result. Equal
      // timestamps cannot override a later action event's explicit ordering.
      if (candidate?.event && !candidate.event.phase && !turn.process.slice(candidateIndex + 1).some(item => item.action)) {
        resultKeys = new Set([candidate.key]);
      }
    }
    turn.results = turn.process.filter(item => resultKeys.has(item.key)).map(item => item.event!);
    turn.process = turn.process.filter(item => !resultKeys.has(item.key));
  });
  return turns;
}
