/** Test doubles for DTOs: the spec supplies only the fields it exercises. */
export function partial<T>(value: Record<string, unknown>): T {
  return value as unknown as T;
}
