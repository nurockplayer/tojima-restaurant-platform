import { describe, expect, it } from 'vitest';
import { initialSyncState, syncReducer, type SyncAction, type SyncOrigin, type SyncState } from './sync-state';

const origin: SyncOrigin = { sessionID: 'session-a', token: 'grant-a' };
const otherSession: SyncOrigin = { sessionID: 'session-b', token: 'grant-a' };
const otherGrant: SyncOrigin = { sessionID: 'session-a', token: 'grant-b' };

function stateFor(expectedOrigin: SyncOrigin = origin): SyncState {
  return { ...initialSyncState, origin: expectedOrigin };
}

function reduce(state: SyncState, ...actions: SyncAction[]): SyncState {
  return actions.reduce(syncReducer, state);
}

describe('syncReducer', () => {
  it('starts empty and reset scopes a fresh state to the current session and grant', () => {
    expect(initialSyncState).toEqual({ origin: null, lastSyncedAt: null, degraded: false, consecutiveFailures: 0 });
    const dirty = { origin, lastSyncedAt: 100, degraded: true, consecutiveFailures: 3 };
    expect(syncReducer(dirty, { type: 'reset', origin: otherGrant })).toEqual({
      origin: otherGrant, lastSyncedAt: null, degraded: false, consecutiveFailures: 0,
    });
    expect(syncReducer(dirty, { type: 'reset', origin: null })).toEqual(initialSyncState);
  });

  it('records a successful accepted sync and clears degradation and failures', () => {
    const degraded = { origin, lastSyncedAt: 100, degraded: true, consecutiveFailures: 2 };
    expect(syncReducer(degraded, { type: 'success', origin, at: 250 })).toEqual({
      origin, lastSyncedAt: 250, degraded: false, consecutiveFailures: 0,
    });
  });

  it('degrades on two consecutive network or server read failures', () => {
    const afterNetworkFailure = syncReducer(stateFor(), { type: 'read-failure', origin });
    expect(afterNetworkFailure).toMatchObject({ degraded: false, consecutiveFailures: 1, lastSyncedAt: null });
    expect(syncReducer(afterNetworkFailure, { type: 'read-failure', origin, status: 503 })).toMatchObject({
      degraded: true, consecutiveFailures: 2, lastSyncedAt: null,
    });
    expect(syncReducer(stateFor(), { type: 'read-failure', origin, status: 599 })).toMatchObject({
      degraded: false, consecutiveFailures: 1,
    });
  });

  it.each([400, 401, 403, 409, 410, 499, 600])('does not record excluded HTTP failure %i', (status) => {
    const state = stateFor();
    expect(syncReducer(state, { type: 'read-failure', origin, status })).toBe(state);
  });

  it.each([
    ['different session', otherSession],
    ['replaced grant in the same session', otherGrant],
  ] as const)('ignores late events from a %s', (_label, staleOrigin) => {
    const state = { ...stateFor(), lastSyncedAt: 100, consecutiveFailures: 1 };
    const events: SyncAction[] = [
      { type: 'success', origin: staleOrigin, at: 200 },
      { type: 'read-failure', origin: staleOrigin, status: 503 },
      { type: 'offline', origin: staleOrigin },
      { type: 'online', origin: staleOrigin },
      { type: 'watchdog', origin: staleOrigin },
    ];
    for (const event of events) expect(syncReducer(state, event)).toBe(state);
  });

  it('offline and watchdog degrade the matching origin, while online does not clear it', () => {
    const offline = syncReducer(stateFor(), { type: 'offline', origin });
    expect(offline.degraded).toBe(true);
    expect(syncReducer(offline, { type: 'online', origin })).toBe(offline);
    expect(syncReducer(stateFor(), { type: 'watchdog', origin })).toMatchObject({ degraded: true });
  });

  it('keeps an earlier accepted time through failures and clears only on accepted success', () => {
    const synced = { ...stateFor(), lastSyncedAt: 100 };
    const failed = reduce(synced,
      { type: 'read-failure', origin, status: 503 },
      { type: 'read-failure', origin, status: 500 },
      { type: 'online', origin },
    );
    expect(failed).toMatchObject({ lastSyncedAt: 100, degraded: true, consecutiveFailures: 2 });
    expect(syncReducer(failed, { type: 'success', origin, at: 400 })).toEqual({
      origin, lastSyncedAt: 400, degraded: false, consecutiveFailures: 0,
    });
  });

  it('ignores old-grant events after same-session replacement and clears on the next current-grant success', () => {
    const oldGrantSynced = syncReducer(stateFor(), { type: 'success', origin, at: 100 });
    const replaced = syncReducer(oldGrantSynced, { type: 'reset', origin: otherGrant });
    const degraded = reduce(replaced,
      { type: 'success', origin: otherGrant, at: 200 },
      { type: 'watchdog', origin: otherGrant },
    );
    const oldGrantEvents: SyncAction[] = [
      { type: 'success', origin, at: 300 },
      { type: 'read-failure', origin, status: 503 },
      { type: 'offline', origin },
      { type: 'watchdog', origin },
    ];

    for (const event of oldGrantEvents) expect(syncReducer(degraded, event)).toBe(degraded);
    expect(syncReducer(degraded, { type: 'success', origin: otherGrant, at: 400 })).toEqual({
      origin: otherGrant, lastSyncedAt: 400, degraded: false, consecutiveFailures: 0,
    });
  });
});
