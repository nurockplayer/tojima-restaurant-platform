import { afterEach, describe, expect, it, vi } from 'vitest';
import { probeStaffAuth, staffLoginURL, staffStepUpURL, submitStaffLogout } from './staff-auth';

afterEach(() => vi.restoreAllMocks());

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

describe('staff cookie auth helpers', () => {
  it('probes a valid signed-in session without caching and with same-origin credentials', async () => {
    const fetch = vi.fn().mockResolvedValue(jsonResponse({ subject_id: 'staff-1', mfa: true, step_up_fresh: false, expires_at: '2026-10-27T00:00:00Z' }));
    await expect(probeStaffAuth(fetch)).resolves.toEqual({ kind: 'signed-in', subjectID: 'staff-1', mfa: true, stepUpFresh: false, expiresAt: '2026-10-27T00:00:00Z' });
    expect(fetch).toHaveBeenCalledWith('/staff/auth/session', expect.objectContaining({ cache: 'no-store', credentials: 'same-origin' }));
  });

  it('recognizes signed-out sessions and treats unsupported or malformed responses as unsupported', async () => {
    expect(await probeStaffAuth(vi.fn().mockResolvedValue(jsonResponse({ error: 'unauthenticated' }, 401)))).toEqual({ kind: 'signed-out' });
    expect(await probeStaffAuth(vi.fn().mockResolvedValue(new Response('<html>spa</html>', { status: 200 })))).toEqual({ kind: 'unsupported' });
    expect(await probeStaffAuth(vi.fn().mockResolvedValue(jsonResponse({ subject_id: 3 })))).toEqual({ kind: 'unsupported' });
    expect(await probeStaffAuth(vi.fn().mockRejectedValue(new Error('offline')))).toEqual({ kind: 'unsupported' });
  });

  it('treats a stalled session request as unsupported after a bounded wait', async () => {
    vi.useFakeTimers();
    const pending = probeStaffAuth(() => new Promise<Response>(() => undefined));
    await vi.advanceTimersByTimeAsync(5000);
    await expect(pending).resolves.toEqual({ kind: 'unsupported' });
    vi.useRealTimers();
  });

  it('builds safe login and step-up return URLs', () => {
    expect(staffLoginURL('/kitchen?mode=fast')).toBe('/staff/auth/login?return=%2Fkitchen%3Fmode%3Dfast');
    expect(staffStepUpURL('//evil.test')).toBe('/staff/auth/step-up?return=%2Fstaff%2F');
    expect(staffLoginURL('staff')).toBe('/staff/auth/login?return=%2Fstaff%2F');
  });

  it('submits logout as a same-origin POST form', () => {
    const formSubmit = vi.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(() => undefined);
    submitStaffLogout(document);
    const form = document.querySelector<HTMLFormElement>('form[action="/staff/auth/logout"]')!;
    expect(form.method).toBe('post');
    expect(form.hidden).toBe(true);
    expect(formSubmit).toHaveBeenCalledOnce();
    form.remove();
  });
});
