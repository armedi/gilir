import { createRouter } from '@solidjs/router';
import QueuePage from './pages/QueuePage';
import TaskPage from './pages/TaskPage';
import NewTaskPage from './pages/NewTaskPage';
import ProjectsPage from './pages/ProjectsPage';
import NotFoundPage from './pages/NotFoundPage';
import { isTaskId } from './shell';

// Explicit route table. The shell (App) is the router's root layout, so it
// stays mounted while these pages swap in its center slot; polling, filters,
// and panel state are not recreated per navigation.
export const Router = createRouter({
  routes: [
    { path: '/', component: QueuePage, info: { screen: 'queue' } },
    { path: '/tasks/new', component: NewTaskPage, info: { screen: 'new-task' } },
    {
      path: '/tasks/:taskId',
      component: TaskPage,
      // Non-canonical IDs fall through to the not-found page instead of
      // querying the API with a malformed ID.
      matchFilters: { taskId: isTaskId },
      info: { screen: 'task' },
    },
    {
      // One definition for both management URLs keeps the view mounted while
      // the add-repository form opens.
      path: ['/projects', '/projects/repos/new'],
      component: ProjectsPage,
      info: { screen: 'projects' },
    },
    { path: '*404', component: NotFoundPage, info: { screen: 'not-found' } },
  ],
});
