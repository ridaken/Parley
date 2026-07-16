import { useState } from "react";
import { FileText, Loader2, MessageSquareText } from "lucide-react";

import { MeetingService } from "../../bindings/github.com/tomvokac/parley";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export function ExportDialog({
  open,
  onOpenChange,
  sessionID,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  sessionID: number;
}) {
  const [busy, setBusy] = useState<"notes" | "transcript" | null>(null);
  const [error, setError] = useState("");

  const run = async (kind: "notes" | "transcript") => {
    setBusy(kind);
    setError("");
    try {
      if (kind === "notes") await MeetingService.ExportMarkdown(sessionID);
      else await MeetingService.ExportTranscriptMarkdown(sessionID);
      onOpenChange(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "The meeting could not be exported.");
    } finally {
      setBusy(null);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!busy) onOpenChange(next);
        if (next) setError("");
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Export meeting</DialogTitle>
          <DialogDescription>
            Choose a polished notes export or the original context followed by the full transcript.
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-3 sm:grid-cols-2">
          <Button
            variant="outline"
            className="h-auto items-start justify-start gap-3 p-4 text-left"
            disabled={busy !== null}
            onClick={() => run("notes")}
          >
            {busy === "notes" ? <Loader2 className="mt-0.5 h-5 w-5 animate-spin" /> : <FileText className="mt-0.5 h-5 w-5" />}
            <span className="flex flex-col gap-1 whitespace-normal">
              <span className="font-medium">Meeting notes</span>
              <span className="text-xs font-normal text-muted-foreground">Summary, action items, topics, and assertions.</span>
            </span>
          </Button>
          <Button
            variant="outline"
            className="h-auto items-start justify-start gap-3 p-4 text-left"
            disabled={busy !== null}
            onClick={() => run("transcript")}
          >
            {busy === "transcript" ? <Loader2 className="mt-0.5 h-5 w-5 animate-spin" /> : <MessageSquareText className="mt-0.5 h-5 w-5" />}
            <span className="flex flex-col gap-1 whitespace-normal">
              <span className="font-medium">Context + transcript</span>
              <span className="text-xs font-normal text-muted-foreground">Pre-meeting context and every timestamped transcript line.</span>
            </span>
          </Button>
        </div>
        {error && <p className="text-sm text-destructive">{error}</p>}
      </DialogContent>
    </Dialog>
  );
}
