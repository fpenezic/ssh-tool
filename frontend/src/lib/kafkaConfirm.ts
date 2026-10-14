// Confirmation for destructive Kafka actions (delete / truncate a topic,
// delete a group). On a connection marked sensitive the name has to be typed,
// like the guard GitHub puts on deleting a repository; elsewhere a plain
// danger confirm.

import { showConfirm } from "./confirmModal.svelte.ts";
import { showPrompt } from "./promptModal.svelte.ts";
import { toast } from "./toast.svelte.ts";

export async function confirmKafkaDestructive(
  title: string,
  name: string,
  sensitive: boolean,
  message: string,
): Promise<boolean> {
  if (!sensitive) {
    return showConfirm({ title, message, okLabel: title, danger: true });
  }
  const typed = await showPrompt(`${message}\n\nThis connection is marked sensitive. Type "${name}" to confirm.`);
  if (typed === null) return false;
  if (typed !== name) {
    toast.err("The name did not match - nothing was changed");
    return false;
  }
  return true;
}
