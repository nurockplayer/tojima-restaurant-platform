import { createRoot } from 'react-dom/client';
import { App } from './ui/App';
import { StaffApp } from './ui/StaffApp';
import './ui/styles.css';

const Root = window.location.pathname.replace(/\/$/, '') === '/staff' ? StaffApp : App;
createRoot(document.getElementById('root')!).render(<Root />);
