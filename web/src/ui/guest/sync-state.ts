export type SyncOrigin = {
  sessionID: string;
  token: string;
};

export type SyncState = {
  origin: SyncOrigin | null;
  lastSyncedAt: number | null;
  degraded: boolean;
  consecutiveFailures: number;
};

export type SyncAction =
  | { type: 'reset'; origin: SyncOrigin | null }
  | { type: 'success'; origin: SyncOrigin; at: number }
  | { type: 'read-failure'; origin: SyncOrigin; status?: number }
  | { type: 'offline'; origin: SyncOrigin }
  | { type: 'online'; origin: SyncOrigin }
  | { type: 'watchdog'; origin: SyncOrigin };

export const initialSyncState: SyncState = {
  origin: null,
  lastSyncedAt: null,
  degraded: false,
  consecutiveFailures: 0,
};

function sameOrigin(left: SyncOrigin | null, right: SyncOrigin): boolean {
  return left !== null && left.sessionID === right.sessionID && left.token === right.token;
}

export function syncReducer(state: SyncState, action: SyncAction): SyncState {
  if (action.type === 'reset') {
    return {
      origin: action.origin,
      lastSyncedAt: null,
      degraded: false,
      consecutiveFailures: 0,
    };
  }

  if (!sameOrigin(state.origin, action.origin)) return state;

  switch (action.type) {
    case 'success':
      return {
        ...state,
        lastSyncedAt: action.at,
        degraded: false,
        consecutiveFailures: 0,
      };
    case 'read-failure': {
      if (action.status !== undefined && (action.status < 500 || action.status > 599)) return state;
      const consecutiveFailures = state.consecutiveFailures + 1;
      return {
        ...state,
        consecutiveFailures,
        degraded: state.degraded || consecutiveFailures >= 2,
      };
    }
    case 'offline':
    case 'watchdog':
      return { ...state, degraded: true };
    case 'online':
      return state;
  }
}
