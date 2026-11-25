import { For, Show } from 'solid-js';
import RepoForm from './RepoForm';

export default function ReposPanel(props) {
  return (
    <section class="panel repos-panel" aria-labelledby="repos-title">
      <div class="panel-inner">
        <div class="panel-header">
          <h2 id="repos-title" class="panel-title">
            Repositories
          </h2>
          <span class="panel-count">{props.repos.length}</span>
        </div>

        <Show when={props.error}>
          <div class="inline-alert" role="alert">
            <span>{props.error}</span>
            <button class="btn btn-ghost btn-sm" type="button" onClick={props.onRetry}>
              Retry
            </button>
          </div>
        </Show>

        <Show when={props.repos.length > 0} fallback={<p class="muted-small">No repositories yet.</p>}>
          <ul class="repo-list">
            <For each={props.repos}>
              {(repo) => (
                <li class="repo-item" title={repo.path}>
                  {repo.path}
                </li>
              )}
            </For>
          </ul>
        </Show>

        <Show when={!props.loading || props.repos.length > 0}>
          <div class="panel-divider" />
          <RepoForm projectId={props.projectId} onAdded={props.onAdded} />
        </Show>
      </div>
    </section>
  );
}
