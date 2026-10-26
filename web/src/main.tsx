import { createRoot } from 'react-dom/client';
import { demoEnabled, initialStaffViewFor, Surface, surfaceFor } from './surface';
import './shared/tokens.css';
import './ui/styles.css';
import { probeStaffAuth } from './api/staff-auth';

const rootElement = document.getElementById('root')!;
const demo = demoEnabled(rootElement);
const initialView = initialStaffViewFor(window.location.pathname);

const surface = surfaceFor(window.location.pathname);
const render = async () => {
  const staffAuth = surface === 'staff' && !demo ? await probeStaffAuth() : undefined;
  createRoot(rootElement).render(<Surface name={surface} demo={demo} initialView={initialView} staffAuth={staffAuth} />);
};
void render();
