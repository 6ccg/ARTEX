import type { LogLine } from "./types";

export function appendLiveLog(lines: LogLine[], incoming: LogLine): LogLine[] {
  if (lines.some((line) => (incoming.db_id ? line.db_id === incoming.db_id : line.seq === incoming.seq))) return lines;
  return [...lines, incoming].slice(-3000);
}

export function prependLogHistory(lines: LogLine[], older: LogLine[]): LogLine[] {
  const seen = new Set<number>();
  return [...older, ...lines]
    .filter((line) => {
      if (!line.db_id) return true;
      if (seen.has(line.db_id)) return false;
      seen.add(line.db_id);
      return true;
    })
    .slice(0, 5000);
}
