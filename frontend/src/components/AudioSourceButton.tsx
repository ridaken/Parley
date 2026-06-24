import { ChevronDown, Mic, MicOff } from "lucide-react";

import type { StatusEvent } from "../../bindings/github.com/tomvokac/parley";
import { cn } from "@/lib/utils";

/**
 * AudioSourceButton is a Material-style split button: the wide segment shows the
 * current microphone status ("Mic ready" / "Mic active" / "No mic"), and the
 * trailing caret segment opens the audio-source picker. Both segments open the
 * picker, so the whole control reads as "tap to choose your audio sources" while
 * doubling as the at-a-glance status indicator that used to be a separate badge.
 */
export function AudioSourceButton({
  status,
  micConfigured,
  running,
  onOpen,
}: {
  status: StatusEvent;
  micConfigured: boolean;
  running: boolean;
  onOpen: () => void;
}) {
  const micOn = running ? status.micAvailable : micConfigured;
  const label = running
    ? status.micAvailable
      ? "Mic active"
      : "No mic"
    : micConfigured
    ? "Mic ready"
    : "No mic set";

  return (
    <div
      className={cn(
        "inline-flex h-8 items-stretch overflow-hidden rounded-md border transition-colors",
        micOn ? "border-input" : "border-destructive/40"
      )}
    >
      <button
        type="button"
        onClick={onOpen}
        title="Choose audio sources"
        className="flex items-center gap-1.5 pl-2.5 pr-2 text-xs font-medium transition-colors hover:bg-accent"
      >
        {micOn ? (
          <Mic className="h-3.5 w-3.5 text-you" />
        ) : (
          <MicOff className="h-3.5 w-3.5 text-destructive" />
        )}
        <span className={cn(!micOn && "text-destructive")}>{label}</span>
      </button>
      <span aria-hidden className="w-px self-stretch bg-border" />
      <button
        type="button"
        onClick={onOpen}
        aria-label="Choose audio sources"
        title="Choose audio sources"
        className="flex items-center px-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        <ChevronDown className="h-3.5 w-3.5" />
      </button>
    </div>
  );
}
