import { useState } from "react";
import { Send } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// Scope of a live note: "topic" expires when the topic changes; "meeting" persists.
export type NoteScope = "topic" | "meeting";

/**
 * LiveContextBar lets the user inject corrections/context into the running
 * analysis. "This topic" notes correct the immediate discussion and expire when
 * the topic rolls over; "Whole meeting" notes (names, themes) persist all session.
 */
export function LiveContextBar({
  disabled,
  onSend,
}: {
  disabled: boolean;
  onSend: (scope: NoteScope, text: string) => Promise<void>;
}) {
  const [text, setText] = useState("");
  const [scope, setScope] = useState<NoteScope>("topic");
  const [sending, setSending] = useState(false);

  const submit = async () => {
    const t = text.trim();
    if (!t || sending) return;
    setSending(true);
    try {
      await onSend(scope, t);
      setText("");
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="flex flex-col gap-1.5 border-t px-4 py-2.5">
      <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
        <span>Apply to</span>
        <div
          role="tablist"
          aria-label="Note scope"
          className="inline-flex rounded-md bg-muted p-0.5"
        >
          {(
            [
              ["topic", "This topic"],
              ["meeting", "Whole meeting"],
            ] as [NoteScope, string][]
          ).map(([val, label]) => (
            <button
              key={val}
              type="button"
              role="tab"
              aria-selected={scope === val}
              onClick={() => setScope(val)}
              className={cn(
                "rounded-[5px] px-2.5 py-1 font-medium transition-colors",
                scope === val
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground"
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </div>
      <div className="flex items-center gap-2">
        <Input
          value={text}
          disabled={disabled}
          placeholder={
            scope === "topic"
              ? "Correct the current topic, e.g. “this is about margins, not revenue”"
              : "Standing note, e.g. “the client is Acme — A-C-M-E”"
          }
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit();
          }}
        />
        <Button
          size="icon"
          variant="secondary"
          disabled={disabled || sending || !text.trim()}
          onClick={submit}
          title="Send context to the assistant"
        >
          <Send className="h-4 w-4" />
        </Button>
      </div>
    </div>
  );
}
