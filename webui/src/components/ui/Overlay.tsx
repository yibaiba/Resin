import type { HTMLAttributes } from "react";
import { createPortal } from "react-dom";

export function Overlay({ children, ...props }: HTMLAttributes<HTMLDivElement>) {
  return createPortal(<div {...props}>{children}</div>, document.body);
}
