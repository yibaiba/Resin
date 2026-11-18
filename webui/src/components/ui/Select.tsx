import {
  Children,
  isValidElement,
  useMemo,
  useState,
  type ChangeEvent,
  type ReactNode,
  type SelectHTMLAttributes,
} from "react";
import { Listbox, type ListboxOption } from "./Listbox";

type SelectProps = Omit<SelectHTMLAttributes<HTMLSelectElement>, "size" | "multiple"> & {
  invalid?: boolean;
};

function optionLabel(node: ReactNode): string {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(optionLabel).join("");
  if (isValidElement<{ children?: ReactNode }>(node)) return optionLabel(node.props.children);
  return "";
}

function optionsFromChildren(children: ReactNode): ListboxOption[] {
  const options: ListboxOption[] = [];
  Children.forEach(children, (child) => {
    if (!isValidElement<{ value?: string | number; disabled?: boolean; children?: ReactNode }>(child)) return;
    if (child.type !== "option") return;
    const label = optionLabel(child.props.children);
    const value = child.props.value === undefined ? label : String(child.props.value);
    options.push({
      value,
      label,
      disabled: Boolean(child.props.disabled),
    });
  });
  return options;
}

export function Select({
  id,
  className,
  invalid,
  children,
  value,
  defaultValue,
  onChange,
  disabled,
  name,
  style,
  "aria-label": ariaLabel,
}: SelectProps) {
  const options = useMemo(() => optionsFromChildren(children), [children]);
  const isControlled = value !== undefined;
  const [uncontrolled, setUncontrolled] = useState(() => String(defaultValue ?? options[0]?.value ?? ""));
  const current = isControlled ? String(value) : uncontrolled;

  const handleChange = (next: string) => {
    if (!isControlled) setUncontrolled(next);
    if (!onChange) return;
    onChange({
      target: { value: next, name: name ?? "" },
      currentTarget: { value: next, name: name ?? "" },
    } as ChangeEvent<HTMLSelectElement>);
  };

  return (
    <Listbox
      id={id}
      className={className}
      value={current}
      onChange={handleChange}
      options={options}
      disabled={disabled}
      invalid={invalid}
      aria-label={ariaLabel}
      style={style}
    />
  );
}
