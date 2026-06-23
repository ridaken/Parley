// Maps a transcript speaker label to a Badge variant.
export function speakerVariant(label: string): "you" | "others" | "room" {
  if (label === "You") return "you";
  if (label === "Room") return "room";
  return "others";
}
