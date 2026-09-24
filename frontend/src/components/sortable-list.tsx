import { useState, type HTMLAttributes, type ReactNode } from "react";
import {
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type Announcements,
} from "@dnd-kit/core";
import { SortableContext, sortableKeyboardCoordinates, useSortable } from "@dnd-kit/sortable";
import { HugeiconsIcon } from "@hugeicons/react";
import { DragDropVerticalIcon } from "@hugeicons/core-free-icons";

// Rows stay put while dragging; a line marks where the row will land.
const noShift = () => null;
const handleClass =
  "inline-flex size-8 shrink-0 cursor-grab touch-none items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring active:cursor-grabbing disabled:cursor-default disabled:opacity-50";
const rowClass =
  "relative data-dragging:opacity-40 before:pointer-events-none before:absolute before:inset-x-0 before:h-0.5 before:rounded-full before:bg-primary before:opacity-0 data-[drop=before]:before:-top-[3px] data-[drop=before]:before:opacity-100 data-[drop=after]:before:-bottom-[3px] data-[drop=after]:before:opacity-100";

/**
 * A reorderable list: drag a row by its handle (mouse, touch, or keyboard:
 * Space to lift, arrows to move, Space to drop, Esc to cancel). `onMove`
 * gets the old and new index once, on drop.
 */
export function SortableList<T>({
  items,
  id,
  name,
  onMove,
  disabled,
  className,
  row,
  children,
}: {
  items: readonly T[];
  id: (item: T) => string;
  /** Spoken name of a row. */
  name: (item: T) => string;
  onMove: (from: number, to: number) => void;
  disabled?: boolean;
  className?: string;
  /** Attributes of a row's <li>. */
  row?: (item: T) => HTMLAttributes<HTMLLIElement> & Record<`data-${string}`, unknown>;
  /** A row's content; `handle` is its drag handle. */
  children: (item: T, handle: ReactNode) => ReactNode;
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const ids = items.map(id);
  const [active, setActive] = useState<string | null>(null);
  const [over, setOver] = useState<string | null>(null);
  const at = (key: unknown) => ids.indexOf(String(key));
  const label = (key: unknown) => {
    const i = at(key);
    return i < 0 ? "item" : name(items[i]);
  };
  const announcements: Announcements = {
    onDragStart: ({ active }) => `Picked up ${label(active.id)}, position ${at(active.id) + 1} of ${ids.length}.`,
    onDragOver: ({ active, over }) => (over ? `${label(active.id)} will move to position ${at(over.id) + 1} of ${ids.length}.` : ""),
    onDragEnd: ({ active, over }) => (over ? `${label(active.id)} dropped at position ${at(over.id) + 1} of ${ids.length}.` : `${label(active.id)} dropped.`),
    onDragCancel: ({ active }) => `Moving ${label(active.id)} cancelled.`,
  };
  const from = active ? at(active) : -1;
  const to = over ? at(over) : -1;
  const dragged = from >= 0 ? items[from] : undefined;
  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      accessibility={{ announcements, screenReaderInstructions: { draggable: "To reorder, press Space or Enter, move with the arrow keys, and press Space or Enter again to drop. Escape cancels." } }}
      onDragStart={(e) => setActive(String(e.active.id))}
      onDragOver={(e) => setOver(e.over ? String(e.over.id) : null)}
      onDragCancel={() => {
        setActive(null);
        setOver(null);
      }}
      onDragEnd={(e) => {
        setActive(null);
        setOver(null);
        const a = at(e.active.id);
        const b = e.over ? at(e.over.id) : -1;
        if (a >= 0 && b >= 0 && a !== b) onMove(a, b);
      }}
    >
      <SortableContext items={ids} strategy={noShift} disabled={disabled}>
        <ol className={className}>
          {items.map((item, i) => (
            <Row
              key={ids[i]}
              id={ids[i]}
              label={name(item)}
              attrs={row?.(item)}
              disabled={disabled}
              dragging={i === from}
              drop={from >= 0 && i === to && to !== from ? (to > from ? "after" : "before") : undefined}
              render={(handle) => children(item, handle)}
            />
          ))}
        </ol>
      </SortableContext>
      <DragOverlay dropAnimation={null}>
        {dragged !== undefined ? (
          <ol className={className} data-overlay="">
            <li {...row?.(dragged)} className={`${row?.(dragged).className ?? ""} cursor-grabbing rounded-md bg-background px-1 shadow-lg ring-1 ring-border`}>
              {children(
                dragged,
                <span className={handleClass}>
                  <HandleIcon />
                </span>,
              )}
            </li>
          </ol>
        ) : null}
      </DragOverlay>
    </DndContext>
  );
}

function Row({
  id,
  label,
  attrs,
  disabled,
  dragging,
  drop,
  render,
}: {
  id: string;
  label: string;
  attrs?: HTMLAttributes<HTMLLIElement>;
  disabled?: boolean;
  dragging: boolean;
  drop?: "before" | "after";
  render: (handle: ReactNode) => ReactNode;
}) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef } = useSortable({ id });
  const handle = (
    <button
      type="button"
      ref={setActivatorNodeRef}
      className={handleClass}
      aria-label={`Reorder ${label}`}
      disabled={disabled}
      {...attributes}
      {...listeners}
    >
      <HandleIcon />
    </button>
  );
  return (
    <li {...attrs} className={`${attrs?.className ?? ""} ${rowClass}`} ref={setNodeRef} data-drop={drop} data-dragging={dragging || undefined}>
      {render(handle)}
    </li>
  );
}

function HandleIcon() {
  return <HugeiconsIcon icon={DragDropVerticalIcon} size={18} />;
}
