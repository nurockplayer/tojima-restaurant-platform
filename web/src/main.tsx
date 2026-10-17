import { createRoot } from 'react-dom/client';
import { Surface, surfaceFor } from './surface';
import './ui/styles.css';

createRoot(document.getElementById('root')!).render(<Surface name={surfaceFor(window.location.pathname)} />);
