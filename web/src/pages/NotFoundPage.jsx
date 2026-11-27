import { PATHS } from '../shell';

export default function NotFoundPage() {
  return (
    <section class="center-panel" aria-label="Page not found">
      <div class="mobile-back-bar">
        <a class="btn btn-ghost btn-sm" href={PATHS.queue}>
          ← Queue
        </a>
      </div>
      <div class="pane-empty">
        <h2>Page not found</h2>
        <p>That address is not part of the Gilir dashboard.</p>
        <a class="btn btn-secondary" href={PATHS.queue}>
          Back to queue
        </a>
      </div>
    </section>
  );
}
