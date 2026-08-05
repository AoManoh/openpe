export function normalizeEditorText(text: string): string {
  return text.replace(/\r\n?/g, "\n").replace(/\n$/, "");
}

export function editorTextsEqual(
  textarea: boolean,
  current: string,
  expected: string,
): boolean {
  // textarea.value 没有渲染尾换行噪声，必须逐字比较；contenteditable
  // 才对 CRLF 和一个渲染尾换行做归一化。
  return textarea
    ? current === expected
    : normalizeEditorText(current) === normalizeEditorText(expected);
}
