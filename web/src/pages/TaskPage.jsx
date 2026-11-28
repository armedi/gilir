import { Show, createMemo } from 'solid-js';
import TaskDetailPane from '../components/TaskDetailPane';
import { PATHS, useShell } from '../shell';

// A direct link to a deleted or never-existing task is definitive: the ID is
// never reused, so the shell marks it missing after a not_found response (or
// immediately for a tombstoned ID). This view keeps the
// destination URL and makes the queue one click away.
function TaskNotFound(props) {
  return (
    <section class="center-panel" aria-label="Task not found">
      <div class="mobile-back-bar">
        <a class="btn btn-ghost btn-sm" href={PATHS.queue}>
          ← Queue
        </a>
      </div>
      <div class="pane-empty">
        <h2>Task not found</h2>
        <p>
          <Show
            when={props.taskId}
            fallback="This task address does not point to an existing task."
          >
            {(taskId) => `${taskId()} does not exist or was deleted.`}
          </Show>
        </p>
        <a class="btn btn-secondary" href={PATHS.queue}>
          Back to queue
        </a>
      </div>
    </section>
  );
}

export default function TaskPage() {
  const shell = useShell();

  // A fresh task URL has no snapshot until its first detail request completes.
  const loading = createMemo(
    () => shell.detail() == null && shell.detailError() == null && !shell.detailMissing(),
  );

  return (
    <Show
      when={!shell.detailMissing()}
      fallback={<TaskNotFound taskId={shell.selectedTaskId()} />}
    >
      <TaskDetailPane
        taskId={shell.selectedTaskId()}
        detail={shell.detail()}
        loading={loading()}
        error={shell.detailError()}
        projects={shell.projects()}
        onBack={() => shell.goToQueue()}
        onClose={shell.closeTask}
        onOpenMobileInspector={shell.openMobileInspector}
        onRetry={shell.retryTaskDetail}
        onDelete={shell.handleDeleteTask}
        notify={shell.notify}
      />
    </Show>
  );
}
