import { Check, ChevronDown } from "lucide-react";
import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactElement,
} from "react";
import { createPortal } from "react-dom";
import { cn } from "../../lib/cn";
import "./Listbox.css";

export type ListboxOption = {
  value: string;
  label: string;
  disabled?: boolean;
};

type MenuCoords = {
  top?: number;
  bottom?: number;
  left: number;
  width: number;
  maxHeight: number;
  placement: "top" | "bottom";
};

type ListboxCommon = {
  id?: string;
  options: ListboxOption[];
  placeholder?: string;
  disabled?: boolean;
  invalid?: boolean;
  className?: string;
  style?: CSSProperties;
  "aria-label"?: string;
};

type ListboxSingleProps = ListboxCommon & {
  multiple?: false;
  value: string;
  onChange: (value: string) => void;
};

type ListboxMultipleProps = ListboxCommon & {
  multiple: true;
  value: string[];
  onChange: (value: string[]) => void;
};

export type ListboxProps = ListboxSingleProps | ListboxMultipleProps;

const MENU_GAP = 6;
const VIEWPORT_PAD = 8;
const MENU_MAX_HEIGHT = 280;
const TYPEAHEAD_MS = 500;
const CLOSE_MS = 160;

function measureMenu(trigger: HTMLElement): MenuCoords {
  const rect = trigger.getBoundingClientRect();
  const width = Math.max(rect.width, 180);
  const left = Math.min(Math.max(rect.left, VIEWPORT_PAD), Math.max(VIEWPORT_PAD, window.innerWidth - width - VIEWPORT_PAD));
  const spaceBelow = window.innerHeight - rect.bottom - MENU_GAP - VIEWPORT_PAD;
  const spaceAbove = rect.top - MENU_GAP - VIEWPORT_PAD;
  const placement = spaceBelow < 132 && spaceAbove > spaceBelow ? "top" : "bottom";
  const maxHeight = Math.max(96, Math.min(MENU_MAX_HEIGHT, placement === "top" ? spaceAbove : spaceBelow));
  if (placement === "top") {
    return {
      bottom: window.innerHeight - rect.top + MENU_GAP,
      left,
      width,
      maxHeight,
      placement,
    };
  }
  return {
    top: rect.bottom + MENU_GAP,
    left,
    width,
    maxHeight,
    placement,
  };
}

