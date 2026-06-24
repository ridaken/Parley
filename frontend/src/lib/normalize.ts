// normKey produces a stable identity for a free-text item (suggestion, assertion,
// action item) so pinning, dismissing, de-duplication, and "new item" highlighting
// all agree on what counts as "the same" item across analysis passes. Every caller
// must use this same normalizer or those features silently disagree.
export const normKey = (s: string) => s.trim().toLowerCase().replace(/\s+/g, " ");
