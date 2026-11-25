import { createMemo } from 'solid-js';
import { renderMarkdown } from '../markdown';

export default function Markdown(props) {
  const html = createMemo(() => renderMarkdown(props.source));

  return <div class="markdown" innerHTML={html()} />;
}
