import { useEffect, useRef, useState } from "react";
import {
  Check,
  FileUp,
  Loader2,
  Plus,
  Sparkles,
  Star,
  Trash2,
  X,
} from "lucide-react";

import { LibraryService } from "../../bindings/github.com/tomvokac/parley";
import type { Profile } from "../../bindings/github.com/tomvokac/parley/internal/store/models";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";

const BLANK: Profile = {
  id: 0,
  name: "",
  summary: "",
  people: "",
  notes: "",
  updatedAt: "",
};

export function ContextDialog({
  open,
  onOpenChange,
  onActiveChange,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onActiveChange?: (id: number) => void;
}) {
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [draft, setDraft] = useState<Profile>(BLANK);
  const [activeId, setActiveId] = useState<number>(0);
  const fileRef = useRef<HTMLInputElement | null>(null);

  // AI condense ("minimize") of the notes field: proposed result is previewed
  // and only applied on the user's confirmation, so we never silently rewrite
  // what they typed.
  const [condensing, setCondensing] = useState(false);
  const [condensed, setCondensed] = useState<string | null>(null);
  const [condenseErr, setCondenseErr] = useState<string | null>(null);

  const clearCondense = () => {
    setCondensed(null);
    setCondenseErr(null);
  };

  const refresh = async () => {
    const [list, settings] = await Promise.all([
      LibraryService.ListProfiles(),
      LibraryService.GetSettings(),
    ]);
    setProfiles(list ?? []);
    setActiveId(settings?.activeProfileID ?? 0);
  };

  useEffect(() => {
    if (open) refresh().catch(console.error);
    else clearCondense();
  }, [open]);

  // Ask the active LLM to shrink the notes. The result is held as a preview
  // (applyCondensed commits it); clearCondense drops a stale proposal whenever
  // the underlying notes change, so it can't be applied to the wrong text.
  const condense = async () => {
    const text = draft.notes.trim();
    if (!text || condensing) return;
    setCondensing(true);
    setCondenseErr(null);
    setCondensed(null);
    try {
      const result = await LibraryService.CondenseContext(text);
      setCondensed(result);
    } catch (err) {
      setCondenseErr(err instanceof Error ? err.message : String(err));
    } finally {
      setCondensing(false);
    }
  };

  const applyCondensed = () => {
    if (condensed != null) setDraft((d) => ({ ...d, notes: condensed }));
    clearCondense();
  };

  const save = async () => {
    if (!draft.name.trim()) {
      setDraft({ ...draft, name: "Untitled context" });
    }
    const saved = await LibraryService.SaveProfile({
      ...draft,
      name: draft.name.trim() || "Untitled context",
    });
    setDraft(saved);
    await refresh();
  };

  const remove = async (id: number) => {
    await LibraryService.DeleteProfile(id);
    if (draft.id === id) setDraft(BLANK);
    await refresh();
  };

  const makeActive = async (id: number) => {
    const settings = await LibraryService.GetSettings();
    await LibraryService.SaveSettings({ ...settings, activeProfileID: id });
    setActiveId(id);
    onActiveChange?.(id);
  };

  const importFile = async (file: File) => {
    const text = await file.text();
    setDraft((d) => ({
      ...d,
      name: d.name || file.name.replace(/\.[^.]+$/, ""),
      notes: d.notes ? d.notes + "\n\n" + text : text,
    }));
    clearCondense();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>Meeting context</DialogTitle>
          <DialogDescription>
            Give the assistant background — the agenda, who's attending, and any
            notes. Save profiles to reuse them, and mark one as active for the
            next session.
          </DialogDescription>
        </DialogHeader>

        <div className="grid grid-cols-[200px_1fr] gap-4">
          {/* Saved profiles */}
          <div className="flex min-h-0 flex-col gap-2">
            <div className="flex items-center justify-between">
              <span className="text-xs font-medium text-muted-foreground">
                Saved
              </span>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setDraft(BLANK);
                  clearCondense();
                }}
                title="New context"
              >
                <Plus className="h-4 w-4" />
              </Button>
            </div>
            <ScrollArea className="h-64 rounded-md border">
              <div className="flex flex-col p-1">
                {profiles.length === 0 && (
                  <span className="p-3 text-xs text-muted-foreground/60">
                    No saved contexts yet.
                  </span>
                )}
                {profiles.map((p) => (
                  <button
                    key={p.id}
                    onClick={() => {
                      setDraft(p);
                      clearCondense();
                    }}
                    className={cn(
                      "flex items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent",
                      draft.id === p.id && "bg-accent"
                    )}
                  >
                    {activeId === p.id && (
                      <Star className="h-3 w-3 shrink-0 fill-primary text-primary" />
                    )}
                    <span className="truncate">{p.name}</span>
                  </button>
                ))}
              </div>
            </ScrollArea>
          </div>

          {/* Editor */}
          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="ctx-name">Name</Label>
              <Input
                id="ctx-name"
                value={draft.name}
                placeholder="Q3 planning sync"
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="ctx-summary">Summary / agenda</Label>
              <Textarea
                id="ctx-summary"
                value={draft.summary}
                placeholder="What is this meeting about?"
                onChange={(e) => setDraft({ ...draft, summary: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="ctx-people">People</Label>
              <Input
                id="ctx-people"
                value={draft.people}
                placeholder="Names & roles of attendees"
                onChange={(e) => setDraft({ ...draft, people: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <div className="flex items-center justify-between">
                <Label htmlFor="ctx-notes">Notes</Label>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 gap-1.5 text-xs"
                  disabled={!draft.notes.trim() || condensing}
                  onClick={condense}
                  title="Use the active LLM to shrink these notes while keeping the key facts"
                >
                  {condensing ? (
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <Sparkles className="h-3.5 w-3.5" />
                  )}
                  Condense with AI
                </Button>
              </div>
              <Textarea
                id="ctx-notes"
                value={draft.notes}
                className="min-h-24"
                placeholder="Paste documents, background, agenda detail — anything that helps the assistant follow along…"
                onChange={(e) => {
                  setDraft({ ...draft, notes: e.target.value });
                  clearCondense();
                }}
              />
              {condenseErr && (
                <p className="text-xs text-destructive">{condenseErr}</p>
              )}
              {condensed != null && (
                <div className="flex flex-col gap-2 rounded-md border border-primary/40 bg-primary/5 p-2.5">
                  <div className="flex items-center gap-1.5 text-xs font-medium text-primary">
                    <Sparkles className="h-3.5 w-3.5" />
                    Condensed preview
                    <span className="font-normal text-muted-foreground">
                      {draft.notes.length} → {condensed.length} chars
                    </span>
                  </div>
                  <ScrollArea className="max-h-40 rounded border bg-background">
                    <p className="whitespace-pre-wrap p-2 text-sm leading-relaxed">
                      {condensed}
                    </p>
                  </ScrollArea>
                  <div className="flex justify-end gap-2">
                    <Button size="sm" variant="ghost" onClick={clearCondense}>
                      <X className="h-3.5 w-3.5" /> Discard
                    </Button>
                    <Button size="sm" onClick={applyCondensed}>
                      <Check className="h-3.5 w-3.5" /> Replace notes
                    </Button>
                  </div>
                </div>
              )}
            </div>
            <input
              ref={fileRef}
              type="file"
              accept=".txt,.md,text/plain"
              className="hidden"
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) importFile(f);
                e.target.value = "";
              }}
            />
          </div>
        </div>

        <DialogFooter className="sm:justify-between">
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => fileRef.current?.click()}>
              <FileUp className="h-4 w-4" /> Import .txt
            </Button>
            {draft.id !== 0 && (
              <Button variant="ghost" onClick={() => remove(draft.id)}>
                <Trash2 className="h-4 w-4" /> Delete
              </Button>
            )}
          </div>
          <div className="flex gap-2">
            {draft.id !== 0 && activeId !== draft.id && (
              <Button variant="outline" onClick={() => makeActive(draft.id)}>
                <Star className="h-4 w-4" /> Use for meeting
              </Button>
            )}
            <Button onClick={save}>Save</Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
