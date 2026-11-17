type SearchableTask = {
  id: string;
  name?: string;
  category_name?: string;
  description: string;
  goal: string;
};

// Polling preserves unchanged task objects. Cache their normalized fields so
// typing does not repeatedly lowercase every task's potentially long goal.
const textByTask = new WeakMap<SearchableTask, string>();

export function matchesTaskQuery(task: SearchableTask, query: string): boolean {
  if (!query) return true;
  let text = textByTask.get(task);
  if (text === undefined) {
    // The search input is single-line, so a newline keeps fields from matching
    // across boundaries while allowing one native substring search per task.
    text = [task.name ?? "", task.category_name ?? "", task.description, task.goal, task.id].join("\n").toLowerCase();
    textByTask.set(task, text);
  }
  return text.includes(query);
}
