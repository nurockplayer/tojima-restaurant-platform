import { createRoot } from 'react-dom/client';
import { demoEnabled, initialStaffViewFor, Surface, surfaceFor } from './surface';
import './ui/styles.css';

const rootElement = document.getElementById('root')!;
const demo = demoEnabled(rootElement);
const initialView = initialStaffViewFor(window.location.pathname);

createRoot(rootElement).render(<Surface name={surfaceFor(window.location.pathname)} demo={demo} initialView={initialView} />);
