/**
 * Column labels for a table row, or null when the row does not line up with the header
 * (conditional columns, colspan rows). Used to render tables as stacked cards on phones.
 */
export function rowLabels(heads: readonly string[], cellCount: number): string[] | null {
  return heads.length > 0 && heads.length === cellCount ? [...heads] : null;
}

function headerText(th: Element): string {
  // Visually hidden headers (e.g. "Actions") and checkbox columns get no label.
  if (th.querySelector('.sr-only, input')) return '';
  return (th.textContent ?? '').trim();
}

/** Copies column headers into `data-label` attributes of body cells (CSS reads them). */
export function labelTables(root: ParentNode): void {
  for (const table of root.querySelectorAll('table')) {
    const heads = [...table.querySelectorAll('thead th')].map(headerText);
    for (const row of table.querySelectorAll('tbody tr')) {
      const cells = [...row.children].filter((c) => c.tagName === 'TD');
      const labels = rowLabels(heads, cells.length);
      if (!labels) continue;
      cells.forEach((cell, i) => {
        if (cell.getAttribute('data-label') !== labels[i])
          cell.setAttribute('data-label', labels[i]);
      });
    }
  }
}
