import '@fontsource-variable/instrument-sans';
import '@fontsource-variable/jetbrains-mono';
import { render } from '@solidjs/web';
import App from './App';
import { Router } from './router';
import './styles.css';

// App is the router's root layout: it mounts once, owns the dashboard shell,
// and the URL selects which page is rendered in its center slot.
render(
  () => <Router>{App}</Router>,
  document.getElementById('root'),
);
