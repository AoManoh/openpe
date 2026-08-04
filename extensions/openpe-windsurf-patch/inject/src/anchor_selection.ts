export function selectRightmostByGroup<Group, Value>(
  candidates: ReadonlyArray<{ group: Group; value: Value; right: number }>,
): Value[] {
  const selected = new Map<Group, { value: Value; right: number }>();
  for (const candidate of candidates) {
    const current = selected.get(candidate.group);
    if (!current || candidate.right > current.right) {
      selected.set(candidate.group, {
        value: candidate.value,
        right: candidate.right,
      });
    }
  }
  return Array.from(selected.values(), (candidate) => candidate.value);
}
