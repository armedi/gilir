import DOMPurify from 'dompurify';
import { marked } from 'marked';

const FORBIDDEN_TAGS = [
  'script',
  'style',
  'iframe',
  'object',
  'embed',
  'form',
  'input',
  'button',
  'textarea',
  'select',
  'option',
  'link',
  'meta',
  'base',
  'img',
  'picture',
  'source',
  'video',
  'audio',
  'svg',
  'math',
];

const FORBIDDEN_ATTRIBUTES = ['style', 'srcset', 'poster', 'background', 'srcdoc'];

let hookInstalled = false;

function installHook() {
  if (hookInstalled) return;
  DOMPurify.addHook('afterSanitizeAttributes', (node) => {
    if (node.tagName !== 'A') return;
    const href = node.getAttribute('href');
    if (!href) {
      node.removeAttribute('target');
      return;
    }
    if (/^https?:\/\//i.test(href)) {
      node.setAttribute('target', '_blank');
    } else {
      node.removeAttribute('target');
    }
    node.setAttribute('rel', 'noopener noreferrer');
  });
  hookInstalled = true;
}

export function renderMarkdown(source) {
  if (typeof source !== 'string' || !source.trim()) return '';
  installHook();
  const html = marked.parse(source, {
    async: false,
    breaks: true,
    gfm: true,
  });
  return DOMPurify.sanitize(html, {
    USE_PROFILES: { html: true },
    FORBID_TAGS: FORBIDDEN_TAGS,
    FORBID_ATTR: FORBIDDEN_ATTRIBUTES,
    ALLOW_DATA_ATTR: false,
  });
}