export function Listbox(props: ListboxSingleProps): ReactElement;
export function Listbox(props: ListboxMultipleProps): ReactElement;
export function Listbox(props: ListboxProps): ReactElement {
  const {
    id,
    options,
    placeholder = "",
    disabled = false,
    invalid = false,
    className,
    style,
    "aria-label": ariaLabel,
  } = props;
  const multiple = props.multiple === true;
  const selectedValues = multiple ? props.value : [props.value];
  const selectedSet = new Set(selectedValues);
  const selectedLabels = selectedValues
    .map((item) => options.find((option) => option.value === item)?.label ?? item)
    .filter((label) => label.length > 0);
  const triggerLabel = multiple
    ? selectedLabels.length === 0
      ? placeholder
      : selectedLabels.length <= 2
        ? selectedLabels.join("、")
        : `${selectedLabels.slice(0, 2).join("、")} 等 ${selectedLabels.length} 个`
    : (options.find((item) => item.value === props.value)?.label ?? placeholder);
  const hasValue = multiple ? selectedValues.length > 0 : Boolean(options.find((item) => item.value === props.value));

  const reactId = useId();
  const triggerId = id ?? reactId;
  const listId = `${triggerId}-list`;
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  const searchRef = useRef({ query: "", at: 0 });
  const closeTimerRef = useRef(0);
  const openRafRef = useRef(0);
  const suppressTriggerClickRef = useRef(false);
  const [rendered, setRendered] = useState(false);
  const [open, setOpen] = useState(false);
  const [coords, setCoords] = useState<MenuCoords | null>(null);
  const [activeIndex, setActiveIndex] = useState(0);

  const enabledIndexes = options.flatMap((item, index) => (item.disabled ? [] : [index]));
  const canOpen = !disabled && options.length > 0;

  const closeMenu = () => {
    cancelAnimationFrame(openRafRef.current);
    setOpen(false);
    window.clearTimeout(closeTimerRef.current);
    closeTimerRef.current = window.setTimeout(() => {
      setRendered(false);
      setCoords(null);
    }, CLOSE_MS);
  };

  const openMenu = (index?: number) => {
    if (!canOpen) return;
    window.clearTimeout(closeTimerRef.current);
    const selectedIndex = options.findIndex((item) => selectedSet.has(item.value) && !item.disabled);
    const nextIndex = index ?? (selectedIndex >= 0 ? selectedIndex : (enabledIndexes[0] ?? 0));
    setActiveIndex(nextIndex);
    if (triggerRef.current) {
      setCoords(measureMenu(triggerRef.current));
    }
    setRendered(true);
    cancelAnimationFrame(openRafRef.current);
    openRafRef.current = requestAnimationFrame(() => {
      openRafRef.current = requestAnimationFrame(() => setOpen(true));
    });
  };

  const selectIndex = (index: number) => {
    const option = options[index];
    if (!option || option.disabled) return;
    if (multiple) {
      const next = selectedSet.has(option.value)
        ? selectedValues.filter((item) => item !== option.value)
        : [...selectedValues, option.value];
      props.onChange(next);
      return;
    }
    suppressTriggerClickRef.current = true;
    props.onChange(option.value);
    closeMenu();
    triggerRef.current?.focus();
    window.setTimeout(() => {
      suppressTriggerClickRef.current = false;
    }, 0);
  };

  const toggleMenu = () => {
    if (open) {
      closeMenu();
      return;
    }
    openMenu();
  };

  const onTriggerClick = () => {
    if (suppressTriggerClickRef.current) {
      suppressTriggerClickRef.current = false;
      return;
    }
    toggleMenu();
  };

  const moveActive = (delta: number) => {
    if (enabledIndexes.length === 0) return;
    const currentPos = enabledIndexes.indexOf(activeIndex);
    const start = currentPos === -1 ? (delta > 0 ? -1 : 0) : currentPos;
    const nextPos = (start + delta + enabledIndexes.length) % enabledIndexes.length;
    setActiveIndex(enabledIndexes[nextPos] ?? enabledIndexes[0]);
  };

  const typeahead = (key: string) => {
    const now = Date.now();
    const query = now - searchRef.current.at > TYPEAHEAD_MS ? key : searchRef.current.query + key;
    searchRef.current = { query, at: now };
    const needle = query.toLowerCase();
    const match = options.findIndex((item) => !item.disabled && item.label.toLowerCase().startsWith(needle));
    if (match >= 0) {
      setActiveIndex(match);
      if (!open) {
        openMenu(match);
      }
    }
  };

  useLayoutEffect(() => {
    if (!rendered || !triggerRef.current) return;
    setCoords(measureMenu(triggerRef.current));
  }, [rendered, options.length]);

  useEffect(() => {
    if (!rendered) return;
    const update = () => {
      if (triggerRef.current) {
        setCoords(measureMenu(triggerRef.current));
      }
    };
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [rendered]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (!target) return;
      if (triggerRef.current?.contains(target) || menuRef.current?.contains(target)) return;
      closeMenu();
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  useEffect(() => {
    return () => {
      window.clearTimeout(closeTimerRef.current);
      cancelAnimationFrame(openRafRef.current);
    };
  }, []);

  useLayoutEffect(() => {
    if (!open) return;
    const option = menuRef.current?.querySelector<HTMLElement>(`[data-index="${activeIndex}"]`);
    option?.scrollIntoView({ block: "nearest" });
  }, [activeIndex, open]);

  const onTriggerKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (disabled) return;
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (open) {
          moveActive(1);
        } else {
          openMenu();
        }
        return;
      case "ArrowUp":
        event.preventDefault();
        if (open) {
          moveActive(-1);
        } else {
          openMenu(enabledIndexes[enabledIndexes.length - 1]);
        }
        return;
      case "Home":
        if (!open) return;
        event.preventDefault();
        setActiveIndex(enabledIndexes[0] ?? 0);
        return;
      case "End":
        if (!open) return;
        event.preventDefault();
        setActiveIndex(enabledIndexes[enabledIndexes.length - 1] ?? 0);
        return;
      case "Enter":
      case " ":
        event.preventDefault();
        if (open) {
          selectIndex(activeIndex);
        } else {
          openMenu();
        }
        return;
      case "Escape":
        if (!open && !rendered) return;
        event.preventDefault();
        closeMenu();
        return;
      case "Tab":
        if (open || rendered) {
          closeMenu();
        }
        return;
      default:
        if (event.key.length === 1 && !event.metaKey && !event.ctrlKey && !event.altKey) {
          event.preventDefault();
          typeahead(event.key);
        }
    }
  };

  const activeOption = options[activeIndex];
  const activeId = activeOption ? `${listId}-opt-${activeIndex}` : undefined;

  return (
    <div className={cn("listbox", className)}>
      <button
        ref={triggerRef}
        type="button"
        id={triggerId}
        className={cn("listbox-trigger", invalid && "input-invalid")}
        style={style}
        disabled={disabled}
        role="combobox"
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-autocomplete="none"
        aria-expanded={open}
        aria-controls={rendered ? listId : undefined}
        aria-activedescendant={open ? activeId : undefined}
        aria-invalid={invalid || undefined}
        aria-multiselectable={multiple || undefined}
        onClick={onTriggerClick}
        onKeyDown={onTriggerKeyDown}
      >
        <span className={cn("listbox-value", !hasValue && "is-placeholder")}>{triggerLabel}</span>
        <ChevronDown className="listbox-chevron" size={16} aria-hidden="true" />
      </button>
      {rendered && coords
        ? createPortal(
            <ul
              ref={menuRef}
              id={listId}
              className="listbox-menu"
              role="listbox"
              data-open={open ? "true" : "false"}
              data-placement={coords.placement}
              aria-labelledby={triggerId}
              aria-multiselectable={multiple || undefined}
              style={{
                position: "fixed",
                top: coords.top,
                bottom: coords.bottom,
                left: coords.left,
                width: coords.width,
                maxHeight: coords.maxHeight,
              }}
            >
              {options.length === 0 ? (
                <li className="listbox-empty">{placeholder}</li>
              ) : (
                options.map((item, index) => {
                  const isSelected = selectedSet.has(item.value);
                  return (
                    <li
                      key={item.value === "" ? `__empty-${index}` : item.value}
                      id={`${listId}-opt-${index}`}
                      className="listbox-option"
                      role="option"
                      data-index={index}
                      data-active={index === activeIndex}
                      aria-selected={isSelected}
                      aria-disabled={item.disabled || undefined}
                      onPointerDown={(event) => {
                        if (event.button !== 0 || item.disabled) return;
                        event.preventDefault();
                        selectIndex(index);
                      }}
                      onMouseEnter={() => {
                        if (!item.disabled) setActiveIndex(index);
                      }}
                    >
                      <span className="listbox-option-label">{item.label}</span>
                      {isSelected ? <Check className="listbox-option-check" size={14} aria-hidden="true" /> : null}
                    </li>
                  );
                })
              )}
            </ul>,
            document.body,
          )
        : null}
    </div>
  );
}
