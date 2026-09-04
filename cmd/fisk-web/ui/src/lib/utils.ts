// The shadcn/ui primitives import cn from here while the AI Elements components import it
// from the cn package. Re-exporting rather than reimplementing keeps one merge behavior
// across both sets.
export { cn } from "cn"
