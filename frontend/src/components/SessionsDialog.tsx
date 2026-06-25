import { useEffect, useState } from "react";
import { Check, Download, Loader2, Pencil, Play, Trash2, Eye } from "lucide-react";

import { MeetingService } from "../../bindings/github.com/tomvokac/parley";
import type { Session } from "../../bindings/github.com/tomvokac/parley/internal/store/models";
import type { LoadedSession } from "../../bindings/github.com/tomvokac/parley/models";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ScrollArea } from "@/components/ui/scroll-area";

function fmtDate(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/**
 * SessionsDialog browses saved meetings. A meeting can be viewed read-only or
 * resumed (continue recording, appending to the same saved state).
 */
export function SessionsDialog({
  open,
  onOpenChange,
  onView,
  onResume,
  onExport,
  disabled,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onView: (loaded: LoadedSession) => void;
  onResume: (id: number) => void;
  onExport: (id: number) => Promise<string>;
  disabled: boolean; // a meeting is currently recording
}) {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [editTitle, setEditTitle] = useState("");
  const [exportingId, setExportingId] = useState<number | null>(null);

  const refresh = async () => {
    const list = await MeetingService.ListSessions();
    setSessions(list ?? []);
  };

  useEffect(() => {
    if (open) refresh().catch(console.error);
  }, [open]);

  const view = async (id: number) => {
    const loaded = await MeetingService.LoadSession(id);
    onView(loaded);
    onOpenChange(false);
  };

  const resume = (id: number) => {
    onResume(id);
    onOpenChange(false);
  };

  const remove = async (id: number) => {
    await MeetingService.DeleteSession(id);
    await refresh();
  };

  const exportMeeting = async (id: number) => {
    setExportingId(id);
    try {
      await onExport(id);
    } finally {
      setExportingId(null);
    }
  };

  const commitRename = async (id: number) => {
    await MeetingService.RenameSession(id, editTitle.trim() || "Untitled meeting");
    setEditingId(null);
    await refresh();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Saved meetings</DialogTitle>
          <DialogDescription>
            Open a meeting to review its transcript and analysis, or resume it to
            keep recording — useful for meetings that happen in several parts.
          </DialogDescription>
        </DialogHeader>

        <ScrollArea className="max-h-[60vh] rounded-md border">
          <div className="flex flex-col divide-y">
            {sessions.length === 0 && (
              <span className="p-4 text-sm text-muted-foreground/60">
                No saved meetings yet. Start listening and one will appear here.
              </span>
            )}
            {sessions.map((s) => (
              <div key={s.id} className="flex items-center gap-2 p-2.5">
                <div className="min-w-0 flex-1">
                  {editingId === s.id ? (
                    <div className="flex items-center gap-1.5">
                      <Input
                        autoFocus
                        value={editTitle}
                        onChange={(e) => setEditTitle(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") commitRename(s.id);
                          if (e.key === "Escape") setEditingId(null);
                        }}
                        className="h-7"
                      />
                      <Button
                        size="icon"
                        variant="ghost"
                        className="h-7 w-7"
                        onClick={() => commitRename(s.id)}
                      >
                        <Check className="h-4 w-4" />
                      </Button>
                    </div>
                  ) : (
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-sm font-medium">{s.title}</span>
                      <button
                        className="text-muted-foreground/50 hover:text-foreground"
                        onClick={() => {
                          setEditingId(s.id);
                          setEditTitle(s.title);
                        }}
                        title="Rename"
                      >
                        <Pencil className="h-3 w-3" />
                      </button>
                    </div>
                  )}
                  <div className="mt-0.5 text-[11px] text-muted-foreground">
                    {fmtDate(s.startedAt)} · {s.segmentCount} line
                    {s.segmentCount === 1 ? "" : "s"}
                  </div>
                </div>

                <Button size="sm" variant="ghost" onClick={() => view(s.id)}>
                  <Eye className="h-4 w-4" /> View
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8"
                  disabled={exportingId === s.id}
                  onClick={() => exportMeeting(s.id)}
                  title="Export Markdown"
                >
                  {exportingId === s.id ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    <Download className="h-4 w-4" />
                  )}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={disabled}
                  onClick={() => resume(s.id)}
                  title={disabled ? "Stop the current meeting first" : "Continue recording"}
                >
                  <Play className="h-4 w-4" /> Resume
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 text-muted-foreground/60 hover:text-destructive"
                  disabled={disabled}
                  onClick={() => remove(s.id)}
                  title="Delete"
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            ))}
          </div>
        </ScrollArea>
      </DialogContent>
    </Dialog>
  );
}
