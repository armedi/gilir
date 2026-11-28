// The namespace carries a version because draft payloads are tied to the
// stored task shape. After the schema reset task IDs can be reused, so a draft
// saved for an old GILIR-1 must not resurface on a new task that carries the
// same ID. Bumping this prefix orphans every earlier draft.
const PREFIX = 'gilir:draft:v2:';

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
