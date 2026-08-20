/** Render the canonical, indented JSON IR with exactly one trailing newline. */
export function renderDocumentJSON(document) {
    return `${JSON.stringify(document, null, 2)}\n`;
}
