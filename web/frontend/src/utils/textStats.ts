import type { ChangeSet, Text } from '@codemirror/state';

const encoder = new TextEncoder();

/** Count only changed spans, including UTF-16 pairs split/joined at a boundary. */
export function updatedUTF8Size(before: Text, changes: ChangeSet, after: Text, previous: number): number {
  let size = previous;
  const oldEdges = new Set<number>(), newEdges = new Set<number>();
  changes.iterChanges((fromA, toA, fromB, toB, inserted) => {
    size += encoder.encode(inserted.toString()).length - encoder.encode(before.sliceString(fromA, toA)).length;
    oldEdges.add(fromA); oldEdges.add(toA);
    newEdges.add(fromB); newEdges.add(toB);
  });
  const splitsPair = (doc: Text, edge: number) => {
    if (edge <= 0 || edge >= doc.length) return false;
    const pair = doc.sliceString(edge - 1, edge + 1);
    return pair.charCodeAt(0) >= 0xd800 && pair.charCodeAt(0) <= 0xdbff
      && pair.charCodeAt(1) >= 0xdc00 && pair.charCodeAt(1) <= 0xdfff;
  };
  // Two lone surrogate encodings take six bytes; their joined code point takes four.
  for (const edge of oldEdges) if (splitsPair(before, edge)) size += 2;
  for (const edge of newEdges) if (splitsPair(after, edge)) size -= 2;
  return size;
}
