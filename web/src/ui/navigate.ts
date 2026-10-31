// Full page navigation, kept in its own module so tests can replace it without touching window.location.
export function navigate(url: string): void {
  window.location.assign(url);
}
