// FormData.get returns FormDataEntryValue | null (= string | File | null).
// Stringifying a File via String(...) yields '[object File]' — Sonar
// rule typescript:S6551 catches this. This helper picks the string
// payload (or empty) so callers can validate length / format without
// guessing the type at every call site.

export function formField(fd: FormData, key: string): string {
  const raw = fd.get(key);
  return typeof raw === 'string' ? raw : '';
}
