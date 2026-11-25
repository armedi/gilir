const PREFIX = 'gilir:draft:';

export function draftKey(...parts) {
  return `${PREFIX}${parts.map((part) => encodeURIComponent(String(part ?? 'none'))).join(':')}`;
}

export function loadDraft(key) {
  try {
    const raw = window.localStorage.getItem(key);
    return raw == null ? null : JSON.parse(raw);
  } catch {
    return null;
  }
}

export function saveDraft(key, value) {
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    return;
  }
}

export function clearDraft(key) {
  try {
    window.localStorage.removeItem(key);
  } catch {
    return;
  }
}
