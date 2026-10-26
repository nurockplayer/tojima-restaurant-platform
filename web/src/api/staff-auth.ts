export type StaffAuthState =
  | { kind: 'unsupported' }
  | { kind: 'unavailable' }
  | { kind: 'signed-out' }
  | { kind: 'signed-in'; subjectID: string; mfa: boolean; stepUpFresh: boolean; expiresAt: string };

export async function probeStaffAuth(fetchImpl: typeof fetch = fetch): Promise<StaffAuthState> {
  const controller = new AbortController();
  let timeout: ReturnType<typeof setTimeout> | undefined;
  // The timeout bounds the whole probe, body included: a stalled body must not leave the staff surface blank.
  const timedOut = new Promise<StaffAuthState>((resolve) => {
    timeout = setTimeout(() => { controller.abort(); resolve({ kind: 'unavailable' }); }, 5000);
  });
  try {
    return await Promise.race([readStaffSession(fetchImpl, controller.signal), timedOut]);
  } finally {
    if (timeout !== undefined) clearTimeout(timeout);
  }
}

async function readStaffSession(fetchImpl: typeof fetch, signal: AbortSignal): Promise<StaffAuthState> {
  let response: Response;
  try {
    response = await fetchImpl('/staff/auth/session', {
      method: 'GET', cache: 'no-store', credentials: 'same-origin', signal,
      headers: { accept: 'application/json' },
    });
  } catch {
    return { kind: 'unavailable' };
  }
  if (response.status === 404) return { kind: 'unsupported' };
  if (response.status === 401) return { kind: 'signed-out' };
  if (response.status < 200 || response.status >= 300) return { kind: 'unavailable' };
  let text: string;
  try { text = await response.text(); } catch { return { kind: 'unavailable' }; }
  let body: unknown;
  try { body = JSON.parse(text); } catch { return { kind: 'unsupported' }; }
  if (!isSessionBody(body)) return { kind: 'unsupported' };
  return {
    kind: 'signed-in', subjectID: body.subject_id, mfa: body.mfa,
    stepUpFresh: body.step_up_fresh, expiresAt: body.expires_at,
  };
}

export function staffLoginURL(returnPath: string): string {
  return authURL('/staff/auth/login', returnPath);
}

export function staffStepUpURL(returnPath: string): string {
  return authURL('/staff/auth/step-up', returnPath);
}

function authURL(path: string, returnPath: string): string {
  const safePath = returnPath.startsWith('/') && !returnPath.startsWith('//') ? returnPath : '/staff/';
  return `${path}?return=${encodeURIComponent(safePath)}`;
}

export function submitStaffLogout(doc: Document = document): void {
  const form = doc.createElement('form');
  form.method = 'post';
  form.action = '/staff/auth/logout';
  form.hidden = true;
  doc.body.append(form);
  form.submit();
}

function isSessionBody(value: unknown): value is { subject_id: string; mfa: boolean; step_up_fresh: boolean; expires_at: string } {
  if (!value || typeof value !== 'object') return false;
  const body = value as Record<string, unknown>;
  return typeof body.subject_id === 'string' && typeof body.mfa === 'boolean'
    && typeof body.step_up_fresh === 'boolean' && typeof body.expires_at === 'string';
}

