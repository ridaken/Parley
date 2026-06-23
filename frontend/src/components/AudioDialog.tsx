import { useEffect, useState } from "react";
import { Check, Mic, RefreshCw, Speaker } from "lucide-react";

import { LibraryService, MeetingService } from "../../bindings/github.com/tomvokac/parley";
import type { DeviceInfo } from "../../bindings/github.com/tomvokac/parley/internal/audio/models";
import type { CaptureSource } from "../../bindings/github.com/tomvokac/parley/internal/store/models";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";

const ROLES = [
  { value: "You", label: "Me (my mic)" },
  { value: "Others", label: "Others (remote)" },
  { value: "Room", label: "In-person / mixed" },
];

// kind used for capture: inputs are captured directly, outputs via loopback.
const captureKind = (d: DeviceInfo) => (d.kind === "input" ? "input" : "loopback");
const keyOf = (d: DeviceInfo) => `${captureKind(d)}:${d.id}`;

export function AudioDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const [devices, setDevices] = useState<DeviceInfo[]>([]);
  // key -> role label for selected sources
  const [selected, setSelected] = useState<Record<string, string>>({});

  const load = async () => {
    const [devs, settings] = await Promise.all([
      MeetingService.ListDevices(),
      LibraryService.GetSettings(),
    ]);
    setDevices(devs ?? []);
    const sel: Record<string, string> = {};
    for (const cs of settings?.captureSources ?? []) {
      sel[`${cs.kind}:${cs.id}`] = cs.label;
    }
    setSelected(sel);
  };

  useEffect(() => {
    if (open) load().catch(console.error);
  }, [open]);

  const toggle = (d: DeviceInfo) => {
    const k = keyOf(d);
    setSelected((prev) => {
      const next = { ...prev };
      if (next[k]) {
        delete next[k];
      } else {
        next[k] = d.kind === "input" ? "You" : "Others";
      }
      return next;
    });
  };

  const setRole = (d: DeviceInfo, role: string) =>
    setSelected((prev) => ({ ...prev, [keyOf(d)]: role }));

  const save = async () => {
    const sources: CaptureSource[] = devices
      .filter((d) => selected[keyOf(d)])
      .map((d) => ({
        id: d.id,
        name: d.name,
        kind: captureKind(d),
        label: selected[keyOf(d)],
      }));
    const settings = await LibraryService.GetSettings();
    await LibraryService.SaveSettings({ ...settings, captureSources: sources });
    onOpenChange(false);
  };

  const reset = () => setSelected({});

  const inputs = devices.filter((d) => d.kind === "input");
  const outputs = devices.filter((d) => d.kind === "output");

  const DeviceRow = ({ d }: { d: DeviceInfo }) => {
    const k = keyOf(d);
    const on = !!selected[k];
    return (
      <div
        className={cn(
          "flex items-center gap-2 rounded-md px-2 py-1.5",
          on && "bg-accent/50"
        )}
      >
        <button
          onClick={() => toggle(d)}
          className={cn(
            "flex h-4 w-4 shrink-0 items-center justify-center rounded border",
            on ? "border-primary bg-primary text-primary-foreground" : "border-input"
          )}
        >
          {on && <Check className="h-3 w-3" />}
        </button>
        <span className="flex-1 truncate text-sm">
          {d.name}
          {d.isDefault && (
            <span className="ml-1 text-[10px] text-muted-foreground">(default)</span>
          )}
        </span>
        {on && (
          <Select value={selected[k]} onValueChange={(v) => setRole(d, v)}>
            <SelectTrigger className="h-7 w-40 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {ROLES.map((r) => (
                <SelectItem key={r.value} value={r.value}>
                  {r.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </div>
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Audio sources</DialogTitle>
          <DialogDescription>
            Choose which devices to capture and how each is labelled. Mark your own
            mic as <b>Me</b> and the meeting audio as <b>Others</b>. If a single mic
            records a whole in-person conversation where your voice can't be told
            apart, choose <b>In-person / mixed</b> — those lines are labelled "Room".
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
              <Mic className="h-3.5 w-3.5" /> Microphones & inputs
            </div>
            <ScrollArea className="max-h-40 rounded-md border">
              <div className="flex flex-col p-1">
                {inputs.length === 0 && (
                  <span className="p-3 text-xs text-muted-foreground/60">
                    No input devices detected.
                  </span>
                )}
                {inputs.map((d) => (
                  <DeviceRow key={d.id} d={d} />
                ))}
              </div>
            </ScrollArea>
          </div>

          <div className="flex flex-col gap-1.5">
            <div className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
              <Speaker className="h-3.5 w-3.5" /> System output (captured via loopback)
            </div>
            <ScrollArea className="max-h-40 rounded-md border">
              <div className="flex flex-col p-1">
                {outputs.length === 0 && (
                  <span className="p-3 text-xs text-muted-foreground/60">
                    No output devices detected.
                  </span>
                )}
                {outputs.map((d) => (
                  <DeviceRow key={d.id} d={d} />
                ))}
              </div>
            </ScrollArea>
          </div>
        </div>

        <DialogFooter className="sm:justify-between">
          <Button variant="ghost" onClick={load} title="Re-scan devices">
            <RefreshCw className="h-4 w-4" /> Refresh
          </Button>
          <div className="flex gap-2">
            <Button variant="outline" onClick={reset}>
              Use defaults
            </Button>
            <Button onClick={save}>Save</Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
